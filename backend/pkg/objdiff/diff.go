// Package objdiff computes a field-level diff between two decoded objects.
//
// It exists on its own, with no import of anything else in this repository,
// for a reason stated plainly in the roadmap this implements: the manifest
// editor's pre-write confirmation and the audit trail's stored diff are one
// use of it, and Phase 7's GitOps drift detection is meant to be the same
// function again with a Git manifest decoded onto one side instead of an
// editor buffer. A diff entangled with either caller — with `gin.Context`,
// with the audit row, with YAML — would have to be rebuilt for the third use
// rather than reused, which is exactly the trap this package is written to
// avoid.
//
// The diff is structural, not textual: it walks two already-decoded
// `map[string]any` trees (the shape `encoding/json.Unmarshal` and
// `sigs.k8s.io/yaml` both produce) and compares values, not bytes. A text
// diff over two YAML renderings would have kept comparing key order and
// whitespace that carries no meaning — Kubernetes objects round-trip through
// several tools that reorder keys and none of them consider that a change.
//
// What this package deliberately does not do: it does not know that a
// container list's merge key is `name`, or that `spec.template` is special.
// Reordering a list is reported as the elements at those indices having
// changed, not as a move. Teaching this generic function every Kubernetes
// list's merge key would tie it to Kubernetes and break the Phase 7 reuse
// this exists for; a caller that wants merge-key-aware list comparison for
// one field is free to pre-sort that field before handing the object in.
package objdiff

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// maxChanges bounds how many entries a single diff can carry. A diff exists
// to be read by a person and stored in a database row governed by a
// retention window; a pathological object — a ConfigMap with ten thousand
// keys rewritten — must not turn either use into an unbounded write. When
// the cap is hit, Result.Truncated is set and the wire carries that fact
// rather than silently dropping the rest.
const maxChanges = 500

// maxValueBytes bounds how much of one old/new value is kept, measured as
// the length of its JSON encoding. A diff is read next to the field that
// produced it, not as a second copy of the manifest, and a value inside a
// diff can be sensitive even for an object whose *kind* is not redacted — a
// ConfigMap can hold a certificate, a Deployment's env can hold an inlined
// token. A size cap alone does not make that safe (a short secret is still a
// secret), so callers that store this in a place with looser access than the
// object itself — the audit trail, chiefly — are expected to apply the same
// kind-level redaction this package's caller already has, before diffing.
// This cap exists for a different, narrower reason: to keep one enormous
// value (a base64 blob, a rendered certificate bundle) from making the row
// unbounded, not to make an unredacted value safe to store.
const maxValueBytes = 4096

// ChangeKind is what happened at one path.
type ChangeKind string

const (
	Added   ChangeKind = "added"
	Removed ChangeKind = "removed"
	Changed ChangeKind = "changed"
)

// Change is one field-level difference between two decoded objects.
type Change struct {
	// Path is a dotted, ordinal path to the field — e.g.
	// "spec.template.spec.containers[0].image". It is built rather than
	// escaped, the same rule the rest of this codebase applies to anything
	// assembled for a machine to read back, because a map key can legally
	// contain a dot and the path is for a person, not a query language.
	Path string     `json:"path"`
	Kind ChangeKind `json:"kind"`
	// Old is absent for Added, New is absent for Removed. Both are the
	// decoded value, not a re-encoded string, so a client can render a
	// number as a number — except when Truncated is true, in which case the
	// value has been reduced to its JSON string form and cut to
	// maxValueBytes, because keeping a decoded shape only makes sense for a
	// value cheap enough not to need cutting in the first place.
	Old any `json:"old,omitempty"`
	New any `json:"new,omitempty"`
	// Truncated marks a Change whose Old and/or New were shortened to fit
	// maxValueBytes. It is per-change, not just on the Result, because a diff
	// that is otherwise complete but has quietly clipped one giant value is a
	// different fact from a diff that dropped changes altogether.
	Truncated bool `json:"truncated,omitempty"`
}

// Result is a complete diff between two decoded objects: stable, ordered and
// small enough to serialise into a JSON response or a database row without
// further processing.
type Result struct {
	Changes []Change `json:"changes"`
	// Truncated means the object carried more differences than maxChanges;
	// what is here is a prefix of the real diff, in the same order it would
	// have continued in, not a sample of it.
	Truncated bool `json:"truncated"`
}

