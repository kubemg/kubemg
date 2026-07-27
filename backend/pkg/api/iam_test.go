package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

type userListResponse struct {
	Users []userResponse `json:"users"`
}

type groupListResponse struct {
	Groups []groupResponse `json:"groups"`
}

type permissionListResponse struct {
	UserPermissions  []permissionResponse `json:"user_permissions"`
	GroupPermissions []permissionResponse `json:"group_permissions"`
}

func validUserPayload() map[string]any {
	return map[string]any{
		"username":    "devops",
		"email":       "devops@example.com",
		"password":    "correct-horse",
		"system_role": db.SystemRoleUser,
	}
}

func TestListUsersRequiresAdmin(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("devops", "pw", db.RoleUser)

	rec := env.do(t, http.MethodGet, "/api/v1/users", env.tokenFor(t, user), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d", http.StatusForbidden, rec.Code)
	}
}

func TestListUsersNeverLeaksPasswordHashes(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	env.store.addUser("devops", "pw", db.RoleUser)

	rec := env.do(t, http.MethodGet, "/api/v1/users", env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}

	body := decode[userListResponse](t, rec)
	if len(body.Users) != 2 {
		t.Fatalf("expected 2 users, got %d", len(body.Users))
	}
	if got := rec.Body.String(); strings.Contains(got, "password") || strings.Contains(got, "$2a$") {
		t.Fatalf("user list leaked credential material: %s", got)
	}
}

func TestCreateUser(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)

	rec := env.do(t, http.MethodPost, "/api/v1/users", env.tokenFor(t, admin), validUserPayload())
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusCreated, rec.Code, rec.Body.String())
	}

	body := decode[userResponse](t, rec)
	if body.ID == 0 {
		t.Fatal("expected a persisted user id")
	}
	if !body.IsActive {
		t.Fatal("a new user must start active")
	}
	if body.SystemRole != db.SystemRoleUser || body.Role != db.RoleUser {
		t.Fatalf("unexpected roles: system=%q legacy=%q", body.SystemRole, body.Role)
	}
}

func TestCreateUserRejectsShortPassword(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)

	payload := validUserPayload()
	payload["password"] = "short"

	rec := env.do(t, http.MethodPost, "/api/v1/users", env.tokenFor(t, admin), payload)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
	}
}

func TestCreateUserRejectsDuplicateUsername(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	env.store.addUser("devops", "pw", db.RoleUser)

	rec := env.do(t, http.MethodPost, "/api/v1/users", env.tokenFor(t, admin), validUserPayload())
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status %d, got %d", http.StatusConflict, rec.Code)
	}
}

func TestCreateSuperAdminRequiresSuperAdmin(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)

	payload := validUserPayload()
	payload["system_role"] = db.SystemRoleSuperAdmin

	rec := env.do(t, http.MethodPost, "/api/v1/users", env.tokenFor(t, admin), payload)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusForbidden, rec.Code, rec.Body.String())
	}
}

func TestUpdateUserChangesSystemRole(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	target := env.store.addUser("devops", "pw", db.RoleUser)

	rec := env.do(t, http.MethodPut, "/api/v1/users/"+itoa(target.ID), env.tokenFor(t, admin),
		map[string]any{"system_role": db.SystemRoleAdmin, "email": "ops@example.com"})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}

	body := decode[userResponse](t, rec)
	if body.SystemRole != db.SystemRoleAdmin {
		t.Fatalf("expected system role %q, got %q", db.SystemRoleAdmin, body.SystemRole)
	}
	// The coarse role the JWT carries must follow the system role.
	if body.Role != db.RoleAdmin {
		t.Fatalf("expected legacy role %q, got %q", db.RoleAdmin, body.Role)
	}
	if body.Email != "ops@example.com" {
		t.Fatalf("expected the email to be updated, got %q", body.Email)
	}
}

func TestUpdateUserCannotChangeOwnSystemRole(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	env.store.addUser("other-admin", "pw", db.RoleAdmin)

	rec := env.do(t, http.MethodPut, "/api/v1/users/"+itoa(admin.ID), env.tokenFor(t, admin),
		map[string]any{"system_role": db.SystemRoleUser})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusForbidden, rec.Code, rec.Body.String())
	}
}

