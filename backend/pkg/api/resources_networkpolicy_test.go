package api

import (
	"net/http"
	"slices"
	"testing"
)

/*
 * NetworkPolicies as a resource, and "who can reach this".
 *
 * The list normalisation is the fixed-inventory pattern and is not worth
 * pinning beyond what every other list already covers. What is pinned here is
 * the derivation: selector matching for every LabelSelector shape, the
 * `policyTypes` default, how a peer renders, and — the answer the roadmap
 * calls out as the most useful one — a workload selected by nothing at all in
 * a namespace where other things are.
 */

// expression is the anonymous MatchExpressions element type, named locally so
// a test can build one without repeating the inline struct literal at every
// call site — the same convention resources_workload_logs_test.go uses for the
// same type.
type expression = struct {
	Key      string   `json:"key"`
	Operator string   `json:"operator"`
	Values   []string `json:"values"`
}

/* --------------------------------------------------------------- selectors --- */

func TestSelectorMatchesMatchLabels(t *testing.T) {
	sel := labelSelector{MatchLabels: map[string]string{"app": "api", "tier": "web"}}

	if !selectorMatches(sel, map[string]string{"app": "api", "tier": "web", "extra": "yes"}) {
		t.Fatal("a superset of labels should match")
	}
	if selectorMatches(sel, map[string]string{"app": "api"}) {
		t.Fatal("a missing matchLabels key must not match")
	}
	if selectorMatches(sel, map[string]string{"app": "api", "tier": "worker"}) {
		t.Fatal("a wrong value must not match")
	}
}

func TestSelectorMatchesEmptySelectorMatchesEverything(t *testing.T) {
	// podSelector: {} — the shape a NetworkPolicy uses to mean "every pod in
	// the namespace" — has to match a workload with no labels at all as much
	// as one with a hundred.
	if !selectorMatches(labelSelector{}, map[string]string{}) {
		t.Fatal("an empty selector must match a pod with no labels")
	}
	if !selectorMatches(labelSelector{}, map[string]string{"app": "api"}) {
		t.Fatal("an empty selector must match a pod with labels too")
	}
}

