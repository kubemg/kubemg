package bastion

import (
	"net/http"
	"slices"
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
		"Connection":        []string{"keep-alive"},
		"User-Agent":        []string{"kubectl/v1.31.4"},
	}

	out := forwardHeaders(src, &db.User{Username: "devops"}, db.UserClusterAccess{K8sRole: db.K8sRoleView})

	// A client that could pick its own identity would defeat the gateway.
	if got := out["Impersonate-User"]; !slices.Equal(got, []string{"devops"}) {
		t.Fatalf("Impersonate-User = %v, want the authenticated user", got)
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
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := allowedNamespace(tc.grant, ParsePath(tc.path), tc.path)
			if got != tc.want {
				t.Fatalf("allowedNamespace(%q) = %v (%s), want %v", tc.path, got, reason, tc.want)
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
		// A bounded log read is an ordinary request/response pair.
		{path: "/api/v1/namespaces/team-a/pods/web-0/log?tailLines=100"},
		{path: "/api/v1/namespaces/team-a/pods"},
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

func TestPortForwardIsStillRefusedNotStalled(t *testing.T) {
	// It multiplexes arbitrary TCP inside the session, which this framing does
	// not carry. An honest 501 beats a hung client.
	reason, refused := unsupportedStream(ParsePath("/api/v1/namespaces/team-a/pods/web-0/portforward"))
	if !refused {
		t.Fatal("port-forward must be refused until the tunnel can carry it")
	}
	if reason == "" {
		t.Fatal("a refusal must explain itself")
	}

	if _, refused := unsupportedStream(ParsePath("/api/v1/namespaces/team-a/pods/web-0/exec")); refused {
		t.Fatal("exec is carried now and must not be refused")
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
