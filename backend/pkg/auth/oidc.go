package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * OIDC.
 *
 * Authorization code flow with PKCE. The code exchange is server to server and
 * the ID token is verified against the issuer's published keys, so nothing the
 * browser hands back is believed on its own — a callback carrying a forged code
 * fails at the exchange, and one carrying a stolen code fails the PKCE check
 * because the verifier never left this process.
 *
 * PKCE is used even though KubeMG is a confidential client with a secret. It
 * costs one hash and it is what makes a code intercepted in a redirect chain
 * useless, which is the failure mode a bastion cannot afford.
 */

// oidcDiscoveryTTL is how long a discovered issuer configuration is reused.
// Discovery is a round trip to the IdP; doing it on every sign-in makes login
// latency depend on someone else's uptime for no benefit, and an issuer that
// rotates its endpoints does not do it in fifteen minutes.
const oidcDiscoveryTTL = 15 * time.Minute

// OIDCClient is one configured provider, ready to start and finish a sign-in.
type OIDCClient struct {
	provider *oidc.Provider
	config   *db.SSOProviderConfig
	oauth    oauth2.Config
	verifier *oidc.IDTokenVerifier
}

// OIDCAuthRequest is everything a caller must keep until the callback comes
// back. The verifier and the nonce never reach the browser: the browser gets a
// URL and an opaque state, and these stay here to be checked against it.
type OIDCAuthRequest struct {
	URL      string
	Nonce    string
	Verifier string
}

type discovered struct {
	provider *oidc.Provider
	at       time.Time
}

var (
	discoveryMu    sync.Mutex
	discoveryCache = map[string]discovered{}
)

// discover reads an issuer's well-known configuration, reusing a recent read.
func discover(ctx context.Context, issuer string) (*oidc.Provider, error) {
	discoveryMu.Lock()
	cached, ok := discoveryCache[issuer]
	discoveryMu.Unlock()
	if ok && time.Since(cached.at) < oidcDiscoveryTTL {
		return cached.provider, nil
	}

	provider, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, fmt.Errorf("discover issuer %q: %w", issuer, err)
	}

	discoveryMu.Lock()
	discoveryCache[issuer] = discovered{provider: provider, at: time.Now()}
	discoveryMu.Unlock()
	return provider, nil
}

// NewOIDCClient prepares a provider for use. redirectURL must be the callback
// KubeMG serves and must match what the IdP has registered, byte for byte —
// a mismatch is the single most common reason a correct configuration fails.
func NewOIDCClient(
	ctx context.Context, config *db.SSOProviderConfig, redirectURL string,
) (*OIDCClient, error) {
	if config == nil || config.Protocol != db.ProtocolOIDC {
		return nil, errors.New("provider is not an OIDC provider")
	}
	if strings.TrimSpace(config.IssuerURL) == "" {
		return nil, errors.New("this provider has no issuer URL")
	}
	if strings.TrimSpace(config.ClientID) == "" {
		return nil, errors.New("this provider has no client ID")
	}

	provider, err := discover(ctx, config.IssuerURL)
	if err != nil {
		return nil, err
	}

	scopes := []string{oidc.ScopeOpenID}
	for _, scope := range strings.Fields(config.Scopes) {
		if scope != oidc.ScopeOpenID {
			scopes = append(scopes, scope)
		}
	}

	return &OIDCClient{
		provider: provider,
		config:   config,
		oauth: oauth2.Config{
			ClientID:     config.ClientID,
			ClientSecret: config.ClientSecret,
			Endpoint:     provider.Endpoint(),
			RedirectURL:  redirectURL,
			Scopes:       scopes,
		},
		verifier: provider.Verifier(&oidc.Config{ClientID: config.ClientID}),
	}, nil
}

