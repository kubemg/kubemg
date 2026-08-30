package api

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

type clusterListResponse struct {
	Clusters []clusterResponse `json:"clusters"`
}

func validClusterPayload() map[string]string {
	return map[string]string{
		"name":                  "prod-eu",
		"environment":           db.EnvProd,
		"api_url":               "https://prod-eu.example.com:6443",
		"ca_cert_data":          "LS0tLS1CRUdJTg==",
		"service_account_token": "eyJhbGciOi",
	}
}

func TestListClustersRequiresAuth(t *testing.T) {
	env := newTestEnv(t)

	rec := env.do(t, http.MethodGet, "/api/v1/clusters", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestListClustersAdminSeesAll(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	env.store.addCluster("prod-eu", db.EnvProd)
	env.store.addCluster("dev-eu", db.EnvDev)

	rec := env.do(t, http.MethodGet, "/api/v1/clusters", env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}

	body := decode[clusterListResponse](t, rec)
	if len(body.Clusters) != 2 {
		t.Fatalf("expected 2 clusters, got %d", len(body.Clusters))
	}
	for _, cluster := range body.Clusters {
		if cluster.K8sRole != db.K8sRoleClusterAdmin {
			t.Fatalf("expected admin to get %q, got %q", db.K8sRoleClusterAdmin, cluster.K8sRole)
		}
	}
}

func TestListClustersUserSeesOnlyGranted(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("devops", "pw", db.RoleUser)
	granted := env.store.addCluster("dev-eu", db.EnvDev)
	env.store.addCluster("prod-eu", db.EnvProd)
	env.store.grant(user.ID, granted.ID, db.K8sRoleEdit, []string{"team-a", "team-b"})

	rec := env.do(t, http.MethodGet, "/api/v1/clusters", env.tokenFor(t, user), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}

	body := decode[clusterListResponse](t, rec)
	if len(body.Clusters) != 1 {
		t.Fatalf("expected 1 cluster, got %d", len(body.Clusters))
	}

	got := body.Clusters[0]
	if got.Name != "dev-eu" {
		t.Fatalf("expected cluster \"dev-eu\", got %q", got.Name)
	}
	if got.K8sRole != db.K8sRoleEdit {
		t.Fatalf("expected k8s_role %q, got %q", db.K8sRoleEdit, got.K8sRole)
	}
	if len(got.Namespaces) != 2 || got.Namespaces[0] != "team-a" || got.Namespaces[1] != "team-b" {
		t.Fatalf("unexpected namespaces: %v", got.Namespaces)
	}
}

func TestListClustersNeverLeaksClusterSecrets(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	env.store.addCluster("prod-eu", db.EnvProd)

	rec := env.do(t, http.MethodGet, "/api/v1/clusters", env.tokenFor(t, admin), nil)

	body := rec.Body.String()
	secrets := []string{"secret-token", "service_account_token", "ca_cert_data", "BEGIN CERTIFICATE"}
	for _, secret := range secrets {
		if strings.Contains(body, secret) {
			t.Fatalf("cluster list leaked %q: %s", secret, body)
		}
	}
}

func TestCreateClusterAsAdmin(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)

	rec := env.do(t, http.MethodPost, "/api/v1/clusters", env.tokenFor(t, admin), validClusterPayload())
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusCreated, rec.Code, rec.Body.String())
	}

	body := decode[clusterResponse](t, rec)
	if body.ID == 0 {
		t.Fatal("expected a persisted cluster id")
	}
	if body.Status != db.StatusPending {
		t.Fatalf("expected status %q, got %q", db.StatusPending, body.Status)
	}

	stored, ok := env.store.clusters[body.ID]
	if !ok {
		t.Fatal("cluster was not persisted")
	}
	if stored.ServiceAccountToken != "eyJhbGciOi" {
		t.Fatalf("service account token not stored, got %q", stored.ServiceAccountToken)
	}
}

