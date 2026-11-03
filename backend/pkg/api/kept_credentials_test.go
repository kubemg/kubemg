package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

func TestSameOrigin(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"https://vm.example.com", "https://vm.example.com:443/prometheus", true},
		{"https://VM.example.com/a", "https://vm.example.com/b", true},
		{"http://vm.example.com", "http://vm.example.com:80", true},
		{"https://vm.example.com", "http://vm.example.com", false},
		{"https://vm.example.com", "https://vm.example.com:8443", false},
		{"https://vm.example.com", "https://attacker.example", false},
		{"https://vm.example.com", "not a url", false},
		{"", "", false},
	} {
		if got := sameOrigin(tc.a, tc.b); got != tc.want {
			t.Errorf("sameOrigin(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

// listener counts what reaches a host and remembers the last Authorization
// header, so a test can say whether a stored credential was sent there.
func listener(t *testing.T) (*httptest.Server, *atomic.Int32, *atomic.Value) {
	t.Helper()
	var hits atomic.Int32
	var auth atomic.Value
	auth.Store("")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		auth.Store(r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)
	return server, &hits, &auth
}

// A stored datasource token goes only where it was given. A save or a Test
// that moves the address and leaves the credential out is refused before
// anything is dialled; the same edit with the credential typed again is not.
func TestAStoredDatasourceCredentialDoesNotFollowANewAddress(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	cluster := env.store.addCluster("prod-eu", db.EnvProd)
	token := env.tokenFor(t, admin)
	backend := promServer(t)
	elsewhere, hits, _ := listener(t)

	base := "/api/v1/clusters/" + itoa(cluster.ID) + "/observability/sources/metrics"
	if rec := env.do(t, http.MethodPut, base, token, directSourcePayload(backend.URL)); rec.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
	}
	moved := directSourcePayload(elsewhere.URL)
	delete(moved, "credential")
	for _, route := range []struct{ method, path string }{
		{http.MethodPost, base + "/test"},
		{http.MethodPut, base},
	} {
		rec := env.do(t, route.method, route.path, token, moved)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "enter the credential again") {
			t.Fatalf("%s %s with a new host and no credential: want 400, got %d (%s)",
				route.method, route.path, rec.Code, rec.Body.String())
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("the new host was dialled %d times", hits.Load())
	}
	retyped := directSourcePayload(elsewhere.URL)
	retyped["credential"] = "typed-again"
	if rec := env.do(t, http.MethodPost, base+"/test", token, retyped); rec.Code != http.StatusOK {
		t.Fatalf("a test with the credential typed again: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAStoredHelmCredentialDoesNotFollowANewAddress(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")
	token := env.tokenFor(t, admin)
	index := chartIndex(t, oneChartIndex)
	elsewhere, hits, _ := listener(t)

	env.do(t, http.MethodPut, "/api/v1/helm/repositories/charts", token,
		map[string]any{"url": index.URL, "username": "reader", "credential": "hunter2"})
	rec := env.do(t, http.MethodPut, "/api/v1/helm/repositories/charts", token,
		map[string]any{"url": elsewhere.URL, "username": "reader"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a new host and no credential: want 400, got %d (%s)", rec.Code, rec.Body.String())
	}
	if hits.Load() != 0 || env.store.helmRepos["charts"].URL != index.URL {
		t.Fatalf("the edit reached the new host (%d) or landed", hits.Load())
	}
}

func TestAStoredAlarmSecretDoesNotFollowANewHost(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	channel := env.store.addAlarmChannel(db.AlarmChannel{
		Name: "siem", Kind: db.ChannelWebhook,
		URL: "https://siem.example.com/in", AuthMode: db.AuthBearer,
		Secret: "kept-token", Enabled: true,
	})
	path := fmt.Sprintf("/api/v1/alarms/channels/%d", channel.ID)
	rec := env.do(t, http.MethodPut, path, env.tokenFor(t, admin), map[string]any{
		"name": "siem", "kind": db.ChannelWebhook,
		"url": "https://attacker.example/in", "auth_mode": db.AuthBearer,
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a new host and no secret: want 400, got %d (%s)", rec.Code, rec.Body.String())
	}
	stored, err := env.store.AlarmChannelByID(t.Context(), channel.ID)
	if err != nil || stored.URL != "https://siem.example.com/in" {
		t.Fatalf("the refused edit landed: %+v %v", stored, err)
	}
	rec = env.do(t, http.MethodPut, path, env.tokenFor(t, admin), map[string]any{
		"name": "siem", "kind": db.ChannelWebhook, "secret": "typed-again",
		"url": "https://attacker.example/in", "auth_mode": db.AuthBearer,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("the same edit with the secret typed again: %d %s", rec.Code, rec.Body.String())
	}
}

// An address with user:password@ in it is shown to everyone who reads the row,
// so a save refuses one; a row stored before the rule never shows it.
func TestAnAddressCarryingALoginIsRefusedAndNeverShown(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	dev := env.store.addUser("dev", "pw", db.RoleUser)
	cluster := env.store.addCluster("prod-eu", db.EnvProd)
	env.store.grant(dev.ID, cluster.ID, db.K8sRoleView, nil)
	token := env.tokenFor(t, admin)
	backend := promServer(t)

	withLogin := strings.Replace(backend.URL, "://", "://reader:hunter2@", 1)
	base := "/api/v1/clusters/" + itoa(cluster.ID) + "/observability/sources/metrics"
	if rec := env.do(t, http.MethodPut, base, token, directSourcePayload(withLogin)); rec.Code != http.StatusBadRequest {
		t.Fatalf("a datasource address with a login: want 400, got %d", rec.Code)
	}
	if rec := env.do(t, http.MethodPut, "/api/v1/helm/repositories/charts", token,
		map[string]any{"url": "https://reader:hunter2@charts.example.com"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("a repository address with a login: want 400, got %d", rec.Code)
	}

	// A row stored before the rule: kept working, never shown.
	if rec := env.do(t, http.MethodPut, base, token, directSourcePayload(backend.URL)); rec.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
	}
	stored, err := env.store.ObservabilitySource(t.Context(), cluster.ID, db.SourceMetrics)
	if err != nil {
		t.Fatal(err)
	}
	stored.URL = withLogin
	if err := env.store.PutObservabilitySource(t.Context(), stored); err != nil {
		t.Fatal(err)
	}
	rec := env.do(t, http.MethodGet, "/api/v1/clusters/"+itoa(cluster.ID)+"/observability", env.tokenFor(t, dev), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), strings.TrimPrefix(backend.URL, "http://")) {
		t.Fatalf("the developer's list does not show the address at all: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "hunter2") || strings.Contains(rec.Body.String(), "reader:") {
		t.Fatalf("a developer read the login in the address: %s", rec.Body.String())
	}
}
