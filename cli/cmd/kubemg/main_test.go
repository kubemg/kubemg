package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kubemg/kubemg/cli/internal/client"
	"github.com/kubemg/kubemg/cli/internal/store"
)

// fakeServer is the CLI half of a kubemg server, with the knobs the tests turn.
type fakeServer struct {
	*httptest.Server
	minted   atomic.Int32
	revoked  atomic.Bool
	loggedIn atomic.Bool
	ttl      time.Duration
	// clusterServer and staticUser let a test play a server that lies.
	clusterServer string
	staticUser    string
	verifyURL     string
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	f := &fakeServer{ttl: 15 * time.Minute, staticUser: "{token: static-token}"}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/auth/cli/device", func(w http.ResponseWriter, r *http.Request) {
		verify := f.verifyURL
		if verify == "" {
			verify = f.URL + "/cli/activate?code=BCDF-GHJK"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_code": "kmgd_x", "user_code": "BCDF-GHJK",
			"verification_uri_complete": verify,
			"expires_in":                600, "interval": 5,
		})
	})
	mux.HandleFunc("POST /api/v1/auth/cli/token", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"refresh_token": "kmgr_secret", "session_id": "s-1", "expires_at": time.Now().Add(12 * time.Hour),
			"user": map[string]string{"username": "dana"},
		})
	})
	authed := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer kmgr_secret" || f.revoked.Load() {
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "this CLI session was revoked; run kubemg login"})
				return
			}
			next(w, r)
		}
	}
	mux.HandleFunc("GET /api/v1/auth/cli/clusters", authed(func(w http.ResponseWriter, r *http.Request) {
		server := f.clusterServer
		if server == "" {
			server = f.URL + "/api/v1/clusters/7/proxy"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"clusters": []map[string]any{
			{"id": 7, "name": "edge-us", "connection_mode": "agent", "role": "view", "namespaces": []string{"payments"},
				"server": server, "context_name": "dana@edge-us", "ca_data": "Q0E="},
			{"id": 9, "name": "legacy", "connection_mode": "direct", "role": "view",
				"note": "registered for direct API access — download its kubeconfig from the console"},
		}})
	}))
	mux.HandleFunc("POST /api/v1/auth/cli/credential", authed(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["static"] == true {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"context": "dana@edge-us", "expires_at": time.Now().Add(8 * time.Hour),
				"kubeconfig": "apiVersion: v1\nkind: Config\nclusters:\n- name: edge-us\n  cluster: {server: x}\nusers:\n- name: dana@edge-us\n  user: " + f.staticUser + "\ncontexts:\n- name: dana@edge-us\n  context: {cluster: edge-us, user: dana@edge-us}\n",
			})
			return
		}
		n := f.minted.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token": "access-" + string(rune('0'+n)), "expires_at": time.Now().Add(f.ttl),
		})
	}))
	mux.HandleFunc("POST /api/v1/auth/cli/logout", authed(func(w http.ResponseWriter, r *http.Request) {
		f.loggedIn.Store(false)
		w.WriteHeader(http.StatusNoContent)
	}))
	f.Server = httptest.NewTLSServer(mux)
	t.Cleanup(f.Close)
	return f
}

type harness struct {
	app            *app
	stdout, stderr *bytes.Buffer
	opened         string
	dir            string
	env            map[string]string
}

// trustPEM is the fake server's certificate, which a test trusts the way a
// person trusts a self-signed bastion: with --ca-file.
func trustPEM(f *fakeServer) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.Certificate().Raw})
}

func host(f *fakeServer) string { return strings.TrimPrefix(f.URL, "https://") }

func caFile(t *testing.T, f *fakeServer) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, trustPEM(f), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	h := &harness{stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, dir: dir, env: map[string]string{}}
	h.app = &app{
		stdout: h.stdout,
		stderr: h.stderr,
		store:  store.Store{ConfigDir: filepath.Join(dir, "config"), CacheDir: filepath.Join(dir, "cache")},
		getenv: func(key string) string { return h.env[key] },
		now:    time.Now,
		newClient: func(server string, caPEM []byte) (*client.Client, error) {
			c, err := client.New(server, caPEM)
			if err == nil {
				c.Sleep = func(context.Context, time.Duration) error { return nil }
			}
			return c, err
		},
		openBrowser: func(url string) error { h.opened = url; return nil },
		executable:  func() string { return "kubemg" },
	}
	return h
}

func (h *harness) run(args ...string) int {
	h.stdout.Reset()
	h.stderr.Reset()
	return h.app.run(context.Background(), args)
}

