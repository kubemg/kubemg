package bastion

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
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

// impersonationRefusal is what the API server answers when the agent asserts a
// group its ClusterRole does not name.
func impersonationRefusal(group string) string {
	return `{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure","message":"groups \"` + group +
		`\" is forbidden: User \"system:serviceaccount:kubemg-system:kubemg-agent\" cannot impersonate resource \"groups\" in API group \"\" at the cluster scope","reason":"Forbidden","details":{"name":"` + group +
		`","kind":"groups"},"code":403}`
}

func TestStaleAgentRefusalNamesTheFix(t *testing.T) {
	asserted := []string{"kubemg:view", "kubemg:users", "kubemg:g:ops"}
	message, ok := staleAgentRefusal(http.StatusForbidden, statusMessage([]byte(impersonationRefusal("kubemg:g:ops"))), asserted)
	if !ok {
		t.Fatal("the refusal of a confirmed team group was not recognised")
	}
	for _, want := range []string{"kubemg:g:ops", "Re-apply the package", "Agent install"} {
		if !strings.Contains(message, want) {
			t.Fatalf("message %q does not say %q", message, want)
		}
	}

	// Anything else is the cluster's own answer and is left exactly as it was.
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"another 403":               {http.StatusForbidden, `{"message":"pods is forbidden: User \"kubemg:u:bob\" cannot list resource \"pods\""}`},
		"a group KubeMG never sent": {http.StatusForbidden, impersonationRefusal("kubemg:g:platform")},
		"a fixed group":             {http.StatusForbidden, impersonationRefusal("kubemg:edit")},
		"not a 403":                 {http.StatusOK, impersonationRefusal("kubemg:g:ops")},
	} {
		if _, ok := staleAgentRefusal(tc.status, statusMessage([]byte(tc.body)), asserted); ok {
			t.Fatalf("%s: rewritten, want the cluster's answer untouched", name)
		}
	}
}

// teamHarness is a harness whose caller is granted the cluster through the
// confirmed team ops, served by an agent that answers every call with resp.
func teamHarness(t *testing.T, respond func(Request) Response) (*harness, *db.User, *fakeAgent) {
	t.Helper()
	h := newHarness(t)
	cluster := h.addCluster(1, "prod-eu", "kmg_valid")
	h.store.mu.Lock()
	cluster.AgentTeamGroups = "ops"
	h.store.mu.Unlock()
	user := h.addUser(10, "bob", db.SystemRoleUser)
	h.grant(10, 1, db.K8sRoleView, nil)
	h.store.mu.Lock()
	grant := h.store.access[10][1]
	grant.TeamGroups = []string{"ops"}
	h.store.access[10][1] = grant
	h.store.mu.Unlock()
	agent, err := h.dialAgent("kmg_valid", respond)
	if err != nil {
		t.Fatalf("dial agent: %v", err)
	}
	waitFor(t, func() bool { return h.gateway.Registry().Connected(1) })
	return h, user, agent
}

func refusedByCluster(Request) Response {
	return Response{
		Status: http.StatusForbidden,
		Header: map[string][]string{"Content-Type": {"application/json"}},
		Body:   []byte(impersonationRefusal("kubemg:g:ops")),
	}
}

// staleUntilWithdrawn answers like an API server whose agent package names none
// of the team groups: the first one asserted is refused, and a call without any
// is answered.
func staleUntilWithdrawn(r Request) Response {
	for _, group := range r.Header["Impersonate-Group"] {
		if strings.HasPrefix(group, TeamGroupPrefix) {
			return Response{
				Status: http.StatusForbidden,
				Header: map[string][]string{"Content-Type": {"application/json"}},
				Body:   []byte(impersonationRefusal(group)),
			}
		}
	}
	return Response{
		Status: http.StatusOK,
		Header: map[string][]string{"Content-Type": {"application/json"}},
		Body:   []byte(`{"kind":"PodList","items":[]}`),
	}
}

// A confirmation the cluster shows to be wrong is withdrawn and the call goes
// again without the group, so the caller keeps the base role instead of being
// locked out until somebody re-applies the package. The withdrawal is its own
// audit row, and the call's row says what was asserted and what is now waiting.
func TestProxyWithdrawsAStaleTeamGroupAndAnswersWithTheBaseRole(t *testing.T) {
	h, user, agent := teamHarness(t, staleUntilWithdrawn)

	resp := h.proxyRequest(http.MethodGet, "/api/v1/clusters/1/proxy/api/v1/namespaces/team-a/pods", h.token(user))
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected the base role to answer, got %d", resp.StatusCode)
	}

	seen := agent.requests()
	if len(seen) != 2 {
		t.Fatalf("expected the call and one retry, got %d requests", len(seen))
	}
	if !slices.Contains(seen[0].Header["Impersonate-Group"], "kubemg:g:ops") {
		t.Fatalf("the first attempt should assert the team: %v", seen[0].Header["Impersonate-Group"])
	}
	if got := seen[1].Header["Impersonate-Group"]; slices.Contains(got, "kubemg:g:ops") || !slices.Contains(got, "kubemg:view") {
		t.Fatalf("the retry must carry the base role and not the team, got %v", got)
	}

	h.store.mu.Lock()
	confirmed := h.store.clusters[1].AgentTeamGroups
	h.store.mu.Unlock()
	if confirmed != "" {
		t.Fatalf("the confirmation must be withdrawn, got %q", confirmed)
	}

	events := h.audit.events()
	if len(events) != 2 {
		t.Fatalf("expected the withdrawal and the call, got %+v", events)
	}
	withdrawal, call := events[0], events[1]
	if withdrawal.Verb != VerbAgentTeamGroups || withdrawal.Name != "kubemg:g:ops" ||
		!strings.Contains(withdrawal.Error, "withdrawn") {
		t.Fatalf("unexpected withdrawal row: %+v", withdrawal)
	}
	if call.Status != http.StatusOK || slices.Contains(call.ImpersonatedGroups, "kubemg:g:ops") ||
		!slices.Equal(call.WithheldTeamGroups, []string{"kubemg:g:ops"}) {
		t.Fatalf("the call's row must show the team as waiting: %+v", call)
	}
}

