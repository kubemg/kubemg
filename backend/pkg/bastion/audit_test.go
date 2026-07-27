package bastion

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"testing"
	"time"
)

func TestParsePath(t *testing.T) {
	cases := []struct {
		path string
		want APIPath
	}{
		{"/api/v1/namespaces/team-a/pods", APIPath{Namespace: "team-a", Resource: "pods"}},
		{"/api/v1/namespaces/team-a/pods/web-0", APIPath{Namespace: "team-a", Resource: "pods", Name: "web-0"}},
		{
			"/api/v1/namespaces/team-a/pods/web-0/log?tailLines=10",
			APIPath{Namespace: "team-a", Resource: "pods", Name: "web-0", Subresource: "log"},
		},
		{"/api/v1/pods", APIPath{Resource: "pods"}},
		{"/api/v1/nodes/worker-1", APIPath{Resource: "nodes", Name: "worker-1"}},
		// A bare /namespaces/<ns> is an operation on the namespace object, not
		// a namespace-scoped call.
		{"/api/v1/namespaces/team-a", APIPath{Resource: "namespaces", Name: "team-a"}},
		{
			"/apis/apps/v1/namespaces/team-a/deployments/web",
			APIPath{Namespace: "team-a", Resource: "deployments", Name: "web"},
		},
		{"/apis/apps/v1/deployments", APIPath{Resource: "deployments"}},
		// Not API-shaped at all.
		{"/version", APIPath{}},
		{"/healthz", APIPath{}},
		{"/", APIPath{}},
	}

	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			if got := ParsePath(tc.path); got != tc.want {
				t.Fatalf("ParsePath(%q) = %+v, want %+v", tc.path, got, tc.want)
			}
		})
	}
}

func TestVerbFor(t *testing.T) {
	cases := []struct {
		method string
		path   string
		want   string
	}{
		{http.MethodGet, "/api/v1/namespaces/team-a/pods", "list"},
		{http.MethodGet, "/api/v1/namespaces/team-a/pods/web-0", "get"},
		{http.MethodGet, "/api/v1/namespaces/team-a/pods?watch=true", "watch"},
		{http.MethodGet, "/api/v1/namespaces/team-a/pods?watch", "watch"},
		// The sensitive ones are named for what they are, not for their method.
		{http.MethodGet, "/api/v1/namespaces/team-a/pods/web-0/exec?command=sh", "exec"},
		{http.MethodPost, "/api/v1/namespaces/team-a/pods/web-0/exec?command=sh", "exec"},
		{http.MethodGet, "/api/v1/namespaces/team-a/pods/web-0/attach", "attach"},
		{http.MethodGet, "/api/v1/namespaces/team-a/pods/web-0/log?tailLines=10", "log"},
		{http.MethodPost, "/api/v1/namespaces/team-a/pods", "create"},
		{http.MethodPut, "/api/v1/namespaces/team-a/pods/web-0", "update"},
		{http.MethodPatch, "/api/v1/namespaces/team-a/pods/web-0", "patch"},
		{http.MethodDelete, "/api/v1/namespaces/team-a/pods/web-0", "delete"},
		{http.MethodHead, "/version", "head"},
	}

	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			if got := VerbFor(tc.method, tc.path); got != tc.want {
				t.Fatalf("VerbFor(%q, %q) = %q, want %q", tc.method, tc.path, got, tc.want)
			}
		})
	}
}

func TestAuditorRecordsTheImpersonatedIdentity(t *testing.T) {
	var buf bytes.Buffer
	auditor := NewAuditor(slog.New(slog.NewJSONHandler(&buf, nil)))

	auditor.Record(context.Background(), Event{
		At:                 time.Now().UTC(),
		UserID:             7,
		Username:           "devops",
		ClusterID:          3,
		Cluster:            "prod-eu",
		Verb:               "list",
		Method:             http.MethodGet,
		Path:               "/api/v1/namespaces/team-a/pods",
		Namespace:          "team-a",
		Resource:           "pods",
		ImpersonatedUser:   "devops",
		ImpersonatedGroups: []string{"kubemg:view", "kubemg:users"},
		Status:             http.StatusOK,
		Duration:           120 * time.Millisecond,
	})

	var record map[string]any
	if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
		t.Fatalf("audit record is not JSON: %v (%s)", err, buf.String())
	}

	want := map[string]any{
		"audit":              "kubemg.proxy",
		"username":           "devops",
		"cluster":            "prod-eu",
		"verb":               "list",
		"uri":                "/api/v1/namespaces/team-a/pods",
		"impersonate_user":   "devops",
		"impersonate_groups": "kubemg:view,kubemg:users",
	}
	for key, expected := range want {
		if record[key] != expected {
			t.Errorf("audit[%q] = %v, want %v", key, record[key], expected)
		}
	}
	if record["status_code"] != float64(http.StatusOK) {
		t.Errorf("audit status_code = %v", record["status_code"])
	}
	if record["level"] != "INFO" {
		t.Errorf("a successful call should log at INFO, got %v", record["level"])
	}
}

func TestAuditorEscalatesRefusals(t *testing.T) {
	var buf bytes.Buffer
	auditor := NewAuditor(slog.New(slog.NewJSONHandler(&buf, nil)))

	auditor.Record(context.Background(), Event{
		Username: "devops",
		Status:   http.StatusForbidden,
		Error:    "namespace kube-system is outside your granted scope",
	})

	var record map[string]any
	if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
		t.Fatalf("audit record is not JSON: %v", err)
	}
	if record["level"] != "WARN" {
		t.Fatalf("a denied call should log at WARN, got %v", record["level"])
	}
	if record["error"] == nil {
		t.Fatal("a denied call must record why it was denied")
	}
}
