package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/auth"
	"github.com/kubemg/kubemg/backend/pkg/bastion"
	"github.com/kubemg/kubemg/backend/pkg/db"
)

const (
	cliDevicePath     = "/api/v1/auth/cli/device"
	cliTokenPath      = "/api/v1/auth/cli/token"
	cliCredentialPath = "/api/v1/auth/cli/credential"
	cliClustersPath   = "/api/v1/auth/cli/clusters"
	cliSessionsPath   = "/api/v1/cli-sessions"
)

// fromAddress is env.do with the caller's address chosen, for the rules that
// are about where a request came from.
func fromAddress(t *testing.T, env *testEnv, method, path, token, addr string, body any) *httptest.ResponseRecorder {
	t.Helper()
	payload, _ := json.Marshal(body)
	req := httptest.NewRequest(method, path, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.RemoteAddr = addr + ":40000"
	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)
	return rec
}

func startCLILogin(t *testing.T, env *testEnv) deviceStartResponse {
	t.Helper()
	rec := env.do(t, http.MethodPost, cliDevicePath, "", map[string]string{
		"client_name": "kubemg", "client_host": "laptop-17", "client_os": "darwin/arm64", "client_version": "0.16.0",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
	}
	return decode[deviceStartResponse](t, rec)
}

func pollCLI(t *testing.T, env *testEnv, deviceCode string) *httptest.ResponseRecorder {
	t.Helper()
	return env.do(t, http.MethodPost, cliTokenPath, "", map[string]string{"device_code": deviceCode})
}

func deviceErrorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	return decode[map[string]string](t, rec)["error"]
}

func approvePath(code string) string { return cliDevicePath + "/" + code + "/approve" }

// mfaTokenFor is a session that passed a second factor.
func mfaTokenFor(t *testing.T, env *testEnv, user *db.User) string {
	t.Helper()
	token, _, err := env.jwt.GenerateWithMFA(user.ID, user.Username, user.Role)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

// signInCLI walks the whole device flow for a user and returns the session.
func signInCLI(t *testing.T, env *testEnv, user *db.User) cliSessionResponse {
	t.Helper()
	start := startCLILogin(t, env)
	if rec := env.do(t, http.MethodPost, approvePath(start.UserCode), mfaTokenFor(t, env, user), nil); rec.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
	}
	rec := pollCLI(t, env, start.DeviceCode)
	if rec.Code != http.StatusOK {
		t.Fatalf("token: %d %s", rec.Code, rec.Body.String())
	}
	return decode[cliSessionResponse](t, rec)
}

func cliCredential(t *testing.T, env *testEnv, refresh string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	return env.do(t, http.MethodPost, cliCredentialPath, refresh, body)
}

