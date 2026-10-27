package bastion

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

func jsonResponse(status int, body string) *Response {
	return &Response{
		Status: status,
		Header: map[string][]string{"Content-Type": {"application/json"}},
		Body:   []byte(body),
	}
}

func TestObjectName(t *testing.T) {
	created := `{"kind":"Deployment","metadata":{"name":"web","namespace":"payments"}}`
	cases := []struct {
		name   string
		method string
		path   string
		resp   *Response
		want   string
	}{
		{"create takes the answer's name", http.MethodPost,
			"/apis/apps/v1/namespaces/payments/deployments", jsonResponse(201, created), "web"},
		// generateName is resolved by the API server; only its answer knows it.
		{"create with generateName", http.MethodPost,
			"/apis/batch/v1/namespaces/ops/jobs",
			jsonResponse(201, `{"metadata":{"generateName":"backup-","name":"backup-x7k2p"}}`), "backup-x7k2p"},
		{"create of a cluster-scoped object", http.MethodPost,
			"/api/v1/namespaces", jsonResponse(201, `{"metadata":{"name":"payments"}}`), "payments"},
		{"get names its object in the path", http.MethodGet,
			"/api/v1/namespaces/team-a/pods/web-0", jsonResponse(200, `{"metadata":{"name":"other"}}`), "web-0"},
		{"update names its object in the path", http.MethodPut,
			"/apis/apps/v1/namespaces/team-a/deployments/web", nil, "web"},
		{"delete names its object in the path", http.MethodDelete,
			"/apis/apps/v1/namespaces/team-a/deployments/web", nil, "web"},
		{"a subresource names its parent", http.MethodPost,
			"/api/v1/namespaces/team-a/pods/web-0/eviction", jsonResponse(201, `{}`), "web-0"},
		{"a list names nothing", http.MethodGet,
			"/api/v1/namespaces/team-a/pods", jsonResponse(200, `{"metadata":{"resourceVersion":"9"}}`), ""},
		// A refused create made nothing; the Status it answers with is not an
		// object, and its details are not read as one.
		{"refused create is not guessed at", http.MethodPost,
			"/apis/apps/v1/namespaces/payments/deployments",
			jsonResponse(409, `{"kind":"Status","metadata":{},"reason":"AlreadyExists","details":{"name":"web"}}`), ""},
		{"forbidden create", http.MethodPost,
			"/apis/apps/v1/namespaces/payments/deployments", jsonResponse(403, created), ""},
		{"tunnel failure, no answer", http.MethodPost,
			"/apis/apps/v1/namespaces/payments/deployments", nil, ""},
		{"compressed answer is left alone", http.MethodPost,
			"/apis/apps/v1/namespaces/payments/deployments",
			&Response{Status: 201, Header: map[string][]string{"Content-Encoding": {"gzip"}}, Body: []byte(created)}, ""},
		{"protobuf answer is left alone", http.MethodPost,
			"/apis/apps/v1/namespaces/payments/deployments",
			&Response{Status: 201, Header: map[string][]string{"Content-Type": {"application/vnd.kubernetes.protobuf"}}, Body: []byte(created)}, ""},
		{"unparseable answer", http.MethodPost,
			"/apis/apps/v1/namespaces/payments/deployments", jsonResponse(201, `not json`), ""},
		{"a name no object could have is dropped", http.MethodPost,
			"/apis/apps/v1/namespaces/payments/deployments", jsonResponse(201, `{"metadata":{"name":"web\nforged"}}`), ""},
		{"an over-long name is dropped", http.MethodPost,
			"/apis/apps/v1/namespaces/payments/deployments",
			jsonResponse(201, `{"metadata":{"name":"`+strings.Repeat("a", maxObjectName+1)+`"}}`), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ObjectName(ParsePath(tc.path), VerbFor(tc.method, tc.path), tc.resp)
			if got != tc.want {
				t.Fatalf("ObjectName = %q, want %q", got, tc.want)
			}
		})
	}
}