// An admin can strip every other admin, but never itself, so an active admin
// always survives whatever an administrative session does.
func TestAdminCannotRemoveItself(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	other := env.store.addUser("other-admin", "pw", db.RoleAdmin)
	token := env.tokenFor(t, admin)

	rec := env.do(t, http.MethodDelete, "/api/v1/users/"+itoa(other.ID), token, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusNoContent, rec.Code, rec.Body.String())
	}

	rec = env.do(t, http.MethodDelete, "/api/v1/users/"+itoa(admin.ID), token, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected self-delete to be refused, got %d (%s)", rec.Code, rec.Body.String())
	}
	if _, ok := env.store.users[admin.ID]; !ok {
		t.Fatal("the caller's own account must survive")
	}
}

func TestSuperAdminIsProtectedFromPlainAdmins(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	root := env.store.addUser("root", "pw", db.RoleAdmin)
	root.SystemRole = db.SystemRoleSuperAdmin

	rec := env.do(t, http.MethodDelete, "/api/v1/users/"+itoa(root.ID), env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusForbidden, rec.Code, rec.Body.String())
	}

	rec = env.do(t, http.MethodPut, "/api/v1/users/"+itoa(root.ID), env.tokenFor(t, admin),
		map[string]any{"system_role": db.SystemRoleUser})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusForbidden, rec.Code, rec.Body.String())
	}
}

func TestSetUserStatusDisablesAccount(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	target := env.store.addUser("devops", "pw", db.RoleUser)

	rec := env.do(t, http.MethodPatch, "/api/v1/users/"+itoa(target.ID)+"/status",
		env.tokenFor(t, admin), map[string]any{"is_active": false})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	if decode[userResponse](t, rec).IsActive {
		t.Fatal("expected the account to be disabled")
	}

	// A disabled account must lose access immediately, not at token expiry.
	rec = env.do(t, http.MethodGet, "/api/v1/clusters", env.tokenFor(t, target), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected a disabled user to be refused, got %d", rec.Code)
	}
}

func TestSetUserStatusCannotDisableSelf(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	env.store.addUser("other-admin", "pw", db.RoleAdmin)

	rec := env.do(t, http.MethodPatch, "/api/v1/users/"+itoa(admin.ID)+"/status",
		env.tokenFor(t, admin), map[string]any{"is_active": false})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusForbidden, rec.Code, rec.Body.String())
	}
}

func TestLoginRefusesDisabledAccount(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("devops", "correct-horse", db.RoleUser)
	user.IsActive = false

	rec := env.do(t, http.MethodPost, "/api/v1/auth/login", "",
		map[string]string{"username": "devops", "password": "correct-horse"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusForbidden, rec.Code, rec.Body.String())
	}
}

func TestLoginRecordsLastLogin(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("devops", "correct-horse", db.RoleUser)

	rec := env.do(t, http.MethodPost, "/api/v1/auth/login", "",
		map[string]string{"username": "devops", "password": "correct-horse"})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	if env.store.users[user.ID].LastLoginAt == nil {
		t.Fatal("expected the sign-in to be stamped on the account")
	}
}

func TestDeleteUserDropsGrantsAndMemberships(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	target := env.store.addUser("devops", "pw", db.RoleUser)
	cluster := env.store.addCluster("dev-eu", db.EnvDev)
	group := env.store.addGroup("platform")
	env.store.grant(target.ID, cluster.ID, db.K8sRoleEdit, nil)
	_ = env.store.AddGroupMember(context.Background(), group.ID, target.ID)

	rec := env.do(t, http.MethodDelete, "/api/v1/users/"+itoa(target.ID), env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusNoContent, rec.Code, rec.Body.String())
	}
	if _, ok := env.store.access[target.ID]; ok {
		t.Fatal("expected the user's cluster grants to be removed")
	}
	if env.store.members[group.ID][target.ID] {
		t.Fatal("expected the user's group membership to be removed")
	}
}

