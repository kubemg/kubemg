package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

/*
 * A traffic map is a chain of reads where every link can break, and the map is
 * only worth drawing if each break is drawn as the break it is. These tests feed
 * the resolver canned cluster answers and pin the joins: which Service a route
 * reaches, whether the port is one it exposes, whether its selector matches
 * anything, whether those pods are ready — and that a namespace outside the
 * caller's grant is drawn as such without ever being asked for.
 */

// fakeCluster answers GETs from a table of paths. Anything not in it is a 404,
// which is what a cluster says about an object that does not exist.
type fakeCluster struct {
	answers   map[string]fakeAnswer
	requested []string
}

type fakeAnswer struct {
	status int
	body   any
}

func newFakeCluster() *fakeCluster {
	return &fakeCluster{answers: map[string]fakeAnswer{}}
}

func (f *fakeCluster) get(path string) (int, []byte, error) {
	f.requested = append(f.requested, path)
	answer, ok := f.answers[path]
	if !ok {
		return http.StatusNotFound, []byte(`{"message":"not found"}`), nil
	}
	body, _ := json.Marshal(answer.body)
	return answer.status, body, nil
}

func (f *fakeCluster) object(path string, body any) { f.answers[path] = fakeAnswer{200, body} }

func (f *fakeCluster) list(path, selector string, items ...any) {
	query := url.Values{}
	query.Set("limit", "250")
	if selector != "" {
		query.Set("labelSelector", selector)
	}
	if items == nil {
		items = []any{}
	}
	f.answers[path+"?"+query.Encode()] = fakeAnswer{200, map[string]any{"items": items}}
}

func (f *fakeCluster) refuse(path string, status int) {
	f.answers[path] = fakeAnswer{status, map[string]string{"message": "pods is forbidden: User \"kubemg:u:dev\" cannot list resource \"pods\""}}
}

/* ------------------------------------------------------------ fixtures --- */

func meta(namespace, name string) map[string]any {
	return map[string]any{"name": name, "namespace": namespace}
}

func service(namespace, name string, selector map[string]string, ports ...map[string]any) map[string]any {
	spec := map[string]any{"type": "ClusterIP", "ports": ports}
	if selector != nil {
		spec["selector"] = selector
	}
	return map[string]any{"metadata": meta(namespace, name), "spec": spec}
}

func port(number int, name string, target any) map[string]any {
	return map[string]any{"port": number, "name": name, "targetPort": target, "protocol": "TCP"}
}

func endpointSlice(ready ...bool) map[string]any {
	endpoints := []any{}
	for _, r := range ready {
		endpoints = append(endpoints, map[string]any{"conditions": map[string]any{"ready": r}})
	}
	return map[string]any{"endpoints": endpoints}
}

func pod(namespace, name, ownerKind, ownerName string, ready bool, reason string) map[string]any {
	metadata := meta(namespace, name)
	if ownerKind != "" {
		metadata["ownerReferences"] = []any{map[string]any{"kind": ownerKind, "name": ownerName, "controller": true}}
	}
	state := map[string]any{"running": map[string]any{}}
	phase := "Running"
	if !ready {
		state = map[string]any{"waiting": map[string]any{"reason": reason}}
	}
	return map[string]any{
		"metadata": metadata,
		"spec":     map[string]any{"containers": []any{map[string]any{"name": "app", "image": "app:1"}}},
		"status": map[string]any{
			"phase": phase,
			"containerStatuses": []any{map[string]any{
				"name": "app", "image": "app:1", "ready": ready, "state": state,
			}},
		},
	}
}

func ownedReplicaSet(namespace, name, deployment string) map[string]any {
	metadata := meta(namespace, name)
	metadata["ownerReferences"] = []any{map[string]any{"kind": "Deployment", "name": deployment, "controller": true}}
	return map[string]any{"metadata": metadata}
}

