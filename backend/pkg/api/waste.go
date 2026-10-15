package api

import (
	"cmp"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/db"
	"github.com/kubemg/kubemg/backend/pkg/observability"
)

/*
 * Waste: what a cluster reserves and does not use.
 *
 * The capacity page answers "why will nothing schedule" from requests, and its
 * one FinOps note — a node mostly reserved and half unspent — says *that* money
 * is idle without saying *whose*. This answers whose, three ways:
 *
 *   workloads       containers whose request is well past what they used over
 *                   a window, with a request sized from that usage
 *   volumes         claims no pod mounts, and volumes left Released or never
 *                   bound — storage that is paid for and read by nothing
 *   load balancers  `type: LoadBalancer` Services with no ready endpoint — a
 *                   cloud load balancer in front of nothing
 *
 * **It recommends; it never writes.** The recommendation is YAML an operator
 * applies where the manifest is kept — usually Git, which is exactly why a
 * button here would be wrong: the next sync would put the old request back.
 *
 * **Sizing reads history, not the Metrics API.** metrics-server keeps two
 * minutes, and a request sized from two minutes of a workload is sized from
 * whichever two minutes the page happened to open in. So the workload reading
 * needs the cluster's metrics datasource, and without one it says so — volumes
 * and load balancers are read from the cluster alone and are answered anyway.
 *
 * **Money only at the operator's rates.** Reclaimable capacity is cores and
 * bytes; a figure in a currency appears only where an admin has set the
 * cluster's rates (cost_rates.go), because a price is a property of a contract,
 * not of the cluster, and a vendor's list price would be wrong for nearly
 * everyone who read it. The same pods also answer what the cluster reserves,
 * per namespace and workload (cost.go) — with or without rates or history.
 *
 * Every cluster read rides the same impersonated, audited tunnel as the lists;
 * the history read is the observability path's, scoped by the same grant. A
 * namespace-scoped grant is answered over its own namespaces, and is not shown
 * PersistentVolumes, which are cluster-scoped.
 */

/*
 * Thresholds. Constants rather than settings for the reason capacity's are: a
 * number an operator can raise until the list empties stops meaning anything.
 */
const (
	// wasteOverRatio is "requested resources exceed actual usage by more than
	// 50%": a dimension is over-requested when its request is more than this
	// multiple of what it used.
	wasteOverRatio = 1.5
	// wasteHeadroom is what a recommendation adds on top of the reading — room
	// for the week that was busier than this one.
	wasteHeadroom = 1.2

	// A workload is listed only when what it would give back is worth a change:
	// a hundred millicores or 128 MiB across its replicas. Below that, every
	// small Deployment in the cluster qualifies and the list is noise.
	wasteMinCPU    = 100
	wasteMinMemory = 128 << 20

	// Floors: a recommendation is never smaller than this, however idle the
	// container was. A request of 1m is a request the scheduler places anywhere
	// and the kernel starves first.
	wasteFloorCPU    = 10
	wasteFloorMemory = 32 << 20

	// wasteRoundCPU and wasteRoundMemory are what a recommendation rounds up to,
	// so the YAML reads like something a person wrote.
	wasteRoundCPU    = 5
	wasteRoundMemory = 1 << 20

	// wasteMinHistory is how much history a workload needs before it is sized —
	// counted from the later of its oldest pod's start and the first sample the
	// datasource holds. A day covers one daily cycle; less than that is a guess.
	wasteMinHistory = 24 * time.Hour

	// maxWasteWorkloads caps the rows carried back. The count is exact.
	maxWasteWorkloads = 200
)

// wasteSpans are the windows the page offers. One hour of usage is not a basis
// for a request, so the console's global time range is deliberately not used.
var wasteSpans = map[string]time.Duration{
	"1d":  24 * time.Hour,
	"7d":  7 * 24 * time.Hour,
	"14d": 14 * 24 * time.Hour,
	"30d": 30 * 24 * time.Hour,
}

const defaultWasteSpan = "7d"

/* ------------------------------------------------------------ the payload -- */

// wasteDimension is one resource of one container. Every field is absent where
// it was not declared or not measured, which the console draws as a dash with a
// reason — never as zero.
type wasteDimension struct {
	Request     *int64 `json:"request,omitempty"`
	Observed    *int64 `json:"observed,omitempty"`
	Recommended *int64 `json:"recommended,omitempty"`
	// Over marks the dimension the recommendation changes.
	Over bool `json:"over"`
}

