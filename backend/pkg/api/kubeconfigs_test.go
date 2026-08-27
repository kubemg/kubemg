package api

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/credentials"
	"github.com/kubemg/kubemg/backend/pkg/db"
)

const kubeconfigsPath = "/api/v1/kubeconfigs"

func revokePath(id uint) string { return kubeconfigsPath + "/" + itoa(id) + "/revoke" }

// grantView gives a non-admin standing access, which is what makes them able to
// generate a kubeconfig at all.
func grantView(store *fakeStore, userID, clusterID uint) {
	if store.access[userID] == nil {
		store.access[userID] = map[uint]db.UserClusterAccess{}
	}
	store.access[userID][clusterID] = db.UserClusterAccess{
		UserID: userID, ClusterID: clusterID, K8sRole: db.K8sRoleView,
	}
}

// TestGeneratingAKubeconfigWritesTheRegister is the first half of the feature:
// there was no row at all before, and the register is what revocation needs to
// exist before it can mean anything.
func TestGeneratingAKubeconfigWritesTheRegister(t *testing.T) {
	auditor := &recordingAuditor{}
	env := newTestEnvWith(t, func(o *Options) { o.Auditor = auditor })
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	cluster := env.store.addAgentCluster("edge-us", db.EnvStaging, "kmg_token")

	rec := env.do(t, http.MethodPost, generatePath(cluster.ID), env.tokenFor(t, admin), map[string]any{
		"namespace": "payments",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("generate failed: %d (%s)", rec.Code, rec.Body.String())
	}

	if len(env.store.issuances) != 1 {
		t.Fatalf("expected one register row, got %d", len(env.store.issuances))
	}
	row := env.store.issuances[0]
	switch {
	case row.UserID != admin.ID || row.Username != "admin":
		t.Fatalf("register row does not name the holder: %+v", row)
	case row.ClusterID != cluster.ID || row.ClusterName != "edge-us":
		t.Fatalf("register row does not name the cluster: %+v", row)
	case row.ConnectionMode != db.ModeAgent:
		t.Fatalf("expected an agent-mode row, got %q", row.ConnectionMode)
	case row.Namespace != "payments" || row.K8sRole != db.K8sRoleClusterAdmin:
		t.Fatalf("register row does not carry the scope: %+v", row)
	case row.TokenID == "":
		t.Fatal("an agent-mode row must carry the credential's own token id")
	case row.ExpiresAt.IsZero():
		t.Fatal("a register row with no expiry cannot say when access ends")
	case row.IssuedBy != admin.ID:
		t.Fatalf("register row does not name who issued it: %+v", row)
	}

	// The audit half. Neither half answers "who was given production access"
	// alone: the row says what exists, the record says who handed it over.
	events := auditor.all()
	if len(events) != 1 || events[0].Verb != verbKubeconfigIssue {
		t.Fatalf("expected one %s record, got %+v", verbKubeconfigIssue, events)
	}
	if events[0].Username != "admin" || events[0].ImpersonatedUser != "admin" {
		t.Fatalf("the issuance record does not cross the identities: %+v", events[0])
	}
	if events[0].ClusterID != cluster.ID {
		t.Fatalf("the issuance record does not name the cluster: %+v", events[0])
	}
}

// A direct-mode kubeconfig is recorded too. It cannot be revoked from here, but
// "who holds access to production right now" is a question the mode does not
// change.
func TestDirectModeIsRegisteredWithItsServiceAccount(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	cluster := env.store.addCluster("prod-eu", db.EnvProd)

	rec := env.do(t, http.MethodPost, generatePath(cluster.ID), env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("generate failed: %d (%s)", rec.Code, rec.Body.String())
	}
	if len(env.store.issuances) != 1 {
		t.Fatalf("expected one register row, got %d", len(env.store.issuances))
	}
	row := env.store.issuances[0]
	if row.ConnectionMode != db.ModeDirect {
		t.Fatalf("expected a direct-mode row, got %q", row.ConnectionMode)
	}
	if row.ServiceAccount != "kubemg-admin" {
		t.Fatalf("a direct-mode row must name the account the token is bound to, got %q", row.ServiceAccount)
	}
	// It still gets an identity of its own, so every read of the table keys on
	// one column — but nothing the file carries mentions it.
	if row.TokenID == "" {
		t.Fatal("a register row must have an identity")
	}
	if row.RevocableHere() {
		t.Fatal("a direct-mode credential must not claim to be revocable here")
	}
}

// The read follows the audit trail's rule: a non-admin sees their own rows, and
// the query parameter can narrow that but never widen it.
func TestRegisterNarrowsANonAdminToTheirOwnRows(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	dana := env.store.addUser("dana", "pw", db.RoleUser)
	cluster := env.store.addAgentCluster("edge-us", db.EnvStaging, "kmg_token")
	grantView(env.store, dana.ID, cluster.ID)

	env.do(t, http.MethodPost, generatePath(cluster.ID), env.tokenFor(t, admin), nil)
	env.do(t, http.MethodPost, generatePath(cluster.ID), env.tokenFor(t, dana), nil)

	// Asking for somebody else's is not an error and not a refusal — it is
	// simply narrowed back to the caller's own, the way the audit list behaves.
	rec := env.do(t, http.MethodGet, kubeconfigsPath+"?user_id="+itoa(admin.ID), env.tokenFor(t, dana), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected the register to be readable, got %d (%s)", rec.Code, rec.Body.String())
	}
	body := decode[struct {
		Kubeconfigs []kubeconfigResponse `json:"kubeconfigs"`
	}](t, rec)
	if len(body.Kubeconfigs) != 1 || body.Kubeconfigs[0].Username != "dana" {
		t.Fatalf("user_id widened a non-admin's view: %+v", body.Kubeconfigs)
	}

	// An admin sees the whole register.
	rec = env.do(t, http.MethodGet, kubeconfigsPath, env.tokenFor(t, admin), nil)
	body = decode[struct {
		Kubeconfigs []kubeconfigResponse `json:"kubeconfigs"`
	}](t, rec)
	if len(body.Kubeconfigs) != 2 {
		t.Fatalf("expected an admin to see both rows, got %d", len(body.Kubeconfigs))
	}
}

// Revoking one credential writes the row and republishes the snapshot the
// gateway reads, in that order, before the caller is told it worked.
func TestRevokingOneCredentialPublishesIt(t *testing.T) {
	auditor := &recordingAuditor{}
	env := newTestEnvWith(t, func(o *Options) { o.Auditor = auditor })
	dana := env.store.addUser("dana", "pw", db.RoleUser)
	cluster := env.store.addAgentCluster("edge-us", db.EnvStaging, "kmg_token")
	grantView(env.store, dana.ID, cluster.ID)

	env.do(t, http.MethodPost, generatePath(cluster.ID), env.tokenFor(t, dana), nil)
	row := env.store.issuances[0]
	if env.issued.Revoked(row.TokenID) {
		t.Fatal("a freshly issued credential was already revoked")
	}

	// Revoking your own is never administrative.
	rec := env.do(t, http.MethodPost, revokePath(row.ID), env.tokenFor(t, dana), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected a holder to revoke their own credential, got %d (%s)", rec.Code, rec.Body.String())
	}
	body := decode[kubeconfigResponse](t, rec)
	if body.Status != "revoked" || body.RevokedAt == nil {
		t.Fatalf("the row does not read as revoked: %+v", body)
	}
	if !env.issued.Revoked(row.TokenID) {
		t.Fatal("the revoked credential was not published to the gateway")
	}
	// The row survives: "what existed and when did it stop" is the question.
	if len(env.store.issuances) != 1 {
		t.Fatalf("revoking deleted the row: %d rows left", len(env.store.issuances))
	}

	verbs := []string{}
	for _, event := range auditor.all() {
		verbs = append(verbs, event.Verb)
	}
	if len(verbs) != 2 || verbs[1] != verbKubeconfigRevoke {
		t.Fatalf("expected an issue then a revoke record, got %v", verbs)
	}
}

func TestRevokingSomebodyElsesNeedsAnAdmin(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	dana := env.store.addUser("dana", "pw", db.RoleUser)
	eve := env.store.addUser("eve", "pw", db.RoleUser)
	cluster := env.store.addAgentCluster("edge-us", db.EnvStaging, "kmg_token")
	grantView(env.store, dana.ID, cluster.ID)

	env.do(t, http.MethodPost, generatePath(cluster.ID), env.tokenFor(t, dana), nil)
	row := env.store.issuances[0]

	// Whose credential it is, is not something the address may disclose.
	rec := env.do(t, http.MethodPost, revokePath(row.ID), env.tokenFor(t, eve), nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected %d for somebody else's credential, got %d (%s)",
			http.StatusNotFound, rec.Code, rec.Body.String())
	}
	if env.issued.Revoked(row.TokenID) {
		t.Fatal("a refused revoke still published a revocation")
	}

	if rec := env.do(t, http.MethodPost, revokePath(row.ID), env.tokenFor(t, admin), nil); rec.Code != http.StatusOK {
		t.Fatalf("expected an admin to revoke it, got %d (%s)", rec.Code, rec.Body.String())
	}
}