func TestLoginStoresTheSessionAndOpensTheApprovalPage(t *testing.T) {
	srv := newFakeServer(t)
	h := newHarness(t)
	if code := h.run("login", "--server", srv.URL+"/", "--ca-file", caFile(t, srv)); code != 0 {
		t.Fatalf("login exit %d: %s", code, h.stderr)
	}
	if h.opened != srv.URL+"/cli/activate?code=BCDF-GHJK" || !strings.Contains(h.stderr.String(), "BCDF-GHJK") {
		t.Fatalf("opened %q, stderr %s", h.opened, h.stderr)
	}
	session, err := h.app.store.LoadSession(srv.URL)
	if err != nil || session.RefreshToken != "kmgr_secret" || session.Username != "dana" {
		t.Fatalf("session %+v %v", session, err)
	}
	info, _ := os.Stat(filepath.Join(h.dir, "config", "sessions", store.Key(srv.URL)+".json"))
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("session file mode %v", info.Mode().Perm())
	}
}

func TestUpdateKubeconfigWritesAnExecEntryAndRefusesDirectMode(t *testing.T) {
	srv := newFakeServer(t)
	h := newHarness(t)
	h.run("login", "--server", srv.URL, "--no-browser", "--ca-file", caFile(t, srv))
	kubeconfig := filepath.Join(h.dir, "kube", "config")

	if code := h.run("update-kubeconfig", "--cluster", "edge-us", "--kubeconfig", kubeconfig); code != 0 {
		t.Fatalf("exit %d: %s", code, h.stderr)
	}
	data, _ := os.ReadFile(kubeconfig)
	for _, want := range []string{
		"server: " + srv.URL + "/api/v1/clusters/7/proxy",
		"certificate-authority-data: Q0E=",
		"command: kubemg",
		"args: [credential, --server, '" + srv.URL + "', --cluster-id, \"7\"]",
		"interactiveMode: Never",
		"namespace: payments",
		"current-context: " + host(srv) + "/edge-us",
		"name: " + host(srv) + "/edge-us/dana",
		"name: kubemg",
	} {
		if !strings.Contains(string(data), want) {
			t.Errorf("missing %q in:\n%s", want, data)
		}
	}
	if strings.Contains(string(data), "kmgr_") || strings.Contains(string(data), "token:") {
		t.Fatalf("a credential was written into the kubeconfig:\n%s", data)
	}

	if code := h.run("update-kubeconfig", "--cluster", "legacy", "--kubeconfig", kubeconfig); code != 1 ||
		!strings.Contains(h.stderr.String(), "direct API access") {
		t.Fatalf("direct mode: exit %d, %s", code, h.stderr)
	}
	if code := h.run("update-kubeconfig", "--cluster", "edge-us", "--namespace", "kube-system", "--kubeconfig", kubeconfig); code != 1 ||
		!strings.Contains(h.stderr.String(), "outside your grant") {
		t.Fatalf("namespace outside the grant: exit %d, %s", code, h.stderr)
	}
}

func TestTheExecPluginCachesAndRenews(t *testing.T) {
	srv := newFakeServer(t)
	h := newHarness(t)
	h.run("login", "--server", srv.URL, "--no-browser", "--ca-file", caFile(t, srv))

	var first execCredential
	if code := h.run("credential", "--server", srv.URL, "--cluster-id", "7"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.stderr)
	}
	if err := json.Unmarshal(h.stdout.Bytes(), &first); err != nil {
		t.Fatalf("not an ExecCredential: %s", h.stdout)
	}
	if first.APIVersion != "client.authentication.k8s.io/v1" || first.Kind != "ExecCredential" ||
		first.Status.Token != "access-1" || first.Status.ExpirationTimestamp == "" {
		t.Fatalf("credential %+v", first)
	}

	// A second kubectl call inside the window is served from the cache.
	h.run("credential", "--server", srv.URL, "--cluster-id", "7")
	if srv.minted.Load() != 1 {
		t.Fatalf("minted %d times", srv.minted.Load())
	}

	// Within a minute of expiry it is renewed rather than handed out.
	h.app.store.SaveCredential(srv.URL, 7, store.Credential{Token: "access-1", ExpiresAt: time.Now().Add(30 * time.Second)})
	h.run("credential", "--server", srv.URL, "--cluster-id", "7")
	if srv.minted.Load() != 2 || !strings.Contains(h.stdout.String(), "access-2") {
		t.Fatalf("not renewed: minted %d, %s", srv.minted.Load(), h.stdout)
	}
}

