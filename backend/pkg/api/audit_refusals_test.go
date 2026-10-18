package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/kubemg/kubemg/backend/pkg/bastion"
	"github.com/kubemg/kubemg/backend/pkg/db"
)

// The trail promises every attempt, refusals included. These pin the half the
// proxy cannot keep on its own: a refusal the API layer makes before anything
// reaches the tunnel is recorded exactly once, marked as KubeMG's, and a
// refusal the cluster gave is never recorded a second time.

// newRefusalEnv wires one recording auditor into both halves, the way the
// server does: the API's own records and the proxy's go to the same place.
func newRefusalEnv(t *testing.T) (*testEnv, *recordingAuditor) {
	t.Helper()
	auditor := &recordingAuditor{}
	env := newTestEnvWith(t, func(opts *Options) {
		opts.Auditor = auditor
		opts.Proxy = bastion.NewProxy(bastion.ProxyOptions{
			Store:    opts.Store.(bastion.ProxyStore),
			Registry: opts.Bastion.Registry(),
			Auditor:  auditor,
			Guard:    opts.Guardrails,
		})
	})
	return env, auditor
}

// onlyRefusal asserts the trail holds exactly one record and that it is
// KubeMG's own refusal, shaped like the proxy's.
func onlyRefusal(t *testing.T, auditor *recordingAuditor, status int) bastion.Event {
	t.Helper()
	events := auditor.all()
	if len(events) != 1 {
		t.Fatalf("expected exactly one audit record, got %d: %+v", len(events), events)
	}
	event := events[0]
	if event.Status != status {
		t.Fatalf("expected the record to carry status %d, got %d", status, event.Status)
	}
	if event.RefusedBy != bastion.RefusedByKubeMG {
		t.Fatalf("a refusal KubeMG made must say so, got refused_by=%q", event.RefusedBy)
	}
	if event.Error == "" {
		t.Fatal("the record must carry the refusal's explanation")
	}
	return event
}

func TestARefusedNamespaceIsRecordedOnce(t *testing.T) {
	for _, resource := range []string{"pods", "services", "configmaps", "jobs"} {
		t.Run(resource, func(t *testing.T) {
			env, auditor := newRefusalEnv(t)
			user := env.store.addUser("scoped", "secret123", db.RoleUser)
			cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
			env.store.grant(user.ID, cluster.ID, "view", []string{"team-a"})

			rec := env.do(t, http.MethodGet,
				"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/"+resource+"?namespace=team-b",
				env.tokenFor(t, user), nil)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("expected %d, got %d (%s)", http.StatusForbidden, rec.Code, rec.Body.String())
			}

			event := onlyRefusal(t, auditor, http.StatusForbidden)
			if event.Username != "scoped" || event.UserID != user.ID {
				t.Fatalf("the record must name the caller, got %q/%d", event.Username, event.UserID)
			}
			if event.ClusterID != cluster.ID || event.Cluster != "edge" {
				t.Fatalf("the record must name the cluster, got %q/%d", event.Cluster, event.ClusterID)
			}
			if event.Namespace != "team-b" || event.Verb != "list" {
				t.Fatalf("expected a list in team-b, got %q in %q", event.Verb, event.Namespace)
			}
		})
	}
}

func TestAClusterScopeRefusalIsRecorded(t *testing.T) {
	env, auditor := newRefusalEnv(t)
	user := env.store.addUser("scoped", "secret123", db.RoleUser)
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	env.store.grant(user.ID, cluster.ID, "view", []string{"team-a"})

	rec := env.do(t, http.MethodGet, "/api/v1/clusters/"+itoa(cluster.ID)+"/resources/nodes",
		env.tokenFor(t, user), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected %d, got %d (%s)", http.StatusForbidden, rec.Code, rec.Body.String())
	}

	event := onlyRefusal(t, auditor, http.StatusForbidden)
	if event.Resource != "nodes" || event.Verb != "list" {
		t.Fatalf("expected a list of nodes, got %q %q", event.Verb, event.Resource)
	}
	if !strings.Contains(event.Error, "team-a") {
		t.Fatalf("the recorded message must be the one the caller was given, got %q", event.Error)
	}
	if event.ImpersonatedUser != "" {
		t.Fatalf("nothing was asserted to the cluster, so no identity may be recorded as asserted: %q",
			event.ImpersonatedUser)
	}
}