// The direct-mode truth. A button that reported success while the token kept
// working would leave an administrator believing an incident was closed.
func TestRevokingADirectModeCredentialIsRefusedAndExplained(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	cluster := env.store.addCluster("prod-eu", db.EnvProd)

	env.do(t, http.MethodPost, generatePath(cluster.ID), env.tokenFor(t, admin), nil)
	row := env.store.issuances[0]

	rec := env.do(t, http.MethodPost, revokePath(row.ID), env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected %d for a direct-mode credential, got %d (%s)",
			http.StatusConflict, rec.Code, rec.Body.String())
	}
	message := rec.Body.String()
	if !contains(message, "kubemg-admin") || !contains(message, "ServiceAccount") {
		t.Fatalf("the refusal does not name the one lever that exists: %s", message)
	}
	if env.store.issuances[0].Revoked() {
		t.Fatal("a refused revoke still stamped the row")
	}
}

// The blanket action: one call, and an honest account of what it reached.
func TestRevokingEverythingStatesWhatItCouldNotReach(t *testing.T) {
	env := newTestEnv(t)
	dana := env.store.addUser("dana", "pw", db.RoleUser)
	agent := env.store.addAgentCluster("edge-us", db.EnvStaging, "kmg_token")
	direct := env.store.addCluster("prod-eu", db.EnvProd)
	grantView(env.store, dana.ID, agent.ID)
	grantView(env.store, dana.ID, direct.ID)

	env.do(t, http.MethodPost, generatePath(agent.ID), env.tokenFor(t, dana), nil)
	env.do(t, http.MethodPost, generatePath(direct.ID), env.tokenFor(t, dana), nil)

	rec := env.do(t, http.MethodPost, kubeconfigsPath+"/revoke-all", env.tokenFor(t, dana), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected a holder to revoke their own credentials, got %d (%s)", rec.Code, rec.Body.String())
	}
	body := decode[revokeAllKubeconfigsResponse](t, rec)
	if body.Revoked != 1 || body.StillValid != 1 {
		t.Fatalf("expected one stopped and one still valid, got %+v", body)
	}
	if len(body.Clusters) != 1 || body.Clusters[0] != "prod-eu" {
		t.Fatalf("the response does not name the cluster it could not reach: %+v", body)
	}
	if body.Explanation == "" {
		t.Fatal("a partial revoke that says nothing is worse than no button")
	}

	// The agent-mode one is genuinely stopped.
	for _, row := range env.store.issuances {
		if row.ConnectionMode == db.ModeAgent && !env.issued.Revoked(row.TokenID) {
			t.Fatal("the blanket revoke did not publish the agent-mode credential")
		}
	}
}

