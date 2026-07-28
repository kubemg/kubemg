package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * The federation surface, exercised through the router.
 *
 * The engines themselves are not reachable without a live IdP, so what is tested
 * here is everything around them: which routes an unauthenticated caller can
 * touch and what they disclose, that a disabled provider stops sign-ins, that a
 * callback cannot be replayed or pointed anywhere the operator did not allow,
 * and that the administrative side never hands a secret back.
 */

// --- fake store: federation ---------------------------------------------

func (f *fakeStore) addProvider(name, protocol string, enabled bool) *db.SSOProviderConfig {
	provider := &db.SSOProviderConfig{
		ID:                f.nextID,
		Name:              name,
		Protocol:          protocol,
		Enabled:           enabled,
		AllowJIT:          true,
		DefaultSystemRole: db.SystemRoleUser,
		IssuerURL:         "https://idp.example.com",
		ClientID:          "kubemg",
		ClientSecret:      "super-secret",
		LDAPHost:          "ldap.example.com",
		LDAPBaseDN:        "dc=example,dc=com",
		LDAPBindPassword:  "bind-secret",
		SAMLMetadataURL:   "https://idp.example.com/metadata",
		CreatedAt:         time.Now(),
	}
	provider.Normalize()
	f.nextID++
	f.providers[provider.ID] = provider
	return provider
}