func TestTheDeviceFlowEndsInExactlyOneSession(t *testing.T) {
	auditor := &recordingAuditor{}
	env := newTestEnvWith(t, func(o *Options) { o.Auditor = auditor })
	dana := env.store.addUser("dana", "pw", db.RoleUser)

	start := startCLILogin(t, env)
	if !strings.HasPrefix(start.DeviceCode, cliDevicePrefix) || normalizeUserCode(start.UserCode) != start.UserCode {
		t.Fatalf("codes: %+v", start)
	}
	if start.VerificationURIComplete != "https://kubemg.example.com/cli/activate?code="+start.UserCode {
		t.Fatalf("verification uri: %s", start.VerificationURIComplete)
	}

	if code := deviceErrorCode(t, pollCLI(t, env, start.DeviceCode)); code != "authorization_pending" {
		t.Fatalf("first poll: %s", code)
	}
	// Asking again at once is asking faster than the interval.
	if code := deviceErrorCode(t, pollCLI(t, env, start.DeviceCode)); code != "slow_down" {
		t.Fatalf("an immediate second poll: %s", code)
	}

	if rec := env.do(t, http.MethodPost, approvePath(start.UserCode), mfaTokenFor(t, env, dana), nil); rec.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
	}
	rec := pollCLI(t, env, start.DeviceCode)
	if rec.Code != http.StatusOK {
		t.Fatalf("token: %d %s", rec.Code, rec.Body.String())
	}
	session := decode[cliSessionResponse](t, rec)
	if !strings.HasPrefix(session.RefreshToken, auth.CLIRefreshPrefix) || session.User.Username != "dana" {
		t.Fatalf("session: %+v", session)
	}
	stored, err := env.store.CLISessionByID(t.Context(), session.SessionID)
	if err != nil || stored.ClientHost != "laptop-17" || stored.RefreshHash == session.RefreshToken {
		t.Fatalf("stored session: %+v %v", stored, err)
	}

	// The request is spent: a second poll gets nothing.
	if code := deviceErrorCode(t, pollCLI(t, env, start.DeviceCode)); code != "expired_token" {
		t.Fatalf("a spent request polled again: %s", code)
	}
	if !slices.ContainsFunc(auditor.all(), func(e bastion.Event) bool {
		return e.Verb == verbCLILogin && e.Username == "dana" && strings.Contains(e.Path, "host=laptop-17")
	}) {
		t.Fatalf("the approval is not in the trail: %+v", auditor.all())
	}
}

func TestADeniedRequestIsAnsweredAsDenied(t *testing.T) {
	env := newTestEnv(t)
	dana := env.store.addUser("dana", "pw", db.RoleUser)
	start := startCLILogin(t, env)
	if rec := env.do(t, http.MethodPost, cliDevicePath+"/"+start.UserCode+"/deny", env.tokenFor(t, dana), nil); rec.Code != http.StatusOK {
		t.Fatalf("deny: %d", rec.Code)
	}
	if code := deviceErrorCode(t, pollCLI(t, env, start.DeviceCode)); code != "access_denied" {
		t.Fatalf("poll after deny: %s", code)
	}
	if rec := env.do(t, http.MethodPost, approvePath(start.UserCode), env.tokenFor(t, dana), nil); rec.Code != http.StatusNotFound {
		t.Fatalf("approving a spent request: %d", rec.Code)
	}
}

func TestARequestIsAnsweredOnce(t *testing.T) {
	env := newTestEnv(t)
	dana := env.store.addUser("dana", "pw", db.RoleUser)
	start := startCLILogin(t, env)
	env.do(t, http.MethodPost, approvePath(start.UserCode), env.tokenFor(t, dana), nil)
	if rec := env.do(t, http.MethodPost, approvePath(start.UserCode), env.tokenFor(t, dana), nil); rec.Code != http.StatusConflict {
		t.Fatalf("a second approval: %d", rec.Code)
	}
}

func TestTheApprovalPageSaysWhereTheRequestCameFrom(t *testing.T) {
	env := newTestEnv(t)
	dana := env.store.addUser("dana", "pw", db.RoleUser)
	rec := fromAddress(t, env, http.MethodPost, cliDevicePath, "", "203.0.113.9", map[string]string{"client_host": "build\x07box"})
	start := decode[deviceStartResponse](t, rec)

	// Typed the way people type it.
	lower := strings.ToLower(strings.ReplaceAll(start.UserCode, "-", " "))
	rec = fromAddress(t, env, http.MethodGet, cliDevicePath+"/"+strings.ReplaceAll(lower, " ", "%20"), env.tokenFor(t, dana), "198.51.100.4", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("describe: %d %s", rec.Code, rec.Body.String())
	}
	view := decode[deviceRequestResponse](t, rec)
	if view.SourceIP != "203.0.113.9" || view.SameAddress || view.ClientHost != "buildbox" {
		t.Fatalf("view: %+v", view)
	}
	if view.SessionHours != defaultCLISessionHours {
		t.Fatalf("session hours: %d", view.SessionHours)
	}
}

