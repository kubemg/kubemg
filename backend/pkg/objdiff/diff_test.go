package objdiff

import (
	"fmt"
	"testing"
)

func changeByPath(t *testing.T, result Result, path string) Change {
	t.Helper()
	for _, c := range result.Changes {
		if c.Path == path {
			return c
		}
	}
	t.Fatalf("no change at path %q, got %+v", path, result.Changes)
	return Change{}
}

func TestDiffNoDifference(t *testing.T) {
	a := map[string]any{"spec": map[string]any{"replicas": float64(3)}}
	b := map[string]any{"spec": map[string]any{"replicas": float64(3)}}
	result := Diff(a, b)
	if len(result.Changes) != 0 {
		t.Fatalf("expected no changes, got %+v", result.Changes)
	}
}

func TestDiffNestedMapChanged(t *testing.T) {
	before := map[string]any{
		"spec": map[string]any{
			"replicas": float64(2),
			"template": map[string]any{
				"spec": map[string]any{
					"containers": []any{
						map[string]any{"name": "app", "image": "app:1.0"},
					},
				},
			},
		},
	}
	after := map[string]any{
		"spec": map[string]any{
			"replicas": float64(3),
			"template": map[string]any{
				"spec": map[string]any{
					"containers": []any{
						map[string]any{"name": "app", "image": "app:1.1"},
					},
				},
			},
		},
	}
	result := Diff(before, after)
	if len(result.Changes) != 2 {
		t.Fatalf("expected 2 changes, got %+v", result.Changes)
	}
	replicas := changeByPath(t, result, "spec.replicas")
	if replicas.Kind != Changed || replicas.Old != float64(2) || replicas.New != float64(3) {
		t.Fatalf("unexpected replicas change: %+v", replicas)
	}
	image := changeByPath(t, result, "spec.template.spec.containers[0].image")
	if image.Kind != Changed || image.Old != "app:1.0" || image.New != "app:1.1" {
		t.Fatalf("unexpected image change: %+v", image)
	}
}

func TestDiffAddedAndRemovedKeys(t *testing.T) {
	before := map[string]any{"a": float64(1), "b": float64(2)}
	after := map[string]any{"a": float64(1), "c": float64(3)}
	result := Diff(before, after)
	if len(result.Changes) != 2 {
		t.Fatalf("expected 2 changes, got %+v", result.Changes)
	}
	// Deterministic ordering: map keys are visited sorted, so "b" (removed)
	// comes before "c" (added).
	if result.Changes[0].Path != "b" || result.Changes[0].Kind != Removed {
		t.Fatalf("expected b removed first, got %+v", result.Changes[0])
	}
	if result.Changes[1].Path != "c" || result.Changes[1].Kind != Added {
		t.Fatalf("expected c added second, got %+v", result.Changes[1])
	}
}

func TestDiffListLengthChange(t *testing.T) {
	before := map[string]any{"items": []any{"x", "y"}}
	after := map[string]any{"items": []any{"x", "y", "z"}}
	result := Diff(before, after)
	change := changeByPath(t, result, "items[2]")
	if change.Kind != Added || change.New != "z" {
		t.Fatalf("unexpected: %+v", change)
	}

	result2 := Diff(after, before)
	change2 := changeByPath(t, result2, "items[2]")
	if change2.Kind != Removed || change2.Old != "z" {
		t.Fatalf("unexpected: %+v", change2)
	}
}

func TestDiffOrderingIsStable(t *testing.T) {
	before := map[string]any{"z": float64(1), "a": float64(1), "m": float64(1)}
	after := map[string]any{"z": float64(2), "a": float64(2), "m": float64(2)}
	first := Diff(before, after)
	second := Diff(before, after)
	if len(first.Changes) != 3 || len(second.Changes) != 3 {
		t.Fatalf("expected 3 changes each run")
	}
	for i := range first.Changes {
		if first.Changes[i].Path != second.Changes[i].Path {
			t.Fatalf("ordering not stable across runs: %+v vs %+v", first.Changes, second.Changes)
		}
	}
	wantOrder := []string{"a", "m", "z"}
	for i, want := range wantOrder {
		if first.Changes[i].Path != want {
			t.Fatalf("expected sorted key order %v, got %+v", wantOrder, first.Changes)
		}
	}
}

func TestDiffMismatchedShapeIsOneLeafChange(t *testing.T) {
	before := map[string]any{"spec": map[string]any{"foo": "bar"}}
	after := map[string]any{"spec": "now a string"}
	result := Diff(before, after)
	if len(result.Changes) != 1 {
		t.Fatalf("expected exactly one leaf change for a shape mismatch, got %+v", result.Changes)
	}
	change := result.Changes[0]
	if change.Path != "spec" || change.Kind != Changed {
		t.Fatalf("unexpected: %+v", change)
	}
}

func TestDiffTruncatesAtCap(t *testing.T) {
	before := map[string]any{}
	after := map[string]any{}
	for i := 0; i < maxChanges+50; i++ {
		key := fmt.Sprintf("k%04d", i)
		before[key] = float64(i)
		after[key] = float64(i) + 1
	}
	result := Diff(before, after)
	if !result.Truncated {
		t.Fatalf("expected Truncated to be set")
	}
	if len(result.Changes) != maxChanges {
		t.Fatalf("expected exactly %d changes, got %d", maxChanges, len(result.Changes))
	}
}

func TestDiffClipsLargeValues(t *testing.T) {
	huge := make([]any, 0, maxValueBytes)
	for i := 0; i < maxValueBytes; i++ {
		huge = append(huge, "x")
	}
	before := map[string]any{"blob": "small"}
	after := map[string]any{"blob": huge}
	result := Diff(before, after)
	change := changeByPath(t, result, "blob")
	if !change.Truncated {
		t.Fatalf("expected the large value to be marked truncated: %+v", change)
	}
	s, ok := change.New.(string)
	if !ok || len(s) > maxValueBytes {
		t.Fatalf("expected a clipped string within the cap, got %T len=%v", change.New, change.New)
	}
}

func TestDiffAddedRemovedHaveOnlyOneSide(t *testing.T) {
	before := map[string]any{"a": float64(1)}
	after := map[string]any{"a": float64(1), "b": float64(2)}
	result := Diff(before, after)
	change := changeByPath(t, result, "b")
	if change.Old != nil {
		t.Fatalf("expected Added change to carry no Old value, got %+v", change.Old)
	}
	if change.New != float64(2) {
		t.Fatalf("unexpected New: %+v", change.New)
	}
}

func TestPathHasPrefix(t *testing.T) {
	cases := []struct {
		path, prefix string
		want         bool
	}{
		{"spec.template.spec.containers[0].image", "spec.template", true},
		{"spec.replicas", "spec.template", false},
		{"spec", "spec", true},
		{"spec.template", "", true},
		{"specification", "spec", false},
	}
	for _, tc := range cases {
		if got := PathHasPrefix(tc.path, tc.prefix); got != tc.want {
			t.Errorf("PathHasPrefix(%q, %q) = %v, want %v", tc.path, tc.prefix, got, tc.want)
		}
	}
}