type wasteContainer struct {
	Name string `json:"name"`
	// Sidecar marks a native sidecar: an init container that keeps running.
	Sidecar bool           `json:"sidecar,omitempty"`
	CPU     wasteDimension `json:"cpu"`
	Memory  wasteDimension `json:"memory"`
	// Note says why a reading is missing, in the console's words.
	Note string `json:"note,omitempty"`
}

type wasteAmount struct {
	CPU    int64 `json:"cpu"`
	Memory int64 `json:"memory"`
}

type wasteWorkload struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	// Replicas is how many pods are running now — what the reclaimable figure
	// is multiplied by.
	Replicas int `json:"replicas"`
	// Since is where the reading starts: the later of the oldest running pod's
	// start and the first sample the datasource holds for it.
	Since time.Time `json:"since"`
	// Rollout marks pods that disagree about a request — a rollout in progress.
	// The newest pod's request is the one shown.
	Rollout     bool             `json:"rollout,omitempty"`
	Containers  []wasteContainer `json:"containers"`
	Reclaimable wasteAmount      `json:"reclaimable"`
	// Monthly is what the reclaimable capacity costs at the cluster's rates.
	Monthly *float64 `json:"monthly,omitempty"`
}

type wasteVolume struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	// Capacity in bytes, as the cluster reported it; zero when it did not.
	Capacity     int64  `json:"capacity"`
	StorageClass string `json:"storage_class,omitempty"`
	// State is unmounted (a bound claim no pod names), released (its claim was
	// deleted and the data kept) or available (never bound, or freed).
	State string `json:"state"`
	// Volume is the PersistentVolume behind a claim; Claim the claim a
	// released volume belonged to.
	Volume        string `json:"volume,omitempty"`
	Claim         string `json:"claim,omitempty"`
	ReclaimPolicy string `json:"reclaim_policy,omitempty"`
	// StatefulSet names the StatefulSet whose template the claim's name
	// follows, and ScaledDown marks an ordinal past its replicas: a StatefulSet
	// keeps those claims on purpose when it scales down.
	StatefulSet string    `json:"statefulset,omitempty"`
	ScaledDown  bool      `json:"scaled_down,omitempty"`
	Created     time.Time `json:"created_at"`
	Monthly     *float64  `json:"monthly,omitempty"`
}

type wasteLoadBalancer struct {
	Namespace string   `json:"namespace"`
	Name      string   `json:"name"`
	Addresses []string `json:"addresses"`
	// State is no-ready-endpoints or no-selector.
	State   string    `json:"state"`
	Created time.Time `json:"created_at"`
	Monthly *float64  `json:"monthly,omitempty"`
}

type wasteSummary struct {
	ReclaimableCPU    int64 `json:"reclaimable_cpu"`
	ReclaimableMemory int64 `json:"reclaimable_memory"`
	UnusedStorage     int64 `json:"unused_storage"`
	IdleLoadBalancers int   `json:"idle_load_balancers"`
	// The workloads the reading looked at, and the two reasons one was not
	// sized, so an empty list is never mistaken for a clean cluster.
	WorkloadsChecked      int `json:"workloads_checked"`
	WorkloadsShortHistory int `json:"workloads_short_history"`
	WorkloadsUnmeasured   int `json:"workloads_unmeasured"`
	// Monthly is present only where the cluster has rates.
	Monthly *wasteMonthly `json:"monthly,omitempty"`
}

type wasteReport struct {
	Workloads      []wasteWorkload     `json:"workloads"`
	WorkloadsTotal int                 `json:"workloads_total"`
	Volumes        []wasteVolume       `json:"volumes"`
	LoadBalancers  []wasteLoadBalancer `json:"load_balancers"`
	Summary        wasteSummary        `json:"summary"`
	Reserved       reservedReport      `json:"reserved"`
}

/* --------------------------------------------------------------- the reads -- */

type wastePod struct {
	Metadata struct {
		Name              string            `json:"name"`
		Namespace         string            `json:"namespace"`
		CreationTimestamp time.Time         `json:"creationTimestamp"`
		Labels            map[string]string `json:"labels"`
		OwnerReferences   []ownerRef        `json:"ownerReferences"`
	} `json:"metadata"`
	Spec struct {
		NodeName       string                  `json:"nodeName"`
		Overhead       map[string]string       `json:"overhead"`
		Containers     []capacityContainer     `json:"containers"`
		InitContainers []capacityInitContainer `json:"initContainers"`
		Volumes        []struct {
			PersistentVolumeClaim *struct {
				ClaimName string `json:"claimName"`
			} `json:"persistentVolumeClaim"`
		} `json:"volumes"`
	} `json:"spec"`
}