func TestSelectorMatchesExpressionOperators(t *testing.T) {
	cases := []struct {
		name   string
		expr   expression
		labels map[string]string
		want   bool
	}{
		{"In present in set", expression{Key: "tier", Operator: "In", Values: []string{"web", "worker"}},
			map[string]string{"tier": "web"}, true},
		{"In present outside set", expression{Key: "tier", Operator: "In", Values: []string{"web"}},
			map[string]string{"tier": "worker"}, false},
		{"In absent", expression{Key: "tier", Operator: "In", Values: []string{"web"}},
			map[string]string{}, false},
		{"NotIn absent key satisfies", expression{Key: "tier", Operator: "NotIn", Values: []string{"web"}},
			map[string]string{}, true},
		{"NotIn present outside set satisfies", expression{Key: "tier", Operator: "NotIn", Values: []string{"web"}},
			map[string]string{"tier": "worker"}, true},
		{"NotIn present inside set fails", expression{Key: "tier", Operator: "NotIn", Values: []string{"web"}},
			map[string]string{"tier": "web"}, false},
		{"Exists present", expression{Key: "track", Operator: "Exists"},
			map[string]string{"track": ""}, true},
		{"Exists absent", expression{Key: "track", Operator: "Exists"}, map[string]string{}, false},
		{"DoesNotExist absent", expression{Key: "retired", Operator: "DoesNotExist"}, map[string]string{}, true},
		{"DoesNotExist present", expression{Key: "retired", Operator: "DoesNotExist"},
			map[string]string{"retired": "true"}, false},
		{"Unknown operator never matches", expression{Key: "tier", Operator: "Matches", Values: []string{"web"}},
			map[string]string{"tier": "web"}, false},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			sel := labelSelector{MatchExpressions: []expression{test.expr}}
			if got := selectorMatches(sel, test.labels); got != test.want {
				t.Fatalf("selectorMatches() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestEffectivePolicyTypesDefaultsFromEgress(t *testing.T) {
	// No explicit policyTypes and no egress rules: Ingress only, the documented
	// default for a policy that says nothing about either direction.
	if got := effectivePolicyTypes(netpolSpec{}); !slices.Equal(got, []string{"Ingress"}) {
		t.Fatalf("policyTypes = %v, want [Ingress]", got)
	}

	// No explicit policyTypes but an egress rule exists: the policy isolates
	// egress whether or not it says so, and the default has to say so too —
	// this is the direction that is easy to get backwards.
	withEgress := netpolSpec{Egress: []netpolEgressRule{{}}}
	if got := effectivePolicyTypes(withEgress); !slices.Equal(got, []string{"Ingress", "Egress"}) {
		t.Fatalf("policyTypes = %v, want [Ingress Egress]", got)
	}

	// An explicit list always wins, even a single-direction one alongside an
	// egress rule — that combination is unusual but the field is authoritative.
	explicit := netpolSpec{PolicyTypes: []string{"Egress"}, Egress: []netpolEgressRule{{}}}
	if got := effectivePolicyTypes(explicit); !slices.Equal(got, []string{"Egress"}) {
		t.Fatalf("policyTypes = %v, want [Egress]", got)
	}
}

/* -------------------------------------------------------------- peer views --- */

func TestPeerViewEveryShape(t *testing.T) {
	podSel := labelSelector{MatchLabels: map[string]string{"app": "api"}}
	nsSel := labelSelector{MatchLabels: map[string]string{"env": "prod"}}

	cases := []struct {
		name string
		peer netpolPeer
		want peerView
	}{
		{
			"ipBlock",
			netpolPeer{IPBlock: &struct {
				CIDR   string   `json:"cidr"`
				Except []string `json:"except"`
			}{CIDR: "10.0.0.0/8", Except: []string{"10.0.1.0/24"}}},
			peerView{Kind: "ip_block", CIDR: "10.0.0.0/8", Except: []string{"10.0.1.0/24"}},
		},
		{
			"namespaceSelector alone reaches every pod in matching namespaces",
			netpolPeer{NamespaceSelector: &nsSel},
			peerView{Kind: "namespace", NamespaceSelector: "env=prod"},
		},
		{
			"podSelector alone is scoped to the policy's own namespace",
			netpolPeer{PodSelector: &podSel},
			peerView{Kind: "pod", PodSelector: "app=api", Namespace: "payments"},
		},
		{
			"both selectors narrow to pods inside matching namespaces",
			netpolPeer{PodSelector: &podSel, NamespaceSelector: &nsSel},
			peerView{Kind: "pod", PodSelector: "app=api", NamespaceSelector: "env=prod"},
		},
		{
			"a peer naming nothing reads as everything, fail-safe",
			netpolPeer{},
			peerView{Kind: "all"},
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got := test.peer.view("payments")
			if got.Kind != test.want.Kind || got.PodSelector != test.want.PodSelector ||
				got.NamespaceSelector != test.want.NamespaceSelector || got.Namespace != test.want.Namespace ||
				got.CIDR != test.want.CIDR || !slices.Equal(got.Except, test.want.Except) {
				t.Fatalf("peer.view() = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestRulePeersEmptyMeansEverySource(t *testing.T) {
	// Kubernetes defines an ingress/egress rule with no `from`/`to` entries as
	// matching every source or destination — the opposite of what "empty"
	// would naively suggest, and the one place a reachability reader could
	// silently report an open rule as a closed one.
	got := rulePeers(nil, "payments")
	if len(got) != 1 || got[0].Kind != "all" {
		t.Fatalf("rulePeers(nil) = %+v, want a single 'all' peer", got)
	}
}

/* ------------------------------------------------------------- reachability --- */

func TestDeriveReachabilityCoversWhatSelectsIt(t *testing.T) {
	labels := map[string]string{"app": "api"}
	nsSel := labelSelector{MatchLabels: map[string]string{"env": "prod"}}

	policies := []netpolObject{
		{
			Metadata: objectMeta{Name: "allow-from-prod", Namespace: "payments"},
			Spec: netpolSpec{
				PodSelector: labelSelector{MatchLabels: map[string]string{"app": "api"}},
				PolicyTypes: []string{"Ingress"},
				Ingress:     []netpolIngressRule{{From: []netpolPeer{{NamespaceSelector: &nsSel}}}},
			},
		},
		// Selects a different workload entirely; must not contribute.
		{
			Metadata: objectMeta{Name: "unrelated", Namespace: "payments"},
			Spec: netpolSpec{
				PodSelector: labelSelector{MatchLabels: map[string]string{"app": "worker"}},
				PolicyTypes: []string{"Ingress"},
			},
		},
	}

	view := deriveReachability("payments", labels, policies)

	if !view.IngressCovered {
		t.Fatal("a workload selected by a matching policy must be covered")
	}
	if !slices.Equal(view.IngressPolicies, []string{"allow-from-prod"}) {
		t.Fatalf("ingress_policies = %v, want only the selecting policy", view.IngressPolicies)
	}
	if len(view.IngressPeers) != 1 || view.IngressPeers[0].Kind != "namespace" {
		t.Fatalf("ingress_peers = %+v, want the one namespace peer", view.IngressPeers)
	}
	if view.EgressCovered {
		t.Fatal("no policy here declares Egress, so it must not be reported as covered")
	}
	// Nothing else in the namespace declares Egress either.
	if view.NamespaceHasEgressPolicies {
		t.Fatal("namespace_has_egress_policies must be false when nothing declares Egress")
	}
}

func TestDeriveReachabilityWideOpenByOmission(t *testing.T) {
	// The case the roadmap calls out as the most useful answer: a workload no
	// policy selects, in a namespace where something else is governed. That
	// has to be told apart from a namespace using no NetworkPolicy at all.
	policies := []netpolObject{
		{
			Metadata: objectMeta{Name: "guards-something-else", Namespace: "payments"},
			Spec: netpolSpec{
				PodSelector: labelSelector{MatchLabels: map[string]string{"app": "worker"}},
				PolicyTypes: []string{"Ingress"},
			},
		},
	}

	view := deriveReachability("payments", map[string]string{"app": "api"}, policies)

	if view.IngressCovered {
		t.Fatal("a policy that does not select this workload must not cover it")
	}
	if !view.NamespaceHasIngressPolicies {
		t.Fatal("the namespace does have an Ingress policy, just not one that selects this workload")
	}
}

func TestDeriveReachabilityNamespaceWithNoPolicies(t *testing.T) {
	// The other honest answer: nobody here uses NetworkPolicy for this
	// direction at all, which is a different (and less specific) finding than
	// being skipped by name.
	view := deriveReachability("payments", map[string]string{"app": "api"}, nil)

	if view.IngressCovered || view.NamespaceHasIngressPolicies {
		t.Fatal("an empty policy list must report both covered and namespace-has as false")
	}
}

func TestDeriveReachabilityEmptyIngressRulesMeanDenyAll(t *testing.T) {
	// PolicyTypes: [Ingress] with no ingress rules at all is a real and common
	// policy — deny all ingress — and it must still read as covered: the
	// workload is governed, the governance just permits nothing.
	policies := []netpolObject{
		{
			Metadata: objectMeta{Name: "deny-all-ingress", Namespace: "payments"},
			Spec: netpolSpec{
				PodSelector: labelSelector{},
				PolicyTypes: []string{"Ingress"},
			},
		},
	}

	view := deriveReachability("payments", map[string]string{"app": "api"}, policies)

	if !view.IngressCovered {
		t.Fatal("an Ingress-type policy with no rules still covers — it denies everything")
	}
	if len(view.IngressPeers) != 0 {
		t.Fatalf("ingress_peers = %+v, want none: nothing is permitted", view.IngressPeers)
	}
}

/* --------------------------------------------------------------- coverage --- */

func TestComputeCoverageBucketsByDirection(t *testing.T) {
	policies := []netpolObject{
		{
			Metadata: objectMeta{Name: "guard-api", Namespace: "payments"},
			Spec: netpolSpec{
				PodSelector: labelSelector{MatchLabels: map[string]string{"app": "api"}},
				PolicyTypes: []string{"Ingress"},
			},
		},
	}
	pods := []podLabelRef{
		{Name: "api-1", Labels: map[string]string{"app": "api"}},
		{Name: "worker-1", Labels: map[string]string{"app": "worker"}},
	}

	view := computeCoverage("payments", pods, policies)

	if view.IngressCoveredPods != 1 || view.IngressUncoveredPods != 1 {
		t.Fatalf("ingress coverage = %d covered / %d uncovered, want 1/1",
			view.IngressCoveredPods, view.IngressUncoveredPods)
	}
	if !slices.Equal(view.IngressUncoveredExample, []string{"worker-1"}) {
		t.Fatalf("ingress_uncovered_examples = %v, want [worker-1]", view.IngressUncoveredExample)
	}
	// Nothing here declares Egress, so every pod is uncovered for it.
	if view.EgressCoveredPods != 0 || view.EgressUncoveredPods != len(pods) {
		t.Fatalf("egress coverage = %d covered / %d uncovered, want 0/%d",
			view.EgressCoveredPods, view.EgressUncoveredPods, len(pods))
	}
}

/* --------------------------------------------------------------- routes --- */

// The reachability question is restricted to the kinds that actually carry pod
// labels — asking about a Service or a ConfigMap would produce a meaningless
// answer, so it is refused before anything is read.
func TestNetworkPolicyReachabilityRefusesUnsupportedKind(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("op", "secret123", "user")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	env.store.grant(user.ID, cluster.ID, "view", nil)
	token := env.tokenFor(t, user)

	rec := env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/networkpolicies/reachability"+
			"?kind=services&name=api&namespace=payments",
		token, nil)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d for a kind with no pod labels, got %d (%s)",
			http.StatusBadRequest, rec.Code, rec.Body.String())
	}
}

// A scoped grant cannot ask about a namespace outside it — the same rule every
// other namespaced read follows, and it matters more here: the answer would
// otherwise reveal whether a workload exists in a namespace the caller cannot
// see at all.
func TestNetworkPolicyReachabilityRefusesNamespaceOutsideGrant(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("scoped", "secret123", "user")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	env.store.grant(user.ID, cluster.ID, "view", []string{"team-a"})
	token := env.tokenFor(t, user)

	rec := env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/networkpolicies/reachability"+
			"?kind=pods&name=api&namespace=team-b",
		token, nil)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d for a namespace outside the grant, got %d (%s)",
			http.StatusForbidden, rec.Code, rec.Body.String())
	}
}

// Reachability and coverage are both live-cluster reads, so a direct-mode
// cluster — no agent tunnel to read through — refuses both exactly as it
// refuses every other resource route.
func TestNetworkPolicyRoutesRefuseDirectClusters(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")
	cluster := env.store.addCluster("legacy", "dev") // direct mode
	token := env.tokenFor(t, admin)

	paths := []string{
		"/resources/networkpolicies/reachability?kind=pods&name=api&namespace=default",
		"/resources/networkpolicies/coverage?namespace=default",
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			rec := env.do(t, http.MethodGet,
				"/api/v1/clusters/"+itoa(cluster.ID)+path, token, nil)
			if rec.Code != http.StatusConflict {
				t.Fatalf("expected status %d for a direct-mode cluster, got %d (%s)",
					http.StatusConflict, rec.Code, rec.Body.String())
			}
		})
	}
}