func TestCreateGroupAndManageMembers(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	member := env.store.addUser("devops", "pw", db.RoleUser)
	token := env.tokenFor(t, admin)

	rec := env.do(t, http.MethodPost, "/api/v1/groups", token,
		map[string]string{"name": "platform", "description": "Platform engineering"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusCreated, rec.Code, rec.Body.String())
	}
	group := decode[groupResponse](t, rec)

	rec = env.do(t, http.MethodPost, "/api/v1/groups/"+itoa(group.ID)+"/members", token,
		map[string]any{"user_id": member.ID})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusCreated, rec.Code, rec.Body.String())
	}

	rec = env.do(t, http.MethodGet, "/api/v1/groups", token, nil)
	groups := decode[groupListResponse](t, rec).Groups
	if len(groups) != 1 || len(groups[0].MemberIDs) != 1 || groups[0].MemberIDs[0] != member.ID {
		t.Fatalf("unexpected group listing: %+v", groups)
	}

	rec = env.do(t, http.MethodDelete,
		"/api/v1/groups/"+itoa(group.ID)+"/members/"+itoa(member.ID), token, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusNoContent, rec.Code, rec.Body.String())
	}
}

func TestCreateGroupRejectsDuplicateName(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	env.store.addGroup("platform")

	rec := env.do(t, http.MethodPost, "/api/v1/groups", env.tokenFor(t, admin),
		map[string]string{"name": "platform"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status %d, got %d", http.StatusConflict, rec.Code)
	}
}

func TestGroupRoutesRequireAdmin(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("devops", "pw", db.RoleUser)

	rec := env.do(t, http.MethodGet, "/api/v1/groups", env.tokenFor(t, user), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d", http.StatusForbidden, rec.Code)
	}
}

func TestAssignAndRevokeUserPermission(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	target := env.store.addUser("devops", "pw", db.RoleUser)
	cluster := env.store.addCluster("dev-eu", db.EnvDev)
	token := env.tokenFor(t, admin)

	rec := env.do(t, http.MethodPost, "/api/v1/permissions/assign", token, map[string]any{
		"subject_type": "user",
		"subject_id":   target.ID,
		"cluster_id":   cluster.ID,
		"k8s_role":     db.K8sRoleEdit,
		"namespaces":   []string{"team-a", "team-b"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}

	body := decode[permissionResponse](t, rec)
	if body.SubjectName != "devops" || body.ClusterName != "dev-eu" {
		t.Fatalf("expected resolved display names, got %+v", body)
	}
	if len(body.Namespaces) != 2 {
		t.Fatalf("expected 2 namespaces, got %v", body.Namespaces)
	}

	// The grant must actually reach the user's cluster list.
	rec = env.do(t, http.MethodGet, "/api/v1/clusters", env.tokenFor(t, target), nil)
	clusters := decode[clusterListResponse](t, rec).Clusters
	if len(clusters) != 1 || clusters[0].K8sRole != db.K8sRoleEdit {
		t.Fatalf("expected the grant to be effective, got %+v", clusters)
	}

	rec = env.do(t, http.MethodPost, "/api/v1/permissions/revoke", token, map[string]any{
		"subject_type": "user",
		"subject_id":   target.ID,
		"cluster_id":   cluster.ID,
	})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusNoContent, rec.Code, rec.Body.String())
	}

	rec = env.do(t, http.MethodGet, "/api/v1/clusters", env.tokenFor(t, target), nil)
	if len(decode[clusterListResponse](t, rec).Clusters) != 0 {
		t.Fatal("expected the revoked cluster to disappear from the user's list")
	}
}

func TestGroupPermissionIsInheritedByMembers(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	member := env.store.addUser("devops", "pw", db.RoleUser)
	cluster := env.store.addCluster("dev-eu", db.EnvDev)
	group := env.store.addGroup("platform")
	token := env.tokenFor(t, admin)

	if err := env.store.AddGroupMember(context.Background(), group.ID, member.ID); err != nil {
		t.Fatalf("add group member: %v", err)
	}

	rec := env.do(t, http.MethodPost, "/api/v1/permissions/assign", token, map[string]any{
		"subject_type": "group",
		"subject_id":   group.ID,
		"cluster_id":   cluster.ID,
		"k8s_role":     db.K8sRoleView,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}

	rec = env.do(t, http.MethodGet, "/api/v1/clusters", env.tokenFor(t, member), nil)
	clusters := decode[clusterListResponse](t, rec).Clusters
	if len(clusters) != 1 || clusters[0].K8sRole != db.K8sRoleView {
		t.Fatalf("expected the member to inherit the group grant, got %+v", clusters)
	}
}

func TestDirectGrantWinsOverWeakerGroupGrant(t *testing.T) {
	env := newTestEnv(t)
	member := env.store.addUser("devops", "pw", db.RoleUser)
	cluster := env.store.addCluster("dev-eu", db.EnvDev)
	group := env.store.addGroup("platform")

	env.store.grant(member.ID, cluster.ID, db.K8sRoleEdit, []string{"team-a"})
	if err := env.store.AddGroupMember(context.Background(), group.ID, member.ID); err != nil {
		t.Fatalf("add group member: %v", err)
	}
	grant := db.GroupClusterAccess{GroupID: group.ID, ClusterID: cluster.ID, K8sRole: db.K8sRoleView}
	if err := env.store.AssignGroupAccess(context.Background(), &grant); err != nil {
		t.Fatalf("assign group access: %v", err)
	}

	rec := env.do(t, http.MethodGet, "/api/v1/clusters", env.tokenFor(t, member), nil)
	clusters := decode[clusterListResponse](t, rec).Clusters
	if len(clusters) != 1 || clusters[0].K8sRole != db.K8sRoleEdit {
		t.Fatalf("expected the stronger direct grant to win, got %+v", clusters)
	}
	// The group grant is unscoped, which means every namespace, so the merged
	// grant must not stay pinned to team-a.
	if len(clusters[0].Namespaces) != 0 {
		t.Fatalf("expected the unscoped grant to widen the scope, got %v", clusters[0].Namespaces)
	}
}

func TestAssignPermissionRejectsUnknownRole(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	target := env.store.addUser("devops", "pw", db.RoleUser)
	cluster := env.store.addCluster("dev-eu", db.EnvDev)

	rec := env.do(t, http.MethodPost, "/api/v1/permissions/assign", env.tokenFor(t, admin), map[string]any{
		"subject_type": "user",
		"subject_id":   target.ID,
		"cluster_id":   cluster.ID,
		"k8s_role":     "root",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
	}
}

func TestAssignPermissionRejectsUnknownCluster(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	target := env.store.addUser("devops", "pw", db.RoleUser)

	rec := env.do(t, http.MethodPost, "/api/v1/permissions/assign", env.tokenFor(t, admin), map[string]any{
		"subject_type": "user",
		"subject_id":   target.ID,
		"cluster_id":   4242,
		"k8s_role":     db.K8sRoleView,
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, rec.Code)
	}
}

func TestListPermissionsSplitsUsersAndGroups(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	target := env.store.addUser("devops", "pw", db.RoleUser)
	cluster := env.store.addCluster("dev-eu", db.EnvDev)
	group := env.store.addGroup("platform")

	env.store.grant(target.ID, cluster.ID, db.K8sRoleEdit, []string{"team-a"})
	grant := db.GroupClusterAccess{GroupID: group.ID, ClusterID: cluster.ID, K8sRole: db.K8sRoleView}
	if err := env.store.AssignGroupAccess(context.Background(), &grant); err != nil {
		t.Fatalf("assign group access: %v", err)
	}

	rec := env.do(t, http.MethodGet, "/api/v1/permissions", env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}

	body := decode[permissionListResponse](t, rec)
	if len(body.UserPermissions) != 1 || body.UserPermissions[0].SubjectName != "devops" {
		t.Fatalf("unexpected user permissions: %+v", body.UserPermissions)
	}
	if len(body.GroupPermissions) != 1 || body.GroupPermissions[0].SubjectName != "platform" {
		t.Fatalf("unexpected group permissions: %+v", body.GroupPermissions)
	}
}

func TestPermissionRoutesRequireAdmin(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("devops", "pw", db.RoleUser)

	rec := env.do(t, http.MethodGet, "/api/v1/permissions", env.tokenFor(t, user), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d", http.StatusForbidden, rec.Code)
	}
}
