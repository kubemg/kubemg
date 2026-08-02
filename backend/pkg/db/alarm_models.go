package db

import "time"

/*
 * Alarms: where a cluster event or a refused action goes when somebody has to
 * know about it now.
 *
 * The trail answers "what happened" for anyone who goes and looks. A rule is for
 * the things nobody is looking at: a pod OOMKilled at 03:00, a scheduler that
 * cannot place a workload, a developer whose kubectl was refused thirty times in
 * a minute. Two tables, split the way the responsibility splits — a channel is
 * where a message goes and is configured once, a rule is what is worth sending
 * and is configured many times.
 *
 * They are separate rows rather than one because the credential lives on the
 * channel. A rule can then be edited, duplicated and switched on and off by
 * anyone who may administer alarms without the PagerDuty routing key being
 * read back out to do it.
 */

// Alarm channel kinds. Each one is a different body over the same POST, which is
// the whole reason the kind is stored rather than inferred from the URL: an
// Alertmanager and a Slack webhook are both "an https endpoint", and sending one
// the other's payload fails silently at the far end.
const (
	// ChannelAlertmanager posts the Alertmanager v2 alert array. It is the one
	// that composes: a fleet that already routes Alertmanager gets KubeMG's
	// alarms through its existing silences, inhibitions and on-call rotation
	// rather than beside them.
	ChannelAlertmanager = "alertmanager"
	ChannelSlack        = "slack"
	// ChannelTeams posts an Adaptive Card to a Microsoft Teams webhook. It is its
	// own kind rather than a Slack-compatible one because Teams accepts neither
	// Slack's attachments nor its blocks: the body is a card inside an attachment
	// envelope, and sending Slack's shape to it fails at the far end with a 400
	// that says nothing useful.
	ChannelTeams     = "teams"
	ChannelPagerDuty = "pagerduty"
	// ChannelServiceNow opens an incident through the Table API, which is also
	// close enough to what most ITSM tools accept to be the generic ITSM shape.
	ChannelServiceNow = "servicenow"
	// ChannelWebhook posts the signal itself as JSON. This is what a SIEM
	// aggregator wants — its own parsers, not a vendor's alert envelope.
	ChannelWebhook = "webhook"
)

// AlarmChannelKinds enumerates the supported destinations.
var AlarmChannelKinds = []string{
	ChannelAlertmanager,
	ChannelSlack,
	ChannelTeams,
	ChannelPagerDuty,
	ChannelServiceNow,
	ChannelWebhook,
}

// AuthKey is a routing or integration key carried in the *payload* rather than in
// a header — PagerDuty's Events API v2 shape. It joins AuthNone/AuthBearer/
// AuthBasic, which the observability datasources already define and which mean
// exactly the same thing here: a credential is a credential, and a second
// vocabulary for it would only be a second thing to keep in step.
const AuthKey = "key"

// AlarmAuthModes enumerates what a channel may authenticate with.
var AlarmAuthModes = []string{AuthNone, AuthBearer, AuthBasic, AuthKey}

// Trigger sources. A rule watches one of them; they are genuinely different
// things and a rule that tried to match both would need every field of each.
const (
	// TriggerClusterEvent matches Kubernetes Events read from the cluster —
	// OOMKilled, FailedScheduling, anything of type Warning.
	TriggerClusterEvent = "cluster_event"
	// TriggerAudit matches KubeMG's own audit records, which is how a refused
	// action becomes a page. This is the half no cluster-side alerting can see:
	// the request never reached the API server, so the cluster has no event for it.
	TriggerAudit = "audit"
)

// AlarmTriggers enumerates the supported rule sources.
var AlarmTriggers = []string{TriggerClusterEvent, TriggerAudit}

// Alarm severities, ordered least to most urgent. They are KubeMG's own word for
// it and each channel renders them into its vendor's vocabulary.
const (
	SeverityInfo     = "info"
	SeverityWarning  = "warning"
	SeverityCritical = "critical"
)

// AlarmSeverities enumerates the supported severities.
var AlarmSeverities = []string{SeverityInfo, SeverityWarning, SeverityCritical}

