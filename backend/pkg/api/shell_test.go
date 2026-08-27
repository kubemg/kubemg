package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/auth"
	"github.com/kubemg/kubemg/backend/pkg/bastion"
	"github.com/kubemg/kubemg/backend/pkg/db"
	"github.com/kubemg/kubemg/backend/pkg/shell"
)

/*
 * The browser shell's refusals and its disclosure.
 *
 * The interesting half of this feature is what it says before it does anything:
 * whether it is switched on, whether this cluster can carry one, what the shell
 * will be able to reach, and how long it lives. Every one of those is decided
 * before a tunnel is involved, which is why they belong here rather than in a
 * browser session run once.
 */

// shellEnv is a test server with the shell wired on. The default harness leaves
// it off — there is no image — which is itself one of the cases below.
func shellEnv(t *testing.T) *testEnv {
	t.Helper()
	return newTestEnvWith(t, func(opts *Options) {
		opts.ShellEnabled = true
		opts.ShellImage = "ghcr.io/kubemg/kubemg-shell:test"
	})
}

func shellPath(clusterID uint) string { return "/api/v1/clusters/" + itoa(clusterID) + "/shell" }

// A server started with no shell image cannot run one, and says so rather than
// answering 404 — a missing feature and a switched-off one are indistinguishable
// from a browser otherwise.
func TestShellReportsItselfSwitchedOffWithoutAnImage(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("ada", "pw", db.RoleUser)
	cluster := env.store.addAgentCluster("prod-eu", db.EnvProd, "token")
	env.store.grant(user.ID, cluster.ID, db.K8sRoleEdit, nil)

	rec := env.do(t, http.MethodGet, shellPath(cluster.ID), env.tokenFor(t, user), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
	}
	body := decode[shellResponse](t, rec)
	if body.Enabled {
		t.Fatal("a server with no shell image must not report the shell as enabled")
	}
	if body.Reason == "" {
		t.Fatal("a switched-off shell has to say so")
	}
}

