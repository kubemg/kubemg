package api

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/kubemg/kubemg/backend/pkg/bastion"
	"github.com/kubemg/kubemg/backend/pkg/db"
)

// grantTeam gives a group a role on a cluster and, when members are named,
// puts them in it.
func (f *fakeStore) grantTeam(group *db.Group, clusterID uint, role string, members ...*db.User) {
	if f.groupAccess[group.ID] == nil {
		f.groupAccess[group.ID] = map[uint]db.GroupClusterAccess{}
	}
	f.groupAccess[group.ID][clusterID] = db.GroupClusterAccess{GroupID: group.ID, ClusterID: clusterID, K8sRole: role}
	for _, member := range members {
		if f.members[group.ID] == nil {
			f.members[group.ID] = map[uint]bool{}
		}
		f.members[group.ID][member.ID] = true
	}
}

// The install package for a cluster names the teams granted *that* cluster, and
// the sheet says each one waits until the re-apply is confirmed. A team granted
// another cluster only, and a group whose name can never be asserted, are in
// neither the package nor the lists.
func TestClusterKustomizeListsTheClustersTeamGroups(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	cluster := env.store.addAgentCluster("edge-us", db.EnvStaging, "kmg_install-token")
	other := env.store.addAgentCluster("edge-eu", db.EnvStaging, "kmg_other-token")
	env.store.grantTeam(env.store.addGroup("payments"), cluster.ID, db.K8sRoleView)
	env.store.grantTeam(env.store.addGroup("platform"), other.ID, db.K8sRoleView)
	env.store.grantTeam(env.store.addGroup("Legacy Team"), cluster.ID, db.K8sRoleView)

	body := mintInstall(t, env, admin, cluster.ID)
	if !slices.Equal(body.TeamGroups, []string{"payments"}) {
		t.Fatalf("team_groups = %v, want only the team granted this cluster", body.TeamGroups)
	}
	if len(body.ConfirmedTeamGroups) != 0 || !slices.Equal(body.WaitingTeamGroups, []string{"payments"}) {
		t.Fatalf("confirmed = %v, waiting = %v, want payments waiting", body.ConfirmedTeamGroups, body.WaitingTeamGroups)
	}
	rbac := body.Files["rbac.yaml"]
	if !strings.Contains(rbac, `"kubemg:g:payments"`) {
		t.Fatal("the package does not let the agent assert the team granted this cluster")
	}
	if strings.Contains(rbac, "kubemg:g:platform") || strings.Contains(rbac, "Legacy") {
		t.Fatalf("the package names a team this cluster does not grant:\n%s", rbac)
	}
	if !strings.Contains(body.Manifest, `"kubemg:g:payments"`) {
		t.Fatal("the flat manifest must carry the same group list as the package")
	}
}

// A package rendered while the store cannot say which groups are granted would
// take back the agent's right to assert teams already confirmed. It is refused.
func TestClusterKustomizeFailsClosedWhenTeamGroupsCannotBeRead(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	cluster := env.store.addAgentCluster("edge-us", db.EnvStaging, "kmg_install-token")
	env.store.teamGroupsErr = errors.New("database is down")

	for _, path := range []string{"/kustomize", "/kustomize?format=yaml"} {
		rec := env.do(t, http.MethodGet, "/api/v1/clusters/"+itoa(cluster.ID)+path, env.tokenFor(t, admin), nil)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("%s: expected %d, got %d (%s)", path, http.StatusInternalServerError, rec.Code, rec.Body.String())
		}
	}
}

