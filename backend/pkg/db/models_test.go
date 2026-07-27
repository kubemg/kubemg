package db

import (
	"reflect"
	"testing"
)

func TestNamespaceList(t *testing.T) {
	tests := []struct {
		stored string
		want   []string
	}{
		{"", []string{}},
		{"   ", []string{}},
		{"team-a", []string{"team-a"}},
		{"team-a,team-b", []string{"team-a", "team-b"}},
		{" team-a , ,team-b ", []string{"team-a", "team-b"}},
	}

	for _, tc := range tests {
		got := UserClusterAccess{Namespaces: tc.stored}.NamespaceList()
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("NamespaceList(%q) = %v, want %v", tc.stored, got, tc.want)
		}
	}
}

func TestJoinNamespaces(t *testing.T) {
	tests := []struct {
		in   []string
		want string
	}{
		{nil, ""},
		{[]string{}, ""},
		{[]string{"team-a"}, "team-a"},
		{[]string{"team-a", "team-b"}, "team-a,team-b"},
		{[]string{" team-a ", "", "team-b"}, "team-a,team-b"},
	}

	for _, tc := range tests {
		if got := JoinNamespaces(tc.in); got != tc.want {
			t.Fatalf("JoinNamespaces(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNamespaceRoundTrip(t *testing.T) {
	want := []string{"team-a", "team-b"}
	access := UserClusterAccess{Namespaces: JoinNamespaces(want)}
	if got := access.NamespaceList(); !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip = %v, want %v", got, want)
	}
}

func TestTableNames(t *testing.T) {
	if got := (UserClusterAccess{}).TableName(); got != "user_cluster_access" {
		t.Fatalf("expected table \"user_cluster_access\", got %q", got)
	}
	if got := (GroupClusterAccess{}).TableName(); got != "group_cluster_access" {
		t.Fatalf("expected table \"group_cluster_access\", got %q", got)
	}
	if got := (UserGroup{}).TableName(); got != "user_groups" {
		t.Fatalf("expected table \"user_groups\", got %q", got)
	}
}

func TestNormalizeDerivesRoles(t *testing.T) {
	tests := []struct {
		in         User
		wantSystem string
		wantLegacy string
	}{
		// A row written before the IAM schema carries only the legacy role.
		{User{Role: RoleAdmin}, SystemRoleAdmin, RoleAdmin},
		{User{Role: RoleUser}, SystemRoleUser, RoleUser},
		{User{}, SystemRoleUser, RoleUser},
		{User{SystemRole: SystemRoleSuperAdmin}, SystemRoleSuperAdmin, RoleAdmin},
		// A system role the schema does not know falls back to the least
		// privileged option rather than being trusted.
		{User{SystemRole: "root", Role: RoleAdmin}, SystemRoleUser, RoleUser},
	}

	for _, tc := range tests {
		user := tc.in
		user.Normalize()
		if user.SystemRole != tc.wantSystem || user.Role != tc.wantLegacy {
			t.Fatalf("Normalize(%+v) = system %q legacy %q, want %q/%q",
				tc.in, user.SystemRole, user.Role, tc.wantSystem, tc.wantLegacy)
		}
	}
}

func TestMergeAccessPrefersTheStrongerGrant(t *testing.T) {
	view := UserClusterAccess{K8sRole: K8sRoleView, Namespaces: "team-a"}
	edit := UserClusterAccess{K8sRole: K8sRoleEdit, Namespaces: "team-b"}

	merged := MergeAccess(view, edit)
	if merged.K8sRole != K8sRoleEdit {
		t.Fatalf("expected %q to win, got %q", K8sRoleEdit, merged.K8sRole)
	}
	if merged.Namespaces != "team-a,team-b" {
		t.Fatalf("expected the namespace scopes to be unioned, got %q", merged.Namespaces)
	}
}

func TestMergeAccessUnscopedWins(t *testing.T) {
	scoped := UserClusterAccess{K8sRole: K8sRoleEdit, Namespaces: "team-a"}
	unscoped := UserClusterAccess{K8sRole: K8sRoleView}

	// An empty scope means every namespace, so it must absorb the narrower one.
	if got := MergeAccess(scoped, unscoped); got.Namespaces != "" {
		t.Fatalf("expected an unscoped merge, got %q", got.Namespaces)
	}
	if got := MergeAccess(unscoped, scoped); got.Namespaces != "" {
		t.Fatalf("expected an unscoped merge, got %q", got.Namespaces)
	}
}

func TestValidSystemRole(t *testing.T) {
	if !ValidSystemRole(SystemRoleSuperAdmin) || ValidSystemRole("root") {
		t.Fatal("unexpected system role validation")
	}
}

func TestIsAdmin(t *testing.T) {
	if !(User{Role: RoleAdmin}).IsAdmin() {
		t.Fatal("expected admin role to report IsAdmin")
	}
	if (User{Role: RoleUser}).IsAdmin() {
		t.Fatal("expected user role not to report IsAdmin")
	}
	if (User{}).IsAdmin() {
		t.Fatal("expected empty role not to report IsAdmin")
	}
}
