package api

import (
	"net/http"
	"strings"
	"testing"
)

/*
 * A dependency map is worth drawing only where it is right about what is
 * missing — and only if it learns that without reading a Secret. These pin the
 * joins against canned cluster answers, as the traffic map's tests do.
 */

const (
	deploymentPath = "/apis/apps/v1/namespaces/shop/deployments/api"
	configMapPath  = "/api/v1/namespaces/shop/configmaps/api-config"
	accountPath    = "/api/v1/namespaces/shop/serviceaccounts/api"
	claimPath      = "/api/v1/namespaces/shop/persistentvolumeclaims/api-data"
	volumePath     = "/api/v1/persistentvolumes/pv-1"
)

// apiDeployment names every kind of dependency once: a ConfigMap by volume and
// by key, a Secret by env key and as an image pull secret, its own
// ServiceAccount, and a volume claim.
func apiDeployment() map[string]any {
	return map[string]any{
		"metadata": meta("shop", "api"),
		"spec": map[string]any{
			"replicas": 1,
			"selector": map[string]any{"matchLabels": map[string]any{"app": "api"}},
			"template": map[string]any{"spec": map[string]any{
				"serviceAccountName": "api",
				"imagePullSecrets":   []any{map[string]any{"name": "registry"}},
				"volumes": []any{
					map[string]any{"name": "config", "configMap": map[string]any{"name": "api-config"}},
					map[string]any{"name": "data", "persistentVolumeClaim": map[string]any{"claimName": "api-data"}},
				},
				"containers": []any{map[string]any{
					"name": "api",
					"env": []any{
						map[string]any{"name": "LOG_LEVEL", "valueFrom": map[string]any{
							"configMapKeyRef": map[string]any{"name": "api-config", "key": "log-level"}}},
						map[string]any{"name": "DB_PASSWORD", "valueFrom": map[string]any{
							"secretKeyRef": map[string]any{"name": "db", "key": "password"}}},
					},
					"volumeMounts": []any{
						map[string]any{"name": "config", "mountPath": "/etc/api"},
						map[string]any{"name": "data", "mountPath": "/var/lib/api"},
					},
				}},
			}},
		},
	}
}

func waitingPod(name, message string) map[string]any {
	p := pod("shop", name, "ReplicaSet", "api-1", false, "CreateContainerConfigError")
	status := p["status"].(map[string]any)
	status["phase"] = "Pending"
	status["containerStatuses"] = []any{map[string]any{
		"name": "api", "ready": false,
		"state": map[string]any{"waiting": map[string]any{"reason": "CreateContainerConfigError", "message": message}},
	}}
	return p
}

func healthyAPI() *fakeCluster {
	cluster := newFakeCluster()
	cluster.object(deploymentPath, apiDeployment())
	cluster.object(configMapPath, map[string]any{"metadata": meta("shop", "api-config"),
		"data": map[string]any{"log-level": "info", "app.yaml": "x: 1"}})
	cluster.object(accountPath, map[string]any{"metadata": meta("shop", "api")})
	cluster.object(claimPath, map[string]any{
		"spec":   map[string]any{"volumeName": "pv-1", "storageClassName": "standard", "resources": map[string]any{"requests": map[string]any{"storage": "1Gi"}}},
		"status": map[string]any{"phase": "Bound"},
	})
	cluster.object(volumePath, map[string]any{
		"spec":   map[string]any{"capacity": map[string]any{"storage": "1Gi"}, "persistentVolumeReclaimPolicy": "Delete"},
		"status": map[string]any{"phase": "Bound"},
	})
	cluster.list(podsPath, "app=api", pod("shop", "api-1-a", "ReplicaSet", "api-1", true, ""))
	return cluster
}

func dependencyMap(t *testing.T, cluster *fakeCluster, allowed []string) *trafficMap {
	t.Helper()
	resolver := newTrafficResolver(cluster, allowed)
	if failure := resolver.resolveDependencies("deployments", "shop", "api"); failure != nil {
		t.Fatalf("expected a map, got %+v", failure)
	}
	return &resolver.out
}

func TestDependencyMapDrawsEverythingATemplateNames(t *testing.T) {
	m := dependencyMap(t, healthyAPI(), nil)

	if m.Root != "deployments/shop/api" || m.Columns[1] != "Uses" {
		t.Fatalf("root %q columns %v", m.Root, m.Columns)
	}
	cm := findNode(t, m, "configmaps/shop/api-config")
	if cm.State != trafficOK || cm.Detail[0] != "2 keys" {
		t.Fatalf("expected the ConfigMap read for its keys, got %+v", cm)
	}
	edge := findEdge(t, m, "deployments/shop/api", "configmaps/shop/api-config")
	if strings.Join(edge.Labels, "|") != "volume config → /etc/api|env LOG_LEVEL" {
		t.Fatalf("expected both uses on one edge, got %v", edge.Labels)
	}
	findNode(t, m, "serviceaccounts/shop/api")
	findNode(t, m, "secrets/shop/registry")
	if claim := findNode(t, m, "persistentvolumeclaims/shop/api-data"); claim.State != trafficOK {
		t.Fatalf("expected a bound claim, got %+v", claim)
	}
	findEdge(t, m, "persistentvolumeclaims/shop/api-data", "persistentvolumes//pv-1")
	if root := findNode(t, m, "deployments/shop/api"); root.Detail[0] != "1 of 1 pods ready" {
		t.Fatalf("expected the root to count its pods, got %+v", root)
	}
}