func TestOneAddressMayHoldOnlyAFewRequestsOpen(t *testing.T) {
	env := newTestEnv(t)
	for i := range cliPendingPerAddress {
		if rec := fromAddress(t, env, http.MethodPost, cliDevicePath, "", "203.0.113.9", map[string]string{}); rec.Code != http.StatusOK {
			t.Fatalf("request %d: %d", i, rec.Code)
		}
	}
	if rec := fromAddress(t, env, http.MethodPost, cliDevicePath, "", "203.0.113.9", map[string]string{}); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("past the bound: %d", rec.Code)
	}
	if rec := fromAddress(t, env, http.MethodPost, cliDevicePath, "", "203.0.113.10", map[string]string{}); rec.Code != http.StatusOK {
		t.Fatalf("another address: %d", rec.Code)
	}
}

// A session that never met the second factor must not mint CLI sessions for an
// account that holds one, or must hold one.
func TestApprovingNeedsASessionThatPassedTheSecondFactor(t *testing.T) {
	env := newTestEnv(t)
	dana := env.store.addUser("dana", "pw", db.RoleUser)
	giveFactor(t, env, dana)
	start := startCLILogin(t, env)

	if rec := env.do(t, http.MethodPost, approvePath(start.UserCode), env.tokenFor(t, dana), nil); rec.Code != http.StatusForbidden {
		t.Fatalf("an enrolled account approved from a password-only session: %d", rec.Code)
	}
	if rec := env.do(t, http.MethodPost, approvePath(start.UserCode), mfaTokenFor(t, env, dana), nil); rec.Code != http.StatusOK {
		t.Fatalf("approval from an MFA session: %d %s", rec.Code, rec.Body.String())
	}

	// Required and not enrolled is owed too.
	admin := env.store.addUser("root", "pw", db.RoleAdmin)
	setEnforcement(env, mfaEnforcementAdmins)
	other := startCLILogin(t, env)
	if rec := env.do(t, http.MethodPost, approvePath(other.UserCode), env.tokenFor(t, admin), nil); rec.Code != http.StatusForbidden {
		t.Fatalf("an admin who must hold a factor approved without one: %d", rec.Code)
	}

	// An OIDC account's factor is its provider's, which KubeMG cannot see.
	fed := env.store.addUser("fed", "pw", db.RoleUser)
	fed.AuthSource = db.ProtocolOIDC
	third := startCLILogin(t, env)
	if rec := env.do(t, http.MethodPost, approvePath(third.UserCode), env.tokenFor(t, fed), nil); rec.Code != http.StatusOK {
		t.Fatalf("an OIDC account's approval: %d", rec.Code)
	}
}

// The refresh token reaches the CLI routes and nothing else.
func TestTheCLITokenOpensOnlyTheCLIRoutes(t *testing.T) {
	env := newTestEnv(t)
	dana := env.store.addUser("dana", "pw", db.RoleUser)
	cluster := env.store.addAgentCluster("edge-us", db.EnvStaging, "kmg_token")
	grantView(env.store, dana.ID, cluster.ID)
	session := signInCLI(t, env, dana)

	for _, path := range []string{"/api/v1/auth/me", "/api/v1/clusters", "/api/v1/clusters/" + itoa(cluster.ID) + "/proxy/api/v1/pods"} {
		if rec := env.do(t, http.MethodGet, path, session.RefreshToken, nil); rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s with a CLI token: %d", path, rec.Code)
		}
	}
	// And a session token does not open the CLI routes.
	if rec := env.do(t, http.MethodGet, cliClustersPath, env.tokenFor(t, dana), nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("a console session on a CLI route: %d", rec.Code)
	}
	if rec := env.do(t, http.MethodGet, cliClustersPath, session.RefreshToken, nil); rec.Code != http.StatusOK {
		t.Fatalf("clusters: %d", rec.Code)
	}
}

