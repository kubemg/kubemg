package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * Workload security posture.
 *
 * The list normalisation and the scan's fan-out are not worth pinning beyond
 * what every other list read here already covers — that machinery is shared
 * with the rest of resources.go and is exercised by its own tests. What is
 * pinned here is the part that is actually new: the seven rule bodies, each
 * with its true positive, its true negative, and — where the rule is honestly
 * uncertain rather than a plain yes/no — the case that proves it stays
 * uncertain instead of asserting an answer it cannot support. The ranking is
 * pinned too, since "ordered by what a finding permits, not by count" is a
 * requirement on the wire, not merely an implementation detail.
 */

func boolPtr(v bool) *bool    { return &v }
func int64Ptr(v int64) *int64 { return &v }

/* ---------------------------------------------------------- host namespace --- */

func TestPodSpecPostureFindingsHostNetworkAndHostPID(t *testing.T) {
	spec := podSpecFields{HostNetwork: true, HostPID: true, HostIPC: true}
	findings := podSpecPostureFindings("Pod", "agent", "kube-system", spec, nil)

	var network, pid, ipc bool
	for _, f := range findings {
		if f.Rule != string(ruleHostNamespace) {
			continue
		}
		if f.Field == "spec.hostNetwork" {
			network = true
		}
		if f.Field == "spec.hostPID" {
			pid = true
		}
		if f.Field == "spec.hostIPC" {
			ipc = true
		}
	}
	if !network || !pid || !ipc {
		t.Fatalf("hostNetwork, hostPID and hostIPC must each produce their own named finding, got %+v", findings)
	}
}

func TestPodSpecPostureFindingsNeitherHostNamespace(t *testing.T) {
	findings := podSpecPostureFindings("Pod", "web", "payments", podSpecFields{}, nil)
	for _, f := range findings {
		if f.Rule == string(ruleHostNamespace) {
			t.Fatalf(
				"a pod declaring none of hostNetwork, hostPID or hostIPC must not fire host_namespace, got %+v", f)
		}
	}
}

// PSS's "Host Namespaces" baseline control covers hostNetwork, hostPID *and*
// hostIPC (see pssDocURL) — this pins that hostIPC alone, with the other two
// left false, still fires its own named finding rather than only firing when
// paired with one of the others.
func TestPodSpecPostureFindingsHostIPCAlone(t *testing.T) {
	findings := podSpecPostureFindings("Pod", "agent", "kube-system", podSpecFields{HostIPC: true}, nil)
	f, ok := findingFor(findings, ruleHostNamespace, "")
	if !ok || f.Field != "spec.hostIPC" {
		t.Fatalf("hostIPC alone must fire host_namespace on spec.hostIPC, got %+v", findings)
	}
}

/* ------------------------------------------------------------------ hostPath --- */

func TestPodSpecPostureFindingsHostPathVolume(t *testing.T) {
	spec := podSpecFields{
		Volumes: []postureVolume{
			{Name: "docker-sock", HostPath: &struct {
				Path string `json:"path"`
			}{Path: "/var/run/docker.sock"}},
			{Name: "config", HostPath: nil},
		},
	}
	findings := podSpecPostureFindings("DaemonSet", "node-agent", "monitoring", spec, nil)

	var found bool
	for _, f := range findings {
		if f.Rule == string(ruleHostPath) {
			found = true
			if f.Field != "spec.volumes[docker-sock].hostPath.path" {
				t.Fatalf("field = %q, want the named volume's hostPath.path", f.Field)
			}
		}
	}
	if !found {
		t.Fatal("a hostPath volume must fire, and a non-hostPath volume alongside it must not add a second one")
	}
}

func TestPodSpecPostureFindingsNoVolumesNoHostPathFinding(t *testing.T) {
	findings := podSpecPostureFindings("Pod", "web", "payments", podSpecFields{}, nil)
	for _, f := range findings {
		if f.Rule == string(ruleHostPath) {
			t.Fatalf("a pod with no volumes must not fire hostpath_volume, got %+v", f)
		}
	}
}

/* ------------------------------------------------------------- automount SA --- */

