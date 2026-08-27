package auditpolicy

import "testing"

func TestZeroPolicyRecordsEverything(t *testing.T) {
	// The most important property in this package: a server whose settings could
	// not be read must keep a complete trail, not a silent one.
	var policy *Policy
	if !policy.Records("list", 200, false, false) {
		t.Fatal("a nil policy must record everything")
	}
	if !policy.RecordSessions() {
		t.Fatal("a nil policy must record sessions")
	}
	if !New().Records("get", 200, false, false) {
		t.Fatal("a fresh policy must record everything")
	}
}

func TestSelectionSuppressesUnpickedVerbs(t *testing.T) {
	policy := New()
	policy.Store(NewSnapshot([]string{"create", "delete"}, true))

	if policy.Records("list", 200, false, false) {
		t.Fatal("list was not selected and should not be recorded")
	}
	if !policy.Records("delete", 200, false, false) {
		t.Fatal("delete was selected and must be recorded")
	}
}

func TestFloorSurvivesEverySelection(t *testing.T) {
	// An empty selection is the most restrictive input the settings layer can
	// produce, and the floor still has to hold: without it the control would be a
	// way to act with no trail at all.
	snapshot := NewSnapshot([]string{}, false)

	cases := []struct {
		name      string
		verb      string
		status    int
		failed    bool
		streaming bool
	}{
		{"a refusal", "list", 403, false, false},
		{"a server error", "get", 500, false, false},
		{"a call that never landed", "get", 0, true, false},
		{"an interactive session", "exec", 101, false, true},
		{"watching a recording", "replay", 200, false, false},
		{"destroying a recording", "recording-delete", 204, false, false},
		{"revealing a secret value", "secret-reveal", 200, false, false},
		{"a verb this build does not know", "impersonate", 200, false, false},
	}
	for _, tc := range cases {
		if !snapshot.Records(tc.verb, tc.status, tc.failed, tc.streaming) {
			t.Errorf("%s must always be recorded", tc.name)
		}
	}

	// And the thing the selection is actually for is still suppressed.
	if snapshot.Records("list", 200, false, false) {
		t.Fatal("a successful list should be suppressed by an empty selection")
	}
}

func TestEnabledVerbsReportsWhetherASelectionExists(t *testing.T) {
	if verbs := NewSnapshot(nil, true).EnabledVerbs(); verbs != nil {
		t.Fatalf("no selection should report nil, got %v", verbs)
	}

	verbs := NewSnapshot([]string{"Delete", " create ", "create"}, true).EnabledVerbs()
	if len(verbs) != 2 || verbs[0] != "create" || verbs[1] != "delete" {
		t.Fatalf("expected a normalised sorted pair, got %v", verbs)
	}
}

func TestSessionRecordingSwitch(t *testing.T) {
	policy := New()
	policy.Store(NewSnapshot(nil, false))
	if policy.RecordSessions() {
		t.Fatal("recording should be off")
	}
	policy.Store(NewSnapshot(nil, true))
	if !policy.RecordSessions() {
		t.Fatal("recording should be back on")
	}
}