func TestRevokingEverythingForSomebodyElseNeedsAnAdmin(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	dana := env.store.addUser("dana", "pw", db.RoleUser)
	eve := env.store.addUser("eve", "pw", db.RoleUser)

	rec := env.do(t, http.MethodPost, kubeconfigsPath+"/revoke-all", env.tokenFor(t, eve),
		map[string]any{"user_id": dana.ID})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected %d, got %d (%s)", http.StatusForbidden, rec.Code, rec.Body.String())
	}

	rec = env.do(t, http.MethodPost, kubeconfigsPath+"/revoke-all", env.tokenFor(t, admin),
		map[string]any{"user_id": dana.ID})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected an admin to revoke another account's credentials, got %d (%s)",
			rec.Code, rec.Body.String())
	}
}

// An expired credential is not something to revoke, and a revoked one is not
// republished twice.
func TestBlanketRevokeSkipsWhatHasAlreadyStopped(t *testing.T) {
	env := newTestEnv(t)
	dana := env.store.addUser("dana", "pw", db.RoleUser)
	cluster := env.store.addAgentCluster("edge-us", db.EnvStaging, "kmg_token")
	grantView(env.store, dana.ID, cluster.ID)

	env.do(t, http.MethodPost, generatePath(cluster.ID), env.tokenFor(t, dana), nil)
	// Age the row past its own window.
	env.store.issuances[0].ExpiresAt = time.Now().UTC().Add(-time.Hour)

	rec := env.do(t, http.MethodPost, kubeconfigsPath+"/revoke-all", env.tokenFor(t, dana), nil)
	body := decode[revokeAllKubeconfigsResponse](t, rec)
	if body.Revoked != 0 || body.StillValid != 0 {
		t.Fatalf("an expired credential was counted as revoked: %+v", body)
	}
	if env.store.issuances[0].Revoked() {
		t.Fatal("an expired credential was stamped as revoked")
	}
}