func TestAutomountEffectivePrecedence(t *testing.T) {
	cases := []struct {
		name       string
		podSetting *bool
		saSetting  *bool
		want       bool
	}{
		{"pod true wins over SA false", boolPtr(true), boolPtr(false), true},
		{"pod false wins over SA true", boolPtr(false), boolPtr(true), false},
		{"SA setting used when pod says nothing", nil, boolPtr(false), false},
		{"mounted by default when neither says anything", nil, nil, true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := automountEffective(test.podSetting, test.saSetting); got != test.want {
				t.Fatalf("automountEffective() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestPodSpecPostureFindingsAutomountDefaultServiceAccount(t *testing.T) {
	// Names no ServiceAccount (resolves to "default") and nothing declines the
	// mount: the honest positive.
	findings := podSpecPostureFindings("Pod", "web", "payments", podSpecFields{}, nil)
	if !hasRule(findings, ruleAutomountDefaultSA) {
		t.Fatal("the default ServiceAccount with no automount decision anywhere must fire")
	}
}

func TestPodSpecPostureFindingsCustomServiceAccountNeverFires(t *testing.T) {
	// A workload naming its own ServiceAccount made a deliberate identity
	// choice — this rule is about the *default* one, and only that one.
	spec := podSpecFields{ServiceAccountName: "payments-api"}
	findings := podSpecPostureFindings("Pod", "web", "payments", spec, nil)
	if hasRule(findings, ruleAutomountDefaultSA) {
		t.Fatal("a workload naming its own ServiceAccount must never fire the default-SA rule")
	}
}

func TestPodSpecPostureFindingsDefaultServiceAccountDeclinedNeverFires(t *testing.T) {
	// The namespace's default ServiceAccount has explicitly declined the mount
	// — the cluster's own honest answer, and this must not override it.
	saAutomount := boolPtr(false)
	findings := podSpecPostureFindings("Pod", "web", "payments", podSpecFields{}, saAutomount)
	if hasRule(findings, ruleAutomountDefaultSA) {
		t.Fatal("a default ServiceAccount that declines automount must not fire")
	}
}

func TestPodSpecPostureFindingsPodOverridesDeclinedServiceAccount(t *testing.T) {
	// The ServiceAccount declines, but the pod spec overrides it back on — pod
	// wins, per Kubernetes' own precedence, so this must still fire.
	spec := podSpecFields{AutomountServiceAccountToken: boolPtr(true)}
	findings := podSpecPostureFindings("Pod", "web", "payments", spec, boolPtr(false))
	if !hasRule(findings, ruleAutomountDefaultSA) {
		t.Fatal("the pod spec overriding the SA's decline back on must fire, per pod-wins precedence")
	}
}

/* ------------------------------------------------------------ non-root user --- */

func TestDeclaresNonRootHonestUncertainty(t *testing.T) {
	// The rule's own honesty case: nothing declared means "uncertain", not
	// "runs as root". declaresNonRoot must answer false here — the caller is
	// responsible for phrasing that as an absence, never as a verdict — and the
	// two cases that *do* rule root out must both come back true.
	cases := []struct {
		name        string
		podSC       *postureSecurityContext
		containerSC *postureSecurityContext
		want        bool
	}{
		{"nothing declared anywhere: uncertain", nil, nil, false},
		{"container declares runAsNonRoot true", nil, &postureSecurityContext{RunAsNonRoot: boolPtr(true)}, true},
		{"container declares runAsNonRoot false: still root, not uncertain", nil,
			&postureSecurityContext{RunAsNonRoot: boolPtr(false)}, false},
		{"container declares a nonzero runAsUser", nil, &postureSecurityContext{RunAsUser: int64Ptr(1000)}, true},
		{"container declares runAsUser 0: explicitly root", nil,
			&postureSecurityContext{RunAsUser: int64Ptr(0)}, false},
		{"pod-level declares it, container says nothing",
			&postureSecurityContext{RunAsNonRoot: boolPtr(true)}, nil, true},
		{"container securityContext present but empty still inherits the pod's declaration",
			&postureSecurityContext{RunAsNonRoot: boolPtr(true)}, &postureSecurityContext{}, true},
		{"container explicitly overrides a permissive pod-level setting back to root",
			&postureSecurityContext{RunAsNonRoot: boolPtr(true)}, &postureSecurityContext{RunAsNonRoot: boolPtr(false)}, false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := declaresNonRoot(test.podSC, test.containerSC); got != test.want {
				t.Fatalf("declaresNonRoot() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestPodSpecPostureFindingsNonRootMessageNeverAssertsRoot(t *testing.T) {
	findings := podSpecPostureFindings("Pod", "web", "payments", podSpecFields{
		Containers: []postureContainer{{Name: "app"}},
	}, nil)

	f, ok := findingFor(findings, ruleRunAsRootUndeclared, "app")
	if !ok {
		t.Fatal("a container with no securityContext at all must fire the undeclared-non-root rule")
	}
	if !containsAll(f.Message, "does not mean", "runs as root", "nothing here rules it out") {
		t.Fatalf("message must phrase this as an absence rather than a verdict, got %q", f.Message)
	}
}

/* --------------------------------------------------------- resource limits --- */

func TestNoLimitsDeclared(t *testing.T) {
	cases := []struct {
		name   string
		limits map[string]string
		want   bool
	}{
		{"nil map", nil, true},
		{"empty map", map[string]string{}, true},
		{"cpu only still counts as declaring something", map[string]string{"cpu": "500m"}, false},
		{"memory only still counts as declaring something", map[string]string{"memory": "256Mi"}, false},
		{"both declared", map[string]string{"cpu": "500m", "memory": "256Mi"}, false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := noLimitsDeclared(test.limits); got != test.want {
				t.Fatalf("noLimitsDeclared(%v) = %v, want %v", test.limits, got, test.want)
			}
		})
	}
}

/* -------------------------------------------------------------- privileged --- */

func TestPodSpecPostureFindingsPrivilegedContainer(t *testing.T) {
	spec := podSpecFields{
		Containers: []postureContainer{
			{Name: "app", SecurityContext: &postureSecurityContext{Privileged: boolPtr(true)}},
			{Name: "sidecar"},
		},
	}
	findings := podSpecPostureFindings("Pod", "web", "payments", spec, nil)

	if _, ok := findingFor(findings, rulePrivileged, "app"); !ok {
		t.Fatal("the privileged container must fire")
	}
	if _, ok := findingFor(findings, rulePrivileged, "sidecar"); ok {
		t.Fatal("a container with no privileged flag must not fire")
	}
}

func TestPodSpecPostureFindingsInitAndEphemeralContainersEvaluatedTheSame(t *testing.T) {
	// An init container that ran and exited still touched the node while it
	// did, so it is not a lesser finding for having finished — see the file
	// header. Only its label should differ.
	spec := podSpecFields{
		InitContainers: []postureContainer{
			{Name: "prep", SecurityContext: &postureSecurityContext{Privileged: boolPtr(true)}},
		},
		EphemeralContainers: []postureContainer{
			{Name: "debugger", SecurityContext: &postureSecurityContext{Privileged: boolPtr(true)}},
		},
	}
	findings := podSpecPostureFindings("Pod", "web", "payments", spec, nil)

	if _, ok := findingFor(findings, rulePrivileged, "init:prep"); !ok {
		t.Fatal("a privileged init container must fire, labelled init:<name>")
	}
	if _, ok := findingFor(findings, rulePrivileged, "ephemeral:debugger"); !ok {
		t.Fatal("a privileged ephemeral container must fire, labelled ephemeral:<name>")
	}
}

/* --------------------------------------------------------- namespace policy --- */

func TestNamespaceNetworkPolicyFinding(t *testing.T) {
	if finding := namespaceNetworkPolicyFinding("payments", 0, false); finding != nil {
		t.Fatal("an empty namespace with nothing running in it must not be a finding")
	}
	if finding := namespaceNetworkPolicyFinding("payments", 1, true); finding != nil {
		t.Fatal("a namespace that has a NetworkPolicy must not fire, regardless of what runs there")
	}
	finding := namespaceNetworkPolicyFinding("payments", 0, true)
	if finding == nil {
		t.Fatal("a namespace with workloads and zero NetworkPolicies must fire")
	}
	if finding.Kind != "Namespace" || finding.Name != "payments" {
		t.Fatalf("finding = %+v, want it addressed at the namespace itself", finding)
	}
}

/* --------------------------------------------------------------- ranking --- */

func TestSortPostureFindingsOrdersByPermitsNotByCount(t *testing.T) {
	findings := []postureFinding{
		newPostureFinding(ruleNoResourceLimits, "Pod", "a", "ns", "", "f", "m"),
		newPostureFinding(ruleNoResourceLimits, "Pod", "b", "ns", "", "f", "m"),
		newPostureFinding(ruleNoResourceLimits, "Pod", "c", "ns", "", "f", "m"),
		// One rule with a higher rank must sort ahead of three lower-ranked
		// ones, however many of those there are — the roadmap's "ordered by
		// what they permit rather than by count" as a literal assertion.
		newPostureFinding(rulePrivileged, "Pod", "d", "ns", "app", "f", "m"),
	}
	sortPostureFindings(findings)

	if findings[0].Rule != string(rulePrivileged) {
		t.Fatalf("the single highest-permits finding must sort first, got %+v", findings[0])
	}
	for i := 1; i < len(findings); i++ {
		if findings[i].Permits > findings[i-1].Permits {
			t.Fatalf("findings are not sorted by permits descending: %+v", findings)
		}
	}
}

func TestPostureRulesPermitsAreStrictlyOrderedAsDocumented(t *testing.T) {
	// The relative order asserted in prose on postureRuleInfo.Permits, pinned so
	// a future edit to one rule's rank cannot silently invert another's.
	order := []postureRuleID{
		rulePrivileged, ruleHostNamespace, ruleHostPath,
		ruleNoNetworkPolicy, ruleAutomountDefaultSA, ruleRunAsRootUndeclared, ruleNoResourceLimits,
	}
	for i := 1; i < len(order); i++ {
		if postureRules[order[i-1]].Permits <= postureRules[order[i]].Permits {
			t.Fatalf("%s (permits %d) must outrank %s (permits %d)",
				order[i-1], postureRules[order[i-1]].Permits, order[i], postureRules[order[i]].Permits)
		}
	}
}

/* --------------------------------------------------------------------- PSS --- */

// TestPostureRulesPSSMappingIsExhaustive pins the classification itself: every
// rule declares exactly one of PSS or NotPSSReason, never both and never
// neither — the property that keeps "not a PSS control" readable rather than
// an empty field a caller has to guess the meaning of.
func TestPostureRulesPSSMappingIsExhaustive(t *testing.T) {
	for id, info := range postureRules {
		hasPSS := info.PSS != nil
		hasReason := info.NotPSSReason != ""
		if hasPSS == hasReason {
			t.Fatalf("%s must declare exactly one of PSS or NotPSSReason, got PSS=%v NotPSSReason=%q",
				id, info.PSS, info.NotPSSReason)
		}
	}
}

// TestPostureRulesPSSMapping pins the mapping itself, verified against
// pssDocURL: which four rules are PSS controls, which profile and control
// name each cites, and that the other three are marked absent rather than
// silently blank.
func TestPostureRulesPSSMapping(t *testing.T) {
	cases := []struct {
		rule    postureRuleID
		profile pssProfile
		control string
	}{
		{rulePrivileged, pssProfileBaseline, "Privileged Containers"},
		{ruleHostNamespace, pssProfileBaseline, "Host Namespaces"},
		{ruleHostPath, pssProfileBaseline, "HostPath Volumes"},
		{ruleRunAsRootUndeclared, pssProfileRestricted, "Running as Non-root"},
	}
	for _, test := range cases {
		info := postureRules[test.rule]
		if info.PSS == nil {
			t.Fatalf("%s must cite a PSS control, got none", test.rule)
		}
		if info.PSS.Profile != test.profile || info.PSS.Control != test.control {
			t.Fatalf("%s = {%s, %q}, want {%s, %q}",
				test.rule, info.PSS.Profile, info.PSS.Control, test.profile, test.control)
		}
	}

	for _, rule := range []postureRuleID{ruleNoNetworkPolicy, ruleAutomountDefaultSA, ruleNoResourceLimits} {
		info := postureRules[rule]
		if info.PSS != nil {
			t.Fatalf("%s must not cite a PSS control, got %+v", rule, info.PSS)
		}
		if info.NotPSSReason == "" {
			t.Fatalf("%s must carry a non-empty NotPSSReason", rule)
		}
	}
}

// A finding must carry the same PSS-covered-or-not statement as its rule, on
// the wire — never an empty PSSProfile/PSSControl a caller could mistake for
// "not populated" rather than "deliberately not a PSS control".
func TestNewPostureFindingCarriesPSSMapping(t *testing.T) {
	covered := newPostureFinding(rulePrivileged, "Pod", "web", "payments", "app", "f", "m")
	if !covered.PSSCovered || covered.PSSProfile != "baseline" || covered.PSSControl != "Privileged Containers" {
		t.Fatalf("a PSS-covered rule's finding must carry PSSCovered/PSSProfile/PSSControl, got %+v", covered)
	}
	if covered.PSSNote != "" {
		t.Fatalf("a PSS-covered finding must not also carry a PSSNote, got %q", covered.PSSNote)
	}

	notCovered := newPostureFinding(ruleNoResourceLimits, "Pod", "web", "payments", "app", "f", "m")
	if notCovered.PSSCovered {
		t.Fatalf("a non-PSS rule's finding must have PSSCovered = false, got %+v", notCovered)
	}
	if notCovered.PSSProfile != "" || notCovered.PSSControl != "" {
		t.Fatalf("a non-PSS finding must not carry a profile or control, got %+v", notCovered)
	}
	if notCovered.PSSNote == "" {
		t.Fatal("a non-PSS finding must carry a non-empty PSSNote explaining why")
	}
}

// The PSS profile must never become a second sort key: two rules with the
// same Permits but different PSS coverage must stay in identity order, and a
// higher-Permits non-PSS rule must still outrank a lower-Permits PSS one —
// the ranking axis and the citation axis are asserted independently.
func TestSortPostureFindingsDoesNotSortByPSSProfile(t *testing.T) {
	findings := []postureFinding{
		// no_resource_limits (not a PSS control, Permits 10)
		newPostureFinding(ruleNoResourceLimits, "Pod", "a", "ns", "app", "f", "m"),
		// no_nonroot_declaration (PSS restricted, Permits 30) — outranks the
		// finding above purely on Permits, even though one cites PSS and
		// ranks lower here on the list and the other does not cite PSS at all.
	}
	higher := newPostureFinding(ruleRunAsRootUndeclared, "Pod", "b", "ns", "app", "f", "m")
	findings = append(findings, higher)
	sortPostureFindings(findings)

	if findings[0].Rule != string(ruleRunAsRootUndeclared) {
		t.Fatalf("the higher-Permits finding must sort first regardless of PSS coverage, got %+v", findings[0])
	}
}

/* -------------------------------------------------------- acknowledgement --- */

func TestApplyPostureAcknowledgementsMarksWithoutRemoving(t *testing.T) {
	acked := newPostureFinding(rulePrivileged, "Pod", "debug-tools", "sandbox", "app", "f", "m")
	unacked := newPostureFinding(rulePrivileged, "Pod", "other", "sandbox", "app", "f", "m")
	findings := []postureFinding{acked, unacked}

	index := indexPostureAcknowledgements([]db.PostureAcknowledgement{{
		Kind: "Pod", Namespace: "sandbox", Name: "debug-tools", Rule: string(rulePrivileged),
		Reason: "hardware test", AckedBy: "alice",
	}})

	applyPostureAcknowledgements(findings, index)

	if !findings[0].Acknowledged || findings[0].AckReason != "hardware test" || findings[0].AckBy != "alice" {
		t.Fatalf("the matching finding must be marked acknowledged with its reason and author, got %+v", findings[0])
	}
	if findings[1].Acknowledged {
		t.Fatal("a finding on a different object must not be marked acknowledged")
	}
	// The finding is still present — an acknowledgement marks, it never
	// removes. Both entries above surviving the call is that assertion.
	if len(findings) != 2 {
		t.Fatalf("applyPostureAcknowledgements must not change the finding count, got %d", len(findings))
	}
}

/* --------------------------------------------------------------- helpers --- */

func hasRule(findings []postureFinding, rule postureRuleID) bool {
	for _, f := range findings {
		if f.Rule == string(rule) {
			return true
		}
	}
	return false
}

func findingFor(findings []postureFinding, rule postureRuleID, container string) (postureFinding, bool) {
	for _, f := range findings {
		if f.Rule == string(rule) && f.Container == container {
			return f, true
		}
	}
	return postureFinding{}, false
}

func containsAll(s string, substrings ...string) bool {
	for _, sub := range substrings {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}

/* ---------------------------------------------------------------- routes --- */

// The scan is a live-cluster read, so a direct-mode cluster — no agent tunnel
// to read through — refuses it exactly as it refuses every other resource
// route.
func TestPostureScanRefusesDirectModeCluster(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")
	cluster := env.store.addCluster("legacy", "dev") // direct mode
	token := env.tokenFor(t, admin)

	rec := env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/posture?namespace=default", token, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status %d for a direct-mode cluster, got %d (%s)",
			http.StatusConflict, rec.Code, rec.Body.String())
	}
}

// Acknowledging is gated above plain view access — see requirePostureWriteGrant
// — so a view-only grant is refused before anything is written.
func TestAcknowledgePostureFindingRefusesViewGrant(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("viewer", "secret123", "user")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	env.store.grant(user.ID, cluster.ID, "view", nil)
	token := env.tokenFor(t, user)

	rec := env.do(t, http.MethodPost, "/api/v1/clusters/"+itoa(cluster.ID)+"/resources/posture/ack", token,
		map[string]any{
			"kind": "Pod", "namespace": "payments", "name": "debug-tools",
			"rule": string(rulePrivileged), "reason": "runs privileged on purpose for a hardware test",
		})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d for a view-only grant, got %d (%s)",
			http.StatusForbidden, rec.Code, rec.Body.String())
	}
}

// An acknowledgement with no reason is a mute button, not an audit-able
// decision — refused before it reaches the store.
func TestAcknowledgePostureFindingRequiresAReason(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("editor", "secret123", "user")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	env.store.grant(user.ID, cluster.ID, "edit", nil)
	token := env.tokenFor(t, user)

	rec := env.do(t, http.MethodPost, "/api/v1/clusters/"+itoa(cluster.ID)+"/resources/posture/ack", token,
		map[string]any{
			"kind": "Pod", "namespace": "payments", "name": "debug-tools", "rule": string(rulePrivileged),
		})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d for a missing reason, got %d (%s)",
			http.StatusBadRequest, rec.Code, rec.Body.String())
	}
}

// An edit grant acknowledging a finding round-trips through the fake store and
// is refusable a second time (unacknowledge), on the natural key rather than a
// surrogate id the caller never saw.
func TestAcknowledgeAndUnacknowledgePostureFindingRoundTrips(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("editor", "secret123", "user")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	env.store.grant(user.ID, cluster.ID, "edit", nil)
	token := env.tokenFor(t, user)

	body := map[string]any{
		"kind": "Pod", "namespace": "payments", "name": "debug-tools",
		"rule": string(rulePrivileged), "reason": "runs privileged on purpose for a hardware test",
	}
	rec := env.do(t, http.MethodPost, "/api/v1/clusters/"+itoa(cluster.ID)+"/resources/posture/ack", token, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("acknowledge: expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	ack := decode[map[string]any](t, rec)
	if ack["acked_by"] != "editor" {
		t.Fatalf("acked_by = %v, want the acknowledging user", ack["acked_by"])
	}

	del := env.do(t, http.MethodDelete,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/posture/ack"+
			"?kind=Pod&namespace=payments&name=debug-tools&rule="+string(rulePrivileged),
		token, nil)
	if del.Code != http.StatusNoContent {
		t.Fatalf("unacknowledge: expected status %d, got %d (%s)", http.StatusNoContent, del.Code, del.Body.String())
	}

	// Removing an acknowledgement that no longer exists is a 404, not a
	// silent no-op.
	again := env.do(t, http.MethodDelete,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/posture/ack"+
			"?kind=Pod&namespace=payments&name=debug-tools&rule="+string(rulePrivileged),
		token, nil)
	if again.Code != http.StatusNotFound {
		t.Fatalf("expected status %d for removing an already-removed acknowledgement, got %d (%s)",
			http.StatusNotFound, again.Code, again.Body.String())
	}
}

// An unknown rule id is refused rather than silently stored — a typo in the
// rule field must not create an acknowledgement that no scan will ever match.
func TestAcknowledgePostureFindingRefusesUnknownRule(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("editor", "secret123", "user")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	env.store.grant(user.ID, cluster.ID, "edit", nil)
	token := env.tokenFor(t, user)

	rec := env.do(t, http.MethodPost, "/api/v1/clusters/"+itoa(cluster.ID)+"/resources/posture/ack", token,
		map[string]any{
			"kind": "Pod", "namespace": "payments", "name": "debug-tools",
			"rule": "not_a_real_rule", "reason": "because",
		})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d for an unknown rule, got %d (%s)",
			http.StatusBadRequest, rec.Code, rec.Body.String())
	}
}
