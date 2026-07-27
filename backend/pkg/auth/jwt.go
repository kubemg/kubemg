package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// ErrInvalidToken is returned for malformed, expired, or wrongly signed tokens.
var ErrInvalidToken = errors.New("invalid token")

const issuer = "kubemg"

// Claims is the KubeMG JWT payload.
type Claims struct {
	UserID   uint   `json:"uid"`
	Username string `json:"username"`
	Role     string `json:"role"`
	jwt.RegisteredClaims
}

// Manager signs and verifies KubeMG access tokens.
type Manager struct {
	secret []byte
	ttl    time.Duration
}

// NewManager builds a token manager with an HMAC secret and token lifetime.
func NewManager(secret string, ttl time.Duration) *Manager {
	if ttl <= 0 {
		ttl = 12 * time.Hour
	}
	return &Manager{secret: []byte(secret), ttl: ttl}
}

// TTL is the configured token lifetime.
func (m *Manager) TTL() time.Duration { return m.ttl }

// Generate issues a signed token for the given identity and returns it with its
// expiry time.
func (m *Manager) Generate(userID uint, username, role string) (string, time.Time, error) {
	now := time.Now()
	expiresAt := now.Add(m.ttl)

	claims := Claims{
		UserID:   userID,
		Username: username,
		Role:     role,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    issuer,
			Subject:   fmt.Sprint(userID),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
		},
	}

	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(m.secret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign token: %w", err)
	}
	return signed, expiresAt, nil
}

// Parse verifies a token and returns its claims.
func (m *Manager) Parse(token string) (*Claims, error) {
	claims := &Claims{}
	parsed, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method %q", t.Method.Alg())
		}
		return m.secret, nil
	}, jwt.WithIssuer(issuer), jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
	if err != nil || !parsed.Valid {
		return nil, ErrInvalidToken
	}
	return claims, nil
}
