package api

import (
	"net/http"
	"testing"
)

// Quantities arrive in whichever form wrote them: metrics-server reports CPU in
// nanocores, a pod spec in millicores, and a node's allocatable memory in
// binary SI. Getting any of these wrong shows a plausible but false bar, which
// is worse than showing none.
func TestParseCPUMillicores(t *testing.T) {
	cases := map[string]int64{
		"":            0,
		"0":           0,
		"250m":        250,
		"1":           1000,
		"0.5":         500,
		"2500m":       2500,
		// metrics-server's own unit. Anything finer than a millicore rounds
		// *up*, which is apimachinery's rule and the conservative one for a
		// utilisation reading.
		"123456789n":  124,
		"1500u":       2,
		"not-a-value": 0,
	}

	for raw, want := range cases {
		if got := parseCPUMillicores(raw); got != want {
			t.Errorf("parseCPUMillicores(%q) = %d, want %d", raw, got, want)
		}
	}
}

func TestParseMemoryBytes(t *testing.T) {
	cases := map[string]int64{
		"":            0,
		"128974848":   128974848,
		"1Ki":         1024,
		"1Mi":         1 << 20,
		"1Gi":         1 << 30,
		"1G":          1_000_000_000, // decimal SI is 7% smaller than Gi
		"129e6":       129_000_000,
		"not-a-value": 0,
	}

	for raw, want := range cases {
		if got := parseMemoryBytes(raw); got != want {
			t.Errorf("parseMemoryBytes(%q) = %d, want %d", raw, got, want)
		}
	}
}

func TestPercentHandlesAnUnknownCapacity(t *testing.T) {
	if got := percent(500, 0); got != 0 {
		t.Fatalf("an unknown capacity must read 0, not divide by zero: got %v", got)
	}
	if got := percent(500, 2000); got != 25 {
		t.Fatalf("percent(500, 2000) = %v, want 25", got)
	}
	if got := percent(1, 3); got != 33.3 {
		t.Fatalf("percent(1, 3) = %v, want 33.3", got)
	}
}

// The metrics routes are resource reads like any other, so they inherit the
// same guard chain: agent-only, grant-bound, and cluster-wide reads refused to
// a namespace-scoped grant.
func TestMetricsRoutesShareTheResourceGuards(t *testing.T) {
	t.Run("direct clusters have no live state", func(t *testing.T) {
		env := newTestEnv(t)
		admin := env.store.addUser("admin", "secret123", "admin")
		cluster := env.store.addCluster("legacy", "dev")
		token := env.tokenFor(t, admin)

		for _, path := range []string{"metrics/nodes", "metrics/pods?namespace=default"} {
			rec := env.do(t, http.MethodGet,
				"/api/v1/clusters/"+itoa(cluster.ID)+"/"+path, token, nil)
			if rec.Code != http.StatusConflict {
				t.Fatalf("%s: expected %d for a direct-mode cluster, got %d (%s)",
					path, http.StatusConflict, rec.Code, rec.Body.String())
			}
		}
	})

	t.Run("node metrics are cluster-wide", func(t *testing.T) {
		env := newTestEnv(t)
		user := env.store.addUser("scoped", "secret123", "user")
		cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
		env.store.grant(user.ID, cluster.ID, "view", []string{"team-a"})
		token := env.tokenFor(t, user)

		rec := env.do(t, http.MethodGet,
			"/api/v1/clusters/"+itoa(cluster.ID)+"/metrics/nodes", token, nil)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("expected %d for a scoped grant, got %d (%s)",
				http.StatusForbidden, rec.Code, rec.Body.String())
		}
	})

	t.Run("pod metrics stay inside the grant", func(t *testing.T) {
		env := newTestEnv(t)
		user := env.store.addUser("scoped", "secret123", "user")
		cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
		env.store.grant(user.ID, cluster.ID, "view", []string{"team-a"})
		token := env.tokenFor(t, user)

		rec := env.do(t, http.MethodGet,
			"/api/v1/clusters/"+itoa(cluster.ID)+"/metrics/pods?namespace=team-b", token, nil)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("expected %d for a namespace outside the grant, got %d (%s)",
				http.StatusForbidden, rec.Code, rec.Body.String())
		}
	})

	t.Run("authentication is required", func(t *testing.T) {
		env := newTestEnv(t)
		cluster := env.store.addAgentCluster("edge", "dev", "agent-token")

		rec := env.do(t, http.MethodGet,
			"/api/v1/clusters/"+itoa(cluster.ID)+"/metrics/nodes", "", nil)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected %d without a token, got %d", http.StatusUnauthorized, rec.Code)
		}
	})
}