func ingress(namespace, name, host, path, backend string, backendPort int) map[string]any {
	return map[string]any{
		"metadata": meta(namespace, name),
		"spec": map[string]any{
			"ingressClassName": "nginx",
			"tls":              []any{map[string]any{"hosts": []any{host}, "secretName": "shop-tls"}},
			"rules": []any{map[string]any{
				"host": host,
				"http": map[string]any{"paths": []any{map[string]any{
					"path": path,
					"backend": map[string]any{"service": map[string]any{
						"name": backend, "port": map[string]any{"number": backendPort},
					}},
				}}},
			}},
		},
		"status": map[string]any{"loadBalancer": map[string]any{"ingress": []any{map[string]any{"ip": "203.0.113.10"}}}},
	}
}

const (
	ingressPath  = "/apis/networking.k8s.io/v1/namespaces/shop/ingresses/web"
	servicePath  = "/api/v1/namespaces/shop/services/api"
	slicesPath   = "/apis/discovery.k8s.io/v1/namespaces/shop/endpointslices"
	podsPath     = "/api/v1/namespaces/shop/pods"
	replicaPath  = "/apis/apps/v1/namespaces/shop/replicasets/api-7d9"
	sliceLabel   = "kubernetes.io/service-name=api"
	apiSelector  = "app=api"
	httpRouteV1  = "/apis/gateway.networking.k8s.io/v1/namespaces/shop/httproutes/web"
	virtualSvcV1 = "/apis/networking.istio.io/v1/namespaces/shop/virtualservices/web"
)

// healthyShop is an Ingress sending shop.example.com/api to Service api:80,
// backed by a Deployment with two ready pods.
func healthyShop() *fakeCluster {
	cluster := newFakeCluster()
	cluster.object(ingressPath, ingress("shop", "web", "shop.example.com", "/api", "api", 80))
	cluster.object(servicePath, service("shop", "api", map[string]string{"app": "api"}, port(80, "http", 8080)))
	cluster.list(slicesPath, sliceLabel, endpointSlice(true, true))
	cluster.list(podsPath, apiSelector,
		pod("shop", "api-7d9-a", "ReplicaSet", "api-7d9", true, ""),
		pod("shop", "api-7d9-b", "ReplicaSet", "api-7d9", true, ""))
	cluster.object(replicaPath, ownedReplicaSet("shop", "api-7d9", "api"))
	return cluster
}

func resolveMap(t *testing.T, cluster *fakeCluster, allowed []string, kind, namespace, name string) *trafficMap {
	t.Helper()
	result, failure := newTrafficResolver(cluster, allowed).resolve(kind, namespace, name)
	if failure != nil {
		t.Fatalf("expected a map, got a failure: %+v", failure)
	}
	return result
}

func findNode(t *testing.T, m *trafficMap, id string) trafficNode {
	t.Helper()
	for _, node := range m.Nodes {
		if node.ID == id {
			return node
		}
	}
	t.Fatalf("no node %q in %+v", id, nodeIDs(m))
	return trafficNode{}
}

func findEdge(t *testing.T, m *trafficMap, from, to string) trafficEdge {
	t.Helper()
	for _, edge := range m.Edges {
		if edge.From == from && edge.To == to {
			return edge
		}
	}
	t.Fatalf("no edge %s → %s", from, to)
	return trafficEdge{}
}

func nodeIDs(m *trafficMap) []string {
	ids := make([]string, 0, len(m.Nodes))
	for _, node := range m.Nodes {
		ids = append(ids, node.ID)
	}
	return ids
}

/* --------------------------------------------------------------- tests --- */