// Diff compares two decoded objects and returns their structural difference.
// Either argument may be nil, a map[string]any, a []any, or a JSON scalar —
// whatever `encoding/json.Unmarshal` into `any` produces.
//
// Ordering is deterministic without a separate sort pass: a map's keys are
// visited in sorted order and the walk is depth-first, so two calls over the
// same pair of objects always produce the same Changes slice in the same
// order, which is required of anything that is both rendered to a person and
// persisted for later comparison.
func Diff(before, after any) Result {
	var out Result
	walk("", before, after, &out)
	return out
}

func walk(path string, before, after any, out *Result) {
	if out.Truncated {
		return
	}
	if reflect.DeepEqual(before, after) {
		return
	}

	beforeMap, beforeIsMap := before.(map[string]any)
	afterMap, afterIsMap := after.(map[string]any)
	if beforeIsMap && afterIsMap {
		walkMap(path, beforeMap, afterMap, out)
		return
	}

	beforeSlice, beforeIsSlice := before.([]any)
	afterSlice, afterIsSlice := after.([]any)
	if beforeIsSlice && afterIsSlice {
		walkSlice(path, beforeSlice, afterSlice, out)
		return
	}

	// Different shapes (map vs. slice vs. scalar, or one side nil) or two
	// unequal scalars: this path is one leaf change, whatever depth it sits
	// at. Reporting the whole subtree here rather than recursing into a
	// mismatched shape is deliberate — there is no meaningful per-field diff
	// between a string and a map, so the honest answer is "this changed",
	// not a fabricated set of nested adds and removes.
	emit(path, before, after, out)
}

func walkMap(path string, before, after map[string]any, out *Result) {
	keys := make(map[string]struct{}, len(before)+len(after))
	for k := range before {
		keys[k] = struct{}{}
	}
	for k := range after {
		keys[k] = struct{}{}
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)

	for _, k := range sorted {
		if out.Truncated {
			return
		}
		childPath := joinPath(path, k)
		bv, bok := before[k]
		av, aok := after[k]
		switch {
		case !bok:
			emit(childPath, nil, av, out)
		case !aok:
			emit(childPath, bv, nil, out)
		default:
			walk(childPath, bv, av, out)
		}
	}
}

func walkSlice(path string, before, after []any, out *Result) {
	n := len(before)
	if len(after) > n {
		n = len(after)
	}
	for i := 0; i < n; i++ {
		if out.Truncated {
			return
		}
		childPath := fmt.Sprintf("%s[%d]", path, i)
		switch {
		case i >= len(before):
			emit(childPath, nil, after[i], out)
		case i >= len(after):
			emit(childPath, before[i], nil, out)
		default:
			walk(childPath, before[i], after[i], out)
		}
	}
}

func joinPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func emit(path string, before, after any, out *Result) {
	if len(out.Changes) >= maxChanges {
		out.Truncated = true
		return
	}

	kind := Changed
	switch {
	case before == nil:
		kind = Added
	case after == nil:
		kind = Removed
	}

	oldValue, oldClipped := clip(before)
	newValue, newClipped := clip(after)
	change := Change{
		Path:      path,
		Kind:      kind,
		Old:       oldValue,
		New:       newValue,
		Truncated: oldClipped || newClipped,
	}
	out.Changes = append(out.Changes, change)
}

// clip returns a value fit to carry on the wire — the value itself if its
// JSON encoding is within maxValueBytes, or a truncated JSON string standing
// in for it otherwise — plus whether it actually shortened v. nil passes
// through unchanged: Added and Removed leave the absent side nil rather than
// clipping "null".
func clip(v any) (any, bool) {
	if v == nil {
		return nil, false
	}
	encoded, err := json.Marshal(v)
	if err != nil || len(encoded) <= maxValueBytes {
		return v, false
	}
	s := string(encoded)
	if maxValueBytes > 3 {
		s = s[:maxValueBytes-3] + "..."
	} else {
		s = s[:maxValueBytes]
	}
	return s, true
}

// PathHasPrefix reports whether a Change's Path is at or under a dotted
// prefix (e.g. "spec.template" matches "spec.template.spec.containers[0]").
// It is the one lookup the manifest editor's guardrail explanation needs —
// finding which Change, if any, sits under the field a policy named — kept
// here rather than duplicated at each call site since both the editor and,
// later, drift detection need to ask "which of these changes is this field
// or under it".
func PathHasPrefix(changePath, prefix string) bool {
	if prefix == "" {
		return true
	}
	if changePath == prefix {
		return true
	}
	return strings.HasPrefix(changePath, prefix+".") || strings.HasPrefix(changePath, prefix+"[")
}
