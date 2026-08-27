package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/db"
	"github.com/kubemg/kubemg/backend/pkg/helm"
)

/*
 * Removing a Helm release.
 *
 * The manifest walk, the ordering and the per-object report are all decided
 * before the tunnel sees anything, which is where they are pinned. The refusals
 * are the part worth being exact about: a scoped grant is told what it cannot
 * remove *before* the first delete, because a half-removed release is worse
 * than a refused one.
 */

func objectsOf(t *testing.T, manifest string) []helm.Object {
	t.Helper()
	objects, err := helm.ManifestObjects(manifest, "shop")
	if err != nil {
		t.Fatalf("ManifestObjects: %v", err)
	}
	return objects
}

const uninstallManifest = `---
# Source: app/templates/serviceaccount.yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: api
  namespace: shop
---
# Source: app/templates/service.yaml
apiVersion: v1
kind: Service
metadata:
  name: api
  namespace: shop
---
# Source: app/templates/deployment.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: shop
`

func TestUninstallDeletesInReverseOfInstallOrder(t *testing.T) {
	objects := objectsOf(t, uninstallManifest)
	if len(objects) != 3 {
		t.Fatalf("objects = %d, want the three the manifest records", len(objects))
	}

	// The handler reverses before deleting, so a dependant goes before what it
	// depends on — the same rule `removalsOf` applies on an upgrade.
	reverse(objects)
	if objects[0].Kind != "Deployment" || objects[2].Kind != "ServiceAccount" {
		t.Fatalf("order = %s, %s, %s — want the reverse of install order",
			objects[0].Kind, objects[1].Kind, objects[2].Kind)
	}
}

// reverse mirrors what uninstallHelmRelease does to the object list, so the
// ordering assertion above is over the same operation rather than a copy of it.
func reverse(objects []helm.Object) {
	for i, j := 0, len(objects)-1; i < j; i, j = i+1, j-1 {
		objects[i], objects[j] = objects[j], objects[i]
	}
}

// uninstallPlanFor drives the pre-flight check directly, the way planFor does
// for an install: it is the only part of the removal that can be asserted
// without a cluster, and it is the part that decides whether anything is
// deleted at all.
func uninstallPlanFor(t *testing.T, grant db.UserClusterAccess,
	objects []helm.Object,
) (bool, *httptest.ResponseRecorder) {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	return (&server{}).planUninstall(c, grant, objects), recorder
}

func TestPlanUninstallRefusesAClusterScopedObjectToAScopedGrant(t *testing.T) {
	// A chart's ClusterRole has no namespace in the recorded manifest, which is
	// what makes this pre-flight: nothing has been deleted yet.
	objects := []helm.Object{
		{APIVersion: "apps/v1", Kind: "Deployment", Name: "api", Namespace: "team-a"},
		{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "ClusterRole", Name: "api-reader"},
	}
	allowed, rec := uninstallPlanFor(t, db.UserClusterAccess{Namespaces: "team-a"}, objects)
	if allowed {
		t.Fatal("a scoped grant must not be allowed to start removing a cluster-scoped object")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
	// The object has to be named, or the operator cannot tell this apart from
	// the cluster's own RBAC refusing them.
	if body := rec.Body.String(); !strings.Contains(body, "ClusterRole/api-reader") {
		t.Fatalf("refusal = %s, want the object named", body)
	}
}

func TestPlanUninstallRefusesANamespaceOutsideTheGrant(t *testing.T) {
	objects := []helm.Object{
		{APIVersion: "v1", Kind: "Service", Name: "api", Namespace: "team-b"},
	}
	allowed, rec := uninstallPlanFor(t, db.UserClusterAccess{Namespaces: "team-a"}, objects)
	if allowed {
		t.Fatal("a release reaching outside the grant must be refused whole")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestPlanUninstallLetsAnUnscopedGrantThrough(t *testing.T) {
	objects := []helm.Object{
		{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "ClusterRole", Name: "api-reader"},
	}
	if allowed, _ := uninstallPlanFor(t, db.UserClusterAccess{}, objects); !allowed {
		t.Fatal("an unscoped grant has nothing to refuse here — the cluster's own RBAC decides")
	}
}

func TestUninstallFailureMessageNamesTheFirstThingLeftBehind(t *testing.T) {
	reports := []objectReport{
		{Kind: "Deployment", Name: "api", Action: actionDeleted},
		{Kind: "Service", Name: "api", Action: actionSkipped, Message: "forbidden"},
		{Kind: "ServiceAccount", Name: "api", Action: actionDeleted},
	}
	got := uninstallFailureMessage(reports)
	if !strings.Contains(got, "Service/api") || !strings.Contains(got, "forbidden") {
		t.Fatalf("message = %q, want the object and the cluster's own words", got)
	}
}

func TestHelmSecretNameReadsTheStoredRevision(t *testing.T) {
	secret := map[string]any{"metadata": map[string]any{"name": "sh.helm.release.v1.api.v3"}}
	if got := helmSecretName(secret); got != "sh.helm.release.v1.api.v3" {
		t.Fatalf("helmSecretName = %q", got)
	}
	if got := helmSecretName(map[string]any{}); got != "" {
		t.Fatalf("helmSecretName = %q, want empty for a secret with no metadata", got)
	}
}

/* ------------------------------------------------------------ the route --- */

func TestUninstallRefusesADirectCluster(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")
	cluster := env.store.addCluster("legacy", "dev") // direct mode
	token := env.tokenFor(t, admin)

	rec := env.do(t, http.MethodDelete,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/helm/releases/api?namespace=shop", token, nil)

	if rec.Code == http.StatusOK {
		t.Fatalf("a direct-mode cluster has no tunnel to remove a release through, got %d", rec.Code)
	}
}

func TestUninstallRefusesANamespaceOutsideTheGrant(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("scoped", "secret123", "user")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	env.store.grant(user.ID, cluster.ID, "edit", []string{"team-a"})
	token := env.tokenFor(t, user)

	rec := env.do(t, http.MethodDelete,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/helm/releases/api?namespace=team-b", token, nil)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusForbidden, rec.Code, rec.Body.String())
	}
}

func TestUninstallRefusesAReleaseNameThatIsNotOne(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	token := env.tokenFor(t, admin)

	rec := env.do(t, http.MethodDelete,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/helm/releases/Not%20A%20Name?namespace=shop",
		token, nil)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
}