// AuthRequest builds the redirect that starts a sign-in.
func (c *OIDCClient) AuthRequest(state string) (OIDCAuthRequest, error) {
	nonce, err := NewStateToken()
	if err != nil {
		return OIDCAuthRequest{}, err
	}
	verifier := oauth2.GenerateVerifier()

	return OIDCAuthRequest{
		URL: c.oauth.AuthCodeURL(
			state,
			oidc.Nonce(nonce),
			oauth2.S256ChallengeOption(verifier),
			oauth2.AccessTypeOnline,
		),
		Nonce:    nonce,
		Verifier: verifier,
	}, nil
}

// Exchange completes a sign-in: it trades the authorization code for tokens,
// verifies the ID token, and reads the identity out of its claims.
func (c *OIDCClient) Exchange(
	ctx context.Context, code, verifier, nonce string,
) (db.SSOIdentity, error) {
	token, err := c.oauth.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return db.SSOIdentity{}, fmt.Errorf("exchange authorization code: %w", err)
	}

	raw, ok := token.Extra("id_token").(string)
	if !ok || raw == "" {
		return db.SSOIdentity{}, errors.New("the provider returned no ID token")
	}

	idToken, err := c.verifier.Verify(ctx, raw)
	if err != nil {
		return db.SSOIdentity{}, fmt.Errorf("verify ID token: %w", err)
	}
	// The nonce ties this token to the authorization request KubeMG started.
	// Without the check, a token minted for another session of the same client
	// would be accepted here.
	if idToken.Nonce != nonce {
		return db.SSOIdentity{}, errors.New("the ID token does not match this sign-in request")
	}

	claims := map[string]any{}
	if err := idToken.Claims(&claims); err != nil {
		return db.SSOIdentity{}, fmt.Errorf("read ID token claims: %w", err)
	}

	identity := c.identityFrom(claims, idToken.Subject)

	// Plenty of directories keep group membership out of the ID token to keep it
	// small and serve it from the UserInfo endpoint instead. A provider that
	// does not have one, or refuses, leaves the identity as it is rather than
	// failing a sign-in that has already succeeded.
	if len(identity.Groups) == 0 {
		if info, err := c.provider.UserInfo(ctx, oauth2.StaticTokenSource(token)); err == nil {
			extra := map[string]any{}
			if err := info.Claims(&extra); err == nil {
				identity.Groups = dedupe(
					claimStrings(extra, c.config.GroupsClaim, groupsClaimCandidates),
				)
				if identity.Email == "" {
					identity.Email = claimString(extra, c.config.EmailClaim, emailClaimCandidates)
				}
			}
		}
	}

	if identity.Username == "" {
		return db.SSOIdentity{}, errors.New("the ID token carries no username claim")
	}
	return identity, nil
}

func (c *OIDCClient) identityFrom(claims map[string]any, subject string) db.SSOIdentity {
	username := claimString(claims, c.config.UsernameClaim, usernameClaimCandidates)
	if username == "" {
		username = subject
	}
	return db.SSOIdentity{
		// The subject is the only claim an issuer promises is stable and unique,
		// which is exactly what an account has to be matched on.
		ExternalID: subject,
		Username:   username,
		Email:      claimString(claims, c.config.EmailClaim, emailClaimCandidates),
		Groups:     dedupe(claimStrings(claims, c.config.GroupsClaim, groupsClaimCandidates)),
	}
}

// CheckOIDC verifies that a provider's issuer is reachable and serves a
// discovery document, which is what an operator wants to know before they save
// a configuration and find out at someone's next sign-in.
func CheckOIDC(ctx context.Context, config *db.SSOProviderConfig) (string, error) {
	provider, err := discover(ctx, strings.TrimRight(strings.TrimSpace(config.IssuerURL), "/"))
	if err != nil {
		return "", err
	}
	endpoint := provider.Endpoint()
	if endpoint.AuthURL == "" || endpoint.TokenURL == "" {
		return "", errors.New("the issuer's discovery document names no authorization or token endpoint")
	}
	return fmt.Sprintf("Discovery succeeded; authorization endpoint %s", endpoint.AuthURL), nil
}