func TestCreateClusterForbiddenForNonAdmin(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("devops", "pw", db.RoleUser)

	rec := env.do(t, http.MethodPost, "/api/v1/clusters", env.tokenFor(t, user), validClusterPayload())
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d", http.StatusForbidden, rec.Code)
	}
	if len(env.store.clusters) != 0 {
		t.Fatal("non-admin request must not persist a cluster")
	}
}

func TestCreateClusterValidatesEnvironment(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)

	payload := validClusterPayload()
	payload["environment"] = "qa"

	rec := env.do(t, http.MethodPost, "/api/v1/clusters", env.tokenFor(t, admin), payload)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
	}
}

func TestCreateClusterRejectsUnparseableCACert(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)

	payload := validClusterPayload()
	payload["ca_cert_data"] = "!!! not a certificate !!!"

	rec := env.do(t, http.MethodPost, "/api/v1/clusters", env.tokenFor(t, admin), payload)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
	if len(env.store.clusters) != 0 {
		t.Fatal("a cluster with an invalid CA must not be persisted")
	}
}

func TestCreateClusterAcceptsEmptyCACert(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)

	payload := validClusterPayload()
	payload["ca_cert_data"] = ""

	rec := env.do(t, http.MethodPost, "/api/v1/clusters", env.tokenFor(t, admin), payload)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusCreated, rec.Code, rec.Body.String())
	}
}

func TestCreateClusterRejectsDuplicateName(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	env.store.addCluster("prod-eu", db.EnvProd)

	rec := env.do(t, http.MethodPost, "/api/v1/clusters", env.tokenFor(t, admin), validClusterPayload())
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status %d, got %d", http.StatusConflict, rec.Code)
	}
}

func TestDeleteClusterAsAdmin(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	cluster := env.store.addCluster("prod-eu", db.EnvProd)

	rec := env.do(t, http.MethodDelete, "/api/v1/clusters/"+itoa(cluster.ID), env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusNoContent, rec.Code, rec.Body.String())
	}
	if _, ok := env.store.clusters[cluster.ID]; ok {
		t.Fatal("cluster was not deleted")
	}
}

func TestDeleteClusterForbiddenForNonAdmin(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("devops", "pw", db.RoleUser)
	cluster := env.store.addCluster("prod-eu", db.EnvProd)

	rec := env.do(t, http.MethodDelete, "/api/v1/clusters/"+itoa(cluster.ID), env.tokenFor(t, user), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d", http.StatusForbidden, rec.Code)
	}
	if _, ok := env.store.clusters[cluster.ID]; !ok {
		t.Fatal("cluster must not be deleted by a non-admin")
	}
}

func TestDeleteClusterNotFound(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)

	rec := env.do(t, http.MethodDelete, "/api/v1/clusters/4242", env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, rec.Code)
	}
}

func TestDeleteClusterRejectsInvalidID(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)

	rec := env.do(t, http.MethodDelete, "/api/v1/clusters/not-a-number", env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
	}
}

/* ------------------------------------------------- the chip and its label --- */

func clusterPath(id uint) string {
	return "/api/v1/clusters/" + strconv.FormatUint(uint64(id), 10)
}

func TestCreateClusterNormalizesShortName(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)

	payload := validClusterPayload()
	payload["short_name"] = "eu-west-1"

	rec := env.do(t, http.MethodPost, "/api/v1/clusters", env.tokenFor(t, admin), payload)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusCreated, rec.Code, rec.Body.String())
	}

	// Folded to the four characters the chip can draw, not refused: every input
	// this normalizes is one somebody meant something reasonable by.
	body := decode[clusterResponse](t, rec)
	if body.ShortName != "EUWE" {
		t.Fatalf("expected short name %q, got %q", "EUWE", body.ShortName)
	}
	if env.store.clusters[body.ID].ShortName != "EUWE" {
		t.Fatalf("short name not persisted: %q", env.store.clusters[body.ID].ShortName)
	}
}