// The console's create path: the agent answers gzip, Call decodes it, and the
// record names what was made.
func TestCallNamesTheObjectACreateMade(t *testing.T) {
	h := newHarness(t)
	cluster := h.addCluster(1, "prod-eu", "kmg_valid")
	user := h.addUser(10, "alice", db.SystemRoleUser)

	if _, err := h.dialAgent("kmg_valid", func(Request) Response {
		resp := gzipResponse(gzipped(t, []byte(`{"kind":"Deployment","metadata":{"name":"cert-manager"}}`)))
		resp.Status = http.StatusCreated
		return *resp
	}); err != nil {
		t.Fatalf("dial agent: %v", err)
	}
	waitFor(t, func() bool { return h.gateway.Registry().Connected(1) })

	grant := db.UserClusterAccess{UserID: user.ID, ClusterID: cluster.ID, K8sRole: db.K8sRoleEdit}
	if _, err := h.proxy.Call(context.Background(), user, cluster, grant, http.MethodPost,
		"/apis/apps/v1/namespaces/cert-manager/deployments", []byte(`{}`), nil); err != nil {
		t.Fatalf("Call: %v", err)
	}

	events := h.audit.events()
	if len(events) != 1 || events[0].Verb != "create" || events[0].Name != "cert-manager" {
		t.Fatalf("audit = %+v, want one create naming cert-manager", events)
	}
}

func TestCallLeavesARefusedCreateUnnamed(t *testing.T) {
	h := newHarness(t)
	cluster := h.addCluster(1, "prod-eu", "kmg_valid")
	user := h.addUser(10, "alice", db.SystemRoleUser)

	if _, err := h.dialAgent("kmg_valid", func(Request) Response {
		return *jsonResponse(http.StatusConflict,
			`{"kind":"Status","metadata":{},"reason":"AlreadyExists","details":{"name":"web"}}`)
	}); err != nil {
		t.Fatalf("dial agent: %v", err)
	}
	waitFor(t, func() bool { return h.gateway.Registry().Connected(1) })

	grant := db.UserClusterAccess{UserID: user.ID, ClusterID: cluster.ID, K8sRole: db.K8sRoleEdit}
	_, _ = h.proxy.Call(context.Background(), user, cluster, grant, http.MethodPost,
		"/apis/apps/v1/namespaces/payments/deployments", []byte(`{"metadata":{"name":"web"}}`), nil)

	events := h.audit.events()
	if len(events) != 1 || events[0].Status != http.StatusConflict || events[0].Name != "" {
		t.Fatalf("audit = %+v, want one unnamed 409", events)
	}
}

// kubectl's path through Handle: a plain JSON answer is read for the name.
func TestProxyNamesTheObjectACreateMade(t *testing.T) {
	h := newHarness(t)
	h.addCluster(1, "prod-eu", "kmg_valid")
	user := h.addUser(10, "devops", db.SystemRoleUser)
	h.grant(10, 1, db.K8sRoleEdit, nil)

	if _, err := h.dialAgent("kmg_valid", func(Request) Response {
		return *jsonResponse(http.StatusCreated, `{"kind":"ConfigMap","metadata":{"name":"settings-4f9x"}}`)
	}); err != nil {
		t.Fatalf("dial agent: %v", err)
	}
	waitFor(t, func() bool { return h.gateway.Registry().Connected(1) })

	resp := h.proxyRequest(http.MethodPost,
		"/api/v1/clusters/1/proxy/api/v1/namespaces/team-a/configmaps", h.token(user))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}

	waitFor(t, func() bool { return len(h.audit.events()) == 1 })
	if got := h.audit.events()[0]; got.Name != "settings-4f9x" || got.Resource != "configmaps" {
		t.Fatalf("audit = %+v, want the created ConfigMap named", got)
	}
}

func TestAuditorRecordsTheObjectName(t *testing.T) {
	var buf bytes.Buffer
	auditor := NewAuditor(slog.New(slog.NewJSONHandler(&buf, nil)))
	auditor.Record(context.Background(), Event{
		Verb: "create", Namespace: "payments", Resource: "deployments", Name: "web", Status: 201,
	})

	var record map[string]any
	if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
		t.Fatalf("audit record is not JSON: %v", err)
	}
	if record["name"] != "web" {
		t.Fatalf("audit[name] = %v, want web", record["name"])
	}
}

func TestStoredRowCarriesTheObjectName(t *testing.T) {
	row := toAuditRow(context.Background(), Event{Verb: "create", Resource: "deployments", Name: "web"})
	if row.Name != "web" {
		t.Fatalf("row.Name = %q, want web", row.Name)
	}
}