// Two stale teams are withdrawn one refusal at a time, and the call still ends
// with the base role.
func TestProxyWithdrawsEveryStaleTeamGroup(t *testing.T) {
	h, user, agent := teamHarness(t, staleUntilWithdrawn)
	h.store.mu.Lock()
	h.store.clusters[1].AgentTeamGroups = "ops,platform"
	grant := h.store.access[10][1]
	grant.TeamGroups = []string{"ops", "platform"}
	h.store.access[10][1] = grant
	h.store.mu.Unlock()

	resp := h.proxyRequest(http.MethodGet, "/api/v1/clusters/1/proxy/api/v1/namespaces/team-a/pods", h.token(user))
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected the base role to answer, got %d", resp.StatusCode)
	}
	if got := len(agent.requests()); got != 3 {
		t.Fatalf("expected the call and two retries, got %d requests", got)
	}
	h.store.mu.Lock()
	confirmed := h.store.clusters[1].AgentTeamGroups
	h.store.mu.Unlock()
	if confirmed != "" {
		t.Fatalf("both confirmations must be withdrawn, got %q", confirmed)
	}
}

// A refusal about anything else is the cluster's answer and passes through, and
// no confirmation is touched on its account.
func TestProxyLeavesOtherRefusalsAlone(t *testing.T) {
	h, user, agent := teamHarness(t, func(Request) Response {
		return Response{
			Status: http.StatusForbidden,
			Header: map[string][]string{"Content-Type": {"application/json"}},
			Body:   []byte(`{"kind":"Status","status":"Failure","message":"pods is forbidden: User \"kubemg:u:bob\" cannot list resource \"pods\"","reason":"Forbidden","code":403}`),
		}
	})

	resp := h.proxyRequest(http.MethodGet, "/api/v1/clusters/1/proxy/api/v1/namespaces/team-a/pods", h.token(user))
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected the cluster's 403, got %d", resp.StatusCode)
	}
	if got := len(agent.requests()); got != 1 {
		t.Fatalf("an ordinary refusal must not be retried, got %d requests", got)
	}
	h.store.mu.Lock()
	confirmed := h.store.clusters[1].AgentTeamGroups
	h.store.mu.Unlock()
	if confirmed != "ops" {
		t.Fatalf("the confirmation must stay, got %q", confirmed)
	}
}

// The console's calls recover the same way.
func TestCallWithdrawsAStaleTeamGroupAndAnswersWithTheBaseRole(t *testing.T) {
	h, user, agent := teamHarness(t, staleUntilWithdrawn)
	h.store.mu.Lock()
	cluster, grant := h.store.clusters[1], h.store.access[10][1]
	h.store.mu.Unlock()

	resp, err := h.proxy.Call(context.Background(), user, cluster, grant, http.MethodGet,
		"/api/v1/namespaces/team-a/pods", nil, nil)
	if err != nil || resp.Status != http.StatusOK {
		t.Fatalf("expected the base role to answer, got %v (%+v)", err, resp)
	}
	if got := len(agent.requests()); got != 2 {
		t.Fatalf("expected the call and one retry, got %d requests", got)
	}
}

// kubectl through the proxy gets a Kubernetes Status naming the re-apply, so
// it prints the reason instead of the agent's ServiceAccount, and the audit
// row carries the same explanation.
func TestProxyExplainsAStaleAgentRefusal(t *testing.T) {
	h, user, _ := teamHarness(t, refusedByCluster)
	// The confirmation cannot be withdrawn here, so the refusal is all there is.
	h.store.forgetErr = errors.New("database unavailable")

	resp := h.proxyRequest(http.MethodGet, "/api/v1/clusters/1/proxy/api/v1/namespaces/team-a/pods", h.token(user))
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}
	var status struct {
		Kind, Reason, Message string
		Code                  int
	}
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatalf("the refusal is not a Kubernetes Status: %v", err)
	}
	if status.Kind != "Status" || status.Reason != "Forbidden" || status.Code != http.StatusForbidden ||
		!strings.Contains(status.Message, "Re-apply the package") {
		t.Fatalf("unexpected refusal: %+v", status)
	}

	events := h.audit.events()
	if len(events) != 1 || events[0].Status != http.StatusForbidden || !strings.Contains(events[0].Error, "kubemg:g:ops") {
		t.Fatalf("the audit row must carry the explanation, got %+v", events)
	}
	if events[0].RefusedBy != "" {
		t.Fatalf("the cluster refused the call, not KubeMG: refused_by=%q", events[0].RefusedBy)
	}
}

// The console's calls get the same explanation as a CallError.
func TestCallExplainsAStaleAgentRefusal(t *testing.T) {
	h, user, _ := teamHarness(t, refusedByCluster)
	h.store.forgetErr = errors.New("database unavailable")
	h.store.mu.Lock()
	cluster, grant := h.store.clusters[1], h.store.access[10][1]
	h.store.mu.Unlock()

	_, err := h.proxy.Call(context.Background(), user, cluster, grant, http.MethodGet,
		"/api/v1/namespaces/team-a/pods", nil, nil)
	var callErr *CallError
	if !errors.As(err, &callErr) || callErr.Status != http.StatusForbidden ||
		!strings.Contains(callErr.Message, "Re-apply the package") {
		t.Fatalf("expected the explained refusal, got %v", err)
	}
}
