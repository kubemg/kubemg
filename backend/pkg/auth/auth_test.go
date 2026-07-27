package auth

import (
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
