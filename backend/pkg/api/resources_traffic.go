package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/bastion"
	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * Where a route's traffic actually goes.
 *
 * An Ingress, an HTTPRoute and a VirtualService each say "send this host and
 * path to that Service", and every one of the ways that sentence goes wrong is
 * invisible from the route itself: the Service was renamed, the port is not one
 * the Service exposes, its selector matches nothing, or every pod it matches is
 * failing its readiness probe. Finding which meant five `kubectl` reads and
 * holding the answers in your head. This is those reads, joined, and the joins
 * that fail are drawn as failures with the reason beside them.
 *
 * It is a resolver in the shape workload/pods is: the caller names one object,
 * everything else is derived from what that object says — never from the
 * caller — and every read goes down the same impersonated tunnel, so a hop the
 * cluster's RBAC refuses is drawn as refused rather than failing the map, and a
 * hop into a namespace outside the caller's grant is drawn as outside it and
 * **not asked for at all**.
 *
 * Two things it deliberately does not read. A TLS Secret is named, never
 * opened: checking that it exists would put a Secret `get` in the trail every
 * time somebody looked at a route. And an Istio DestinationRule is not
 * followed, so a subset is a label on the edge rather than a set of pods.
 */

// The columns a map is drawn in, left to right: where traffic enters, the route
// that matches it, the Service it is sent to, the workload behind that Service,
// and the pods that answer. A column is part of the answer rather than layout
// the console invents, because which column a node is in is a statement about
// its role in the path.
const (
	trafficColumnEntry = iota
	trafficColumnRoute
	trafficColumnService
	trafficColumnWorkload
	trafficColumnPod
)

// What a hop is known to be. `unchecked` with no problem is a node kubemg drew
// without reading — a mesh, an external host, an Istio gateway — and reads as
// neutral; with a problem it is a read that did not answer.
const (
	trafficOK        = "ok"
	trafficWarn      = "warn"
	trafficBad       = "bad"
	trafficUnchecked = "unchecked"
	trafficDenied    = "denied"
	trafficOutside   = "outside"
)

// maxTrafficServices bounds how many Services one map follows. Each is three
// reads (the Service, its endpoint slices, its pods) plus one per ReplicaSet,
// and a VirtualService fanning out to forty backends is asking for a page of
// its own rather than a drawing.
const maxTrafficServices = 10

// maxTrafficPodsPerWorkload is how many pods a workload is drawn with before
// the rest fold into one "+N more" node. Unready pods are drawn first, so the
// ones that explain a failure are never the ones folded away.
const maxTrafficPodsPerWorkload = 4

// trafficListLimit is one page. A namespace with more routes or a Service with
// more pods than this is drawn from the first page and says so.
const trafficListLimit = 250

// The kinds a map can be asked for.
var trafficRootKinds = []string{"ingresses", "httproutes", "virtualservices", "services"}

type trafficNode struct {
	ID string `json:"id"`
	// Kind is the object's kind as Kubernetes spells it, or a word for a node
	// that is not an object (Host, Mesh, External).
	Kind string `json:"kind"`
	// Resource is the inventory key the console opens the node with; empty when
	// there is nothing to open. For a custom resource outside the fixed
	// inventory it is the plural, with APIGroup naming the group, and the
	// console resolves the served version from the cluster's own CRD list.
	Resource  string   `json:"resource,omitempty"`
	APIGroup  string   `json:"api_group,omitempty"`
	Namespace string   `json:"namespace,omitempty"`
	Name      string   `json:"name"`
	Column    int      `json:"column"`
	Detail    []string `json:"detail"`
	State     string   `json:"state"`
	Problem   string   `json:"problem,omitempty"`
	// Pod is the row, for a pod node, so opening it lands on the same drawer a
	// pod list opens — logs and terminal included — without a second read.
	Pod *podView `json:"pod,omitempty"`
}

type trafficEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
	// Labels are the matches that send traffic down this edge — a host and
	// path, a weight, a port. Several rules sending to one Service are one edge
	// with several labels, not several edges drawn on top of each other.
	Labels  []string `json:"labels"`
	State   string   `json:"state"`
	Problem string   `json:"problem,omitempty"`
}

type trafficMap struct {
	Root  string        `json:"root"`
	Nodes []trafficNode `json:"nodes"`
	Edges []trafficEdge `json:"edges"`
	// Notes are what this map did not look at, said where the map is read.
	Notes []string `json:"notes"`
}

// trafficReader is the one thing the resolver needs from a cluster: a GET by
// path, answered with the cluster's status and body. The handler's goes down
// the impersonated tunnel; a test's is a map of canned answers.
type trafficReader interface {
	get(path string) (status int, body []byte, err error)
}

// tunnelReader reads through the gateway as the caller, exactly as every other
// resource read does — impersonated, namespace-scoped and audited.
type tunnelReader struct {
	s       *server
	c       *gin.Context
	user    *db.User
	cluster *db.Cluster
	grant   db.UserClusterAccess
}

func (r tunnelReader) get(path string) (int, []byte, error) {
	resp, err := r.s.proxy.Call(r.c.Request.Context(), r.user, r.cluster, r.grant,
		http.MethodGet, path, nil, nil)
	if err != nil {
		// A refusal from the gateway itself — scope, guardrail — is an answer
		// about this hop, not a reason to abandon the map.
		var callErr *bastion.CallError
		if errors.As(err, &callErr) {
			body, _ := json.Marshal(map[string]string{"message": callErr.Message})
			return callErr.Status, body, nil
		}
		return 0, nil, err
	}
	return resp.Status, resp.Body, nil
}

