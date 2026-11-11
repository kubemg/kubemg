package bastion

import (
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

// kubectl watches one object through a field selector on the collection, and
// the API server resolves that selector into the request's name. The record
// carries the same name, so searching the trail for the object finds its
// watches too.
func TestProxyNamesTheObjectASingleObjectWatchAddresses(t *testing.T) {
	h := newHarness(t)
	h.addCluster(1, "prod-eu", "kmg_valid")
	admin := h.addUser(10, "admin", db.SystemRoleAdmin)

	agent, err := h.dialStreamingAgent("kmg_valid", okResponse("{}"),
		echoStream(`{"type":"MODIFIED"}`+"\n"))
	if err != nil {
		t.Fatalf("dial agent: %v", err)
	}
	waitFor(t, func() bool { return h.gateway.Registry().Connected(1) })

	// What `kubectl get pod web-0 -w` sends.
	resp := h.proxyRequest(http.MethodGet,
		"/api/v1/clusters/1/proxy/api/v1/namespaces/team-a/pods?fieldSelector=metadata.name%3Dweb-0&watch=true",
		h.token(admin))
	body, _ := io.ReadAll(resp.Body)
	if string(body) != `{"type":"MODIFIED"}`+"\n" {
		t.Fatalf("watch body did not stream through: %q", body)
	}
	if len(agent.streamOpens()) != 1 || len(agent.requests()) != 0 {
		t.Fatalf("want one stream open and no request/response pair, got %d opens and %d requests",
			len(agent.streamOpens()), len(agent.requests()))
	}

	waitFor(t, func() bool { return len(h.audit.events()) >= 2 })
	for _, event := range h.audit.events() {
		if event.Verb != "watch" || !event.Streaming || event.Name != "web-0" {
			t.Fatalf("want a streaming watch naming web-0, got verb %q streaming %v name %q",
				event.Verb, event.Streaming, event.Name)
		}
	}
}

// The API server reads watch only on a collection. A GET on a named object is
// served by its get handler, one answer whatever the query says, so it takes
// the request/response path and is recorded as the get RBAC saw.
func TestProxyServesAWatchOnANamedObjectAsAGet(t *testing.T) {
	h := newHarness(t)
	h.addCluster(1, "prod-eu", "kmg_valid")
	admin := h.addUser(10, "admin", db.SystemRoleAdmin)

	agent, err := h.dialStreamingAgent("kmg_valid",
		okResponse(`{"kind":"Pod","metadata":{"name":"web-0"}}`), echoStream())
	if err != nil {
		t.Fatalf("dial agent: %v", err)
	}
	waitFor(t, func() bool { return h.gateway.Registry().Connected(1) })

	resp := h.proxyRequest(http.MethodGet,
		"/api/v1/clusters/1/proxy/api/v1/namespaces/team-a/pods/web-0?watch=true", h.token(admin))
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != `{"kind":"Pod","metadata":{"name":"web-0"}}` {
		t.Fatalf("want the object answered once, got %d %q", resp.StatusCode, body)
	}
	if len(agent.requests()) != 1 || len(agent.streamOpens()) != 0 {
		t.Fatalf("want one request/response pair and no stream, got %d requests and %d opens",
			len(agent.requests()), len(agent.streamOpens()))
	}

	waitFor(t, func() bool { return len(h.audit.events()) >= 1 })
	events := h.audit.events()
	if len(events) != 1 || events[0].Verb != "get" || events[0].Streaming || events[0].Name != "web-0" {
		t.Fatalf("want one non-streaming get naming web-0, got %+v", events)
	}
}

// The console's watches go through Watch, and its record is named the same way.
func TestWatchNamesTheObjectItsSelectorAddresses(t *testing.T) {
	h := newHarness(t)
	cluster := h.addCluster(1, "prod-eu", "kmg_valid")
	user := h.addUser(10, "alice", db.SystemRoleUser)

	if _, err := h.dialStreamingAgent("kmg_valid", okResponse("{}"),
		echoStream(`{"type":"ADDED"}`+"\n")); err != nil {
		t.Fatalf("dial agent: %v", err)
	}
	waitFor(t, func() bool { return h.gateway.Registry().Connected(1) })

	grant := db.UserClusterAccess{UserID: user.ID, ClusterID: cluster.ID, K8sRole: db.K8sRoleView}
	stream, err := h.proxy.Watch(context.Background(), user, cluster, grant,
		"/apis/apps/v1/namespaces/team-a/deployments?fieldSelector=metadata.name%3Dweb&watch=true")
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	stream.Close(nil)

	events := h.audit.events()
	if len(events) != 1 || events[0].Verb != "watch" || events[0].Name != "web" {
		t.Fatalf("audit = %+v, want one watch naming web", events)
	}
}
