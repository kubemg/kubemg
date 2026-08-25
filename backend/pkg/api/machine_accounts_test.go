package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/auth"
	"github.com/kubemg/kubemg/backend/pkg/db"
)

type machineAccountListResponse struct {
	MachineAccounts []machineAccountResponse `json:"machine_accounts"`
}

type machineTokenListResponse struct {
	Tokens []machineTokenResponse `json:"tokens"`
}

func TestMachineAccountSurfaceIsAdminOnly(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("devops", "pw", db.RoleUser)

	for _, route := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/machine-accounts"},
		{http.MethodPost, "/api/v1/machine-accounts"},
		{http.MethodGet, "/api/v1/machine-accounts/1/tokens"},
		{http.MethodPost, "/api/v1/machine-accounts/1/tokens"},
	} {
		rec := env.do(t, route.method, route.path, env.tokenFor(t, user), map[string]any{})
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s %s: expected %d, got %d", route.method, route.path,
				http.StatusForbidden, rec.Code)
		}
	}
}

func TestCreateMachineAccountHoldsNoPasswordAndNoAdminRole(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)

	rec := env.do(t, http.MethodPost, "/api/v1/machine-accounts", env.tokenFor(t, admin),
		map[string]any{"username": "jenkins-release", "email": "platform@example.com"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected %d, got %d (%s)", http.StatusCreated, rec.Code, rec.Body.String())
	}

	body := decode[machineAccountResponse](t, rec)
	if body.AccountType != db.AccountTypeMachine {
		t.Fatalf("expected a machine account, got %q", body.AccountType)
	}
	if body.Role != db.RoleUser || body.SystemRole != db.SystemRoleUser {
		t.Fatalf("a machine account must never be an administrator: %q/%q", body.Role, body.SystemRole)
	}

	stored, err := env.store.UserByUsername(t.Context(), "jenkins-release")
	if err != nil {
		t.Fatalf("account was not stored: %v", err)
	}
	if stored.PasswordHash != "" {
		t.Fatal("a machine account must hold no password hash at all")
	}
}

// A name that reaches the cluster as Impersonate-User has to be one a
// RoleBinding can name, so it is validated rather than trimmed.
func TestCreateMachineAccountRejectsAnUnusableName(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)

	for _, name := range []string{"a", "Jenkins Release", "jenkins/release", "-jenkins", "jenkins:release"} {
		rec := env.do(t, http.MethodPost, "/api/v1/machine-accounts", env.tokenFor(t, admin),
			map[string]any{"username": name})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%q: expected %d, got %d", name, http.StatusBadRequest, rec.Code)
		}
	}
}

// The two identity surfaces stay apart in both directions: a machine account is
// not a person, and the user routes apply a person's affordances.
func TestMachineAccountsAreNotOnTheUserSurface(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	machine := env.store.addMachineAccount("jenkins")

	rec := env.do(t, http.MethodGet, "/api/v1/users", env.tokenFor(t, admin), nil)
	body := decode[userListResponse](t, rec)
	for _, user := range body.Users {
		if user.Username == "jenkins" {
			t.Fatal("a machine account must not appear on the people surface")
		}
	}

	rec = env.do(t, http.MethodPut, "/api/v1/users/"+itoa(machine.ID), env.tokenFor(t, admin),
		map[string]any{"system_role": db.SystemRoleAdmin})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected %d, got %d (%s)", http.StatusNotFound, rec.Code, rec.Body.String())
	}
}

// A machine account has no password, so a login attempt against its name must
// answer exactly as an unknown username does — anything else enumerates them.
func TestMachineAccountCannotSignIn(t *testing.T) {
	env := newTestEnv(t)
	env.store.addMachineAccount("jenkins")

	rec := env.do(t, http.MethodPost, "/api/v1/auth/login", "",
		map[string]any{"username": "jenkins", "password": ""})
	if rec.Code != http.StatusBadRequest && rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected a refusal, got %d", rec.Code)
	}

	rec = env.do(t, http.MethodPost, "/api/v1/auth/login", "",
		map[string]any{"username": "jenkins", "password": "anything"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected %d, got %d (%s)", http.StatusUnauthorized, rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "machine") || strings.Contains(rec.Body.String(), "service") {
		t.Fatalf("the refusal must not say what kind of account this is: %s", rec.Body.String())
	}
}

// Deriving admin from a hand-edited row is the failure this guards: the
// credential outlives the request that made it.
func TestMachineAccountNormalizesAwayAdmin(t *testing.T) {
	user := db.User{AccountType: db.AccountTypeMachine, SystemRole: db.SystemRoleSuperAdmin}
	user.Normalize()
	if user.IsAdmin() {
		t.Fatalf("a machine account must never normalize to an administrator: %+v", user)
	}
}

