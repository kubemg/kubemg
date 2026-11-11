package bastion

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

func TestImpersonationGroupsCarryTheGrant(t *testing.T) {
	got := ImpersonationGroups(db.K8sRoleEdit)
	if !slices.Equal(got, []string{"kubemg:edit", "kubemg:users"}) {
		t.Fatalf("unexpected groups: %v", got)
	}

	// An empty grant must never fall through to something permissive.
	if got := ImpersonationGroups(""); got[0] != "kubemg:view" {
		t.Fatalf("an empty role should default to view, got %v", got)
	}
}

func TestForwardHeadersStripsClientCredentials(t *testing.T) {
	src := http.Header{
		"Accept":            []string{"application/json"},
		"Authorization":     []string{"Bearer the-callers-kubemg-token"},
		"Impersonate-User":  []string{"root"},
		"Impersonate-Group": []string{"system:masters"},
		"Impersonate-Extra-Scopes": []string{"cluster-admin"},
		"impersonate-extra-reason": []string{"lowercase too"},
		"Connection":        []string{"keep-alive"},
		"User-Agent":        []string{"kubectl/v1.31.4"},
	}

	out := forwardHeaders(src, &db.User{Username: "devops"}, db.UserClusterAccess{K8sRole: db.K8sRoleView})

	// A client that could pick its own identity would defeat the gateway.
	if got := out["Impersonate-User"]; !slices.Equal(got, []string{"kubemg:u:devops"}) {
		t.Fatalf("Impersonate-User = %v, want the authenticated user, prefixed", got)
	}
	for name := range out {
		if isImpersonateExtra(name) {
			t.Fatalf("a client-supplied %s survived", name)
		}
	}
	if got := out["Impersonate-Group"]; slices.Contains(got, "system:masters") {
		t.Fatalf("a client-supplied group survived: %v", got)
	}
	if _, ok := out["Authorization"]; ok {
		t.Fatal("the caller's KubeMG token must not reach the target cluster")
	}
	if _, ok := out["Connection"]; ok {
		t.Fatal("hop-by-hop headers must not be forwarded")
	}
	if got := out["User-Agent"]; !slices.Equal(got, []string{"kubectl/v1.31.4"}) {
		t.Fatalf("ordinary headers should pass through, got %v", got)
	}
}

func TestAllowedNamespace(t *testing.T) {
	scoped := db.UserClusterAccess{K8sRole: db.K8sRoleEdit, Namespaces: "team-a,team-b"}
	unscoped := db.UserClusterAccess{K8sRole: db.K8sRoleEdit}

	cases := []struct {
		name  string
		grant db.UserClusterAccess
		path  string
		want  bool
	}{
		{"unscoped grant reaches anything", unscoped, "/api/v1/pods", true},
		{"granted namespace", scoped, "/api/v1/namespaces/team-a/pods", true},
		{"other namespace", scoped, "/api/v1/namespaces/kube-system/secrets", false},
		// A cluster-wide list would return objects from namespaces the grant
		// does not cover, so it is refused rather than silently filtered.
		{"cluster-wide list under a scoped grant", scoped, "/api/v1/pods", false},
		{"node read under a scoped grant", scoped, "/api/v1/nodes", false},
		// kubectl cannot resolve a single resource without these.
		{"discovery root", scoped, "/api", true},
		{"group discovery", scoped, "/apis/apps/v1", true},
		{"version", scoped, "/version", true},
		{"openapi", scoped, "/openapi/v2", true},
		// Discovery is let past the scope for reading only.
		{"write to a discovery path", scoped, "/apis/apps/v1", false},
		// Whatever the grant, a path the API server would read differently is
		// refused before anything else reads it.
		{"double-encoded slash, scoped", scoped, "/api/v1%2Fnamespaces%2Fkube-system%2Fsecrets", false},
		{"double-encoded slash, unscoped", unscoped, "/api/v1/namespaces/team-a%2F..%2Fkube-system/secrets", false},
		{"encoded namespace", unscoped, "/api/v1/namespaces/kubemg%2Dsystem/secrets", false},
		{"fragment cuts the path", unscoped, "/api/v1/secrets#/x", false},
		{"dot segments", scoped, "/api/v1/namespaces/team-a/pods/../../kube-system/pods", false},
		{"empty segment", unscoped, "/api/v1/namespaces//pods", false},
		{"trailing slash on discovery", scoped, "/apis/", true},
		// Past a proxy subresource the path is the proxied service's.
		{"service proxy tail", scoped, "/api/v1/namespaces/team-a/services/http:web:80/proxy/a%2Fb", true},
		{"encoded service proxy head", scoped, "/api/v1/namespaces/team-a/services/http:web%3A80/proxy/", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			method := http.MethodGet
			if strings.HasPrefix(tc.name, "write") {
				method = http.MethodPost
			}
			got, reason := allowedPath(tc.grant, method, ParsePath(tc.path), tc.path)
			if got != tc.want {
				t.Fatalf("allowedPath(%s %q) = %v (%s), want %v", method, tc.path, got, reason, tc.want)
			}
			if !got && reason == "" {
				t.Fatal("a refusal must explain itself")
			}
		})
	}
}

