package db

import "time"

/*
 * Command guardrails: the calls this platform refuses to pass on.
 *
 * Everything else in KubeMG answers "may this person do this?" by asking the
 * cluster — the proxy impersonates the caller and the cluster's own RBAC decides.
 * A guardrail is the one control that is deliberately *not* that. It is not about
 * privilege: the people it stops are usually the ones who genuinely hold the
 * privilege, which is exactly why `kubectl delete ns prod` succeeds. RBAC has no
 * way to express "an admin may do this, but not by typing it into a terminal at
 * 03:00", and that is the whole of what a guardrail says.
 *
 * So it sits in the gateway, in front of the tunnel, and it applies to both
 * shapes a destructive act arrives in: an API call, and a line typed into a
 * shell. Two scopes, for the same reason alarm rules have two: a fleet-wide rule
 * covers clusters registered *after* it was written, which no per-cluster rule
 * can keep up with, while a per-cluster rule is what lets production be stricter
 * than a sandbox.
 */

// Guardrail targets. A rule watches one shape of traffic or both; they are
// genuinely different subjects, and a pattern written for one usually reads as
// nonsense against the other.
const (
	// GuardrailTargetAPIRequest matches proxied Kubernetes API calls. The
	// subject is "METHOD /path", so `DELETE /api/v1/namespaces/.*` is a rule
	// about deleting namespaces and nothing else.
	GuardrailTargetAPIRequest = "api_request"
	// GuardrailTargetTerminalExec matches what an operator runs in a container:
	// a line typed into an interactive shell, and the argv of a non-interactive
	// `kubectl exec -- ...`.
	GuardrailTargetTerminalExec = "terminal_exec"
	// GuardrailTargetBoth applies a rule to either subject. It is the right
	// choice for a pattern that reads naturally against both, and the wrong one
	// for most patterns.
	GuardrailTargetBoth = "both"
)

// GuardrailTargets enumerates what a policy may watch.
var GuardrailTargets = []string{
	GuardrailTargetAPIRequest,
	GuardrailTargetTerminalExec,
	GuardrailTargetBoth,
}

// Guardrail actions.
const (
	// GuardrailActionBlock refuses the call. An API request gets a 403 and never
	// reaches the tunnel; a typed command is discarded before the shell sees it.
	GuardrailActionBlock = "block"
	// GuardrailActionWarn lets the call through and records it as having matched.
	// It is how a rule is rolled out on a busy fleet: run it in warn for a week,
	// read the trail, and switch it to block once it is clear what it catches.
	// A rule nobody dares enable is worth less than one that only observes.
	GuardrailActionWarn = "warn"
)

// GuardrailActions enumerates what a policy may do.
var GuardrailActions = []string{GuardrailActionBlock, GuardrailActionWarn}

// GuardrailPolicy is one refusal rule.
//
// Like an alarm rule this is read on every call and written by hand a few times
// a year, so the whole set is compiled into memory and matched there; see
// pkg/guardrails. Nothing about it is normalised into further tables for the
// same reason — the set is small enough to fit in a cache line, and a join on
// the gateway's hot path would cost more than the entire feature saves.
type GuardrailPolicy struct {
	ID   uint   `gorm:"primaryKey" json:"id"`
	Name string `gorm:"size:120;not null" json:"name"`
	// Description is why this rule exists. It matters more here than on most
	// records: the person who meets a guardrail is being told "no" by a colleague
	// who is not in the room, and this is the only place that colleague gets to
	// explain themselves.
	Description string `gorm:"type:text" json:"description,omitempty"`

	// ClusterID scopes the rule to one cluster. Zero means every cluster,
	// including ones registered later — a fleet-wide "never delete a namespace"
	// has to cover the cluster somebody registers next month, which is the case a
	// set of per-cluster rules can never keep up with.
	ClusterID uint `gorm:"index" json:"cluster_id"`

	// Pattern is an RE2 regular expression matched against the subject named by
	// Target. It is unanchored, so it matches anywhere in the subject.
	Pattern string `gorm:"type:text;not null" json:"pattern"`
	// Target is which subject it is matched against. See the target constants.
	Target string `gorm:"size:16;not null;default:'both'" json:"target"`
	// Action is what happens on a match.
	Action string `gorm:"size:16;not null;default:'block'" json:"action"`

	// Enabled deliberately carries **no** `default:true` tag, unlike most flags in
	// this schema. GORM omits a zero-valued field from an INSERT when the column
	// has a default, so `Enabled: false` would be silently written as true — which
	// would arm every seeded preset on a fresh install and quietly enable any rule
	// created through the API with the box unticked. The default belongs where the
	// decision is made: the HTTP handler defaults a new rule to enabled, and the
	// seed writes false and means it.
	Enabled bool `gorm:"not null" json:"enabled"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName pins the policy table name.
func (GuardrailPolicy) TableName() string { return "guardrail_policies" }

// Global reports whether this policy covers the whole fleet.
func (p GuardrailPolicy) Global() bool { return p.ClusterID == 0 }

// CoversAPIRequests reports whether this policy is matched against proxied API
// calls.
func (p GuardrailPolicy) CoversAPIRequests() bool {
	return p.Target == GuardrailTargetAPIRequest || p.Target == GuardrailTargetBoth
}

// CoversTerminalInput reports whether this policy is matched against commands
// run inside a container.
func (p GuardrailPolicy) CoversTerminalInput() bool {
	return p.Target == GuardrailTargetTerminalExec || p.Target == GuardrailTargetBoth
}
