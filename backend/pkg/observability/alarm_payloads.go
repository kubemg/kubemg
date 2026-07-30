package observability

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * One signal, five bodies.
 *
 * Every destination here accepts an HTTP POST of JSON, and that is where the
 * similarity ends: Alertmanager wants an array of label sets, PagerDuty wants a
 * routing key inside the payload, ServiceNow wants incident fields with numeric
 * urgencies, Slack wants prose. Rendering is kept in one file so that the
 * differences are read side by side, and each renderer is deliberately dull —
 * the interesting decisions are all in what a Signal contains.
 *
 * The rule that holds across all of them: nothing here invents information. A
 * field a signal does not carry is omitted rather than filled with a placeholder,
 * because a pager entry that says "namespace: unknown" costs the recipient a
 * lookup to discover it means "cluster-scoped".
 */

// render builds the body for one channel.
func (d *Dispatcher) render(
	channel db.AlarmChannel, rule db.AlarmRule, signal Signal,
) ([]byte, string, error) {
	severity := rule.Severity
	if severity == "" {
		severity = db.SeverityWarning
	}

	var payload any
	switch channel.Kind {
	case db.ChannelAlertmanager:
		payload = d.alertmanagerPayload(rule, signal, severity)
	case db.ChannelSlack:
		payload = d.slackPayload(rule, signal, severity)
	case db.ChannelPagerDuty:
		payload = d.pagerDutyPayload(channel, rule, signal, severity)
	case db.ChannelServiceNow:
		payload = d.serviceNowPayload(rule, signal, severity)
	case db.ChannelWebhook:
		payload = d.webhookPayload(rule, signal, severity)
	default:
		return nil, "", fmt.Errorf("unsupported alarm channel kind %q", channel.Kind)
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, "", fmt.Errorf("encode %s payload: %w", channel.Kind, err)
	}
	return body, "application/json", nil
}

// summary is the one line a recipient reads first, on whichever surface they read
// it. Every renderer leads with it.
func summary(signal Signal, severity string) string {
	var b strings.Builder
	b.WriteString("[")
	b.WriteString(strings.ToUpper(severity))
	b.WriteString("] ")

	if signal.Source == SourceAudit {
		verb := signal.Verb
		if verb == "" {
			verb = "request"
		}
		if signal.Denied {
			fmt.Fprintf(&b, "%s denied for %s", verb, orDash(signal.Username))
		} else {
			fmt.Fprintf(&b, "%s by %s", verb, orDash(signal.Username))
		}
	} else {
		fmt.Fprintf(&b, "%s", orDash(signal.Reason))
		if signal.Object != "" {
			fmt.Fprintf(&b, " on %s", signal.Object)
		}
	}

	if signal.Cluster != "" {
		fmt.Fprintf(&b, " · %s", signal.Cluster)
	}
	if signal.Namespace != "" {
		fmt.Fprintf(&b, "/%s", signal.Namespace)
	}
	return b.String()
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

// labels is the flat, low-cardinality set an Alertmanager routes on. Anything
// unbounded — a path, a message — belongs in annotations instead: a label with a
// pod name per value is how a fleet's Alertmanager runs out of memory.
func labels(rule db.AlarmRule, signal Signal, severity string) map[string]string {
	out := map[string]string{
		"alertname": alertName(signal),
		"severity":  severity,
		"source":    "kubemg",
		"stream":    signal.Source,
		"rule":      rule.Name,
	}
	put := func(key, value string) {
		if strings.TrimSpace(value) != "" {
			out[key] = value
		}
	}
	put("cluster", signal.Cluster)
	put("namespace", signal.Namespace)
	put("reason", signal.Reason)
	put("type", signal.Type)
	put("verb", signal.Verb)
	// The username is bounded by the size of the user directory, which is the same
	// order as the namespace count Alertmanager already carries fine.
	put("username", signal.Username)
	if signal.Status != 0 {
		out["status"] = strconv.Itoa(signal.Status)
	}
	return out
}

// alertName is the stable identity of the *condition*, which is what an
// Alertmanager groups, silences and inhibits by — so it must not contain the
// object it fired on.
func alertName(signal Signal) string {
	if signal.Source == SourceAudit {
		if signal.Denied {
			return "KubeMGActionDenied"
		}
		return "KubeMGAuditEvent"
	}
	if signal.Reason != "" {
		return "KubeMGClusterEvent" + sanitizeLabel(signal.Reason)
	}
	return "KubeMGClusterEvent"
}

// sanitizeLabel keeps only what a Prometheus label value may hold in a name
// position. An Event reason is already CamelCase in practice, but it comes from
// whichever controller wrote it, so it is not trusted to be.
func sanitizeLabel(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		}
	}
	return b.String()
}

