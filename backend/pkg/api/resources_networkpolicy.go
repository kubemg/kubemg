package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/gin-gonic/gin"

)

/*
 * NetworkPolicies as a resource, and "who can reach this".
 *
 * `networkpolicies` was missing from the Explore inventory entirely, which meant
 * the one object that decides whether a workload is reachable was the one object
 * this console could not show. The list below closes that, and it is the
 * fixed-inventory pattern with nothing new in it — the same normalised-list,
 * same-tunnel shape as Services or Ingresses.
 *
 * The reachability derivation beside it is the part actually worth building: a
 * workload's own view of the policies that select it. Three things it answers —
 * what may reach it, what it may reach, and whether nothing selects it at all in
 * a namespace where other things are — and one thing it refuses to imply:
 *
 *   - This is a derivation from the NetworkPolicy *objects* alone. It reports
 *     what they declare, never what the cluster's CNI actually enforces — a
 *     cluster running a CNI that ignores NetworkPolicy (or one that only
 *     partially implements it) will happily serve a policy list that means
 *     nothing on the wire, and KubeMG has no way to tell the two apart from here.
 *   - It does not trace a live connection. There is no packet capture and no
 *     probe; this is a policy reading, not a connectivity test.
 *   - Ports and protocols inside a rule are **not evaluated**. A peer named here
 *     may reach this workload only if the rule's ports also allow it, and this
 *     derivation does not check that — evaluating selectors correctly and
 *     leaving ports alone is safer than evaluating both and getting one of them
 *     wrong silently.
 *
 * `networkPolicyDisclaimer` is that statement, carried on the wire on both
 * responses below rather than left to a comment only the backend ever reads —
 * the task is explicit that this is part of the feature, not a footnote.
 *
 * Selector matching implements `matchLabels` and every `matchExpressions`
 * operator (`In`, `NotIn`, `Exists`, `DoesNotExist`) for real, because a
 * silently mis-evaluated selector would make this actively harmful rather than
 * merely incomplete — an operator would read "no policy reaches this" as
 * ground truth. An operator this code does not recognise refuses to match
 * rather than guessing, which is the same rule `objectKinds` follows for a
 * kind it does not know: fail towards saying nothing rather than towards a
 * wrong yes.
 */

// networkPolicyGroup is the one API NetworkPolicy has ever been served under.
const networkPolicyGroup = "/apis/networking.k8s.io/v1"

// maxCoverageExamples bounds how many uncovered pod names the namespace summary
// names outright, on the same rule as maxSubjects: a namespace with two hundred
// uncovered pods needs the count, not all two hundred names.
const maxCoverageExamples = 10

// networkPolicyDisclaimer is carried on every response below. See the file
// header for why: this is what keeps a policy reading from being mistaken for
// enforcement reality or for a live connectivity test.
const networkPolicyDisclaimer = "Derived from NetworkPolicy objects alone: it reports what they declare, " +
	"not what this cluster's CNI actually enforces, and it does not trace any live connection. " +
	"Ports and protocols inside a rule are not evaluated, so a peer named here can reach this workload " +
	"only if the rule's ports allow it too."

/* ------------------------------------------------------------------ wire --- */

// netpolPeer is one `NetworkPolicyPeer`: exactly one of the three should be set,
// and none of them is what "no peer" looks like — an ingress or egress rule with
// no peers *at all* is what means "everything", which is handled one level up
// where the rule's own (possibly empty) peer list is read.
type netpolPeer struct {
	PodSelector       *labelSelector `json:"podSelector"`
	NamespaceSelector *labelSelector `json:"namespaceSelector"`
	IPBlock           *struct {
		CIDR   string   `json:"cidr"`
		Except []string `json:"except"`
	} `json:"ipBlock"`
}

type netpolIngressRule struct {
	From []netpolPeer `json:"from"`
}

type netpolEgressRule struct {
	To []netpolPeer `json:"to"`
}

type netpolSpec struct {
	// PodSelector is not a pointer: it is a required field, and an empty one
	// ({}) is not "unset" but "every pod in the namespace", which is exactly
	// what a zero-value labelSelector already matches.
	PodSelector labelSelector       `json:"podSelector"`
	PolicyTypes []string            `json:"policyTypes"`
	Ingress     []netpolIngressRule `json:"ingress"`
	Egress      []netpolEgressRule  `json:"egress"`
}

