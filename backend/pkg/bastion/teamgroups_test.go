package bastion

import (
	"net/http"
	"slices"
	"testing"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

// A caller granted the cluster through two teams, one of which the agent is
// confirmed to allow: the agent is told the confirmed team, the other is left
// off the wire, and the audit row carries both facts — what was asserted and
// what was withheld.
func TestProxyAssertsConfirmedTeamsAndRecordsWithheldOnes(t *testing.T) {
	h := newHarness(t)
	cluster := h.addCluster(1, "prod-eu", "kmg_valid")
	h.store.mu.Lock()
	cluster.AgentTeamGroups = "platform"
	h.store.mu.Unlock()
	user := h.addUser(10, "devops", db.SystemRoleUser)
	h.grant(10, 1, db.K8sRoleView, nil)
	h.store.mu.Lock()
	grant := h.store.access[10][1]
	grant.TeamGroups = []string{"payments", "platform"}
	h.store.access[10][1] = grant
	h.store.mu.Unlock()

	agent, err := h.dialAgent("kmg_valid", okResponse(`{"kind":"PodList"}`))
	if err != nil {
		t.Fatalf("dial agent: %v", err)
	}
	waitFor(t, func() bool { return h.gateway.Registry().Connected(1) })

	resp := h.proxyRequest(http.MethodGet,
		"/api/v1/clusters/1/proxy/api/v1/namespaces/team-a/pods", h.token(user))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	want := []string{"kubemg:view", "kubemg:users", "kubemg:g:platform"}
	seen := agent.requests()
	if len(seen) != 1 {
		t.Fatalf("expected exactly one request through the tunnel, got %d", len(seen))
	}
	if got := seen[0].Header["Impersonate-Group"]; !slices.Equal(got, want) {
		t.Fatalf("Impersonate-Group = %v, want %v", got, want)
	}

	events := h.audit.events()
	if len(events) != 1 {
		t.Fatalf("expected one audit record, got %d", len(events))
	}
	if !slices.Equal(events[0].ImpersonatedGroups, want) {
		t.Fatalf("audited groups = %v, want what the agent was told: %v", events[0].ImpersonatedGroups, want)
	}
	if !slices.Equal(events[0].WithheldTeamGroups, []string{"kubemg:g:payments"}) {
		t.Fatalf("withheld = %v, want the team waiting for the re-apply", events[0].WithheldTeamGroups)
	}
}

// A client that names a team group itself gets nowhere: impersonation headers
// from the caller are dropped before KubeMG decides the identity.
func TestProxyDropsAClientSuppliedTeamGroup(t *testing.T) {
	h := newHarness(t)
	h.addCluster(1, "prod-eu", "kmg_valid")
	user := h.addUser(10, "devops", db.SystemRoleUser)
	h.grant(10, 1, db.K8sRoleView, nil)

	agent, err := h.dialAgent("kmg_valid", okResponse(`{"kind":"PodList"}`))
	if err != nil {
		t.Fatalf("dial agent: %v", err)
	}
	waitFor(t, func() bool { return h.gateway.Registry().Connected(1) })

	req, err := http.NewRequest(http.MethodGet,
		h.server.URL+"/api/v1/clusters/1/proxy/api/v1/namespaces/team-a/pods", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+h.token(user))
	req.Header.Add("Impersonate-Group", "kubemg:g:platform")
	resp, err := h.server.Client().Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	_ = resp.Body.Close()

	seen := agent.requests()
	if len(seen) != 1 {
		t.Fatalf("expected exactly one request through the tunnel, got %d", len(seen))
	}
	if got := seen[0].Header["Impersonate-Group"]; slices.Contains(got, "kubemg:g:platform") {
		t.Fatalf("a client-supplied team group reached the agent: %v", got)
	}
}
