// Package guardrails decides which proxied calls and which typed commands the
// gateway refuses to pass on.
//
// It exists as a package of its own for the same reason auditpolicy does, and
// with the same shape. The rules live in the database and are edited from the
// Settings page, which is the HTTP layer's business; the decision is consulted
// once per proxied call and once per line typed into a shell, which is the
// gateway's hot path. Neither side may depend on the other, and the hot path must
// never take a database round trip to find out whether to refuse — so the rules
// are resolved and compiled by the HTTP layer, published here as an immutable
// snapshot, and read lock-free.
//
// The zero-value Engine refuses nothing. That is the important default: a server
// that has not read its rules yet, or one wired without an engine at all, is a
// gateway that behaves exactly as it did before this package existed. A
// guardrail failing open is a policy that did not apply; a guardrail failing
// closed would be a fleet nobody can reach.
package guardrails

import (
	"fmt"
	"regexp"
	"strings"
	"sync/atomic"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

// maxPatternLength bounds a stored pattern. RE2 has no catastrophic
// backtracking, so this is about keeping a rule readable and a compile cheap
// rather than about safety.
const maxPatternLength = 512

// maxSubjectLength bounds what is matched against. A pasted heredoc or a very
// long API path is truncated rather than matched in full: RE2 is linear in the
// subject, and an operator smuggling something past a rule by padding it to a
// megabyte would have to get the interesting part past the truncation first.
const maxSubjectLength = 4096

// Decision is one policy match. Nil means nothing matched, which is the answer
// almost every call gets.
type Decision struct {
	// PolicyID and Policy identify the rule, so a refusal can name itself. A
	// message that says only "blocked" sends its reader to an administrator with
	// nothing to go on.
	PolicyID uint
	Policy   string
	// Action is what the match means; see the db action constants.
	Action string
	// Scope is "global" or "cluster", which is the first thing anyone asks when a
	// rule fires somewhere they did not expect.
	Scope string
	// Pattern is the expression that matched, carried for the audit record.
	Pattern string
	// Target is the subject the rule was written against.
	Target string
}

// Scope values.
const (
	ScopeGlobal  = "global"
	ScopeCluster = "cluster"
)

// Blocked reports whether this decision refuses the call. A nil decision and a
// warn-only match both allow it, which is why callers ask this rather than
// checking for nil.
func (d *Decision) Blocked() bool {
	return d != nil && d.Action == db.GuardrailActionBlock
}

// Message is what the person who hit the rule is told. It names the policy,
// because the alternative is a refusal nobody can act on.
func (d *Decision) Message() string {
	if d == nil {
		return ""
	}
	if d.Action == db.GuardrailActionWarn {
		return fmt.Sprintf("Flagged by KubeMG Safety Policy: %s", d.Policy)
	}
	return fmt.Sprintf("Blocked by KubeMG Safety Policy: %s", d.Policy)
}

// rule is one compiled policy. Which subjects it applies to is resolved here
// rather than per call: the target never changes between publishes, and every
// proxied request would otherwise pay to re-derive it.
type rule struct {
	id       uint
	name     string
	action   string
	target   string
	pattern  string
	scope    string
	re       *regexp.Regexp
	api      bool
	terminal bool
}

func (r rule) decide() *Decision {
	return &Decision{
		PolicyID: r.id,
		Policy:   r.name,
		Action:   r.action,
		Scope:    r.scope,
		Pattern:  r.pattern,
		Target:   r.target,
	}
}

// Snapshot is one resolved rule set. It is replaced wholesale rather than
// mutated, so a reader always sees a coherent set rather than a half-applied
// change — which matters here more than in most caches: half of a rule set is a
// gateway enforcing a policy nobody wrote.
type Snapshot struct {
	// global applies to every cluster. Split from the per-cluster rules at
	// compile time rather than filtered per call, because every call pays for
	// this and the split never changes between publishes.
	global    []rule
	byCluster map[uint][]rule
}

// Compile resolves a rule set from stored policies.
//
// A policy whose pattern does not compile is skipped and reported rather than
// failing the whole set. The alternative is worse in both directions: refusing
// the publish leaves the gateway on a stale rule set nobody can see, and taking
// the set down entirely would let one bad regular expression disable every other
// rule. Disabled policies are dropped here, so the hot path never sees them.
func Compile(policies []db.GuardrailPolicy) (Snapshot, []error) {
	snapshot := Snapshot{byCluster: map[uint][]rule{}}
	var problems []error

	for _, policy := range policies {
		if !policy.Enabled {
			continue
		}
		re, err := compilePattern(policy.Pattern)
		if err != nil {
			problems = append(problems, fmt.Errorf("guardrail %q (id %d): %w", policy.Name, policy.ID, err))
			continue
		}

		compiled := rule{
			id:       policy.ID,
			name:     policy.Name,
			action:   policy.Action,
			target:   policy.Target,
			pattern:  policy.Pattern,
			scope:    ScopeCluster,
			re:       re,
			api:      policy.CoversAPIRequests(),
			terminal: policy.CoversTerminalInput(),
		}
		if policy.Global() {
			compiled.scope = ScopeGlobal
			snapshot.global = append(snapshot.global, compiled)
			continue
		}
		snapshot.byCluster[policy.ClusterID] = append(snapshot.byCluster[policy.ClusterID], compiled)
	}
	return snapshot, problems
}

// Rules reports how many rules this snapshot holds, which is what lets the
// console say "nothing is enforced" rather than showing an empty list that could
// equally mean the rules failed to load.
func (s Snapshot) Rules() int {
	total := len(s.global)
	for _, rules := range s.byCluster {
		total += len(rules)
	}
	return total
}

// compilePattern turns a stored pattern into a matcher, refusing the ones that
// are not rules at all. See ValidatePattern for why the empty match is rejected.
func compilePattern(pattern string) (*regexp.Regexp, error) {
	if err := ValidatePattern(pattern); err != nil {
		return nil, err
	}
	return regexp.Compile(pattern)
}

// ValidatePattern checks a pattern before it is stored, so an operator finds out
// at the form rather than from a rule that silently never loads.
//
// The interesting rule is the last one. A pattern that matches the empty string
// — `.*`, `.?`, `^`, an empty box — matches every subject there will ever be,
// which as a block rule is a fleet nobody can reach through KubeMG, entered by
// typing two characters into a text field. It is refused rather than allowed
// with a warning, because the blast radius is every cluster and the person who
// would have to undo it is locked out of the same console.
func ValidatePattern(pattern string) error {
	trimmed := strings.TrimSpace(pattern)
	if trimmed == "" {
		return fmt.Errorf("a pattern is required")
	}
	if len(trimmed) > maxPatternLength {
		return fmt.Errorf("a pattern may be at most %d characters", maxPatternLength)
	}

	re, err := regexp.Compile(trimmed)
	if err != nil {
		return fmt.Errorf("not a valid regular expression: %w", err)
	}
	if re.MatchString("") {
		return fmt.Errorf(
			"this pattern matches everything, which would block every request on every cluster it covers; narrow it")
	}
	return nil
}

// Engine holds the published rule set. The nil Engine is usable and refuses
// nothing, which is what lets the gateway hold one unconditionally.
type Engine struct {
	current atomic.Pointer[Snapshot]
}

// New returns an engine with no rules.
func New() *Engine { return &Engine{} }

// Store publishes a rule set. It replaces the previous one atomically; a call
// in flight finishes against whichever set it started with.
func (e *Engine) Store(snapshot Snapshot) {
	if e == nil {
		return
	}
	e.current.Store(&snapshot)
}

// Snapshot returns the published rule set, or an empty one.
func (e *Engine) Snapshot() Snapshot {
	if e == nil {
		return Snapshot{}
	}
	if current := e.current.Load(); current != nil {
		return *current
	}
	return Snapshot{}
}

// EvaluateAPIRequest matches a proxied Kubernetes API call.
//
// The subject is "METHOD /path", including the query string, which is what makes
// `DELETE /api/v1/namespaces/.*` read as the rule an operator meant to write.
func (e *Engine) EvaluateAPIRequest(clusterID uint, method, path string) *Decision {
	if e == nil {
		return nil
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	subject := strings.ToUpper(strings.TrimSpace(method)) + " " + path
	return e.evaluate(clusterID, subject, func(r rule) bool { return r.api })
}

// EvaluateTerminalInput matches a command run inside a container — one line an
// operator typed, or the argv of a non-interactive exec.
func (e *Engine) EvaluateTerminalInput(clusterID uint, input string) *Decision {
	if e == nil {
		return nil
	}
	subject := strings.TrimSpace(input)
	if subject == "" {
		return nil
	}
	return e.evaluate(clusterID, subject, func(r rule) bool { return r.terminal })
}

// covers reports whether a rule applies to the subject being matched.
type covers func(rule) bool

// evaluate walks the global rules and then the cluster's own.
//
// A block anywhere wins immediately: the question is whether the call is
// refused, and once one rule says so no other rule can un-say it. A warn is
// remembered but kept looking, so a cluster rule that blocks is not masked by a
// global rule that only observes.
func (e *Engine) evaluate(clusterID uint, subject string, applies covers) *Decision {
	if len(subject) > maxSubjectLength {
		subject = subject[:maxSubjectLength]
	}
	snapshot := e.Snapshot()

	var warned *Decision
	for _, set := range [][]rule{snapshot.global, snapshot.byCluster[clusterID]} {
		for _, candidate := range set {
			if !applies(candidate) {
				continue
			}
			if !candidate.re.MatchString(subject) {
				continue
			}
			decision := candidate.decide()
			if decision.Blocked() {
				return decision
			}
			if warned == nil {
				warned = decision
			}
		}
	}
	return warned
}