type wasteStatefulSet struct {
	Metadata struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	Spec struct {
		Replicas             *int32 `json:"replicas"`
		VolumeClaimTemplates []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		} `json:"volumeClaimTemplates"`
	} `json:"spec"`
}

type wasteClaim struct {
	Metadata struct {
		Name              string    `json:"name"`
		Namespace         string    `json:"namespace"`
		CreationTimestamp time.Time `json:"creationTimestamp"`
	} `json:"metadata"`
	Spec struct {
		StorageClassName *string `json:"storageClassName"`
		VolumeName       string  `json:"volumeName"`
	} `json:"spec"`
	Status struct {
		Phase    string            `json:"phase"`
		Capacity map[string]string `json:"capacity"`
	} `json:"status"`
}

type wastePersistentVolume struct {
	Metadata struct {
		Name              string    `json:"name"`
		CreationTimestamp time.Time `json:"creationTimestamp"`
	} `json:"metadata"`
	Spec struct {
		Capacity         map[string]string `json:"capacity"`
		StorageClassName string            `json:"storageClassName"`
		ReclaimPolicy    string            `json:"persistentVolumeReclaimPolicy"`
		ClaimRef         *struct {
			Namespace string `json:"namespace"`
			Name      string `json:"name"`
		} `json:"claimRef"`
	} `json:"spec"`
	Status struct {
		Phase string `json:"phase"`
	} `json:"status"`
}

