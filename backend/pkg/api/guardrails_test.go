package api

import (
	"net/http"
	"testing"

	"github.com/kubemg/kubemg/backend/pkg/bastion"
	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * The guardrail surface, and what it actually stops.
 *
 * The enforcement tests go through the real proxy route rather than calling the
 * engine, because the thing worth asserting is not that a regular expression
 * matches — that is covered in pkg/guardrails — but that a refusal happens
 * *before* the tunnel, on the path a kubectl call really takes. A cluster with no
 * agent attached answers 503, which is what makes it a usable control: a 503
 * proves the call got past the guardrails and reached the tunnel lookup, and a
 * 403 proves it did not.
 */

// createPolicy stores a rule through the API and returns it.
func createPolicy(t *testing.T, env *testEnv, token string, body map[string]any) guardrailPolicyResponse {
	t.Helper()
	rec := env.do(t, http.MethodPost, "/api/v1/guardrails", token, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected %d, got %d (%s)", http.StatusCreated, rec.Code, rec.Body.String())
	}
	return decode[guardrailPolicyResponse](t, rec)
}

func TestGuardrailsAreAdminOnly(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("devops", "secret123", db.RoleUser)
	token := env.tokenFor(t, user)

	rec := env.do(t, http.MethodGet, "/api/v1/guardrails", token, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected %d for a non-admin read, got %d", http.StatusForbidden, rec.Code)
	}

	// The one that matters: a non-admin must not be able to write a rule, and
	// least of all to delete one that is stopping them.
	rec = env.do(t, http.MethodPost, "/api/v1/guardrails", token, map[string]any{
		"name": "mine", "pattern": "^DELETE /api/v1/nodes/",
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected %d for a non-admin create, got %d", http.StatusForbidden, rec.Code)
	}

	admin := env.store.addUser("admin", "secret123", db.RoleAdmin)
	policy := createPolicy(t, env, env.tokenFor(t, admin), map[string]any{
		"name": "no node deletion", "pattern": "^DELETE /api/v1/nodes/",
	})

	rec = env.do(t, http.MethodDelete, "/api/v1/guardrails/"+itoa(policy.ID), token, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected %d for a non-admin delete, got %d", http.StatusForbidden, rec.Code)
	}
}

func TestGuardrailsRequireAuthentication(t *testing.T) {
	env := newTestEnv(t)

	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/guardrails"},
		{http.MethodGet, "/api/v1/guardrails/templates"},
		{http.MethodPost, "/api/v1/guardrails"},
		{http.MethodPut, "/api/v1/guardrails/1"},
		{http.MethodDelete, "/api/v1/guardrails/1"},
	} {
		rec := env.do(t, route.method, route.path, "", nil)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s: expected %d without a token, got %d",
				route.method, route.path, http.StatusUnauthorized, rec.Code)
		}
	}
}

