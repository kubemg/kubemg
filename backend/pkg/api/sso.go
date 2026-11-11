package api

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/auth"
	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * Enterprise sign-in.
 *
 * Two surfaces, and they are deliberately not the same shape. The *public* one
 * is what an unauthenticated browser touches: which providers exist, where a
 * sign-in starts, and where it comes back to. It says as little as it can — a
 * name, a protocol and an id — because it is readable by anyone who can reach
 * the login page. The *administrative* one is the configuration behind it, and
 * no secret ever leaves through either.
 *
 * The identity a provider asserts is turned into KubeMG access by
 * db.SyncSSOUserAndGroups, not here. This file's job ends at "the directory says
 * this is Ada and she is in these groups"; what that is worth is the mapping
 * rules, which is what keeps a protocol engine from quietly becoming an
 * authorization decision.
 */

const (
	// ssoFlowTTL bounds how long a started sign-in can be completed. It is
	// generous enough for a password manager and an MFA prompt, and short enough
	// that an abandoned state is not sitting around to be replayed.
	ssoFlowTTL = 10 * time.Minute
	// consoleCallbackPath is where the browser app finishes a federated sign-in.
	consoleCallbackPath = "/auth/callback"

	// maxSSOFlows and maxSSOFlowsPerSource bound the sign-ins waiting on an
	// IdP. Starting one needs no session, so without a bound a loop on the
	// login route grew the map for ten minutes per request.
	//
	// Past a bound the oldest waiting flow is dropped, never the new one
	// refused: a refusal would let anyone sharing an address (a NAT, or every
	// caller at once when KubeMG sits behind a proxy it was not told to
	// trust) lock everyone else out of signing in. A dropped flow costs its
	// owner one more click. The per-source bound comes first, so one address
	// cannot push everyone else's flows out of the shared store.
	maxSSOFlows          = 10000
	maxSSOFlowsPerSource = 100
)

// ssoBindingPattern is the console's per-tab value that ties a sign-in to the
// browser that started it. It is opaque to the server, which only checks that
// it is a token and hands it back.
var ssoBindingPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

// ssoFlow is the half of a sign-in that must not travel with the browser: the
// PKCE verifier, the nonce, and which provider and console the flow belongs to.
// The browser carries only the opaque state that points at it.
type ssoFlow struct {
	providerID uint
	nonce      string
	verifier   string
	// redirect is the console origin to hand the session back to, resolved
	// against the allowed origins when the flow started.
	redirect  string
	expiresAt time.Time
	// source is the address that started the flow (its /64 for IPv6), for
	// the per-source bound.
	source string
	// binding is the console's value for the tab that started the sign-in,
	// handed back beside the session so that tab can tell a sign-in it
	// started from one somebody else started and sent it the end of.
	binding string
	// samlRequestID is the ID of the AuthnRequest sent to a SAML IdP. The
	// assertion has to answer it, so a response captured from another
	// sign-in cannot be posted against this one.
	samlRequestID string
}

// flowStore holds in-flight sign-ins.
//
// It is in memory on purpose and it is the one piece of state in KubeMG that
// does not survive a restart: a flow is worthless ninety seconds after it is
// created, and persisting PKCE verifiers to buy that would be storing a secret
// to solve a problem the user fixes by clicking sign-in again. The consequence
// is worth stating plainly: two KubeMG replicas behind a round-robin load
// balancer need session affinity for the callback, or half of federated
// sign-ins land on the replica that never saw the request start.
type flowStore struct {
	mu    sync.Mutex
	flows map[string]*flowEntry
	// order holds every waiting flow's state oldest first, and bySource the
	// same per source. Every flow lives for the same TTL, so oldest is also
	// soonest to expire, and both the sweep and the bounds work from the front.
	order    *list.List
	bySource map[string]*list.List
}

type flowEntry struct {
	flow   ssoFlow
	global *list.Element
	local  *list.Element
}

func newFlowStore() *flowStore {
	return &flowStore{flows: map[string]*flowEntry{}, order: list.New(), bySource: map[string]*list.List{}}
}

