package bastion

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/auth"
	"github.com/kubemg/kubemg/backend/pkg/credentials"
	"github.com/kubemg/kubemg/backend/pkg/db"
)

// revocationHarness is the proxy mounted behind the real middleware with a
// credential register wired in, which is the arrangement main.go builds. No
// agent is attached: a revoked credential must be refused before the gateway
// ever looks for a tunnel, and that is exactly what these tests assert.
type revocationHarness struct {
	router *gin.Engine
	jwt    *auth.Manager
	issued *credentials.Register
	audit  *recordingAuditor
}

func newRevocationHarness(t *testing.T) *revocationHarness {
	t.Helper()

	store := newTunnelStore()
	user := &db.User{ID: 7, Username: "dana", SystemRole: db.SystemRoleUser, IsActive: true}
	user.Normalize()
	store.users[user.ID] = user
	store.clusters[3] = &db.Cluster{
		ID: 3, Name: "prod-eu", ConnectionMode: db.ModeAgent, AgentToken: "tok",
	}
	store.access[user.ID] = map[uint]db.UserClusterAccess{
		3: {UserID: user.ID, ClusterID: 3, K8sRole: db.K8sRoleView},
	}

	issued := credentials.New()
	auditor := &recordingAuditor{}
	gateway := NewServer(ServerOptions{Store: store})
	proxy := NewProxy(ProxyOptions{
		Store:       store,
		Registry:    gateway.Registry(),
		Auditor:     auditor,
		Credentials: issued,
	})

	manager := auth.NewManager("test-secret", time.Hour)
	router := gin.New()
	router.Any("/api/v1/clusters/:id/proxy/*path", auth.RequireAuth(manager), proxy.Handle)

	return &revocationHarness{router: router, jwt: manager, issued: issued, audit: auditor}
}

func (h *revocationHarness) kubeconfigToken(t *testing.T) (token, tokenID string) {
	t.Helper()
	token, tokenID, _, err := h.jwt.GenerateProxyToken(7, "dana", db.RoleUser, 3, time.Hour)
	if err != nil {
		t.Fatalf("generate proxy token: %v", err)
	}
	return token, tokenID
}

func (h *revocationHarness) call(token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/clusters/3/proxy/api/v1/namespaces/team/pods", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	return rec
}

func TestRevokedKubeconfigIsRefusedByTheGateway(t *testing.T) {
	h := newRevocationHarness(t)
	token, tokenID := h.kubeconfigToken(t)

	// Before the revocation is published the credential is ordinary. No agent is
	// attached, so this cannot succeed — what matters is that it is not refused
	// as a revoked credential.
	if rec := h.call(token); rec.Code == http.StatusUnauthorized {
		t.Fatalf("a live kubeconfig was refused as revoked: %s", rec.Body.String())
	}

	h.issued.Store(credentials.NewSnapshot([]string{tokenID}))

	rec := h.call(token)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected %d for a revoked kubeconfig, got %d (%s)",
			http.StatusUnauthorized, rec.Code, rec.Body.String())
	}
	// kubectl shows this line to whoever is holding the file, so it has to say
	// what happened rather than "invalid or expired token".
	if !strings.Contains(rec.Body.String(), "revoked") {
		t.Fatalf("the refusal does not say the credential was revoked: %s", rec.Body.String())
	}
}

func TestRevokingOneCredentialLeavesTheOthersAlone(t *testing.T) {
	h := newRevocationHarness(t)
	kept, _ := h.kubeconfigToken(t)
	_, goneID := h.kubeconfigToken(t)

	h.issued.Store(credentials.NewSnapshot([]string{goneID}))

	if rec := h.call(kept); rec.Code == http.StatusUnauthorized {
		t.Fatalf("revoking one credential refused another: %s", rec.Body.String())
	}
}

func TestAnUnreadableRegisterRefusesNothing(t *testing.T) {
	// The fail-open rule, from the gateway's side: a server whose register was
	// never published — because it has not read it yet, or because the read
	// failed — knows of no revocations and must let kubectl work.
	h := newRevocationHarness(t)
	token, tokenID := h.kubeconfigToken(t)
	h.issued.Store(credentials.NewSnapshot([]string{tokenID}))
	// A failed refresh leaves the previous set in place; an empty publish is what
	// "nothing is known to be withdrawn" looks like.
	h.issued.Store(credentials.NewSnapshot(nil))

	if rec := h.call(token); rec.Code == http.StatusUnauthorized {
		t.Fatalf("an empty register refused a credential: %s", rec.Body.String())
	}
}

func TestASessionTokenIsNeverMatchedAgainstTheRegister(t *testing.T) {
	// A console session carries no id, so nothing in the register can name it.
	// The set below holds the empty string's worth of nothing precisely because
	// NewSnapshot drops it — this asserts the gateway agrees.
	h := newRevocationHarness(t)
	h.issued.Store(credentials.NewSnapshot([]string{""}))

	token, _, err := h.jwt.Generate(7, "dana", db.RoleUser)
	if err != nil {
		t.Fatalf("generate session token: %v", err)
	}
	if rec := h.call(token); rec.Code == http.StatusUnauthorized {
		t.Fatalf("a session token was refused as revoked: %s", rec.Body.String())
	}
}