// annotations carry the unbounded detail.
func (d *Dispatcher) annotations(signal Signal, severity string) map[string]string {
	out := map[string]string{
		"summary": summary(signal, severity),
	}
	put := func(key, value string) {
		if strings.TrimSpace(value) != "" {
			out[key] = value
		}
	}
	put("description", signal.Message)
	put("object", signal.Object)
	put("path", signal.Path)
	put("error", signal.Error)
	if signal.Count > 1 {
		out["occurrences"] = strconv.FormatInt(int64(signal.Count), 10)
	}
	return out
}

// generatorURL links back to the thing the alarm is about. It points at the audit
// trail for an audit signal and at the cluster's resources for a cluster event,
// because those are where the next question gets answered.
func (d *Dispatcher) generatorURL(signal Signal) string {
	if d.origin == "" {
		return ""
	}
	if signal.Source == SourceAudit {
		return fmt.Sprintf("%s/audit", d.origin)
	}
	if signal.ClusterID != 0 {
		return fmt.Sprintf("%s/explore/%d", d.origin, signal.ClusterID)
	}
	return d.origin
}

/* --------------------------------------------------------- alertmanager --- */

// alertmanagerAlert is one entry of the v2 POST /api/v2/alerts array.
type alertmanagerAlert struct {
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	StartsAt     string            `json:"startsAt"`
	GeneratorURL string            `json:"generatorURL,omitempty"`
}

// alertmanagerPayload renders the v2 alert array.
//
// No `endsAt` is sent. An alarm here is a point-in-time fact — a pod was
// OOMKilled, a request was refused — not a condition being continuously
// evaluated, so KubeMG has nothing that would ever resolve it. Alertmanager's own
// `resolve_timeout` is the right thing to expire it, and sending an endsAt we
// cannot honour would make an alert vanish while the problem stands.
func (d *Dispatcher) alertmanagerPayload(
	rule db.AlarmRule, signal Signal, severity string,
) []alertmanagerAlert {
	return []alertmanagerAlert{{
		Labels:       labels(rule, signal, severity),
		Annotations:  d.annotations(signal, severity),
		StartsAt:     signal.At.UTC().Format(time.RFC3339),
		GeneratorURL: d.generatorURL(signal),
	}}
}

/* ---------------------------------------------------------------- slack --- */

type slackField struct {
	Title string `json:"title"`
	Value string `json:"value"`
	Short bool   `json:"short"`
}

type slackAttachment struct {
	Color  string       `json:"color"`
	Title  string       `json:"title,omitempty"`
	Text   string       `json:"text,omitempty"`
	Fields []slackField `json:"fields,omitempty"`
	Footer string       `json:"footer,omitempty"`
	TS     int64        `json:"ts,omitempty"`
}

type slackMessage struct {
	Text        string            `json:"text"`
	Attachments []slackAttachment `json:"attachments,omitempty"`
}

// slackPayload renders an incoming-webhook message. Attachments rather than
// Block Kit: attachments carry a colour bar, which is the fastest way to read
// severity in a busy channel, and they are what every Slack-compatible endpoint
// (Mattermost, Rocket.Chat) also accepts.
func (d *Dispatcher) slackPayload(rule db.AlarmRule, signal Signal, severity string) slackMessage {
	fields := []slackField{}
	add := func(title, value string) {
		if strings.TrimSpace(value) != "" {
			fields = append(fields, slackField{Title: title, Value: value, Short: true})
		}
	}
	add("Cluster", signal.Cluster)
	add("Namespace", signal.Namespace)
	add("Object", signal.Object)
	add("Reason", signal.Reason)
	add("User", signal.Username)
	if signal.Status != 0 {
		add("Status", strconv.Itoa(signal.Status))
	}
	if signal.Count > 1 {
		add("Occurrences", strconv.FormatInt(int64(signal.Count), 10))
	}

	text := signal.Message
	if signal.Path != "" {
		text = strings.TrimSpace(text + "\n`" + signal.Path + "`")
	}

	return slackMessage{
		Text: summary(signal, severity),
		Attachments: []slackAttachment{{
			Color:  slackColor(severity),
			Text:   text,
			Fields: fields,
			Footer: "KubeMG · " + rule.Name,
			TS:     signal.At.Unix(),
		}},
	}
}