func TestTrafficMapFollowsAnIngressToItsPods(t *testing.T) {
	m := resolveMap(t, healthyShop(), nil, "ingresses", "shop", "web")

	if m.Root != "ingresses/shop/web" {
		t.Fatalf("root = %q", m.Root)
	}
	host := findNode(t, m, "host/shop.example.com")
	if host.Column != trafficColumnEntry || !strings.Contains(strings.Join(host.Detail, " "), "shop-tls") {
		t.Fatalf("expected the host as an entry naming its TLS secret, got %+v", host)
	}
	findEdge(t, m, "host/shop.example.com", "ingresses/shop/web")

	edge := findEdge(t, m, "ingresses/shop/web", "services/shop/api")
	if edge.State != trafficOK || len(edge.Labels) != 1 || edge.Labels[0] != "shop.example.com/api:80" {
		t.Fatalf("expected one healthy edge labelled with the rule, got %+v", edge)
	}

	svc := findNode(t, m, "services/shop/api")
	if svc.State != trafficOK || !strings.Contains(strings.Join(svc.Detail, " "), "2 of 2 endpoints ready") {
		t.Fatalf("expected a healthy Service reporting its endpoints, got %+v", svc)
	}

	// The ReplicaSet is resolved to the Deployment an operator thinks in.
	deployment := findNode(t, m, "deployments/shop/api")
	if deployment.Resource != "deployments" || deployment.Column != trafficColumnWorkload || deployment.State != trafficOK {
		t.Fatalf("expected the Deployment as the workload, got %+v", deployment)
	}
	findEdge(t, m, "services/shop/api", "deployments/shop/api")

	pod := findNode(t, m, "pods/shop/api-7d9-a")
	if pod.Pod == nil || pod.Pod.Name != "api-7d9-a" || pod.State != trafficOK {
		t.Fatalf("expected the pod with its row, got %+v", pod)
	}
	findEdge(t, m, "deployments/shop/api", "pods/shop/api-7d9-a")
}

// The TLS Secret is named, never opened: checking it exists would put a Secret
// read in the trail every time somebody looked at a route.
func TestTrafficMapNeverReadsASecret(t *testing.T) {
	cluster := healthyShop()
	resolveMap(t, cluster, nil, "ingresses", "shop", "web")
	for _, path := range cluster.requested {
		if strings.Contains(path, "/secrets") {
			t.Fatalf("the map read a Secret: %s", path)
		}
	}
}

func TestTrafficMapDrawsAMissingServiceAsBroken(t *testing.T) {
	cluster := healthyShop()
	delete(cluster.answers, servicePath)

	m := resolveMap(t, cluster, nil, "ingresses", "shop", "web")
	svc := findNode(t, m, "services/shop/api")
	if svc.State != trafficBad || !strings.Contains(svc.Problem, "does not exist") {
		t.Fatalf("expected a missing Service drawn as broken, got %+v", svc)
	}
	if edge := findEdge(t, m, "ingresses/shop/web", "services/shop/api"); edge.State != trafficBad {
		t.Fatalf("expected the edge to a missing Service broken, got %+v", edge)
	}
}

func TestTrafficMapDrawsAPortTheServiceDoesNotExpose(t *testing.T) {
	cluster := healthyShop()
	cluster.object(ingressPath, ingress("shop", "web", "shop.example.com", "/api", "api", 8080))

	m := resolveMap(t, cluster, nil, "ingresses", "shop", "web")
	edge := findEdge(t, m, "ingresses/shop/web", "services/shop/api")
	if edge.State != trafficBad || !strings.Contains(edge.Problem, "port 8080") {
		t.Fatalf("expected the edge refused for its port, got %+v", edge)
	}
	// The Service itself is fine — another route may reach it on a port it
	// does expose — so only the edge is broken.
	if svc := findNode(t, m, "services/shop/api"); svc.State != trafficOK {
		t.Fatalf("expected the Service left healthy, got %+v", svc)
	}
}

