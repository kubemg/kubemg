package client

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// deviceServer answers the token endpoint from a script of RFC 8628 replies.
func deviceServer(t *testing.T, script []string) (*Client, *[]time.Duration) {
	t.Helper()
	calls := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/auth/cli/token" {
			http.NotFound(w, r)
			return
		}
		reply := script[min(calls, len(script)-1)]
		calls++
		if reply == "ok" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"refresh_token": "kmgr_x", "session_id": "s-1", "expires_at": time.Now().Add(time.Hour),
				"user": map[string]string{"username": "dana"},
			})
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": reply, "error_description": reply})
	}))
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, certPEM(srv))
	if err != nil {
		t.Fatal(err)
	}
	waits := &[]time.Duration{}
	c.Sleep = func(_ context.Context, d time.Duration) error {
		*waits = append(*waits, d)
		return nil
	}
	return c, waits
}

func TestWaitingBacksOffWhenToldToSlowDown(t *testing.T) {
	c, waits := deviceServer(t, []string{"authorization_pending", "slow_down", "authorization_pending", "ok"})
	session, err := c.WaitForApproval(context.Background(), Device{DeviceCode: "kmgd_x", Interval: 5, ExpiresIn: 600})
	if err != nil || session.RefreshToken != "kmgr_x" || session.User.Username != "dana" {
		t.Fatalf("session %+v %v", session, err)
	}
	want := []time.Duration{5 * time.Second, 5 * time.Second, 10 * time.Second, 10 * time.Second}
	if len(*waits) != len(want) {
		t.Fatalf("waits %v", *waits)
	}
	for i := range want {
		if (*waits)[i] != want[i] {
			t.Fatalf("waits %v, want %v", *waits, want)
		}
	}
}

func TestADeniedOrExpiredSignInEndsTheWait(t *testing.T) {
	for reply, want := range map[string]error{"access_denied": ErrDenied, "expired_token": ErrExpired} {
		c, _ := deviceServer(t, []string{"authorization_pending", reply})
		if _, err := c.WaitForApproval(context.Background(), Device{Interval: 5, ExpiresIn: 600}); !errors.Is(err, want) {
			t.Errorf("%s: %v", reply, err)
		}
	}
}

func TestAnEndedSessionIsNamedAsSuch(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer kmgr_x" {
			t.Errorf("auth header %q", r.Header.Get("Authorization"))
		}
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "this CLI session was revoked; run kubemg login"})
	}))
	t.Cleanup(srv.Close)
	c, _ := New(srv.URL, certPEM(srv))
	_, err := c.Credential(context.Background(), "kmgr_x", 7)
	if !errors.Is(err, ErrSessionEnded) {
		t.Fatalf("got %v", err)
	}
}

// certPEM is the test server's certificate, trusted as a self-signed bastion
// would be: through --ca-file.
func certPEM(srv *httptest.Server) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
}

// The refresh token is a credential: no plaintext server, ever.
func TestAServerMustBeHTTPSAndACABundleMustHoldACertificate(t *testing.T) {
	for _, server := range []string{"kubemg.example.com", "http://kubemg.example.com", "https://"} {
		if _, err := New(server, nil); err == nil {
			t.Errorf("%q was accepted", server)
		}
	}
	// Without the bastion's CA a self-signed server is not trusted.
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	c, _ := New(srv.URL, nil)
	if _, err := c.Clusters(context.Background(), "kmgr_x"); err == nil {
		t.Error("an untrusted certificate was accepted")
	}
	if _, err := New("https://kubemg.example.com", []byte("not a pem")); err == nil {
		t.Fatal("a CA file with no certificate was accepted")
	}
}
