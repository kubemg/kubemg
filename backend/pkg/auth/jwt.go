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

// ScopeProxy marks a token that may only be replayed against one cluster's
// kubectl proxy. It is what generated kubeconfigs carry in agent mode: the file
// lands on a laptop, so it must not also open the rest of the KubeMG API.
const ScopeProxy = "proxy"

// Claims is the KubeMG JWT payload.
type Claims struct {
	UserID   uint   `json:"uid"`
	Username string `json:"username"`
	Role     string `json:"role"`
	// Scope narrows what the token may do. Empty is a full session token;
	// ScopeProxy is confined to ClusterID's proxy routes.
	Scope string `json:"scope,omitempty"`
	// ClusterID is the only cluster a scoped token may address.
	ClusterID uint `json:"cid,omitempty"`
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
	return m.sign(Claims{UserID: userID, Username: username, Role: role}, m.ttl)
}

// GenerateProxyToken issues a credential that only works against one cluster's
// kubectl proxy, for the given lifetime. This is the token a generated
// kubeconfig carries when KubeMG reaches the cluster through an agent tunnel
// and therefore has no API server to mint a service account token on.
func (m *Manager) GenerateProxyToken(
	userID uint, username, role string, clusterID uint, ttl time.Duration,
) (string, time.Time, error) {
	if ttl <= 0 {
		ttl = m.ttl
	}
	return m.sign(Claims{
		UserID:    userID,
		Username:  username,
		Role:      role,
		Scope:     ScopeProxy,
		ClusterID: clusterID,
	}, ttl)
}

func (m *Manager) sign(claims Claims, ttl time.Duration) (string, time.Time, error) {
	now := time.Now()
	expiresAt := now.Add(ttl)

	claims.RegisteredClaims = jwt.RegisteredClaims{
		Issuer:    issuer,
		Subject:   fmt.Sprint(claims.UserID),
		IssuedAt:  jwt.NewNumericDate(now),
		NotBefore: jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(expiresAt),
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
