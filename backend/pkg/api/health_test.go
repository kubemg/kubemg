package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/kubemg/kubemg/backend/pkg/db"
	"github.com/kubemg/kubemg/backend/pkg/k8s"
)

func checkPath(id uint) string { return "/api/v1/clusters/" + itoa(id) + "/check" }

func TestCheckClusterRecordsHealthy(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	cluster := env.store.addCluster("prod-eu", db.EnvProd)

	rec := env.do(t, http.MethodPost, checkPath(cluster.ID), env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}

	body := decode[clusterResponse](t, rec)
	if body.Status != db.StatusHealthy {
		t.Fatalf("expected status %q, got %q", db.StatusHealthy, body.Status)
	}
	if body.KubernetesVersion != "v1.31.4" {
		t.Fatalf("expected the reported version, got %q", body.KubernetesVersion)
	}
	if body.LastCheckedAt == nil || body.LastCheckedAt.IsZero() {
		t.Fatal("expected last_checked_at to be recorded")
	}
	if body.StatusMessage != "" {
		t.Fatalf("a healthy cluster must not carry a message, got %q", body.StatusMessage)
	}

	if stored := env.store.clusters[cluster.ID]; stored.Status != db.StatusHealthy {
		t.Fatalf("health was not persisted, stored status %q", stored.Status)
	}
}

func TestCheckClusterRecordsUnhealthy(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	cluster := env.store.addCluster("prod-eu", db.EnvProd)
	env.health.report = k8s.HealthReport{Message: "the API server did not respond"}

	rec := env.do(t, http.MethodPost, checkPath(cluster.ID), env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}

	body := decode[clusterResponse](t, rec)
	if body.Status != db.StatusUnhealthy {
		t.Fatalf("expected status %q, got %q", db.StatusUnhealthy, body.Status)
	}
	if body.StatusMessage != "the API server did not respond" {
		t.Fatalf("expected the probe message to surface, got %q", body.StatusMessage)
	}
}

func TestCheckClusterKeepsLastKnownVersionWhenUnreachable(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	cluster := env.store.addCluster("prod-eu", db.EnvProd)
	cluster.KubernetesVersion = "v1.30.1"
	env.health.report = k8s.HealthReport{Message: "the API server did not respond"}

	rec := env.do(t, http.MethodPost, checkPath(cluster.ID), env.tokenFor(t, admin), nil)

	body := decode[clusterResponse](t, rec)
	if body.KubernetesVersion != "v1.30.1" {
		t.Fatalf("expected the last known version to be kept, got %q", body.KubernetesVersion)
	}
}

func TestCheckClusterRequiresAdmin(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("devops", "pw", db.RoleUser)
	cluster := env.store.addCluster("prod-eu", db.EnvProd)
	env.store.grant(user.ID, cluster.ID, db.K8sRoleView, nil)

	rec := env.do(t, http.MethodPost, checkPath(cluster.ID), env.tokenFor(t, user), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d", http.StatusForbidden, rec.Code)
	}
	if env.health.calls != 0 {
		t.Fatal("a non-admin must not trigger a probe")
	}
}

func TestCheckClusterUnknownCluster(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)

	rec := env.do(t, http.MethodPost, checkPath(4242), env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, rec.Code)
	}
}

func TestCheckRouteAbsentWithoutChecker(t *testing.T) {
	store := newFakeStore()
	admin := store.addUser("admin", "pw", db.RoleAdmin)
	cluster := store.addCluster("prod-eu", db.EnvProd)
	manager := authManagerForTest()

	env := &testEnv{router: NewRouter(Options{Store: store, JWT: manager}), store: store, jwt: manager}

	rec := env.do(t, http.MethodPost, checkPath(cluster.ID), env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status %d without a checker, got %d", http.StatusNotFound, rec.Code)
	}
}

func TestShowClusterForGrantedUser(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("devops", "pw", db.RoleUser)
	cluster := env.store.addCluster("dev-eu", db.EnvDev)
	env.store.grant(user.ID, cluster.ID, db.K8sRoleEdit, []string{"team-a"})

	rec := env.do(t, http.MethodGet, "/api/v1/clusters/"+itoa(cluster.ID), env.tokenFor(t, user), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}

	body := decode[clusterResponse](t, rec)
	if body.Name != "dev-eu" || body.K8sRole != db.K8sRoleEdit {
		t.Fatalf("unexpected cluster payload: %+v", body)
	}
	if len(body.Namespaces) != 1 || body.Namespaces[0] != "team-a" {
		t.Fatalf("unexpected namespaces: %v", body.Namespaces)
	}
}

func TestShowClusterHidesSecrets(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	cluster := env.store.addCluster("prod-eu", db.EnvProd)

	rec := env.do(t, http.MethodGet, "/api/v1/clusters/"+itoa(cluster.ID), env.tokenFor(t, admin), nil)

	for _, secret := range []string{"secret-token", "service_account_token", "BEGIN CERTIFICATE"} {
		if body := rec.Body.String(); strings.Contains(body, secret) {
			t.Fatalf("cluster detail leaked %q: %s", secret, body)
		}
	}
}

func TestShowClusterRejectsUngranted(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("devops", "pw", db.RoleUser)
	cluster := env.store.addCluster("prod-eu", db.EnvProd)

	rec := env.do(t, http.MethodGet, "/api/v1/clusters/"+itoa(cluster.ID), env.tokenFor(t, user), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d", http.StatusForbidden, rec.Code)
	}
}
