package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/auditpolicy"
	"github.com/kubemg/kubemg/backend/pkg/db"
	"github.com/kubemg/kubemg/backend/pkg/observability"
)

/*
 * Configuring alarms.
 *
 * The whole surface is administrative, and not only because it changes fleet-wide
 * behaviour: a channel is an outbound destination for audit records, so anyone who
 * can add one can forward the trail off the platform. That makes it a
 * data-egress control, which is a higher bar than "changes a setting".
 *
 * The credential never travels back out. A channel is read back with
 * `has_secret` and nothing else, the same rule a cluster's service account token
 * and an observability datasource's credential already follow — and omitting the
 * secret on an edit keeps the stored one, so changing a URL does not mean
 * re-typing a routing key nobody can read.
 */

// Field bounds. A name and a URL both end up in a delivered payload, so they are
// bounded here rather than by the column width.
const (
	maxAlarmNameLength = 120
	maxAlarmURLLength  = 2048
	maxAlarmCooloff    = 24 * 60 * 60
)

type alarmChannelResponse struct {
	ID       uint   `json:"id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	URL      string `json:"url"`
	AuthMode string `json:"auth_mode"`
	Username string `json:"username,omitempty"`
	Headers  string `json:"headers,omitempty"`
	Enabled  bool   `json:"enabled"`

	// HasSecret stands in for the credential. It is what makes an edit form
	// honest: an empty token box means "keep what is stored", and without this
	// flag there is no way to tell that from "there is nothing stored".
	HasSecret bool `json:"has_secret"`

	LastStatus    string     `json:"last_status,omitempty"`
	LastMessage   string     `json:"last_message,omitempty"`
	LastAttemptAt *time.Time `json:"last_attempt_at,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func toAlarmChannelResponse(channel db.AlarmChannel) alarmChannelResponse {
	return alarmChannelResponse{
		ID:            channel.ID,
		Name:          channel.Name,
		Kind:          channel.Kind,
		URL:           channel.URL,
		AuthMode:      channel.AuthMode,
		Username:      channel.Username,
		Headers:       channel.Headers,
		Enabled:       channel.Enabled,
		HasSecret:     channel.HasSecret(),
		LastStatus:    channel.LastStatus,
		LastMessage:   channel.LastMessage,
		LastAttemptAt: channel.LastAttemptAt,
		CreatedAt:     channel.CreatedAt,
		UpdatedAt:     channel.UpdatedAt,
	}
}

type alarmChannelRequest struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	URL      string `json:"url"`
	AuthMode string `json:"auth_mode"`
	Username string `json:"username"`
	// Secret is write-only. Omitted on an update, the stored one is kept.
	Secret  string `json:"secret"`
	Headers string `json:"headers"`
	// Enabled defaults to true on create — a channel somebody just configured is
	// one they want working — and is honoured as sent on an update.
	Enabled *bool `json:"enabled"`
}

// listAlarmChannels returns every configured destination (admin only).
func (s *server) listAlarmChannels(c *gin.Context) {
	channels, err := s.store.ListAlarmChannels(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not read the alarm channels"})
		return
	}
	out := make([]alarmChannelResponse, 0, len(channels))
	for _, channel := range channels {
		out = append(out, toAlarmChannelResponse(channel))
	}
	c.JSON(http.StatusOK, gin.H{
		"channels": out,
		"kinds":    db.AlarmChannelKinds,
	})
}