// showTrafficMap answers GET .../resources/traffic?kind=&namespace=&name=.
func (s *server) showTrafficMap(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}

	kind := strings.TrimSpace(c.Query("kind"))
	if !slices.Contains(trafficRootKinds, kind) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "kubemg draws a traffic map for Ingresses, HTTPRoutes, VirtualServices and Services only"})
		return
	}
	name := strings.TrimSpace(c.Query("name"))
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "a resource name is required"})
		return
	}
	namespace, ok := s.resourceNamespace(c, grant)
	if !ok {
		return
	}

	resolver := newTrafficResolver(tunnelReader{s: s, c: c, user: user, cluster: cluster, grant: grant},
		grant.NamespaceList())
	result, failure := resolver.resolve(kind, namespace, name)
	if failure != nil {
		// The object the map was asked about could not be read, which is the
		// request's failure rather than a hop's — in the cluster's own words.
		c.JSON(failure.status, gin.H{"error": failure.problem})
		return
	}
	c.JSON(http.StatusOK, result)
}

/* ------------------------------------------------------------- resolver --- */

type trafficResolver struct {
	read    trafficReader
	allowed []string

	out   trafficMap
	nodes map[string]int
	edges map[[2]string]int
	notes map[string]bool

	// services is every Service already followed, nil for one that was drawn
	// but could not be read, so a Service two rules send to is read once.
	services map[string]*serviceObject
	followed int
	// owners caches the workload a pod's controller resolves to, so ten pods
	// of one ReplicaSet cost one ReplicaSet read.
	owners map[string]string
}

// readFailure is a read that did not produce an object, as a node state and as
// the HTTP answer it would be if it were the request's own object.
type readFailure struct {
	state   string
	problem string
	status  int
}

func newTrafficResolver(read trafficReader, allowed []string) *trafficResolver {
	return &trafficResolver{
		read:     read,
		allowed:  allowed,
		out:      trafficMap{Nodes: []trafficNode{}, Edges: []trafficEdge{}, Notes: []string{}},
		nodes:    map[string]int{},
		edges:    map[[2]string]int{},
		notes:    map[string]bool{},
		services: map[string]*serviceObject{},
		owners:   map[string]string{},
	}
}

func (r *trafficResolver) resolve(kind, namespace, name string) (*trafficMap, *readFailure) {
	switch kind {
	case "ingresses":
		var object ingressObject
		if failure := r.object(namespace, objectKinds["ingresses"].versions, name, &object); failure != nil {
			return nil, failure
		}
		r.out.Root = r.addIngress(object, "")
	case "httproutes":
		var object httpRouteObject
		if failure := r.object(namespace, objectKinds["httproutes"].versions, name, &object); failure != nil {
			return nil, failure
		}
		r.out.Root = r.addHTTPRoute(object, "")
	case "virtualservices":
		var object virtualServiceObject
		if failure := r.object(namespace, objectKinds["virtualservices"].versions, name, &object); failure != nil {
			return nil, failure
		}
		r.out.Root = r.addVirtualService(object, "")
	case "services":
		if failure := r.resolveServiceRoot(namespace, name); failure != nil {
			return nil, failure
		}
	}
	return &r.out, nil
}

func trafficID(resource, namespace, name string) string {
	return resource + "/" + namespace + "/" + name
}

func (r *trafficResolver) inGrant(namespace string) bool {
	return len(r.allowed) == 0 || slices.Contains(r.allowed, namespace)
}

func (r *trafficResolver) note(text string) {
	if r.notes[text] {
		return
	}
	r.notes[text] = true
	r.out.Notes = append(r.out.Notes, text)
}

// addNode draws a node once; a second draw of the same id keeps the first.
func (r *trafficResolver) addNode(node trafficNode) string {
	if _, seen := r.nodes[node.ID]; seen {
		return node.ID
	}
	if node.Detail == nil {
		node.Detail = []string{}
	}
	r.nodes[node.ID] = len(r.out.Nodes)
	r.out.Nodes = append(r.out.Nodes, node)
	return node.ID
}

func (r *trafficResolver) node(id string) *trafficNode {
	index, ok := r.nodes[id]
	if !ok {
		return nil
	}
	return &r.out.Nodes[index]
}

// mark raises a node's state, never lowers it: a Service that is both missing
// a port on one edge and fine on another is still a Service with a problem.
func (r *trafficResolver) mark(id, state, problem string) {
	node := r.node(id)
	if node == nil || severity(state) <= severity(node.State) {
		return
	}
	node.State = state
	node.Problem = problem
}

func severity(state string) int {
	switch state {
	case trafficBad:
		return 3
	case trafficWarn:
		return 2
	case trafficOK:
		return 1
	}
	return 0
}

// addEdge draws one edge per pair, folding a second rule's label onto it.
func (r *trafficResolver) addEdge(from, to, label, state, problem string) {
	key := [2]string{from, to}
	if index, seen := r.edges[key]; seen {
		edge := &r.out.Edges[index]
		if label != "" && !slices.Contains(edge.Labels, label) {
			edge.Labels = append(edge.Labels, label)
		}
		if severity(state) > severity(edge.State) {
			edge.State, edge.Problem = state, problem
		}
		return
	}
	edge := trafficEdge{From: from, To: to, Labels: []string{}, State: state, Problem: problem}
	if label != "" {
		edge.Labels = append(edge.Labels, label)
	}
	r.edges[key] = len(r.out.Edges)
	r.out.Edges = append(r.out.Edges, edge)
}

/* ---------------------------------------------------------------- reads --- */

// object reads one namespaced object, walking the kind's candidate versions the
// way readObject does — but answering with a failure rather than writing one,
// because for every hop but the first a failed read is part of the map.
func (r *trafficResolver) object(namespace string, versions []resourceListPath, name string, out any) *readFailure {
	if !r.inGrant(namespace) {
		return &readFailure{trafficOutside,
			"namespace " + namespace + " is outside your granted scope, so kubemg did not read it",
			http.StatusForbidden}
	}
	paths := objectKind{versions: versions, namespaced: true}.objectPaths(namespace, name)
	for i, path := range paths {
		status, body, err := r.read.get(path)
		if err != nil {
			return &readFailure{trafficUnchecked, "could not read from the cluster", http.StatusBadGateway}
		}
		if status == http.StatusNotFound && i < len(paths)-1 {
			continue
		}
		if failure := statusFailure(status, body); failure != nil {
			return failure
		}
		if err := json.Unmarshal(body, out); err != nil {
			return &readFailure{trafficUnchecked, "the cluster returned an unreadable response", http.StatusBadGateway}
		}
		return nil
	}
	return &readFailure{trafficBad, "not found", http.StatusNotFound}
}