type netpolObject struct {
	Metadata objectMeta `json:"metadata"`
	Spec     netpolSpec `json:"spec"`
}

func (n netpolObject) view() networkPolicyView {
	return networkPolicyView{
		listMeta:     n.Metadata.meta(),
		PodSelector:  selectorText(n.Spec.PodSelector),
		PolicyTypes:  effectivePolicyTypes(n.Spec),
		IngressRules: len(n.Spec.Ingress),
		EgressRules:  len(n.Spec.Egress),
	}
}

// networkPolicyView is one NetworkPolicy as the Explore list shows it. The
// rules themselves are counted rather than summarised the way a Role's are —
// there is no single axis like "verbs and resources" for a peer list, and the
// reachability view below is where a rule is actually worth reading.
type networkPolicyView struct {
	listMeta
	PodSelector  string   `json:"pod_selector"`
	PolicyTypes  []string `json:"policy_types"`
	IngressRules int      `json:"ingress_rules"`
	EgressRules  int      `json:"egress_rules"`
}

func (v networkPolicyView) sortKey() (string, string) { return v.Namespace, v.Name }

// listNetworkPolicies serves the inventory. Same shape as every other
// namespaced list in resources_inventory.go — impersonated, scoped, cached.
func (s *server) listNetworkPolicies(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	scope, ok := s.resourceScope(c, grant)
	if !ok {
		return
	}

	out := []networkPolicyView{}
	for _, path := range scope.paths(resourceListPath{networkPolicyGroup, "networkpolicies"}) {
		var list struct {
			Items []netpolObject `json:"items"`
		}
		if !fetchList(s, c, user, cluster, grant, path, &list.Items) {
			return
		}
		for _, item := range list.Items {
			out = append(out, item.view())
		}
	}

	sortResources(out)
	listResponse(c, gin.H{
		"networkpolicies": out,
		"namespace":       scope.Namespace,
		"all_namespaces":  scope.All,
	})
}

/* -------------------------------------------------------------- selectors --- */

// effectivePolicyTypes applies the documented default: an explicit list wins,
// and an absent one defaults to Ingress alone unless the policy declares an
// egress rule of its own, in which case it defaults to both. Getting this
// wrong in either direction is exactly the kind of silent misread this feature
// exists to avoid — a policy with no `policyTypes` and an `egress` block is
// isolating ingress too, whether or not it says so.
func effectivePolicyTypes(spec netpolSpec) []string {
	if len(spec.PolicyTypes) > 0 {
		return spec.PolicyTypes
	}
	if len(spec.Egress) > 0 {
		return []string{"Ingress", "Egress"}
	}
	return []string{"Ingress"}
}

// selectorMatches evaluates one LabelSelector against a set of labels, `AND`ing
// every matchLabels pair with every matchExpressions term — the same semantics
// Kubernetes itself gives a LabelSelector. `NotIn` and `DoesNotExist` are
// satisfied by a label that is simply absent, which is the one place this is
// easy to get backwards: "not in this set" is true of a key that was never set
// at all.
func selectorMatches(sel labelSelector, labels map[string]string) bool {
	for key, want := range sel.MatchLabels {
		if labels[key] != want {
			return false
		}
	}
	for _, expr := range sel.MatchExpressions {
		value, present := labels[expr.Key]
		switch expr.Operator {
		case "In":
			if !present || !slices.Contains(expr.Values, value) {
				return false
			}
		case "NotIn":
			if present && slices.Contains(expr.Values, value) {
				return false
			}
		case "Exists":
			if !present {
				return false
			}
		case "DoesNotExist":
			if present {
				return false
			}
		default:
			// An operator this cannot evaluate must not be silently treated as
			// satisfied — that would report a workload as selected (or not) on
			// a guess. Refusing to match is the fail-safe direction: it can
			// only under-report a policy's reach, never over-report it.
			return false
		}
	}
	return true
}

// selectorText renders a selector the way a reader wants it, sorted so the same
// selector always prints the same way. An empty string is the real answer for
// an empty selector — "every pod" or "every namespace" — and the caller decides
// how to say that, since the two mean different things in the two places a
// selector appears.
func selectorText(sel labelSelector) string {
	terms := make([]string, 0, len(sel.MatchLabels)+len(sel.MatchExpressions))
	for _, key := range sortedMapKeys(sel.MatchLabels) {
		terms = append(terms, fmt.Sprintf("%s=%s", key, sel.MatchLabels[key]))
	}
	for _, expr := range sel.MatchExpressions {
		switch expr.Operator {
		case "Exists":
			terms = append(terms, expr.Key)
		case "DoesNotExist":
			terms = append(terms, "!"+expr.Key)
		default:
			terms = append(terms, fmt.Sprintf("%s %s (%s)",
				expr.Key, strings.ToLower(expr.Operator), strings.Join(expr.Values, ",")))
		}
	}
	slices.Sort(terms)
	return strings.Join(terms, ",")
}

func sortedMapKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	slices.Sort(out)
	return out
}

// peerView is one source (ingress) or destination (egress) a rule names,
// normalised the way a reader actually distinguishes them: an IP range is a
// different question from a namespace's pods, which is a different question
// again from "everything".
type peerView struct {
	// Kind is "all", "namespace", "pod" or "ip_block".
	Kind string `json:"kind"`
	// PodSelector and NamespaceSelector are the selector text, empty meaning
	// "every pod" / "every namespace" respectively — that is what an empty
	// LabelSelector actually selects, so an empty string here is a real answer
	// and not a missing one.
	PodSelector       string `json:"pod_selector,omitempty"`
	NamespaceSelector string `json:"namespace_selector,omitempty"`
	// Namespace is set only when a pod selector applies with no namespace
	// selector alongside it — which selects pods in the *policy's own*
	// namespace, so the peer is otherwise silent about which namespace it means.
	Namespace string   `json:"namespace,omitempty"`
	CIDR      string   `json:"cidr,omitempty"`
	Except    []string `json:"except,omitempty"`
}

func (p netpolPeer) view(ownNamespace string) peerView {
	if p.IPBlock != nil {
		except := slices.Clone(p.IPBlock.Except)
		slices.Sort(except)
		return peerView{Kind: "ip_block", CIDR: p.IPBlock.CIDR, Except: except}
	}
	switch {
	case p.NamespaceSelector != nil && p.PodSelector != nil:
		// Pods matching PodSelector, but only inside namespaces matching
		// NamespaceSelector — the one shape where a policy names a namespace
		// with no ambiguity about which one.
		return peerView{
			Kind:              "pod",
			PodSelector:       selectorText(*p.PodSelector),
			NamespaceSelector: selectorText(*p.NamespaceSelector),
		}
	case p.NamespaceSelector != nil:
		return peerView{Kind: "namespace", NamespaceSelector: selectorText(*p.NamespaceSelector)}
	case p.PodSelector != nil:
		return peerView{Kind: "pod", PodSelector: selectorText(*p.PodSelector), Namespace: ownNamespace}
	default:
		// A NetworkPolicyPeer with none of the three set is not valid input the
		// API server would have accepted, but decoding one from a cluster that
		// somehow served it should read as "everything" rather than "nothing" —
		// the fail-safe direction for an ingress/egress reachability answer.
		return peerView{Kind: "all"}
	}
}

// rulePeers renders one rule's peer list. An empty (or absent) list is not "no
// peers" — Kubernetes defines that as "matches all sources/destinations" — so it
// is rendered as the one `all` peer rather than as nothing.
func rulePeers(peers []netpolPeer, ownNamespace string) []peerView {
	if len(peers) == 0 {
		return []peerView{{Kind: "all"}}
	}
	out := make([]peerView, 0, len(peers))
	for _, peer := range peers {
		out = append(out, peer.view(ownNamespace))
	}
	return out
}

// peerKey is a canonical string for one peerView, used only to dedupe and sort
// — never rendered — so two policies naming the same peer collapse to one line
// rather than repeating it.
func peerKey(p peerView) string {
	return strings.Join([]string{
		p.Kind, p.PodSelector, p.NamespaceSelector, p.Namespace, p.CIDR, strings.Join(p.Except, ","),
	}, "\x00")
}

func dedupePeers(peers []peerView) []peerView {
	seen := make(map[string]bool, len(peers))
	out := make([]peerView, 0, len(peers))
	for _, peer := range peers {
		key := peerKey(peer)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, peer)
	}
	slices.SortFunc(out, func(a, b peerView) int { return strings.Compare(peerKey(a), peerKey(b)) })
	return out
}

/* --------------------------------------------------------- reachability --- */