// The fail-open rule, from the HTTP layer's side: a register that will not read
// leaves the previously published set alone rather than emptying it or refusing
// everything.
func TestAnUnreadableRegisterLeavesThePublishedSetAlone(t *testing.T) {
	env := newTestEnv(t)
	env.issued.Store(credentials.NewSnapshot([]string{"still-revoked"}))

	env.store.revokedIDsErr = errors.New("database unavailable")
	s := &server{store: env.store, credentials: env.issued}
	s.publishRevokedCredentials(t.Context())

	if !env.issued.Revoked("still-revoked") {
		t.Fatal("a failed refresh dropped a revocation that was already published")
	}
	if env.issued.Revoked("never-revoked") {
		t.Fatal("a failed refresh revoked something nobody withdrew")
	}
}

// The register's expiry filter: an expired credential is refused by its own
// signature long before the snapshot is consulted, so carrying it forever would
// grow the set to answer a question nothing asks.
func TestPublishedSetHoldsOnlyLiveAgentModeRevocations(t *testing.T) {
	env := newTestEnv(t)
	now := time.Now().UTC()
	revoked := now.Add(-time.Minute)
	env.store.issuances = []*db.KubeconfigIssuance{
		{TokenID: "live", ConnectionMode: db.ModeAgent, ExpiresAt: now.Add(time.Hour), RevokedAt: &revoked},
		{TokenID: "past", ConnectionMode: db.ModeAgent, ExpiresAt: now.Add(-time.Hour), RevokedAt: &revoked},
		{TokenID: "direct", ConnectionMode: db.ModeDirect, ExpiresAt: now.Add(time.Hour), RevokedAt: &revoked},
		{TokenID: "open", ConnectionMode: db.ModeAgent, ExpiresAt: now.Add(time.Hour)},
	}

	s := &server{store: env.store, credentials: env.issued}
	s.publishRevokedCredentials(t.Context())

	if !env.issued.Revoked("live") {
		t.Fatal("a live revocation is missing from the published set")
	}
	for _, id := range []string{"past", "direct", "open"} {
		if env.issued.Revoked(id) {
			t.Fatalf("%q should not be in the published set", id)
		}
	}
}