// AlarmChannel is one destination alarms are delivered to.
type AlarmChannel struct {
	ID   uint   `gorm:"primaryKey" json:"id"`
	Name string `gorm:"size:120;uniqueIndex;not null" json:"name"`
	// Kind decides the body. See the channel constants.
	Kind string `gorm:"size:32;not null" json:"kind"`
	// URL is the endpoint. It is dialled from the bastion rather than from inside
	// a cluster, so it has to be reachable from here — which is usually the point:
	// a central Alertmanager or a hosted ITSM is exactly what a fleet has one of.
	URL string `gorm:"type:text;not null" json:"url"`

	AuthMode string `gorm:"size:16;not null;default:'none'" json:"auth_mode"`
	// Username pairs with Secret for basic auth. It is not a credential on its
	// own, so unlike Secret it is readable.
	Username string `gorm:"size:190" json:"username,omitempty"`
	// Secret is the bearer token, the basic-auth password or the routing key. It
	// is never serialized, for the same reason a cluster's service account token
	// is not: an admin console that reads credentials back out turns one
	// compromised session into every integration KubeMG holds.
	Secret string `gorm:"type:text" json:"-"`
	// Headers are extra headers as a JSON object, for the destinations that want
	// one — a SIEM's tenant id, ServiceNow's API version. Stored as text because
	// this is the only place that reads it and a second table would buy nothing.
	Headers string `gorm:"type:text" json:"headers,omitempty"`

	Enabled bool `gorm:"not null;default:true" json:"enabled"`

	// Delivery health, recorded on every attempt. An integration that silently
	// stopped working is the failure mode that matters here: nobody notices a
	// page that was never sent, so the channel has to be able to say so.
	LastStatus    string     `gorm:"size:16" json:"last_status,omitempty"`
	LastMessage   string     `gorm:"type:text" json:"last_message,omitempty"`
	LastAttemptAt *time.Time `json:"last_attempt_at,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName pins the channel table name.
func (AlarmChannel) TableName() string { return "alarm_channels" }

// HasSecret reports whether a credential is stored, which is what the UI shows
// in place of the credential itself.
func (c AlarmChannel) HasSecret() bool { return c.Secret != "" }

// AlarmRule is one condition worth sending somewhere.
//
// Every matching field is stored as a comma-separated list rather than a join
// table on purpose: a rule is read on every signal and written by hand a few
// times a year, so the whole rule set is loaded into memory and matched there.
// Normalising it would add three tables and a join to a query that runs against
// a set small enough to fit in a cache line.
type AlarmRule struct {
	ID   uint   `gorm:"primaryKey" json:"id"`
	Name string `gorm:"size:120;not null" json:"name"`
	// Description is why this rule exists, which is the field that stops a rule
	// set from becoming undeletable six months later.
	Description string `gorm:"type:text" json:"description,omitempty"`

	ChannelID uint `gorm:"index;not null" json:"channel_id"`
	Enabled   bool `gorm:"not null;default:true" json:"enabled"`

	// Trigger is which stream this rule watches.
	Trigger string `gorm:"size:24;not null" json:"trigger"`
	// ClusterID scopes the rule to one cluster. Zero means every cluster,
	// *including ones registered later* — which is the useful default for a rule
	// like "page me when a pod is OOMKilled anywhere", and something a rule written
	// per cluster could never keep up with.
	ClusterID uint `gorm:"index" json:"cluster_id"`
	// Namespaces narrows to a comma-separated list. Empty means every namespace.
	Namespaces string `gorm:"type:text" json:"namespaces,omitempty"`

	// Cluster-event matching.
	//
	// EventReasons is a comma-separated list of Event reasons — OOMKilled,
	// FailedScheduling, BackOff. Empty means any reason, which is only sensible
	// alongside an EventType.
	EventReasons string `gorm:"type:text" json:"event_reasons,omitempty"`
	// EventType is Warning or Normal. Empty means either, though in practice a
	// rule that pages on Normal events pages constantly.
	EventType string `gorm:"size:16" json:"event_type,omitempty"`

	// Audit matching.
	//
	// Verbs is a comma-separated list of audit verbs. Empty means any verb.
	Verbs string `gorm:"type:text" json:"verbs,omitempty"`
	// DeniedOnly keeps only refusals — a 4xx, a 5xx or a call that never reached
	// the API server. This is the setting most audit rules want and the reason
	// this trigger exists: an action KubeMG refused leaves no trace in the cluster.
	DeniedOnly bool `gorm:"not null;default:false" json:"denied_only"`
	// MinStatus keeps records at or above an HTTP status. Zero means any.
	MinStatus int `json:"min_status,omitempty"`

	// Severity is what the delivered alarm claims about itself.
	Severity string `gorm:"size:16;not null;default:'warning'" json:"severity"`
	// CooloffSeconds suppresses a repeat of the same fingerprint. Zero takes the
	// dispatcher's default. It is per rule because the right answer differs: a
	// crash loop re-emits its event every few seconds and a refused delete does
	// not, and a channel that pages once a minute forever gets muted by its
	// recipient, which is the same as not having it.
	CooloffSeconds int `json:"cooloff_seconds,omitempty"`

	LastFiredAt *time.Time `json:"last_fired_at,omitempty"`
	FireCount   int64      `gorm:"not null;default:0" json:"fire_count"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName pins the rule table name.
func (AlarmRule) TableName() string { return "alarm_rules" }