func TestTrafficMapDrawsASelectorThatMatchesNothing(t *testing.T) {
	cluster := healthyShop()
	cluster.list(podsPath, apiSelector)
	cluster.list(slicesPath, sliceLabel)

	m := resolveMap(t, cluster, nil, "ingresses", "shop", "web")
	svc := findNode(t, m, "services/shop/api")
	if svc.State != trafficBad || !strings.Contains(svc.Problem, "matches no pods") || !strings.Contains(svc.Problem, apiSelector) {
		t.Fatalf("expected the selector named as matching nothing, got %+v", svc)
	}
}

func TestTrafficMapDrawsAServiceWithNoReadyEndpoint(t *testing.T) {
	cluster := healthyShop()
	cluster.list(slicesPath, sliceLabel, endpointSlice(false, false))
	cluster.list(podsPath, apiSelector,
		pod("shop", "api-7d9-a", "ReplicaSet", "api-7d9", false, "CrashLoopBackOff"),
		pod("shop", "api-7d9-b", "ReplicaSet", "api-7d9", false, "CrashLoopBackOff"))

	m := resolveMap(t, cluster, nil, "ingresses", "shop", "web")
	if svc := findNode(t, m, "services/shop/api"); svc.State != trafficBad || !strings.Contains(svc.Problem, "no endpoint is ready") {
		t.Fatalf("expected the Service drawn with no ready endpoint, got %+v", svc)
	}
	if deployment := findNode(t, m, "deployments/shop/api"); deployment.State != trafficBad {
		t.Fatalf("expected the Deployment drawn broken, got %+v", deployment)
	}
	// In the container's own word, not a generic "not ready".
	if p := findNode(t, m, "pods/shop/api-7d9-a"); p.State != trafficBad || p.Problem != "CrashLoopBackOff" {
		t.Fatalf("expected the pod's reason, got %+v", p)
	}
}

func TestTrafficMapFoldsPodsButNeverTheUnreadyOnes(t *testing.T) {
	cluster := healthyShop()
	pods := []any{}
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		pods = append(pods, pod("shop", "api-7d9-"+name, "ReplicaSet", "api-7d9", true, ""))
	}
	pods = append(pods, pod("shop", "api-7d9-z", "ReplicaSet", "api-7d9", false, "ImagePullBackOff"))
	cluster.list(podsPath, apiSelector, pods...)

	m := resolveMap(t, cluster, nil, "ingresses", "shop", "web")
	findNode(t, m, "pods/shop/api-7d9-z")
	more := findNode(t, m, "more/deployments/shop/api")
	if more.Name != "+2 more" {
		t.Fatalf("expected two pods folded away, got %+v", more)
	}
	if d := findNode(t, m, "deployments/shop/api"); d.Detail[0] != "5 of 6 pods ready" || d.State != trafficWarn {
		t.Fatalf("expected the count across every pod, got %+v", d)
	}
}

// A hop into a namespace the caller holds no grant on is drawn as outside it —
// and never read, which is the part that matters.
func TestTrafficMapDoesNotReadOutsideTheGrant(t *testing.T) {
	cluster := newFakeCluster()
	cluster.object(virtualSvcV1, map[string]any{
		"metadata": meta("shop", "web"),
		"spec": map[string]any{
			"hosts": []any{"shop.example.com"},
			"http": []any{map[string]any{"route": []any{
				map[string]any{"destination": map[string]any{"host": "ledger.payments.svc.cluster.local"}},
			}}},
		},
	})

	m := resolveMap(t, cluster, []string{"shop"}, "virtualservices", "shop", "web")
	ledger := findNode(t, m, "services/payments/ledger")
	if ledger.State != trafficOutside {
		t.Fatalf("expected the Service drawn outside the grant, got %+v", ledger)
	}
	for _, path := range cluster.requested {
		if strings.Contains(path, "/namespaces/payments/") {
			t.Fatalf("the map read a namespace outside the grant: %s", path)
		}
	}
	if edge := findEdge(t, m, "virtualservices/shop/web", "services/payments/ledger"); edge.State != trafficUnchecked {
		t.Fatalf("expected the edge drawn as unknown rather than broken, got %+v", edge)
	}
}

