package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * The alarm dispatcher.
 *
 * Two streams feed it and they are the two things KubeMG is uniquely placed to
 * see. Cluster Events — OOMKilled, FailedScheduling, anything of type Warning —
 * are read down the agent tunnel, which matters because the fleet KubeMG is built
 * for is exactly the fleet whose clusters cannot be scraped from a central
 * Prometheus. And KubeMG's own audit records, which no cluster-side alerting can
 * ever see: a refused kubectl never reached the API server, so there is no event
 * for it anywhere but here.
 *
 * Three properties are load-bearing:
 *
 *   - Observe never blocks and never fails a caller. It is called from the audit
 *     path, which is beside a proxied request. A webhook endpoint that has gone
 *     slow must not become a slow kubectl, so a full queue drops the signal and
 *     says so — the same trade StoreAuditor already makes, for the same reason.
 *   - Delivery is deduplicated by fingerprint with a per-rule cool-off. A crash
 *     loop re-emits its Event every few seconds; a channel that pages every few
 *     seconds gets muted by its recipient, which is indistinguishable from having
 *     no alarms at all.
 *   - The rule set is cached and refreshed on a timer rather than read per signal.
 *     A rule set is a handful of rows edited a few times a year and matched
 *     thousands of times an hour.
 */

// Dispatcher delivery limits.
const (
	// alarmQueueSize is how many pending signals are held. It is smaller than the
	// audit queue on purpose: an alarm backlog of thousands is not a backlog, it is
	// a rule that matches everything, and delivering it late helps nobody.
	alarmQueueSize = 512
	// alarmDeliveryTimeout bounds one HTTP attempt.
	alarmDeliveryTimeout = 10 * time.Second
	// alarmAttempts is how many times one signal is offered to a channel. A page
	// is worth a retry; a third attempt against an endpoint that has failed twice
	// in a row is not, and the health field is what surfaces the failure.
	alarmAttempts = 2
	// alarmRetryDelay separates those attempts.
	alarmRetryDelay = 2 * time.Second
	// defaultCooloff suppresses a repeat of the same fingerprint. Five minutes is
	// long enough to collapse a crash loop and short enough that a recurrence after
	// an operator thought they had fixed it still arrives.
	defaultCooloff = 5 * time.Minute
	// alarmRuleRefresh is how often the rule and channel set is re-read.
	alarmRuleRefresh = 30 * time.Second
	// cooloffCapacity bounds the dedup table. Past it the oldest half is dropped —
	// an unbounded map keyed by cluster/namespace/object is a memory leak with a
	// fleet-sized denominator.
	cooloffCapacity = 4096
)

// SignalSource says which stream a signal came from, and therefore which half of
// a rule can match it.
const (
	SourceClusterEvent = db.TriggerClusterEvent
	SourceAudit        = db.TriggerAudit
)

// Signal is one thing that happened, normalised so that a rule can be matched
// against it without knowing which stream produced it.
//
// It is also, verbatim, the body a webhook channel receives. That is deliberate:
// a SIEM wants the fact rather than a vendor's alert envelope around it, and
// having one shape means the payload a SIEM parses is the same one the matcher
// reasoned about.
type Signal struct {
	Source string    `json:"source"`
	At     time.Time `json:"at"`

	ClusterID uint   `json:"cluster_id"`
	Cluster   string `json:"cluster"`
	Namespace string `json:"namespace,omitempty"`

	// Reason is the Event reason for a cluster event, and the verb for an audit
	// record. It is the field a rule matches on most often, which is why the two
	// streams share it rather than each having their own.
	Reason string `json:"reason,omitempty"`
	// Type is the Event type — Warning or Normal. Empty on an audit signal.
	Type string `json:"type,omitempty"`
	// Object is what the signal is about: "pod/api-7d9f", or the audit record's
	// resource.
	Object string `json:"object,omitempty"`
	// Message is the human-readable line. For a cluster event it is the event's
	// own message, which is the part that says *why*.
	Message string `json:"message"`
	// Count is how many times the cluster has seen this event. A first occurrence
	// and the four hundredth read very differently on a pager.
	Count int32 `json:"count,omitempty"`

	// Audit-only fields.
	Verb     string `json:"verb,omitempty"`
	Username string `json:"username,omitempty"`
	UserID   uint   `json:"user_id,omitempty"`
	Status   int    `json:"status,omitempty"`
	Path     string `json:"path,omitempty"`
	Denied   bool   `json:"denied,omitempty"`
	Error    string `json:"error,omitempty"`

	// Fingerprint identifies the *thing*, not the occurrence, and is what the
	// cool-off is keyed by. It is set by the producer because only the producer
	// knows what "the same problem again" means for its stream.
	Fingerprint string `json:"fingerprint,omitempty"`
}