func TestCreateGuardrailValidatesThePattern(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", db.RoleAdmin)
	token := env.tokenFor(t, admin)

	cases := map[string]map[string]any{
		"no name":            {"name": "  ", "pattern": "^DELETE /api/v1/nodes/"},
		"no pattern":         {"name": "x", "pattern": ""},
		"uncompilable":       {"name": "x", "pattern": "^DELETE /api/(v1"},
		"matches everything": {"name": "x", "pattern": ".*"},
		"unknown target":     {"name": "x", "pattern": "nodes", "target": "sideways"},
		"unknown action":     {"name": "x", "pattern": "nodes", "action": "explode"},
		"unknown cluster":    {"name": "x", "pattern": "nodes", "cluster_id": 4242},
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			rec := env.do(t, http.MethodPost, "/api/v1/guardrails", token, body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected %d, got %d (%s)", http.StatusBadRequest, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestGuardrailDefaultsAndScopeBadge(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", db.RoleAdmin)
	token := env.tokenFor(t, admin)
	cluster := env.store.addAgentCluster("edge-us", db.EnvProd, "kmg_token")

	global := createPolicy(t, env, token, map[string]any{
		"name": "fleet rule", "pattern": "^DELETE /api/v1/nodes/",
	})
	if global.ClusterID != 0 || global.ClusterName != "" {
		t.Fatalf("expected a fleet-wide rule, got cluster %d (%q)", global.ClusterID, global.ClusterName)
	}
	// A rule somebody just wrote is one they want in force.
	if !global.Enabled {
		t.Fatal("a new rule defaults to enabled")
	}
	if global.Target != db.GuardrailTargetBoth || global.Action != db.GuardrailActionBlock {
		t.Fatalf("unexpected defaults: target %q action %q", global.Target, global.Action)
	}

	scoped := createPolicy(t, env, token, map[string]any{
		"name": "prod only", "pattern": "^DELETE /api/v1/namespaces/[^/?]+$",
		"cluster_id": cluster.ID,
	})
	// The name is carried so the list can draw a scope badge without a second
	// lookup per row.
	if scoped.ClusterName != "edge-us" {
		t.Fatalf("expected the cluster name on a scoped rule, got %q", scoped.ClusterName)
	}
}

// `?cluster_id=0` is "the fleet-wide rules" and must stay distinguishable from
// the parameter being absent, which is everything.
func TestListGuardrailsFiltersByScope(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", db.RoleAdmin)
	token := env.tokenFor(t, admin)
	cluster := env.store.addAgentCluster("edge-us", db.EnvProd, "kmg_token")

	env.store.addGuardrail(db.GuardrailPolicy{Name: "global", Pattern: "^DELETE /api/v1/nodes/"})
	env.store.addGuardrail(db.GuardrailPolicy{
		Name: "scoped", Pattern: "^DELETE /api/v1/nodes/", ClusterID: cluster.ID,
	})

	all := decode[struct {
		Policies []guardrailPolicyResponse `json:"policies"`
	}](t, env.do(t, http.MethodGet, "/api/v1/guardrails", token, nil))
	if len(all.Policies) != 2 {
		t.Fatalf("expected both rules, got %d", len(all.Policies))
	}

	globals := decode[struct {
		Policies []guardrailPolicyResponse `json:"policies"`
	}](t, env.do(t, http.MethodGet, "/api/v1/guardrails?cluster_id=0", token, nil))
	if len(globals.Policies) != 1 || globals.Policies[0].Name != "global" {
		t.Fatalf("expected only the fleet-wide rule, got %+v", globals.Policies)
	}

	scoped := decode[struct {
		Policies []guardrailPolicyResponse `json:"policies"`
	}](t, env.do(t, http.MethodGet, "/api/v1/guardrails?cluster_id="+itoa(cluster.ID), token, nil))
	if len(scoped.Policies) != 1 || scoped.Policies[0].Name != "scoped" {
		t.Fatalf("expected only the cluster's rule, got %+v", scoped.Policies)
	}
}

func TestGuardrailTemplatesAreServed(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", db.RoleAdmin)

	body := decode[struct {
		Templates []db.GuardrailTemplate `json:"templates"`
	}](t, env.do(t, http.MethodGet, "/api/v1/guardrails/templates", env.tokenFor(t, admin), nil))

	if len(body.Templates) != len(db.GuardrailTemplates) {
		t.Fatalf("expected %d presets, got %d", len(db.GuardrailTemplates), len(body.Templates))
	}
	if body.Templates[0].Pattern == "" || body.Templates[0].Key == "" {
		t.Fatalf("a preset has to carry its pattern: %+v", body.Templates[0])
	}
}

/* ------------------------------------------------------- enforcement --- */

// The headline case: a global rule refuses `kubectl delete ns` on the proxy
// path, with a body that says which rule did it.
func TestGlobalGuardrailBlocksAProxiedDelete(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", db.RoleAdmin)
	token := env.tokenFor(t, admin)
	cluster := env.store.addAgentCluster("edge-us", db.EnvProd, "kmg_token")

	createPolicy(t, env, token, map[string]any{
		"name":    "Block namespace deletion",
		"pattern": `^DELETE /api/v1/namespaces/[^/?]+(\?.*)?$`,
		"target":  db.GuardrailTargetAPIRequest,
	})

	rec := env.do(t, http.MethodDelete,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/proxy/api/v1/namespaces/test-ns", token, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected %d, got %d (%s)", http.StatusForbidden, rec.Code, rec.Body.String())
	}

	body := decode[struct {
		Error  string `json:"error"`
		Guard  bool   `json:"guardrail_blocked"`
		Policy string `json:"policy"`
		Scope  string `json:"scope"`
	}](t, rec)

	// A client meeting a 403 has no other way to tell a KubeMG policy apart from
	// the cluster's own RBAC saying no, and the two call for different actions.
	if !body.Guard {
		t.Fatalf("the refusal must be marked as a guardrail: %s", rec.Body.String())
	}
	if body.Policy != "Block namespace deletion" {
		t.Fatalf("the refusal must name the rule, got %q", body.Policy)
	}
	if body.Scope != "global" {
		t.Fatalf("expected the global scope, got %q", body.Scope)
	}
}