func TestACredentialIsDecidedLikeAKubeconfig(t *testing.T) {
	env := newTestEnv(t)
	dana := env.store.addUser("dana", "pw", db.RoleUser)
	agent := env.store.addAgentCluster("edge-us", db.EnvStaging, "kmg_token")
	direct := env.store.addCluster("legacy", db.EnvDev)
	ungranted := env.store.addAgentCluster("prod", db.EnvProd, "kmg_other")
	env.store.grant(dana.ID, agent.ID, db.K8sRoleView, []string{"payments"})
	env.store.grant(dana.ID, direct.ID, db.K8sRoleView, nil)
	session := signInCLI(t, env, dana)

	rec := cliCredential(t, env, session.RefreshToken, map[string]any{"cluster_id": agent.ID})
	if rec.Code != http.StatusOK {
		t.Fatalf("credential: %d %s", rec.Code, rec.Body.String())
	}
	cred := decode[cliCredentialResponse](t, rec)
	claims := sessionClaims(t, env, cred.Token)
	if claims.Scope != auth.ScopeProxy || claims.ClusterID != agent.ID || claims.ID != session.SessionID {
		t.Fatalf("claims: %+v", claims)
	}
	if lifetime := time.Until(cred.ExpiresAt); lifetime > cliAccessTokenTTL || lifetime < cliAccessTokenTTL-time.Minute {
		t.Fatalf("lifetime %s", lifetime)
	}

	if rec := cliCredential(t, env, session.RefreshToken, map[string]any{"cluster_id": agent.ID, "namespace": "kube-system"}); rec.Code != http.StatusForbidden {
		t.Fatalf("a namespace outside the grant: %d", rec.Code)
	}
	if rec := cliCredential(t, env, session.RefreshToken, map[string]any{"cluster_id": ungranted.ID}); rec.Code != http.StatusForbidden {
		t.Fatalf("an ungranted cluster: %d", rec.Code)
	}
	if rec := cliCredential(t, env, session.RefreshToken, map[string]any{"cluster_id": direct.ID}); rec.Code != http.StatusConflict {
		t.Fatalf("a direct-mode cluster: %d", rec.Code)
	}
}

func TestACredentialNeverOutlivesItsSession(t *testing.T) {
	env := newTestEnv(t)
	dana := env.store.addUser("dana", "pw", db.RoleUser)
	cluster := env.store.addAgentCluster("edge-us", db.EnvStaging, "kmg_token")
	grantView(env.store, dana.ID, cluster.ID)
	session := signInCLI(t, env, dana)
	env.store.cli.sessions[session.SessionID].ExpiresAt = time.Now().Add(4 * time.Minute)

	cred := decode[cliCredentialResponse](t, cliCredential(t, env, session.RefreshToken, map[string]any{"cluster_id": cluster.ID}))
	if time.Until(cred.ExpiresAt) > 4*time.Minute {
		t.Fatalf("a credential outlives its session: %s", cred.ExpiresAt)
	}

	env.store.cli.sessions[session.SessionID].ExpiresAt = time.Now().Add(-time.Second)
	if rec := cliCredential(t, env, session.RefreshToken, map[string]any{"cluster_id": cluster.ID}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("an expired session minted: %d", rec.Code)
	}
}

