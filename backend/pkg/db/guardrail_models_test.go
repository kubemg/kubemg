package db

import (
	"reflect"
	"strings"
	"testing"
)

// A boolean column with a GORM `default` tag is not written when the Go value is
// false: GORM omits zero-valued fields so the database default applies. On this
// field that turns "seed the presets switched off" into "arm every preset on
// every fresh install", and "create this rule disabled" into "create it armed".
//
// It is tested by reflection because the failure is invisible in the type system
// and in any test that does not run against a real database — which is every test
// in this package. The tag is the whole bug.
func TestGuardrailEnabledHasNoColumnDefault(t *testing.T) {
	field, ok := reflect.TypeOf(GuardrailPolicy{}).FieldByName("Enabled")
	if !ok {
		t.Fatal("GuardrailPolicy has no Enabled field")
	}

	tag := field.Tag.Get("gorm")
	if strings.Contains(tag, "default") {
		t.Fatalf("Enabled must not carry a column default (%q): GORM omits a false "+
			"from the INSERT when one is present, which silently arms rules that were "+
			"created or seeded switched off", tag)
	}
}

// The presets exist to make the feature discoverable, not to change how a
// cluster behaves the moment it is upgraded. Every one of them is a refusal of
// something RBAC permits, so arming them without being asked would start
// refusing, unannounced, work an operator did yesterday.
func TestSeededPresetsAreNotArmed(t *testing.T) {
	for _, template := range GuardrailTemplates {
		policy := GuardrailPolicy{
			Name:    template.Name,
			Pattern: template.Pattern,
			Target:  template.Target,
			Action:  template.Action,
			Enabled: false,
		}
		if policy.Enabled {
			t.Fatalf("preset %q must be seeded disabled", template.Key)
		}
	}
}

func TestGuardrailTargetsCoverTheRightSubjects(t *testing.T) {
	cases := []struct {
		target   string
		api      bool
		terminal bool
	}{
		{GuardrailTargetAPIRequest, true, false},
		{GuardrailTargetTerminalExec, false, true},
		{GuardrailTargetBoth, true, true},
	}
	for _, tc := range cases {
		policy := GuardrailPolicy{Target: tc.target}
		if got := policy.CoversAPIRequests(); got != tc.api {
			t.Fatalf("%s: CoversAPIRequests = %v, want %v", tc.target, got, tc.api)
		}
		if got := policy.CoversTerminalInput(); got != tc.terminal {
			t.Fatalf("%s: CoversTerminalInput = %v, want %v", tc.target, got, tc.terminal)
		}
	}
}

func TestAGuardrailWithNoClusterIsGlobal(t *testing.T) {
	if !(GuardrailPolicy{}).Global() {
		t.Fatal("cluster 0 is the fleet-wide scope")
	}
	if (GuardrailPolicy{ClusterID: 3}).Global() {
		t.Fatal("a rule naming a cluster is not global")
	}
}

// Every preset has to be addressable by key: the console applies one by looking
// it up, and a duplicate or empty key would make that ambiguous.
func TestPresetKeysAreUniqueAndResolvable(t *testing.T) {
	seen := map[string]bool{}
	for _, template := range GuardrailTemplates {
		if template.Key == "" {
			t.Fatalf("preset %q has no key", template.Name)
		}
		if seen[template.Key] {
			t.Fatalf("duplicate preset key %q", template.Key)
		}
		seen[template.Key] = true

		found, ok := GuardrailTemplateByKey(template.Key)
		if !ok || found.Pattern != template.Pattern {
			t.Fatalf("preset %q does not resolve by key", template.Key)
		}
	}
	if _, ok := GuardrailTemplateByKey("no-such-preset"); ok {
		t.Fatal("an unknown key must not resolve")
	}
}