// Confirming the re-apply stores the teams the applied package names that are
// still granted here, records who confirmed what, and starts asserting them. A
// name no longer granted is dropped; a granted team the package did not carry
// keeps waiting.
func TestConfirmAgentTeamGroups(t *testing.T) {
	auditor := &recordingAuditor{}
	env := newTestEnvWith(t, func(o *Options) { o.Auditor = auditor })
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	cluster := env.store.addAgentCluster("edge-us", db.EnvStaging, "kmg_install-token")
	env.store.grantTeam(env.store.addGroup("payments"), cluster.ID, db.K8sRoleView)
	env.store.grantTeam(env.store.addGroup("platform"), cluster.ID, db.K8sRoleEdit)

	rec := env.do(t, http.MethodPost, "/api/v1/clusters/"+itoa(cluster.ID)+"/agent-team-groups/confirm",
		env.tokenFor(t, admin), map[string]any{"team_groups": []string{"payments", "ghost"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	body := decode[agentTeamGroupsResponse](t, rec)
	if !slices.Equal(body.ConfirmedTeamGroups, []string{"payments"}) {
		t.Fatalf("confirmed = %v, want payments", body.ConfirmedTeamGroups)
	}
	if !slices.Equal(body.WaitingTeamGroups, []string{"platform"}) {
		t.Fatalf("waiting = %v, want platform, which the applied package did not carry", body.WaitingTeamGroups)
	}
	if got := env.store.clusters[cluster.ID].AgentTeamGroups; got != "payments" {
		t.Fatalf("stored confirmation = %q, want payments", got)
	}

	var confirmed *bastion.Event
	for _, event := range auditor.all() {
		if event.Verb == bastion.VerbAgentTeamGroups {
			confirmed = &event
		}
	}
	if confirmed == nil {
		t.Fatal("the confirmation was not recorded in the audit trail")
	}
	if confirmed.Username != "admin" || confirmed.ClusterID != cluster.ID || confirmed.Name != "kubemg:g:payments" {
		t.Fatalf("the record must name who confirmed which groups on which cluster, got %+v", *confirmed)
	}
}

func TestConfirmAgentTeamGroupsRefusals(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	user := env.store.addUser("devops", "pw", db.RoleUser)
	cluster := env.store.addAgentCluster("edge-us", db.EnvStaging, "kmg_install-token")
	direct := env.store.addCluster("prod-eu", db.EnvProd)
	confirm := func(clusterID uint, token string, body any) int {
		return env.do(t, http.MethodPost, "/api/v1/clusters/"+itoa(clusterID)+"/agent-team-groups/confirm",
			token, body).Code
	}
	payload := map[string]any{"team_groups": []string{"payments"}}

	if code := confirm(cluster.ID, env.tokenFor(t, user), payload); code != http.StatusForbidden {
		t.Fatalf("a non-admin confirmed the agent's team groups: %d", code)
	}
	if code := confirm(direct.ID, env.tokenFor(t, admin), payload); code != http.StatusConflict {
		t.Fatalf("a direct-mode cluster has no agent to confirm, got %d", code)
	}
	bad := map[string]any{"team_groups": []string{"system:masters"}}
	if code := confirm(cluster.ID, env.tokenFor(t, admin), bad); code != http.StatusBadRequest {
		t.Fatalf("an invalid group name was accepted, got %d", code)
	}
	env.store.teamGroupsErr = errors.New("database is down")
	if code := confirm(cluster.ID, env.tokenFor(t, admin), payload); code != http.StatusInternalServerError {
		t.Fatalf("a confirmation that cannot be checked must be refused, got %d", code)
	}
	if got := env.store.clusters[cluster.ID].AgentTeamGroups; got != "" {
		t.Fatalf("a refused confirmation changed the stored list to %q", got)
	}
}

// A group's name is also the Kubernetes group it is asserted as, so one the
// cluster could not be told about safely is refused where it is created.
func TestCreateGroupRefusesANameClustersCannotCarry(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	token := env.tokenFor(t, admin)

	for _, name := range []string{"Platform", "platform team", "a:b", "-edge", strings.Repeat("a", 64)} {
		rec := env.do(t, http.MethodPost, "/api/v1/groups", token, map[string]string{"name": name})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("group %q: expected %d, got %d", name, http.StatusBadRequest, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "kubemg:g:") {
			t.Fatalf("group %q: the refusal must say why, got %s", name, rec.Body.String())
		}
	}

	rec := env.do(t, http.MethodPost, "/api/v1/groups", token, map[string]string{"name": "platform-eng"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected %d, got %d (%s)", http.StatusCreated, rec.Code, rec.Body.String())
	}
	if got := decode[groupResponse](t, rec).TeamGroup; got != "kubemg:g:platform-eng" {
		t.Fatalf("team_group = %q, want the group clusters will see", got)
	}
}

// A group created before the rule may carry a name that can never be asserted.
// The listing says so, by naming no team group for it, instead of leaving an
// operator to wait for a group the cluster is never sent.
func TestGroupListingFlagsANameThatCannotBeATeamGroup(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	env.store.addGroup("Legacy Team")
	env.store.addGroup("payments")

	rec := env.do(t, http.MethodGet, "/api/v1/groups", env.tokenFor(t, admin), nil)
	got := map[string]string{}
	for _, group := range decode[groupListResponse](t, rec).Groups {
		got[group.Name] = group.TeamGroup
	}
	if got["Legacy Team"] != "" || got["payments"] != "kubemg:g:payments" {
		t.Fatalf("team groups = %v, want none for the legacy name and kubemg:g:payments", got)
	}
}

// The identity page shows the confirmed team among the groups the cluster is
// told, and the unconfirmed one as withheld — the same split the proxy makes.
func TestGrantIdentityShowsAssertedAndWithheldTeamGroups(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("devops", "secret123", db.RoleUser)
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	cluster.AgentTeamGroups = "platform"
	env.store.grantTeam(env.store.addGroup("platform"), cluster.ID, db.K8sRoleView, user)
	env.store.grantTeam(env.store.addGroup("payments"), cluster.ID, db.K8sRoleView, user)

	rec := env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/access-review/identity", env.tokenFor(t, user), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	body := decode[struct {
		Groups   []string `json:"groups"`
		Withheld []string `json:"withheld_team_groups"`
	}](t, rec)
	want := []string{"kubemg:view", "kubemg:users", "kubemg:g:platform"}
	if !slices.Equal(body.Groups, want) {
		t.Fatalf("groups = %v, want %v", body.Groups, want)
	}
	if !slices.Equal(body.Withheld, []string{"kubemg:g:payments"}) {
		t.Fatalf("withheld = %v, want the unconfirmed team", body.Withheld)
	}
}