func TestAKindKubeMGWillNotAuthorIsRecorded(t *testing.T) {
	env, auditor := newRefusalEnv(t)
	admin := env.store.addUser("admin", "secret123", db.RoleAdmin)
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")

	rec := env.do(t, http.MethodPost, createURL(cluster.ID, "kind=clusterrolebindings"),
		env.tokenFor(t, admin), map[string]string{
			"yaml": "apiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRoleBinding\nmetadata:\n  name: r\n",
		})
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected %d, got %d (%s)", http.StatusConflict, rec.Code, rec.Body.String())
	}

	event := onlyRefusal(t, auditor, http.StatusConflict)
	if event.Verb != "create" || event.Resource != "clusterrolebindings" {
		t.Fatalf("expected a create of clusterrolebindings, got %q %q", event.Verb, event.Resource)
	}
}

func TestAManifestForAnotherNamespaceIsRecorded(t *testing.T) {
	env, auditor := newRefusalEnv(t)
	admin := env.store.addUser("admin", "secret123", db.RoleAdmin)
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")

	rec := env.do(t, http.MethodPost, createURL(cluster.ID, "kind=configmaps&namespace=default"),
		env.tokenFor(t, admin), map[string]string{
			"yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: app\n  namespace: other\n",
		})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d (%s)", http.StatusBadRequest, rec.Code, rec.Body.String())
	}

	event := onlyRefusal(t, auditor, http.StatusBadRequest)
	if event.Namespace != "default" || event.Verb != "create" {
		t.Fatalf("expected a create addressed at default, got %q in %q", event.Verb, event.Namespace)
	}
}

// A console action whose method is not its verb is recorded under the verb the
// tunnel call would have carried, so a refused scale sits beside allowed ones.
func TestAConsoleActionIsRecordedUnderItsKubernetesVerb(t *testing.T) {
	env, auditor := newRefusalEnv(t)
	user := env.store.addUser("scoped", "secret123", db.RoleUser)
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	env.store.grant(user.ID, cluster.ID, "edit", []string{"team-a"})

	rec := env.do(t, http.MethodPost,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/scale?kind=deployments&namespace=team-b&name=api",
		env.tokenFor(t, user), map[string]int{"replicas": 2})
	if rec.Code < 400 || rec.Code >= 500 {
		t.Fatalf("expected a refusal, got %d (%s)", rec.Code, rec.Body.String())
	}

	event := onlyRefusal(t, auditor, rec.Code)
	if event.Verb != "update" {
		t.Fatalf("a scale is an update, got %q", event.Verb)
	}
}

// kubectl's route is in the trail when the proxy refuses before its own record:
// a caller with no grant on the cluster at all.
func TestAProxyCallerWithNoGrantIsRecorded(t *testing.T) {
	env, auditor := newRefusalEnv(t)
	user := env.store.addUser("stranger", "secret123", db.RoleUser)
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")

	rec := env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/proxy/api/v1/namespaces/default/pods",
		env.tokenFor(t, user), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected %d, got %d (%s)", http.StatusForbidden, rec.Code, rec.Body.String())
	}

	event := onlyRefusal(t, auditor, http.StatusForbidden)
	if event.Path != "/api/v1/namespaces/default/pods" || event.Verb != "list" ||
		event.Namespace != "default" || event.Resource != "pods" {
		t.Fatalf("a kubectl refusal must be described as the proxy describes a call, got %+v", event)
	}
}

// The proxy's own namespace refusal is already in the trail; the middleware
// must not add a second row for it.
func TestAProxyNamespaceRefusalIsNotRecordedTwice(t *testing.T) {
	env, auditor := newRefusalEnv(t)
	user := env.store.addUser("scoped", "secret123", db.RoleUser)
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	env.store.grant(user.ID, cluster.ID, "view", []string{"team-a"})

	rec := env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/proxy/api/v1/namespaces/team-b/pods",
		env.tokenFor(t, user), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected %d, got %d (%s)", http.StatusForbidden, rec.Code, rec.Body.String())
	}
	onlyRefusal(t, auditor, http.StatusForbidden)
}

// No identity, no row: a request without a session is refused before anything
// here could say who made it.
func TestAnUnauthenticatedRequestWritesNoRow(t *testing.T) {
	env, auditor := newRefusalEnv(t)
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")

	rec := env.do(t, http.MethodGet, "/api/v1/clusters/"+itoa(cluster.ID)+"/resources/nodes", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected %d, got %d", http.StatusUnauthorized, rec.Code)
	}
	if events := auditor.all(); len(events) != 0 {
		t.Fatalf("expected no record without an identity, got %+v", events)
	}
}