func (f *fakeStore) ListSSOProviders(_ context.Context) ([]db.SSOProviderConfig, error) {
	out := []db.SSOProviderConfig{}
	for _, provider := range f.providers {
		out = append(out, *provider)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (f *fakeStore) SSOProviderByID(_ context.Context, id uint) (*db.SSOProviderConfig, error) {
	provider, ok := f.providers[id]
	if !ok {
		return nil, db.ErrNotFound
	}
	copied := *provider
	return &copied, nil
}

func (f *fakeStore) CreateSSOProvider(_ context.Context, provider *db.SSOProviderConfig) error {
	for _, existing := range f.providers {
		if existing.Name == provider.Name {
			return db.ErrConflict
		}
	}
	provider.ID = f.nextID
	f.nextID++
	copied := *provider
	f.providers[provider.ID] = &copied
	return nil
}

func (f *fakeStore) UpdateSSOProvider(_ context.Context, provider *db.SSOProviderConfig) error {
	if _, ok := f.providers[provider.ID]; !ok {
		return db.ErrNotFound
	}
	copied := *provider
	f.providers[provider.ID] = &copied
	return nil
}

func (f *fakeStore) UpdateSSOProviderHealth(_ context.Context, id uint, status, message string) error {
	provider, ok := f.providers[id]
	if !ok {
		return db.ErrNotFound
	}
	now := time.Now()
	provider.LastStatus, provider.LastMessage, provider.LastCheckedAt = status, message, &now
	return nil
}

func (f *fakeStore) DeleteSSOProvider(_ context.Context, id uint) error {
	if _, ok := f.providers[id]; !ok {
		return db.ErrNotFound
	}
	delete(f.providers, id)
	for mappingID, mapping := range f.mappings {
		if mapping.ProviderID == id {
			delete(f.mappings, mappingID)
		}
	}
	return nil
}

func (f *fakeStore) ListSSOMappings(_ context.Context, providerID uint) ([]db.SSOGroupMapping, error) {
	out := []db.SSOGroupMapping{}
	for _, mapping := range f.mappings {
		if providerID == 0 || mapping.ProviderID == providerID {
			out = append(out, *mapping)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (f *fakeStore) CreateSSOMapping(_ context.Context, mapping *db.SSOGroupMapping) error {
	mapping.ID = f.nextID
	f.nextID++
	copied := *mapping
	f.mappings[mapping.ID] = &copied
	return nil
}

func (f *fakeStore) UpdateSSOMapping(_ context.Context, mapping *db.SSOGroupMapping) error {
	if _, ok := f.mappings[mapping.ID]; !ok {
		return db.ErrNotFound
	}
	copied := *mapping
	f.mappings[mapping.ID] = &copied
	return nil
}

func (f *fakeStore) DeleteSSOMapping(_ context.Context, id uint) error {
	if _, ok := f.mappings[id]; !ok {
		return db.ErrNotFound
	}
	delete(f.mappings, id)
	return nil
}

func (f *fakeStore) SyncSSOUserAndGroups(
	_ context.Context, provider *db.SSOProviderConfig, identity db.SSOIdentity,
) (*db.SSOSyncResult, error) {
	f.syncedIdentities = append(f.syncedIdentities, identity)
	if f.syncErr != nil {
		return nil, f.syncErr
	}
	if result, ok := f.syncResults[identity.Username]; ok {
		return result, nil
	}

	user := f.addUser(identity.Username, "unused", db.RoleUser)
	user.AuthSource = provider.Protocol
	user.Email = identity.Email
	return &db.SSOSyncResult{User: user, Created: true, MatchedGroups: identity.Groups}, nil
}

// --- tests ---------------------------------------------------------------

func TestPublicProviderListHidesConfiguration(t *testing.T) {
	env := newTestEnv(t)
	store := env.store
	store.addProvider("Okta", db.ProtocolOIDC, true)
	store.addProvider("Parked", db.ProtocolSAML, false)

	res := env.do(t, http.MethodGet, "/api/v1/auth/sso/providers", "", nil)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.Code)
	}

	// Nobody has signed in yet, so this answer is world-readable: it must carry
	// a name and nothing that describes how the provider is configured.
	if strings.Contains(res.Body.String(), "super-secret") ||
		strings.Contains(res.Body.String(), "idp.example.com") {
		t.Fatalf("public provider list leaked configuration: %s", res.Body.String())
	}

	var payload struct {
		Providers []ssoProviderPublic `json:"providers"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Providers) != 1 {
		t.Fatalf("providers = %d, want only the enabled one", len(payload.Providers))
	}
	if payload.Providers[0].Name != "Okta" || !payload.Providers[0].Interactive {
		t.Fatalf("unexpected provider %+v", payload.Providers[0])
	}
}

func TestLDAPProviderIsNotInteractive(t *testing.T) {
	env := newTestEnv(t)
	store := env.store
	store.addProvider("Directory", db.ProtocolLDAP, true)

	res := env.do(t, http.MethodGet, "/api/v1/auth/sso/providers", "", nil)
	var payload struct {
		Providers []ssoProviderPublic `json:"providers"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Providers) != 1 || payload.Providers[0].Interactive {
		t.Fatalf("LDAP must not be interactive: %+v", payload.Providers)
	}
}

func TestDisabledProviderRefusesLogin(t *testing.T) {
	env := newTestEnv(t)
	store := env.store
	provider := store.addProvider("Parked", db.ProtocolOIDC, false)

	res := env.do(t, http.MethodGet, providerPath(provider.ID, "/login"), "", nil)
	if res.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a disabled provider", res.Code)
	}
}

func TestInteractiveLoginRefusesForeignRedirect(t *testing.T) {
	env := newTestEnv(t)
	store := env.store
	provider := store.addProvider("Okta", db.ProtocolOIDC, true)

	// The callback hands a session token to whatever origin the flow named, so
	// naming one is an open-redirect surface and must be refused before the
	// browser ever leaves for the IdP.
	path := providerPath(provider.ID, "/login") + "?redirect_uri=" + url.QueryEscape("https://evil.example/steal")
	res := env.do(t, http.MethodGet, path, "", nil)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an origin that is not a console", res.Code)
	}
}

func TestLDAPRouteRefusesRedirectProviders(t *testing.T) {
	env := newTestEnv(t)
	store := env.store
	provider := store.addProvider("Okta", db.ProtocolOIDC, true)

	res := env.do(t, http.MethodPost, providerPath(provider.ID, "/login"), "",
		map[string]string{"username": "ada", "password": "hunter2"})
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 posting credentials to a redirect provider", res.Code)
	}
}

func TestCallbackWithUnknownStateRedirectsToConsole(t *testing.T) {
	env := newTestEnv(t)
	store := env.store
	provider := store.addProvider("Okta", db.ProtocolOIDC, true)

	res := env.do(t, http.MethodGet,
		providerPath(provider.ID, "/callback")+"?code=abc&state=never-issued", "", nil)
	if res.Code != http.StatusFound {
		t.Fatalf("status = %d, want a redirect back to the console", res.Code)
	}

	location := res.Header().Get("Location")
	if !strings.Contains(location, consoleCallbackPath) || !strings.Contains(location, "#error=") {
		t.Fatalf("location = %q, want a console callback carrying an error", location)
	}
	// A state that was never issued must not be treated as a code worth
	// exchanging, so nothing may have reached the sync.
	if len(store.syncedIdentities) != 0 {
		t.Fatalf("an unknown state reached the sync: %+v", store.syncedIdentities)
	}
}

