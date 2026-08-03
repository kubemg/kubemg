package observability

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

/*
 * What the two query engines share: the window, the scope, and the rule that
 * nothing a caller sends is ever interpolated into a query language.
 *
 * That last rule is the whole security posture of this package's query side, and
 * it is the same one `resources_custom.go` follows for API paths. A namespace or
 * a pod name reaching a PromQL label matcher — or a LogsQL filter — is a place
 * where a quote character stops being data and starts being syntax: `pod="a"} or
 * up{` reads every series in the backend. KubeMG therefore **validates rather
 * than escapes**. A value that does not look like the Kubernetes name it claims
 * to be is refused, and since a validated Kubernetes name cannot contain a quote,
 * a backslash or a brace, what lands in the query is inert by construction.
 *
 * Escaping would also work, right up until someone adds the second query
 * language and escapes it for the first one's rules.
 */

// kubeName is the Kubernetes object-name grammar (RFC 1123 label, as used for
// namespaces, pods and containers). It is anchored, so a value either is one of
// these or is refused — there is no partial match to slip past.
var kubeName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?$`)

// validateName refuses anything that is not a Kubernetes object name, naming the
// field so the refusal is actionable rather than mysterious.
func validateName(field, value string) error {
	if value == "" {
		return nil
	}
	if !kubeName.MatchString(value) {
		return fmt.Errorf("%q is not a valid %s name", value, field)
	}
	return nil
}

// Window is the time range a query covers.
//
// Both engines take one, and both bound it: an unbounded range against a backend
// holding months of data is not a chart, it is an outage. The step is derived
// rather than taken from the caller, because a caller asking for a one-second
// step over a week is asking for six hundred thousand points that no browser will
// draw and no backend should be made to produce.
type Window struct {
	Start time.Time
	End   time.Time
	// Step is the resolution of a range query. Zero means the engine picks one.
	Step time.Duration
}

// MaxWindow is the longest range a single query may cover. Thirty days is well
// past what a chart in a drawer is for, and short enough that a mistyped range
// cannot ask a backend for its whole retention. It is exported because it is
// also what "all time" has to resolve to on this path: a datasource has
// retention, so the widest honest answer is the widest window allowed here.
const MaxWindow = maxWindow

const (
	maxWindow = 30 * 24 * time.Hour
	// defaultWindow is what a caller naming no range gets: long enough to show a
	// deployment's effect, short enough to load instantly.
	defaultWindow = time.Hour
	// maxPoints bounds the resolution. A chart a thousand pixels wide cannot
	// show more, and the step is chosen to respect this rather than the caller's
	// wishes — which is what keeps a wide range from becoming a huge response.
	maxPoints = 500
	// minStep floors the resolution. Below the backend's own scrape interval a
	// finer step invents nothing, it just repeats samples.
	minStep = 15 * time.Second
)

// Normalize fills in what the caller left out and refuses what cannot be served.
// It is deliberately forgiving about absent values and strict about impossible
// ones: an empty range is a caller who wants the default, while a range that ends
// before it starts is a caller who has made a mistake worth being told about.
func (w Window) Normalize(now time.Time) (Window, error) {
	out := w
	if out.End.IsZero() {
		out.End = now
	}
	if out.Start.IsZero() {
		out.Start = out.End.Add(-defaultWindow)
	}
	if !out.End.After(out.Start) {
		return Window{}, fmt.Errorf("the range has to end after it starts")
	}
	span := out.End.Sub(out.Start)
	if span > maxWindow {
		return Window{}, fmt.Errorf(
			"a single query covers at most %d days; ask for a shorter range",
			int(maxWindow.Hours()/24))
	}

	// The step is always at least what the point ceiling implies, so widening the
	// range coarsens the resolution instead of enlarging the answer.
	floor := span / maxPoints
	if floor < minStep {
		floor = minStep
	}
	if out.Step < floor {
		out.Step = floor
	}
	return out, nil
}

// Scope is what the caller is allowed to see, resolved from their grant before
// any query is built.
//
// This is the part that cannot be delegated to the cluster the way a resource
// read can. A read through the tunnel is impersonated, so the cluster's own RBAC
// answers it; a query goes to a metrics backend that has never heard of the
// caller and will answer anything it is asked. So the scope is enforced *here*,
// by building the query around it — which is why a caller never supplies a query
// string in the first place.
type Scope struct {
	// Namespaces is the grant's namespace list. Empty means unscoped: the caller
	// may read the whole cluster, which is what an unscoped grant means
	// everywhere else in KubeMG.
	Namespaces []string
}

// Unscoped reports whether this scope covers the cluster.
func (s Scope) Unscoped() bool { return len(s.Namespaces) == 0 }

// Allows reports whether a namespace is inside the scope.
func (s Scope) Allows(namespace string) bool {
	if s.Unscoped() {
		return true
	}
	for _, allowed := range s.Namespaces {
		if allowed == namespace {
			return true
		}
	}
	return false
}

// resolveNamespace settles which namespace a query runs against, and refuses one
// the caller was not granted.
//
// A scoped caller naming nothing is answered across their own namespaces rather
// than across the cluster — the same rule the resource lists follow for
// `all_namespaces=true`, and for the same reason: the alternative reaches past
// the grant.
func (s Scope) resolveNamespace(requested string) ([]string, error) {
	if err := validateName("namespace", requested); err != nil {
		return nil, err
	}
	if requested != "" {
		if !s.Allows(requested) {
			return nil, fmt.Errorf("namespace %q is outside your granted scope", requested)
		}
		return []string{requested}, nil
	}
	if s.Unscoped() {
		return nil, nil
	}
	return s.Namespaces, nil
}

// promLabelAlternation renders a set of already-validated names as a PromQL
// regex alternation, for matching a scoped caller's namespaces in one query
// rather than one query per namespace.
//
// Every value here has been through validateName, so none of them can carry a
// regex metacharacter beyond the dot a DNS name legitimately contains — and a
// dot inside a character-free alternation matches only itself in practice, since
// no other namespace differs from this one by a single character in that
// position. Anchoring is PromQL's own: `=~` is fully anchored.
func promLabelAlternation(values []string) string {
	escaped := make([]string, 0, len(values))
	for _, value := range values {
		escaped = append(escaped, strings.ReplaceAll(value, ".", `\\.`))
	}
	return strings.Join(escaped, "|")
}
