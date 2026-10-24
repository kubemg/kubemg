package observability

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

func argoTarget() Target {
	return Target{
		Kind: db.SourceGitOps, Provider: db.ProviderArgoCD, AccessMode: db.AccessInCluster,
		ServiceNamespace: "argocd", ServiceName: "argocd-server", ServicePort: "443", ServiceScheme: "https",
		AuthMode: db.AuthBearer, Credential: "tok.en-1",
	}
}

func TestArgoSourceValidation(t *testing.T) {
	ok := argoTarget()
	if err := ok.Validate(); err != nil {
		t.Fatalf("expected an in-cluster bearer source to validate, got %v", err)
	}
	for name, mutate := range map[string]func(*Target){
		"no auth":        func(t *Target) { t.AuthMode, t.Credential = db.AuthNone, "" },
		"basic auth":     func(t *Target) { t.AuthMode, t.Username = db.AuthBasic, "admin" },
		"no token":       func(t *Target) { t.Credential = "  " },
		"wrong provider": func(t *Target) { t.Provider = db.ProviderPrometheus },
	} {
		t.Run(name, func(t *testing.T) {
			target := argoTarget()
			mutate(&target)
			if err := target.Validate(); err == nil {
				t.Fatal("expected the source refused")
			}
		})
	}
	if err := (Target{Kind: db.SourceMetrics, Provider: db.ProviderArgoCD, AccessMode: db.AccessDirect,
		URL: "https://argocd.example.com", AuthMode: db.AuthBearer, Credential: "x"}).Validate(); err == nil {
		t.Fatal("expected Argo CD refused as a metrics provider")
	}
}

// In-cluster, the token travels as Argo CD's session cookie: the API server's
// Service proxy consumes Authorization for its own authentication.
func TestArgoInClusterCarriesTheTokenAsACookie(t *testing.T) {
	var header http.Header
	tunnel := func(_ context.Context, _, _ string, h http.Header, _ []byte) (int, []byte, error) {
		header = h
		return http.StatusOK, []byte(`{"loggedIn":true,"username":"kubemg"}`), nil
	}
	_, _, _ = call(context.Background(), argoTarget(), "/api/v1/session/userinfo", tunnel)
	if got := header.Get("Cookie"); got != "argocd.token=tok.en-1" {
		t.Fatalf("expected the argocd.token cookie, got %q", got)
	}
	if header.Get("Authorization") != "" {
		t.Fatal("expected no Authorization header down the tunnel")
	}

	metrics := metricsTarget()
	metrics.AuthMode, metrics.Credential = db.AuthBearer, "secret"
	header = nil
	_, _, _ = call(context.Background(), metrics, "/api/v1/query?query=1", tunnel)
	if header != nil {
		t.Fatalf("expected no header for any other provider, got %v", header)
	}
}

func argoProbeTunnel(userinfo string, listStatus int, list string) TunnelCall {
	return func(_ context.Context, _, path string, _ http.Header, _ []byte) (int, []byte, error) {
		switch {
		case strings.HasSuffix(path, "/api/v1/session/userinfo"):
			return http.StatusOK, []byte(userinfo), nil
		case strings.Contains(path, "/api/v1/applications"):
			return listStatus, []byte(list), nil
		case strings.HasSuffix(path, "/api/version"):
			return http.StatusOK, []byte(`{"Version":"v3.1.16+abc"}`), nil
		}
		return http.StatusNotFound, nil, nil
	}
}

func TestArgoProbe(t *testing.T) {
	cases := map[string]struct {
		tunnel    TunnelCall
		reachable bool
		contains  string
	}{
		"signed in with applications": {
			argoProbeTunnel(`{"loggedIn":true,"username":"kubemg"}`, 200, `{"items":[{},{}]}`),
			true, "2 applications visible",
		},
		"signed in, none visible": {
			argoProbeTunnel(`{"loggedIn":true}`, 200, `{"items":null}`),
			true, "can see no applications",
		},
		"token not a session": {
			argoProbeTunnel(`{"loggedIn":false}`, 200, `{"items":[]}`),
			false, "did not accept the token",
		},
		"list refused": {
			argoProbeTunnel(`{"loggedIn":true}`, 403, `{"error":"permission denied"}`),
			false, "role:readonly",
		},
		"something else answering": {
			argoProbeTunnel(`<html>`, 200, ``),
			false, "not as Argo CD does",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			result := Probe(context.Background(), argoTarget(), tc.tunnel)
			if result.Reachable != tc.reachable || !strings.Contains(result.Message, tc.contains) {
				t.Fatalf("expected reachable=%v containing %q, got %+v", tc.reachable, tc.contains, result)
			}
			if tc.reachable && result.Version != "v3.1.16+abc" {
				t.Fatalf("expected the version read, got %q", result.Version)
			}
		})
	}
}

func TestArgoManagedResource(t *testing.T) {
	var asked string
	answer := `{"items":[
		{"group":"apps","kind":"Deployment","namespace":"shop","name":"other","normalizedLiveState":"{\"x\":1}"},
		{"group":"apps","kind":"Deployment","namespace":"shop","name":"web","modified":true,
		 "normalizedLiveState":"{\"spec\":{\"replicas\":3}}","predictedLiveState":"{\"spec\":{\"replicas\":1}}"}]}`
	tunnel := func(_ context.Context, _, path string, _ http.Header, _ []byte) (int, []byte, error) {
		asked = path
		return http.StatusOK, []byte(answer), nil
	}
	object := ArgoObject{App: "shop", AppNamespace: "argocd", Group: "apps", Kind: "Deployment", Namespace: "shop", Name: "web"}
	state, err := ArgoManagedResource(context.Background(), argoTarget(), tunnel, object)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Modified || !strings.Contains(state.NormalizedLive, "3") || !strings.Contains(state.PredictedLive, "1") {
		t.Fatalf("expected the matching item, got %+v", state)
	}
	parsed, err := url.Parse(asked[strings.Index(asked, "/proxy")+len("/proxy"):])
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Path != "/api/v1/applications/shop/managed-resources" {
		t.Fatalf("expected the managed-resources path, got %q", parsed.Path)
	}
	query := parsed.Query()
	for key, want := range map[string]string{"appNamespace": "argocd", "group": "apps", "kind": "Deployment",
		"namespace": "shop", "name": "web"} {
		if query.Get(key) != want {
			t.Fatalf("expected %s=%s, got %q", key, want, query.Get(key))
		}
	}

	object.Name = "absent"
	if _, err := ArgoManagedResource(context.Background(), argoTarget(), tunnel, object); !errors.Is(err, ErrArgoNoResource) {
		t.Fatalf("expected no resource, got %v", err)
	}

	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusInternalServerError} {
		refusing := func(context.Context, string, string, http.Header, []byte) (int, []byte, error) {
			return status, []byte(`{}`), nil
		}
		var argoErr *ArgoError
		if _, err := ArgoManagedResource(context.Background(), argoTarget(), refusing, object); !errors.As(err, &argoErr) || argoErr.Status != status {
			t.Fatalf("HTTP %d: expected an ArgoError, got %v", status, err)
		}
	}
}