// AlarmStore is the persistence the dispatcher needs.
type AlarmStore interface {
	ListAlarmRules(ctx context.Context) ([]db.AlarmRule, error)
	ListAlarmChannels(ctx context.Context) ([]db.AlarmChannel, error)
	RecordAlarmDelivery(ctx context.Context, id uint, status, message string) error
	RecordAlarmFired(ctx context.Context, id uint, at time.Time) error
}

// Delivery health values recorded on a channel.
const (
	DeliveryOK     = "ok"
	DeliveryFailed = "failed"
)

// Dispatcher matches signals against rules and delivers them.
type Dispatcher struct {
	store  AlarmStore
	client *http.Client
	logger *slog.Logger
	// origin is KubeMG's own public address, used as the generator URL an alert
	// links back to. A page with no link to the thing it is about costs the
	// recipient the first five minutes of every incident.
	origin string

	queue   chan Signal
	dropped int64

	mu       sync.RWMutex
	rules    []db.AlarmRule
	channels map[uint]db.AlarmChannel

	cooloffMu sync.Mutex
	cooloff   map[string]time.Time

	done chan struct{}
}

// DispatcherOptions wires the dispatcher.
type DispatcherOptions struct {
	Store  AlarmStore
	Logger *slog.Logger
	// Origin is the console's own address, linked from delivered alarms.
	Origin string
	// Client overrides the HTTP client, which is what the tests do. It exists as an
	// option rather than being built here because "can this reach the endpoint" is
	// the one thing about a channel worth being able to substitute.
	Client *http.Client
}

// NewDispatcher builds a dispatcher. Run must be started for anything to be
// delivered; until then Observe accepts and discards, which is what keeps a
// partially-wired server from blocking on a channel nobody is draining.
func NewDispatcher(opts DispatcherOptions) *Dispatcher {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	client := opts.Client
	if client == nil {
		client = &http.Client{Timeout: alarmDeliveryTimeout}
	}
	return &Dispatcher{
		store:    opts.Store,
		client:   client,
		logger:   logger,
		origin:   strings.TrimRight(opts.Origin, "/"),
		queue:    make(chan Signal, alarmQueueSize),
		channels: map[uint]db.AlarmChannel{},
		cooloff:  map[string]time.Time{},
		done:     make(chan struct{}),
	}
}

// Observe offers a signal for delivery. It never blocks and never returns an
// error: the callers are the audit path and a background poller, and neither has
// anything useful to do with a delivery failure.
func (d *Dispatcher) Observe(signal Signal) {
	if d == nil {
		return
	}
	if signal.At.IsZero() {
		signal.At = time.Now().UTC()
	}
	// Nothing configured means nothing to match. Checking here keeps a fleet with
	// no alarms from paying a channel send per proxied call.
	if !d.armed() {
		return
	}
	select {
	case d.queue <- signal:
	default:
		d.dropped++
		if d.dropped == 1 || d.dropped%100 == 0 {
			d.logger.Error("alarm queue is full, dropping signals",
				slog.Int64("dropped_total", d.dropped))
		}
	}
}

// armed reports whether any enabled rule exists. A dispatcher whose rules have
// not been loaded yet is not armed, which is correct: it has nothing to match
// against and would only fill its queue.
func (d *Dispatcher) armed() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	for _, rule := range d.rules {
		if rule.Enabled {
			return true
		}
	}
	return false
}