// createAlarmChannel stores a new destination (admin only).
func (s *server) createAlarmChannel(c *gin.Context) {
	var req alarmChannelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	channel, ok := s.channelFrom(c, req, nil)
	if !ok {
		return
	}
	if err := s.store.CreateAlarmChannel(c.Request.Context(), channel); err != nil {
		if errors.Is(err, db.ErrConflict) {
			c.JSON(http.StatusConflict, gin.H{"error": "a channel with that name already exists"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not save the alarm channel"})
		return
	}

	s.refreshAlarms(c)
	c.JSON(http.StatusCreated, toAlarmChannelResponse(*channel))
}

// updateAlarmChannel replaces a destination's configuration (admin only).
func (s *server) updateAlarmChannel(c *gin.Context) {
	id, ok := parseIDParam(c, "id", "alarm")
	if !ok {
		return
	}
	existing, ok := s.loadChannel(c, id)
	if !ok {
		return
	}

	var req alarmChannelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	channel, ok := s.channelFrom(c, req, existing)
	if !ok {
		return
	}
	channel.ID = existing.ID

	if err := s.store.UpdateAlarmChannel(c.Request.Context(), channel); err != nil {
		switch {
		case errors.Is(err, db.ErrConflict):
			c.JSON(http.StatusConflict, gin.H{"error": "a channel with that name already exists"})
		case errors.Is(err, db.ErrNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": "channel not found"})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "could not save the alarm channel"})
		}
		return
	}

	updated, err := s.store.AlarmChannelByID(c.Request.Context(), channel.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not read the alarm channel back"})
		return
	}
	s.refreshAlarms(c)
	c.JSON(http.StatusOK, toAlarmChannelResponse(*updated))
}

// deleteAlarmChannel removes a destination and the rules pointing at it.
func (s *server) deleteAlarmChannel(c *gin.Context) {
	id, ok := parseIDParam(c, "id", "alarm")
	if !ok {
		return
	}
	if err := s.store.DeleteAlarmChannel(c.Request.Context(), id); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "channel not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not delete the alarm channel"})
		return
	}
	s.refreshAlarms(c)
	c.Status(http.StatusNoContent)
}

// testAlarmChannel delivers a synthetic alarm to a stored channel.
//
// It bypasses the rule matcher and the cool-off deliberately: an operator
// pressing Test is asking whether the endpoint accepts KubeMG's payload, and
// answering "suppressed, you tested it four minutes ago" would make the button
// useless exactly when it is being used to iterate on a broken channel.
func (s *server) testAlarmChannel(c *gin.Context) {
	if s.alarms == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "the alarm dispatcher is not running on this server",
		})
		return
	}

	id, ok := parseIDParam(c, "id", "alarm")
	if !ok {
		return
	}
	channel, ok := s.loadChannel(c, id)
	if !ok {
		return
	}

	caller, ok := s.currentUser(c)
	if !ok {
		return
	}

	if err := s.alarms.Test(c.Request.Context(), *channel, caller.Username); err != nil {
		// A failed test is a successful request: the operator asked whether it
		// works and the answer is no, with the endpoint's own words.
		c.JSON(http.StatusOK, gin.H{"ok": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "message": "the endpoint accepted a test alarm"})
}

// channelFrom validates a request into a channel row. existing is nil on create;
// on update it supplies the stored secret when the request omits one.
func (s *server) channelFrom(
	c *gin.Context, req alarmChannelRequest, existing *db.AlarmChannel,
) (*db.AlarmChannel, bool) {
	name := strings.TrimSpace(req.Name)
	if name == "" || len(name) > maxAlarmNameLength {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "a channel needs a name of at most 120 characters",
		})
		return nil, false
	}

	kind := strings.ToLower(strings.TrimSpace(req.Kind))
	if !slices.Contains(db.AlarmChannelKinds, kind) {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "kind must be one of " + strings.Join(db.AlarmChannelKinds, ", "),
		})
		return nil, false
	}

	rawURL := strings.TrimSpace(req.URL)
	if len(rawURL) > maxAlarmURLLength {
		c.JSON(http.StatusBadRequest, gin.H{"error": "webhook URL is too long"})
		return nil, false
	}
	if err := observability.ValidateChannelURL(rawURL); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return nil, false
	}

	authMode := strings.ToLower(strings.TrimSpace(req.AuthMode))
	if authMode == "" {
		authMode = defaultAuthMode(kind)
	}
	if !slices.Contains(db.AlarmAuthModes, authMode) {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "auth_mode must be none, bearer, basic or key",
		})
		return nil, false
	}

	if headers := strings.TrimSpace(req.Headers); headers != "" && !validHeaderJSON(headers) {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": `headers must be a JSON object of string values, for example {"X-Tenant":"prod"}`,
		})
		return nil, false
	}

	secret := strings.TrimSpace(req.Secret)
	if secret == "" && existing != nil {
		secret = existing.Secret
	}
	// PagerDuty's routing key is the payload; without it every delivery is
	// rejected with a 400 the operator would have to go and read. Say so here.
	if kind == db.ChannelPagerDuty && secret == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "a PagerDuty channel needs its Events API routing key",
		})
		return nil, false
	}
	if authMode == db.AuthBasic && strings.TrimSpace(req.Username) == "" && existing == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "basic auth needs a username"})
		return nil, false
	}

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	} else if existing != nil {
		enabled = existing.Enabled
	}

	username := strings.TrimSpace(req.Username)
	if username == "" && existing != nil {
		username = existing.Username
	}

	return &db.AlarmChannel{
		Name:     name,
		Kind:     kind,
		URL:      rawURL,
		AuthMode: authMode,
		Username: username,
		Secret:   secret,
		Headers:  strings.TrimSpace(req.Headers),
		Enabled:  enabled,
	}, true
}