// A rule scoped to one cluster must not refuse the same call on another. This is
// the per-cluster half of the feature, and the 503 is the proof: cluster B's call
// got past the guardrails and only then found no agent attached.
func TestClusterScopedGuardrailLeavesOtherClustersAlone(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", db.RoleAdmin)
	token := env.tokenFor(t, admin)
	clusterA := env.store.addAgentCluster("cluster-a", db.EnvProd, "kmg_a")
	clusterB := env.store.addAgentCluster("cluster-b", db.EnvDev, "kmg_b")

	createPolicy(t, env, token, map[string]any{
		"name":       "no pod deletion in A",
		"pattern":    `^DELETE /api/v1/namespaces/default/pods/`,
		"target":     db.GuardrailTargetAPIRequest,
		"cluster_id": clusterA.ID,
	})

	path := "/proxy/api/v1/namespaces/default/pods/web-1"

	rec := env.do(t, http.MethodDelete, "/api/v1/clusters/"+itoa(clusterA.ID)+path, token, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cluster A: expected %d, got %d (%s)", http.StatusForbidden, rec.Code, rec.Body.String())
	}

	rec = env.do(t, http.MethodDelete, "/api/v1/clusters/"+itoa(clusterB.ID)+path, token, nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("cluster B is not covered by the rule and should have reached the tunnel lookup; "+
			"expected %d, got %d (%s)", http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	}
}

// Disabling a rule takes effect on the next request, not on the next restart.
func TestDisablingAGuardrailStopsEnforcingIt(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", db.RoleAdmin)
	token := env.tokenFor(t, admin)
	cluster := env.store.addAgentCluster("edge-us", db.EnvProd, "kmg_token")
	path := "/api/v1/clusters/" + itoa(cluster.ID) + "/proxy/api/v1/nodes/node-1"

	policy := createPolicy(t, env, token, map[string]any{
		"name": "no node deletion", "pattern": `^DELETE /api/v1/nodes/`,
		"target": db.GuardrailTargetAPIRequest,
	})
	if rec := env.do(t, http.MethodDelete, path, token, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("expected the rule to be in force, got %d", rec.Code)
	}

	rec := env.do(t, http.MethodPut, "/api/v1/guardrails/"+itoa(policy.ID), token, map[string]any{
		"name": "no node deletion", "pattern": `^DELETE /api/v1/nodes/`,
		"target": db.GuardrailTargetAPIRequest, "enabled": false,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}

	if rec := env.do(t, http.MethodDelete, path, token, nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("a disabled rule enforces nothing; expected %d, got %d (%s)",
			http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	}
}

// An omitted `enabled` on an update keeps the stored value. Silently arming a
// rule somebody had switched off, because a form did not send the field, would
// start refusing calls nobody chose to refuse.
func TestUpdateKeepsTheStoredEnabledFlag(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", db.RoleAdmin)
	token := env.tokenFor(t, admin)

	stored := env.store.addGuardrail(db.GuardrailPolicy{
		Name: "off", Pattern: `^DELETE /api/v1/nodes/`, Enabled: false,
	})

	rec := env.do(t, http.MethodPut, "/api/v1/guardrails/"+itoa(stored.ID), token, map[string]any{
		"name": "renamed", "pattern": `^DELETE /api/v1/nodes/`,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	if body := decode[guardrailPolicyResponse](t, rec); body.Enabled {
		t.Fatal("an omitted enabled must keep the rule switched off")
	}
}

func TestDeletingAGuardrailStopsEnforcingIt(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", db.RoleAdmin)
	token := env.tokenFor(t, admin)
	cluster := env.store.addAgentCluster("edge-us", db.EnvProd, "kmg_token")
	path := "/api/v1/clusters/" + itoa(cluster.ID) + "/proxy/api/v1/nodes/node-1"

	policy := createPolicy(t, env, token, map[string]any{
		"name": "no node deletion", "pattern": `^DELETE /api/v1/nodes/`,
		"target": db.GuardrailTargetAPIRequest,
	})
	if rec := env.do(t, http.MethodDelete, path, token, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("expected the rule to be in force, got %d", rec.Code)
	}

	rec := env.do(t, http.MethodDelete, "/api/v1/guardrails/"+itoa(policy.ID), token, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected %d, got %d (%s)", http.StatusNoContent, rec.Code, rec.Body.String())
	}
	if rec := env.do(t, http.MethodDelete, path, token, nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("a deleted rule enforces nothing; expected %d, got %d",
			http.StatusServiceUnavailable, rec.Code)
	}
}

// A guardrail refuses calls the caller is entitled to make, so it has to apply
// to an administrator too — an admin is exactly who deletes a namespace by
// accident. Nothing in the enforcement path consults the caller's role.
func TestGuardrailsApplyToAdministratorsToo(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", db.RoleAdmin)
	token := env.tokenFor(t, admin)
	cluster := env.store.addAgentCluster("edge-us", db.EnvProd, "kmg_token")

	createPolicy(t, env, token, map[string]any{
		"name": "no node deletion", "pattern": `^DELETE /api/v1/nodes/`,
		"target": db.GuardrailTargetAPIRequest,
	})

	rec := env.do(t, http.MethodDelete,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/proxy/api/v1/nodes/node-1", token, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("an admin is not exempt; expected %d, got %d", http.StatusForbidden, rec.Code)
	}
}

// A terminal_exec rule must not fire on API traffic. Getting this wrong would
// block ordinary reads whose path happens to contain the word.
func TestATerminalRuleDoesNotBlockAPICalls(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", db.RoleAdmin)
	token := env.tokenFor(t, admin)
	cluster := env.store.addAgentCluster("edge-us", db.EnvProd, "kmg_token")

	createPolicy(t, env, token, map[string]any{
		"name": "no shell deletes", "pattern": `nodes`,
		"target": db.GuardrailTargetTerminalExec,
	})

	rec := env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/proxy/api/v1/nodes", token, nil)
	if rec.Code == http.StatusForbidden {
		t.Fatalf("a terminal rule must not refuse an API call: %s", rec.Body.String())
	}
}

// The non-interactive exec: the command is in the query string and runs without
// anything being typed, so the keystroke guard would never see it.
func TestExecArgvIsCheckedAtStreamOpen(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", db.RoleAdmin)
	token := env.tokenFor(t, admin)
	cluster := env.store.addAgentCluster("edge-us", db.EnvProd, "kmg_token")

	createPolicy(t, env, token, map[string]any{
		"name": "no rm -rf", "pattern": `rm\s+-rf\s+/`,
		"target": db.GuardrailTargetTerminalExec,
	})

	rec := env.do(t, http.MethodGet, "/api/v1/clusters/"+itoa(cluster.ID)+
		"/proxy/api/v1/namespaces/default/pods/web/exec?command=rm&command=-rf&command=%2F", token, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected the argv to be refused at open; got %d (%s)", rec.Code, rec.Body.String())
	}
	if body := decode[struct {
		Guard bool `json:"guardrail_blocked"`
	}](t, rec); !body.Guard {
		t.Fatalf("the refusal must be marked as a guardrail: %s", rec.Body.String())
	}
}

// A guardrail refusal is exactly the row an auditor opens the trail to find, so
// it has to be recorded — with the rule that caused it, not only a 403.
func TestABlockedCallIsAudited(t *testing.T) {
	auditor := &recordingAuditor{}

	// The proxy the default stack builds records to a logger, so the auditor has
	// to be wired into a proxy of this test's own — the record being asserted on
	// is written by the gateway, not by the HTTP layer.
	env := newTestEnvWith(t, func(opts *Options) {
		opts.Auditor = auditor
		opts.Proxy = bastion.NewProxy(bastion.ProxyOptions{
			Store:    opts.Store.(bastion.ProxyStore),
			Registry: opts.Bastion.Registry(),
			Auditor:  auditor,
			Guard:    opts.Guardrails,
		})
	})
	admin := env.store.addUser("admin", "secret123", db.RoleAdmin)
	token := env.tokenFor(t, admin)
	cluster := env.store.addAgentCluster("edge-us", db.EnvProd, "kmg_token")

	createPolicy(t, env, token, map[string]any{
		"name": "Block namespace deletion", "pattern": `^DELETE /api/v1/namespaces/[^/?]+$`,
		"target": db.GuardrailTargetAPIRequest,
	})

	rec := env.do(t, http.MethodDelete,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/proxy/api/v1/namespaces/test-ns", token, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected %d, got %d", http.StatusForbidden, rec.Code)
	}

	recorded := auditor.all()
	if len(recorded) == 0 {
		t.Fatal("a refused call must reach the audit trail")
	}
	found := false
	for _, event := range recorded {
		if event.Status == http.StatusForbidden && event.GuardrailPolicy == "Block namespace deletion" {
			found = true
			if event.GuardrailAction != db.GuardrailActionBlock {
				t.Fatalf("expected the action recorded, got %q", event.GuardrailAction)
			}
			if event.Path != "/api/v1/namespaces/test-ns" {
				t.Fatalf("expected the refused path recorded, got %q", event.Path)
			}
		}
	}
	if !found {
		t.Fatalf("expected a 403 naming the policy, got %+v", recorded)
	}
}

// A rule created with the box unticked has to come back disabled. This is the
// handler half of the invariant; the storage half is guarded in pkg/db, where a
// column default would silently turn the false into a true.
func TestARuleCanBeCreatedDisabled(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", db.RoleAdmin)

	policy := createPolicy(t, env, env.tokenFor(t, admin), map[string]any{
		"name": "not yet", "pattern": `^DELETE /api/v1/nodes/`, "enabled": false,
	})
	if policy.Enabled {
		t.Fatal("a rule created with enabled:false must be stored disabled")
	}
}

// The trail has to carry the rule that fired as its own field. A policy name
// buried in a free-text error string is not something an operator can filter on,
// and filtering is the entire reason to run a rule in warn before arming it.
func TestTheAuditTrailCarriesTheGuardrailFields(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", db.RoleAdmin)
	cluster := env.store.addAgentCluster("edge-us", db.EnvProd, "kmg_token")

	env.store.addAuditEvent(db.AuditEvent{
		UserID: admin.ID, Username: "admin", ClusterID: cluster.ID,
		Verb: "delete", Method: http.MethodDelete, Path: "/api/v1/namespaces/prod",
		Status:          http.StatusForbidden,
		GuardrailPolicy: "Block namespace deletion",
		GuardrailAction: db.GuardrailActionBlock,
		Error:           "Blocked by KubeMG Safety Policy: Block namespace deletion",
	})

	body := decode[struct {
		Events []struct {
			Status          int    `json:"status"`
			GuardrailPolicy string `json:"guardrail_policy"`
			GuardrailAction string `json:"guardrail_action"`
		} `json:"events"`
	}](t, env.do(t, http.MethodGet, "/api/v1/audit", env.tokenFor(t, admin), nil))

	if len(body.Events) != 1 {
		t.Fatalf("expected one row, got %d", len(body.Events))
	}
	if body.Events[0].GuardrailPolicy != "Block namespace deletion" {
		t.Fatalf("the row must name the rule, got %q", body.Events[0].GuardrailPolicy)
	}
	if body.Events[0].GuardrailAction != db.GuardrailActionBlock {
		t.Fatalf("the row must carry the action, got %q", body.Events[0].GuardrailAction)
	}
}
