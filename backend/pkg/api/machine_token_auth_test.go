package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/auth"
	"github.com/kubemg/kubemg/backend/pkg/db"
)

// seedMachineToken puts a live credential in the store and hands back the
// secret, the way an issuance would.
func seedMachineToken(t *testing.T, env *testEnv, user *db.User, clusterID uint) (string, *db.MachineToken) {
	t.Helper()
	secret, hash, hint, err := auth.NewMachineToken()
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}
	token := &db.MachineToken{
		UserID: user.ID, ClusterID: clusterID, Name: "release", TokenHash: hash, Hint: hint,
	}
	if err := env.store.CreateMachineToken(t.Context(), token); err != nil {
		t.Fatalf("store token: %v", err)
	}
	return secret, token
}

// The whole point of a stored credential: it is confined to one cluster's proxy
// route, exactly as an issued kubeconfig is. A token in a CI secret store must
// not also open the console's API.
func TestMachineTokenIsConfinedToItsClustersProxy(t *testing.T) {
	env := newTestEnv(t)
	machine := env.store.addMachineAccount("jenkins")
	cluster := env.store.addAgentCluster("prod", db.EnvProd, "kmg_agent")
	other := env.store.addAgentCluster("staging", db.EnvStaging, "kmg_agent2")
	env.store.grant(machine.ID, cluster.ID, db.K8sRoleView, nil)
	secret, _ := seedMachineToken(t, env, machine, cluster.ID)

	for _, path := range []string{
		"/api/v1/auth/me",
		"/api/v1/clusters",
		"/api/v1/machine-accounts",
		"/api/v1/clusters/" + itoa(other.ID) + "/proxy/api/v1/namespaces",
	} {
		rec := env.do(t, http.MethodGet, path, secret, nil)
		if rec.Code != http.StatusForbidden && rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: a machine token must not reach this route, got %d (%s)",
				path, rec.Code, rec.Body.String())
		}
	}

	// And on its own cluster's proxy it authenticates: with no agent attached the
	// call fails at the tunnel, which is past the credential check.
	rec := env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/proxy/api/v1/namespaces", secret, nil)
	if rec.Code == http.StatusUnauthorized || rec.Code == http.StatusForbidden {
		t.Fatalf("expected the credential to be accepted on its own proxy, got %d (%s)",
			rec.Code, rec.Body.String())
	}
}

func TestMachineTokenStopsWorkingWhenRevokedExpiredOrDisabled(t *testing.T) {
	proxyPath := func(id uint) string {
		return "/api/v1/clusters/" + itoa(id) + "/proxy/api/v1/namespaces"
	}

	t.Run("revoked", func(t *testing.T) {
		env := newTestEnv(t)
		machine := env.store.addMachineAccount("jenkins")
		cluster := env.store.addAgentCluster("prod", db.EnvProd, "kmg_agent")
		env.store.grant(machine.ID, cluster.ID, db.K8sRoleView, nil)
		secret, token := seedMachineToken(t, env, machine, cluster.ID)

		if _, err := env.store.RevokeMachineToken(t.Context(), token.ID, time.Now().UTC()); err != nil {
			t.Fatalf("revoke: %v", err)
		}
		rec := env.do(t, http.MethodGet, proxyPath(cluster.ID), secret, nil)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("a revoked credential must stop at the next call, got %d (%s)",
				rec.Code, rec.Body.String())
		}
	})

	t.Run("expired", func(t *testing.T) {
		env := newTestEnv(t)
		machine := env.store.addMachineAccount("jenkins")
		cluster := env.store.addAgentCluster("prod", db.EnvProd, "kmg_agent")
		env.store.grant(machine.ID, cluster.ID, db.K8sRoleView, nil)
		secret, token := seedMachineToken(t, env, machine, cluster.ID)

		past := time.Now().UTC().Add(-time.Minute)
		for _, stored := range env.store.machineTokens {
			if stored.ID == token.ID {
				stored.ExpiresAt = &past
			}
		}
		rec := env.do(t, http.MethodGet, proxyPath(cluster.ID), secret, nil)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("an expired credential must be refused, got %d", rec.Code)
		}
	})

	t.Run("account disabled", func(t *testing.T) {
		env := newTestEnv(t)
		machine := env.store.addMachineAccount("jenkins")
		cluster := env.store.addAgentCluster("prod", db.EnvProd, "kmg_agent")
		env.store.grant(machine.ID, cluster.ID, db.K8sRoleView, nil)
		secret, _ := seedMachineToken(t, env, machine, cluster.ID)

		if _, err := env.store.SetUserActive(t.Context(), machine.ID, false); err != nil {
			t.Fatalf("disable: %v", err)
		}
		rec := env.do(t, http.MethodGet, proxyPath(cluster.ID), secret, nil)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("disabling the account must stop every credential it holds, got %d", rec.Code)
		}
	})
}

// A token whose account is a person's is refused: this is the one path where a
// row deciding what an account is would otherwise go unread.
func TestMachineTokenIsRefusedForAHumanAccount(t *testing.T) {
	env := newTestEnv(t)
	human := env.store.addUser("devops", "pw", db.RoleUser)
	cluster := env.store.addAgentCluster("prod", db.EnvProd, "kmg_agent")
	env.store.grant(human.ID, cluster.ID, db.K8sRoleView, nil)
	secret, _ := seedMachineToken(t, env, human, cluster.ID)

	rec := env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/proxy/api/v1/namespaces", secret, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected %d, got %d (%s)", http.StatusUnauthorized, rec.Code, rec.Body.String())
	}
}

func TestUnknownMachineTokenIsRefusedWithoutFallingThroughToTheJWTParser(t *testing.T) {
	env := newTestEnv(t)
	cluster := env.store.addAgentCluster("prod", db.EnvProd, "kmg_agent")

	rec := env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/proxy/api/v1/namespaces",
		auth.MachineTokenPrefix+"not-a-real-secret", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected %d, got %d (%s)", http.StatusUnauthorized, rec.Code, rec.Body.String())
	}
}

// The prefix is a support-desk fact as much as a parsing one: an agent
// registration token and a machine credential are revoked in different places.
func TestMachineTokenPrefixIsDistinctFromTheAgentTokens(t *testing.T) {
	secret, hash, hint, err := auth.NewMachineToken()
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if !auth.IsMachineToken(secret) {
		t.Fatalf("expected %q to carry the machine prefix", secret)
	}
	if auth.MachineTokenPrefix == "kmg_" {
		t.Fatal("the machine prefix must not be the agent registration token's")
	}
	if hash == secret || len(hash) != 64 {
		t.Fatalf("expected a sha-256 hex digest, got %q", hash)
	}
	if len(hint) >= len(secret) {
		t.Fatalf("a hint must be a fragment, got %q", hint)
	}
	if auth.HashMachineToken(secret) != hash {
		t.Fatal("hashing must be deterministic")
	}
}
