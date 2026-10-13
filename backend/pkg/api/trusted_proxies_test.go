package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

func clientIPWith(t *testing.T, proxies []string, remote, forwarded string) string {
	t.Helper()
	router := gin.New()
	trustProxies(router, proxies, nil)
	router.GET("/ip", func(c *gin.Context) { c.String(http.StatusOK, c.ClientIP()) })

	req := httptest.NewRequest(http.MethodGet, "/ip", nil)
	req.RemoteAddr = remote
	req.Header.Set("X-Forwarded-For", forwarded)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec.Body.String()
}

// Gin believes X-Forwarded-For from every peer unless told otherwise, which
// made the address on every audit row a header the caller wrote.
func TestForwardingHeadersAreIgnoredFromAnUntrustedPeer(t *testing.T) {
	if got := clientIPWith(t, nil, "192.0.2.1:4000", "203.0.113.9"); got != "192.0.2.1" {
		t.Fatalf("ClientIP = %q, want the connecting peer", got)
	}
	if got := clientIPWith(t, []string{"10.0.0.0/8"}, "192.0.2.1:4000", "203.0.113.9"); got != "192.0.2.1" {
		t.Fatalf("ClientIP = %q, want the peer, which is not in the trusted range", got)
	}
}

func TestForwardingHeadersAreBelievedFromATrustedProxy(t *testing.T) {
	if got := clientIPWith(t, []string{"192.0.2.0/24"}, "192.0.2.1:4000", "203.0.113.9"); got != "203.0.113.9" {
		t.Fatalf("ClientIP = %q, want the forwarded client", got)
	}
}

// A list the router cannot take trusts nobody rather than everybody.
func TestAnUnusableProxyListTrustsNone(t *testing.T) {
	if got := clientIPWith(t, []string{"not-an-address"}, "192.0.2.1:4000", "203.0.113.9"); got != "192.0.2.1" {
		t.Fatalf("ClientIP = %q, want the connecting peer", got)
	}
}

// End to end through the real router: a sign-in records where it came from,
// and a forged header does not change that.
func TestASignInRecordsThePeerNotAForgedHeader(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("devops", "pw", db.RoleUser)

	body, _ := json.Marshal(map[string]string{"username": "devops", "password": "pw"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", "198.51.100.66")
	req.RemoteAddr = "192.0.2.1:4000"
	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("sign-in failed: %d %s", rec.Code, rec.Body.String())
	}
	if got := env.store.users[user.ID].LastLoginAddr; got != "192.0.2.1" {
		t.Fatalf("last sign-in address = %q, want the connecting peer", got)
	}
}