func TestStreamRouting(t *testing.T) {
	cases := []struct {
		path    string
		upgrade bool
		body    bool
	}{
		// Interactive sessions need a channel in both directions.
		{path: "/api/v1/namespaces/team-a/pods/web-0/exec?command=sh", upgrade: true},
		{path: "/api/v1/namespaces/team-a/pods/web-0/attach", upgrade: true},
		// These keep a response body open instead.
		{path: "/api/v1/namespaces/team-a/pods/web-0/log?follow=true", body: true},
		{path: "/api/v1/namespaces/team-a/pods?watch=true", body: true},
		{path: "/api/v1/namespaces/team-a/pods?watch=1&timeoutSeconds=60", body: true},
		// However the API server is asked to stream, it streams.
		{path: "/api/v1/namespaces/team-a/pods/web-0/log?follow=True", body: true},
		{path: "/api/v1/namespaces/team-a/pods/web-0/log?follow=t&tailLines=10", body: true},
		{path: "/api/v1/namespaces/team-a/pods/web-0/log?follow", body: true},
		{path: "/api/v1/namespaces/team-a/pods?watch=TRUE", body: true},
		{path: "/api/v1/namespaces/team-a/pods?watch=yes", body: true},
		// A bounded log read is an ordinary request/response pair.
		{path: "/api/v1/namespaces/team-a/pods/web-0/log?tailLines=100"},
		{path: "/api/v1/namespaces/team-a/pods/web-0/log?follow=false"},
		{path: "/api/v1/namespaces/team-a/pods/web-0/log?follow=0&follow=1"},
		{path: "/api/v1/namespaces/team-a/pods?watch=False"},
		{path: "/api/v1/namespaces/team-a/pods"},
		// follow means something only on a log.
		{path: "/api/v1/namespaces/team-a/pods?follow=true"},
		// watch means something only on a collection: the API server answers a
		// named object once, and a log streams only when followed.
		{path: "/api/v1/namespaces/team-a/pods/web-0?watch=true"},
		{path: "/api/v1/nodes/worker-1?watch=1"},
		{path: "/api/v1/namespaces/team-a/pods/web-0/log?watch=true"},
		// One object watched through its field selector streams.
		{path: "/api/v1/namespaces/team-a/pods?fieldSelector=metadata.name%3Dweb-0&watch=true", body: true},
	}

	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			parsed := ParsePath(tc.path)
			if got := wantsUpgrade(parsed); got != tc.upgrade {
				t.Errorf("wantsUpgrade(%q) = %v, want %v", tc.path, got, tc.upgrade)
			}
			if got := wantsBodyStream(parsed, tc.path); got != tc.body {
				t.Errorf("wantsBodyStream(%q) = %v, want %v", tc.path, got, tc.body)
			}
		})
	}
}