// podLabelKinds are the resource keys a reachability question may be asked
// about: the kinds that carry pod labels, which is what a NetworkPolicy's
// podSelector actually matches against.
//
// A Pod carries its labels directly. A Deployment, StatefulSet, DaemonSet or Job
// does not — it carries a pod *template*, and the labels a policy will actually
// see are whatever the running pod ends up with, which is the template's labels
// unless something else (a mutating admission webhook, a hand-applied patch)
// changed them after the fact. That gap is real and this derivation does not
// close it: `LabelSource` on the response says which case a given answer is,
// so "pod template" rather than "pod" is a visible fact about the answer, not a
// detail buried in how it was computed.
//
// A CronJob is deliberately absent, on the same rule `WORKLOAD_LOG_KINDS` in the
// frontend already follows: it owns Jobs, not pods, and has no pod template of
// its own to read a label off.
var podLabelKinds = map[string]bool{
	"pods":         true,
	"deployments":  true,
	"statefulsets": true,
	"daemonsets":   true,
	"jobs":         true,
}

// reachabilityView is a workload's own view of the policies that select it.
type reachabilityView struct {
	Kind      string            `json:"kind"`
	Name      string            `json:"name"`
	Namespace string            `json:"namespace"`
	PodLabels map[string]string `json:"pod_labels"`
	// LabelSource says whether PodLabels came off the object itself (a Pod) or
	// off its pod template (everything else this is asked about) — see
	// podLabelKinds for why that distinction has to stay visible.
	LabelSource string `json:"label_source"`

	IngressCovered  bool       `json:"ingress_covered"`
	IngressPolicies []string   `json:"ingress_policies"`
	IngressPeers    []peerView `json:"ingress_peers"`
	// NamespaceHasIngressPolicies is true when *some* NetworkPolicy in this
	// namespace declares Ingress, whether or not it selects this workload. It
	// is what turns "not covered" into an actual finding: a workload that no
	// policy selects, in a namespace where other things are governed, is wide
	// open by omission rather than by decision — the case the roadmap calls
	// out as the most useful answer this whole feature gives. Without it,
	// "not covered" cannot be told apart from "nobody here uses NetworkPolicy
	// at all", which is a shrug rather than a finding.
	NamespaceHasIngressPolicies bool `json:"namespace_has_ingress_policies"`

	EgressCovered              bool       `json:"egress_covered"`
	EgressPolicies             []string   `json:"egress_policies"`
	EgressPeers                []peerView `json:"egress_peers"`
	NamespaceHasEgressPolicies bool       `json:"namespace_has_egress_policies"`

	// PoliciesAvailable is false when the NetworkPolicy list itself could not
	// be read — a scoped grant that may see this workload but not its
	// policies. Everything above is meaningless without it, so the zero values
	// are not "no policies exist", they are "unknown", and the reason says why.
	PoliciesAvailable bool   `json:"policies_available"`
	UnavailableReason string `json:"unavailable_reason,omitempty"`

	Disclaimer string `json:"disclaimer"`
}

// selectingPolicies filters to the policies in this namespace whose podSelector
// matches these labels. A NetworkPolicy's podSelector only ever applies within
// its own namespace — there is no such thing as a NetworkPolicy that selects
// pods somewhere else.
func selectingPolicies(namespace string, labels map[string]string, policies []netpolObject) []netpolObject {
	out := make([]netpolObject, 0, len(policies))
	for _, policy := range policies {
		if policy.Metadata.Namespace != namespace {
			continue
		}
		if selectorMatches(policy.Spec.PodSelector, labels) {
			out = append(out, policy)
		}
	}
	return out
}

// namespaceHasPolicyType reports whether any policy in the namespace declares
// the given direction, regardless of who it selects — see
// NamespaceHasIngressPolicies above for why that is asked separately from
// whether it selects *this* workload.
func namespaceHasPolicyType(policies []netpolObject, direction string) bool {
	for _, policy := range policies {
		if slices.Contains(effectivePolicyTypes(policy.Spec), direction) {
			return true
		}
	}
	return false
}