// slackColor maps severity onto Slack's own palette words where it has them, and
// onto a hex otherwise. These are Slack's colours rather than the deck's on
// purpose: the message is rendered by Slack, in Slack's own light and dark
// themes, and a token from index.css would be meaningless there.
func slackColor(severity string) string {
	switch severity {
	case db.SeverityCritical:
		return "danger"
	case db.SeverityWarning:
		return "warning"
	default:
		return "#4a90d9"
	}
}

/* ------------------------------------------------------------ pagerduty --- */

type pagerDutyPayloadBody struct {
	Summary       string         `json:"summary"`
	Severity      string         `json:"severity"`
	Source        string         `json:"source"`
	Component     string         `json:"component,omitempty"`
	Group         string         `json:"group,omitempty"`
	Class         string         `json:"class,omitempty"`
	Timestamp     string         `json:"timestamp,omitempty"`
	CustomDetails map[string]any `json:"custom_details,omitempty"`
}

type pagerDutyEvent struct {
	RoutingKey  string               `json:"routing_key"`
	EventAction string               `json:"event_action"`
	DedupKey    string               `json:"dedup_key,omitempty"`
	Client      string               `json:"client,omitempty"`
	ClientURL   string               `json:"client_url,omitempty"`
	Payload     pagerDutyPayloadBody `json:"payload"`
}

// pagerDutyPayload renders an Events API v2 trigger.
//
// The routing key is the channel's stored secret and goes in the body, which is
// why AuthKey exists as an auth mode at all — PagerDuty has no header for it. The
// dedup key is the signal's fingerprint, so PagerDuty collapses repeats into one
// incident even where they arrive from different KubeMG replicas whose in-memory
// cool-offs know nothing about each other.
func (d *Dispatcher) pagerDutyPayload(
	channel db.AlarmChannel, rule db.AlarmRule, signal Signal, severity string,
) pagerDutyEvent {
	details := map[string]any{"rule": rule.Name, "stream": signal.Source}
	put := func(key string, value string) {
		if strings.TrimSpace(value) != "" {
			details[key] = value
		}
	}
	put("cluster", signal.Cluster)
	put("namespace", signal.Namespace)
	put("object", signal.Object)
	put("reason", signal.Reason)
	put("message", signal.Message)
	put("username", signal.Username)
	put("path", signal.Path)
	put("error", signal.Error)
	if signal.Status != 0 {
		details["status"] = signal.Status
	}
	if signal.Count > 1 {
		details["occurrences"] = signal.Count
	}

	source := signal.Cluster
	if source == "" {
		source = "kubemg"
	}

	return pagerDutyEvent{
		RoutingKey:  channel.Secret,
		EventAction: "trigger",
		DedupKey:    fingerprintOf(signal),
		Client:      "KubeMG",
		ClientURL:   d.generatorURL(signal),
		Payload: pagerDutyPayloadBody{
			Summary:       summary(signal, severity),
			Severity:      pagerDutySeverity(severity),
			Source:        source,
			Component:     signal.Namespace,
			Group:         signal.Cluster,
			Class:         signal.Reason,
			Timestamp:     signal.At.UTC().Format(time.RFC3339),
			CustomDetails: details,
		},
	}
}

// pagerDutySeverity maps onto the four words the Events API accepts. Anything it
// does not recognise is rejected with a 400, so this is a total mapping.
func pagerDutySeverity(severity string) string {
	switch severity {
	case db.SeverityCritical:
		return "critical"
	case db.SeverityWarning:
		return "warning"
	default:
		return "info"
	}
}

/* ----------------------------------------------------------- servicenow --- */