// port-forward is carried in its WebSocket transport and only that one. A SPDY
// client has to be told so at the handshake: an honest 501 naming the fix beats
// an upgrade that hangs.
func TestPortForwardRefusesSPDYAndAcceptsWebSocket(t *testing.T) {
	const path = "/api/v1/namespaces/team-a/pods/web-0/portforward?ports=8080"
	parsed := ParsePath(path)

	spdy := httptest.NewRequest(http.MethodPost, path, nil)
	spdy.Header.Set("Connection", "Upgrade")
	spdy.Header.Set("Upgrade", "SPDY/3.1")

	reason, refused := unsupportedStream(spdy, parsed)
	if !refused {
		t.Fatal("a SPDY port-forward must be refused rather than stalled")
	}
	if !strings.Contains(reason, "WebSocket") {
		t.Fatalf("the refusal must name the transport that works, got %q", reason)
	}

	socket := httptest.NewRequest(http.MethodGet, path, nil)
	socket.Header.Set("Connection", "Upgrade")
	socket.Header.Set("Upgrade", "websocket")
	socket.Header.Set("Sec-Websocket-Version", "13")
	socket.Header.Set("Sec-Websocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	socket.Header.Set("Sec-Websocket-Protocol", "v2.portforward.k8s.io")

	if _, refused := unsupportedStream(socket, parsed); refused {
		t.Fatal("a WebSocket port-forward is carried and must not be refused")
	}
	if !wantsUpgrade(parsed) {
		t.Fatal("port-forward is an upgraded session and must route as one")
	}
	// The two subprotocol families are not interchangeable: negotiating a
	// port-forward as a channel protocol yields a session neither end can read.
	if got := offeredSubprotocols(parsed); !slices.Equal(got, PortForwardSubprotocols) {
		t.Fatalf("port-forward must offer the port-forward subprotocols, got %v", got)
	}
	if got := offeredSubprotocols(ParsePath("/api/v1/namespaces/team-a/pods/web-0/exec")); !slices.Equal(
		got, ChannelSubprotocols,
	) {
		t.Fatalf("exec must offer the channel subprotocols, got %v", got)
	}
}

// A stored audit diff must never describe a write that did not actually
// happen: a guardrail block or a tunnel failure never reaches Call's tail at
// all, so the only case worth pinning here is a status the cluster itself
// sent back on a completed round trip.
func TestDiffForAuditRowOnlyRidesOnSuccess(t *testing.T) {
	diff := []byte(`{"changes":[]}`)

	cases := []struct {
		name   string
		diff   []byte
		status int
		want   bool
	}{
		{"200 keeps the diff", diff, http.StatusOK, true},
		{"201 keeps the diff", diff, http.StatusCreated, true},
		{"403 from the cluster's own RBAC drops it", diff, http.StatusForbidden, false},
		{"409 stale resourceVersion drops it", diff, http.StatusConflict, false},
		{"500 drops it", diff, http.StatusInternalServerError, false},
		{"no diff to begin with stays nil", nil, http.StatusOK, false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got := diffForAuditRow(test.diff, test.status)
			if test.want && got == nil {
				t.Fatalf("expected the diff to survive status %d", test.status)
			}
			if !test.want && got != nil {
				t.Fatalf("expected the diff to be dropped for status %d, got %s", test.status, got)
			}
		})
	}
}

func TestTunnelFailureMapsToUsefulStatuses(t *testing.T) {
	status, _ := tunnelFailure(ErrNoTunnel)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("a missing tunnel should be 503, got %d", status)
	}
	status, _ = tunnelFailure(ErrTunnelClosed)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("a dropped tunnel should be 503, got %d", status)
	}
}

// The in-page terminal names the shell to exec in the query string, and the
// only parameter KubeMG removes on the way through is its own session token.
// Dropping `command` would silently exec the container's entrypoint instead of
// the shell the operator picked, so what survives the strip is pinned here.
func TestStrippedQueryKeepsExecParameters(t *testing.T) {
	raw := "container=web&stdin=true&tty=true&command=%2Fbin%2Fsh&access_token=secret"

	got := strippedQuery(raw)
	if strings.Contains(got, "access_token") {
		t.Fatalf("the session token reached the cluster: %q", got)
	}
	if !strings.Contains(got, "command=%2Fbin%2Fsh") {
		t.Fatalf("the chosen shell was dropped: %q", got)
	}
	if !strings.Contains(got, "container=web") || !strings.Contains(got, "tty=true") {
		t.Fatalf("the exec parameters were mangled: %q", got)
	}
}