// put stores a flow, first dropping expired ones and then, past either bound,
// the oldest waiting flow from the same source or from everyone.
func (f *flowStore) put(state string, flow ssoFlow) {
	f.mu.Lock()
	defer f.mu.Unlock()

	now := time.Now()
	for front := f.order.Front(); front != nil; front = f.order.Front() {
		key := front.Value.(string)
		if !now.After(f.flows[key].flow.expiresAt) {
			break
		}
		f.forget(key)
	}
	f.forget(state)

	flow.source = flowSourceKey(flow.source)
	for local := f.bySource[flow.source]; local != nil && local.Len() >= maxSSOFlowsPerSource; local = f.bySource[flow.source] {
		f.forget(local.Front().Value.(string))
	}
	for f.order.Len() >= maxSSOFlows {
		f.forget(f.order.Front().Value.(string))
	}

	local := f.bySource[flow.source]
	if local == nil {
		local = list.New()
		f.bySource[flow.source] = local
	}
	f.flows[state] = &flowEntry{
		flow:   flow,
		global: f.order.PushBack(state),
		local:  local.PushBack(state),
	}
}

// forget removes a flow from the map and both orders. The caller holds the lock.
func (f *flowStore) forget(state string) {
	entry, ok := f.flows[state]
	if !ok {
		return
	}
	delete(f.flows, state)
	f.order.Remove(entry.global)
	if local := f.bySource[entry.flow.source]; local != nil {
		local.Remove(entry.local)
		if local.Len() == 0 {
			delete(f.bySource, entry.flow.source)
		}
	}
}

// take returns a flow and removes it, so a state is good for exactly one
// callback — a replayed callback finds nothing.
func (f *flowStore) take(state string) (ssoFlow, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	entry, ok := f.flows[state]
	if !ok {
		return ssoFlow{}, false
	}
	f.forget(state)
	if time.Now().After(entry.flow.expiresAt) {
		return ssoFlow{}, false
	}
	return entry.flow, true
}

// flowSourceKey is the address a flow is counted against. An IPv6 caller
// usually holds a whole /64, so it is counted as one, or rotating through it
// would sidestep the per-source bound.
func flowSourceKey(address string) string {
	ip := net.ParseIP(address)
	if ip == nil {
		return address
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	prefix := net.CIDRMask(64, 128)
	return (&net.IPNet{IP: ip.Mask(prefix), Mask: prefix}).String()
}

// ssoProviderPublic is one provider as the login page sees it: enough to draw a
// button and nothing else.
type ssoProviderPublic struct {
	ID       uint   `json:"id"`
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	// Interactive is false for LDAP, which takes a username and password on
	// KubeMG's own form instead of sending the browser to an IdP.
	Interactive bool `json:"interactive"`
}

// ssoProviderResponse is one provider as an administrator sees it. Secrets are
// represented by whether there is one; the three URLs are rendered because they
// are what has to be pasted into the IdP, and an operator typing them out by
// hand is how a configuration ends up failing at the audience check.
type ssoProviderResponse struct {
	db.SSOProviderConfig

	HasClientSecret bool `json:"has_client_secret"`
	HasBindPassword bool `json:"has_bind_password"`

	// RedirectURL is the OIDC redirect URI / SAML assertion consumer service URL.
	RedirectURL string `json:"redirect_url"`
	// EntityID is what KubeMG calls itself to a SAML IdP.
	EntityID string `json:"entity_id,omitempty"`
	// MetadataURL serves KubeMG's own SP metadata for upload into the IdP.
	MetadataURL string `json:"metadata_url,omitempty"`
}

func (s *server) toSSOProviderResponse(provider db.SSOProviderConfig, publicURL string) ssoProviderResponse {
	out := ssoProviderResponse{
		SSOProviderConfig: provider,
		HasClientSecret:   provider.HasClientSecret(),
		HasBindPassword:   provider.HasBindPassword(),
	}
	if provider.Protocol == db.ProtocolLDAP {
		return out
	}

	out.RedirectURL = ssoCallbackURL(publicURL, provider.ID)
	if provider.Protocol == db.ProtocolSAML {
		out.EntityID = samlEntityID(provider, publicURL)
		out.MetadataURL = ssoMetadataURL(publicURL, provider.ID)
	}
	return out
}

func ssoCallbackURL(publicURL string, providerID uint) string {
	return fmt.Sprintf("%s/api/v1/auth/sso/providers/%d/callback", publicURL, providerID)
}

func ssoMetadataURL(publicURL string, providerID uint) string {
	return fmt.Sprintf("%s/api/v1/auth/sso/providers/%d/metadata", publicURL, providerID)
}

// samlEntityID is what KubeMG calls itself. An operator can override it — some
// IdPs are registered against a name chosen years ago — and the default is the
// metadata URL, which is both unique and resolvable.
func samlEntityID(provider db.SSOProviderConfig, publicURL string) string {
	if configured := strings.TrimSpace(provider.SAMLEntityID); configured != "" {
		return configured
	}
	return ssoMetadataURL(publicURL, provider.ID)
}

// listSSOProvidersPublic is what the login page reads. It is unauthenticated by
// necessity — nobody has signed in yet — so it carries no configuration at all,
// and a disabled provider is simply absent.
func (s *server) listSSOProvidersPublic(c *gin.Context) {
	providers, err := s.store.ListSSOProviders(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load the identity providers"})
		return
	}

	out := []ssoProviderPublic{}
	for _, provider := range providers {
		if !provider.Enabled {
			continue
		}
		out = append(out, ssoProviderPublic{
			ID:          provider.ID,
			Name:        provider.Name,
			Protocol:    provider.Protocol,
			Interactive: provider.Interactive(),
		})
	}
	c.JSON(http.StatusOK, gin.H{"providers": out})
}

// startSSOLogin sends the browser to the identity provider.
func (s *server) startSSOLogin(c *gin.Context) {
	provider, ok := s.enabledProvider(c)
	if !ok {
		return
	}
	if !provider.Interactive() {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "this provider signs in with a username and password, not a redirect",
		})
		return
	}

	settings := s.settings(c.Request.Context())
	redirect, err := s.resolveConsoleOrigin(c.Query("redirect_uri"), settings.PublicURL)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	binding := strings.TrimSpace(c.Query("binding"))
	if binding != "" && !ssoBindingPattern.MatchString(binding) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "the sign-in's browser binding is not one the console writes"})
		return
	}

	state, err := auth.NewStateToken()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not start the sign-in"})
		return
	}
	flow := ssoFlow{
		providerID: provider.ID,
		redirect:   redirect,
		expiresAt:  time.Now().Add(ssoFlowTTL),
		source:     c.ClientIP(),
		binding:    binding,
	}

	var target string
	switch provider.Protocol {
	case db.ProtocolOIDC:
		client, err := auth.NewOIDCClient(
			c.Request.Context(), provider, ssoCallbackURL(settings.PublicURL, provider.ID),
		)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}
		request, err := client.AuthRequest(state)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		target, flow.nonce, flow.verifier = request.URL, request.Nonce, request.Verifier

	case db.ProtocolSAML:
		client, err := auth.NewSAMLClient(
			c.Request.Context(), provider,
			ssoCallbackURL(settings.PublicURL, provider.ID),
			samlEntityID(*provider, settings.PublicURL),
		)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}
		if target, flow.samlRequestID, err = client.AuthURL(state); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported provider protocol"})
		return
	}

	s.ssoFlows.put(state, flow)
	c.Redirect(http.StatusFound, target)
}

