package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/kubemg/kubemg/backend/pkg/bastion"
	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * The install URL and the tunnel credential, kept apart. What is asserted:
 * a download ticket answers exactly once and never after its expiry; a URL in
 * the old shape — the tunnel credential in the path — is refused without being
 * looked up; and a rotation replaces the credential, withdraws outstanding
 * tickets, cuts the attached agent off, refuses the old token at the next
 * handshake, and is on the record.
 */

type fakeInstallTicket struct {
	clusterID uint
	expiresAt time.Time
}

func (f *fakeStore) PutAgentInstallTicket(_ context.Context, hash string, clusterID uint, expiresAt time.Time) error {
	if f.installTicketErr != nil {
		return f.installTicketErr
	}
	if f.installTickets == nil {
		f.installTickets = map[string]fakeInstallTicket{}
	}
	f.installTickets[hash] = fakeInstallTicket{clusterID: clusterID, expiresAt: expiresAt}
	return nil
}

func (f *fakeStore) TakeAgentInstallTicket(_ context.Context, hash string) (uint, bool, error) {
	if f.installTicketErr != nil {
		return 0, false, f.installTicketErr
	}
	ticket, ok := f.installTickets[hash]
	if !ok || !time.Now().Before(ticket.expiresAt) {
		return 0, false, nil
	}
	delete(f.installTickets, hash)
	return ticket.clusterID, true, nil
}

func (f *fakeStore) RotateClusterAgentToken(_ context.Context, clusterID uint, token string) error {
	cluster, ok := f.clusters[clusterID]
	if !ok || cluster.ConnectionMode != db.ModeAgent {
		return db.ErrNotFound
	}
	cluster.AgentToken = token
	for hash, ticket := range f.installTickets {
		if ticket.clusterID == clusterID {
			delete(f.installTickets, hash)
		}
	}
	return nil
}