// deriveReachability is the pure computation behind the reachability endpoint:
// given a workload's labels and every NetworkPolicy in its namespace, what
// selects it and what those policies say. It takes no cluster, no grant and no
// gin context on purpose — it is the part of this feature worth unit testing
// directly, table by table, against every selector shape.
func deriveReachability(namespace string, labels map[string]string, policies []netpolObject) reachabilityView {
	view := reachabilityView{
		Namespace:                   namespace,
		PodLabels:                   labels,
		IngressPolicies:             []string{},
		IngressPeers:                []peerView{},
		EgressPolicies:              []string{},
		EgressPeers:                 []peerView{},
		PoliciesAvailable:           true,
		NamespaceHasIngressPolicies: namespaceHasPolicyType(policies, "Ingress"),
		NamespaceHasEgressPolicies:  namespaceHasPolicyType(policies, "Egress"),
	}

	for _, policy := range selectingPolicies(namespace, labels, policies) {
		types := effectivePolicyTypes(policy.Spec)
		if slices.Contains(types, "Ingress") {
			view.IngressCovered = true
			view.IngressPolicies = append(view.IngressPolicies, policy.Metadata.Name)
			for _, rule := range policy.Spec.Ingress {
				view.IngressPeers = append(view.IngressPeers, rulePeers(rule.From, namespace)...)
			}
		}
		if slices.Contains(types, "Egress") {
			view.EgressCovered = true
			view.EgressPolicies = append(view.EgressPolicies, policy.Metadata.Name)
			for _, rule := range policy.Spec.Egress {
				view.EgressPeers = append(view.EgressPeers, rulePeers(rule.To, namespace)...)
			}
		}
	}

	slices.Sort(view.IngressPolicies)
	slices.Sort(view.EgressPolicies)
	view.IngressPeers = dedupePeers(view.IngressPeers)
	view.EgressPeers = dedupePeers(view.EgressPeers)
	return view
}