// A Secret is named, never read — its state is what the pods say.
func TestDependencyMapNeverReadsASecret(t *testing.T) {
	cluster := healthyAPI()
	m := dependencyMap(t, cluster, nil)
	for _, path := range cluster.requested {
		if strings.Contains(path, "/secrets") {
			t.Fatalf("read a Secret: %s", path)
		}
	}
	if db := findNode(t, m, "secrets/shop/db"); db.State != trafficUnchecked || db.Problem != "" {
		t.Fatalf("expected a Secret nobody complains about drawn as named, got %+v", db)
	}
}

func TestDependencyMapLearnsAMissingSecretFromThePods(t *testing.T) {
	cluster := healthyAPI()
	cluster.list(podsPath, "app=api", waitingPod("api-1-a", `secret "db" not found`))

	m := dependencyMap(t, cluster, nil)
	db := findNode(t, m, "secrets/shop/db")
	if db.State != trafficBad || !strings.Contains(db.Problem, `secret "db" not found`) {
		t.Fatalf("expected the pod's report on the Secret, got %+v", db)
	}
	if edge := findEdge(t, m, "deployments/shop/api", "secrets/shop/db"); edge.State != trafficBad {
		t.Fatalf("expected the edge broken, got %+v", edge)
	}
}

func TestDependencyMapLearnsAMissingSecretKeyFromThePods(t *testing.T) {
	cluster := healthyAPI()
	cluster.list(podsPath, "app=api", waitingPod("api-1-a", "couldn't find key password in Secret shop/db"))

	m := dependencyMap(t, cluster, nil)
	edge := findEdge(t, m, "deployments/shop/api", "secrets/shop/db")
	if edge.State != trafficBad || !strings.Contains(edge.Problem, "key password") {
		t.Fatalf("expected the key named on the edge, got %+v", edge)
	}
}

func TestDependencyMapDrawsAMissingConfigMapAndKey(t *testing.T) {
	cluster := healthyAPI()
	cluster.object(configMapPath, map[string]any{"metadata": meta("shop", "api-config"),
		"data": map[string]any{"app.yaml": "x: 1"}})

	m := dependencyMap(t, cluster, nil)
	edge := findEdge(t, m, "deployments/shop/api", "configmaps/shop/api-config")
	if edge.State != trafficBad || !strings.Contains(edge.Problem, "key log-level") || edge.Labels[0] != "env LOG_LEVEL" {
		t.Fatalf("expected the missing key, its use first on the edge, got %+v", edge)
	}

	delete(cluster.answers, configMapPath)
	m = dependencyMap(t, cluster, nil)
	if cm := findNode(t, m, "configmaps/shop/api-config"); cm.State != trafficBad || !strings.Contains(cm.Problem, "does not exist") {
		t.Fatalf("expected a missing ConfigMap drawn broken, got %+v", cm)
	}
}

// An optional reference to an absent ConfigMap is how Kubernetes says "start
// without it" — a warning, not a failure.
func TestDependencyMapTreatsAnOptionalAbsenceAsAWarning(t *testing.T) {
	cluster := healthyAPI()
	deployment := apiDeployment()
	spec := deployment["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	spec["volumes"] = []any{map[string]any{"name": "extra",
		"configMap": map[string]any{"name": "api-extra", "optional": true}}}
	cluster.object(deploymentPath, deployment)

	m := dependencyMap(t, cluster, nil)
	if extra := findNode(t, m, "configmaps/shop/api-extra"); extra.State != trafficWarn {
		t.Fatalf("expected an optional absence to warn, got %+v", extra)
	}
}

func TestDependencyMapDrawsAPendingClaimAndAMissingAccount(t *testing.T) {
	cluster := healthyAPI()
	delete(cluster.answers, accountPath)
	cluster.object(claimPath, map[string]any{"spec": map[string]any{}, "status": map[string]any{"phase": "Pending"}})

	m := dependencyMap(t, cluster, nil)
	if sa := findNode(t, m, "serviceaccounts/shop/api"); sa.State != trafficBad || !strings.Contains(sa.Problem, "cannot create the pods") {
		t.Fatalf("expected a missing ServiceAccount drawn broken, got %+v", sa)
	}
	if claim := findNode(t, m, "persistentvolumeclaims/shop/api-data"); claim.State != trafficWarn {
		t.Fatalf("expected a Pending claim to warn, got %+v", claim)
	}
}