// The cluster's RBAC refusing one hop is part of the map, not its failure.
func TestTrafficMapSurvivesARefusedHop(t *testing.T) {
	cluster := healthyShop()
	query := url.Values{"labelSelector": {apiSelector}, "limit": {"250"}}
	cluster.refuse(podsPath+"?"+query.Encode(), http.StatusForbidden)

	m := resolveMap(t, cluster, nil, "ingresses", "shop", "web")
	svc := findNode(t, m, "services/shop/api")
	if !strings.Contains(strings.Join(svc.Detail, " "), "pods not read: pods is forbidden") {
		t.Fatalf("expected the refusal in the cluster's words, got %+v", svc)
	}
}

func TestTrafficMapReadsAnHTTPRouteFromItsStatus(t *testing.T) {
	cluster := healthyShop()
	cluster.object(httpRouteV1, map[string]any{
		"metadata": meta("shop", "web"),
		"spec": map[string]any{
			"parentRefs": []any{map[string]any{"name": "edge", "namespace": "infra"}},
			"hostnames":  []any{"shop.example.com"},
			"rules": []any{map[string]any{
				"matches": []any{map[string]any{"path": map[string]any{"value": "/api"}}},
				"backendRefs": []any{
					map[string]any{"name": "api", "port": 80, "weight": 90},
					map[string]any{"name": "api-canary", "port": 80, "weight": 10},
				},
			}},
		},
		"status": map[string]any{"parents": []any{map[string]any{
			"parentRef": map[string]any{"name": "edge", "namespace": "infra"},
			"conditions": []any{
				map[string]any{"type": "Accepted", "status": "True"},
				map[string]any{"type": "ResolvedRefs", "status": "False", "reason": "BackendNotFound",
					"message": "Service shop/api-canary not found"},
			},
		}}},
	})

	m := resolveMap(t, cluster, nil, "httproutes", "shop", "web")
	route := findNode(t, m, "httproutes/shop/web")
	if route.State != trafficBad || !strings.Contains(route.Problem, "BackendNotFound") {
		t.Fatalf("expected the controller's verdict on the route, got %+v", route)
	}
	gateway := findNode(t, m, "gateways/infra/edge")
	if gateway.State != trafficOK || gateway.APIGroup != "gateway.networking.k8s.io" || gateway.Resource != "gateways" {
		t.Fatalf("expected the accepting Gateway, openable as a custom resource, got %+v", gateway)
	}
	if edge := findEdge(t, m, "httproutes/shop/web", "services/shop/api"); edge.Labels[0] != "/api:80 · 90%" {
		t.Fatalf("expected the weight on the edge, got %+v", edge.Labels)
	}
	if canary := findNode(t, m, "services/shop/api-canary"); canary.State != trafficBad {
		t.Fatalf("expected the missing canary drawn broken, got %+v", canary)
	}
}

func TestTrafficMapDrawsAnExternalHostWithoutFollowingIt(t *testing.T) {
	cluster := newFakeCluster()
	cluster.object(virtualSvcV1, map[string]any{
		"metadata": meta("shop", "web"),
		"spec": map[string]any{"http": []any{map[string]any{"route": []any{
			map[string]any{"destination": map[string]any{"host": "api.stripe.com"}},
		}}}},
	})

	m := resolveMap(t, cluster, nil, "virtualservices", "shop", "web")
	findNode(t, m, "mesh")
	if external := findNode(t, m, "external/api.stripe.com"); external.Kind != "External" || external.Resource != "" {
		t.Fatalf("expected an unopenable external host, got %+v", external)
	}
	if len(cluster.requested) != 1 {
		t.Fatalf("expected only the VirtualService read, got %v", cluster.requested)
	}
}