// serveAgent attaches a fake agent to the router that answers every request
// with one status, and counts what it was asked.
func serveAgent(t *testing.T, env *testEnv, cluster *db.Cluster, status int, body string) *countingAgent {
	t.Helper()
	server := httptest.NewServer(env.router)
	t.Cleanup(server.Close)

	conn, err := dialAgent(t, server, cluster.AgentToken)
	if err != nil {
		t.Fatalf("dial agent: %v", err)
	}
	waitUntil(t, func() bool { return env.registry.Connected(cluster.ID) })

	agent := &countingAgent{}
	go func() {
		for {
			var msg bastion.Message
			if err := conn.ReadJSON(&msg); err != nil {
				return
			}
			if msg.Type != bastion.MessageRequest || msg.Request == nil {
				continue
			}
			agent.mu.Lock()
			agent.seen++
			agent.mu.Unlock()
			if err := conn.WriteJSON(bastion.Message{
				Type: bastion.MessageResponse, ID: msg.ID,
				Response: &bastion.Response{
					Status: status,
					Header: map[string][]string{"Content-Type": {"application/json"}},
					Body:   []byte(body),
				},
			}); err != nil {
				return
			}
		}
	}()
	return agent
}

type countingAgent struct {
	mu   sync.Mutex
	seen int
}

func (a *countingAgent) requests() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.seen
}

// A refusal the cluster gave is recorded by the proxy that carried it, and only
// there: one row per call that crossed the tunnel, none of them KubeMG's.
func TestAClusterRefusalIsRecordedOnceByTheProxy(t *testing.T) {
	env, auditor := newRefusalEnv(t)
	admin := env.store.addUser("admin", "secret123", db.RoleAdmin)
	cluster := env.store.addAgentCluster("edge", "dev", "kmg_agent-token")
	agent := serveAgent(t, env, cluster, http.StatusForbidden,
		`{"kind":"Status","apiVersion":"v1","status":"Failure","message":"configmaps is forbidden","reason":"Forbidden","code":403}`)

	rec := env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/configmaps?namespace=default",
		env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected the cluster's %d, got %d (%s)", http.StatusForbidden, rec.Code, rec.Body.String())
	}

	assertOnlyTunnelRows(t, auditor, agent)
	for _, event := range auditor.all() {
		if event.Status != http.StatusForbidden {
			t.Fatalf("expected the cluster's refusal on every row, got %d", event.Status)
		}
	}
}

func TestASuccessfulReadWritesNoRefusal(t *testing.T) {
	env, auditor := newRefusalEnv(t)
	admin := env.store.addUser("admin", "secret123", db.RoleAdmin)
	cluster := env.store.addAgentCluster("edge", "dev", "kmg_agent-token")
	agent := serveAgent(t, env, cluster, http.StatusOK,
		`{"kind":"ConfigMapList","apiVersion":"v1","metadata":{},"items":[]}`)

	rec := env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/configmaps?namespace=default",
		env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	assertOnlyTunnelRows(t, auditor, agent)
}

// assertOnlyTunnelRows checks the trail holds one row per call the agent was
// asked and nothing of the middleware's.
func assertOnlyTunnelRows(t *testing.T, auditor *recordingAuditor, agent *countingAgent) {
	t.Helper()
	events := auditor.all()
	if agent.requests() == 0 {
		t.Fatal("the read should have crossed the tunnel")
	}
	if len(events) != agent.requests() {
		t.Fatalf("expected one row per tunnel call (%d), got %d: %+v", agent.requests(), len(events), events)
	}
	for _, event := range events {
		if event.RefusedBy != "" {
			t.Fatalf("no row may be KubeMG's own refusal here, got %+v", event)
		}
	}
}

func TestConsoleRouteDropsParameters(t *testing.T) {
	cases := map[string]string{
		"/api/v1/clusters/:id/resources/pods/:pod/logs":        "pods/logs",
		"/api/v1/clusters/:id/resources/helm/releases/:name":   "helm/releases",
		"/api/v1/clusters/:id/observability/silences/:sid":     "observability/silences",
		"/api/v1/clusters/:id/metrics/nodes":                   "metrics/nodes",
		"/api/v1/clusters/:id/resources/scale":                 "scale",
		"/api/v1/clusters/:id/resources/helm/releases/:name/x": "helm/releases/x",
	}
	for full, want := range cases {
		if got := consoleRoute(full); got != want {
			t.Errorf("consoleRoute(%q) = %q, want %q", full, got, want)
		}
	}
}

// The browser terminal's ticket rides the query string; a refusal recorded
// against that route must not carry it.
func TestStripCredentialsDropsTheTicket(t *testing.T) {
	got := bastion.StripCredentials("access_token=kmgt_secret&container=app")
	if strings.Contains(got, "kmgt_secret") || got != "container=app" {
		t.Fatalf("the ticket must not reach the trail, got %q", got)
	}
}