func TestAnEndedSessionSaysToSignInAgain(t *testing.T) {
	srv := newFakeServer(t)
	h := newHarness(t)
	h.run("login", "--server", srv.URL, "--no-browser", "--ca-file", caFile(t, srv))
	h.run("credential", "--server", srv.URL, "--cluster-id", "7")
	srv.revoked.Store(true)
	h.app.store.SaveCredential(srv.URL, 7, store.Credential{Token: "access-1", ExpiresAt: time.Now().Add(10 * time.Second)})

	code := h.run("credential", "--server", srv.URL, "--cluster-id", "7")
	if code != 1 || h.stdout.Len() != 0 {
		t.Fatalf("exit %d, stdout %q", code, h.stdout)
	}
	if !strings.Contains(h.stderr.String(), "kubemg login --server "+srv.URL) {
		t.Fatalf("stderr %s", h.stderr)
	}
	if _, ok := h.app.store.LoadCredential(srv.URL, 7); ok {
		t.Fatal("a refused credential stayed cached")
	}
}

func TestAMachineTokenNeedsNoSession(t *testing.T) {
	srv := newFakeServer(t)
	h := newHarness(t)
	h.env["KUBEMG_TOKEN"] = "kmgm_machine"
	if code := h.run("credential", "--server", srv.URL, "--cluster-id", "7"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.stderr)
	}
	if !strings.Contains(h.stdout.String(), `"token":"kmgm_machine"`) || srv.minted.Load() != 0 {
		t.Fatalf("stdout %s, minted %d", h.stdout, srv.minted.Load())
	}

	kubeconfig := filepath.Join(h.dir, "ci-config")
	if code := h.run("update-kubeconfig", "--server", srv.URL, "--cluster", "7", "--kubeconfig", kubeconfig); code != 0 {
		t.Fatalf("exit %d: %s", code, h.stderr)
	}
	data, _ := os.ReadFile(kubeconfig)
	if !strings.Contains(string(data), srv.URL+"/api/v1/clusters/7/proxy") {
		t.Fatalf("machine kubeconfig:\n%s", data)
	}
	if code := h.run("update-kubeconfig", "--server", srv.URL, "--cluster", "edge-us", "--kubeconfig", kubeconfig); code != 2 {
		t.Fatalf("a name with a machine token: exit %d", code)
	}
}

func TestStaticWritesTheServersKubeconfig(t *testing.T) {
	srv := newFakeServer(t)
	h := newHarness(t)
	h.run("login", "--server", srv.URL, "--no-browser", "--ca-file", caFile(t, srv))
	kubeconfig := filepath.Join(h.dir, "static-config")
	if code := h.run("update-kubeconfig", "--cluster", "7", "--static", "--ttl", "8h", "--kubeconfig", kubeconfig); code != 0 {
		t.Fatalf("exit %d: %s", code, h.stderr)
	}
	data, _ := os.ReadFile(kubeconfig)
	if !strings.Contains(string(data), "token: static-token") || !strings.Contains(string(data), "current-context: "+host(srv)+"/edge-us") {
		t.Fatalf("static:\n%s", data)
	}
	if code := h.run("update-kubeconfig", "--cluster", "7", "--ttl", "8h", "--kubeconfig", kubeconfig); code != 2 {
		t.Fatalf("--ttl without --static: exit %d", code)
	}
}

func TestLogoutForgetsEverythingLocally(t *testing.T) {
	srv := newFakeServer(t)
	h := newHarness(t)
	h.run("login", "--server", srv.URL, "--no-browser", "--ca-file", caFile(t, srv))
	h.run("credential", "--server", srv.URL, "--cluster-id", "7")
	// One server signed in: no --server needed.
	if code := h.run("logout"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.stderr)
	}
	if _, err := h.app.store.LoadSession(srv.URL); err != store.ErrNoSession {
		t.Fatalf("session left: %v", err)
	}
	if _, ok := h.app.store.LoadCredential(srv.URL, 7); ok {
		t.Fatal("a credential outlived the logout")
	}
	if code := h.run("clusters"); code != 2 {
		t.Fatalf("clusters with nothing signed in: exit %d", code)
	}
}