// defaultAuthMode is what each destination usually wants, so the common case
// needs no choice. A Slack webhook's secret is its URL; PagerDuty's key rides in
// the body; a SIEM collector almost always wants a bearer token.
func defaultAuthMode(kind string) string {
	switch kind {
	case db.ChannelPagerDuty:
		return db.AuthKey
	case db.ChannelSlack:
		return db.AuthNone
	case db.ChannelServiceNow:
		return db.AuthBasic
	default:
		return db.AuthNone
	}
}

// validHeaderJSON checks the extra-header field is a flat string map. It is
// validated on the way in as well as ignored-if-broken on the way out, so an
// operator learns about a typo here rather than from an alarm that quietly went
// without its tenant header.
func validHeaderJSON(raw string) bool {
	var out map[string]string
	return json.Unmarshal([]byte(raw), &out) == nil
}

func (s *server) loadChannel(c *gin.Context, id uint) (*db.AlarmChannel, bool) {
	channel, err := s.store.AlarmChannelByID(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "channel not found"})
			return nil, false
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not read the alarm channel"})
		return nil, false
	}
	return channel, true
}

/* ----------------------------------------------------------------- rules --- */

type alarmRuleRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	ChannelID   uint   `json:"channel_id"`
	Enabled     *bool  `json:"enabled"`

	Trigger    string   `json:"trigger"`
	ClusterID  uint     `json:"cluster_id"`
	Namespaces []string `json:"namespaces"`

	EventReasons []string `json:"event_reasons"`
	EventType    string   `json:"event_type"`

	Verbs      []string `json:"verbs"`
	DeniedOnly bool     `json:"denied_only"`
	MinStatus  int      `json:"min_status"`

	Severity       string `json:"severity"`
	CooloffSeconds int    `json:"cooloff_seconds"`
}

// listAlarmRules returns the rule set alongside the vocabularies the editor needs,
// so the form's options and the server's validation cannot drift apart.
func (s *server) listAlarmRules(c *gin.Context) {
	rules, err := s.store.ListAlarmRules(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not read the alarm rules"})
		return
	}
	if rules == nil {
		rules = []db.AlarmRule{}
	}
	c.JSON(http.StatusOK, gin.H{
		"rules":      rules,
		"triggers":   db.AlarmTriggers,
		"severities": db.AlarmSeverities,
		// The reasons worth suggesting. It is a suggestion list rather than an
		// allowed set: a reason comes from whichever controller wrote the event, so
		// refusing an unrecognised one would make every operator's own CRD
		// unalarmable.
		"suggested_reasons": suggestedEventReasons,
		// Whether the cluster-event half of this feature can work at all on this
		// server. Without a proxy there are no tunnels to read events down, and a
		// rule that can never fire should say so rather than look configured.
		"cluster_events_available": s.proxy != nil && s.alarms != nil,
		"dispatcher_running":       s.alarms != nil,
	})
}

// suggestedEventReasons are the Event reasons an operator reaches for first. They
// are the ones that mean "this workload is not running and will not start on its
// own", which is the class of thing worth waking somebody for.
var suggestedEventReasons = []string{
	"OOMKilled",
	"FailedScheduling",
	"BackOff",
	"CrashLoopBackOff",
	"Failed",
	"FailedMount",
	"FailedCreatePodSandBox",
	"Evicted",
	"NodeNotReady",
	"Unhealthy",
	"FailedAttachVolume",
	"ImagePullBackOff",
	"ErrImagePull",
}

func (s *server) createAlarmRule(c *gin.Context) {
	var req alarmRuleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	rule, ok := s.ruleFrom(c, req)
	if !ok {
		return
	}
	if err := s.store.CreateAlarmRule(c.Request.Context(), rule); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not save the alarm rule"})
		return
	}
	s.refreshAlarms(c)
	c.JSON(http.StatusCreated, rule)
}

func (s *server) updateAlarmRule(c *gin.Context) {
	id, ok := parseIDParam(c, "id", "alarm")
	if !ok {
		return
	}
	var req alarmRuleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	rule, ok := s.ruleFrom(c, req)
	if !ok {
		return
	}
	rule.ID = id

	if err := s.store.UpdateAlarmRule(c.Request.Context(), rule); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "rule not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not save the alarm rule"})
		return
	}

	updated, err := s.store.AlarmRuleByID(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not read the alarm rule back"})
		return
	}
	s.refreshAlarms(c)
	c.JSON(http.StatusOK, updated)
}

func (s *server) deleteAlarmRule(c *gin.Context) {
	id, ok := parseIDParam(c, "id", "alarm")
	if !ok {
		return
	}
	if err := s.store.DeleteAlarmRule(c.Request.Context(), id); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "rule not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not delete the alarm rule"})
		return
	}
	s.refreshAlarms(c)
	c.Status(http.StatusNoContent)
}