// ssoCallback finishes an interactive sign-in. OIDC arrives as a GET with a
// code, SAML as a POST with an assertion; both are matched back to the flow that
// started them before anything they carry is believed.
func (s *server) ssoCallback(c *gin.Context) {
	provider, ok := s.enabledProvider(c)
	if !ok {
		return
	}

	state := strings.TrimSpace(c.Query("state"))
	if state == "" {
		state = strings.TrimSpace(c.PostForm("RelayState"))
	}
	flow, found := s.ssoFlows.take(state)
	if !found || flow.providerID != provider.ID {
		// Deliberately vague and deliberately not fatal-looking: the ordinary
		// cause is a bookmarked callback or a browser left open over lunch.
		s.failSSO(c, s.consoleFallback(), "This sign-in request has expired. Please try again.")
		return
	}

	// An IdP that refused says so in the callback rather than by not answering,
	// and its own description is more useful than anything invented here.
	if reason := strings.TrimSpace(c.Query("error_description")); reason != "" {
		s.failSSO(c, flow.redirect, reason)
		return
	}
	if reason := strings.TrimSpace(c.Query("error")); reason != "" {
		s.failSSO(c, flow.redirect, reason)
		return
	}

	var (
		identity db.SSOIdentity
		err      error
	)
	settings := s.settings(c.Request.Context())
	switch provider.Protocol {
	case db.ProtocolOIDC:
		code := strings.TrimSpace(c.Query("code"))
		if code == "" {
			s.failSSO(c, flow.redirect, "The identity provider returned no authorization code.")
			return
		}
		var client *auth.OIDCClient
		client, err = auth.NewOIDCClient(
			c.Request.Context(), provider, ssoCallbackURL(settings.PublicURL, provider.ID),
		)
		if err == nil {
			identity, err = client.Exchange(c.Request.Context(), code, flow.verifier, flow.nonce)
		}

	case db.ProtocolSAML:
		response := strings.TrimSpace(c.PostForm("SAMLResponse"))
		if response == "" {
			s.failSSO(c, flow.redirect, "The identity provider returned no SAML assertion.")
			return
		}
		var client *auth.SAMLClient
		client, err = auth.NewSAMLClient(
			c.Request.Context(), provider,
			ssoCallbackURL(settings.PublicURL, provider.ID),
			samlEntityID(*provider, settings.PublicURL),
		)
		if err == nil {
			identity, err = client.Assertion(response, flow.samlRequestID)
		}

	default:
		s.failSSO(c, flow.redirect, "unsupported provider protocol")
		return
	}
	if err != nil {
		s.failSSO(c, flow.redirect, err.Error())
		return
	}

	session, err := s.completeSSOLogin(c.Request.Context(), provider, identity)
	if err != nil {
		s.failSSO(c, flow.redirect, err.Error())
		return
	}

	// The session goes back in the URL *fragment*, which a browser never sends
	// to a server: a token in the query string would land in the access log of
	// every proxy in front of the console and in the browser's own history.
	//
	// The binding rides beside it: the console accepts the session only in the
	// tab that started this sign-in, so a callback URL somebody else started
	// and sent on cannot sign the person who opens it into their account.
	c.Redirect(http.StatusFound, fmt.Sprintf(
		"%s%s#token=%s&expires_at=%s&binding=%s",
		flow.redirect, consoleCallbackPath,
		url.QueryEscape(session.Token),
		url.QueryEscape(session.ExpiresAt.Format(time.RFC3339)),
		url.QueryEscape(flow.binding),
	))
}