// Rules returns the enabled rules for a given trigger. The cluster-event poller
// reads this to decide which clusters are worth polling at all — with no
// cluster-event rule configured, nothing polls anything.
func (d *Dispatcher) Rules(trigger string) []db.AlarmRule {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := []db.AlarmRule{}
	for _, rule := range d.rules {
		if rule.Enabled && rule.Trigger == trigger {
			out = append(out, rule)
		}
	}
	return out
}

// Run drains the queue and keeps the rule set fresh until the context is
// cancelled. It blocks and is meant to be started on its own goroutine.
func (d *Dispatcher) Run(ctx context.Context) {
	defer close(d.done)

	ticker := time.NewTicker(alarmRuleRefresh)
	defer ticker.Stop()

	d.Refresh(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.Refresh(ctx)
		case signal := <-d.queue:
			d.deliver(ctx, signal)
		}
	}
}

// Wait blocks until Run has returned.
func (d *Dispatcher) Wait() { <-d.done }

// Refresh re-reads the rules and channels. A read failure keeps the previous set
// rather than disarming: alarms going quiet because a database blipped is the
// worst possible response to a database blip.
func (d *Dispatcher) Refresh(ctx context.Context) {
	rules, err := d.store.ListAlarmRules(ctx)
	if err != nil {
		if ctx.Err() == nil {
			d.logger.Warn("could not refresh alarm rules", slog.String("error", err.Error()))
		}
		return
	}
	channels, err := d.store.ListAlarmChannels(ctx)
	if err != nil {
		if ctx.Err() == nil {
			d.logger.Warn("could not refresh alarm channels", slog.String("error", err.Error()))
		}
		return
	}

	byID := make(map[uint]db.AlarmChannel, len(channels))
	for _, channel := range channels {
		byID[channel.ID] = channel
	}

	d.mu.Lock()
	d.rules = rules
	d.channels = byID
	d.mu.Unlock()
}

// deliver matches one signal and sends it wherever it goes.
func (d *Dispatcher) deliver(ctx context.Context, signal Signal) {
	d.mu.RLock()
	rules := make([]db.AlarmRule, len(d.rules))
	copy(rules, d.rules)
	channels := d.channels
	d.mu.RUnlock()

	for _, rule := range rules {
		if !rule.Enabled || !Matches(rule, signal) {
			continue
		}
		channel, ok := channels[rule.ChannelID]
		if !ok || !channel.Enabled {
			continue
		}
		// The cool-off is per rule *and* per fingerprint: two rules watching the
		// same event for different audiences must both fire, and the same rule
		// firing on the same pod every ten seconds must not.
		if !d.claim(rule, signal) {
			continue
		}
		d.send(ctx, rule, channel, signal)
	}
}

// claim reserves this rule/fingerprint pair for its cool-off window, reporting
// false when a recent delivery already covers it.
func (d *Dispatcher) claim(rule db.AlarmRule, signal Signal) bool {
	window := time.Duration(rule.CooloffSeconds) * time.Second
	if window <= 0 {
		window = defaultCooloff
	}
	key := fmt.Sprintf("%d|%s", rule.ID, fingerprintOf(signal))
	now := time.Now()

	d.cooloffMu.Lock()
	defer d.cooloffMu.Unlock()

	if until, seen := d.cooloff[key]; seen && now.Before(until) {
		return false
	}
	if len(d.cooloff) >= cooloffCapacity {
		// Drop everything already expired first; if that is not enough, the table
		// is genuinely full of live entries and dropping some means a duplicate
		// page rather than a missed one — which is the right way round.
		for k, until := range d.cooloff {
			if now.After(until) {
				delete(d.cooloff, k)
			}
		}
		for k := range d.cooloff {
			if len(d.cooloff) < cooloffCapacity {
				break
			}
			delete(d.cooloff, k)
		}
	}
	d.cooloff[key] = now.Add(window)
	return true
}