// mintInstall opens the install package the way the console does.
func mintInstall(t *testing.T, env *testEnv, admin *db.User, clusterID uint) agentInstallResponse {
	t.Helper()
	rec := env.do(t, http.MethodGet, "/api/v1/clusters/"+itoa(clusterID)+"/kustomize", env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("mint install: expected %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	return decode[agentInstallResponse](t, rec)
}

// pathOf strips the public URL off an install URL so the router can serve it.
func pathOf(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		panic(err)
	}
	return parsed.Path
}

func TestInstallTicketAnswersOnce(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	cluster := env.store.addAgentCluster("edge-us", db.EnvStaging, "kmg_install-token")
	install := mintInstall(t, env, admin, cluster.ID)

	if rec := env.do(t, http.MethodGet, pathOf(install.ManifestURL), "", nil); rec.Code != http.StatusOK {
		t.Fatalf("first fetch: expected %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	// The same URL again, and the other form carrying the same ticket: both
	// spent by the first fetch. A leaked URL is dead once it has been used.
	for _, raw := range []string{install.ManifestURL, install.ArchiveURL} {
		rec := env.do(t, http.MethodGet, pathOf(raw), "", nil)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: a spent ticket must be refused, got %d", raw, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "kmg_install-token") {
			t.Fatal("a refused fetch must not carry the credential")
		}
	}
}

func TestInstallTicketsAreFreshPerOpening(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	cluster := env.store.addAgentCluster("edge-us", db.EnvStaging, "kmg_install-token")

	first := mintInstall(t, env, admin, cluster.ID)
	second := mintInstall(t, env, admin, cluster.ID)
	if first.ManifestURL == second.ManifestURL {
		t.Fatal("each opening of the install package must mint its own URL")
	}
	until := time.Until(first.DownloadExpiresAt)
	if until <= 0 || until > installTicketTTL {
		t.Fatalf("the download expiry should be within the ticket TTL, got %s", until)
	}
	// Opening it is not a rotation.
	if env.store.clusters[cluster.ID].AgentToken != "kmg_install-token" {
		t.Fatal("opening the install package must never rotate the tunnel credential")
	}
	// And the first is still good: minting another does not revoke it.
	if rec := env.do(t, http.MethodGet, pathOf(first.ManifestURL), "", nil); rec.Code != http.StatusOK {
		t.Fatalf("an unspent ticket should still answer, got %d", rec.Code)
	}
}

func TestInstallTicketExpires(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	cluster := env.store.addAgentCluster("edge-us", db.EnvStaging, "kmg_install-token")
	install := mintInstall(t, env, admin, cluster.ID)

	for hash, ticket := range env.store.installTickets {
		ticket.expiresAt = time.Now().Add(-time.Second)
		env.store.installTickets[hash] = ticket
	}
	if rec := env.do(t, http.MethodGet, pathOf(install.ManifestURL), "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("an expired ticket must be refused, got %d", rec.Code)
	}
}

func TestInstallTicketRefusedWhenTheStoreIsUnreadable(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	cluster := env.store.addAgentCluster("edge-us", db.EnvStaging, "kmg_install-token")
	install := mintInstall(t, env, admin, cluster.ID)

	env.store.installTicketErr = errors.New("database is down")
	rec := env.do(t, http.MethodGet, pathOf(install.ManifestURL), "", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("an unreadable store must refuse, got %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "kmg_install-token") {
		t.Fatal("a refused fetch must not carry the credential")
	}

	// And the console says so rather than handing out a URL nobody filed.
	rec = env.do(t, http.MethodGet, "/api/v1/clusters/"+itoa(cluster.ID)+"/kustomize", env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("a ticket that could not be filed must fail the render, got %d", rec.Code)
	}
}

func TestLegacyInstallURLIsGone(t *testing.T) {
	env := newTestEnv(t)
	env.store.addAgentCluster("edge-us", db.EnvStaging, "kmg_install-token")

	// The live credential and a made-up one answer identically: the old shape is
	// refused without being looked up, so it is not an oracle either.
	for _, token := range []string{"kmg_install-token", "kmg_wrong"} {
		for _, file := range []string{"agent.yaml", "kustomize.tar.gz"} {
			rec := env.do(t, http.MethodGet, "/install/"+token+"/"+file, "", nil)
			if rec.Code != http.StatusGone {
				t.Fatalf("%s/%s: expected %d, got %d", token, file, http.StatusGone, rec.Code)
			}
			if strings.Contains(rec.Body.String(), "kind: Secret") {
				t.Fatal("an old-style URL must not return the package")
			}
			if !strings.Contains(rec.Body.String(), "Agent install") {
				t.Fatalf("the refusal should say where a fresh URL comes from: %s", rec.Body.String())
			}
		}
	}
}

func TestRotateAgentTokenRequiresAdmin(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("devops", "pw", db.RoleUser)
	cluster := env.store.addAgentCluster("edge-us", db.EnvStaging, "kmg_install-token")

	rec := env.do(t, http.MethodPost, "/api/v1/clusters/"+itoa(cluster.ID)+"/agent-token/rotate",
		env.tokenFor(t, user), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected %d, got %d", http.StatusForbidden, rec.Code)
	}
	if env.store.clusters[cluster.ID].AgentToken != "kmg_install-token" {
		t.Fatal("a refused rotation must change nothing")
	}
}

func TestRotateAgentTokenRefusesADirectCluster(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	cluster := env.store.addCluster("prod-eu", db.EnvProd)

	rec := env.do(t, http.MethodPost, "/api/v1/clusters/"+itoa(cluster.ID)+"/agent-token/rotate",
		env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected %d, got %d (%s)", http.StatusConflict, rec.Code, rec.Body.String())
	}
}

// dialAgent opens a tunnel against the router the way the agent does, and
// returns once the bastion has welcomed it.
func dialAgent(t *testing.T, server *httptest.Server, token string) (*websocket.Conn, error) {
	t.Helper()
	header := http.Header{}
	header.Set("Authorization", "Bearer "+token)
	conn, _, err := websocket.DefaultDialer.Dial(
		"ws"+strings.TrimPrefix(server.URL, "http")+"/agent/v1/tunnel", header)
	if err != nil {
		return nil, err
	}
	hello := bastion.Message{Type: bastion.MessageHello, Hello: &bastion.Hello{
		ProtocolVersion: bastion.ProtocolVersion,
		AgentVersion:    "test-agent",
	}}
	if err := conn.WriteJSON(hello); err != nil {
		return nil, err
	}
	var welcome bastion.Message
	if err := conn.ReadJSON(&welcome); err != nil {
		return nil, err
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn, nil
}

func TestRotateAgentTokenCutsTheOldCredentialOff(t *testing.T) {
	auditor := &recordingAuditor{}
	env := newTestEnvWith(t, func(o *Options) { o.Auditor = auditor })
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	cluster := env.store.addAgentCluster("edge-us", db.EnvStaging, "kmg_install-token")
	server := httptest.NewServer(env.router)
	t.Cleanup(server.Close)

	agent, err := dialAgent(t, server, "kmg_install-token")
	if err != nil {
		t.Fatalf("dial agent: %v", err)
	}
	waitUntil(t, func() bool { return env.registry.Connected(cluster.ID) })
	before := mintInstall(t, env, admin, cluster.ID)

	rec := env.do(t, http.MethodPost, "/api/v1/clusters/"+itoa(cluster.ID)+"/agent-token/rotate",
		env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	body := decode[agentTokenRotation](t, rec)

	stored := env.store.clusters[cluster.ID].AgentToken
	if stored == "kmg_install-token" || !strings.HasPrefix(stored, "kmg_") {
		t.Fatalf("the credential was not replaced: %q", stored)
	}
	if body.Install.AgentToken != stored || !strings.Contains(body.Install.Manifest, stored) {
		t.Fatal("the rotation should answer with the package for the new credential")
	}
	if !body.Disconnected {
		t.Fatal("the attached agent should have been cut off")
	}

	// The tunnel that was up is closed, with the reason, and the agent's own
	// reconnect on the old token is refused at the handshake.
	_ = agent.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, _, readErr := agent.ReadMessage()
	var closeErr *websocket.CloseError
	if !errors.As(readErr, &closeErr) || !strings.Contains(closeErr.Text, "rotated") {
		t.Fatalf("the agent should be told its token was rotated, got %v", readErr)
	}
	waitUntil(t, func() bool { return !env.registry.Connected(cluster.ID) })
	if _, err := dialAgent(t, server, "kmg_install-token"); err == nil {
		t.Fatal("the old credential must be refused at the next handshake")
	}
	if _, err := dialAgent(t, server, stored); err != nil {
		t.Fatalf("the new credential should attach: %v", err)
	}

	// A URL minted before the rotation would render the *new* token; it is
	// withdrawn with the old one.
	if rec := env.do(t, http.MethodGet, pathOf(before.ManifestURL), "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("a pre-rotation install URL must be withdrawn, got %d", rec.Code)
	}
	if rec := env.do(t, http.MethodGet, pathOf(body.Install.ManifestURL), "", nil); rec.Code != http.StatusOK {
		t.Fatalf("the rotation's own install URL should work, got %d", rec.Code)
	}

	var rotated *bastion.Event
	for _, event := range auditor.all() {
		if event.Verb == bastion.VerbAgentTokenRotate {
			rotated = &event
		}
	}
	if rotated == nil {
		t.Fatal("a rotation must be on the record")
	}
	if rotated.Username != "admin" || rotated.ClusterID != cluster.ID || rotated.Status != http.StatusOK {
		t.Fatalf("unexpected rotation record: %+v", rotated)
	}
	if strings.Contains(rotated.Path, "kmg_") {
		t.Fatal("the record must not carry a credential")
	}
}

func waitUntil(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for the condition")
}
