package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	m.Run()
}

func TestHashPasswordRoundTrip(t *testing.T) {
	hash, err := HashPassword("s3cret")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	if hash == "s3cret" {
		t.Fatal("password was stored in plain text")
	}
	if !CheckPassword(hash, "s3cret") {
		t.Fatal("correct password was rejected")
	}
	if CheckPassword(hash, "wrong") {
		t.Fatal("incorrect password was accepted")
	}
}

func TestHashPasswordIsSalted(t *testing.T) {
	first, err := HashPassword("s3cret")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	second, err := HashPassword("s3cret")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	if first == second {
		t.Fatal("expected distinct hashes for the same password")
	}
}

func TestHashPasswordRejectsEmpty(t *testing.T) {
	if _, err := HashPassword(""); !errors.Is(err, ErrEmptyPassword) {
		t.Fatalf("expected ErrEmptyPassword, got %v", err)
	}
}

func TestGenerateAndParse(t *testing.T) {
	m := NewManager("secret", time.Hour)

	token, expiresAt, err := m.Generate(7, "devops", "admin")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if time.Until(expiresAt) <= 0 {
		t.Fatalf("expected a future expiry, got %s", expiresAt)
	}

	claims, err := m.Parse(token)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if claims.UserID != 7 || claims.Username != "devops" || claims.Role != "admin" {
		t.Fatalf("unexpected claims: %+v", claims)
	}
	if claims.Subject != "7" {
		t.Fatalf("expected subject \"7\", got %q", claims.Subject)
	}
}

func TestParseRejectsWrongSecret(t *testing.T) {
	token, _, err := NewManager("secret-a", time.Hour).Generate(1, "devops", "user")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	if _, err := NewManager("secret-b", time.Hour).Parse(token); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken, got %v", err)
	}
}

func TestParseRejectsExpiredToken(t *testing.T) {
	m := NewManager("secret", time.Hour)
	claims := Claims{
		UserID:   1,
		Username: "devops",
		Role:     "user",
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    issuer,
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Minute)),
			IssuedAt:  jwt.NewNumericDate(time.Now().Add(-time.Hour)),
		},
	}
	expired, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(m.secret)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	if _, err := m.Parse(expired); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken, got %v", err)
	}
}

func TestParseRejectsUnsignedToken(t *testing.T) {
	m := NewManager("secret", time.Hour)
	claims := Claims{RegisteredClaims: jwt.RegisteredClaims{
		Issuer:    issuer,
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
	}}
	unsigned, err := jwt.NewWithClaims(jwt.SigningMethodNone, claims).SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	if _, err := m.Parse(unsigned); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken for alg=none, got %v", err)
	}
}

func TestNewManagerDefaultsTTL(t *testing.T) {
	if got := NewManager("secret", 0).TTL(); got != 12*time.Hour {
		t.Fatalf("expected default TTL of 12h, got %s", got)
	}
}

func TestRequireAuth(t *testing.T) {
	m := NewManager("secret", time.Hour)
	token, _, err := m.Generate(3, "devops", "user")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	router := gin.New()
	router.GET("/protected", RequireAuth(m), func(c *gin.Context) {
		claims, ok := ClaimsFrom(c)
		if !ok {
			c.Status(http.StatusInternalServerError)
			return
		}
		c.String(http.StatusOK, claims.Username)
	})

	tests := []struct {
		name   string
		header string
		want   int
	}{
		{"valid bearer token", "Bearer " + token, http.StatusOK},
		{"lowercase scheme", "bearer " + token, http.StatusOK},
		{"missing header", "", http.StatusUnauthorized},
		{"wrong scheme", "Basic " + token, http.StatusUnauthorized},
		{"empty token", "Bearer ", http.StatusUnauthorized},
		{"garbage token", "Bearer not.a.jwt", http.StatusUnauthorized},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/protected", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != tc.want {
				t.Fatalf("expected status %d, got %d", tc.want, rec.Code)
			}
			if tc.want == http.StatusOK && !strings.Contains(rec.Body.String(), "devops") {
				t.Fatalf("expected handler to see claims, got %q", rec.Body.String())
			}
		})
	}
}