// ruleFrom validates a request into a rule row.
func (s *server) ruleFrom(c *gin.Context, req alarmRuleRequest) (*db.AlarmRule, bool) {
	name := strings.TrimSpace(req.Name)
	if name == "" || len(name) > maxAlarmNameLength {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "a rule needs a name of at most 120 characters",
		})
		return nil, false
	}

	trigger := strings.ToLower(strings.TrimSpace(req.Trigger))
	if !slices.Contains(db.AlarmTriggers, trigger) {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "trigger must be one of " + strings.Join(db.AlarmTriggers, ", "),
		})
		return nil, false
	}

	// A rule with no channel is a condition that matches and goes nowhere, which
	// is worse than no rule: it looks like coverage.
	if req.ChannelID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "a rule needs a channel to deliver to"})
		return nil, false
	}
	if _, err := s.store.AlarmChannelByID(c.Request.Context(), req.ChannelID); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "that channel does not exist"})
			return nil, false
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not read the alarm channel"})
		return nil, false
	}

	if req.ClusterID != 0 {
		if _, err := s.store.ClusterByID(c.Request.Context(), req.ClusterID); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "that cluster does not exist"})
			return nil, false
		}
	}

	severity := strings.ToLower(strings.TrimSpace(req.Severity))
	if severity == "" {
		severity = db.SeverityWarning
	}
	if !slices.Contains(db.AlarmSeverities, severity) {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "severity must be one of " + strings.Join(db.AlarmSeverities, ", "),
		})
		return nil, false
	}

	eventType := strings.TrimSpace(req.EventType)
	if eventType != "" && !slices.Contains([]string{"Normal", "Warning"}, eventType) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "event_type must be Normal or Warning"})
		return nil, false
	}

	if req.MinStatus != 0 && (req.MinStatus < 100 || req.MinStatus > 599) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "min_status must be an HTTP status code"})
		return nil, false
	}
	if req.CooloffSeconds < 0 || req.CooloffSeconds > maxAlarmCooloff {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "cooloff must be between 0 and 86400 seconds",
		})
		return nil, false
	}

	verbs := joinList(req.Verbs)
	// A verb the trail never produces is a rule that never fires, and it looks
	// identical to one that does. Refusing it is the only way an operator finds out.
	for _, verb := range splitTrimmed(verbs) {
		if !slices.Contains(auditVerbVocabulary, strings.ToLower(verb)) {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": verb + " is not a verb the audit trail records",
			})
			return nil, false
		}
	}

	// A cluster-event rule matching every reason *and* every type fires on
	// everything the cluster says, which is thousands of Normal events an hour.
	// Refusing it here is kinder than letting somebody discover it on a pager.
	if trigger == db.TriggerClusterEvent && eventType == "" && len(req.EventReasons) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "a cluster-event rule needs at least an event type or one reason, " +
				"otherwise it matches every event the cluster emits",
		})
		return nil, false
	}

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	return &db.AlarmRule{
		Name:           name,
		Description:    strings.TrimSpace(req.Description),
		ChannelID:      req.ChannelID,
		Enabled:        enabled,
		Trigger:        trigger,
		ClusterID:      req.ClusterID,
		Namespaces:     joinList(req.Namespaces),
		EventReasons:   joinList(req.EventReasons),
		EventType:      eventType,
		Verbs:          verbs,
		DeniedOnly:     req.DeniedOnly,
		MinStatus:      req.MinStatus,
		Severity:       severity,
		CooloffSeconds: req.CooloffSeconds,
	}, true
}

// auditVerbVocabulary is every verb an audit record can carry: the ones the
// selection in Settings governs, plus KubeMG's own recording-access verbs — which
// are exactly the ones an alarm on "who watched a production shell" would name.
var auditVerbVocabulary = append(
	append([]string{}, auditpolicy.Verbs...),
	"replay", "recording-get", "recording-delete",
)

// joinList renders a submitted list into the stored comma-separated form,
// trimming and dropping blanks so a trailing comma in a form field does not
// become an empty matcher term that matches nothing.
func joinList(values []string) string {
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return strings.Join(out, ",")
}

func splitTrimmed(raw string) []string {
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

// refreshAlarms re-reads the rule set immediately after a change, so a rule saved
// in the console is live on the next signal rather than at the next refresh tick.
func (s *server) refreshAlarms(c *gin.Context) {
	if s.alarms == nil {
		return
	}
	s.alarms.Refresh(c.Request.Context())
}