func TestIssueMachineTokenRequiresAgentModeAndAGrant(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	machine := env.store.addMachineAccount("jenkins")
	direct := env.store.addCluster("legacy", db.EnvProd)
	agent := env.store.addAgentCluster("prod", db.EnvProd, "kmg_agent")

	// Direct mode mints on the cluster, where KubeMG cannot revoke what it
	// issued — refused rather than served.
	env.store.grant(machine.ID, direct.ID, db.K8sRoleView, nil)
	rec := env.do(t, http.MethodPost, "/api/v1/machine-accounts/"+itoa(machine.ID)+"/tokens",
		env.tokenFor(t, admin), map[string]any{"name": "release", "cluster_id": direct.ID})
	if rec.Code != http.StatusConflict {
		t.Fatalf("direct mode: expected %d, got %d (%s)", http.StatusConflict, rec.Code, rec.Body.String())
	}

	// No grant means a credential that authenticates and is then refused by the
	// cluster, which is a file nobody can debug.
	rec = env.do(t, http.MethodPost, "/api/v1/machine-accounts/"+itoa(machine.ID)+"/tokens",
		env.tokenFor(t, admin), map[string]any{"name": "release", "cluster_id": agent.ID})
	if rec.Code != http.StatusConflict {
		t.Fatalf("ungranted: expected %d, got %d (%s)", http.StatusConflict, rec.Code, rec.Body.String())
	}
}

func TestIssueMachineTokenReturnsTheSecretOnceAndStoresOnlyItsHash(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	machine := env.store.addMachineAccount("jenkins")
	cluster := env.store.addAgentCluster("prod", db.EnvProd, "kmg_agent")
	env.store.grant(machine.ID, cluster.ID, db.K8sRoleEdit, []string{"payments"})

	rec := env.do(t, http.MethodPost, "/api/v1/machine-accounts/"+itoa(machine.ID)+"/tokens",
		env.tokenFor(t, admin), map[string]any{
			"name": "release pipeline", "cluster_id": cluster.ID, "namespace": "payments",
		})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected %d, got %d (%s)", http.StatusCreated, rec.Code, rec.Body.String())
	}

	body := decode[issueMachineTokenResponse](t, rec)
	if !auth.IsMachineToken(body.Secret) {
		t.Fatalf("expected a kmgm_ credential, got %q", body.Secret)
	}
	if body.Token.Status != "active" || body.Token.ExpiresAt == nil {
		t.Fatalf("expected a bounded active token, got %+v", body.Token)
	}
	// The file has to carry the credential and point at this server's proxy.
	if !strings.Contains(body.Kubeconfig, body.Secret) {
		t.Fatal("the kubeconfig must carry the issued credential")
	}
	if !strings.Contains(body.Server, "/api/v1/clusters/"+itoa(cluster.ID)+"/proxy") {
		t.Fatalf("unexpected server address %q", body.Server)
	}

	stored, err := env.store.MachineTokenByHash(t.Context(), auth.HashMachineToken(body.Secret))
	if err != nil {
		t.Fatalf("token was not stored: %v", err)
	}
	if strings.Contains(stored.TokenHash, body.Secret) || stored.Hint == body.Secret {
		t.Fatal("the secret itself must never be stored")
	}

	// And the list never shows it again.
	rec = env.do(t, http.MethodGet, "/api/v1/machine-accounts/"+itoa(machine.ID)+"/tokens",
		env.tokenFor(t, admin), nil)
	if strings.Contains(rec.Body.String(), body.Secret) {
		t.Fatalf("a listed token must not carry its secret: %s", rec.Body.String())
	}
	listed := decode[machineTokenListResponse](t, rec)
	if len(listed.Tokens) != 1 || listed.Tokens[0].Hint != stored.Hint {
		t.Fatalf("expected the issued token back by its hint, got %+v", listed.Tokens)
	}
}

// A namespace outside the account's own grant is refused rather than written
// into the file as a default that answers 403.
func TestIssueMachineTokenEnforcesTheAccountsNamespaceScope(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	machine := env.store.addMachineAccount("jenkins")
	cluster := env.store.addAgentCluster("prod", db.EnvProd, "kmg_agent")
	env.store.grant(machine.ID, cluster.ID, db.K8sRoleEdit, []string{"payments"})

	rec := env.do(t, http.MethodPost, "/api/v1/machine-accounts/"+itoa(machine.ID)+"/tokens",
		env.tokenFor(t, admin), map[string]any{
			"name": "release", "cluster_id": cluster.ID, "namespace": "kube-system",
		})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected %d, got %d (%s)", http.StatusForbidden, rec.Code, rec.Body.String())
	}
}