type serviceNowIncident struct {
	ShortDescription string `json:"short_description"`
	Description      string `json:"description"`
	// Urgency and Impact are ServiceNow's own 1–3 scale, 1 being highest. They are
	// what its priority matrix is computed from, so sending only one of them lands
	// every incident on the default priority.
	Urgency  string `json:"urgency"`
	Impact   string `json:"impact"`
	Category string `json:"category,omitempty"`
	// CallerID is left to the instance's own default. KubeMG's usernames are not
	// ServiceNow sys_ids, and guessing at one produces an incident attributed to
	// nobody.
	CorrelationID      string `json:"correlation_id,omitempty"`
	CorrelationDisplay string `json:"correlation_display,omitempty"`
	CmdbCI             string `json:"cmdb_ci,omitempty"`
	WorkNotes          string `json:"work_notes,omitempty"`
}

// serviceNowPayload renders a Table API incident, which is close enough to what
// most ITSM tools accept to be the generic ITSM shape.
//
// The correlation id is the fingerprint, which is what stops a crash loop from
// opening four hundred tickets: ServiceNow's own correlation rules collapse them,
// and unlike a pager an ITSM queue that has been flooded needs a human to empty
// it.
func (d *Dispatcher) serviceNowPayload(
	rule db.AlarmRule, signal Signal, severity string,
) serviceNowIncident {
	urgency, impact := serviceNowPriority(severity)

	var description strings.Builder
	description.WriteString(summary(signal, severity))
	description.WriteString("\n\n")
	writeLine := func(label, value string) {
		if strings.TrimSpace(value) != "" {
			fmt.Fprintf(&description, "%s: %s\n", label, value)
		}
	}
	writeLine("Detected", signal.At.UTC().Format(time.RFC3339))
	writeLine("Cluster", signal.Cluster)
	writeLine("Namespace", signal.Namespace)
	writeLine("Object", signal.Object)
	writeLine("Reason", signal.Reason)
	writeLine("User", signal.Username)
	if signal.Status != 0 {
		writeLine("Status", strconv.Itoa(signal.Status))
	}
	writeLine("Request", signal.Path)
	writeLine("Detail", signal.Message)
	writeLine("Error", signal.Error)
	writeLine("Rule", rule.Name)
	if link := d.generatorURL(signal); link != "" {
		writeLine("KubeMG", link)
	}

	return serviceNowIncident{
		ShortDescription:   truncate(summary(signal, severity), 160),
		Description:        description.String(),
		Urgency:            urgency,
		Impact:             impact,
		Category:           "kubernetes",
		CorrelationID:      fingerprintOf(signal),
		CorrelationDisplay: "KubeMG",
		CmdbCI:             signal.Cluster,
	}
}

// serviceNowPriority maps severity onto the urgency/impact pair. A critical is
// 1/1; a warning is urgent but narrow (2/3), because a single OOMKilled pod is
// not a service outage and filing it as one is how an ITSM integration gets
// switched off.
func serviceNowPriority(severity string) (string, string) {
	switch severity {
	case db.SeverityCritical:
		return "1", "1"
	case db.SeverityWarning:
		return "2", "3"
	default:
		return "3", "3"
	}
}

/* --------------------------------------------------------------- webhook --- */

// webhookEnvelope is the SIEM shape: the signal itself, plus which rule matched
// and how urgent that rule considers it. Nothing is reshaped, because a SIEM has
// its own parsers and every transformation here would be one it has to undo.
type webhookEnvelope struct {
	Version  string `json:"version"`
	Source   string `json:"source"`
	Rule     string `json:"rule"`
	RuleID   uint   `json:"rule_id"`
	Severity string `json:"severity"`
	Link     string `json:"link,omitempty"`
	Signal   Signal `json:"signal"`
}

func (d *Dispatcher) webhookPayload(
	rule db.AlarmRule, signal Signal, severity string,
) webhookEnvelope {
	if signal.Fingerprint == "" {
		signal.Fingerprint = fingerprintOf(signal)
	}
	return webhookEnvelope{
		Version:  "kubemg.alarm/v1",
		Source:   "kubemg",
		Rule:     rule.Name,
		RuleID:   rule.ID,
		Severity: severity,
		Link:     d.generatorURL(signal),
		Signal:   signal,
	}
}