// fingerprintOf falls back to the signal's own identifying fields when the
// producer did not set one, so a missing fingerprint degrades to "the same object
// and reason" rather than to no deduplication at all.
func fingerprintOf(signal Signal) string {
	if signal.Fingerprint != "" {
		return signal.Fingerprint
	}
	return strings.Join([]string{
		signal.Source,
		strconv.FormatUint(uint64(signal.ClusterID), 10),
		signal.Namespace,
		signal.Object,
		signal.Reason,
	}, "/")
}

// send renders the payload and posts it, recording the verdict on the channel.
func (d *Dispatcher) send(
	ctx context.Context, rule db.AlarmRule, channel db.AlarmChannel, signal Signal,
) {
	body, contentType, err := d.render(channel, rule, signal)
	if err != nil {
		d.logger.Error("could not render an alarm payload",
			slog.String("channel", channel.Name),
			slog.String("error", err.Error()))
		d.recordDelivery(ctx, channel.ID, DeliveryFailed, err.Error())
		return
	}

	var lastErr error
	for attempt := 1; attempt <= alarmAttempts; attempt++ {
		lastErr = d.post(ctx, channel, body, contentType)
		if lastErr == nil {
			break
		}
		if attempt < alarmAttempts {
			select {
			case <-ctx.Done():
				return
			case <-time.After(alarmRetryDelay):
			}
		}
	}

	if lastErr != nil {
		d.logger.Warn("alarm delivery failed",
			slog.String("channel", channel.Name),
			slog.String("rule", rule.Name),
			slog.String("error", lastErr.Error()))
		d.recordDelivery(ctx, channel.ID, DeliveryFailed, lastErr.Error())
		return
	}

	d.recordDelivery(ctx, channel.ID, DeliveryOK, "")
	if err := d.store.RecordAlarmFired(ctx, rule.ID, time.Now().UTC()); err != nil && ctx.Err() == nil {
		d.logger.Warn("could not stamp a fired alarm rule", slog.String("error", err.Error()))
	}
	d.logger.Info("alarm delivered",
		slog.String("rule", rule.Name),
		slog.String("channel", channel.Name),
		slog.String("kind", channel.Kind),
		slog.String("reason", signal.Reason),
		slog.String("cluster", signal.Cluster))
}

func (d *Dispatcher) recordDelivery(ctx context.Context, id uint, status, message string) {
	// A detached context: recording that a delivery failed is worth doing even
	// when the thing that cancelled the delivery was the server shutting down.
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := d.store.RecordAlarmDelivery(writeCtx, id, status, truncate(message, 500)); err != nil {
		d.logger.Warn("could not record alarm delivery health", slog.String("error", err.Error()))
	}
}

