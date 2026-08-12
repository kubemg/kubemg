package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/bastion"
)

/*
 * The target cluster's own RBAC, read.
 *
 * KubeMG's permissions matrix governs *KubeMG's* authorization — who may open
 * which cluster, in which namespaces, with which role. What that grant is
 * actually worth is decided somewhere else entirely: by the cluster, through
 * impersonation, against bindings KubeMG did not write and does not own. Until
 * now the console could not show any of it, which made "the cluster decides" a
 * claim rather than something an operator could check.
 *
 * These reads are the inventory half of closing that: Roles, ClusterRoles,
 * RoleBindings, ClusterRoleBindings and ServiceAccounts as Explore resources of
 * their own, down the same impersonated tunnel as every other list. The binding
 * tables resolve subject → role rather than printing a ref, because a binding
 * printed as its own fields ("roleRef: ClusterRole/edit") is a lookup an
 * operator then has to do by hand across two other lists.
 *
 * It is **read-only in both directions**, and that is a design decision rather
 * than an unfinished one. KubeMG does not create Roles or Bindings here: writing
 * a cluster's RBAC from a tool whose own permission model is separate is exactly
 * how the two silently diverge, and the manifest editor already applies a Role
 * for anyone whose grant permits it. So there is no route here that is not a GET
 * — except the access review, which is a `create` in RBAC's eyes while being a
 * question in every other sense (see accessReview below).
 */

// maxRuleSummary bounds how many policy rules one Role row carries. A Role can
// hold hundreds — an aggregated ClusterRole is built out of them — and the row
// shows what a Role is *for*, not the whole policy; the YAML tab is the complete
// view and the count says how much of it was left there.
const maxRuleSummary = 12

// maxSubjects bounds the subjects one binding row carries, on the same rule. A
// binding with two hundred subjects is a group mapping, and the count is the
// interesting part of it rather than the list.
const maxSubjects = 20

/* ----------------------------------------------------------------- views --- */

// policyRuleView is one rule of a Role or ClusterRole, rendered as the three
// axes RBAC actually has. Every field is a list because every field is one in
// the API, and an empty verbs list is a real (and useless) rule rather than
// something to hide.
type policyRuleView struct {
	Verbs     []string `json:"verbs"`
	APIGroups []string `json:"api_groups,omitempty"`
	Resources []string `json:"resources,omitempty"`
	// ResourceNames narrows a rule to named objects. It is the difference
	// between "may delete pods" and "may delete one pod", so it is never folded
	// away even though it is usually empty.
	ResourceNames []string `json:"resource_names,omitempty"`
	// NonResourceURLs is the other kind of rule entirely — `/healthz`, `/metrics`
	// — and only a ClusterRole can carry one.
	NonResourceURLs []string `json:"non_resource_urls,omitempty"`
}

// roleView is a Role or a ClusterRole. Both are one type here because they are
// one type in every way that matters to a reader: the same rules, differing only
// in whether a namespace bounds them.
type roleView struct {
	listMeta
	// Rules is a bounded prefix of the policy; RuleCount is how many there are.
	Rules     []policyRuleView `json:"rules"`
	RuleCount int              `json:"rule_count"`
	// Verbs and Resources are the union across every rule — what a row can show
	// in one line, and what somebody scanning a list of forty roles is reading.
	Verbs     []string `json:"verbs"`
	Resources []string `json:"resources"`
	// Aggregated marks a ClusterRole whose rules are assembled by the controller
	// from other ClusterRoles' labels. Its rules are an output, so editing them
	// achieves nothing and the row should say why.
	Aggregated bool `json:"aggregated,omitempty"`
	// Builtin marks one of Kubernetes' own roles (`kubernetes.io/bootstrapping`),
	// which is most of what a fresh cluster's ClusterRole list is made of.
	Builtin bool `json:"builtin,omitempty"`
	// Wildcard marks a role holding a rule that grants everything on something —
	// `*` verbs, or `*` resources. It is the one property of a Role worth a
	// colour in a list, because it is how "read-only" roles turn out not to be.
	Wildcard bool `json:"wildcard,omitempty"`
}