// podLabelsOf pulls the labels a policy would actually see out of a decoded
// object, keyed by which kind it is: a Pod's own metadata, or a workload's pod
// template. Nil, not an error, is the honest answer for "declares no labels".
func podLabelsOf(key string, body []byte) (map[string]string, string, error) {
	if key == "pods" {
		var pod struct {
			Metadata objectMeta `json:"metadata"`
		}
		if err := json.Unmarshal(body, &pod); err != nil {
			return nil, "", err
		}
		return pod.Metadata.Labels, "pod", nil
	}

	var workload struct {
		Spec struct {
			Template struct {
				Metadata struct {
					Labels map[string]string `json:"labels"`
				} `json:"metadata"`
			} `json:"template"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(body, &workload); err != nil {
		return nil, "", err
	}
	return workload.Spec.Template.Metadata.Labels, "pod template", nil
}

// networkPolicyReachability answers what may reach one workload, what it may
// reach, and whether nothing selects it at all.
func (s *server) networkPolicyReachability(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}

	key := strings.TrimSpace(c.Query("kind"))
	if !podLabelKinds[key] {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "reachability is only derived for kinds that carry pod labels: pods, deployments, statefulsets, daemonsets, jobs",
		})
		return
	}
	kind, known := objectKinds[key]
	if !known {
		c.JSON(http.StatusBadRequest, gin.H{"error": "kubemg does not serve manifests for " + key})
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

	body, ok := s.readObject(c, user, cluster, grant, kind, namespace, name)
	if !ok {
		return
	}

	labels, source, err := podLabelsOf(key, body)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "the cluster returned an unreadable object"})
		return
	}
	if labels == nil {
		labels = map[string]string{}
	}

	var policyList struct {
		Items []netpolObject `json:"items"`
	}
	available, reason, ok := fetchDegradingList(s, c, user, cluster, grant,
		resourceListPath{networkPolicyGroup, "networkpolicies"}.namespaced(namespace), &policyList.Items)
	if !ok {
		return
	}

	var view reachabilityView
	if available {
		view = deriveReachability(namespace, labels, policyList.Items)
	} else {
		view = reachabilityView{
			Namespace:         namespace,
			PodLabels:         labels,
			PoliciesAvailable: false,
			UnavailableReason: reason,
		}
	}
	view.Kind = key
	view.Name = name
	view.LabelSource = source
	view.Disclaimer = networkPolicyDisclaimer
	c.JSON(http.StatusOK, view)
}

/* ------------------------------------------------------ namespace coverage --- */

// podLabelRef is the one field a coverage pass needs off a pod: its labels.
// Kept separate from podObject on purpose — that type is the pod list's own,
// and widening it to carry labels would put them on every pod list response
// rather than only on this one, dedicated read.
type podLabelRef struct {
	Name   string
	Labels map[string]string
}

// coverageView is the namespace-level summary: how much of what is here is
// actually governed by a NetworkPolicy, by direction, since `policyTypes`
// means a workload can be covered for one direction and wide open on the
// other.
type coverageView struct {
	Namespace   string `json:"namespace"`
	PolicyCount int    `json:"policy_count"`
	PodCount    int    `json:"pod_count"`

	IngressCoveredPods      int      `json:"ingress_covered_pods"`
	IngressUncoveredPods    int      `json:"ingress_uncovered_pods"`
	IngressUncoveredExample []string `json:"ingress_uncovered_examples,omitempty"`

	EgressCoveredPods      int      `json:"egress_covered_pods"`
	EgressUncoveredPods    int      `json:"egress_uncovered_pods"`
	EgressUncoveredExample []string `json:"egress_uncovered_examples,omitempty"`

	Available         bool   `json:"available"`
	UnavailableReason string `json:"unavailable_reason,omitempty"`
	Disclaimer        string `json:"disclaimer"`
}

// computeCoverage is deriveReachability's namespace-wide twin, and just as
// deliberately pure: every pod in the namespace against every policy, bucketed
// by direction. It says nothing about the CNI either, for the same reason.
func computeCoverage(namespace string, pods []podLabelRef, policies []netpolObject) coverageView {
	view := coverageView{
		Namespace:               namespace,
		PolicyCount:             len(policies),
		PodCount:                len(pods),
		Available:               true,
		IngressUncoveredExample: []string{},
		EgressUncoveredExample:  []string{},
	}

	for _, pod := range pods {
		selecting := selectingPolicies(namespace, pod.Labels, policies)
		ingress, egress := false, false
		for _, policy := range selecting {
			types := effectivePolicyTypes(policy.Spec)
			if slices.Contains(types, "Ingress") {
				ingress = true
			}
			if slices.Contains(types, "Egress") {
				egress = true
			}
		}
		if ingress {
			view.IngressCoveredPods++
		} else {
			view.IngressUncoveredPods++
			if len(view.IngressUncoveredExample) < maxCoverageExamples {
				view.IngressUncoveredExample = append(view.IngressUncoveredExample, pod.Name)
			}
		}
		if egress {
			view.EgressCoveredPods++
		} else {
			view.EgressUncoveredPods++
			if len(view.EgressUncoveredExample) < maxCoverageExamples {
				view.EgressUncoveredExample = append(view.EgressUncoveredExample, pod.Name)
			}
		}
	}
	return view
}

// networkPolicyCoverage answers, for one namespace, how much of what is running
// there is actually governed by a NetworkPolicy — the summary the roadmap asks
// for beside the per-workload tab. It is deliberately single-namespace only:
// a NetworkPolicy's reach never crosses a namespace boundary, so "coverage"
// only ever means something inside one, and resourceNamespace already refuses
// a namespace outside a scoped grant on the same rule every other read here
// follows — there is no cluster-wide fan-out to build for this at all.
func (s *server) networkPolicyCoverage(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	namespace, ok := s.resourceNamespace(c, grant)
	if !ok {
		return
	}

	var policyList struct {
		Items []netpolObject `json:"items"`
	}
	policiesAvailable, reason, ok := fetchDegradingList(s, c, user, cluster, grant,
		resourceListPath{networkPolicyGroup, "networkpolicies"}.namespaced(namespace), &policyList.Items)
	if !ok {
		return
	}
	if !policiesAvailable {
		c.JSON(http.StatusOK, coverageView{
			Namespace: namespace, Available: false, UnavailableReason: reason,
			Disclaimer: networkPolicyDisclaimer,
		})
		return
	}

	var podList struct {
		Items []struct {
			Metadata objectMeta `json:"metadata"`
		} `json:"items"`
	}
	podsAvailable, podReason, ok := fetchDegradingList(s, c, user, cluster, grant,
		resourceListPath{"/api/v1", "pods"}.namespaced(namespace), &podList.Items)
	if !ok {
		return
	}
	if !podsAvailable {
		c.JSON(http.StatusOK, coverageView{
			Namespace: namespace, Available: false, UnavailableReason: podReason,
			Disclaimer: networkPolicyDisclaimer,
		})
		return
	}

	pods := make([]podLabelRef, 0, len(podList.Items))
	for _, item := range podList.Items {
		pods = append(pods, podLabelRef{Name: item.Metadata.Name, Labels: item.Metadata.Labels})
	}

	view := computeCoverage(namespace, pods, policyList.Items)
	view.Disclaimer = networkPolicyDisclaimer
	c.JSON(http.StatusOK, view)
}

/* -------------------------------------------------------------- helpers --- */

