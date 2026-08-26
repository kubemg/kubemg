package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/gin-gonic/gin"
	"helm.sh/helm/v3/pkg/chartutil"

	"github.com/kubemg/kubemg/backend/pkg/bastion"
	"github.com/kubemg/kubemg/backend/pkg/db"
	"github.com/kubemg/kubemg/backend/pkg/helm"
)

/*
 * Writing a rendered chart into a cluster.
 *
 * Everything in this file is the answer to one question: what does KubeMG do
 * that `helm install` would have done, and what does it deliberately not do.
 *
 * **The objects go down the same tunnel as everything else, one at a time.**
 * There is no batch route and no privileged path. Each object is a `get` and
 * then a `create` or an `update`, impersonated as the caller, decided by the
 * target cluster's own RBAC, and each earns its own audit record — so an
 * install of a forty-object chart is forty rows naming forty objects, which is
 * what makes "who installed cert-manager here" a question the audit trail
 * answers. It also means a `view` grant cannot install anything, without a line
 * of code here saying so.
 *
 * **The manifest editor's deny list does not apply.** `notCreatable` refuses
 * four RBAC kinds and Node from a hand-typed manifest, on the argument that
 * KubeMG should not be how a cluster's RBAC gets authored. A chart is the
 * opposite case: cert-manager, ingress-nginx and every operator worth installing
 * ship a ServiceAccount, a ClusterRole and a binding, and refusing those would
 * leave the install button able to install nothing anyone wants. What decides
 * here is what decides for `kubectl apply`: the cluster's RBAC, answering an
 * impersonated request.
 *
 * **A namespace-scoped grant cannot install a chart that reaches outside it.**
 * This is checked *before* the first write rather than discovered on object
 * nineteen — a refused install must leave nothing behind, and a scoped
 * developer installing a chart with a ClusterRole in it has to be told that up
 * front. Cluster-scoped objects are refused outright for a scoped grant, the
 * same rule `requireClusterScope` applies to a list.
 *
 * **A failure stops the run.** Helm's `--atomic` rolls back; there is no
 * rollback here, because rolling back a partial install means deleting objects
 * this run created and that is a destructive act taken on a caller's behalf
 * without their asking. So the run stops at the first refusal, the release is
 * recorded as `failed`, and the report says exactly which objects were written
 * and which were not — which is the state `helm install` without `--atomic`
 * leaves too, and is the one an operator can act on.
 */

// applyAction is what happened to one object.
const (
	actionCreated    = "created"
	actionConfigured = "configured"
	actionUnchanged  = "unchanged"
	actionDeleted    = "deleted"
	actionFailed     = "failed"
	actionSkipped    = "skipped"
)

// objectReport is one line of the report an install answers with. It names the
// object in the cluster's own vocabulary and, when something refused it, in the
// cluster's own words.
type objectReport struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
	Source    string `json:"source,omitempty"`
	Action    string `json:"action"`
	Hook      bool   `json:"hook,omitempty"`
	Message   string `json:"message,omitempty"`
}

// apiResource is what discovery says about one kind: the plural the API server
// serves it under, and whether it lives in a namespace.
type apiResource struct {
	Plural     string
	Namespaced bool
}

/* ------------------------------------------------------------- discovery --- */

// clusterDiscovery is one read of what the target cluster serves, used twice.
//
// It is one pass rather than two because the answer to both questions is in the
// same document. Rendering needs `.Capabilities.APIVersions`, which charts use
// to decide whether to emit an Ingress or an OpenShift Route and which is a set
// of `group/version` and `group/version/Kind` strings. Writing needs the plural
// resource name for each kind, because an object carries its Kind and an API
// path carries the plural, and nothing but the API server knows the mapping —
// `Ingress` is `ingresses` and `Endpoints` is `endpoints`, and no pluralisation
// rule gets both right.
//
// It costs one call per served group-version, which on an ordinary cluster is
// around thirty small reads. They are impersonated and audited like everything
// else, which is the right trade: discovery is what the `helm` CLI does on every
// invocation too, and the alternative — guessing plurals — is wrong on exactly
// the kinds an operator cannot debug.
type clusterDiscovery struct {
	capabilities *chartutil.Capabilities
	// resources is groupVersion -> Kind -> resource.
	resources map[string]map[string]apiResource
}