// post performs one delivery attempt.
func (d *Dispatcher) post(
	ctx context.Context, channel db.AlarmChannel, body []byte, contentType string,
) error {
	sendCtx, cancel := context.WithTimeout(ctx, alarmDeliveryTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(sendCtx, http.MethodPost, channel.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("User-Agent", "kubemg-alarms")

	for name, value := range parseHeaders(channel.Headers) {
		req.Header.Set(name, value)
	}
	switch channel.AuthMode {
	case db.AuthBearer:
		if channel.Secret != "" {
			req.Header.Set("Authorization", "Bearer "+channel.Secret)
		}
	case db.AuthBasic:
		if channel.Username != "" || channel.Secret != "" {
			req.SetBasicAuth(channel.Username, channel.Secret)
		}
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// Read a bounded amount of the body so a rejection can say *why*. "202
	// Accepted" needs no explanation; a 400 from PagerDuty naming the field it
	// disliked is the difference between a fixable channel and a mystery.
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	return fmt.Errorf("%s answered %d: %s",
		channel.Kind, resp.StatusCode, strings.TrimSpace(string(snippet)))
}

// parseHeaders reads the stored extra headers. A malformed value is ignored
// rather than failing the delivery: a page held back over a typo in an optional
// header field is the wrong trade.
func parseHeaders(raw string) map[string]string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out map[string]string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	// Never let a stored header set the credential or the body type — those are
	// decided by the channel's own fields, and a header map that could override
	// them would be a second, unvalidated way to configure authentication.
	delete(out, "Authorization")
	delete(out, "Content-Type")
	return out
}

// Test delivers a synthetic alarm to one channel and reports what the endpoint
// said, synchronously.
//
// Synchronous is the point: an operator pressing Test is waiting for the answer,
// and a queued delivery whose verdict lands in a log five seconds later is not an
// answer. It bypasses the rules and the cool-off for the same reason — the
// question is about the endpoint, not about the rule set.
func (d *Dispatcher) Test(ctx context.Context, channel db.AlarmChannel, by string) error {
	rule := db.AlarmRule{
		Name:     "channel test",
		Severity: db.SeverityInfo,
	}
	signal := Signal{
		Source:      SourceAudit,
		At:          time.Now().UTC(),
		Reason:      "KubeMGChannelTest",
		Object:      "kubemg/alarm-channel",
		Message:     "Test alarm from KubeMG, sent by " + orDash(by) + ". Nothing is wrong.",
		Username:    by,
		Fingerprint: fmt.Sprintf("test/%d/%d", channel.ID, time.Now().UnixNano()),
	}

	body, contentType, err := d.render(channel, rule, signal)
	if err != nil {
		d.recordDelivery(ctx, channel.ID, DeliveryFailed, err.Error())
		return err
	}
	if err := d.post(ctx, channel, body, contentType); err != nil {
		d.recordDelivery(ctx, channel.ID, DeliveryFailed, err.Error())
		return err
	}
	d.recordDelivery(ctx, channel.ID, DeliveryOK, "test alarm accepted")
	return nil
}

// Matches reports whether a rule covers a signal. It is exported because it is
// the whole semantics of a rule and deserves to be testable on its own.
func Matches(rule db.AlarmRule, signal Signal) bool {
	if rule.Trigger != signal.Source {
		return false
	}
	if rule.ClusterID != 0 && rule.ClusterID != signal.ClusterID {
		return false
	}
	if namespaces := splitList(rule.Namespaces); len(namespaces) > 0 {
		if !containsFold(namespaces, signal.Namespace) {
			return false
		}
	}

	switch rule.Trigger {
	case db.TriggerClusterEvent:
		if rule.EventType != "" && !strings.EqualFold(rule.EventType, signal.Type) {
			return false
		}
		if reasons := splitList(rule.EventReasons); len(reasons) > 0 {
			if !containsFold(reasons, signal.Reason) {
				return false
			}
		}
		return true

	case db.TriggerAudit:
		if rule.DeniedOnly && !signal.Denied {
			return false
		}
		if rule.MinStatus != 0 && signal.Status < rule.MinStatus {
			return false
		}
		if verbs := splitList(rule.Verbs); len(verbs) > 0 {
			if !containsFold(verbs, signal.Verb) {
				return false
			}
		}
		return true

	default:
		// An unknown trigger matches nothing. A rule this build cannot evaluate
		// must not fall through to "everything" — that would page on every signal
		// after a downgrade.
		return false
	}
}

// splitList reads one of the comma-separated matcher fields.
func splitList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	out := []string{}
	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func containsFold(list []string, value string) bool {
	for _, entry := range list {
		if strings.EqualFold(entry, value) {
			return true
		}
	}
	return false
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// ValidateChannelURL checks a destination address.
//
// It is deliberately not an SSRF guard. An alarm channel is an admin-configured
// outbound integration, and the addresses that matter are internal by nature — an
// in-fleet Alertmanager, a self-hosted ServiceNow, a SIEM collector on a private
// subnet — so refusing private ranges would refuse the common case. What is
// checked is that the URL is one that can be posted to at all, and that no
// credential is smuggled in the userinfo, where it would end up in every proxy
// log between here and the endpoint.
func ValidateChannelURL(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("webhook URL is not a valid address")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("webhook URL must be an http:// or https:// address")
	}
	if parsed.Host == "" {
		return fmt.Errorf("webhook URL must include a host")
	}
	if parsed.User != nil {
		return fmt.Errorf("put the credential in the channel's own token field, not in the URL")
	}
	return nil
}
