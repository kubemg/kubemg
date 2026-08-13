package api

import (
	"math"
	"net/http"
	"slices"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * What this cluster costs, and the two numbers that are not the same number.
 *
 * The capacity report answers what the scheduler has promised away. This prices
 * it — and the first thing it has to be honest about is that **a Kubernetes
 * cluster does not buy pods, it buys nodes**. A node is paid for whole, running
 * or idle, reserved or empty. So there are two totals here and they deliberately
 * do not match:
 *
 *   infrastructure  every node's allocatable capacity at the rate card. This is
 *                   the closest thing here to a bill: it is what the fleet costs
 *                   whether or not anything runs on it.
 *   attributed      every workload's *requests* at the same rate card. This is
 *                   what the workloads have claimed, and it is what a team can
 *                   actually be asked to reduce.
 *
 * The gap between them is unallocated capacity — nodes bought and not claimed —
 * and it is reported as its own line rather than being spread across the
 * workloads. Spreading it is what most showback tools do, and it produces a
 * per-team number that moves when a *different* team scales down, which is the
 * fastest way to make a cost report something nobody trusts.
 *
 * Requests rather than usage is the other deliberate choice. A pod that reserves
 * two cores and burns a tenth of one has cost two cores: the reservation is what
 * kept the scheduler from placing something else there, and it is what made the
 * next node necessary. Usage appears beside it, where metrics-server is
 * installed, as the gap that says the reservation was wrong — which is a
 * right-sizing finding, not a cheaper bill.
 *
 * Everything here is an estimate over rates an operator typed in. There is no
 * billing integration, and `db/rate_cards.go` says why that is a decision.
 * Cluster-wide by nature — a node's price says nothing about a namespace — so a
 * namespace-scoped grant is refused exactly as it is on capacity.
 */

// topCostedWorkloads bounds the workload table. A cluster with four thousand
// Deployments has a cost report nobody scrolls; the namespace rollup beside it
// is the shape that stays readable, and the count of what was left out is
// reported so the table never quietly claims to be everything.
const topCostedWorkloads = 50

// minAttributableMonthly drops workloads costing effectively nothing out of the
// table. A DaemonSet reserving 10m of CPU is a real workload and a rounding
// error in this report, and fifty rows of rounding error is fifty rows nobody
// reads.
const minAttributableMonthly = 0.01

/* ------------------------------------------------------------ the payload -- */

// moneyDimension is one resource's share of a cost, in the rate card's currency
// per month.
type moneyDimension struct {
	CPU    float64 `json:"cpu"`
	Memory float64 `json:"memory"`
	Total  float64 `json:"total"`
}

func (m *moneyDimension) add(other moneyDimension) {
	m.CPU += other.CPU
	m.Memory += other.Memory
	m.Total += other.Total
}

func (m moneyDimension) rounded() moneyDimension {
	return moneyDimension{CPU: money(m.CPU), Memory: money(m.Memory), Total: money(m.Total)}
}

// costedWorkload is one thing that owns pods, and what its reservations cost.
type costedWorkload struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`

	// Pods is how many of this workload's pods are currently holding capacity.
	// It is a live count rather than the workload's declared replica count,
	// because the declared count is not what is on a node right now.
	Pods int `json:"pods"`

	CPUMillicores int64 `json:"cpu_millicores"`
	MemoryBytes   int64 `json:"memory_bytes"`

	Monthly moneyDimension `json:"monthly"`

	// Used* and Idle are present only where metrics-server answered. Idle is
	// the reserved-and-unspent portion priced at the same rates: the money
	// a right-sizing pass is looking for, and the only figure here that needs
	// live usage to mean anything.
	Used    bool           `json:"used"`
	UsedCPU int64          `json:"used_cpu_millicores"`
	UsedMem int64          `json:"used_memory_bytes"`
	Idle    moneyDimension `json:"idle_monthly"`
}

func (w costedWorkload) sortKey() (string, string) { return w.Namespace, w.Name }

// costedNamespace is the rollup a fleet is actually discussed in. A namespace is
// a team far more often than a Deployment is, and it stays readable on a cluster
// where the workload table cannot.
type costedNamespace struct {
	Namespace string         `json:"namespace"`
	Workloads int            `json:"workloads"`
	Pods      int            `json:"pods"`
	Monthly   moneyDimension `json:"monthly"`
	Idle      moneyDimension `json:"idle_monthly"`
}

// costSummary is the fleet-level reading: what the nodes cost, what the
// workloads claimed, and the gap.
type costSummary struct {
	Nodes int `json:"nodes"`

	// Infrastructure is every node's allocatable capacity at the rate card.
	Infrastructure moneyDimension `json:"infrastructure_monthly"`
	// Attributed is every workload's requests at the same rates.
	Attributed moneyDimension `json:"attributed_monthly"`
	// Unallocated is what was bought and not claimed. It is Infrastructure less
	// Attributed, floored at zero: a cluster whose requests exceed its
	// allocatable capacity is oversubscribed, which the capacity report says
	// far better than a negative amount of money would.
	Unallocated moneyDimension `json:"unallocated_monthly"`
	// AttributedPercent is how much of the fleet's cost the workloads account
	// for. It is the one ratio worth leading with: a cluster at 30% has two
	// thirds of its bill sitting in nodes nothing has claimed.
	AttributedPercent float64 `json:"attributed_percent"`

	// Idle is the reserved-and-unspent total, where usage is known.
	Idle moneyDimension `json:"idle_monthly"`
}

/* -------------------------------------------------------------- the read -- */

// clusterCost prices a cluster's capacity and its workloads' reservations.
func (s *server) clusterCost(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	if !s.requireClusterScope(c, grant, "cluster costs") {
		return
	}

	card, err := s.store.RateCardFor(c.Request.Context(), cluster.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not read the rate card"})
		return
	}
	// An unpriced fleet is told it is unpriced. Reporting zeroes would be a
	// report; reporting a made-up rate would be a lie; this is neither, and it
	// costs no cluster reads to answer.
	if card == nil || !card.Priced() {
		c.JSON(http.StatusOK, gin.H{
			"priced": false,
			"reason": costUnpricedReason,
		})
		return
	}

	nodes, ok := s.fetchNodes(c, user, cluster, grant)
	if !ok {
		return
	}
	pods, ok := s.fetchSchedulablePods(c, user, cluster, grant)
	if !ok {
		return
	}
	owners, ok := s.fetchReplicaSetOwners(c, user, cluster, grant)
	if !ok {
		return
	}
	usage, usageAvailable, ok := s.fetchPodUsage(c, user, cluster, grant)
	if !ok {
		return
	}

	report := buildCost(*card, nodes, pods, owners, usage)

	payload := gin.H{
		"priced":          true,
		"currency":        card.Currency,
		"rate_card":       viewOfRateCard(card, cluster.ID),
		"summary":         report.summary,
		"workloads":       report.workloads,
		"workloads_total": report.workloadCount,
		"namespaces":      report.namespaces,
		"usage_available": usageAvailable,
	}
	if !usageAvailable {
		payload["usage_reason"] = costUsageUnavailableReason
	}
	c.JSON(http.StatusOK, payload)
}

// costUnpricedReason is what an operator sees before any rates exist. It says
// what to do rather than that something failed, because nothing has.
const costUnpricedReason = "No rates are configured for this cluster, so there is nothing to " +
	"cost it against. KubeMG calls no billing API and holds no cloud credential — the rates are " +
	"entered once, in Settings, and a cluster on different hardware can override them."

// costUsageUnavailableReason explains the column that needs metrics-server.
const costUsageUnavailableReason = "This cluster does not serve the Kubernetes Metrics API, so " +
	"reserved-and-unspent cannot be measured. The costs below are reservations at your rates and " +
	"are unaffected."

/* ------------------------------------------------------- owner resolution -- */

// replicaSetOwners maps a ReplicaSet to the Deployment that controls it.
//
// Resolving it costs one list, and skipping it would cost the report its
// vocabulary: a pod's controller is a ReplicaSet named `api-7d4b9c8f5`, and a
// cost table listing thirty of those is a table about nothing an operator
// deploys. Stripping the hash off the name is the obvious shortcut and it is
// wrong on any ReplicaSet somebody created directly.
type replicaSetOwners map[string]ownerRef

// replicaSetList is the ReplicaSet list reduced to the ownership chain.
type replicaSetList struct {
	Items []struct {
		Metadata struct {
			Name            string     `json:"name"`
			Namespace       string     `json:"namespace"`
			OwnerReferences []ownerRef `json:"ownerReferences"`
		} `json:"metadata"`
	} `json:"items"`
}

func (s *server) fetchReplicaSetOwners(c *gin.Context, user *db.User, cluster *db.Cluster,
	grant db.UserClusterAccess,
) (replicaSetOwners, bool) {
	var list replicaSetList
	if !s.fetch(c, user, cluster, grant, "/apis/apps/v1/replicasets", &list) {
		return nil, false
	}

	out := make(replicaSetOwners, len(list.Items))
	for _, item := range list.Items {
		if owner, found := controllerOf(item.Metadata.OwnerReferences); found {
			out[item.Metadata.Namespace+"/"+item.Metadata.Name] = owner
		}
	}
	return out, true
}

// controllerOf picks the owner that actually manages an object. An object may
// carry several ownerReferences and at most one of them is the controller;
// taking the first would attribute a pod to whatever happened to be listed
// first.
func controllerOf(refs []ownerRef) (ownerRef, bool) {
	for _, ref := range refs {
		if ref.Controller != nil && *ref.Controller {
			return ref, true
		}
	}
	return ownerRef{}, false
}

// workloadOf names the thing a pod belongs to, walking one hop through a
// ReplicaSet where there is one.
//
// A Job owned by a CronJob is deliberately left as the Job. It could be walked
// one further hop, at the price of another cluster-wide list — but this report
// only ever sees pods that are still holding capacity, and a CronJob's finished
// runs released theirs. What is listed is what is running now, which for a
// CronJob is the run that is running now, and that is the honest row.
func workloadOf(pod capacityPod, owners replicaSetOwners) (kind, name string) {
	owner, found := controllerOf(pod.Metadata.OwnerReferences)
	if !found {
		// A pod nobody controls is a workload in its own right — that is what a
		// bare pod is — and it is named as one rather than dropped, because an
		// unmanaged pod holding a node is exactly the thing a cost report should
		// surface.
		return "Pod", pod.Metadata.Name
	}
	if owner.Kind == "ReplicaSet" {
		if deployment, known := owners[pod.Metadata.Namespace+"/"+owner.Name]; known {
			return deployment.Kind, deployment.Name
		}
	}
	return owner.Kind, owner.Name
}

/* ------------------------------------------------------------- pod usage -- */

// podUsageKey identifies a pod across namespaces. Pod names are unique only
// within a namespace, and a map keyed on the name alone silently merges the
// `api` in two of them.
type podUsageKey struct{ namespace, name string }

// fetchPodUsage reads what every pod is using, cluster-wide, treating an absent
// Metrics API as an absent column rather than a failure — the same contract the
// capacity report keeps.
func (s *server) fetchPodUsage(c *gin.Context, user *db.User, cluster *db.Cluster,
	grant db.UserClusterAccess,
) (map[podUsageKey]nodeSize, bool, bool) {
	var usage metricsList
	found, ok := s.fetchMetrics(c, user, cluster, grant, metricsAPIGroup+"/pods", &usage)
	if !ok {
		return nil, false, false
	}
	if !found {
		return map[podUsageKey]nodeSize{}, false, true
	}

	out := make(map[podUsageKey]nodeSize, len(usage.Items))
	for _, item := range usage.Items {
		var total nodeSize
		for _, container := range item.Containers {
			total.cpu += parseCPUMillicores(container.Usage["cpu"])
			total.memory += parseMemoryBytes(container.Usage["memory"])
		}
		out[podUsageKey{item.Metadata.Namespace, item.Metadata.Name}] = total
	}
	return out, true, true
}

/* ----------------------------------------------------------- the costing -- */

// costReport is what buildCost produces.
type costReport struct {
	summary    costSummary
	workloads  []costedWorkload
	namespaces []costedNamespace
	// workloadCount is how many workloads were costed in total, which is not
	// how many are in the table. See topCostedWorkloads.
	workloadCount int
}

// buildCost is the whole feature, as a pure function of four reads. Keeping it
// pure is what makes the arithmetic — which is the part that can be wrong in a
// way nobody notices — testable without a cluster.
func buildCost(card db.RateCard, nodes []nodeRecord, pods []capacityPod,
	owners replicaSetOwners, usage map[podUsageKey]nodeSize,
) costReport {
	summary := costSummary{Nodes: len(nodes)}
	for _, node := range nodes {
		summary.Infrastructure.add(priceOf(card, node.Allocatable.cpu, node.Allocatable.memory))
	}

	type key struct{ kind, namespace, name string }
	index := map[key]*costedWorkload{}
	order := []key{}

	usageKnown := len(usage) > 0
	for _, pod := range pods {
		// A pod the scheduler has not placed reserves nothing on any node and
		// costs nothing yet. It is the capacity report's finding, not this
		// one's, and counting it here would price capacity that was never
		// bought.
		if pod.Spec.NodeName == "" {
			continue
		}

		kind, name := workloadOf(pod, owners)
		id := key{kind, pod.Metadata.Namespace, name}
		entry, seen := index[id]
		if !seen {
			entry = &costedWorkload{Kind: kind, Name: name, Namespace: pod.Metadata.Namespace}
			index[id] = entry
			order = append(order, id)
		}

		demand := demandOf(pod)
		entry.Pods++
		entry.CPUMillicores += demand.cpuRequest
		entry.MemoryBytes += demand.memoryRequest

		if spent, known := usage[podUsageKey{pod.Metadata.Namespace, pod.Metadata.Name}]; known {
			entry.Used = true
			entry.UsedCPU += spent.cpu
			entry.UsedMem += spent.memory
		}
	}

	workloads := make([]costedWorkload, 0, len(order))
	namespaces := map[string]*costedNamespace{}
	namespaceOrder := []string{}

	for _, id := range order {
		entry := index[id]
		entry.Monthly = priceOf(card, entry.CPUMillicores, entry.MemoryBytes)

		// Idle is priced per resource and floored per resource. A workload
		// overspending its CPU reservation while underspending its memory one
		// has idle memory and no idle CPU, and netting the two off would report
		// a workload as efficient because it is wrong in both directions.
		if entry.Used {
			entry.Idle = priceOf(card,
				max(entry.CPUMillicores-entry.UsedCPU, 0),
				max(entry.MemoryBytes-entry.UsedMem, 0))
		}

		summary.Attributed.add(entry.Monthly)
		summary.Idle.add(entry.Idle)

		bucket, seen := namespaces[entry.Namespace]
		if !seen {
			bucket = &costedNamespace{Namespace: entry.Namespace}
			namespaces[entry.Namespace] = bucket
			namespaceOrder = append(namespaceOrder, entry.Namespace)
		}
		bucket.Workloads++
		bucket.Pods += entry.Pods
		bucket.Monthly.add(entry.Monthly)
		bucket.Idle.add(entry.Idle)

		workloads = append(workloads, *entry)
	}

	report := costReport{workloadCount: len(workloads)}

	summary.Unallocated = moneyDimension{
		CPU:    math.Max(summary.Infrastructure.CPU-summary.Attributed.CPU, 0),
		Memory: math.Max(summary.Infrastructure.Memory-summary.Attributed.Memory, 0),
	}
	summary.Unallocated.Total = summary.Unallocated.CPU + summary.Unallocated.Memory
	summary.AttributedPercent = ratio(summary.Attributed.Total, summary.Infrastructure.Total)

	summary.Infrastructure = summary.Infrastructure.rounded()
	summary.Attributed = summary.Attributed.rounded()
	summary.Unallocated = summary.Unallocated.rounded()
	summary.Idle = summary.Idle.rounded()
	report.summary = summary

	report.namespaces = finishNamespaces(namespaceOrder, namespaces)
	report.workloads = finishWorkloads(workloads, usageKnown)
	return report
}

// finishWorkloads ranks the table by what it is a table of — money — and cuts
// it where it stops being readable.
func finishWorkloads(all []costedWorkload, usageKnown bool) []costedWorkload {
	all = slices.DeleteFunc(all, func(entry costedWorkload) bool {
		return entry.Monthly.Total < minAttributableMonthly
	})
	slices.SortFunc(all, func(a, b costedWorkload) int {
		if a.Monthly.Total != b.Monthly.Total {
			if a.Monthly.Total > b.Monthly.Total {
				return -1
			}
			return 1
		}
		if order := strings.Compare(a.Namespace, b.Namespace); order != 0 {
			return order
		}
		return strings.Compare(a.Name, b.Name)
	})
	if len(all) > topCostedWorkloads {
		all = all[:topCostedWorkloads]
	}

	out := make([]costedWorkload, 0, len(all))
	for _, entry := range all {
		entry.Monthly = entry.Monthly.rounded()
		entry.Idle = entry.Idle.rounded()
		// Without metrics-server nobody's usage is known, and a `used: false`
		// row beside a `used: true` one would read as "this workload is idle"
		// rather than "this workload was not measured".
		if !usageKnown {
			entry.Used = false
		}
		out = append(out, entry)
	}
	return out
}

// finishNamespaces rounds and ranks the rollup. It is not truncated: a cluster
// with more namespaces than a chart can draw is a chart's problem to solve, and
// the browser can group a tail it can see far better than the server can guess
// at one it cannot.
func finishNamespaces(order []string, index map[string]*costedNamespace) []costedNamespace {
	out := make([]costedNamespace, 0, len(order))
	for _, name := range order {
		entry := *index[name]
		entry.Monthly = entry.Monthly.rounded()
		entry.Idle = entry.Idle.rounded()
		out = append(out, entry)
	}
	slices.SortFunc(out, func(a, b costedNamespace) int {
		if a.Monthly.Total != b.Monthly.Total {
			if a.Monthly.Total > b.Monthly.Total {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Namespace, b.Namespace)
	})
	return out
}

/* ------------------------------------------------------------ arithmetic -- */

// priceOf converts a quantity of capacity into money per month, in the units
// the rest of KubeMG normalises to: millicores and bytes.
func priceOf(card db.RateCard, millicores, bytes int64) moneyDimension {
	out := moneyDimension{
		CPU:    float64(millicores) / millicoresPerCore * card.CPUCoreHour * hoursPerMonth,
		Memory: float64(bytes) / bytesPerGiB * card.MemoryGiBHour * hoursPerMonth,
	}
	out.Total = out.CPU + out.Memory
	return out
}

// money rounds an amount to the currency's smallest ordinary unit. Every total
// is summed before it is rounded, never after: rounding four hundred workloads
// to the cent and then adding them up is off by whatever the rounding was, and
// it is off in a direction that depends on how many rows there were.
func money(amount float64) float64 {
	if math.IsNaN(amount) || math.IsInf(amount, 0) {
		return 0
	}
	return math.Round(amount*100) / 100
}

// ratio renders one amount against another as a percentage to one decimal, with
// an unknown denominator reading as zero rather than as a division.
func ratio(part, whole float64) float64 {
	if whole <= 0 {
		return 0
	}
	return math.Round(part/whole*1000) / 10
}