type wasteService struct {
	Metadata struct {
		Name              string    `json:"name"`
		Namespace         string    `json:"namespace"`
		CreationTimestamp time.Time `json:"creationTimestamp"`
	} `json:"metadata"`
	Spec struct {
		Type     string            `json:"type"`
		Selector map[string]string `json:"selector"`
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

type wasteEndpointSlice struct {
	Metadata struct {
		Namespace string            `json:"namespace"`
		Labels    map[string]string `json:"labels"`
	} `json:"metadata"`
	Endpoints []struct {
		Conditions struct {
			// Ready is nil when the controller did not say, which the API
			// documents as ready.
			Ready *bool `json:"ready"`
		} `json:"conditions"`
	} `json:"endpoints"`
}

// wasteInputs is everything the report is built from. It is a plain value so
// the reading — which is the whole feature — is testable without a cluster.
type wasteInputs struct {
	now          time.Time
	pods         []wastePod
	statefulSets []wasteStatefulSet
	claims       []wasteClaim
	volumes      []wastePersistentVolume
	services     []wasteService
	slices       []wasteEndpointSlice
	// slicesRead is false when the endpoint slices could not be read, which
	// leaves every load balancer unjudged rather than idle.
	slicesRead bool
	// usage is nil when there is no history to size from.
	usage map[observability.UsageKey]observability.UsageProfile
	// rates is nil when the cluster has none set: nothing is priced.
	rates *db.ClusterCostRates
}

/* ------------------------------------------------------------- the handler -- */

// clusterWaste reports what the cluster reserves and does not use.
func (s *server) clusterWaste(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}

	spanName := strings.TrimSpace(c.DefaultQuery("window", defaultWasteSpan))
	span, known := wasteSpans[spanName]
	if !known {
		c.JSON(http.StatusBadRequest, gin.H{"error": "window has to be one of 1d, 7d, 14d or 30d"})
		return
	}

	// A scoped grant reads its own namespaces one at a time; an unscoped one
	// reads cluster-wide. Never a cluster-wide path for a scoped grant.
	allowed := grant.NamespaceList()
	if len(allowed) > maxFanOut {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf(
				"your grant covers %d namespaces, which is more than this view reads at once",
				len(allowed)),
		})
		return
	}
	scope := readScope{Namespaces: allowed, All: true}

	in := wasteInputs{now: time.Now().UTC()}

	podFilter := url.Values{}
	podFilter.Set("fieldSelector", "status.phase!=Succeeded,status.phase!=Failed")
	for _, path := range scope.paths(resourceListPath{"/api/v1", "pods"}) {
		if !fetchList(s, c, user, cluster, grant, path+"?"+podFilter.Encode(), &in.pods) {
			return
		}
	}
	for _, path := range scope.paths(resourceListPath{"/apis/apps/v1", "statefulsets"}) {
		if !fetchList(s, c, user, cluster, grant, path, &in.statefulSets) {
			return
		}
	}
	for _, path := range scope.paths(resourceListPath{"/api/v1", "persistentvolumeclaims"}) {
		if !fetchList(s, c, user, cluster, grant, path, &in.claims) {
			return
		}
	}
	for _, path := range scope.paths(resourceListPath{"/api/v1", "services"}) {
		if !fetchList(s, c, user, cluster, grant, path, &in.services) {
			return
		}
	}

	// PersistentVolumes are cluster-scoped. A scoped grant is not shown them,
	// and a view grant the cluster refuses them to is told so in its words.
	volumesAvailable, volumesReason := false, wasteScopedVolumesReason
	if len(allowed) == 0 {
		volumesAvailable, volumesReason, ok = fetchDegradingList(s, c, user, cluster, grant,
			"/api/v1/persistentvolumes", &in.volumes)
		if !ok {
			return
		}
		if !volumesAvailable {
			volumesReason = "The cluster refused the PersistentVolume list: " + volumesReason
		}
	}

	in.slicesRead = true
	balancersReason := ""
	for _, path := range scope.paths(resourceListPath{"/apis/discovery.k8s.io/v1", "endpointslices"}) {
		read, reason, ok := fetchDegradingList(s, c, user, cluster, grant, path, &in.slices)
		if !ok {
			return
		}
		if !read {
			in.slicesRead = false
			balancersReason = "The cluster refused the EndpointSlice list, so whether a load " +
				"balancer has anything behind it cannot be told: " + reason
			break
		}
	}

	end := in.now
	window := observability.Window{Start: end.Add(-span), End: end}
	usage, usageReason := s.wasteUsage(c, user, cluster, grant, window)
	if usage != nil {
		in.usage = usage.Profiles
	}
	rates, ratesReason := s.wasteRates(c, cluster.ID)
	in.rates = rates

	report := buildWaste(in)

	payload := gin.H{
		"window":                   gin.H{"span": spanName, "start": window.Start, "end": window.End},
		"workloads_available":      usage != nil,
		"workloads":                report.Workloads,
		"workloads_total":          report.WorkloadsTotal,
		"volumes":                  report.Volumes,
		"volumes_pv_available":     volumesAvailable,
		"load_balancers":           report.LoadBalancers,
		"load_balancers_available": in.slicesRead,
		"summary":                  report.Summary,
		"reserved":                 report.Reserved,
	}
	if rates != nil {
		payload["cost"] = gin.H{
			"currency": rates.Currency, "rates": rates, "hours_per_month": hoursPerMonth,
		}
	} else if ratesReason != "" {
		payload["cost_reason"] = ratesReason
	}
	if usage == nil {
		payload["workloads_reason"] = usageReason
	} else {
		payload["query"] = gin.H{
			"cpu": usage.CPUQuery, "memory": usage.MemoryQuery, "history": usage.HistoryQuery,
		}
		if usage.Truncated {
			payload["usage_truncated"] = true
		}
	}
	if !volumesAvailable {
		payload["volumes_pv_reason"] = volumesReason
	}
	if !in.slicesRead {
		payload["load_balancers_reason"] = balancersReason
	}
	listResponse(c, payload)
}

const wasteScopedVolumesReason = "PersistentVolumes are cluster-scoped, and your access to this " +
	"cluster is limited to some namespaces, so released and unbound volumes are not shown. " +
	"Unmounted claims in your namespaces are."

// wasteRates loads the cluster's rates. None set is no money and no reason; a
// store that cannot answer is no money with one — never a failed report.
func (s *server) wasteRates(c *gin.Context, clusterID uint) (*db.ClusterCostRates, string) {
	rates, err := s.store.ClusterCostRates(c.Request.Context(), clusterID)
	switch {
	case errors.Is(err, db.ErrNotFound):
		return nil, ""
	case err != nil:
		return nil, "The cluster's rates could not be loaded, so nothing is priced."
	}
	return rates, ""
}