func TestCreateClusterWithoutShortNameStoresNone(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)

	rec := env.do(t, http.MethodPost, "/api/v1/clusters", env.tokenFor(t, admin), validClusterPayload())

	// Empty is a real answer rather than a missing one — the console falls back
	// to the derivation every cluster had before the field existed — so it must
	// stay empty rather than being invented here.
	body := decode[clusterResponse](t, rec)
	if body.ShortName != "" {
		t.Fatalf("expected no short name, got %q", body.ShortName)
	}
}

func TestPatchClusterUpdatesLabels(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	cluster := env.store.addCluster("prod-eu-west-1", db.EnvDev)

	rec := env.do(t, http.MethodPatch, clusterPath(cluster.ID), env.tokenFor(t, admin), map[string]string{
		"short_name":  "eu1",
		"environment": db.EnvProd,
		"description": "  the one that pages  ",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}

	body := decode[clusterResponse](t, rec)
	if body.ShortName != "EU1" {
		t.Fatalf("expected short name %q, got %q", "EU1", body.ShortName)
	}
	if body.Environment != db.EnvProd {
		t.Fatalf("expected environment %q, got %q", db.EnvProd, body.Environment)
	}
	if body.Description != "the one that pages" {
		t.Fatalf("expected a trimmed description, got %q", body.Description)
	}

	stored := env.store.clusters[cluster.ID]
	if stored.ShortName != "EU1" || stored.Environment != db.EnvProd {
		t.Fatalf("labels not persisted: %+v", stored)
	}
}

func TestPatchClusterLeavesOmittedFieldsAlone(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	cluster := env.store.addCluster("prod-eu", db.EnvProd)
	cluster.ShortName = "EU1"
	cluster.Description = "kept"

	rec := env.do(t, http.MethodPatch, clusterPath(cluster.ID), env.tokenFor(t, admin), map[string]string{
		"description": "rewritten",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}

	body := decode[clusterResponse](t, rec)
	if body.ShortName != "EU1" {
		t.Fatalf("an omitted short name must be kept, got %q", body.ShortName)
	}
	if body.Environment != db.EnvProd {
		t.Fatalf("an omitted environment must be kept, got %q", body.Environment)
	}
	if body.Description != "rewritten" {
		t.Fatalf("expected the description to change, got %q", body.Description)
	}
}

func TestPatchClusterClearsShortNameWhenSentEmpty(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	cluster := env.store.addCluster("prod-eu", db.EnvProd)
	cluster.ShortName = "EU1"

	rec := env.do(t, http.MethodPatch, clusterPath(cluster.ID), env.tokenFor(t, admin), map[string]string{
		"short_name": "",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}

	// Taking a chip back off is how an operator returns to the derivation, so
	// an explicit empty has to clear rather than read as "no change".
	if env.store.clusters[cluster.ID].ShortName != "" {
		t.Fatalf("expected the short name to be cleared, got %q", env.store.clusters[cluster.ID].ShortName)
	}
}

func TestPatchClusterRefusesUnknownEnvironment(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	cluster := env.store.addCluster("prod-eu", db.EnvProd)

	rec := env.do(t, http.MethodPatch, clusterPath(cluster.ID), env.tokenFor(t, admin), map[string]string{
		"environment": "production",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
	if env.store.clusters[cluster.ID].Environment != db.EnvProd {
		t.Fatal("a refused patch must not have written anything")
	}
}

func TestPatchClusterForbiddenForNonAdmin(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("devops", "pw", db.RoleUser)
	cluster := env.store.addCluster("prod-eu", db.EnvProd)
	env.store.grant(user.ID, cluster.ID, db.K8sRoleEdit, nil)

	rec := env.do(t, http.MethodPatch, clusterPath(cluster.ID), env.tokenFor(t, user), map[string]string{
		"short_name": "OWN",
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d", http.StatusForbidden, rec.Code)
	}
	if env.store.clusters[cluster.ID].ShortName != "" {
		t.Fatal("a forbidden patch must not have written anything")
	}
}

func TestPatchClusterUnknownIsNotFound(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)

	rec := env.do(t, http.MethodPatch, "/api/v1/clusters/404", env.tokenFor(t, admin), map[string]string{
		"short_name": "X",
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, rec.Code)
	}
}