// ldapLogin is the non-interactive sign-in: KubeMG's own form, checked against
// the directory. It answers with a session exactly like the local login does, so
// the browser needs no second code path.
func (s *server) ldapLogin(c *gin.Context) {
	provider, ok := s.enabledProvider(c)
	if !ok {
		return
	}
	if provider.Protocol != db.ProtocolLDAP {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "this provider signs in by redirect, not with a username and password",
		})
		return
	}

	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "username and password are required"})
		return
	}

	// Throttled here as well as on the local form, and for a second reason: a
	// directory usually has a lockout policy of its own, and guesses relayed
	// through KubeMG would otherwise spend the owner's directory account.
	attempt, ok := s.beginSignIn(c, signInAccount("ldap:"+strconv.FormatUint(uint64(provider.ID), 10), req.Username))
	if !ok {
		return
	}
	defer attempt.Release()

	identity, err := auth.LDAPAuthenticate(c.Request.Context(), provider, req.Username, req.Password)
	if errors.Is(err, auth.ErrLDAPCredentials) {
		s.signInFailed(c, attempt)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		return
	}
	if err != nil {
		// A directory that cannot be reached is not a wrong password, and saying
		// so is the difference between an operator checking the network and a
		// user retyping their password ten times.
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}

	attempt.Succeed()

	result, err := s.syncSSOAccount(c.Request.Context(), provider, identity)
	if err != nil {
		c.JSON(ssoStatusFor(err), gin.H{"error": err.Error()})
		return
	}
	// A directory bind is a password and nothing else, so an LDAP account owes
	// KubeMG's second factor exactly as a local one does — see User.MFAApplies.
	user := result.User
	user.Normalize()
	if s.challengeSignIn(c, user) {
		return
	}
	s.issueSession(c, user, false)
}