// wasteUsage reads the history the workload reading is sized from. Every way
// it can be missing is an answer with a reason rather than a failed page:
// volumes and load balancers do not need it.
func (s *server) wasteUsage(c *gin.Context, user *db.User, cluster *db.Cluster,
	grant db.UserClusterAccess, window observability.Window,
) (*observability.UsageResult, string) {
	source, err := s.store.ObservabilitySource(c.Request.Context(), cluster.ID, db.SourceMetrics)
	switch {
	case errors.Is(err, db.ErrNotFound):
		return nil, "This cluster has no metrics datasource registered. Sizing a request needs " +
			"usage history, and the Metrics API keeps only the last few minutes."
	case err != nil:
		return nil, "The metrics datasource could not be loaded."
	case !source.Enabled:
		return nil, "This cluster's metrics datasource is turned off."
	}

	result, err := observability.QueryUsageProfile(c.Request.Context(),
		observability.TargetOf(*source), s.tunnelCall(user, cluster),
		queryScope(user, grant), window)
	if err != nil {
		return nil, "The metrics datasource did not answer the usage query: " + err.Error()
	}
	return &result, ""
}

/* ------------------------------------------------------------- the reading -- */

// buildWaste turns the reads into the report.
func buildWaste(in wasteInputs) wasteReport {
	report := wasteReport{
		Workloads:     []wasteWorkload{},
		Volumes:       []wasteVolume{},
		LoadBalancers: []wasteLoadBalancer{},
	}

	if in.usage != nil {
		listed := sizeWorkloads(in, &report.Summary)
		report.WorkloadsTotal = len(listed)
		for _, workload := range listed {
			report.Summary.ReclaimableCPU += workload.Reclaimable.CPU
			report.Summary.ReclaimableMemory += workload.Reclaimable.Memory
		}
		sortByReclaimable(listed, report.Summary)
		if len(listed) > maxWasteWorkloads {
			listed = listed[:maxWasteWorkloads]
		}
		report.Workloads = listed
	}

	report.Volumes = unusedVolumes(in)
	for _, volume := range report.Volumes {
		report.Summary.UnusedStorage += volume.Capacity
	}

	if in.slicesRead {
		report.LoadBalancers = idleLoadBalancers(in)
		report.Summary.IdleLoadBalancers = len(report.LoadBalancers)
	}

	report.Reserved = buildReserved(in.pods, in.rates)
	priceWaste(&report, in.rates)
	return report
}

type workloadKey struct{ namespace, kind, name string }

// sizedKinds are the workloads a request is sized for: the ones that run until
// told otherwise. A Job's request is for one run, and a bare pod has no
// template to change.
var sizedKinds = map[string]bool{"Deployment": true, "StatefulSet": true, "DaemonSet": true}

// sizeWorkloads groups the running pods by workload and returns the ones worth
// listing, counting the rest into the summary.
func sizeWorkloads(in wasteInputs, summary *wasteSummary) []wasteWorkload {
	groups := map[workloadKey][]wastePod{}
	var order []workloadKey
	for _, pod := range in.pods {
		owner := podOwner(pod.Metadata.OwnerReferences, pod.Metadata.Labels)
		if owner == nil || !sizedKinds[owner.Kind] {
			continue
		}
		key := workloadKey{pod.Metadata.Namespace, owner.Kind, owner.Name}
		if _, seen := groups[key]; !seen {
			order = append(order, key)
		}
		groups[key] = append(groups[key], pod)
	}

	listed := []wasteWorkload{}
	for _, key := range order {
		workload, verdict := sizeWorkload(key, groups[key], in.usage, in.now)
		summary.WorkloadsChecked++
		switch verdict {
		case verdictShortHistory:
			summary.WorkloadsShortHistory++
		case verdictUnmeasured:
			summary.WorkloadsUnmeasured++
		case verdictListed:
			listed = append(listed, workload)
		}
	}
	return listed
}

type wasteVerdict int

const (
	verdictFine wasteVerdict = iota
	verdictListed
	verdictShortHistory
	verdictUnmeasured
)

// runningContainer is a container that keeps running for the pod's life: a
// regular one, or a native sidecar. A plain init container is not sized — it
// is a step that finishes, and its usage over a week is mostly absence.
type runningContainer struct {
	capacityContainer
	sidecar bool
}