// resolve finds how one kind is addressed.
func (d *clusterDiscovery) resolve(apiVersion, kind string) (apiResource, error) {
	kinds, ok := d.resources[apiVersion]
	if !ok {
		return apiResource{}, fmt.Errorf("this cluster does not serve %s", apiVersion)
	}
	resource, ok := kinds[kind]
	if !ok {
		return apiResource{}, fmt.Errorf("this cluster does not serve %s in %s — "+
			"if the chart installs a CRD and an instance of it, the definition has to be "+
			"established before the instance can be written", kind, apiVersion)
	}
	return resource, nil
}

// discoverCluster reads what the cluster serves.
func (s *server) discoverCluster(c *gin.Context, user *db.User, cluster *db.Cluster,
	grant db.UserClusterAccess,
) (*clusterDiscovery, bool) {
	version, ok := s.clusterKubeVersion(c, user, cluster, grant)
	if !ok {
		return nil, false
	}

	groupVersions, ok := s.clusterGroupVersions(c, user, cluster, grant)
	if !ok {
		return nil, false
	}

	discovery := &clusterDiscovery{resources: make(map[string]map[string]apiResource, len(groupVersions))}
	versions := make([]string, 0, len(groupVersions)*8)

	for _, groupVersion := range groupVersions {
		kinds, ok := s.clusterResources(c, user, cluster, grant, groupVersion)
		if !ok {
			// A group-version the caller may not read, or one whose aggregated
			// API server is down, is not a failed install: the chart probably
			// does not use it, and if it does the write will say so by name.
			// A whole discovery that fails is a different matter and is caught
			// by the two reads above.
			continue
		}
		discovery.resources[groupVersion] = kinds
		versions = append(versions, groupVersion)
		for kind := range kinds {
			versions = append(versions, groupVersion+"/"+kind)
		}
	}

	discovery.capabilities = &chartutil.Capabilities{
		KubeVersion: *version,
		APIVersions: chartutil.VersionSet(versions),
		HelmVersion: chartutil.DefaultCapabilities.HelmVersion,
	}
	return discovery, true
}

// clusterKubeVersion reads `/version`, which is what `.Capabilities.KubeVersion`
// reports and what a chart's `kubeVersion` constraint is checked against.
func (s *server) clusterKubeVersion(c *gin.Context, user *db.User, cluster *db.Cluster,
	grant db.UserClusterAccess,
) (*chartutil.KubeVersion, bool) {
	resp, ok := s.callResourceWith(c, user, cluster, grant,
		http.MethodGet, "/version", nil, "the cluster's version could not be read")
	if !ok {
		return nil, false
	}
	if resp.Status < 200 || resp.Status >= 300 {
		c.JSON(resp.Status, gin.H{"error": kubeErrorMessage(resp.Body, resp.Status)})
		return nil, false
	}

	var reported struct {
		Major      string `json:"major"`
		Minor      string `json:"minor"`
		GitVersion string `json:"gitVersion"`
	}
	if err := json.Unmarshal(resp.Body, &reported); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "the cluster's version could not be read"})
		return nil, false
	}

	version, err := helm.KubeVersionOf(reported.Major, reported.Minor, reported.GitVersion)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return nil, false
	}
	return version, true
}