func TestFlowStateIsSingleUse(t *testing.T) {
	flows := newFlowStore()
	flows.put("state", ssoFlow{providerID: 7, expiresAt: time.Now().Add(time.Minute)})

	if _, ok := flows.take("state"); !ok {
		t.Fatal("the first callback should find its flow")
	}
	if _, ok := flows.take("state"); ok {
		t.Fatal("a replayed callback must not find the flow again")
	}
}

func TestFlowStateExpires(t *testing.T) {
	flows := newFlowStore()
	flows.put("state", ssoFlow{providerID: 7, expiresAt: time.Now().Add(-time.Second)})

	if _, ok := flows.take("state"); ok {
		t.Fatal("an expired flow must not be usable")
	}
}

func TestSAMLMetadataDescribesThisServer(t *testing.T) {
	env := newTestEnv(t)
	store := env.store
	provider := store.addProvider("ADFS", db.ProtocolSAML, true)

	res := env.do(t, http.MethodGet, providerPath(provider.ID, "/metadata"), "", nil)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.Code)
	}

	body := res.Body.String()
	// The entity ID and the ACS URL are what the audience check enforces, so the
	// document an operator uploads has to name exactly them.
	for _, want := range []string{
		ssoCallbackURL(testPublicURL, provider.ID),
		ssoMetadataURL(testPublicURL, provider.ID),
		`WantAssertionsSigned="true"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("SP metadata is missing %q:\n%s", want, body)
		}
	}
}

func TestAdminProviderCRUDKeepsSecrets(t *testing.T) {
	env := newTestEnv(t)
	store := env.store
	admin := store.addUser("admin", "pw", db.RoleAdmin)
	token := env.tokenFor(t, admin)

	create := env.do(t, http.MethodPost, "/api/v1/admin/sso/providers", token, map[string]any{
		"name":          "Okta",
		"protocol":      "oidc",
		"issuer_url":    "https://okta.example.com",
		"client_id":     "kubemg",
		"client_secret": "the-secret",
	})
	if create.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201: %s", create.Code, create.Body.String())
	}
	if strings.Contains(create.Body.String(), "the-secret") {
		t.Fatalf("the client secret came back out: %s", create.Body.String())
	}

	var created ssoProviderResponse
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !created.HasClientSecret {
		t.Fatal("has_client_secret should be true once one is stored")
	}
	if created.RedirectURL != ssoCallbackURL(testPublicURL, created.ID) {
		t.Fatalf("redirect_url = %q, want the callback an operator registers", created.RedirectURL)
	}

	// Editing without resending the secret must keep it: that is what lets an
	// operator change a claim name without re-typing a credential.
	update := env.do(t, http.MethodPut,
		"/api/v1/admin/sso/providers/"+itoa(created.ID), token, map[string]any{
			"name":           "Okta",
			"protocol":       "oidc",
			"issuer_url":     "https://okta.example.com",
			"client_id":      "kubemg",
			"username_claim": "email",
		})
	if update.Code != http.StatusOK {
		t.Fatalf("update status = %d, want 200: %s", update.Code, update.Body.String())
	}
	if stored := store.providers[created.ID]; stored.ClientSecret != "the-secret" {
		t.Fatalf("client secret = %q, want the stored one kept", stored.ClientSecret)
	}
}

func TestAdminProviderValidationNamesTheMissingField(t *testing.T) {
	env := newTestEnv(t)
	store := env.store
	admin := store.addUser("admin", "pw", db.RoleAdmin)
	token := env.tokenFor(t, admin)

	res := env.do(t, http.MethodPost, "/api/v1/admin/sso/providers", token, map[string]any{
		"name":     "Broken",
		"protocol": "oidc",
	})
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", res.Code)
	}
	if !strings.Contains(res.Body.String(), "issuer URL") {
		t.Fatalf("error should name the missing field: %s", res.Body.String())
	}
}

func TestFederationIsAdministrative(t *testing.T) {
	env := newTestEnv(t)
	store := env.store
	user := store.addUser("dev", "pw", db.RoleUser)
	token := env.tokenFor(t, user)

	for _, path := range []string{"/api/v1/admin/sso/providers", "/api/v1/admin/sso/mappings"} {
		res := env.do(t, http.MethodGet, path, token, nil)
		if res.Code != http.StatusForbidden {
			t.Fatalf("GET %s as a non-admin = %d, want 403", path, res.Code)
		}
	}
}

func TestMappingMustGrantSomething(t *testing.T) {
	env := newTestEnv(t)
	store := env.store
	admin := store.addUser("admin", "pw", db.RoleAdmin)
	provider := store.addProvider("Okta", db.ProtocolOIDC, true)
	token := env.tokenFor(t, admin)

	// A rule that matches and confers nothing is indistinguishable from one
	// whose pattern is wrong, which is the failure nobody notices.
	res := env.do(t, http.MethodPost, "/api/v1/admin/sso/mappings", token, map[string]any{
		"provider_id":            provider.ID,
		"external_group_pattern": "platform-*",
	})
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a rule that grants nothing", res.Code)
	}
}

func TestMappingRoundTrip(t *testing.T) {
	env := newTestEnv(t)
	store := env.store
	admin := store.addUser("admin", "pw", db.RoleAdmin)
	provider := store.addProvider("Okta", db.ProtocolOIDC, true)
	group := store.addGroup("platform")
	token := env.tokenFor(t, admin)

	create := env.do(t, http.MethodPost, "/api/v1/admin/sso/mappings", token, map[string]any{
		"provider_id":            provider.ID,
		"external_group_pattern": "platform-*",
		"target_group_id":        group.ID,
		"target_k8s_role":        "edit",
		"environment_filter":     "staging",
		"namespaces":             []string{"payments", "checkout"},
	})
	if create.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", create.Code, create.Body.String())
	}

	var mapping ssoMappingResponse
	if err := json.Unmarshal(create.Body.Bytes(), &mapping); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !slices.Equal(mapping.Namespaces, []string{"payments", "checkout"}) {
		t.Fatalf("namespaces = %v, want the scope as sent", mapping.Namespaces)
	}

	list := env.do(t, http.MethodGet,
		"/api/v1/admin/sso/mappings?provider_id="+itoa(provider.ID), token, nil)
	if list.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200", list.Code)
	}

	del := env.do(t, http.MethodDelete,
		"/api/v1/admin/sso/mappings/"+itoa(mapping.ID), token, nil)
	if del.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", del.Code)
	}
}

func TestMappingRejectsUnknownGroup(t *testing.T) {
	env := newTestEnv(t)
	store := env.store
	admin := store.addUser("admin", "pw", db.RoleAdmin)
	provider := store.addProvider("Okta", db.ProtocolOIDC, true)
	token := env.tokenFor(t, admin)

	res := env.do(t, http.MethodPost, "/api/v1/admin/sso/mappings", token, map[string]any{
		"provider_id":            provider.ID,
		"external_group_pattern": "*",
		"target_group_id":        4242,
	})
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a rule pointing at a group that does not exist", res.Code)
	}
}

func TestFederatedAccountCannotUsePasswordLogin(t *testing.T) {
	env := newTestEnv(t)
	store := env.store
	user := store.addUser("ada", "hunter2", db.RoleUser)
	user.AuthSource = db.ProtocolOIDC

	res := env.do(t, http.MethodPost, "/api/v1/auth/login", "",
		map[string]string{"username": "ada", "password": "hunter2"})
	if res.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: a federated account has no password here", res.Code)
	}
}

func TestConsoleOriginResolution(t *testing.T) {
	s := &server{allowedOrigins: []string{"https://console.example.com", "*"}}

	// The default is the first configured console, not the API's own address.
	got, err := s.resolveConsoleOrigin("", "https://kubemg.example.com")
	if err != nil || got != "https://console.example.com" {
		t.Fatalf("default origin = %q, %v", got, err)
	}
	// The public URL is a console too — a single-origin install serves both from
	// the same place.
	if got, err := s.resolveConsoleOrigin("https://kubemg.example.com/login", "https://kubemg.example.com"); err != nil || got != "https://kubemg.example.com" {
		t.Fatalf("public URL origin = %q, %v", got, err)
	}
	// A wildcard CORS configuration is not a licence to redirect anywhere.
	if _, err := s.resolveConsoleOrigin("https://evil.example", "https://kubemg.example.com"); err == nil {
		t.Fatal("a foreign origin must be refused even with a wildcard CORS setting")
	}
}

// testPublicURL is the public URL the shared test environment is built with;
// every generated callback and entity ID is rendered against it.
const testPublicURL = "https://kubemg.example.com"

func providerPath(id uint, suffix string) string {
	return "/api/v1/auth/sso/providers/" + itoa(id) + suffix
}
