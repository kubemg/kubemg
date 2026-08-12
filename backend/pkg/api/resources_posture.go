package api

import (
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * Workload security posture, derived from what is already read.
 *
 * Every field this reads — a container's `securityContext`, a volume's
 * `hostPath`, a pod's `hostNetwork`/`hostPID`, a container's `resources.limits`,
 * a ServiceAccount's and a pod's `automountServiceAccountToken`, and whether a
 * namespace has a NetworkPolicy at all — sits on an object Explore's own lists
 * already fetch. This file adds no permission and no dependency: it widens the
 * *decode* of those same objects and evaluates seven fixed rules over the
 * result.
 *
 * Where the read lives is a deliberate choice. `podObject` (resources.go) and
 * `collectWorkloads` (resources.go) decode only what their list views show, and
 * every caller of those two functions is a route somebody has open right now —
 * Explore's pod and workload tables. Widening them to carry every field this
 * scan needs would put that weight on every list read, forever, to serve a page
 * nobody may ever open. So this is its own read, of the same paths, done once
 * per posture scan rather than on every list — "derived from what is already
 * read" is honoured at the level of *which objects and fields*, not by literally
 * reusing the already-narrower list decoders.
 *
 * KubeMG does **not** become a vulnerability scanner here. There is no image
 * registry credential, no CVE feed, and nothing here inspects image content —
 * only the manifest fields the API server already served. Where a workload's
 * image matters to a reader, the response names it as a string and the
 * `registry` console link (pkg/db/consoles.go) is where a cluster can say what
 * already scans it; this package does not compute or guess that link itself.
 *
 * Findings are ranked by what they **permit**, not counted, per rule below
 * (postureRules). The ranking is asserted once, as a property of the rule, so
 * every caller (the HTTP response, a future export) sorts identically without
 * recomputing a severity heuristic of its own.
 *
 * Two rules deserve their own note, because getting either backwards is exactly
 * the kind of silent wrong answer this feature exists to avoid:
 *
 *   - "runAsRoot with no securityContext" is phrased, and evaluated, as an
 *     absence: nothing here declares a non-root user. It is **not** a claim that
 *     the container runs as root — the image's own `USER` decides that, and
 *     KubeMG cannot see the image. declaresNonRoot below returns whether
 *     anything rules root *out*, and the finding fires on that being false.
 *   - Init and ephemeral containers are evaluated identically to the main
 *     containers for privilege, hostPath exposure and resource limits, because
 *     a privileged init container still touched the node while it ran — running
 *     first and exiting sooner does not make its manifest fact smaller. Their
 *     `Container` label is prefixed (`init:`, `ephemeral:`) so a reader can tell
 *     which kind fired without it changing the rank.
 *
 * A Deployment's replicas share one pod template, so this evaluates the
 * template **once per workload** rather than once per running pod — ten
 * identical replicas produce one finding, not ten, which is what keeps the list
 * readable at any fan-out. A Pod with no owner (nothing in
 * `metadata.ownerReferences`) has no template to read instead, so it is
 * evaluated directly; an owned pod is skipped here because its owning
 * Deployment/StatefulSet/DaemonSet was already evaluated.
 */

/* -------------------------------------------------------------- rule engine --- */

// postureRuleID is one of the seven fixed rules. There is no eighth: adding one
// means adding both the read that feeds it and its place in postureRules below.
type postureRuleID string

const (
	rulePrivileged          postureRuleID = "privileged_container"
	ruleHostNamespace       postureRuleID = "host_namespace"
	ruleHostPath            postureRuleID = "hostpath_volume"
	ruleNoNetworkPolicy     postureRuleID = "namespace_no_network_policy"
	ruleAutomountDefaultSA  postureRuleID = "automount_default_service_account"
	ruleRunAsRootUndeclared postureRuleID = "no_nonroot_declaration"
	ruleNoResourceLimits    postureRuleID = "no_resource_limits"
)

// postureRuleInfo is what a rule always says about itself, independent of any
// particular finding.
type postureRuleInfo struct {
	Title string
	// Permits ranks the rule by what a firing *permits* a workload to do,
	// highest first — the ordering the roadmap asks for instead of a count.
	// The scale is deliberately coarse and asserted here, once, in prose:
	//
	//   100  privileged           — owns the node: every capability root on the
	//                                host has, including reconfiguring the kernel.
	//    90  host namespace       — shares the node's network or process space:
	//                                sees and can touch every other pod there.
	//    80  hostPath             — arbitrary node filesystem access, bounded
	//                                only by the mount and the container's own
	//                                privilege rather than by policy.
	//    55  no NetworkPolicy     — every pod in the namespace is reachable from
	//                                wherever the cluster's network allows,
	//                                since nothing here narrows it.
	//    45  automounted default  — any process in the container can present
	//        SA token               this identity to the API server, whatever it
	//                                turns out to be bound to.
	//    30  no non-root decl.    — genuinely uncertain: the image may already
	//                                run non-root, this just does not say so.
	//    10  no resource limits   — a noisy neighbour, not an escape: it can
	//                                starve the node, not leave the pod.
	Permits int
}

var postureRules = map[postureRuleID]postureRuleInfo{
	rulePrivileged:          {Title: "Privileged container", Permits: 100},
	ruleHostNamespace:       {Title: "Shares a host namespace", Permits: 90},
	ruleHostPath:            {Title: "hostPath volume", Permits: 80},
	ruleNoNetworkPolicy:     {Title: "No NetworkPolicy in this namespace", Permits: 55},
	ruleAutomountDefaultSA:  {Title: "Default ServiceAccount token automounted", Permits: 45},
	ruleRunAsRootUndeclared: {Title: "No non-root user declared", Permits: 30},
	ruleNoResourceLimits:    {Title: "No resource limits", Permits: 10},
}

// postureFinding is one rule firing on one object (or, for the namespace rule,
// on the namespace itself). Field names the manifest path that produced it, so
// a reader knows exactly what to look at without KubeMG asserting it can
// scroll there for them.
type postureFinding struct {
	Rule    string `json:"rule"`
	Title   string `json:"title"`
	Permits int    `json:"permits"`

	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
	// Container is empty for a pod/namespace-level finding, and prefixed
	// `init:`/`ephemeral:` when the firing container is not one of the main
	// ones — see the file header.
	Container string `json:"container,omitempty"`

	Field   string `json:"field"`
	Message string `json:"message"`

	Acknowledged bool       `json:"acknowledged"`
	AckReason    string     `json:"ack_reason,omitempty"`
	AckBy        string     `json:"ack_by,omitempty"`
	AckAt        *time.Time `json:"ack_at,omitempty"`
}

func newPostureFinding(rule postureRuleID, kind, name, namespace, container, field, message string) postureFinding {
	info := postureRules[rule]
	return postureFinding{
		Rule: string(rule), Title: info.Title, Permits: info.Permits,
		Kind: kind, Name: name, Namespace: namespace, Container: container,
		Field: field, Message: message,
	}
}

// sortPostureFindings orders by what a finding permits, highest first — the
// ranking requirement — and only falls back to identity fields to make the
// order stable, never to re-rank by anything resembling a count.
func sortPostureFindings(findings []postureFinding) {
	sort.SliceStable(findings, func(i, j int) bool {
		a, b := findings[i], findings[j]
		if a.Permits != b.Permits {
			return a.Permits > b.Permits
		}
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.Container < b.Container
	})
}

/* ------------------------------------------------------------------- wire --- */

// postureVolume is the one shape of `spec.volumes[]` this reads: whether it is
// a hostPath at all.
type postureVolume struct {
	Name     string `json:"name"`
	HostPath *struct {
		Path string `json:"path"`
	} `json:"hostPath"`
}

// postureSecurityContext is the slice of a `securityContext` — pod- or
// container-level — every rule here needs. `Privileged` only ever appears on a
// container's; a pod-level one decoding it simply finds nothing there.
type postureSecurityContext struct {
	RunAsNonRoot *bool  `json:"runAsNonRoot"`
	RunAsUser    *int64 `json:"runAsUser"`
	Privileged   *bool  `json:"privileged"`
}

type postureContainer struct {
	Name            string                  `json:"name"`
	SecurityContext *postureSecurityContext `json:"securityContext"`
	Resources       struct {
		Limits map[string]string `json:"limits"`
	} `json:"resources"`
}

// podSpecFields is the slice of a PodSpec every rule reads. A bare Pod's own
// `spec` and a Deployment/StatefulSet/DaemonSet's `spec.template.spec` are
// byte-for-byte this same shape in the Kubernetes API, so one decode target —
// used for both — is what lets podSpecPostureFindings not care which one it was
// handed.
type podSpecFields struct {
	HostNetwork                  bool                    `json:"hostNetwork"`
	HostPID                      bool                    `json:"hostPID"`
	ServiceAccountName           string                  `json:"serviceAccountName"`
	AutomountServiceAccountToken *bool                   `json:"automountServiceAccountToken"`
	SecurityContext              *postureSecurityContext `json:"securityContext"`
	Volumes                      []postureVolume         `json:"volumes"`
	Containers                   []postureContainer      `json:"containers"`
	InitContainers               []postureContainer      `json:"initContainers"`
	EphemeralContainers          []postureContainer      `json:"ephemeralContainers"`
}

// posturePodMeta is the metadata a posture pass needs off a bare Pod: enough to
// place a finding, and `ownerReferences` to tell a naked pod (evaluated
// directly) apart from one owned by a workload (evaluated through its
// template instead — see the file header for why owned pods are skipped here).
type posturePodMeta struct {
	Name              string    `json:"name"`
	Namespace         string    `json:"namespace"`
	CreationTimestamp time.Time `json:"creationTimestamp"`
	OwnerReferences   []struct {
		Kind string `json:"kind"`
	} `json:"ownerReferences"`
}

type posturePod struct {
	Metadata posturePodMeta `json:"metadata"`
	Spec     podSpecFields  `json:"spec"`
}

func (p posturePod) ownerless() bool { return len(p.Metadata.OwnerReferences) == 0 }

// postureWorkload is the slice of a Deployment/StatefulSet/DaemonSet this
// reads: its pod template's spec.
type postureWorkload struct {
	Metadata objectMeta `json:"metadata"`
	Spec     struct {
		Template struct {
			Spec podSpecFields `json:"spec"`
		} `json:"template"`
	} `json:"spec"`
}

// postureServiceAccount is the one field of a ServiceAccount the automount rule
// needs beyond its identity.
type postureServiceAccount struct {
	Metadata objectMeta `json:"metadata"`
	// AutomountServiceAccountToken mirrors serviceAccountView.AutomountToken:
	// nil is the common case and means the ServiceAccount itself declares
	// nothing, leaving the pod spec to decide (see automountEffective).
	AutomountServiceAccountToken *bool `json:"automountServiceAccountToken"`
}

/* ------------------------------------------------------------ rule bodies --- */

// taggedContainer pairs a container with the label a finding should show —
// its own name, or an `init:`/`ephemeral:`-prefixed one.
type taggedContainer struct {
	label     string
	container postureContainer
}

func taggedContainers(prefix string, containers []postureContainer) []taggedContainer {
	out := make([]taggedContainer, 0, len(containers))
	for _, c := range containers {
		out = append(out, taggedContainer{label: prefix + c.Name, container: c})
	}
	return out
}

// automountEffective applies Kubernetes' own precedence for whether a
// ServiceAccount's token is mounted into a pod: the pod's own setting wins when
// present, the ServiceAccount's is the fallback, and the documented default
// when *neither* says anything is that it **is** mounted.
func automountEffective(podSetting, saSetting *bool) bool {
	if podSetting != nil {
		return *podSetting
	}
	if saSetting != nil {
		return *saSetting
	}
	return true
}

// declaresNonRoot reports whether anything — the container's own
// securityContext, falling back to the pod's — rules root *out*. It does not,
// and cannot, report whether the container runs as root: that is the image's
// `USER` directive, which this never reads. See the file header.
func declaresNonRoot(podSC, containerSC *postureSecurityContext) bool {
	if containerSC != nil {
		if containerSC.RunAsNonRoot != nil {
			return *containerSC.RunAsNonRoot
		}
		if containerSC.RunAsUser != nil {
			return *containerSC.RunAsUser != 0
		}
	}
	if podSC != nil {
		if podSC.RunAsNonRoot != nil {
			return *podSC.RunAsNonRoot
		}
		if podSC.RunAsUser != nil {
			return *podSC.RunAsUser != 0
		}
	}
	return false
}

// noLimitsDeclared reports the literal rule text: no CPU limit and no memory
// limit at all, rather than firing on either alone — a container missing just
// one is a narrower, noisier finding this rule deliberately does not raise.
func noLimitsDeclared(limits map[string]string) bool {
	if len(limits) == 0 {
		return true
	}
	_, cpu := limits["cpu"]
	_, memory := limits["memory"]
	return !cpu && !memory
}

// podSpecPostureFindings evaluates every pod-spec-level rule against one
// workload's effective spec — a bare Pod's own spec, or a workload's pod
// template, which podSpecFields makes indistinguishable to this function on
// purpose. saAutomount is the ServiceAccount's own automountServiceAccountToken
// setting (nil when it could not be read, or declares nothing), needed for the
// precedence automountEffective applies; the caller resolves it once per
// namespace rather than this function reading a ServiceAccount list itself,
// which is what keeps this a pure, table-testable function.
func podSpecPostureFindings(kind, name, namespace string, spec podSpecFields, saAutomount *bool) []postureFinding {
	var out []postureFinding

	if spec.HostNetwork {
		out = append(out, newPostureFinding(ruleHostNamespace, kind, name, namespace, "", "spec.hostNetwork",
			"Shares the node's network namespace: it can bind any port on the node and observe traffic on "+
				"every interface, not only the ports it declares."))
	}
	if spec.HostPID {
		out = append(out, newPostureFinding(ruleHostNamespace, kind, name, namespace, "", "spec.hostPID",
			"Shares the node's process namespace: any container here can see and signal every process on "+
				"the node, including inside other pods' containers."))
	}

	for _, volume := range spec.Volumes {
		if volume.HostPath == nil {
			continue
		}
		path := volume.HostPath.Path
		if path == "" {
			path = "/"
		}
		out = append(out, newPostureFinding(ruleHostPath, kind, name, namespace, "",
			fmt.Sprintf("spec.volumes[%s].hostPath.path", volume.Name),
			fmt.Sprintf("The %q volume mounts %s from the node's own filesystem. Whatever a container with "+
				"this mount can write is there for every other pod later scheduled on the same node.",
				volume.Name, path)))
	}

	// The automount rule fires only for the "default" ServiceAccount — named
	// explicitly or left empty, which the API server resolves to "default" —
	// because a workload naming its own ServiceAccount has made a deliberate
	// identity choice. This is about the token that gets mounted silently when
	// a workload asks for no identity at all.
	saName := spec.ServiceAccountName
	if saName == "" {
		saName = "default"
	}
	if saName == "default" && automountEffective(spec.AutomountServiceAccountToken, saAutomount) {
		out = append(out, newPostureFinding(ruleAutomountDefaultSA, kind, name, namespace, "",
			"spec.automountServiceAccountToken",
			"Runs as the namespace's default ServiceAccount and mounts its token. Any process in any "+
				"container here can present that identity to the API server — whatever it turns out to be "+
				"bound to — without the workload having asked for it by name."))
	}

	containers := taggedContainers("", spec.Containers)
	containers = append(containers, taggedContainers("init:", spec.InitContainers)...)
	containers = append(containers, taggedContainers("ephemeral:", spec.EphemeralContainers)...)

	for _, tc := range containers {
		c := tc.container
		field := fmt.Sprintf("spec.containers[%s]", tc.label)

		if c.SecurityContext != nil && c.SecurityContext.Privileged != nil && *c.SecurityContext.Privileged {
			out = append(out, newPostureFinding(rulePrivileged, kind, name, namespace, tc.label,
				field+".securityContext.privileged",
				fmt.Sprintf("The %q container runs privileged: it has every capability the node's own root "+
					"has, including reconfiguring the kernel and reaching every device on the host.", tc.label)))
		}

		if !declaresNonRoot(spec.SecurityContext, c.SecurityContext) {
			out = append(out, newPostureFinding(ruleRunAsRootUndeclared, kind, name, namespace, tc.label,
				field+".securityContext",
				fmt.Sprintf("Nothing in the pod's or the %q container's securityContext declares "+
					"runAsNonRoot or a non-zero runAsUser. This does not mean the container runs as root — "+
					"the image's own USER decides that, and KubeMG cannot see the image — only that nothing "+
					"here rules it out.", tc.label)))
		}

		if noLimitsDeclared(c.Resources.Limits) {
			out = append(out, newPostureFinding(ruleNoResourceLimits, kind, name, namespace, tc.label,
				field+".resources.limits",
				fmt.Sprintf("The %q container declares no CPU or memory limit: it can consume as much of "+
					"the node as is free, at the expense of whatever else is scheduled there.", tc.label)))
		}
	}

	return out
}

// namespaceNetworkPolicyFinding is the one rule that is about the namespace
// rather than an object in it. It only fires where there is something to be
// wide open — an empty namespace with zero NetworkPolicies is not a finding,
// it is an empty namespace.
func namespaceNetworkPolicyFinding(namespace string, policyCount int, hasWorkload bool) *postureFinding {
	if !hasWorkload || policyCount > 0 {
		return nil
	}
	finding := newPostureFinding(ruleNoNetworkPolicy, "Namespace", namespace, "", "", "networkpolicies",
		fmt.Sprintf("No NetworkPolicy exists in %q. Every pod here is reachable from wherever the cluster's "+
			"network otherwise allows, since nothing here narrows it.", namespace))
	return &finding
}

/* ---------------------------------------------------------------- scanning --- */

// maxPostureScanObjects bounds the total number of workload templates and bare
// pods one posture scan will evaluate. A cluster with twenty thousand pods must
// not produce an unbounded response; past this the scan stops adding findings
// and says so on the wire (postureView.Truncated) rather than either hanging or
// silently reporting an incomplete cluster as a complete one.
const maxPostureScanObjects = 4000

// maxPostureFindings caps the findings actually returned, independent of how
// many objects were scanned — a cluster that is genuinely this permissive
// everywhere should not turn a bounded scan into an unbounded response.
const maxPostureFindings = 1000

// postureReadGap names one read this scan could not complete, and why. Several
// kinds are read to build one answer, and the grant that can list Deployments
// is not guaranteed to also list ServiceAccounts or NetworkPolicies — a
// forbidden read here narrows the scan's coverage rather than failing the
// whole request, on the same convention resources_networkpolicy.go's
// fetchDegrading already establishes.
type postureReadGap struct {
	Resource string `json:"resource"`
	Reason   string `json:"reason"`
}

// postureView is the whole answer: per cluster when the scope is every granted
// namespace, per namespace when it names one, using the same `namespace` /
// `all_namespaces` shape every other resource list already carries.
type postureView struct {
	Namespace     string `json:"namespace"`
	AllNamespaces bool   `json:"all_namespaces"`

	Findings []postureFinding `json:"findings"`

	ScannedWorkloads int  `json:"scanned_workloads"`
	ScannedPods      int  `json:"scanned_pods"`
	Truncated        bool `json:"truncated"`
	FindingsCapped   bool `json:"findings_capped,omitempty"`

	Unavailable []postureReadGap `json:"unavailable,omitempty"`

	// Disclaimer and NonGoalNotice both travel on the wire rather than living
	// only in this file's comments — the roadmap is explicit that the
	// non-scanning boundary is part of the feature, not a footnote for
	// whoever reads the Go source.
	Disclaimer    string `json:"disclaimer"`
	NonGoalNotice string `json:"non_goal_notice"`
}

const postureDisclaimer = "Every finding here names the manifest field that produced it and is derived only from " +
	"objects Explore already lists — no new cluster permission and no new read path."

const postureNonGoalNotice = "This is not a vulnerability scanner: KubeMG holds no image registry credential and " +
	"no CVE feed, and nothing here inspects an image's contents, only manifest fields the API server already " +
	"served. An image's known vulnerabilities belong to whatever already scans your registry — see this " +
	"cluster's registry console link, if one is registered, rather than a guess KubeMG would have to make."

// postureAckKey identifies one acknowledgement's natural key, shared between
// the scan (to annotate findings) and the write handlers (to upsert/delete
// them) so the two can never disagree about what identifies a finding.
func postureAckKey(kind, namespace, name, rule string) string {
	return kind + "\x00" + namespace + "\x00" + name + "\x00" + rule
}

// indexPostureAcknowledgements builds the lookup a scan applies to its own
// findings.
func indexPostureAcknowledgements(acks []db.PostureAcknowledgement) map[string]db.PostureAcknowledgement {
	out := make(map[string]db.PostureAcknowledgement, len(acks))
	for _, ack := range acks {
		out[postureAckKey(ack.Kind, ack.Namespace, ack.Name, ack.Rule)] = ack
	}
	return out
}

// applyPostureAcknowledgements marks the findings an acknowledgement covers.
// The finding is never dropped — it stays in the list, ranked exactly where it
// would otherwise sort, carrying who accepted it and why. That is the whole
// design point: an acknowledgement is visible as one, not a deletion.
func applyPostureAcknowledgements(findings []postureFinding, index map[string]db.PostureAcknowledgement) {
	for i := range findings {
		f := &findings[i]
		ack, found := index[postureAckKey(f.Kind, f.Namespace, f.Name, f.Rule)]
		if !found {
			continue
		}
		f.Acknowledged = true
		f.AckReason = ack.Reason
		f.AckBy = ack.AckedBy
		at := ack.UpdatedAt
		f.AckAt = &at
	}
}

// postureScan answers the workload security posture for one cluster, scoped
// exactly like every other resource list: one namespace, or every namespace
// the caller's grant covers (respecting a namespace-scoped grant's own list —
// resourceScope already refuses to fan out past maxFanOut namespaces, which
// bounds the number of reads the same way it bounds every other all-namespaces
// list here).
func (s *server) postureScan(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	scope, ok := s.resourceScope(c, grant)
	if !ok {
		return
	}

	acks, err := s.store.ListPostureAcknowledgements(c.Request.Context(), cluster.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not read acknowledgements for this cluster"})
		return
	}
	ackIndex := indexPostureAcknowledgements(acks)

	view := postureView{
		Namespace: scope.Namespace, AllNamespaces: scope.All,
		Findings: []postureFinding{}, Unavailable: []postureReadGap{},
		Disclaimer: postureDisclaimer, NonGoalNotice: postureNonGoalNotice,
	}

	// ServiceAccounts and NetworkPolicies are read first: several rules
	// evaluated per-object below need to know, for the object's own namespace,
	// what its ServiceAccount declares and how many policies exist there.
	// Reading them once up front — rather than once per workload — is what
	// keeps this at one read per resource kind per namespace, the same cost
	// every other list read here already pays.
	saAutomount := map[string]map[string]*bool{}
	for _, path := range scope.paths(resourceListPath{"/api/v1", "serviceaccounts"}) {
		var list struct {
			Items []postureServiceAccount `json:"items"`
		}
		available, reason, callOK := s.fetchDegrading(c, user, cluster, grant, path, &list)
		if !callOK {
			return
		}
		if !available {
			view.Unavailable = appendReadGapOnce(view.Unavailable, "serviceaccounts", reason)
			continue
		}
		for _, item := range list.Items {
			ns := item.Metadata.Namespace
			if saAutomount[ns] == nil {
				saAutomount[ns] = map[string]*bool{}
			}
			saAutomount[ns][item.Metadata.Name] = item.AutomountServiceAccountToken
		}
	}

	netpolCount := map[string]int{}
	for _, path := range scope.paths(resourceListPath{networkPolicyGroup, "networkpolicies"}) {
		var list struct {
			Items []struct {
				Metadata objectMeta `json:"metadata"`
			} `json:"items"`
		}
		available, reason, callOK := s.fetchDegrading(c, user, cluster, grant, path, &list)
		if !callOK {
			return
		}
		if !available {
			view.Unavailable = appendReadGapOnce(view.Unavailable, "networkpolicies", reason)
			continue
		}
		for _, item := range list.Items {
			netpolCount[item.Metadata.Namespace]++
		}
	}

	var findings []postureFinding
	scanned := 0
	hasWorkload := map[string]bool{}

	for _, kind := range workloadKinds {
		if scanned >= maxPostureScanObjects {
			view.Truncated = true
			break
		}
		for _, path := range scope.paths(resourceListPath{"/apis/apps/v1", kind.resource}) {
			var list struct {
				Items []postureWorkload `json:"items"`
			}
			available, reason, callOK := s.fetchDegrading(c, user, cluster, grant, path, &list)
			if !callOK {
				return
			}
			if !available {
				view.Unavailable = appendReadGapOnce(view.Unavailable, kind.resource, reason)
				continue
			}
			for _, item := range list.Items {
				if scanned >= maxPostureScanObjects {
					view.Truncated = true
					break
				}
				scanned++
				view.ScannedWorkloads++
				ns := item.Metadata.Namespace
				hasWorkload[ns] = true
				sa := saAutomount[ns][effectiveServiceAccountName(item.Spec.Template.Spec.ServiceAccountName)]
				findings = append(findings,
					podSpecPostureFindings(kind.kind, item.Metadata.Name, ns, item.Spec.Template.Spec, sa)...)
			}
		}
	}

	for _, path := range scope.paths(resourceListPath{"/api/v1", "pods"}) {
		if scanned >= maxPostureScanObjects {
			view.Truncated = true
			break
		}
		var list struct {
			Items []posturePod `json:"items"`
		}
		available, reason, callOK := s.fetchDegrading(c, user, cluster, grant, path, &list)
		if !callOK {
			return
		}
		if !available {
			view.Unavailable = appendReadGapOnce(view.Unavailable, "pods", reason)
			continue
		}
		for _, item := range list.Items {
			ns := item.Metadata.Namespace
			hasWorkload[ns] = true
			if !item.ownerless() {
				// Owned by a Deployment/StatefulSet/DaemonSet already
				// evaluated above through its template — see the file header.
				continue
			}
			if scanned >= maxPostureScanObjects {
				view.Truncated = true
				break
			}
			scanned++
			view.ScannedPods++
			sa := saAutomount[ns][effectiveServiceAccountName(item.Spec.ServiceAccountName)]
			findings = append(findings, podSpecPostureFindings("Pod", item.Metadata.Name, ns, item.Spec, sa)...)
		}
	}

	// The namespace-level rule, applied once every namespace this scan actually
	// touched (whether through a workload or a bare pod) rather than once per
	// object in it.
	for namespace, present := range hasWorkload {
		if finding := namespaceNetworkPolicyFinding(namespace, netpolCount[namespace], present); finding != nil {
			findings = append(findings, *finding)
		}
	}

	applyPostureAcknowledgements(findings, ackIndex)
	sortPostureFindings(findings)
	if len(findings) > maxPostureFindings {
		findings = findings[:maxPostureFindings]
		view.FindingsCapped = true
	}
	view.Findings = findings

	c.JSON(http.StatusOK, view)
}

// effectiveServiceAccountName resolves the API server's own default: a pod
// naming no ServiceAccount runs as "default".
func effectiveServiceAccountName(name string) string {
	if name == "" {
		return "default"
	}
	return name
}

// appendReadGapOnce records a resource kind's read failure at most once per
// scan — an all-namespaces fan-out would otherwise repeat the same "forbidden"
// reason once per namespace it was refused in, which says nothing a single
// line does not already say.
func appendReadGapOnce(gaps []postureReadGap, resource, reason string) []postureReadGap {
	for _, gap := range gaps {
		if gap.Resource == resource {
			return gaps
		}
	}
	return append(gaps, postureReadGap{Resource: resource, Reason: reason})
}