// Forever has to be asked for, and asking for it and a window at once is a
// contradiction rather than a preference.
func TestMachineTokenExpiryIsEitherAWindowOrNone(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	machine := env.store.addMachineAccount("jenkins")
	cluster := env.store.addAgentCluster("prod", db.EnvProd, "kmg_agent")
	env.store.grant(machine.ID, cluster.ID, db.K8sRoleView, nil)
	path := "/api/v1/machine-accounts/" + itoa(machine.ID) + "/tokens"

	rec := env.do(t, http.MethodPost, path, env.tokenFor(t, admin), map[string]any{
		"name": "release", "cluster_id": cluster.ID, "never_expires": true, "ttl_seconds": 3600,
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d (%s)", http.StatusBadRequest, rec.Code, rec.Body.String())
	}

	rec = env.do(t, http.MethodPost, path, env.tokenFor(t, admin), map[string]any{
		"name": "release", "cluster_id": cluster.ID, "never_expires": true,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected %d, got %d (%s)", http.StatusCreated, rec.Code, rec.Body.String())
	}
	body := decode[issueMachineTokenResponse](t, rec)
	if body.Token.ExpiresAt != nil {
		t.Fatalf("expected no expiry, got %v", body.Token.ExpiresAt)
	}
	// A credential with no clock on it has to say so, because what replaces the
	// expiry as a control is somebody reading the row.
	if body.Warning == "" {
		t.Fatal("a never-expiring credential must be disclosed")
	}
}

func TestRevokedMachineTokenKeepsItsRow(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	machine := env.store.addMachineAccount("jenkins")
	cluster := env.store.addAgentCluster("prod", db.EnvProd, "kmg_agent")
	env.store.grant(machine.ID, cluster.ID, db.K8sRoleView, nil)

	rec := env.do(t, http.MethodPost, "/api/v1/machine-accounts/"+itoa(machine.ID)+"/tokens",
		env.tokenFor(t, admin), map[string]any{"name": "release", "cluster_id": cluster.ID})
	issued := decode[issueMachineTokenResponse](t, rec)

	rec = env.do(t, http.MethodDelete,
		"/api/v1/machine-accounts/"+itoa(machine.ID)+"/tokens/"+itoa(issued.Token.ID),
		env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	if decode[machineTokenResponse](t, rec).Status != "revoked" {
		t.Fatal("expected the token to read as revoked")
	}

	listed := decode[machineTokenListResponse](t, env.do(t, http.MethodGet,
		"/api/v1/machine-accounts/"+itoa(machine.ID)+"/tokens", env.tokenFor(t, admin), nil))
	if len(listed.Tokens) != 1 || listed.Tokens[0].RevokedAt == nil {
		t.Fatalf("a revoked credential must stay on the list: %+v", listed.Tokens)
	}
}

// Whose token it is must not be discoverable by address.
func TestRevokingAnotherAccountsTokenIsNotFound(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	owner := env.store.addMachineAccount("jenkins")
	other := env.store.addMachineAccount("argo")
	cluster := env.store.addAgentCluster("prod", db.EnvProd, "kmg_agent")
	env.store.grant(owner.ID, cluster.ID, db.K8sRoleView, nil)

	issued := decode[issueMachineTokenResponse](t, env.do(t, http.MethodPost,
		"/api/v1/machine-accounts/"+itoa(owner.ID)+"/tokens", env.tokenFor(t, admin),
		map[string]any{"name": "release", "cluster_id": cluster.ID}))

	rec := env.do(t, http.MethodDelete,
		"/api/v1/machine-accounts/"+itoa(other.ID)+"/tokens/"+itoa(issued.Token.ID),
		env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected %d, got %d (%s)", http.StatusNotFound, rec.Code, rec.Body.String())
	}
}

func TestMachineTokenRowReadsItsOwnState(t *testing.T) {
	now := time.Now().UTC()
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)

	if (db.MachineToken{}).Expired(now) {
		t.Fatal("a token with no expiry never expires")
	}
	if !(db.MachineToken{ExpiresAt: &past}).Expired(now) {
		t.Fatal("a past expiry must read as expired")
	}
	if (db.MachineToken{ExpiresAt: &future}).Usable(now) != true {
		t.Fatal("a live token must be usable")
	}
	if (db.MachineToken{ExpiresAt: &future, RevokedAt: &past}).Usable(now) {
		t.Fatal("a revoked token must never be usable, whatever its expiry says")
	}
}