// clusterGroupVersions lists every group-version the cluster serves: the core
// one, which `/apis` does not include, and the rest.
func (s *server) clusterGroupVersions(c *gin.Context, user *db.User, cluster *db.Cluster,
	grant db.UserClusterAccess,
) ([]string, bool) {
	resp, ok := s.callResourceWith(c, user, cluster, grant,
		http.MethodGet, "/apis", nil, "the cluster's API list could not be read")
	if !ok {
		return nil, false
	}
	if resp.Status < 200 || resp.Status >= 300 {
		c.JSON(resp.Status, gin.H{"error": kubeErrorMessage(resp.Body, resp.Status)})
		return nil, false
	}

	var groups struct {
		Groups []struct {
			Versions []struct {
				GroupVersion string `json:"groupVersion"`
			} `json:"versions"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(resp.Body, &groups); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "the cluster's API list could not be read"})
		return nil, false
	}

	// The core group is served at /api/v1 and is absent from /apis. A chart that
	// renders a ConfigMap — which is to say every chart — needs it.
	versions := []string{"v1"}
	for _, group := range groups.Groups {
		for _, version := range group.Versions {
			if version.GroupVersion != "" && !slices.Contains(versions, version.GroupVersion) {
				versions = append(versions, version.GroupVersion)
			}
		}
	}
	return versions, true
}

// clusterResources reads one group-version's resource list.
func (s *server) clusterResources(c *gin.Context, user *db.User, cluster *db.Cluster,
	grant db.UserClusterAccess, groupVersion string,
) (map[string]apiResource, bool) {
	resp, err := s.proxy.Call(c.Request.Context(), user, cluster, grant,
		http.MethodGet, discoveryPath(groupVersion), nil, nil)
	if err != nil || resp.Status < 200 || resp.Status >= 300 {
		return nil, false
	}

	var list struct {
		Resources []struct {
			Name       string `json:"name"`
			Kind       string `json:"kind"`
			Namespaced bool   `json:"namespaced"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(resp.Body, &list); err != nil {
		return nil, false
	}

	kinds := make(map[string]apiResource, len(list.Resources))
	for _, resource := range list.Resources {
		// Subresources arrive in the same list as `deployments/scale`. They are
		// not addressable as objects and would shadow the kind they belong to.
		if strings.Contains(resource.Name, "/") || resource.Kind == "" {
			continue
		}
		if _, taken := kinds[resource.Kind]; taken {
			continue
		}
		kinds[resource.Kind] = apiResource{Plural: resource.Name, Namespaced: resource.Namespaced}
	}
	return kinds, true
}

// discoveryPath is where a group-version publishes what it serves.
func discoveryPath(groupVersion string) string {
	if !strings.Contains(groupVersion, "/") {
		return "/api/" + groupVersion
	}
	return "/apis/" + groupVersion
}

/* ------------------------------------------------------------ addressing --- */

// objectPath renders where one object lives, and objectCollection where its kind
// is created. They are built from validated components rather than assembled
// from anything a caller typed: the group-version and kind came from the
// cluster's own discovery, the plural from the same document, and the name and
// namespace are path-escaped.
func objectCollection(apiVersion string, resource apiResource, namespace string) string {
	root := "/apis/" + apiVersion
	if !strings.Contains(apiVersion, "/") {
		root = "/api/" + apiVersion
	}
	if resource.Namespaced {
		return fmt.Sprintf("%s/namespaces/%s/%s", root, url.PathEscape(namespace), resource.Plural)
	}
	return root + "/" + resource.Plural
}

func objectPath(apiVersion string, resource apiResource, namespace, name string) string {
	return objectCollection(apiVersion, resource, namespace) + "/" + url.PathEscape(name)
}

/* ----------------------------------------------------------------- plan --- */

// applyPlan is a rendered release checked against the caller's grant, before
// anything has been written.
type applyPlan struct {
	discovery *clusterDiscovery
	// objects is what will be written, in order: CRDs, pre-install hooks, the
	// release proper, post-install hooks.
	objects []helm.Object
	// removals is what the previous revision wrote and this one does not. Empty
	// for an install.
	removals []helm.Object
	// release and namespace are what the ownership metadata names. They are on
	// the plan rather than threaded through every call because every object in
	// one run belongs to the same release, by definition.
	release   string
	namespace string
}

// planApply resolves every object against discovery and the grant, and refuses
// the whole run rather than writing a prefix of it.
//
// The two refusals are the ones a scoped grant earns, and they are deliberately
// pre-flight. A chart is not a manifest an operator wrote — they may have no
// idea it contains a ClusterRole until it is refused — so being told before the
// first write, with the object named, is the difference between a message they
// can act on and a half-installed release.
func (s *server) planApply(c *gin.Context, discovery *clusterDiscovery,
	grant db.UserClusterAccess, rendered *helm.Rendered, previous []helm.Object,
	release, namespace string,
) (*applyPlan, bool) {
	plan := &applyPlan{discovery: discovery, release: release, namespace: namespace}
	plan.objects = append(plan.objects, rendered.CRDs...)
	plan.objects = append(plan.objects, rendered.PreInstall...)
	plan.objects = append(plan.objects, rendered.Objects...)
	plan.objects = append(plan.objects, rendered.PostInstall...)

	allowed := grant.NamespaceList()
	for _, object := range plan.objects {
		resource, err := discovery.resolve(object.APIVersion, object.Kind)
		if err != nil {
			// A CRD's own instances are the ordinary case: the chart installs
			// the definition in this same run, so the kind is not served yet.
			// It is still refused rather than guessed at, because writing to a
			// path this invented would be a 404 the operator cannot read.
			c.JSON(http.StatusConflict, gin.H{
				"error": fmt.Sprintf("%s (%s) cannot be written: %s", object.Ref(), object.Source, err),
			})
			return nil, false
		}
		if len(allowed) == 0 {
			continue
		}
		if !resource.Namespaced {
			c.JSON(http.StatusForbidden, gin.H{
				"error": fmt.Sprintf("this chart installs %s, which is cluster-scoped, and your "+
					"access to this cluster is limited to %s", object.Ref(), strings.Join(allowed, ", ")),
			})
			return nil, false
		}
		if !slices.Contains(allowed, object.Namespace) {
			c.JSON(http.StatusForbidden, gin.H{
				"error": fmt.Sprintf("this chart installs %s into namespace %s, which is outside "+
					"your granted scope", object.Ref(), object.Namespace),
			})
			return nil, false
		}
	}

	plan.removals = removalsOf(previous, plan.objects)
	return plan, true
}

// removalsOf is what the previous revision wrote and this one does not.
//
// This is the one thing a release's recorded manifest exists for that a re-render
// cannot supply. A template deleted from a chart, or switched off by a value,
// leaves an object running that nothing owns any more — and only the *previous*
// render knows it was ever Helm's to remove. Removals are deliberately reversed
// so dependants go before what they depend on.
func removalsOf(previous, wanted []helm.Object) []helm.Object {
	if len(previous) == 0 {
		return nil
	}
	keep := make(map[string]struct{}, len(wanted))
	for _, object := range wanted {
		keep[object.APIVersion+"|"+object.Ref()] = struct{}{}
	}

	removals := make([]helm.Object, 0, 4)
	for _, object := range previous {
		if _, ok := keep[object.APIVersion+"|"+object.Ref()]; !ok {
			removals = append(removals, object)
		}
	}
	slices.Reverse(removals)
	return removals
}

/* ---------------------------------------------------------------- apply --- */

// applyResult is what a run did.
type applyResult struct {
	Reports []objectReport
	// Failed is the object the run stopped at, if it stopped.
	Failed *objectReport
}

// ok reports whether every object was written.
func (r applyResult) ok() bool { return r.Failed == nil }

// apply writes a plan, in order, stopping at the first refusal.
//
// `original` maps an object to what the previous revision rendered for it, which
// is the third document of the three-way merge. An object with no entry — a new
// one, or one adopted from outside Helm — merges two-way, which removes nothing
// it did not put there.
func (s *server) apply(c *gin.Context, user *db.User, cluster *db.Cluster,
	grant db.UserClusterAccess, plan *applyPlan, original map[string][]byte,
) applyResult {
	result := applyResult{Reports: make([]objectReport, 0, len(plan.objects)+len(plan.removals))}

	for _, object := range plan.objects {
		report := s.applyObject(c, user, cluster, grant, plan.discovery, object,
			original[object.APIVersion+"|"+object.Ref()], plan.release, plan.namespace)
		result.Reports = append(result.Reports, report)

		if report.Action == actionFailed {
			failed := report
			result.Failed = &failed
			// Everything after the failure is reported as untouched, so the
			// report is a complete account of the chart rather than a list that
			// stops. An operator reading it can see what did not get written.
			for _, remaining := range plan.objects[len(result.Reports):] {
				result.Reports = append(result.Reports, objectReport{
					Kind: remaining.Kind, Name: remaining.Name, Namespace: remaining.Namespace,
					Source: remaining.Source, Hook: remaining.Hook, Action: actionSkipped,
					Message: "not written — the run stopped at " + report.Kind + "/" + report.Name,
				})
			}
			return result
		}
	}

	// Removals last, and never fatal. An object that will not delete leaves a
	// release that is otherwise correct, and failing the upgrade over it would
	// be worse than reporting it.
	for _, object := range plan.removals {
		result.Reports = append(result.Reports,
			s.removeObject(c, user, cluster, grant, plan.discovery, object))
	}
	return result
}

// applyObject writes one object: create if it is not there, three-way merge if
// it is.
func (s *server) applyObject(c *gin.Context, user *db.User, cluster *db.Cluster,
	grant db.UserClusterAccess, discovery *clusterDiscovery, object helm.Object,
	original []byte, release, releaseNamespace string,
) objectReport {
	report := objectReport{
		Kind: object.Kind, Name: object.Name, Namespace: object.Namespace,
		Source: object.Source, Hook: object.Hook,
	}

	resource, err := discovery.resolve(object.APIVersion, object.Kind)
	if err != nil {
		report.Action, report.Message = actionFailed, err.Error()
		return report
	}
	namespace := object.Namespace
	if !resource.Namespaced {
		// The render gives every object the release's namespace, because it does
		// not know which kinds are cluster-scoped — only discovery does. Sending
		// one on a cluster-scoped object is not harmless: the API server checks
		// it against the path and refuses the pair. So it is stripped from the
		// document as well as from the report.
		namespace = ""
		report.Namespace = ""
		object.Document = withoutNamespace(object.Document)
	}

	// Helm's ownership metadata, stamped on the way to the API server and not
	// into the recorded manifest — `setMetadataVisitor`'s placement, and for its
	// reasons. See helm.WithOwnership.
	if !object.Hook && !object.CRD {
		object.Document = helm.WithOwnership(object.Document, release, releaseNamespace)
	}

	live, status, err := s.readRenderedObject(c, user, cluster, grant, object.APIVersion, resource, namespace, object.Name)
	if err != nil {
		report.Action, report.Message = actionFailed, err.Error()
		return report
	}

	if status == http.StatusNotFound {
		return s.createObject(c, user, cluster, grant, object, resource, namespace, report)
	}

	merged, err := helm.ThreeWayMerge(original, object.Document, live)
	if err != nil {
		report.Action, report.Message = actionFailed, err.Error()
		return report
	}
	if json.Valid(merged) && string(merged) == string(live) {
		// Nothing to send. Worth its own word rather than reporting a write:
		// `configured` on forty unchanged objects tells an operator their
		// upgrade did something when it did not.
		report.Action = actionUnchanged
		return report
	}

	path := objectPath(object.APIVersion, resource, namespace, object.Name)
	resp, err := s.proxy.Call(c.Request.Context(), user, cluster, grant, http.MethodPut, path, merged, nil)
	if err != nil {
		report.Action, report.Message = actionFailed, callFailureMessage(err)
		return report
	}
	if resp.Status < 200 || resp.Status >= 300 {
		report.Action, report.Message = actionFailed, kubeErrorMessage(resp.Body, resp.Status)
		return report
	}
	report.Action = actionConfigured
	return report
}

// createObject POSTs an object that is not there yet.
func (s *server) createObject(c *gin.Context, user *db.User, cluster *db.Cluster,
	grant db.UserClusterAccess, object helm.Object, resource apiResource,
	namespace string, report objectReport,
) objectReport {
	path := objectCollection(object.APIVersion, resource, namespace)
	resp, err := s.proxy.Call(c.Request.Context(), user, cluster, grant,
		http.MethodPost, path, object.Document, nil)
	if err != nil {
		report.Action, report.Message = actionFailed, callFailureMessage(err)
		return report
	}
	if resp.Status == http.StatusConflict {
		// Created between the read and the write. Rare, and reported as its own
		// thing rather than as a generic failure, because the fix is to run the
		// same operation again.
		report.Action = actionFailed
		report.Message = "something else created this object while the release was being written"
		return report
	}
	if resp.Status < 200 || resp.Status >= 300 {
		report.Action, report.Message = actionFailed, kubeErrorMessage(resp.Body, resp.Status)
		return report
	}
	report.Action = actionCreated
	return report
}

// removeObject deletes an object the previous revision owned and this one does
// not. `Background` propagation, and the same word the delete route uses: a
// DELETE marks an object, it does not remove it.
func (s *server) removeObject(c *gin.Context, user *db.User, cluster *db.Cluster,
	grant db.UserClusterAccess, discovery *clusterDiscovery, object helm.Object,
) objectReport {
	report := objectReport{
		Kind: object.Kind, Name: object.Name, Namespace: object.Namespace, Source: object.Source,
	}

	resource, err := discovery.resolve(object.APIVersion, object.Kind)
	if err != nil {
		report.Action, report.Message = actionSkipped, err.Error()
		return report
	}
	namespace := object.Namespace
	if !resource.Namespaced {
		namespace = ""
		report.Namespace = ""
	}

	path := objectPath(object.APIVersion, resource, namespace, object.Name) +
		"?propagationPolicy=Background"
	resp, err := s.proxy.Call(c.Request.Context(), user, cluster, grant, http.MethodDelete, path, nil, nil)
	switch {
	case err != nil:
		report.Action, report.Message = actionSkipped, callFailureMessage(err)
	case resp.Status == http.StatusNotFound:
		// Already gone. The desired state, reached by somebody else.
		report.Action = actionDeleted
	case resp.Status < 200 || resp.Status >= 300:
		report.Action, report.Message = actionSkipped, kubeErrorMessage(resp.Body, resp.Status)
	default:
		report.Action = actionDeleted
	}
	return report
}

// readRenderedObject fetches an object's current state, distinguishing "not there" from
// "could not be read". The difference decides between a create and a merge, and
// treating an unreadable object as absent would turn a permissions problem into
// an attempted create that fails for a second, more confusing reason.
func (s *server) readRenderedObject(c *gin.Context, user *db.User, cluster *db.Cluster,
	grant db.UserClusterAccess, apiVersion string, resource apiResource,
	namespace, name string,
) ([]byte, int, error) {
	path := objectPath(apiVersion, resource, namespace, name)
	resp, err := s.proxy.Call(c.Request.Context(), user, cluster, grant, http.MethodGet, path, nil, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("%s", callFailureMessage(err))
	}
	switch {
	case resp.Status == http.StatusNotFound:
		return nil, http.StatusNotFound, nil
	case resp.Status < 200 || resp.Status >= 300:
		return nil, resp.Status, fmt.Errorf("%s", kubeErrorMessage(resp.Body, resp.Status))
	}
	return resp.Body, resp.Status, nil
}

// originalOf indexes a previous revision's objects by the key `apply` looks them
// up under.
func originalOf(previous []helm.Object) map[string][]byte {
	index := make(map[string][]byte, len(previous))
	for _, object := range previous {
		index[object.APIVersion+"|"+object.Ref()] = object.Document
	}
	return index
}

// callFailureMessage turns a tunnel failure into the sentence a report line
// carries. A guardrail refusal names the policy that fired, for the reason the
// object route surfaces it: a bare "refused" leaves an operator unable to tell
// the cluster's RBAC from KubeMG's own rules, and only one of those is theirs
// to change.
func callFailureMessage(err error) string {
	var callErr *bastion.CallError
	if errors.As(err, &callErr) {
		if callErr.Policy != "" {
			return callErr.Message + " (blocked by the " + callErr.Policy + " policy)"
		}
		return callErr.Message
	}
	return "the cluster could not be reached"
}

// withoutNamespace removes `metadata.namespace` from a rendered object. A
// document that cannot be re-encoded is returned unchanged rather than dropped:
// the write then fails with the cluster's own message, which is a better
// outcome than an object silently missing from a release.
func withoutNamespace(document []byte) []byte {
	var parsed map[string]any
	if err := json.Unmarshal(document, &parsed); err != nil {
		return document
	}
	metadata, ok := parsed["metadata"].(map[string]any)
	if !ok {
		return document
	}
	if _, present := metadata["namespace"]; !present {
		return document
	}
	delete(metadata, "namespace")

	stripped, err := json.Marshal(parsed)
	if err != nil {
		return document
	}
	return stripped
}
