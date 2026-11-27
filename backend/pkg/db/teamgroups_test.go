package db

import (
	"slices"
	"strings"
	"testing"
)

// A team group name is what a cluster is told as kubemg:g:<name>, so it is the
// namespace rule: nothing that could open another kubemg: segment, differ by
// case alone, or need quoting in the agent's ClusterRole.
func TestValidTeamGroupName(t *testing.T) {
	for _, good := range []string{"a", "payments", "platform-eng", "team-1", strings.Repeat("a", 63)} {
		if !ValidTeamGroupName(good) {
			t.Errorf("%q refused", good)
		}
	}
	for _, bad := range []string{"", "Payments", "platform team", "a:b", "u:admin", "-edge", "edge-",
		"pay.ments", "pay_ments", strings.Repeat("a", 64)} {
		if ValidTeamGroupName(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}

// Grants merged from several groups carry every team once, sorted, whatever
// order the grants were read in — and merging never drops a team.
func TestMergeAccessUnionsTeamGroups(t *testing.T) {
	a := UserClusterAccess{K8sRole: K8sRoleView, TeamGroups: []string{"platform"}}
	b := UserClusterAccess{K8sRole: K8sRoleEdit, TeamGroups: []string{"payments", "platform"}}
	for _, merged := range []UserClusterAccess{MergeAccess(a, b), MergeAccess(b, a)} {
		if !slices.Equal(merged.TeamGroups, []string{"payments", "platform"}) {
			t.Fatalf("team groups = %v, want payments and platform once each", merged.TeamGroups)
		}
		if merged.K8sRole != K8sRoleEdit {
			t.Fatalf("role = %q, the stronger role should still win", merged.K8sRole)
		}
	}

	// A direct grant carries no team; merging it with one keeps the team.
	direct := UserClusterAccess{K8sRole: K8sRoleView}
	if got := MergeAccess(direct, a).TeamGroups; !slices.Equal(got, []string{"platform"}) {
		t.Fatalf("team groups = %v, want platform kept", got)
	}
	if got := MergeAccess(direct, direct).TeamGroups; got != nil {
		t.Fatalf("team groups = %v, want none from two direct grants", got)
	}
}

// Removing a grant forgets the group's confirmation on that cluster, and only
// that group's: the others stay asserted.
func TestWithoutTeamGroup(t *testing.T) {
	if got := WithoutTeamGroup("payments,platform", "payments"); got != "platform" {
		t.Fatalf("got %q, want platform", got)
	}
	if got := WithoutTeamGroup("payments", "payments"); got != "" {
		t.Fatalf("got %q, want nothing left", got)
	}
	// A name the list does not carry leaves it exactly as stored, so the caller
	// can tell there is nothing to write. A prefix of a name is not the name.
	for _, name := range []string{"platform", "pay"} {
		if got := WithoutTeamGroup("payments", name); got != "payments" {
			t.Fatalf("removing %q: got %q, want the list unchanged", name, got)
		}
	}
}