// A merge patch has to say so in its content type. The API server accepts four
// patch media types and `application/json` is not one of them, so getting this
// wrong is a 415 on every patch — and both callers that patch are best-effort,
// which means it would read as an annotation that quietly never lands.
func TestPatchIsSentAsAMergePatch(t *testing.T) {
	if got := contentTypeFor(http.MethodPatch); got != "application/merge-patch+json" {
		t.Fatalf("PATCH content type = %q, want a patch strategy the API server accepts", got)
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodGet} {
		if got := contentTypeFor(method); got != "application/json" {
			t.Fatalf("%s content type = %q, want application/json", method, got)
		}
	}
}

// The reviewer's reproduction, turned around: whatever an account is called,
// the API server must never see a name it already trusts.
func TestImpersonationUserPrefixesEveryAccount(t *testing.T) {
	cases := []struct {
		name string
		user db.User
		want string
	}{
		{"a person", db.User{ID: 7, Username: "ada"}, "kubemg:u:ada"},
		{"an email-shaped name", db.User{ID: 7, Username: "ada@example.com"}, "kubemg:u:ada@example.com"},
		{"a service account's name", db.User{ID: 7, Username: "system:serviceaccount:kube-system:backup-operator"},
			"kubemg:u:system:serviceaccount:kube-system:backup-operator"},
		{"a stored row named like an internal identity", db.User{ID: 7, Username: "kubemg:alarm-watcher"},
			"kubemg:u:kubemg:alarm-watcher"},
		{"an unsaved row without the kubemg: prefix", db.User{Username: "system:masters"}, "kubemg:u:system:masters"},
		{"an unsaved row already in the user space", db.User{Username: "kubemg:u:ada"}, "kubemg:u:kubemg:u:ada"},
		// KubeMG's own identities are built in code, never stored, and are bound
		// by name in the agent's manifests — they must reach the cluster as-is.
		{"the alarm watcher", db.User{Username: "kubemg:alarm-watcher"}, "kubemg:alarm-watcher"},
		{"the shell runner", db.User{Username: "kubemg:shell-runner"}, "kubemg:shell-runner"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ImpersonationUser(&tc.user); got != tc.want {
				t.Fatalf("ImpersonationUser(%q, id %d) = %q, want %q", tc.user.Username, tc.user.ID, got, tc.want)
			}
		})
	}
}

// A namespace-scoped caller must not reach another namespace by encoding the
// slashes twice: the gateway decodes once, the API server once more. The call
// is refused before the agent is asked anything, and the refusal is recorded.
func TestProxyRefusesADoubleEncodedPath(t *testing.T) {
	h := newHarness(t)
	h.addCluster(1, "prod-eu", "kmg_valid")
	dev := h.addUser(11, "dev", db.SystemRoleUser)
	h.grant(dev.ID, 1, db.K8sRoleEdit, []string{"team-a"})

	agent, err := h.dialAgent("kmg_valid", okResponse("{}"))
	if err != nil {
		t.Fatalf("dial agent: %v", err)
	}
	waitFor(t, func() bool { return h.gateway.Registry().Connected(1) })

	for _, method := range []string{http.MethodGet, http.MethodPost} {
		resp := h.proxyRequest(method,
			"/api/v1/clusters/1/proxy/api/v1%252Fnamespaces%252Fkube-system%252Fsecrets", h.token(dev))
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s: want 403, got %d", method, resp.StatusCode)
		}
	}
	// %23 and %3F arrive decoded: forwarded, they would cut the path short or
	// start the query early at the agent.
	for _, path := range []string{
		"/api/v1/namespaces/team-a/pods/x%23/y",
		"/api/v1/namespaces/team-a/pods%3Fwatch=1",
	} {
		resp := h.proxyRequest(http.MethodGet, "/api/v1/clusters/1/proxy"+path, h.token(dev))
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s: want 403, got %d", path, resp.StatusCode)
		}
	}
	if got := len(agent.requests()); got != 0 {
		t.Fatalf("the call must never reach the agent, got %d requests", got)
	}
	waitFor(t, func() bool { return len(h.audit.events()) >= 4 })
	for _, event := range h.audit.events() {
		if event.RefusedBy != RefusedByKubeMG || event.Status != http.StatusForbidden {
			t.Fatalf("want a refusal by kubemg, got %+v", event)
		}
	}
}