func runningContainers(pod wastePod) []runningContainer {
	out := make([]runningContainer, 0, len(pod.Spec.Containers))
	for _, container := range pod.Spec.Containers {
		out = append(out, runningContainer{capacityContainer: container})
	}
	for _, init := range pod.Spec.InitContainers {
		if init.RestartPolicy == restartAlways {
			out = append(out, runningContainer{capacityContainer: init.capacityContainer, sidecar: true})
		}
	}
	return out
}

func sizeWorkload(key workloadKey, pods []wastePod,
	usage map[observability.UsageKey]observability.UsageProfile, now time.Time,
) (wasteWorkload, wasteVerdict) {
	// Newest first: during a rollout the newest pod carries the request the
	// template now says, which is the one a recommendation replaces.
	slices.SortFunc(pods, func(a, b wastePod) int {
		return b.Metadata.CreationTimestamp.Compare(a.Metadata.CreationTimestamp)
	})
	newest := pods[0]

	workload := wasteWorkload{
		Kind:      key.kind,
		Namespace: key.namespace,
		Name:      key.name,
		Replicas:  len(pods),
		Since:     pods[len(pods)-1].Metadata.CreationTimestamp,
	}

	// The readings first, then whether there is enough history behind them.
	// The history is the later of the oldest pod's start and the first sample
	// the datasource holds: a pod older than the datasource has only as much
	// history as the datasource kept.
	var firstSample *time.Time
	measured := false
	for _, container := range runningContainers(newest) {
		row := wasteContainer{Name: container.Name, Sidecar: container.sidecar}
		row.CPU.Request = declaredRequest(container.capacityContainer, "cpu")
		row.Memory.Request = declaredRequest(container.capacityContainer, "memory")

		for _, pod := range pods[1:] {
			if requestsDiffer(pod, container) {
				workload.Rollout = true
			}
		}

		reading := observedFor(pods, container.Name, usage)
		row.CPU.Observed, row.Memory.Observed, row.Note = reading.cpu, reading.memory, reading.note
		if reading.cpu != nil || reading.memory != nil {
			measured = true
		}
		if reading.since != nil && (firstSample == nil || reading.since.Before(*firstSample)) {
			firstSample = reading.since
		}
		workload.Containers = append(workload.Containers, row)
	}
	if firstSample != nil && firstSample.After(workload.Since) {
		workload.Since = *firstSample
	}

	if now.Sub(workload.Since) < wasteMinHistory {
		return workload, verdictShortHistory
	}
	if !measured {
		return workload, verdictUnmeasured
	}

	for i := range workload.Containers {
		row := &workload.Containers[i]
		row.CPU.Recommended = recommend(row.CPU.Observed, wasteFloorCPU, wasteRoundCPU)
		row.Memory.Recommended = recommend(row.Memory.Observed, wasteFloorMemory, wasteRoundMemory)
		row.CPU.Over = over(row.CPU)
		row.Memory.Over = over(row.Memory)
		if row.CPU.Over {
			workload.Reclaimable.CPU += (*row.CPU.Request - *row.CPU.Recommended) * int64(len(pods))
		}
		if row.Memory.Over {
			workload.Reclaimable.Memory += (*row.Memory.Request - *row.Memory.Recommended) * int64(len(pods))
		}
	}

	if workload.Reclaimable.CPU >= wasteMinCPU || workload.Reclaimable.Memory >= wasteMinMemory {
		return workload, verdictListed
	}
	return workload, verdictFine
}

// declaredRequest is a container's request for one resource, nil when it
// declares none — a container asking for nothing is not over-asking.
func declaredRequest(container capacityContainer, resource string) *int64 {
	raw, declared := container.Resources.Requests[resource]
	if !declared {
		return nil
	}
	var value int64
	if resource == "cpu" {
		value = parseCPUMillicores(raw)
	} else {
		value = parseMemoryBytes(raw)
	}
	return &value
}

// requestsDiffer reports whether a pod asks a different request for a
// container than the one shown.
func requestsDiffer(pod wastePod, shown runningContainer) bool {
	for _, container := range runningContainers(pod) {
		if container.Name != shown.Name {
			continue
		}
		for _, resource := range []string{"cpu", "memory"} {
			a, b := declaredRequest(container.capacityContainer, resource),
				declaredRequest(shown.capacityContainer, resource)
			if (a == nil) != (b == nil) || (a != nil && *a != *b) {
				return true
			}
		}
	}
	return false
}

// containerReading is one container's readings across a workload's pods.
type containerReading struct {
	cpu, memory *int64
	// since is the earliest first sample among those pods.
	since *time.Time
	note  string
}