// ssoMetadata serves KubeMG's SAML SP metadata, which is what an operator
// uploads into their IdP.
func (s *server) ssoMetadata(c *gin.Context) {
	provider, ok := s.provider(c)
	if !ok {
		return
	}
	if provider.Protocol != db.ProtocolSAML {
		c.JSON(http.StatusBadRequest, gin.H{"error": "this provider is not a SAML provider"})
		return
	}

	publicURL := s.settings(c.Request.Context()).PublicURL
	metadata, err := auth.SPMetadata(
		samlEntityID(*provider, publicURL), ssoCallbackURL(publicURL, provider.ID),
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.Data(http.StatusOK, "application/samlmetadata+xml", metadata)
}

// completeSSOLogin turns a verified identity into a KubeMG session: the sync
// provisions and reconciles the account, and the JWT issued afterwards is the
// same one a local sign-in gets — a federated user is not a second class of
// caller anywhere else in the API.
func (s *server) completeSSOLogin(
	ctx context.Context, provider *db.SSOProviderConfig, identity db.SSOIdentity,
) (loginResponse, error) {
	result, err := s.syncSSOAccount(ctx, provider, identity)
	if err != nil {
		return loginResponse{}, err
	}

	token, expiresAt, err := s.jwt.Generate(result.User.ID, result.User.Username, result.User.Role)
	if err != nil {
		return loginResponse{}, errors.New("could not issue token")
	}

	return loginResponse{
		Token:     token,
		ExpiresAt: expiresAt,
		User:      toUserResponse(result.User),
	}, nil
}

// syncSSOAccount provisions and reconciles the account a verified identity
// names. It is completeSSOLogin without the session, for the one federated
// path — LDAP — whose sign-in may still owe a second factor.
func (s *server) syncSSOAccount(
	ctx context.Context, provider *db.SSOProviderConfig, identity db.SSOIdentity,
) (*db.SSOSyncResult, error) {
	result, err := s.store.SyncSSOUserAndGroups(ctx, provider, identity)
	if err != nil {
		return nil, err
	}

	if s.logger != nil {
		s.logger.Info("federated sign-in",
			"provider", provider.Name,
			"protocol", provider.Protocol,
			"username", result.User.Username,
			"provisioned", result.Created,
			"matched_groups", result.MatchedGroups,
			"cluster_grants", result.ClusterGrants,
		)
	}
	return result, nil
}

// ssoStatusFor maps the sync's refusals onto status codes. They are all
// deliberate answers rather than failures, so none of them is a 500.
func ssoStatusFor(err error) int {
	switch {
	case errors.Is(err, db.ErrSSONoAccount), errors.Is(err, db.ErrSSOAccountDisabled),
		errors.Is(err, db.ErrSSOUnsafeUsername):
		return http.StatusForbidden
	case errors.Is(err, db.ErrSSOAccountConflict):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

// failSSO sends the browser back to the console with a message instead of
// leaving it on an API URL showing raw JSON — a person who just came back from
// their IdP is not debugging an API.
func (s *server) failSSO(c *gin.Context, origin, message string) {
	c.Redirect(http.StatusFound, fmt.Sprintf(
		"%s%s#error=%s", origin, consoleCallbackPath, url.QueryEscape(message),
	))
}

// provider loads the provider named in the path.
func (s *server) provider(c *gin.Context) (*db.SSOProviderConfig, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid provider id"})
		return nil, false
	}

	provider, err := s.store.SSOProviderByID(c.Request.Context(), uint(id))
	if errors.Is(err, db.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "identity provider not found"})
		return nil, false
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load the identity provider"})
		return nil, false
	}
	provider.Normalize()
	return provider, true
}

// enabledProvider additionally refuses a provider an administrator has parked.
// Disabling one has to stop sign-ins immediately, which is the whole reason the
// switch exists.
func (s *server) enabledProvider(c *gin.Context) (*db.SSOProviderConfig, bool) {
	provider, ok := s.provider(c)
	if !ok {
		return nil, false
	}
	if !provider.Enabled {
		c.JSON(http.StatusForbidden, gin.H{"error": "this identity provider is disabled"})
		return nil, false
	}
	return provider, true
}

// resolveConsoleOrigin decides where a finished sign-in is handed back to.
//
// This is an open-redirect surface and is treated as one: a caller may only name
// an origin that is already trusted to talk to this API — the configured browser
// origins and KubeMG's own public URL — because anything else would make the
// login endpoint a way to bounce a session token to an attacker's page.
func (s *server) resolveConsoleOrigin(requested, publicURL string) (string, error) {
	allowed := s.consoleOrigins(publicURL)
	if strings.TrimSpace(requested) == "" {
		return allowed[0], nil
	}

	parsed, err := url.Parse(strings.TrimSpace(requested))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", errors.New("redirect_uri must be an absolute URL")
	}
	origin := parsed.Scheme + "://" + parsed.Host

	for _, candidate := range allowed {
		if strings.EqualFold(candidate, origin) {
			return candidate, nil
		}
	}
	return "", errors.New("redirect_uri is not an allowed console origin")
}

// consoleOrigins is where a console may live, most-specific first. A wildcard
// CORS configuration is not a licence to redirect anywhere, so it contributes
// nothing here and the public URL stands alone.
func (s *server) consoleOrigins(publicURL string) []string {
	origins := []string{}
	for _, origin := range s.allowedOrigins {
		if origin = strings.TrimRight(strings.TrimSpace(origin), "/"); origin != "" && origin != "*" {
			origins = append(origins, origin)
		}
	}
	if trimmed := strings.TrimRight(strings.TrimSpace(publicURL), "/"); trimmed != "" {
		origins = append(origins, trimmed)
	}
	if len(origins) == 0 {
		origins = append(origins, defaultPublicURL)
	}
	return origins
}

// consoleFallback is where to send a browser whose flow could not be found, and
// therefore whose intended console is unknown.
func (s *server) consoleFallback() string {
	return s.consoleOrigins(s.publicURL)[0]
}