// subjectView is who a binding binds. `namespace` is only meaningful for a
// ServiceAccount; a User or a Group is a string the authenticator produced and
// belongs to no namespace at all.
type subjectView struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
}

// bindingView is a RoleBinding or a ClusterRoleBinding, resolved the way the
// question is asked: *who* gets *what*. The roleRef is flattened into the two
// fields that identify it, and the subjects are carried in full (up to the cap)
// rather than counted, because a binding whose subjects are hidden answers
// nothing.
type bindingView struct {
	listMeta
	// RoleKind is `Role` or `ClusterRole` — a RoleBinding may reference either,
	// and which one it is decides whether the grant is bounded by the namespace
	// the binding lives in or merely *applied* in it.
	RoleKind string `json:"role_kind"`
	RoleName string `json:"role_name"`
	// ClusterScoped marks a ClusterRoleBinding, whose grant covers every
	// namespace at once. It is the field that turns a binding list into an
	// answer about blast radius.
	ClusterScoped bool          `json:"cluster_scoped,omitempty"`
	Subjects      []subjectView `json:"subjects"`
	SubjectCount  int           `json:"subject_count"`
	// Kinds is the set of subject kinds, so a row can say "3 ServiceAccounts"
	// without the reader parsing the list.
	Kinds []string `json:"kinds,omitempty"`
}

// serviceAccountView is a ServiceAccount as a list shows it. A ServiceAccount is
// in this section rather than under Workloads because it is an *identity*: it is
// what a pod authenticates as, and it is the subject half of every binding that
// grants a workload anything.
type serviceAccountView struct {
	listMeta
	// Secrets and ImagePullSecrets are counted rather than named for the reason
	// the Secret list exists: a name is inventory, but a list of token secret
	// names on every row is noise in a table about identities.
	Secrets          int `json:"secrets"`
	ImagePullSecrets int `json:"image_pull_secrets"`
	// AutomountToken reports the explicit setting only. `nil` — the common case —
	// means the pod spec decides, which is not the same as either answer.
	AutomountToken *bool `json:"automount_token,omitempty"`
	// Default marks the ServiceAccount every namespace has whether anyone asked
	// for it, and which every pod that names none runs as.
	Default bool `json:"default,omitempty"`
}

func (v roleView) sortKey() (string, string)           { return v.Namespace, v.Name }
func (v bindingView) sortKey() (string, string)        { return v.Namespace, v.Name }
func (v serviceAccountView) sortKey() (string, string) { return v.Namespace, v.Name }

/* ------------------------------------------------------- roles & bindings --- */

// rbacGroup is the API path every kind here is served under. RBAC has had one
// stable version since 1.8, so unlike the CRD-backed lists there is nothing to
// fall back to.
const rbacGroup = "/apis/rbac.authorization.k8s.io/v1"

// bootstrapLabel is what Kubernetes stamps on the roles it installs itself. Most
// of a fresh cluster's ClusterRole list carries it, and telling those apart from
// the ones somebody wrote is the difference between a list worth reading and 70
// rows of `system:controller:*`.
const bootstrapLabel = "kubernetes.io/bootstrapping"

// rbacRole is the wire shape of a Role or ClusterRole.
type rbacRole struct {
	Metadata objectMeta `json:"metadata"`
	Rules    []struct {
		Verbs           []string `json:"verbs"`
		APIGroups       []string `json:"apiGroups"`
		Resources       []string `json:"resources"`
		ResourceNames   []string `json:"resourceNames"`
		NonResourceURLs []string `json:"nonResourceURLs"`
	} `json:"rules"`
	// AggregationRule is set only on a ClusterRole the controller assembles.
	AggregationRule *struct {
		ClusterRoleSelectors []struct{} `json:"clusterRoleSelectors"`
	} `json:"aggregationRule"`
}