// The coverage summary is single-namespace, so it follows resourceNamespace's
// own rule: a scoped grant asking about a namespace it was not given is
// refused before any pod or policy is read.
func TestNetworkPolicyCoverageRefusesNamespaceOutsideGrant(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("scoped", "secret123", "user")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	env.store.grant(user.ID, cluster.ID, "view", []string{"team-a"})
	token := env.tokenFor(t, user)

	rec := env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/networkpolicies/coverage?namespace=team-b",
		token, nil)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d for a namespace outside the grant, got %d (%s)",
			http.StatusForbidden, rec.Code, rec.Body.String())
	}
}

func TestComputeCoverageBoundsExamples(t *testing.T) {
	pods := make([]podLabelRef, 0, maxCoverageExamples+5)
	for i := 0; i < maxCoverageExamples+5; i++ {
		pods = append(pods, podLabelRef{Name: "pod", Labels: nil})
	}

	view := computeCoverage("payments", pods, nil)

	if view.IngressUncoveredPods != len(pods) {
		t.Fatalf("ingress_uncovered_pods = %d, want %d", view.IngressUncoveredPods, len(pods))
	}
	if len(view.IngressUncoveredExample) != maxCoverageExamples {
		t.Fatalf("ingress_uncovered_examples has %d entries, want the %d-entry cap",
			len(view.IngressUncoveredExample), maxCoverageExamples)
	}
}