// A revoked session is refused by the gateway on the next call made with any
// token it minted — not when that token expires.
func TestRevokingASessionWithdrawsEveryTokenItMinted(t *testing.T) {
	auditor := &recordingAuditor{}
	env := newTestEnvWith(t, func(o *Options) { o.Auditor = auditor })
	dana := env.store.addUser("dana", "pw", db.RoleUser)
	cluster := env.store.addAgentCluster("edge-us", db.EnvStaging, "kmg_token")
	grantView(env.store, dana.ID, cluster.ID)
	session := signInCLI(t, env, dana)
	cred := decode[cliCredentialResponse](t, cliCredential(t, env, session.RefreshToken, map[string]any{"cluster_id": cluster.ID}))
	proxyPath := "/api/v1/clusters/" + itoa(cluster.ID) + "/proxy/api/v1/namespaces"

	if rec := env.do(t, http.MethodGet, proxyPath, cred.Token, nil); rec.Code == http.StatusUnauthorized {
		t.Fatalf("a live session's token was refused: %s", rec.Body.String())
	}

	rec := env.do(t, http.MethodPost, cliSessionsPath+"/"+session.SessionID+"/revoke", env.tokenFor(t, dana), nil)
	if rec.Code != http.StatusOK || decode[cliSessionView](t, rec).Status != "revoked" {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body.String())
	}
	if !env.issued.Revoked(session.SessionID) {
		t.Fatal("the session was not published to the gateway")
	}
	if rec := env.do(t, http.MethodGet, proxyPath, cred.Token, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("a revoked session's token still reached the proxy: %d", rec.Code)
	}
	if rec := cliCredential(t, env, session.RefreshToken, map[string]any{"cluster_id": cluster.ID}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("a revoked session minted again: %d", rec.Code)
	}
	if !slices.ContainsFunc(auditor.all(), func(e bastion.Event) bool { return e.Verb == verbCLISessionRevoke }) {
		t.Fatal("the revoke is not in the trail")
	}
}

func TestLoggingOutEndsTheSession(t *testing.T) {
	env := newTestEnv(t)
	dana := env.store.addUser("dana", "pw", db.RoleUser)
	session := signInCLI(t, env, dana)
	if rec := env.do(t, http.MethodPost, "/api/v1/auth/cli/logout", session.RefreshToken, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("logout: %d", rec.Code)
	}
	if !env.issued.Revoked(session.SessionID) {
		t.Fatal("a logged-out session is not withdrawn")
	}
	if rec := env.do(t, http.MethodGet, cliClustersPath, session.RefreshToken, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("a logged-out session still answers: %d", rec.Code)
	}
}

func TestADisabledAccountsSessionIsRefused(t *testing.T) {
	env := newTestEnv(t)
	dana := env.store.addUser("dana", "pw", db.RoleUser)
	session := signInCLI(t, env, dana)
	dana.IsActive = false
	if rec := env.do(t, http.MethodGet, cliClustersPath, session.RefreshToken, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("a disabled account's session: %d", rec.Code)
	}
}