// view normalises one Role or ClusterRole into what a list row shows.
func (r rbacRole) view() roleView {
	out := roleView{
		listMeta:   r.Metadata.meta(),
		RuleCount:  len(r.Rules),
		Rules:      []policyRuleView{},
		Aggregated: r.AggregationRule != nil,
		Builtin:    r.Metadata.Labels[bootstrapLabel] != "",
	}

	verbs := map[string]struct{}{}
	resources := map[string]struct{}{}

	for i, rule := range r.Rules {
		for _, verb := range rule.Verbs {
			verbs[verb] = struct{}{}
			if verb == "*" {
				out.Wildcard = true
			}
		}
		for _, resource := range rule.Resources {
			resources[resource] = struct{}{}
			if resource == "*" {
				out.Wildcard = true
			}
		}
		if i >= maxRuleSummary {
			continue
		}
		out.Rules = append(out.Rules, policyRuleView{
			Verbs:           rule.Verbs,
			APIGroups:       rule.APIGroups,
			Resources:       rule.Resources,
			ResourceNames:   rule.ResourceNames,
			NonResourceURLs: rule.NonResourceURLs,
		})
	}

	out.Verbs = sortedKeys(verbs)
	out.Resources = sortedKeys(resources)
	return out
}

// rbacBinding is the wire shape of a RoleBinding or ClusterRoleBinding.
type rbacBinding struct {
	Metadata objectMeta `json:"metadata"`
	RoleRef  struct {
		Kind string `json:"kind"`
		Name string `json:"name"`
	} `json:"roleRef"`
	Subjects []struct {
		Kind      string `json:"kind"`
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"subjects"`
}

// view resolves one binding into subject → role.
func (b rbacBinding) view(clusterScoped bool) bindingView {
	out := bindingView{
		listMeta:      b.Metadata.meta(),
		RoleKind:      b.RoleRef.Kind,
		RoleName:      b.RoleRef.Name,
		ClusterScoped: clusterScoped,
		Subjects:      []subjectView{},
		SubjectCount:  len(b.Subjects),
	}

	kinds := map[string]struct{}{}
	for i, subject := range b.Subjects {
		kinds[subject.Kind] = struct{}{}
		if i >= maxSubjects {
			continue
		}
		out.Subjects = append(out.Subjects, subjectView{
			Kind:      subject.Kind,
			Name:      subject.Name,
			Namespace: subject.Namespace,
		})
	}
	out.Kinds = sortedKeys(kinds)
	return out
}

// sortedKeys turns a set into the stable list a response carries. Order matters
// here only because an unordered one would make every response differ from the
// last, which defeats the read cache in front of these routes.
func sortedKeys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	slices.Sort(out)
	return out
}

// listRoles reads namespaced Roles.
func (s *server) listRoles(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	scope, ok := s.resourceScope(c, grant)
	if !ok {
		return
	}

	out := []roleView{}
	for _, path := range scope.paths(resourceListPath{rbacGroup, "roles"}) {
		var list struct {
			Items []rbacRole `json:"items"`
		}
		if !s.fetch(c, user, cluster, grant, path, &list) {
			return
		}
		for _, item := range list.Items {
			out = append(out, item.view())
		}
	}

	sortResources(out)
	c.JSON(http.StatusOK, gin.H{
		"roles":          out,
		"namespace":      scope.Namespace,
		"all_namespaces": scope.All,
	})
}

// listClusterRoles reads ClusterRoles. It is cluster-wide by definition, so a
// namespace-scoped grant is refused here rather than by the proxy — the same
// rule every other cluster-scoped list follows.
func (s *server) listClusterRoles(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	if !s.requireClusterScope(c, grant, "cluster roles") {
		return
	}

	var list struct {
		Items []rbacRole `json:"items"`
	}
	if !s.fetch(c, user, cluster, grant, resourceListPath{rbacGroup, "clusterroles"}.clusterWide(), &list) {
		return
	}

	out := make([]roleView, 0, len(list.Items))
	for _, item := range list.Items {
		out = append(out, item.view())
	}

	sortResources(out)
	c.JSON(http.StatusOK, gin.H{"clusterroles": out})
}

// listRoleBindings reads namespaced RoleBindings.
func (s *server) listRoleBindings(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	scope, ok := s.resourceScope(c, grant)
	if !ok {
		return
	}

	out := []bindingView{}
	for _, path := range scope.paths(resourceListPath{rbacGroup, "rolebindings"}) {
		var list struct {
			Items []rbacBinding `json:"items"`
		}
		if !s.fetch(c, user, cluster, grant, path, &list) {
			return
		}
		for _, item := range list.Items {
			out = append(out, item.view(false))
		}
	}

	sortResources(out)
	c.JSON(http.StatusOK, gin.H{
		"rolebindings":   out,
		"namespace":      scope.Namespace,
		"all_namespaces": scope.All,
	})
}

// listClusterRoleBindings reads ClusterRoleBindings.
func (s *server) listClusterRoleBindings(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	if !s.requireClusterScope(c, grant, "cluster role bindings") {
		return
	}

	var list struct {
		Items []rbacBinding `json:"items"`
	}
	path := resourceListPath{rbacGroup, "clusterrolebindings"}.clusterWide()
	if !s.fetch(c, user, cluster, grant, path, &list) {
		return
	}

	out := make([]bindingView, 0, len(list.Items))
	for _, item := range list.Items {
		out = append(out, item.view(true))
	}

	sortResources(out)
	c.JSON(http.StatusOK, gin.H{"clusterrolebindings": out})
}

// listServiceAccounts reads the identities workloads run as.
func (s *server) listServiceAccounts(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	scope, ok := s.resourceScope(c, grant)
	if !ok {
		return
	}

	out := []serviceAccountView{}
	for _, path := range scope.paths(resourceListPath{"/api/v1", "serviceaccounts"}) {
		var list struct {
			Items []struct {
				Metadata                     objectMeta `json:"metadata"`
				Secrets                      []struct{} `json:"secrets"`
				ImagePullSecrets             []struct{} `json:"imagePullSecrets"`
				AutomountServiceAccountToken *bool      `json:"automountServiceAccountToken"`
			} `json:"items"`
		}
		if !s.fetch(c, user, cluster, grant, path, &list) {
			return
		}
		for _, item := range list.Items {
			out = append(out, serviceAccountView{
				listMeta:         item.Metadata.meta(),
				Secrets:          len(item.Secrets),
				ImagePullSecrets: len(item.ImagePullSecrets),
				AutomountToken:   item.AutomountServiceAccountToken,
				Default:          item.Metadata.Name == "default",
			})
		}
	}

	sortResources(out)
	c.JSON(http.StatusOK, gin.H{
		"serviceaccounts": out,
		"namespace":       scope.Namespace,
		"all_namespaces":  scope.All,
	})
}

/* -------------------------------------------------------- access review --- */

/*
 * The answer an inventory cannot give.
 *
 * Reading every Role and every binding tells you what is written down. It does
 * not tell you what the authorizer will *do*, and the difference between the two
 * is where an audit finding lives: aggregation assembles rules from labels,
 * wildcards mean more than they look like, a subject can be reached through
 * three bindings at once, and the cluster may be running an authorizer that is
 * not RBAC at all (webhook, node, or a cloud provider's own). Deriving a verdict
 * by walking bindings would be KubeMG guessing at an answer the cluster is
 * willing to state.
 *
 * SubjectAccessReview is the cluster stating it. It is asked on behalf of a
 * named subject, so the page can say "this identity may `delete pods` in
 * `payments`" as the authorizer's own verdict rather than as a derivation.
 *
 * Two properties of it are load-bearing and easy to get wrong:
 *
 *   - It is a `create` against `authorization.k8s.io`, so **it is a write in
 *     RBAC's eyes** even though it changes nothing. A caller whose grant does not
 *     carry it is refused, which is correct — asking what an arbitrary identity
 *     may do is itself privileged information — and the refusal is surfaced as
 *     the cluster's, in the cluster's own words, rather than hidden behind an
 *     empty result.
 *   - The review runs under the *caller's* impersonated identity, asking about
 *     someone else. That is what SubjectAccessReview is for, and it is why this
 *     is not a way to escalate: an operator who cannot create the review gets
 *     nothing, and one who can was already trusted with the answer.
 */

// accessReviewRequest is the question, as the console asks it.
type accessReviewRequest struct {
	// Subject is who is being asked about: a user name, a group, or a
	// ServiceAccount named as `system:serviceaccount:<namespace>:<name>`.
	Subject string   `json:"subject"`
	Groups  []string `json:"groups"`

	// The verb and the resource, exactly as RBAC names them.
	Verb     string `json:"verb"`
	Group    string `json:"group"`
	Resource string `json:"resource"`
	// Subresource narrows to `pods/exec`, `pods/log` — which is where several of
	// the answers people actually want live.
	Subresource string `json:"subresource"`
	Name        string `json:"name"`
	Namespace   string `json:"namespace"`
}

// accessReviewResult is the cluster's verdict.
type accessReviewResult struct {
	Allowed bool `json:"allowed"`
	// Denied is not merely "not allowed": an authorizer can *explicitly* deny,
	// which no later authorizer in the chain can then permit. The distinction
	// matters when somebody is trying to work out why adding a binding did not
	// help.
	Denied bool `json:"denied,omitempty"`
	// Reason is the authorizer's own explanation, which usually names the
	// binding that decided it.
	Reason string `json:"reason,omitempty"`
	// EvaluationError is set when the authorizer could not finish. A review that
	// errored is not a denial, and reporting it as one would be a lie about the
	// cluster.
	EvaluationError string `json:"evaluation_error,omitempty"`

	// The question echoed back, so a stored or shared answer says what was asked.
	Subject   string `json:"subject"`
	Verb      string `json:"verb"`
	Resource  string `json:"resource"`
	Namespace string `json:"namespace,omitempty"`
}

// reviewVerbs are the verbs a review may ask about: RBAC's own set, plus the
// wildcard. It is a fixed list because the field goes into a body sent to the
// API server, and because a verb outside this set is a typo rather than a
// question — `del` returns "not allowed" and reads as an answer.
var reviewVerbs = []string{
	"get", "list", "watch", "create", "update", "patch", "delete", "deletecollection",
	"impersonate", "bind", "escalate", "use", "*",
}

// rbacNamePattern is what a subject, group, resource or name may contain. It is
// validated rather than escaped for the same reason the metrics query names are:
// these components are assembled into a request the API server will act on, and
// a validated component cannot become syntax. The set is deliberately wider than
// RFC 1123 — a Kubernetes user name is whatever the authenticator produced, and
// that is routinely an email address or a cloud IAM ARN.
func validRBACName(name string) bool {
	if name == "" || len(name) > 253 {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune(".-_:@/*", r):
		default:
			return false
		}
	}
	return true
}

// accessReview asks the cluster whether a named subject may do something.
func (s *server) accessReview(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}

	var payload accessReviewRequest
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "the access review could not be read"})
		return
	}

	subject := strings.TrimSpace(payload.Subject)
	if !validRBACName(subject) {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "name the identity to ask about — a user, a group, or " +
				"system:serviceaccount:<namespace>:<name>",
		})
		return
	}
	verb := strings.TrimSpace(payload.Verb)
	if !slices.Contains(reviewVerbs, verb) {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "pick one of the RBAC verbs: " + strings.Join(reviewVerbs, ", "),
		})
		return
	}
	resource := strings.TrimSpace(payload.Resource)
	if !validRBACName(resource) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name the resource to ask about, e.g. pods"})
		return
	}

	// The optional narrowings. Each is validated on the same rule and simply
	// omitted when empty, since an empty field in a SubjectAccessReview means
	// "any" rather than "none".
	optional := map[string]string{
		"apiGroup":    strings.TrimSpace(payload.Group),
		"subresource": strings.TrimSpace(payload.Subresource),
		"name":        strings.TrimSpace(payload.Name),
	}
	for field, value := range optional {
		if value != "" && !validRBACName(value) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "the " + field + " is not a valid name"})
			return
		}
	}

	// A namespaced question is checked against the caller's own grant like every
	// other namespaced read. Asking about a namespace outside the grant would let
	// a scoped caller map a cluster they cannot see — the review would answer,
	// because it is asked as *them* and they may be allowed to create reviews.
	namespace := strings.TrimSpace(payload.Namespace)
	if namespace != "" {
		var resolved bool
		namespace, resolved = s.scopedNamespace(c, grant, namespace)
		if !resolved {
			return
		}
	} else if allowed := grant.NamespaceList(); len(allowed) > 0 {
		// A cluster-wide review from a namespace-scoped grant is the same
		// overreach in the other direction: an empty namespace asks about every
		// namespace at once.
		c.JSON(http.StatusForbidden, gin.H{
			"error": "your access to this cluster is limited to " + strings.Join(allowed, ", ") +
				", so a review has to name one of those namespaces",
		})
		return
	}

	// Groups are optional context: an authorizer decides on the union of the
	// user's name and their groups, so a review that omits a group the real
	// request would carry can answer "no" where the cluster would say "yes".
	groups := []string{}
	for _, group := range payload.Groups {
		group = strings.TrimSpace(group)
		if group == "" {
			continue
		}
		if !validRBACName(group) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "the group " + group + " is not a valid name"})
			return
		}
		groups = append(groups, group)
		if len(groups) >= maxSubjects {
			break
		}
	}

	body := map[string]any{
		"apiVersion": "authorization.k8s.io/v1",
		"kind":       "SubjectAccessReview",
		"spec": map[string]any{
			"user":   subject,
			"groups": groups,
			"resourceAttributes": map[string]any{
				"namespace":   namespace,
				"verb":        verb,
				"group":       optional["apiGroup"],
				"resource":    resource,
				"subresource": optional["subresource"],
				"name":        optional["name"],
			},
		},
	}

	document, err := json.Marshal(body)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "the access review could not be encoded"})
		return
	}

	resp, callOK := s.callResourceWith(c, user, cluster, grant, http.MethodPost,
		"/apis/authorization.k8s.io/v1/subjectaccessreviews", document,
		"could not ask the cluster")
	if !callOK {
		return
	}
	if resp.Status < 200 || resp.Status >= 300 {
		// Creating a review is a write in RBAC's eyes, so a `view` grant is
		// refused here — and that refusal is the cluster's own answer about the
		// caller, worth showing as itself rather than as a broken feature.
		c.JSON(resp.Status, gin.H{"error": kubeErrorMessage(resp.Body, resp.Status)})
		return
	}

	var answer struct {
		Status struct {
			Allowed         bool   `json:"allowed"`
			Denied          bool   `json:"denied"`
			Reason          string `json:"reason"`
			EvaluationError string `json:"evaluationError"`
		} `json:"status"`
	}
	if !s.decodeResource(c, resp, &answer) {
		return
	}

	c.JSON(http.StatusOK, accessReviewResult{
		Allowed:         answer.Status.Allowed,
		Denied:          answer.Status.Denied,
		Reason:          answer.Status.Reason,
		EvaluationError: answer.Status.EvaluationError,
		Subject:         subject,
		Verb:            verb,
		Resource:        reviewTarget(resource, optional["subresource"]),
		Namespace:       namespace,
	})
}

// reviewTarget names what was asked about, the way RBAC writes it.
func reviewTarget(resource, subresource string) string {
	if subresource == "" {
		return resource
	}
	return fmt.Sprintf("%s/%s", resource, subresource)
}

// verbCatalogue is the verb list the console's review form offers. It is served
// rather than compiled into the browser for the same reason the JIT window
// presets are: the set the server will accept is the only honest source for what
// the form may offer.
func (s *server) accessReviewVerbs(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"verbs": reviewVerbs})
}

// grantIdentity reports the identity KubeMG would impersonate for the caller on
// this cluster — the subject a review should be asked about to answer "what is
// *my* grant actually worth here", which is the question the permissions matrix
// cannot answer on its own.
func (s *server) grantIdentity(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}

	// The same two values the proxy puts on the wire as `Impersonate-User` and
	// `Impersonate-Group`, read from the same function rather than restated here:
	// an identity page that disagrees with what is actually impersonated would be
	// worse than no identity page.
	c.JSON(http.StatusOK, gin.H{
		"subject":    user.Username,
		"groups":     bastion.ImpersonationGroups(grant.K8sRole),
		"k8s_role":   string(grant.K8sRole),
		"namespaces": grant.NamespaceList(),
		"cluster":    cluster.Name,
	})
}