// Below cluster-admin, nothing reaches the agent's namespace — its Secret, its
// ServiceAccount and every shell live there — and no kubectl read of every
// Secret at once, which would carry the agent's. KubeMG's own unsaved service
// identities manage that namespace and are not refused.
func TestAgentNamespaceIsRefusedBelowClusterAdmin(t *testing.T) {
	p := NewProxy(ProxyOptions{AgentNamespaces: []string{"kubemg-system"}})
	p.SetAgentNamespaces("kubemg-agents", "kubemg-system", "")
	person := &db.User{ID: 4, Username: "dev"}
	runner := &db.User{Username: "kubemg:shell-runner"}
	edit := db.UserClusterAccess{K8sRole: db.K8sRoleEdit}
	admin := db.UserClusterAccess{K8sRole: db.K8sRoleClusterAdmin}

	for _, path := range []string{
		"/api/v1/namespaces/kubemg-system/secrets/kubemg-agent",
		"/api/v1/namespaces/kubemg-agents/pods",
		"/api/v1/namespaces/kubemg-system/pods/kubemg-shell-1/exec?command=sh",
		"/api/v1/namespaces/kubemg-system/serviceaccounts/kubemg-agent/token",
	} {
		parsed := ParsePath(path)
		if p.agentNamespaceRefusal(person, edit, parsed) == "" {
			t.Errorf("%s: an edit grant was let in", path)
		}
		if reason := p.agentNamespaceRefusal(person, admin, parsed); reason != "" {
			t.Errorf("%s: a cluster-admin grant was refused: %s", path, reason)
		}
		if reason := p.agentNamespaceRefusal(runner, db.UserClusterAccess{K8sRole: db.K8sRoleView}, parsed); reason != "" {
			t.Errorf("%s: KubeMG's own runner was refused: %s", path, reason)
		}
	}
	if reason := p.agentNamespaceRefusal(person, edit, ParsePath("/api/v1/namespaces/team-a/secrets")); reason != "" {
		t.Fatalf("an ordinary namespace was refused: %s", reason)
	}

	if clusterWideSecretRefusal(person, edit, ParsePath("/api/v1/secrets?watch=1")) == "" {
		t.Fatal("a cluster-wide Secret read was let through below cluster-admin")
	}
	if reason := clusterWideSecretRefusal(person, admin, ParsePath("/api/v1/secrets")); reason != "" {
		t.Fatalf("a cluster-admin's cluster-wide Secret read was refused: %s", reason)
	}
	if reason := clusterWideSecretRefusal(person, edit, ParsePath("/api/v1/pods")); reason != "" {
		t.Fatalf("a cluster-wide pod read was refused: %s", reason)
	}
}

// Through the gateway: kubectl into the agent's namespace is refused by kubemg
// and recorded, and the agent is never asked.
func TestProxyRefusesTheAgentNamespaceToAnEditGrant(t *testing.T) {
	h := newHarness(t)
	h.proxy.SetAgentNamespaces("kubemg-system")
	h.addCluster(1, "prod-eu", "kmg_valid")
	dev := h.addUser(11, "dev", db.SystemRoleUser)
	h.grant(dev.ID, 1, db.K8sRoleEdit, nil)

	agent, err := h.dialAgent("kmg_valid", okResponse("{}"))
	if err != nil {
		t.Fatalf("dial agent: %v", err)
	}
	waitFor(t, func() bool { return h.gateway.Registry().Connected(1) })

	for _, path := range []string{
		"/api/v1/namespaces/kubemg-system/secrets/kubemg-agent",
		"/api/v1/secrets",
	} {
		resp := h.proxyRequest(http.MethodGet, "/api/v1/clusters/1/proxy"+path, h.token(dev))
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s: want 403, got %d", path, resp.StatusCode)
		}
	}
	if got := len(agent.requests()); got != 0 {
		t.Fatalf("the call must never reach the agent, got %d requests", got)
	}

	resp := h.proxyRequest(http.MethodGet, "/api/v1/clusters/1/proxy/api/v1/namespaces/team-a/secrets", h.token(dev))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("an ordinary namespace: want 200, got %d", resp.StatusCode)
	}
}