// observedFor is a container's reading across a workload's pods: the largest
// of them, because a request has to cover the busiest replica, not the average
// one. A datasource whose series carry no container label answers per pod,
// which sizes a single-container pod and nothing else.
func observedFor(pods []wastePod, container string,
	usage map[observability.UsageKey]observability.UsageProfile,
) containerReading {
	var out containerReading
	unlabelled := false
	for _, pod := range pods {
		profile, found := usage[observability.UsageKey{
			Namespace: pod.Metadata.Namespace, Pod: pod.Metadata.Name, Container: container,
		}]
		if !found {
			whole, wholeFound := usage[observability.UsageKey{
				Namespace: pod.Metadata.Namespace, Pod: pod.Metadata.Name,
			}]
			if !wholeFound {
				continue
			}
			if len(runningContainers(pod)) != 1 {
				unlabelled = true
				continue
			}
			profile = whole
		}
		out.cpu = larger(out.cpu, profile.CPU)
		out.memory = larger(out.memory, profile.Memory)
		if profile.Since != nil && (out.since == nil || profile.Since.Before(*out.since)) {
			out.since = profile.Since
		}
	}

	switch {
	case out.cpu != nil || out.memory != nil:
	case unlabelled:
		out.note = "The metrics datasource does not label usage by container, so a pod " +
			"with more than one container cannot be sized per container."
	default:
		out.note = "No usage was recorded for this container in the window."
	}
	return out
}

func larger(current *int64, reading *float64) *int64 {
	if reading == nil {
		return current
	}
	value := int64(math.Ceil(*reading))
	if current != nil && *current >= value {
		return current
	}
	return &value
}

// recommend sizes a request from a reading: the reading plus headroom, rounded
// up, never below the floor.
func recommend(observed *int64, floor, unit int64) *int64 {
	if observed == nil {
		return nil
	}
	value := int64(math.Ceil(float64(*observed) * wasteHeadroom))
	value = (value + unit - 1) / unit * unit
	value = max(value, floor)
	return &value
}

// over reports a dimension whose request exceeds its reading by more than the
// ratio, and which the recommendation would actually lower.
func over(d wasteDimension) bool {
	if d.Request == nil || d.Observed == nil || d.Recommended == nil || *d.Request <= 0 {
		return false
	}
	return float64(*d.Request) > wasteOverRatio*float64(*d.Observed) && *d.Recommended < *d.Request
}

// sortByReclaimable orders the list by what each workload gives back as a
// share of the page's total, the larger of its two resources — so neither
// unit outranks the other by being counted in smaller pieces.
func sortByReclaimable(list []wasteWorkload, total wasteSummary) {
	share := func(w wasteWorkload) float64 {
		var cpu, memory float64
		if total.ReclaimableCPU > 0 {
			cpu = float64(w.Reclaimable.CPU) / float64(total.ReclaimableCPU)
		}
		if total.ReclaimableMemory > 0 {
			memory = float64(w.Reclaimable.Memory) / float64(total.ReclaimableMemory)
		}
		return max(cpu, memory)
	}
	slices.SortStableFunc(list, func(a, b wasteWorkload) int {
		if order := cmp.Compare(share(b), share(a)); order != 0 {
			return order
		}
		if order := strings.Compare(a.Namespace, b.Namespace); order != 0 {
			return order
		}
		return strings.Compare(a.Name, b.Name)
	})
}