func TestRequireRole(t *testing.T) {
	m := NewManager("secret", time.Hour)

	router := gin.New()
	router.GET("/admin", RequireAuth(m), RequireRole("admin"), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	for _, tc := range []struct {
		role string
		want int
	}{
		{"admin", http.StatusOK},
		{"user", http.StatusForbidden},
		{"", http.StatusForbidden},
	} {
		token, _, err := m.Generate(1, "devops", tc.role)
		if err != nil {
			t.Fatalf("generate: %v", err)
		}

		req := httptest.NewRequest(http.MethodGet, "/admin", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != tc.want {
			t.Fatalf("role %q: expected status %d, got %d", tc.role, tc.want, rec.Code)
		}
	}
}

func TestRequireRoleWithoutAuthMiddleware(t *testing.T) {
	router := gin.New()
	router.GET("/admin", RequireRole("admin"), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

// wsUpgradeRequest builds a request that RequireAuth recognises as a
// WebSocket handshake, with the ticket (if any) on the query string the way a
// browser's WebSocket constructor has to send it.
func wsUpgradeRequest(query string) *http.Request {
	target := "/protected"
	if query != "" {
		target += "?" + query
	}
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	return req
}

func TestIssueAndRedeemWSTicket(t *testing.T) {
	m := NewManager("secret", time.Hour)
	claims := &Claims{UserID: 9, Username: "operator", Role: "admin"}

	ticket, err := m.IssueWSTicket(context.Background(), claims)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if ticket == "" {
		t.Fatal("expected a non-empty ticket")
	}

	redeemed, ok := m.redeemWSTicket(context.Background(), ticket)
	if !ok {
		t.Fatal("expected the ticket to redeem")
	}
	if redeemed.UserID != claims.UserID || redeemed.Username != claims.Username || redeemed.Role != claims.Role {
		t.Fatalf("unexpected claims: %+v", redeemed)
	}
}

func TestRedeemWSTicketIsSingleUse(t *testing.T) {
	m := NewManager("secret", time.Hour)
	ticket, err := m.IssueWSTicket(context.Background(), &Claims{UserID: 1, Username: "devops"})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	if _, ok := m.redeemWSTicket(context.Background(), ticket); !ok {
		t.Fatal("expected the first redemption to succeed")
	}
	if _, ok := m.redeemWSTicket(context.Background(), ticket); ok {
		t.Fatal("expected a replayed ticket to be refused")
	}
}

func TestRedeemWSTicketRejectsUnknownValue(t *testing.T) {
	m := NewManager("secret", time.Hour)
	if _, ok := m.redeemWSTicket(context.Background(), "not-a-real-ticket"); ok {
		t.Fatal("expected an unminted ticket to be refused")
	}
}

// recordingTicketStore is a shared store two managers can point at, standing
// in for the database two replicas share.
type recordingTicketStore struct {
	*memoryWSTickets
	keys    []string
	failing bool
}

func (s *recordingTicketStore) PutWSTicket(ctx context.Context, hash string, payload []byte, expiresAt time.Time) error {
	s.keys = append(s.keys, hash)
	return s.memoryWSTickets.PutWSTicket(ctx, hash, payload, expiresAt)
}

func (s *recordingTicketStore) TakeWSTicket(ctx context.Context, hash string) ([]byte, bool, error) {
	if s.failing {
		return nil, false, errors.New("database unreachable")
	}
	return s.memoryWSTickets.TakeWSTicket(ctx, hash)
}

// TestWSTicketRedeemsOnAnotherReplica is the reason the store is shared: the
// ticket is minted by one request and the upgrade is the next, and a load
// balancer owes the two no affinity.
func TestWSTicketRedeemsOnAnotherReplica(t *testing.T) {
	shared := &recordingTicketStore{memoryWSTickets: newMemoryWSTickets()}
	minter := NewManager("secret", time.Hour)
	minter.UseWSTicketStore(shared)
	receiver := NewManager("secret", time.Hour)
	receiver.UseWSTicketStore(shared)

	ticket, err := minter.IssueWSTicket(context.Background(), &Claims{UserID: 4, Username: "devops", Role: "user"})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	claims, ok := receiver.redeemWSTicket(context.Background(), ticket)
	if !ok || claims.UserID != 4 || claims.Username != "devops" {
		t.Fatalf("expected the other replica to redeem the ticket, got %+v, %v", claims, ok)
	}
	if _, ok := minter.redeemWSTicket(context.Background(), ticket); ok {
		t.Fatal("expected a ticket redeemed on one replica to be refused on another")
	}
}

func TestWSTicketStoreNeverSeesTheTicket(t *testing.T) {
	shared := &recordingTicketStore{memoryWSTickets: newMemoryWSTickets()}
	m := NewManager("secret", time.Hour)
	m.UseWSTicketStore(shared)

	ticket, err := m.IssueWSTicket(context.Background(), &Claims{UserID: 1})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if len(shared.keys) != 1 || shared.keys[0] == ticket || shared.keys[0] != hashWSTicket(ticket) {
		t.Fatalf("expected the store to be keyed on the ticket's hash, got %v", shared.keys)
	}
}

func TestWSTicketUnreadableStoreRefuses(t *testing.T) {
	shared := &recordingTicketStore{memoryWSTickets: newMemoryWSTickets()}
	m := NewManager("secret", time.Hour)
	m.UseWSTicketStore(shared)

	ticket, err := m.IssueWSTicket(context.Background(), &Claims{UserID: 1})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	shared.failing = true
	if _, ok := m.redeemWSTicket(context.Background(), ticket); ok {
		t.Fatal("expected a store error to refuse the upgrade")
	}
}

// TestRequireAuthWebSocketUpgrade covers the query-string fallback that only
// a WebSocket handshake may use: a browser cannot set a header when opening
// one, so it authenticates with a ticket instead. See QueryTokenParam.
func TestRequireAuthWebSocketUpgrade(t *testing.T) {
	m := NewManager("secret", time.Hour)
	sessionToken, _, err := m.Generate(3, "devops", "user")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	ticket, err := m.IssueWSTicket(context.Background(), &Claims{UserID: 3, Username: "devops", Role: "user"})
	if err != nil {
		t.Fatalf("issue ticket: %v", err)
	}

	router := gin.New()
	router.GET("/protected", RequireAuth(m), func(c *gin.Context) {
		claims, ok := ClaimsFrom(c)
		if !ok {
			c.Status(http.StatusInternalServerError)
			return
		}
		c.String(http.StatusOK, claims.Username)
	})

	t.Run("valid ticket succeeds", func(t *testing.T) {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, wsUpgradeRequest(QueryTokenParam+"="+ticket))
		if rec.Code != http.StatusOK {
			t.Fatalf("expected status %d, got %d: %s", http.StatusOK, rec.Code, rec.Body.String())
		}
	})

	t.Run("a redeemed ticket cannot be replayed", func(t *testing.T) {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, wsUpgradeRequest(QueryTokenParam+"="+ticket))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
		}
	})

	t.Run("a raw session JWT on the query string is refused", func(t *testing.T) {
		// The whole point of the ticket is that the session token itself never
		// rides on a URL. Presenting it directly must not work as a shortcut.
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, wsUpgradeRequest(QueryTokenParam+"="+sessionToken))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
		}
	})

	t.Run("query fallback is refused on a non-upgrade request", func(t *testing.T) {
		ticket, err := m.IssueWSTicket(context.Background(), &Claims{UserID: 3, Username: "devops", Role: "user"})
		if err != nil {
			t.Fatalf("issue ticket: %v", err)
		}
		req := httptest.NewRequest(http.MethodGet, "/protected?"+QueryTokenParam+"="+ticket, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
		}
	})

	t.Run("missing ticket on an upgrade is refused", func(t *testing.T) {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, wsUpgradeRequest(""))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
		}
	})
}