// A PersistentVolume is cluster-scoped, so a namespace-scoped grant never
// reads one — the inventory refuses that list for the same reason.
func TestDependencyMapDoesNotReadAVolumeForAScopedGrant(t *testing.T) {
	cluster := healthyAPI()
	m := dependencyMap(t, cluster, []string{"shop"})
	if pv := findNode(t, m, "persistentvolumes//pv-1"); pv.State != trafficOutside {
		t.Fatalf("expected the volume drawn outside the grant, got %+v", pv)
	}
	for _, path := range cluster.requested {
		if path == volumePath {
			t.Fatal("read a cluster-scoped volume for a scoped grant")
		}
	}
}

func TestDependencyMapNamesAStatefulSetsClaims(t *testing.T) {
	cluster := newFakeCluster()
	cluster.object("/apis/apps/v1/namespaces/shop/statefulsets/db", map[string]any{
		"metadata": meta("shop", "db"),
		"spec": map[string]any{
			"replicas":             6,
			"template":             map[string]any{"spec": map[string]any{}},
			"volumeClaimTemplates": []any{map[string]any{"metadata": map[string]any{"name": "data"}}},
		},
	})
	resolver := newTrafficResolver(cluster, nil)
	if failure := resolver.resolveDependencies("statefulsets", "shop", "db"); failure != nil {
		t.Fatalf("failure: %+v", failure)
	}
	findNode(t, &resolver.out, "persistentvolumeclaims/shop/data-db-0")
	findNode(t, &resolver.out, "persistentvolumeclaims/shop/data-db-3")
	for _, id := range nodeIDs(&resolver.out) {
		if id == "persistentvolumeclaims/shop/data-db-4" {
			t.Fatal("drew more claims than the bound")
		}
	}
}

func TestDependencyMapRefusesWhatItDoesNotDraw(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")

	rec := env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/dependencies?kind=services&namespace=shop&name=api",
		env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d (%s)", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
}

/* ---------------------------------------------------- namespace and labels --- */

func TestNamespaceMapDrawsEveryRouteTogether(t *testing.T) {
	cluster := healthyShop()
	cluster.list("/apis/networking.k8s.io/v1/namespaces/shop/ingresses", "",
		ingress("shop", "web", "shop.example.com", "/api", "api", 80),
		ingress("shop", "admin", "admin.example.com", "/", "admin", 80))

	m := resolveMap(t, cluster, nil, "namespaces", "shop", "shop")
	if m.Root != "" {
		t.Fatalf("a namespace map has no root, got %q", m.Root)
	}
	findNode(t, m, "ingresses/shop/web")
	findNode(t, m, "ingresses/shop/admin")
	if admin := findNode(t, m, "services/shop/admin"); admin.State != trafficBad {
		t.Fatalf("expected the missing Service drawn, got %+v", admin)
	}
}

// One edge carries every rule to a Service; the broken rule is the label drawn.
func TestABrokenRuleIsTheLabelDrawn(t *testing.T) {
	cluster := healthyShop()
	object := ingress("shop", "web", "shop.example.com", "/api", "api", 80)
	rules := object["spec"].(map[string]any)["rules"].([]any)
	rules = append(rules, map[string]any{
		"host": "admin.example.com",
		"http": map[string]any{"paths": []any{map[string]any{"path": "/",
			"backend": map[string]any{"service": map[string]any{"name": "api", "port": map[string]any{"number": 9443}}}}}},
	})
	object["spec"].(map[string]any)["rules"] = rules
	cluster.object(ingressPath, object)

	m := resolveMap(t, cluster, nil, "ingresses", "shop", "web")
	edge := findEdge(t, m, "ingresses/shop/web", "services/shop/api")
	if edge.State != trafficBad || edge.Labels[0] != "admin.example.com/:9443" {
		t.Fatalf("expected the broken rule first, got %+v", edge)
	}
}

// Not one route list answering is the read failing, not an empty namespace.
func TestNamespaceMapFailsWhenNothingAnswers(t *testing.T) {
	cluster := newFakeCluster()
	for _, path := range []string{
		"/apis/networking.k8s.io/v1/namespaces/shop/ingresses",
		"/apis/gateway.networking.k8s.io/v1beta1/namespaces/shop/httproutes",
		"/apis/networking.istio.io/v1beta1/namespaces/shop/virtualservices",
	} {
		cluster.refuse(path+"?limit=250", http.StatusBadGateway)
	}
	cluster.refuse("/apis/gateway.networking.k8s.io/v1/namespaces/shop/httproutes?limit=250", http.StatusBadGateway)
	cluster.refuse("/apis/networking.istio.io/v1/namespaces/shop/virtualservices?limit=250", http.StatusBadGateway)

	_, failure := newTrafficResolver(cluster, nil).resolve("namespaces", "shop", "shop")
	if failure == nil {
		t.Fatal("expected the map to fail when no list answered")
	}
}