// unusedVolumes lists bound claims no running pod mounts, and volumes left
// released or never bound. Largest first.
func unusedVolumes(in wasteInputs) []wasteVolume {
	mounted := map[podKey]bool{}
	for _, pod := range in.pods {
		for _, volume := range pod.Spec.Volumes {
			if volume.PersistentVolumeClaim != nil {
				mounted[podKey{pod.Metadata.Namespace, volume.PersistentVolumeClaim.ClaimName}] = true
			}
		}
	}

	out := []wasteVolume{}
	for _, claim := range in.claims {
		if claim.Status.Phase != "Bound" ||
			mounted[podKey{claim.Metadata.Namespace, claim.Metadata.Name}] {
			continue
		}
		row := wasteVolume{
			Kind:      "PersistentVolumeClaim",
			Namespace: claim.Metadata.Namespace,
			Name:      claim.Metadata.Name,
			Capacity:  parseMemoryBytes(claim.Status.Capacity["storage"]),
			State:     "unmounted",
			Volume:    claim.Spec.VolumeName,
			Created:   claim.Metadata.CreationTimestamp,
		}
		if claim.Spec.StorageClassName != nil {
			row.StorageClass = *claim.Spec.StorageClassName
		}
		row.StatefulSet, row.ScaledDown = statefulSetOf(claim, in.statefulSets)
		out = append(out, row)
	}

	for _, volume := range in.volumes {
		var state string
		switch volume.Status.Phase {
		case "Released":
			state = "released"
		case "Available":
			state = "available"
		default:
			continue
		}
		row := wasteVolume{
			Kind:          "PersistentVolume",
			Name:          volume.Metadata.Name,
			Capacity:      parseMemoryBytes(volume.Spec.Capacity["storage"]),
			StorageClass:  volume.Spec.StorageClassName,
			State:         state,
			ReclaimPolicy: volume.Spec.ReclaimPolicy,
			Created:       volume.Metadata.CreationTimestamp,
		}
		if ref := volume.Spec.ClaimRef; ref != nil && state == "released" {
			row.Claim = ref.Namespace + "/" + ref.Name
		}
		out = append(out, row)
	}

	slices.SortStableFunc(out, func(a, b wasteVolume) int {
		if order := cmp.Compare(b.Capacity, a.Capacity); order != 0 {
			return order
		}
		if order := strings.Compare(a.Namespace, b.Namespace); order != 0 {
			return order
		}
		return strings.Compare(a.Name, b.Name)
	})
	return out
}

// statefulSetOf names the StatefulSet a claim was made for. The StatefulSet
// controller names a claim `<template>-<statefulset>-<ordinal>`, so a claim
// matching that shape for a StatefulSet in its namespace is one of its own —
// and one at an ordinal past its replicas was kept, on purpose, by a scale-down.
func statefulSetOf(claim wasteClaim, sets []wasteStatefulSet) (string, bool) {
	for _, set := range sets {
		if set.Metadata.Namespace != claim.Metadata.Namespace {
			continue
		}
		for _, template := range set.Spec.VolumeClaimTemplates {
			prefix := template.Metadata.Name + "-" + set.Metadata.Name + "-"
			rest, matched := strings.CutPrefix(claim.Metadata.Name, prefix)
			if !matched {
				continue
			}
			ordinal, err := strconv.Atoi(rest)
			if err != nil || ordinal < 0 {
				continue
			}
			replicas := int32(1)
			if set.Spec.Replicas != nil {
				replicas = *set.Spec.Replicas
			}
			return set.Metadata.Name, int32(ordinal) >= replicas
		}
	}
	return "", false
}

// idleLoadBalancers lists LoadBalancer Services with no ready endpoint behind
// them.
func idleLoadBalancers(in wasteInputs) []wasteLoadBalancer {
	ready := map[podKey]bool{}
	for _, slice := range in.slices {
		service := slice.Metadata.Labels["kubernetes.io/service-name"]
		if service == "" {
			continue
		}
		for _, endpoint := range slice.Endpoints {
			if endpoint.Conditions.Ready == nil || *endpoint.Conditions.Ready {
				ready[podKey{slice.Metadata.Namespace, service}] = true
				break
			}
		}
	}

	out := []wasteLoadBalancer{}
	for _, service := range in.services {
		if service.Spec.Type != "LoadBalancer" ||
			ready[podKey{service.Metadata.Namespace, service.Metadata.Name}] {
			continue
		}
		row := wasteLoadBalancer{
			Namespace: service.Metadata.Namespace,
			Name:      service.Metadata.Name,
			Addresses: []string{},
			State:     "no-ready-endpoints",
			Created:   service.Metadata.CreationTimestamp,
		}
		if len(service.Spec.Selector) == 0 {
			row.State = "no-selector"
		}
		for _, ingress := range service.Status.LoadBalancer.Ingress {
			if ingress.IP != "" {
				row.Addresses = append(row.Addresses, ingress.IP)
			} else if ingress.Hostname != "" {
				row.Addresses = append(row.Addresses, ingress.Hostname)
			}
		}
		out = append(out, row)
	}
	slices.SortStableFunc(out, func(a, b wasteLoadBalancer) int {
		if order := strings.Compare(a.Namespace, b.Namespace); order != 0 {
			return order
		}
		return strings.Compare(a.Name, b.Name)
	})
	return out
}