func TestUsageMistakesExitTwo(t *testing.T) {
	h := newHarness(t)
	for _, args := range [][]string{{}, {"frobnicate"}, {"login"}, {"credential", "--server", "https://x"}, {"update-kubeconfig"}} {
		if code := h.run(args...); code != 2 {
			t.Errorf("%v: exit %d", args, code)
		}
	}
	if code := h.run("version"); code != 0 || !strings.HasPrefix(h.stdout.String(), "kubemg ") {
		t.Fatalf("version: %d %s", code, h.stdout)
	}
}

func TestTheStaticPathTakesOnlyATokenFromTheServer(t *testing.T) {
	srv := newFakeServer(t)
	srv.staticUser = "{exec: {command: /bin/sh, args: [-c, 'touch /tmp/pwned']}}"
	h := newHarness(t)
	h.run("login", "--server", srv.URL, "--no-browser", "--ca-file", caFile(t, srv))
	kubeconfig := filepath.Join(h.dir, "static-config")
	if code := h.run("update-kubeconfig", "--cluster", "7", "--static", "--kubeconfig", kubeconfig); code != 1 ||
		!strings.Contains(h.stderr.String(), "only a token is accepted") {
		t.Fatalf("exit %d: %s", code, h.stderr)
	}
	if _, err := os.Stat(kubeconfig); err == nil {
		data, _ := os.ReadFile(kubeconfig)
		t.Fatalf("a kubeconfig was written:\n%s", data)
	}
}

func TestANonHTTPSClusterAddressIsNotWritten(t *testing.T) {
	srv := newFakeServer(t)
	srv.clusterServer = "http://evil.example.com/proxy"
	h := newHarness(t)
	h.run("login", "--server", srv.URL, "--no-browser", "--ca-file", caFile(t, srv))
	if code := h.run("update-kubeconfig", "--cluster", "edge-us", "--kubeconfig", filepath.Join(h.dir, "c")); code != 1 ||
		!strings.Contains(h.stderr.String(), "not https") {
		t.Fatalf("exit %d: %s", code, h.stderr)
	}
}

func TestOnlyAnHTTPSApprovalPageIsOpened(t *testing.T) {
	srv := newFakeServer(t)
	srv.verifyURL = "file:///Applications/Calculator.app"
	h := newHarness(t)
	if code := h.run("login", "--server", srv.URL, "--ca-file", caFile(t, srv)); code != 0 {
		t.Fatalf("exit %d: %s", code, h.stderr)
	}
	if h.opened != "" {
		t.Fatalf("opened %q", h.opened)
	}
	for raw, want := range map[string]bool{
		"https://kubemg.example.com/cli/activate?code=BCDF-GHJK": true,
		"http://kubemg.example.com/cli/activate":                 false,
		"file:///etc/passwd":                                     false,
		"smb://share/x":                                          false,
		"https://user:pw@kubemg.example.com/":                    false,
		"https://kubemg.example.com/\" & calc":                   false,
	} {
		if got := safeBrowserURL(raw); got != want {
			t.Errorf("%q: %v", raw, got)
		}
	}
}

func TestASessionIsKeyedByScheme(t *testing.T) {
	if store.Key("https://kubemg.example.com") == store.Key("http://kubemg.example.com") {
		t.Fatal("http and https share a session")
	}
	if strings.ContainsAny(store.Key("https://../../etc:443"), "/") {
		t.Fatal("a key climbs out of its directory")
	}
}

// A server that names its cluster after something already in the kubeconfig
// lands under its own host prefix, and cannot reach the existing entry.
func TestAServerCannotNameItsWayIntoSomebodyElsesEntry(t *testing.T) {
	srv := newFakeServer(t)
	h := newHarness(t)
	h.run("login", "--server", srv.URL, "--no-browser", "--ca-file", caFile(t, srv))
	kubeconfig := filepath.Join(h.dir, "kube-config")
	existing := "apiVersion: v1\nkind: Config\nclusters:\n- name: " + host(srv) + "/edge-us\n  cluster: {server: https://prod.internal:6443}\nusers: []\ncontexts: []\n"
	if err := os.WriteFile(kubeconfig, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := h.run("update-kubeconfig", "--cluster", "edge-us", "--kubeconfig", kubeconfig); code != 1 ||
		!strings.Contains(h.stderr.String(), "kubemg did not write") {
		t.Fatalf("exit %d: %s", code, h.stderr)
	}
	if data, _ := os.ReadFile(kubeconfig); string(data) != existing {
		t.Fatalf("the file changed:\n%s", data)
	}
	if safeName("../prod\nx y") != "..-prod-x-y" {
		t.Fatalf("safeName: %q", safeName("../prod\nx y"))
	}
}