// A blanket revoke that left the CLI working would be a revoke that did not
// happen.
func TestBlanketRevokesEndCLISessionsToo(t *testing.T) {
	env := newTestEnv(t)
	dana := env.store.addUser("dana", "old-password", db.RoleUser)
	first := signInCLI(t, env, dana)

	rec := env.do(t, http.MethodPost, kubeconfigsPath+"/revoke-all", env.tokenFor(t, dana), nil)
	if rec.Code != http.StatusOK || decode[revokeAllKubeconfigsResponse](t, rec).CLISessionsRevoked != 1 {
		t.Fatalf("revoke-all: %d %s", rec.Code, rec.Body.String())
	}
	if !env.issued.Revoked(first.SessionID) {
		t.Fatal("revoke-all left the CLI session live")
	}

	second := signInCLI(t, env, dana)
	rec = env.do(t, http.MethodPost, passwordPath, env.tokenFor(t, dana), map[string]any{
		"current_password": "old-password", "new_password": "new-password", "revoke_kubeconfigs": true,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("password: %d %s", rec.Code, rec.Body.String())
	}
	body := decode[changePasswordResponse](t, rec)
	if body.Credentials == nil || body.Credentials.CLISessionsRevoked != 1 || !env.issued.Revoked(second.SessionID) {
		t.Fatalf("a password change with revoke left the CLI session: %+v", body.Credentials)
	}
}

func TestCLISessionsAreReadLikeTheRegister(t *testing.T) {
	auditor := &recordingAuditor{}
	env := newTestEnvWith(t, func(o *Options) { o.Auditor = auditor })
	dana := env.store.addUser("dana", "pw", db.RoleUser)
	eve := env.store.addUser("eve", "pw", db.RoleUser)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	danas := signInCLI(t, env, dana)
	signInCLI(t, env, eve)

	type page struct {
		Sessions []cliSessionView `json:"sessions"`
		Total    int64            `json:"total"`
	}
	own := decode[page](t, env.do(t, http.MethodGet, cliSessionsPath+"?user_id="+itoa(eve.ID), env.tokenFor(t, dana), nil))
	if own.Total != 1 || own.Sessions[0].Username != "dana" {
		t.Fatalf("user_id widened a non-admin's read: %+v", own)
	}
	if all := decode[page](t, env.do(t, http.MethodGet, cliSessionsPath, env.tokenFor(t, admin), nil)); all.Total != 2 {
		t.Fatalf("an admin reads everybody's: %+v", all)
	}
	if strings.Contains(env.do(t, http.MethodGet, cliSessionsPath, env.tokenFor(t, admin), nil).Body.String(), "refresh") {
		t.Fatal("the refresh hash left the server")
	}

	if rec := env.do(t, http.MethodPost, cliSessionsPath+"/"+danas.SessionID+"/revoke", env.tokenFor(t, eve), nil); rec.Code != http.StatusNotFound {
		t.Fatalf("somebody else's session: %d", rec.Code)
	}
	if rec := env.do(t, http.MethodPost, cliSessionsPath+"/"+danas.SessionID+"/revoke", env.tokenFor(t, admin), nil); rec.Code != http.StatusOK {
		t.Fatalf("an admin revoking: %d", rec.Code)
	}
	if !slices.ContainsFunc(auditor.all(), func(e bastion.Event) bool {
		return e.Verb == verbCLISessionRevoke && e.Username == "admin" && e.ImpersonatedUser == "dana"
	}) {
		t.Fatal("an admin's revoke does not cross the identities")
	}
}

func TestCLIOnlyWithholdsTheLongLivedFile(t *testing.T) {
	env := newTestEnv(t)
	dana := env.store.addUser("dana", "pw", db.RoleUser)
	agent := env.store.addAgentCluster("edge-us", db.EnvStaging, "kmg_token")
	direct := env.store.addCluster("legacy", db.EnvDev)
	grantView(env.store, dana.ID, agent.ID)
	grantView(env.store, dana.ID, direct.ID)
	session := signInCLI(t, env, dana)

	if rec := cliCredential(t, env, session.RefreshToken, map[string]any{"cluster_id": agent.ID, "static": true, "ttl_seconds": 3600}); rec.Code != http.StatusOK {
		t.Fatalf("a static kubeconfig while allowed: %d %s", rec.Code, rec.Body.String())
	}

	env.store.settings[db.SettingKubeconfigIssuance] = issuanceCLIOnly
	if rec := env.do(t, http.MethodPost, generatePath(agent.ID), env.tokenFor(t, dana), nil); rec.Code != http.StatusConflict {
		t.Fatalf("an agent-mode download under cli_only: %d", rec.Code)
	}
	if rec := cliCredential(t, env, session.RefreshToken, map[string]any{"cluster_id": agent.ID, "static": true}); rec.Code != http.StatusConflict {
		t.Fatalf("a static kubeconfig under cli_only: %d", rec.Code)
	}
	if rec := cliCredential(t, env, session.RefreshToken, map[string]any{"cluster_id": agent.ID}); rec.Code != http.StatusOK {
		t.Fatalf("the CLI's own credential under cli_only: %d", rec.Code)
	}
	// Direct mode has no CLI path, so cli_only does not touch it — said, not hidden.
	if rec := env.do(t, http.MethodPost, generatePath(direct.ID), env.tokenFor(t, dana), nil); rec.Code != http.StatusOK {
		t.Fatalf("a direct-mode download under cli_only: %d %s", rec.Code, rec.Body.String())
	}
	policy := decode[map[string]any](t, env.do(t, http.MethodGet, "/api/v1/kubeconfig/policy", env.tokenFor(t, dana), nil))
	if policy["issuance"] != issuanceCLIOnly {
		t.Fatalf("policy: %+v", policy)
	}
}

func TestASessionNeverOutlivesTheKubeconfigCeiling(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	token := env.tokenFor(t, admin)

	if rec := env.do(t, http.MethodPut, "/api/v1/settings", token, map[string]any{"cli_session_max_hours": 1000}); rec.Code != http.StatusBadRequest {
		t.Fatalf("past the bound: %d", rec.Code)
	}
	if rec := env.do(t, http.MethodPut, "/api/v1/settings", token, map[string]any{"kubeconfig_issuance": "sometimes"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("an unknown issuance: %d", rec.Code)
	}
	if rec := env.do(t, http.MethodPut, "/api/v1/settings", token, map[string]any{"cli_session_max_hours": 48}); rec.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
	}
	// The kubeconfig ceiling defaults to 24 hours, so 48 is clamped to it.
	session := signInCLI(t, env, admin)
	if time.Until(session.ExpiresAt) > 24*time.Hour {
		t.Fatalf("a CLI session outlives the kubeconfig ceiling: %s", session.ExpiresAt)
	}
}

func TestTheClusterListIsTheServersNames(t *testing.T) {
	env := newTestEnv(t)
	dana := env.store.addUser("dana", "pw", db.RoleUser)
	agent := env.store.addAgentCluster("edge-us", db.EnvStaging, "kmg_token")
	direct := env.store.addCluster("legacy", db.EnvDev)
	env.store.addAgentCluster("hidden", db.EnvProd, "kmg_x")
	env.store.grant(dana.ID, agent.ID, db.K8sRoleEdit, []string{"payments"})
	grantView(env.store, dana.ID, direct.ID)
	session := signInCLI(t, env, dana)

	var body struct {
		Clusters []cliCluster `json:"clusters"`
	}
	body = decode[struct {
		Clusters []cliCluster `json:"clusters"`
	}](t, env.do(t, http.MethodGet, cliClustersPath, session.RefreshToken, nil))
	if len(body.Clusters) != 2 {
		t.Fatalf("clusters: %+v", body.Clusters)
	}
	for _, cluster := range body.Clusters {
		switch cluster.Name {
		case "edge-us":
			if cluster.Server != "https://kubemg.example.com/api/v1/clusters/"+itoa(agent.ID)+"/proxy" ||
				cluster.ContextName != "dana@edge-us" || cluster.Role != db.K8sRoleEdit ||
				!slices.Equal(cluster.Namespaces, []string{"payments"}) {
				t.Fatalf("agent cluster: %+v", cluster)
			}
		case "legacy":
			if cluster.Server != "" || cluster.Note == "" {
				t.Fatalf("direct cluster: %+v", cluster)
			}
		default:
			t.Fatalf("an ungranted cluster was listed: %+v", cluster)
		}
	}
}

func TestUserCodesAreReadTheWayPeopleTypeThem(t *testing.T) {
	for raw, want := range map[string]string{
		"BCDF-GHJK":  "BCDF-GHJK",
		"bcdfghjk":   "BCDF-GHJK",
		" bcdf ghjk": "BCDF-GHJK",
		"BCDF-GHJ":   "",
		"AEIO-UAEI":  "",
	} {
		if got := normalizeUserCode(raw); got != want {
			t.Errorf("%q: got %q, want %q", raw, got, want)
		}
	}
	for range 50 {
		code, err := newUserCode()
		if err != nil || normalizeUserCode(code) != code {
			t.Fatalf("minted %q (%v)", code, err)
		}
	}
}