// Direct mode has no proxy route for the pod to dial back through, so the shell
// is refused on that cluster with the reason named — not silently absent.
func TestShellRefusesADirectlyConnectedCluster(t *testing.T) {
	env := shellEnv(t)
	user := env.store.addUser("ada", "pw", db.RoleUser)
	cluster := env.store.addCluster("prod-eu", db.EnvProd)
	env.store.grant(user.ID, cluster.ID, db.K8sRoleEdit, nil)

	rec := env.do(t, http.MethodGet, shellPath(cluster.ID), env.tokenFor(t, user), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
	}
	body := decode[shellResponse](t, rec)
	if !body.Enabled {
		t.Fatal("the feature is on; it is this cluster that cannot carry a shell")
	}
	if body.Available {
		t.Fatal("a directly-connected cluster cannot carry a shell")
	}
	if body.Reason == "" {
		t.Fatal("the refusal has to explain itself")
	}

	// And the write refuses outright rather than reporting a state.
	rec = env.do(t, http.MethodPost, shellPath(cluster.ID), env.tokenFor(t, user), nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("start status = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
}

// The surface is open to anyone the cluster is granted to — the pod holds no
// credential of its own, so a `view` grant gets a terminal that can read and
// nothing more. Somebody with no grant at all still gets nothing.
func TestShellIsOpenToAGrantAndClosedWithoutOne(t *testing.T) {
	env := shellEnv(t)
	viewer := env.store.addUser("ada", "pw", db.RoleUser)
	stranger := env.store.addUser("mal", "pw", db.RoleUser)
	cluster := env.store.addAgentCluster("prod-eu", db.EnvProd, "token")
	env.store.grant(viewer.ID, cluster.ID, db.K8sRoleView, []string{"payments"})

	rec := env.do(t, http.MethodGet, shellPath(cluster.ID), env.tokenFor(t, viewer), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("a granted read-only user must be offered a shell, got %d (%s)", rec.Code, rec.Body.String())
	}
	body := decode[shellResponse](t, rec)
	if !body.Enabled {
		t.Fatal("the feature is on for a granted user")
	}

	// The disclosure says what the credential inside can do, which is the
	// caller's own grant and nothing else.
	if body.K8sRole != db.K8sRoleView {
		t.Fatalf("k8s_role = %q, want the caller's own role", body.K8sRole)
	}
	if len(body.Namespaces) != 1 || body.Namespaces[0] != "payments" {
		t.Fatalf("namespaces = %v, want the caller's scope", body.Namespaces)
	}
	// kubectl opens on a namespace the caller can actually read.
	if body.KubeNamespace != "payments" {
		t.Fatalf("kube_namespace = %q, want the granted namespace", body.KubeNamespace)
	}

	rec = env.do(t, http.MethodGet, shellPath(cluster.ID), env.tokenFor(t, stranger), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status for an ungranted user = %d, want 403", rec.Code)
	}
}

// The two clocks are reported, because an operator who knows only the idle one
// is surprised by the other.
func TestShellReportsBothOfItsClocks(t *testing.T) {
	env := shellEnv(t)
	user := env.store.addUser("ada", "pw", db.RoleUser)
	cluster := env.store.addAgentCluster("prod-eu", db.EnvProd, "token")
	env.store.grant(user.ID, cluster.ID, db.K8sRoleEdit, nil)

	// The clocks are disclosed whether or not a shell could start this second:
	// the agent being away does not change how long one would live.
	body := decode[shellResponse](t, env.do(t, http.MethodGet, shellPath(cluster.ID), env.tokenFor(t, user), nil))
	if body.IdleTimeoutSeconds != int64(shell.DefaultIdleTimeout.Seconds()) {
		t.Fatalf("idle timeout = %d, want the default hour", body.IdleTimeoutSeconds)
	}
	if body.MaxLifetimeSeconds != int64(shell.DefaultMaxLifetime.Seconds()) {
		t.Fatalf("max lifetime = %d, want the default eight hours", body.MaxLifetimeSeconds)
	}
}

// A shell must never outlive the credential inside it: a terminal whose kubectl
// stopped working an hour ago looks alive and answers nothing. So the pod's
// deadline is clamped to whatever ceiling this install puts on a kubeconfig.
func TestShellLifetimeIsClampedToTheKubeconfigCeiling(t *testing.T) {
	env := shellEnv(t)
	admin := env.store.addUser("root", "pw", db.RoleAdmin)
	user := env.store.addUser("ada", "pw", db.RoleUser)
	cluster := env.store.addAgentCluster("prod-eu", db.EnvProd, "token")
	env.store.grant(user.ID, cluster.ID, db.K8sRoleEdit, nil)

	rec := env.do(t, http.MethodPut, "/api/v1/settings", env.tokenFor(t, admin),
		map[string]any{"kubeconfig_max_ttl_hours": 2})
	if rec.Code != http.StatusOK {
		t.Fatalf("settings status = %d (%s)", rec.Code, rec.Body.String())
	}

	body := decode[shellResponse](t, env.do(t, http.MethodGet, shellPath(cluster.ID), env.tokenFor(t, user), nil))
	if body.MaxLifetimeSeconds != int64(2*time.Hour/time.Second) {
		t.Fatalf("max lifetime = %d, want it clamped to the two-hour kubeconfig ceiling", body.MaxLifetimeSeconds)
	}
}

// An agent that is away is reported as "not right now" rather than as a failed
// read: the page still has to be able to say what a shell here would be.
func TestShellReadSurvivesAnAgentThatIsAway(t *testing.T) {
	env := shellEnv(t)
	user := env.store.addUser("ada", "pw", db.RoleUser)
	cluster := env.store.addAgentCluster("prod-eu", db.EnvProd, "token")
	env.store.grant(user.ID, cluster.ID, db.K8sRoleEdit, nil)

	rec := env.do(t, http.MethodGet, shellPath(cluster.ID), env.tokenFor(t, user), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want the read to answer rather than fail (%s)", rec.Code, rec.Body.String())
	}
	body := decode[shellResponse](t, rec)
	if body.Available {
		t.Fatal("no tunnel means no shell can be started right now")
	}
	if body.Reason == "" {
		t.Fatal("the reason has to name the tunnel")
	}
	if body.MaxLifetimeSeconds == 0 || body.K8sRole == "" {
		t.Fatalf("the disclosure was dropped with the failed read: %+v", body)
	}
}

// A start on a cluster whose agent is not attached fails on the tunnel rather
// than on anything the shell invented — and says which.
func TestShellStartFailsWithoutATunnel(t *testing.T) {
	env := shellEnv(t)
	user := env.store.addUser("ada", "pw", db.RoleUser)
	cluster := env.store.addAgentCluster("prod-eu", db.EnvProd, "token")
	env.store.grant(user.ID, cluster.ID, db.K8sRoleEdit, nil)

	rec := env.do(t, http.MethodPost, shellPath(cluster.ID), env.tokenFor(t, user), nil)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (%s)", rec.Code, rec.Body.String())
	}
}

// The lifecycle is KubeMG's own act, under an identity of its own — a read-only
// operator cannot create a pod and must not have to.
func TestShellRunnerIsKubeMGsOwnIdentity(t *testing.T) {
	user, grant := shellRunner(9)
	if user.Username != shell.RunnerUser {
		t.Fatalf("runner = %q, want KubeMG's own name", user.Username)
	}
	if grant.K8sRole != db.K8sRoleView {
		t.Fatalf("runner role = %q, want a read-only baseline — its pod verbs come from a Role bound in one namespace",
			grant.K8sRole)
	}
	if grant.ClusterID != 9 {
		t.Fatalf("runner grant is for cluster %d", grant.ClusterID)
	}
	groups := shellRunnerGroups()
	for _, group := range groups {
		if group == bastion.GroupPrefix+db.K8sRoleClusterAdmin {
			t.Fatalf("groups = %v, the shell runner must never carry cluster-admin", groups)
		}
	}
}

// An unscoped grant opens kubectl on `default`; a scoped one opens on the first
// namespace it covers, so the shell does not greet its operator with a refusal.
func TestShellKubeNamespaceFollowsTheGrant(t *testing.T) {
	if got := shellKubeNamespace(db.UserClusterAccess{}); got != "default" {
		t.Fatalf("unscoped namespace = %q, want default", got)
	}
	scoped := db.UserClusterAccess{Namespaces: "payments,shop"}
	if got := shellKubeNamespace(scoped); got != "payments" {
		t.Fatalf("scoped namespace = %q, want the first granted one", got)
	}
}

// The routes exist only where there is a proxy to reach a cluster through, and
// attach is a GET so a browser can open it as a WebSocket.
func TestRouterRegistersTheShell(t *testing.T) {
	router := NewRouter(Options{
		Store:        &db.Store{},
		JWT:          auth.NewManager("secret", time.Hour),
		Proxy:        bastion.NewProxy(bastion.ProxyOptions{}),
		ShellEnabled: true,
		ShellImage:   "ghcr.io/kubemg/kubemg-shell:test",
	})

	found := make(map[string]bool)
	for _, route := range router.Routes() {
		found[route.Method+" "+route.Path] = true
	}
	for _, want := range []string{
		"GET /api/v1/clusters/:id/shell",
		"POST /api/v1/clusters/:id/shell",
		"DELETE /api/v1/clusters/:id/shell",
		"GET /api/v1/clusters/:id/shell/attach",
	} {
		if !found[want] {
			t.Fatalf("route %s is not registered", want)
		}
	}
}

// Without a proxy there is no tunnel and so no shell; the routes are not
// registered at all rather than answering an error nobody can act on.
func TestRouterOmitsTheShellWithoutAProxy(t *testing.T) {
	router := NewRouter(Options{
		Store:        &db.Store{},
		JWT:          auth.NewManager("secret", time.Hour),
		ShellEnabled: true,
		ShellImage:   "ghcr.io/kubemg/kubemg-shell:test",
	})
	for _, route := range router.Routes() {
		if route.Path == "/api/v1/clusters/:id/shell" {
			t.Fatal("the shell must not be registered without a proxy")
		}
	}
}

// Ending a shell ends what was inside it. The pod is gone either way, but the
// token it held stays signed for hours, and a credential the register calls live
// while nothing holds it is the state the register exists to stop.
func TestEndingAShellWithdrawsItsCredential(t *testing.T) {
	store := newFakeStore()
	holder := store.addUser("ada", "pw", db.RoleUser)
	cluster := store.addAgentCluster("prod-eu", db.EnvProd, "token")

	issuance := &db.KubeconfigIssuance{
		TokenID:        "shell-token",
		UserID:         holder.ID,
		Username:       holder.Username,
		ClusterID:      cluster.ID,
		ConnectionMode: db.ModeAgent,
		Purpose:        db.KubeconfigPurposeShell,
		ExpiresAt:      time.Now().UTC().Add(8 * time.Hour),
	}
	if err := store.CreateKubeconfigIssuance(t.Context(), issuance); err != nil {
		t.Fatalf("seed the register: %v", err)
	}

	s := &server{store: store}
	s.withdrawShellCredential(t.Context(), holder, shell.Status{CredentialID: issuance.ID})

	row, err := store.KubeconfigIssuanceByID(t.Context(), issuance.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !row.Revoked() {
		t.Fatal("the shell's credential is still live after the shell ended")
	}

	// A pod that was never seeded has nothing to withdraw, and must not turn
	// that into an error or a stray revoke.
	s.withdrawShellCredential(t.Context(), holder, shell.Status{})
}

/* -------------------------------------------------------------- settings --- */

// The lifetime settings are bounded at the edge rather than clamped three layers
// in, so an operator sees the refusal instead of a number the server is not
// using.
func TestShellSettingsRefuseAnImpossibleLifetime(t *testing.T) {
	env := shellEnv(t)
	admin := env.store.addUser("root", "pw", db.RoleAdmin)

	for name, payload := range map[string]map[string]any{
		"idle below the floor":    {"shell_idle_timeout_minutes": 1},
		"idle above the ceiling":  {"shell_idle_timeout_minutes": 60 * 48},
		"lifetime below floor":    {"shell_max_lifetime_hours": 0 - 1},
		"lifetime above ceiling":  {"shell_max_lifetime_hours": 24 * 30},
	} {
		rec := env.do(t, http.MethodPut, "/api/v1/settings", env.tokenFor(t, admin), payload)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400 (%s)", name, rec.Code, rec.Body.String())
		}
	}
}

// Zero clears an override back to the default, the rule every other numeric
// setting here follows.
func TestShellSettingsAcceptZeroAsCleared(t *testing.T) {
	env := shellEnv(t)
	admin := env.store.addUser("root", "pw", db.RoleAdmin)

	rec := env.do(t, http.MethodPut, "/api/v1/settings", env.tokenFor(t, admin),
		map[string]any{"shell_idle_timeout_minutes": 30})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
	}
	if got := decode[settingsResponse](t, rec).Effective.ShellIdleTimeoutMinutes; got != 30 {
		t.Fatalf("idle timeout = %d, want the stored override", got)
	}

	rec = env.do(t, http.MethodPut, "/api/v1/settings", env.tokenFor(t, admin),
		map[string]any{"shell_idle_timeout_minutes": 0})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
	}
	if got := decode[settingsResponse](t, rec).Effective.ShellIdleTimeoutMinutes; got != int(shell.DefaultIdleTimeout/time.Minute) {
		t.Fatalf("idle timeout = %d, want it cleared back to the default", got)
	}
}

