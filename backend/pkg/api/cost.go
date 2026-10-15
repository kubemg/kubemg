package api

import (
	"cmp"
	"math"
	"slices"
	"strings"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * What a cluster reserves, and what that costs at its operator's rates.
 *
 * **Cost is requests times a rate.** A node-billed cluster pays for nodes, and
 * what fills a node is what the scheduler reserved on it — so a workload's
 * share of the bill is its requests, not its usage. A workload using more than
 * it reserved is borrowing capacity somebody else paid for, and charging it
 * for that would make the figure depend on a datasource and change every time
 * the page opened. The reservation is exact; the rate is the operator's.
 *
 * The request is the scheduler's own arithmetic (`demandOf`, the capacity
 * page's), sidecars, init peaks and pod overhead included, so the two pages
 * agree about what a pod takes. **A pod not yet placed reserves nothing**: it
 * is counted, and left out of the figure.
 *
 * Every figure is rounded to cents only on the way out. A category with no
 * rate has no figure — absent, never zero — and the summary names it, so a
 * total that leaves storage out says that it does.
 */

// hoursPerMonth is the average month: 365 days × 24 hours ÷ 12. Cloud bills
// use the same figure.
const hoursPerMonth = 730

const (
	// maxReservedNamespaces and maxReservedWorkloads cap the rows carried back;
	// the totals and counts are exact.
	maxReservedNamespaces = 100
	maxReservedWorkloads  = 25
)

type reservedWorkload struct {
	Kind    string   `json:"kind"`
	Name    string   `json:"name"`
	Pods    int      `json:"pods"`
	CPU     int64    `json:"cpu"`
	Memory  int64    `json:"memory"`
	Monthly *float64 `json:"monthly,omitempty"`
}

type reservedNamespace struct {
	Namespace      string             `json:"namespace"`
	Pods           int                `json:"pods"`
	CPU            int64              `json:"cpu"`
	Memory         int64              `json:"memory"`
	Monthly        *float64           `json:"monthly,omitempty"`
	Workloads      []reservedWorkload `json:"workloads"`
	WorkloadsTotal int                `json:"workloads_total"`
}

type reservedReport struct {
	Pods            int                 `json:"pods"`
	CPU             int64               `json:"cpu"`
	Memory          int64               `json:"memory"`
	Monthly         *float64            `json:"monthly,omitempty"`
	Namespaces      []reservedNamespace `json:"namespaces"`
	NamespacesTotal int                 `json:"namespaces_total"`
	// Unscheduled counts the pods waiting for a node, which reserve nothing yet.
	Unscheduled int `json:"unscheduled"`
}

// wasteMonthly is the summary's money. Each figure is absent where its rate is.
type wasteMonthly struct {
	Reclaimable       float64  `json:"reclaimable"`
	UnusedStorage     *float64 `json:"unused_storage,omitempty"`
	IdleLoadBalancers *float64 `json:"idle_load_balancers,omitempty"`
	// Total is what every priced category adds up to.
	Total float64 `json:"total"`
	// Unpriced names the categories with something in them and no rate, which
	// the total therefore leaves out.
	Unpriced []string `json:"unpriced"`
}

/* ---------------------------------------------------------- the arithmetic -- */

const gib = float64(1 << 30)

func cents(value float64) float64 { return math.Round(value*100) / 100 }

// computeMonthly prices a reservation of millicores and bytes for a month.
func computeMonthly(cpu, memory int64, rates db.ClusterCostRates) float64 {
	return (float64(cpu)/1000*rates.CPUCoreHour + float64(memory)/gib*rates.MemoryGiBHour) *
		hoursPerMonth
}

// pricedCompute is computeMonthly rounded, or nil without rates.
func pricedCompute(cpu, memory int64, rates *db.ClusterCostRates) *float64 {
	if rates == nil {
		return nil
	}
	value := cents(computeMonthly(cpu, memory, *rates))
	return &value
}

// storageMonthly prices provisioned bytes for a month, nil without a rate.
func storageMonthly(bytes int64, rates *db.ClusterCostRates) *float64 {
	if rates == nil || rates.StorageGiBMonth == nil || bytes <= 0 {
		return nil
	}
	value := float64(bytes) / gib * *rates.StorageGiBMonth
	return &value
}

// balancerMonthly prices one load balancer for a month, nil without a rate.
func balancerMonthly(rates *db.ClusterCostRates) *float64 {
	if rates == nil || rates.LoadBalancerHour == nil {
		return nil
	}
	value := *rates.LoadBalancerHour * hoursPerMonth
	return &value
}

/* ---------------------------------------------------------- the reservation -- */

// capacityPod reduces a waste read's pod to the capacity page's shape, so the
// request is the scheduler's arithmetic and nobody else's.
func (p wastePod) capacityPod() capacityPod {
	var out capacityPod
	out.Metadata.Name = p.Metadata.Name
	out.Metadata.Namespace = p.Metadata.Namespace
	out.Spec.NodeName = p.Spec.NodeName
	out.Spec.Overhead = p.Spec.Overhead
	out.Spec.Containers = p.Spec.Containers
	out.Spec.InitContainers = p.Spec.InitContainers
	return out
}

// buildReserved totals what the running pods reserve, per namespace and per
// workload, priced when there are rates.
func buildReserved(pods []wastePod, rates *db.ClusterCostRates) reservedReport {
	type workloadTotal struct {
		reservedWorkload
		namespace string
	}
	namespaces := map[string]*reservedNamespace{}
	workloads := map[workloadKey]*workloadTotal{}

	report := reservedReport{Namespaces: []reservedNamespace{}}
	for _, pod := range pods {
		if pod.Spec.NodeName == "" {
			report.Unscheduled++
			continue
		}
		demand := demandOf(pod.capacityPod())

		kind, name := "Pod", pod.Metadata.Name
		if owner := podOwner(pod.Metadata.OwnerReferences, pod.Metadata.Labels); owner != nil {
			kind, name = owner.Kind, owner.Name
		}

		namespace := namespaces[pod.Metadata.Namespace]
		if namespace == nil {
			namespace = &reservedNamespace{Namespace: pod.Metadata.Namespace}
			namespaces[pod.Metadata.Namespace] = namespace
		}
		key := workloadKey{pod.Metadata.Namespace, kind, name}
		workload := workloads[key]
		if workload == nil {
			workload = &workloadTotal{
				reservedWorkload: reservedWorkload{Kind: kind, Name: name},
				namespace:        pod.Metadata.Namespace,
			}
			workloads[key] = workload
		}

		for _, total := range []struct {
			pods        *int
			cpu, memory *int64
		}{
			{&report.Pods, &report.CPU, &report.Memory},
			{&namespace.Pods, &namespace.CPU, &namespace.Memory},
			{&workload.Pods, &workload.CPU, &workload.Memory},
		} {
			*total.pods++
			*total.cpu += demand.cpuRequest
			*total.memory += demand.memoryRequest
		}
	}

	report.Monthly = pricedCompute(report.CPU, report.Memory, rates)
	weight := reservationWeight(report.CPU, report.Memory)

	byNamespace := map[string][]reservedWorkload{}
	for _, workload := range workloads {
		row := workload.reservedWorkload
		row.Monthly = pricedCompute(row.CPU, row.Memory, rates)
		byNamespace[workload.namespace] = append(byNamespace[workload.namespace], row)
	}

	for name, namespace := range namespaces {
		rows := byNamespace[name]
		slices.SortStableFunc(rows, func(a, b reservedWorkload) int {
			if order := cmp.Compare(weight(b.CPU, b.Memory), weight(a.CPU, a.Memory)); order != 0 {
				return order
			}
			if order := strings.Compare(a.Kind, b.Kind); order != 0 {
				return order
			}
			return strings.Compare(a.Name, b.Name)
		})
		namespace.WorkloadsTotal = len(rows)
		if len(rows) > maxReservedWorkloads {
			rows = rows[:maxReservedWorkloads]
		}
		namespace.Workloads = rows
		namespace.Monthly = pricedCompute(namespace.CPU, namespace.Memory, rates)
		report.Namespaces = append(report.Namespaces, *namespace)
	}
	slices.SortStableFunc(report.Namespaces, func(a, b reservedNamespace) int {
		if order := cmp.Compare(weight(b.CPU, b.Memory), weight(a.CPU, a.Memory)); order != 0 {
			return order
		}
		return strings.Compare(a.Namespace, b.Namespace)
	})
	report.NamespacesTotal = len(report.Namespaces)
	if len(report.Namespaces) > maxReservedNamespaces {
		report.Namespaces = report.Namespaces[:maxReservedNamespaces]
	}
	return report
}

// reservationWeight orders reservations by the larger of their two shares of
// the cluster's total, so neither unit outranks the other by being counted in
// smaller pieces — the waste list's rule. With rates the order is the same
// question priced, but a share does not need a price to be asked.
func reservationWeight(totalCPU, totalMemory int64) func(cpu, memory int64) float64 {
	return func(cpu, memory int64) float64 {
		var cpuShare, memoryShare float64
		if totalCPU > 0 {
			cpuShare = float64(cpu) / float64(totalCPU)
		}
		if totalMemory > 0 {
			memoryShare = float64(memory) / float64(totalMemory)
		}
		return max(cpuShare, memoryShare)
	}
}

/* --------------------------------------------------------- pricing the waste -- */

// priceWaste puts money on the waste report's rows and summary. Without rates
// it changes nothing.
func priceWaste(report *wasteReport, rates *db.ClusterCostRates) {
	if rates == nil {
		return
	}
	monthly := wasteMonthly{Unpriced: []string{}}

	for i := range report.Workloads {
		workload := &report.Workloads[i]
		workload.Monthly = pricedCompute(workload.Reclaimable.CPU, workload.Reclaimable.Memory, rates)
	}
	// The summary prices the exact total, not the capped rows.
	monthly.Reclaimable = computeMonthly(report.Summary.ReclaimableCPU,
		report.Summary.ReclaimableMemory, *rates)
	monthly.Total += monthly.Reclaimable

	if rates.StorageGiBMonth != nil {
		var storage float64
		for i := range report.Volumes {
			volume := &report.Volumes[i]
			if value := storageMonthly(volume.Capacity, rates); value != nil {
				storage += *value
				rounded := cents(*value)
				volume.Monthly = &rounded
			}
		}
		monthly.UnusedStorage = &storage
		monthly.Total += storage
	} else if report.Summary.UnusedStorage > 0 {
		monthly.Unpriced = append(monthly.Unpriced, "storage")
	}

	if each := balancerMonthly(rates); each != nil {
		rounded := cents(*each)
		for i := range report.LoadBalancers {
			report.LoadBalancers[i].Monthly = &rounded
		}
		balancers := *each * float64(len(report.LoadBalancers))
		monthly.IdleLoadBalancers = &balancers
		monthly.Total += balancers
	} else if len(report.LoadBalancers) > 0 {
		monthly.Unpriced = append(monthly.Unpriced, "load_balancers")
	}

	monthly.Reclaimable = cents(monthly.Reclaimable)
	monthly.Total = cents(monthly.Total)
	for _, figure := range []*float64{monthly.UnusedStorage, monthly.IdleLoadBalancers} {
		if figure != nil {
			*figure = cents(*figure)
		}
	}
	report.Summary.Monthly = &monthly
}