func statusFailure(status int, body []byte) *readFailure {
	switch {
	case status >= 200 && status < 300:
		return nil
	case status == http.StatusNotFound:
		return &readFailure{trafficBad, "not found", status}
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return &readFailure{trafficDenied, kubeErrorMessage(body, status), status}
	}
	return &readFailure{trafficUnchecked, kubeErrorMessage(body, status), status}
}

// list reads one page of a namespaced list. A 404 on the last candidate version
// is answered as such, so an optional CRD that is not installed can be skipped
// rather than reported.
func (r *trafficResolver) list(namespace string, versions []resourceListPath, selector string) ([]json.RawMessage, bool, *readFailure) {
	if !r.inGrant(namespace) {
		return nil, false, &readFailure{trafficOutside, "outside your granted scope", http.StatusForbidden}
	}
	query := url.Values{}
	query.Set("limit", strconv.Itoa(trafficListLimit))
	if selector != "" {
		query.Set("labelSelector", selector)
	}
	for i, version := range versions {
		status, body, err := r.read.get(version.namespaced(namespace) + "?" + query.Encode())
		if err != nil {
			return nil, false, &readFailure{trafficUnchecked, "could not read from the cluster", http.StatusBadGateway}
		}
		if status == http.StatusNotFound && i < len(versions)-1 {
			continue
		}
		if failure := statusFailure(status, body); failure != nil {
			return nil, false, failure
		}
		var page struct {
			Metadata struct {
				Continue string `json:"continue"`
			} `json:"metadata"`
			Items []json.RawMessage `json:"items"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, false, &readFailure{trafficUnchecked, "the cluster returned an unreadable response", http.StatusBadGateway}
		}
		return page.Items, page.Metadata.Continue != "", nil
	}
	return nil, false, &readFailure{trafficBad, "not found", http.StatusNotFound}
}

/* -------------------------------------------------------------- ingress --- */

type ingressObject struct {
	Metadata objectMeta `json:"metadata"`
	Spec     struct {
		IngressClassName string          `json:"ingressClassName"`
		DefaultBackend   *ingressBackend `json:"defaultBackend"`
		TLS              []struct {
			Hosts      []string `json:"hosts"`
			SecretName string   `json:"secretName"`
		} `json:"tls"`
		Rules []struct {
			Host string `json:"host"`
			HTTP *struct {
				Paths []struct {
					Path    string         `json:"path"`
					Backend ingressBackend `json:"backend"`
				} `json:"paths"`
			} `json:"http"`
		} `json:"rules"`
	} `json:"spec"`
	Status struct {
		LoadBalancer struct {
			Ingress []struct {
				IP       string `json:"ip"`
				Hostname string `json:"hostname"`
			} `json:"ingress"`
		} `json:"loadBalancer"`
	} `json:"status"`
}

type ingressBackend struct {
	Service *struct {
		Name string `json:"name"`
		Port struct {
			Number int32  `json:"number"`
			Name   string `json:"name"`
		} `json:"port"`
	} `json:"service"`
	Resource *struct {
		Kind string `json:"kind"`
		Name string `json:"name"`
	} `json:"resource"`
}

// sendsTo reports whether this backend is the Service with id serviceID.
func (b ingressBackend) sendsTo(namespace, serviceID string) bool {
	return b.Service != nil && trafficID("services", namespace, b.Service.Name) == serviceID
}

// addIngress draws an Ingress and follows its backends. `only`, when set, is a
// Service id: the map is that Service's, so only the rules that reach it are
// drawn.
func (r *trafficResolver) addIngress(object ingressObject, only string) string {
	namespace, name := object.Metadata.Namespace, object.Metadata.Name
	id := trafficID("ingresses", namespace, name)

	class := object.Spec.IngressClassName
	if class == "" {
		class = object.Metadata.Annotations["kubernetes.io/ingress.class"]
	}
	node := trafficNode{ID: id, Kind: "Ingress", Resource: "ingresses", Namespace: namespace, Name: name,
		Column: trafficColumnRoute, State: trafficOK}
	if class != "" {
		node.Detail = append(node.Detail, "class "+class)
	}
	var addresses []string
	for _, entry := range object.Status.LoadBalancer.Ingress {
		addresses = append(addresses, cmpOr(entry.IP, entry.Hostname))
	}
	if len(addresses) > 0 {
		node.Detail = append(node.Detail, strings.Join(addresses, ", "))
	} else {
		node.State = trafficWarn
		node.Problem = "no address yet — no ingress controller has admitted it"
	}
	r.addNode(node)

	tls := map[string]string{}
	for _, entry := range object.Spec.TLS {
		for _, host := range entry.Hosts {
			tls[host] = entry.SecretName
		}
	}
	entry := func(host string) string {
		label := cmpOr(host, "*")
		hostID := r.addNode(trafficNode{ID: "host/" + label, Kind: "Host", Name: label,
			Column: trafficColumnEntry, State: trafficOK, Detail: hostDetail(tls, host)})
		r.addEdge(hostID, id, "", trafficOK, "")
		return hostID
	}

	for _, rule := range object.Spec.Rules {
		if rule.HTTP == nil {
			continue
		}
		reaches := only == ""
		for _, path := range rule.HTTP.Paths {
			reaches = reaches || path.Backend.sendsTo(namespace, only)
		}
		if !reaches {
			continue
		}
		entry(rule.Host)
		for _, path := range rule.HTTP.Paths {
			r.ingressBackend(id, cmpOr(rule.Host, "*")+cmpOr(path.Path, "/"), namespace, path.Backend, only)
		}
	}
	if backend := object.Spec.DefaultBackend; backend != nil && (only == "" || backend.sendsTo(namespace, only)) {
		entry("")
		r.ingressBackend(id, "default backend", namespace, *backend, only)
	}
	return id
}

func hostDetail(tls map[string]string, host string) []string {
	if secret, ok := tls[host]; ok && host != "" {
		if secret == "" {
			return []string{"TLS · the controller's default certificate"}
		}
		return []string{"TLS · secret " + secret}
	}
	return []string{"HTTP"}
}

func (r *trafficResolver) ingressBackend(from, label, namespace string, backend ingressBackend, only string) {
	if backend.Service == nil {
		if only != "" || backend.Resource == nil {
			return
		}
		to := r.addNode(trafficNode{ID: trafficID("resource", namespace, backend.Resource.Name),
			Kind: backend.Resource.Kind, Namespace: namespace, Name: backend.Resource.Name,
			Column: trafficColumnService, State: trafficUnchecked,
			Problem: "a resource backend — kubemg follows Service backends only"})
		r.addEdge(from, to, label, trafficUnchecked, "")
		return
	}
	port := servicePortRef{number: backend.Service.Port.Number, name: backend.Service.Port.Name}
	r.serviceBackend(from, label, namespace, backend.Service.Name, port, only)
}

/* ------------------------------------------------------------ httproute --- */

type gatewayParentRef struct {
	Group       *string `json:"group"`
	Kind        *string `json:"kind"`
	Namespace   *string `json:"namespace"`
	Name        string  `json:"name"`
	SectionName *string `json:"sectionName"`
}

type routeCondition struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

type gatewayBackendRef struct {
	Group     *string `json:"group"`
	Kind      *string `json:"kind"`
	Name      string  `json:"name"`
	Namespace *string `json:"namespace"`
	Port      *int32  `json:"port"`
	Weight    *int32  `json:"weight"`
}

type httpRouteObject struct {
	Metadata objectMeta `json:"metadata"`
	Spec     struct {
		ParentRefs []gatewayParentRef `json:"parentRefs"`
		Hostnames  []string           `json:"hostnames"`
		Rules      []struct {
			Matches []struct {
				Path *struct {
					Value string `json:"value"`
				} `json:"path"`
			} `json:"matches"`
			BackendRefs []gatewayBackendRef `json:"backendRefs"`
		} `json:"rules"`
	} `json:"spec"`
	Status struct {
		Parents []struct {
			ParentRef  gatewayParentRef `json:"parentRef"`
			Conditions []routeCondition `json:"conditions"`
		} `json:"parents"`
	} `json:"status"`
}

func deref[T any](value *T, fallback T) T {
	if value == nil {
		return fallback
	}
	return *value
}

func cmpOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// serviceTarget reports which Service a backendRef sends to, and false for a
// backend that is not a core Service.
func (b gatewayBackendRef) serviceTarget(routeNamespace string) (namespace string, ok bool) {
	if deref(b.Group, "") != "" || deref(b.Kind, "Service") != "Service" {
		return "", false
	}
	return deref(b.Namespace, routeNamespace), true
}

func (r *trafficResolver) addHTTPRoute(object httpRouteObject, only string) string {
	namespace, name := object.Metadata.Namespace, object.Metadata.Name
	id := trafficID("httproutes", namespace, name)

	node := trafficNode{ID: id, Kind: "HTTPRoute", Resource: "httproutes", Namespace: namespace, Name: name,
		Column: trafficColumnRoute, State: trafficOK}
	if len(object.Spec.Hostnames) > 0 {
		node.Detail = append(node.Detail, strings.Join(object.Spec.Hostnames, ", "))
	} else {
		node.Detail = append(node.Detail, "any hostname")
	}
	// The route's status is the gateway controller's own verdict on it, and the
	// only place a refused cross-namespace reference or a missing backend is
	// written down — so it is read, not re-derived.
	for _, parent := range object.Status.Parents {
		for _, condition := range parent.Conditions {
			if (condition.Type == "Accepted" || condition.Type == "ResolvedRefs") && condition.Status == "False" {
				node.State = trafficBad
				node.Problem = strings.TrimSpace(condition.Reason + ": " + condition.Message)
			}
		}
	}
	if node.State == trafficOK && len(object.Spec.ParentRefs) > 0 && len(object.Status.Parents) == 0 {
		node.State = trafficWarn
		node.Problem = "no gateway has reported on this route — check its parent reference"
	}
	r.addNode(node)

	for _, ref := range object.Spec.ParentRefs {
		parentNamespace := deref(ref.Namespace, namespace)
		kind := deref(ref.Kind, "Gateway")
		parent := trafficNode{ID: trafficID("gateways", parentNamespace, ref.Name), Kind: kind,
			Namespace: parentNamespace, Name: ref.Name, Column: trafficColumnEntry,
			State: trafficUnchecked, Problem: "has not reported on this route"}
		if kind == "Gateway" && deref(ref.Group, "gateway.networking.k8s.io") == "gateway.networking.k8s.io" {
			parent.Resource, parent.APIGroup = "gateways", "gateway.networking.k8s.io"
		}
		if section := deref(ref.SectionName, ""); section != "" {
			parent.Detail = append(parent.Detail, "listener "+section)
		}
		for _, status := range object.Status.Parents {
			if status.ParentRef.Name != ref.Name || deref(status.ParentRef.Namespace, namespace) != parentNamespace {
				continue
			}
			parent.State, parent.Problem = trafficOK, ""
			for _, condition := range status.Conditions {
				if condition.Type == "Accepted" && condition.Status == "False" {
					parent.State = trafficBad
					parent.Problem = strings.TrimSpace(condition.Reason + ": " + condition.Message)
				}
			}
		}
		parentID := r.addNode(parent)
		edgeState := trafficOK
		if parent.State == trafficBad {
			edgeState = trafficBad
		}
		r.addEdge(parentID, id, "", edgeState, parent.Problem)
	}

	for _, rule := range object.Spec.Rules {
		var paths []string
		for _, match := range rule.Matches {
			if match.Path != nil {
				paths = append(paths, match.Path.Value)
			}
		}
		label := cmpOr(strings.Join(paths, ", "), "/")

		var total int32
		for _, ref := range rule.BackendRefs {
			total += deref(ref.Weight, 1)
		}
		for _, ref := range rule.BackendRefs {
			var extras []string
			if len(rule.BackendRefs) > 1 && total > 0 {
				extras = append(extras, fmt.Sprintf("%d%%", deref(ref.Weight, 1)*100/total))
			}
			edgeLabel := strings.Join(append([]string{label}, extras...), " · ")
			backendNamespace, isService := ref.serviceTarget(namespace)
			if !isService {
				if only != "" {
					continue
				}
				to := r.addNode(trafficNode{ID: trafficID("backend", deref(ref.Namespace, namespace), ref.Name),
					Kind: deref(ref.Kind, "Service"), Namespace: deref(ref.Namespace, namespace), Name: ref.Name,
					Column: trafficColumnService, State: trafficUnchecked,
					Problem: "not a core Service — kubemg follows Service backends only"})
				r.addEdge(id, to, edgeLabel, trafficUnchecked, "")
				continue
			}
			port := servicePortRef{number: deref(ref.Port, 0)}
			r.serviceBackend(id, label, backendNamespace, ref.Name, port, only, extras...)
		}
	}
	return id
}

func (o httpRouteObject) sendsTo(serviceNamespace, service string) bool {
	for _, rule := range o.Spec.Rules {
		for _, ref := range rule.BackendRefs {
			if namespace, ok := ref.serviceTarget(o.Metadata.Namespace); ok && namespace == serviceNamespace && ref.Name == service {
				return true
			}
		}
	}
	return false
}

/* ------------------------------------------------------- virtualservice --- */

type virtualServiceRoute struct {
	Destination struct {
		Host   string `json:"host"`
		Subset string `json:"subset"`
		Port   *struct {
			Number int32 `json:"number"`
		} `json:"port"`
	} `json:"destination"`
	Weight int32 `json:"weight"`
}

type virtualServiceObject struct {
	Metadata objectMeta `json:"metadata"`
	Spec     struct {
		Hosts    []string `json:"hosts"`
		Gateways []string `json:"gateways"`
		HTTP     []struct {
			Name  string `json:"name"`
			Match []struct {
				URI map[string]string `json:"uri"`
			} `json:"match"`
			Route []virtualServiceRoute `json:"route"`
		} `json:"http"`
		TCP []struct {
			Route []virtualServiceRoute `json:"route"`
		} `json:"tcp"`
		TLS []struct {
			Route []virtualServiceRoute `json:"route"`
		} `json:"tls"`
	} `json:"spec"`
}

// istioService resolves a destination host to a Service the way Istio does: a
// name with no dot is in the VirtualService's own namespace, `name.ns.svc[...]`
// is that Service, and anything else is a host Istio does not expand — a
// ServiceEntry, or somewhere outside the mesh — which this map does not follow.
func istioService(host, namespace string) (string, string, bool) {
	if host == "" || strings.Contains(host, "*") {
		return "", "", false
	}
	labels := strings.Split(host, ".")
	if len(labels) == 1 {
		return labels[0], namespace, true
	}
	if len(labels) >= 3 && labels[2] == "svc" {
		return labels[0], labels[1], true
	}
	return "", "", false
}

func (o virtualServiceObject) routes() []struct {
	label  string
	routes []virtualServiceRoute
} {
	var out []struct {
		label  string
		routes []virtualServiceRoute
	}
	for _, rule := range o.Spec.HTTP {
		var matches []string
		for _, match := range rule.Match {
			for kind, value := range match.URI {
				if kind == "regex" {
					value = "~" + value
				}
				matches = append(matches, value)
			}
		}
		slices.Sort(matches)
		label := cmpOr(strings.Join(matches, ", "), cmpOr(rule.Name, "/"))
		out = append(out, struct {
			label  string
			routes []virtualServiceRoute
		}{label, rule.Route})
	}
	for _, tcp := range o.Spec.TCP {
		out = append(out, struct {
			label  string
			routes []virtualServiceRoute
		}{"tcp", tcp.Route})
	}
	for _, tls := range o.Spec.TLS {
		out = append(out, struct {
			label  string
			routes []virtualServiceRoute
		}{"tls", tls.Route})
	}
	return out
}

func (o virtualServiceObject) sendsTo(serviceNamespace, service string) bool {
	for _, entry := range o.routes() {
		for _, route := range entry.routes {
			name, namespace, ok := istioService(route.Destination.Host, o.Metadata.Namespace)
			if ok && name == service && namespace == serviceNamespace {
				return true
			}
		}
	}
	return false
}

func (r *trafficResolver) addVirtualService(object virtualServiceObject, only string) string {
	namespace, name := object.Metadata.Namespace, object.Metadata.Name
	id := trafficID("virtualservices", namespace, name)
	node := trafficNode{ID: id, Kind: "VirtualService", Resource: "virtualservices", Namespace: namespace,
		Name: name, Column: trafficColumnRoute, State: trafficOK}
	if len(object.Spec.Hosts) > 0 {
		node.Detail = append(node.Detail, strings.Join(object.Spec.Hosts, ", "))
	}
	r.addNode(node)

	gateways := object.Spec.Gateways
	if len(gateways) == 0 {
		gateways = []string{"mesh"}
	}
	for _, gateway := range gateways {
		var parent trafficNode
		if gateway == "mesh" {
			parent = trafficNode{ID: "mesh", Kind: "Mesh", Name: "mesh", Column: trafficColumnEntry,
				State: trafficUnchecked, Detail: []string{"sidecars inside the mesh"}}
		} else {
			gatewayNamespace, gatewayName, found := strings.Cut(gateway, "/")
			if !found {
				gatewayNamespace, gatewayName = namespace, gateway
			}
			parent = trafficNode{ID: trafficID("istio-gateways", gatewayNamespace, gatewayName), Kind: "Gateway",
				Resource: "gateways", APIGroup: "networking.istio.io", Namespace: gatewayNamespace,
				Name: gatewayName, Column: trafficColumnEntry, State: trafficUnchecked}
		}
		r.addEdge(r.addNode(parent), id, "", trafficOK, "")
	}

	for _, entry := range object.routes() {
		var total int32
		for _, route := range entry.routes {
			total += route.Weight
		}
		for _, route := range entry.routes {
			var extras []string
			if route.Destination.Subset != "" {
				extras = append(extras, "subset "+route.Destination.Subset)
				r.note("A subset is drawn as a label: DestinationRules are not read, so which pods a subset selects is not shown.")
			}
			if len(entry.routes) > 1 && total > 0 {
				extras = append(extras, fmt.Sprintf("%d%%", route.Weight*100/total))
			}
			label := strings.Join(append([]string{entry.label}, extras...), " · ")
			service, serviceNamespace, ok := istioService(route.Destination.Host, namespace)
			if !ok {
				if only != "" {
					continue
				}
				to := r.addNode(trafficNode{ID: "external/" + route.Destination.Host, Kind: "External",
					Name: route.Destination.Host, Column: trafficColumnService, State: trafficUnchecked,
					Detail: []string{"not a cluster Service — a ServiceEntry or a host outside the mesh"}})
				r.addEdge(id, to, label, trafficUnchecked, "")
				continue
			}
			var port servicePortRef
			if route.Destination.Port != nil {
				port.number = route.Destination.Port.Number
			}
			r.serviceBackend(id, entry.label, serviceNamespace, service, port, only, extras...)
		}
	}
	return id
}

/* -------------------------------------------------------------- service --- */

type servicePortRef struct {
	number int32
	name   string
}

func (p servicePortRef) String() string {
	if p.name != "" {
		return ":" + p.name
	}
	if p.number != 0 {
		return ":" + strconv.Itoa(int(p.number))
	}
	return ""
}

type serviceObject struct {
	Metadata objectMeta `json:"metadata"`
	Spec     struct {
		Type         string            `json:"type"`
		ExternalName string            `json:"externalName"`
		Selector     map[string]string `json:"selector"`
		Ports        []struct {
			Name       string          `json:"name"`
			Port       int32           `json:"port"`
			TargetPort json.RawMessage `json:"targetPort"`
			Protocol   string          `json:"protocol"`
		} `json:"ports"`
	} `json:"spec"`
}

func (s serviceObject) hasPort(ref servicePortRef) bool {
	for _, port := range s.Spec.Ports {
		if (ref.name != "" && port.Name == ref.name) || (ref.number != 0 && port.Port == ref.number) {
			return true
		}
	}
	return false
}

func (s serviceObject) portSummary() string {
	var parts []string
	for _, port := range s.Spec.Ports {
		target := strings.Trim(string(port.TargetPort), `"`)
		part := strconv.Itoa(int(port.Port))
		if target != "" && target != part {
			part += "→" + target
		}
		if port.Protocol != "" && port.Protocol != "TCP" {
			part += "/" + port.Protocol
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, ", ")
}

// serviceBackend draws the edge from a route to a Service, following the
// Service the first time it is reached.
// The label reads match, then port, then whatever qualifies the split —
// "/api:80 · subset v1 · 80%" — so the port stays beside the path it serves.
func (r *trafficResolver) serviceBackend(from, label, namespace, name string, port servicePortRef, only string, extras ...string) {
	to := trafficID("services", namespace, name)
	if only != "" && to != only {
		return
	}
	service := r.service(namespace, name)
	label = strings.Join(append([]string{label + port.String()}, extras...), " · ")

	state, problem := trafficOK, ""
	switch target := r.node(to); {
	case target != nil && target.State == trafficBad && service == nil:
		state, problem = trafficBad, "the Service it sends to does not exist"
	case service == nil:
		// Refused, outside the grant, or not followed: the edge is real, what
		// it reaches is not known.
		state = trafficUnchecked
	case service != nil && port != (servicePortRef{}) && service.Spec.Type != "ExternalName" && !service.hasPort(port):
		// The edge is broken, not the Service: another route may reach the
		// same Service on a port it does expose, and that path works.
		state, problem = trafficBad, fmt.Sprintf("port %s is not a port of Service %s", strings.TrimPrefix(port.String(), ":"), name)
	}
	r.addEdge(from, to, label, state, problem)
}

// service reads a Service once and follows it to its endpoints and pods.
func (r *trafficResolver) service(namespace, name string) *serviceObject {
	id := trafficID("services", namespace, name)
	if service, seen := r.services[id]; seen {
		return service
	}
	node := trafficNode{ID: id, Kind: "Service", Resource: "services", Namespace: namespace, Name: name,
		Column: trafficColumnService}
	if r.followed >= maxTrafficServices {
		node.State = trafficUnchecked
		node.Problem = fmt.Sprintf("not followed — a map follows at most %d Services", maxTrafficServices)
		r.addNode(node)
		r.services[id] = nil
		return nil
	}
	r.followed++

	var object serviceObject
	if failure := r.object(namespace, objectKinds["services"].versions, name, &object); failure != nil {
		node.State, node.Problem = failure.state, failure.problem
		if failure.status == http.StatusNotFound {
			node.Problem = "this Service does not exist"
		}
		r.addNode(node)
		r.services[id] = nil
		return nil
	}
	return r.drawService(object)
}

// drawService draws a Service already read and follows it to its pods.
func (r *trafficResolver) drawService(object serviceObject) *serviceObject {
	namespace, name := object.Metadata.Namespace, object.Metadata.Name
	id := trafficID("services", namespace, name)
	r.services[id] = &object
	node := trafficNode{ID: id, Kind: "Service", Resource: "services", Namespace: namespace, Name: name,
		Column: trafficColumnService, State: trafficOK}
	if object.Spec.Type == "ExternalName" {
		node.Detail = []string{"ExternalName → " + object.Spec.ExternalName}
		r.addNode(node)
		return &object
	}
	node.Detail = []string{cmpOr(object.Spec.Type, "ClusterIP") + " · " + cmpOr(object.portSummary(), "no ports")}
	r.addNode(node)
	r.followService(id, object)
	return &object
}

func (r *trafficResolver) followService(id string, object serviceObject) {
	namespace, name := object.Metadata.Namespace, object.Metadata.Name
	ready, notReady, endpointsRead := r.endpoints(namespace, name)
	if endpointsRead {
		node := r.node(id)
		node.Detail = append(node.Detail, fmt.Sprintf("%d of %d endpoints ready", ready, ready+notReady))
	}

	if len(object.Spec.Selector) == 0 {
		r.mark(id, trafficWarn, "no selector — its endpoints are written by hand, not by Kubernetes")
		return
	}
	selector, err := encodeLabelSelector(labelSelector{MatchLabels: object.Spec.Selector})
	if err != nil {
		r.mark(id, trafficWarn, err.Error())
		return
	}
	pods, truncated, failure := r.pods(namespace, selector)
	if failure != nil {
		node := r.node(id)
		node.Detail = append(node.Detail, "pods not read: "+failure.problem)
		return
	}
	if len(pods) == 0 {
		r.mark(id, trafficBad, "its selector "+selector+" matches no pods")
		return
	}
	if truncated {
		r.note(fmt.Sprintf("Service %s selects more than %d pods; the map is drawn from the first %d.",
			name, trafficListLimit, trafficListLimit))
	}
	if endpointsRead {
		switch {
		case ready == 0:
			r.mark(id, trafficBad, "no endpoint is ready — none of the pods it selects passes its readiness probe")
		case notReady > 0:
			r.mark(id, trafficWarn, fmt.Sprintf("%d of %d endpoints are not ready", notReady, ready+notReady))
		}
	}
	r.addPods(id, namespace, pods)
}

// endpoints counts a Service's endpoints by readiness, from its EndpointSlices.
// An endpoint with no ready condition is ready — that is what the API says an
// absent condition means.
func (r *trafficResolver) endpoints(namespace, name string) (ready, notReady int, ok bool) {
	items, _, failure := r.list(namespace,
		[]resourceListPath{{"/apis/discovery.k8s.io/v1", "endpointslices"}},
		"kubernetes.io/service-name="+name)
	if failure != nil {
		return 0, 0, false
	}
	for _, raw := range items {
		var slice struct {
			Endpoints []struct {
				Conditions struct {
					Ready *bool `json:"ready"`
				} `json:"conditions"`
			} `json:"endpoints"`
		}
		if json.Unmarshal(raw, &slice) != nil {
			continue
		}
		for _, endpoint := range slice.Endpoints {
			if deref(endpoint.Conditions.Ready, true) {
				ready++
			} else {
				notReady++
			}
		}
	}
	return ready, notReady, true
}

/* ----------------------------------------------------- workloads, pods --- */

type ownerRef struct {
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Controller *bool  `json:"controller"`
}

type trafficPod struct {
	view  podView
	owner ownerRef
}

func controllerOf(refs []ownerRef) ownerRef {
	for _, ref := range refs {
		if deref(ref.Controller, false) {
			return ref
		}
	}
	return ownerRef{}
}

func (r *trafficResolver) pods(namespace, selector string) ([]trafficPod, bool, *readFailure) {
	items, truncated, failure := r.list(namespace, []resourceListPath{{"/api/v1", "pods"}}, selector)
	if failure != nil {
		return nil, false, failure
	}
	pods := make([]trafficPod, 0, len(items))
	for _, raw := range items {
		var object podObject
		var meta struct {
			Metadata struct {
				OwnerReferences []ownerRef `json:"ownerReferences"`
			} `json:"metadata"`
		}
		if json.Unmarshal(raw, &object) != nil || json.Unmarshal(raw, &meta) != nil {
			continue
		}
		pods = append(pods, trafficPod{view: object.view(), owner: controllerOf(meta.Metadata.OwnerReferences)})
	}
	return pods, truncated, nil
}

func podReady(pod podView) bool {
	return pod.Phase == "Running" && pod.Total > 0 && pod.Ready == pod.Total
}

// podState says what is wrong with a pod in the words its containers use —
// CrashLoopBackOff, ImagePullBackOff — rather than a generic "not ready".
func podState(pod podView) (string, string) {
	if podReady(pod) {
		return trafficOK, ""
	}
	for _, container := range pod.Containers {
		if !container.Ready && container.State != "running" && container.State != "" {
			if pod.Phase == "Pending" && (container.State == "pending" || container.State == "ContainerCreating") {
				return trafficWarn, "pending"
			}
			return trafficBad, container.State
		}
	}
	if pod.Phase == "Pending" {
		return trafficWarn, "pending"
	}
	return trafficBad, "not ready"
}

// workloadFor resolves a pod's controller to the workload an operator thinks
// of: a ReplicaSet's Deployment rather than the ReplicaSet. Empty for a pod
// nothing controls.
func (r *trafficResolver) workloadFor(namespace string, owner ownerRef) string {
	if owner.Kind == "" {
		return ""
	}
	key := namespace + "/" + owner.Kind + "/" + owner.Name
	if id, seen := r.owners[key]; seen {
		return id
	}

	kind, resource, name := owner.Kind, workloadResource(owner.Kind), owner.Name
	if owner.Kind == "ReplicaSet" {
		var replicaSet struct {
			Metadata struct {
				OwnerReferences []ownerRef `json:"ownerReferences"`
			} `json:"metadata"`
		}
		if r.object(namespace, objectKinds["replicasets"].versions, owner.Name, &replicaSet) == nil {
			if parent := controllerOf(replicaSet.Metadata.OwnerReferences); parent.Kind != "" {
				kind, resource, name = parent.Kind, workloadResource(parent.Kind), parent.Name
			}
		}
	}

	id := trafficID(cmpOr(resource, strings.ToLower(kind)), namespace, name)
	r.addNode(trafficNode{ID: id, Kind: kind, Resource: resource, Namespace: namespace, Name: name,
		Column: trafficColumnWorkload, State: trafficOK})
	r.owners[key] = id
	return id
}

// workloadResource is the inventory key for a controller kind, empty for one
// the console has no list of (an Argo Rollout, say), which is drawn unopenable
// rather than guessed at.
func workloadResource(kind string) string {
	switch kind {
	case "Deployment":
		return "deployments"
	case "StatefulSet":
		return "statefulsets"
	case "DaemonSet":
		return "daemonsets"
	case "ReplicaSet":
		return "replicasets"
	case "Job":
		return "jobs"
	}
	return ""
}

func (r *trafficResolver) addPods(serviceID, namespace string, pods []trafficPod) {
	var parents []string
	groups := map[string][]podView{}
	for _, pod := range pods {
		parent := r.workloadFor(namespace, pod.owner)
		if parent == "" {
			parent = serviceID
		}
		if _, seen := groups[parent]; !seen {
			parents = append(parents, parent)
		}
		groups[parent] = append(groups[parent], pod.view)
	}

	for _, parent := range parents {
		members := groups[parent]
		// Unready first: the pods that explain a failure must never be the
		// ones folded into "+N more".
		slices.SortStableFunc(members, func(a, b podView) int {
			if podReady(a) != podReady(b) {
				if podReady(a) {
					return 1
				}
				return -1
			}
			return strings.Compare(a.Name, b.Name)
		})

		ready := 0
		for _, pod := range members {
			if podReady(pod) {
				ready++
			}
		}
		if parent != serviceID {
			node := r.node(parent)
			node.Detail = []string{fmt.Sprintf("%d of %d pods ready", ready, len(members))}
			switch {
			case ready == 0:
				r.mark(parent, trafficBad, "none of its pods behind this Service is ready")
			case ready < len(members):
				r.mark(parent, trafficWarn, fmt.Sprintf("%d of %d pods are not ready", len(members)-ready, len(members)))
			}
			r.addEdge(serviceID, parent, "", trafficOK, "")
		}

		for i, pod := range members {
			if i == maxTrafficPodsPerWorkload {
				rest := members[i:]
				state := trafficOK
				for _, other := range rest {
					if !podReady(other) {
						state = trafficWarn
					}
				}
				moreID := r.addNode(trafficNode{ID: "more/" + parent, Kind: "More",
					Name: fmt.Sprintf("+%d more", len(rest)), Column: trafficColumnPod, State: state,
					Detail: []string{fmt.Sprintf("%d more pods", len(rest))}})
				r.addEdge(parent, moreID, "", trafficOK, "")
				break
			}
			state, problem := podState(pod)
			row := pod
			podID := r.addNode(trafficNode{ID: trafficID("pods", namespace, pod.Name), Kind: "Pod",
				Resource: "pods", Namespace: namespace, Name: pod.Name, Column: trafficColumnPod,
				State: state, Problem: problem, Pod: &row,
				Detail: []string{fmt.Sprintf("%d/%d ready", pod.Ready, pod.Total)}})
			r.addEdge(parent, podID, "", trafficOK, "")
		}
	}
}

/* ------------------------------------------------------------- reverse --- */

// resolveServiceRoot draws a Service's map the other way round: the routes in
// its namespace that send to it, then the Service forward to its pods.
func (r *trafficResolver) resolveServiceRoot(namespace, name string) *readFailure {
	id := trafficID("services", namespace, name)
	var object serviceObject
	if failure := r.object(namespace, objectKinds["services"].versions, name, &object); failure != nil {
		return failure
	}
	r.out.Root = id
	r.followed++
	r.drawService(object)

	found := 0
	collect := func(versions []resourceListPath, kind string, add func(json.RawMessage) bool) {
		items, truncated, failure := r.list(namespace, versions, "")
		if failure != nil {
			// An optional CRD that is not installed is not something to report.
			if failure.status != http.StatusNotFound {
				r.note(fmt.Sprintf("%s in %s could not be read: %s", kind, namespace, failure.problem))
			}
			return
		}
		if truncated {
			r.note(fmt.Sprintf("Only the first %d %s in %s were searched.", trafficListLimit, kind, namespace))
		}
		for _, raw := range items {
			if add(raw) {
				found++
			}
		}
	}

	collect(objectKinds["ingresses"].versions, "Ingresses", func(raw json.RawMessage) bool {
		var object ingressObject
		if json.Unmarshal(raw, &object) != nil || !object.sendsTo(id) {
			return false
		}
		r.addIngress(object, id)
		return true
	})
	collect(objectKinds["httproutes"].versions, "HTTPRoutes", func(raw json.RawMessage) bool {
		var object httpRouteObject
		if json.Unmarshal(raw, &object) != nil || !object.sendsTo(namespace, name) {
			return false
		}
		r.addHTTPRoute(object, id)
		return true
	})
	collect(objectKinds["virtualservices"].versions, "VirtualServices", func(raw json.RawMessage) bool {
		var object virtualServiceObject
		if json.Unmarshal(raw, &object) != nil || !object.sendsTo(namespace, name) {
			return false
		}
		r.addVirtualService(object, id)
		return true
	})

	if found == 0 {
		r.note(fmt.Sprintf("No Ingress, HTTPRoute or VirtualService in %s sends traffic to this Service.", namespace))
	}
	r.note(fmt.Sprintf("Only routes in %s are searched — an HTTPRoute or VirtualService in another namespace that sends here is not shown.", namespace))
	return nil
}

func (o ingressObject) sendsTo(serviceID string) bool {
	namespace := o.Metadata.Namespace
	if backend := o.Spec.DefaultBackend; backend != nil && backend.sendsTo(namespace, serviceID) {
		return true
	}
	for _, rule := range o.Spec.Rules {
		if rule.HTTP == nil {
			continue
		}
		for _, path := range rule.HTTP.Paths {
			if path.Backend.sendsTo(namespace, serviceID) {
				return true
			}
		}
	}
	return false
}