// A settings row can switch the shell off. It cannot switch on an install that
// has no image to run — a switch that silently does nothing is worse than one
// that says why.
func TestShellSettingCanOnlyTurnTheShellOff(t *testing.T) {
	env := shellEnv(t)
	admin := env.store.addUser("root", "pw", db.RoleAdmin)

	rec := env.do(t, http.MethodPut, "/api/v1/settings", env.tokenFor(t, admin),
		map[string]any{"shell_enabled": false})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
	}
	if decode[settingsResponse](t, rec).Effective.ShellEnabled {
		t.Fatal("the shell was not switched off")
	}

	off := newTestEnv(t)
	offAdmin := off.store.addUser("root", "pw", db.RoleAdmin)
	rec = off.do(t, http.MethodPut, "/api/v1/settings", off.tokenFor(t, offAdmin),
		map[string]any{"shell_enabled": true})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
	}
	if decode[settingsResponse](t, rec).Effective.ShellEnabled {
		t.Fatal("a server with no shell image must not be talked into offering one")
	}
}

// A cluster attached before this feature existed refuses every shell, with an
// answer that is correct and unreadable: kubemg:shell-runner is not a name any
// operator has heard of, and not a permission they can grant from the console.
// The refusal has to name the actual fix.
func TestAStaleAgentRefusalNamesTheFix(t *testing.T) {
	message := staleManifestExplanation(
		`pods is forbidden: User "kubemg:shell-runner" cannot create resource "pods" in API group "" in the namespace "kubemg-system"`)

	// The cluster's own words survive: they are what an operator searches for.
	if !strings.Contains(message, "pods is forbidden") {
		t.Fatalf("message = %q, want the cluster's own answer kept", message)
	}
	if !strings.Contains(message, "Re-apply") || !strings.Contains(message, "kubemg-shell-runner") {
		t.Fatalf("message = %q, want it to name re-applying the agent manifests", message)
	}
}