func TestIstioHostResolution(t *testing.T) {
	cases := []struct {
		host, name, namespace string
		ok                    bool
	}{
		{"reviews", "reviews", "shop", true},
		{"reviews.prod.svc.cluster.local", "reviews", "prod", true},
		{"reviews.prod.svc", "reviews", "prod", true},
		{"reviews.prod.svc.corp.internal", "reviews", "prod", true},
		// Istio expands only a name with no dot; "reviews.prod" is a hostname.
		{"reviews.prod", "", "", false},
		{"api.stripe.com", "", "", false},
		{"*.example.com", "", "", false},
	}
	for _, test := range cases {
		name, namespace, ok := istioService(test.host, "shop")
		if name != test.name || namespace != test.namespace || ok != test.ok {
			t.Fatalf("%s: got (%q, %q, %v)", test.host, name, namespace, ok)
		}
	}
}

func TestTrafficMapForAServiceFindsTheRoutesThatSendToIt(t *testing.T) {
	cluster := healthyShop()
	cluster.list("/apis/networking.k8s.io/v1/namespaces/shop/ingresses", "",
		ingress("shop", "web", "shop.example.com", "/api", "api", 80),
		ingress("shop", "admin", "admin.example.com", "/", "admin", 80))
	// Gateway API and Istio are not installed: their lists are 404, which is
	// not something to report.

	m := resolveMap(t, cluster, nil, "services", "shop", "api")
	if m.Root != "services/shop/api" {
		t.Fatalf("root = %q", m.Root)
	}
	findEdge(t, m, "ingresses/shop/web", "services/shop/api")
	for _, id := range nodeIDs(m) {
		if id == "ingresses/shop/admin" || id == "host/admin.example.com" || id == "services/shop/admin" {
			t.Fatalf("drew a route that does not send to this Service: %s", id)
		}
	}
	findNode(t, m, "pods/shop/api-7d9-a")
	for _, note := range m.Notes {
		if strings.Contains(note, "could not be read") {
			t.Fatalf("an uninstalled CRD was reported: %q", note)
		}
	}
	if !strings.Contains(strings.Join(m.Notes, " "), "Only routes in shop are searched") {
		t.Fatalf("expected the search's limit stated, got %v", m.Notes)
	}
}

func TestTrafficMapForAMissingObjectIsANotFound(t *testing.T) {
	_, failure := newTrafficResolver(newFakeCluster(), nil).resolve("ingresses", "shop", "gone")
	if failure == nil || failure.status != http.StatusNotFound {
		t.Fatalf("expected a 404, got %+v", failure)
	}
}

/* ------------------------------------------------------------- handler --- */

func TestTrafficMapRefusesWhatItDoesNotDraw(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")

	rec := env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/traffic?kind=deployments&namespace=shop&name=api",
		env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d (%s)", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
}

func TestTrafficMapRefusesANamespaceOutsideTheGrant(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("scoped", "secret123", "user")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	env.store.grant(user.ID, cluster.ID, "view", []string{"team-a"})

	rec := env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/traffic?kind=ingresses&namespace=team-b&name=web",
		env.tokenFor(t, user), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected %d, got %d (%s)", http.StatusForbidden, rec.Code, rec.Body.String())
	}
}

func TestTrafficMapRefusesADirectCluster(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")
	cluster := env.store.addCluster("legacy", "dev")

	rec := env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/traffic?kind=ingresses&namespace=shop&name=web",
		env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected %d, got %d (%s)", http.StatusConflict, rec.Code, rec.Body.String())
	}
}

// An Ingress with no address and no host is the ordinary state on a cluster
// with no load balancer, and a `null` list in its row took the Ingresses page
// down. A list field is always a list.
func TestRouteRowsNeverCarryANullList(t *testing.T) {
	for name, value := range map[string]any{
		"empty":     orEmpty(nil),
		"populated": orEmpty([]string{"a"}),
	} {
		body, _ := json.Marshal(value)
		if strings.Contains(string(body), "null") {
			t.Fatalf("%s encoded a null: %s", name, body)
		}
	}
}
