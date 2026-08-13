package api

import (
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * The things still being paid for that nothing is using.
 *
 * Compute waste is a right-sizing problem — a workload reserving more than it
 * spends, which the cost report and the right-sizing pass beside it answer.
 * This is the other kind: objects that are not oversized but *orphaned*. A
 * volume whose pod was deleted six months ago is billed in full every month and
 * appears in no dashboard, because nothing is wrong with it. Nothing is using
 * it either.
 *
 * Three shapes, and each is reported with the reason it might legitimately look
 * like this, because every one of them has a false positive that a report
 * asserting "abandoned" would get wrong:
 *
 *   an unmounted PersistentVolumeClaim   — or a StatefulSet scaled to zero, or a
 *                                          pod that is restarting right now
 *   a Released PersistentVolume          — or a Retain policy doing its job while
 *                                          somebody restores from it
 *   a LoadBalancer Service with no ready
 *   endpoints                            — or a deployment mid-rollout
 *
 * So these are **findings to triage, not verdicts**, and the wording says which
 * is which. KubeMG deletes none of them: it has an impersonated write path and
 * deliberately does not point it at a list it assembled by inference.
 *
 * What it cannot see is worth stating too. An unattached cloud load balancer or
 * a detached elastic IP that no Service ever owned is invisible from inside the
 * cluster — there is no Kubernetes object for it — and finding those needs the
 * cloud account, which is exactly the credential KubeMG does not hold.
 */

// wasteFinding is one object nothing appears to be using.
type wasteFinding struct {
	Code      string `json:"code"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`

	Title  string `json:"title"`
	Detail string `json:"detail"`
	// Caveat is why this might be fine. It travels with every finding rather
	// than living in a footnote, because the footnote is not what gets read
	// before somebody deletes a volume.
	Caveat   string `json:"caveat"`
	Severity string `json:"severity"`

	// Monthly is what this object costs at the cluster's rates, or zero where
	// the rate card prices nothing of this shape.
	Monthly float64 `json:"monthly"`
	// Bytes is the provisioned size for a volume, and absent for anything else.
	Bytes int64 `json:"bytes,omitempty"`

	// Age is how long the object has existed, in days. An orphan is much more
	// convincing when it is old, and this is what lets the console lead with the
	// ones that are.
	AgeDays int `json:"age_days"`
}

func (f wasteFinding) sortKey() (string, string) { return f.Namespace, f.Name }

// wasteSummary totals the triage list.
type wasteSummary struct {
	Findings int     `json:"findings"`
	Monthly  float64 `json:"monthly"`

	UnmountedClaims   int `json:"unmounted_claims"`
	ReleasedVolumes   int `json:"released_volumes"`
	IdleLoadBalancers int `json:"idle_load_balancers"`
}

/* -------------------------------------------------------------- the read -- */

// clusterWaste triages the objects nothing is using.
func (s *server) clusterWaste(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	if !s.requireClusterScope(c, grant, "idle cluster resources") {
		return
	}

	// Rates are optional here in a way they are not on the cost report: an
	// orphaned volume is worth finding whether or not anybody has priced it.
	// Without a rate card the findings arrive unpriced rather than not at all.
	card, err := s.store.RateCardFor(c.Request.Context(), cluster.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not read the rate card"})
		return
	}
	rates := db.RateCard{}
	if card != nil {
		rates = *card
	}

	pods, ok := s.fetchSchedulablePods(c, user, cluster, grant)
	if !ok {
		return
	}
	claims, ok := s.fetchClaims(c, user, cluster, grant)
	if !ok {
		return
	}
	volumes, ok := s.fetchVolumes(c, user, cluster, grant)
	if !ok {
		return
	}
	services, ok := s.fetchServices(c, user, cluster, grant)
	if !ok {
		return
	}
	endpoints, ok := s.fetchReadyEndpoints(c, user, cluster, grant)
	if !ok {
		return
	}

	findings, summary := buildWaste(rates, pods, claims, volumes, services, endpoints, time.Now().UTC())

	payload := gin.H{
		"priced":   rates.Priced(),
		"findings": findings,
		"summary":  summary,
	}
	if rates.Priced() {
		payload["currency"] = rates.Currency
	} else {
		payload["reason"] = wasteUnpricedReason
	}
	c.JSON(http.StatusOK, payload)
}

// wasteUnpricedReason explains an unpriced triage list, which is still a triage
// list.
const wasteUnpricedReason = "No rates are configured, so these are listed without a cost. " +
	"What nothing is using is worth finding either way."

/* --------------------------------------------------------------- the reads -- */

// claimRecord is a PersistentVolumeClaim reduced to what triage needs.
type claimRecord struct {
	Name      string
	Namespace string
	Phase     string
	Volume    string
	Bytes     int64
	Created   time.Time
}

type claimList struct {
	Items []struct {
		Metadata objectMeta `json:"metadata"`
		Spec     struct {
			VolumeName string `json:"volumeName"`
		} `json:"spec"`
		Status struct {
			Phase    string            `json:"phase"`
			Capacity map[string]string `json:"capacity"`
		} `json:"status"`
	} `json:"items"`
}

func (s *server) fetchClaims(c *gin.Context, user *db.User, cluster *db.Cluster,
	grant db.UserClusterAccess,
) ([]claimRecord, bool) {
	var list claimList
	if !s.fetch(c, user, cluster, grant, "/api/v1/persistentvolumeclaims", &list) {
		return nil, false
	}
	out := make([]claimRecord, 0, len(list.Items))
	for _, item := range list.Items {
		out = append(out, claimRecord{
			Name:      item.Metadata.Name,
			Namespace: item.Metadata.Namespace,
			Phase:     item.Status.Phase,
			Volume:    item.Spec.VolumeName,
			// The bound capacity rather than the request: a provisioner is
			// allowed to round up, and what is billed is what was provisioned.
			Bytes:   parseMemoryBytes(item.Status.Capacity["storage"]),
			Created: item.Metadata.CreationTimestamp,
		})
	}
	return out, true
}

// volumeRecord is a PersistentVolume reduced to what triage needs.
type volumeRecord struct {
	Name          string
	Phase         string
	ReclaimPolicy string
	Bytes         int64
	Created       time.Time
}

type volumeList struct {
	Items []struct {
		Metadata objectMeta `json:"metadata"`
		Spec     struct {
			Capacity                      map[string]string `json:"capacity"`
			PersistentVolumeReclaimPolicy string            `json:"persistentVolumeReclaimPolicy"`
		} `json:"spec"`
		Status struct {
			Phase string `json:"phase"`
		} `json:"status"`
	} `json:"items"`
}

func (s *server) fetchVolumes(c *gin.Context, user *db.User, cluster *db.Cluster,
	grant db.UserClusterAccess,
) ([]volumeRecord, bool) {
	var list volumeList
	if !s.fetch(c, user, cluster, grant, "/api/v1/persistentvolumes", &list) {
		return nil, false
	}
	out := make([]volumeRecord, 0, len(list.Items))
	for _, item := range list.Items {
		out = append(out, volumeRecord{
			Name:          item.Metadata.Name,
			Phase:         item.Status.Phase,
			ReclaimPolicy: item.Spec.PersistentVolumeReclaimPolicy,
			Bytes:         parseMemoryBytes(item.Spec.Capacity["storage"]),
			Created:       item.Metadata.CreationTimestamp,
		})
	}
	return out, true
}

// serviceRecord is a Service reduced to what triage needs.
type serviceRecord struct {
	Name      string
	Namespace string
	Type      string
	Created   time.Time
}

type serviceList struct {
	Items []struct {
		Metadata objectMeta `json:"metadata"`
		Spec     struct {
			Type string `json:"type"`
		} `json:"spec"`
	} `json:"items"`
}

func (s *server) fetchServices(c *gin.Context, user *db.User, cluster *db.Cluster,
	grant db.UserClusterAccess,
) ([]serviceRecord, bool) {
	var list serviceList
	if !s.fetch(c, user, cluster, grant, "/api/v1/services", &list) {
		return nil, false
	}
	out := make([]serviceRecord, 0, len(list.Items))
	for _, item := range list.Items {
		out = append(out, serviceRecord{
			Name:      item.Metadata.Name,
			Namespace: item.Metadata.Namespace,
			Type:      item.Spec.Type,
			Created:   item.Metadata.CreationTimestamp,
		})
	}
	return out, true
}

// serviceNameLabel is how an EndpointSlice says which Service it belongs to.
const serviceNameLabel = "kubernetes.io/service-name"

// endpointSliceList is the slice list reduced to readiness.
type endpointSliceList struct {
	Items []struct {
		Metadata  objectMeta `json:"metadata"`
		Endpoints []struct {
			Conditions struct {
				Ready *bool `json:"ready"`
			} `json:"conditions"`
		} `json:"endpoints"`
	} `json:"items"`
}

// fetchReadyEndpoints counts the ready endpoints behind every Service.
//
// EndpointSlices rather than the older Endpoints object: a Service with more
// than a hundred backends is split across several Endpoints objects in a way
// that is awkward to total, and every supported Kubernetes version serves
// slices. The readiness condition is a pointer because an unset condition means
// ready — an endpoint that never declares itself unready is one that is up.
func (s *server) fetchReadyEndpoints(c *gin.Context, user *db.User, cluster *db.Cluster,
	grant db.UserClusterAccess,
) (map[string]int, bool) {
	var list endpointSliceList
	if !s.fetch(c, user, cluster, grant, "/apis/discovery.k8s.io/v1/endpointslices", &list) {
		return nil, false
	}
	out := map[string]int{}
	for _, item := range list.Items {
		service := item.Metadata.Labels[serviceNameLabel]
		if service == "" {
			continue
		}
		for _, endpoint := range item.Endpoints {
			if endpoint.Conditions.Ready == nil || *endpoint.Conditions.Ready {
				out[item.Metadata.Namespace+"/"+service]++
			}
		}
	}
	return out, true
}

/* ------------------------------------------------------------ the triage -- */

// Kubernetes phases and policies this report reasons about.
const (
	claimBound      = "Bound"
	volumeReleased  = "Released"
	volumeAvailable = "Available"
	reclaimRetain   = "Retain"
	serviceLB       = "LoadBalancer"
)

// buildWaste is the triage, as a pure function of the reads and the clock.
func buildWaste(card db.RateCard, pods []capacityPod, claims []claimRecord,
	volumes []volumeRecord, services []serviceRecord, endpoints map[string]int,
	now time.Time,
) ([]wasteFinding, wasteSummary) {
	findings := []wasteFinding{}
	summary := wasteSummary{}

	mounted := mountedClaims(pods)
	boundVolumes := map[string]bool{}

	for _, claim := range claims {
		if claim.Volume != "" {
			boundVolumes[claim.Volume] = true
		}
		if claim.Phase != claimBound {
			// A Pending claim is a provisioning problem rather than a cost one,
			// and it is billing nobody: there is no volume behind it yet.
			continue
		}
		if mounted[claim.Namespace+"/"+claim.Name] {
			continue
		}
		summary.UnmountedClaims++
		findings = append(findings, wasteFinding{
			Code: "unmounted-claim", Kind: "PersistentVolumeClaim",
			Name: claim.Name, Namespace: claim.Namespace,
			Title: "Volume is bound but nothing mounts it",
			Detail: "This claim has a volume provisioned behind it and no running pod is using " +
				"it. A provisioned volume is billed for its whole size whether or not anything " +
				"reads from it.",
			Caveat: "A StatefulSet scaled to zero keeps its claims on purpose, and a pod " +
				"restarting at this moment has released its mount for a few seconds. Check what " +
				"used to use it before deleting anything.",
			Severity: severityNote,
			Monthly:  money(storagePrice(card, claim.Bytes)),
			Bytes:    claim.Bytes,
			AgeDays:  ageInDays(claim.Created, now),
		})
	}

	for _, volume := range volumes {
		// Released is the state that costs money: the claim is gone, the disk is
		// not, and nothing will ever bind to it again without an operator
		// clearing the stale claimRef by hand.
		released := volume.Phase == volumeReleased
		// Available is a provisioned volume waiting for a claim. It is only
		// waste where somebody pre-provisioned and never used it, so it is
		// reported at a lower bar and only when it is genuinely old.
		stale := volume.Phase == volumeAvailable && ageInDays(volume.Created, now) >= 30
		if !released && !stale {
			continue
		}
		if boundVolumes[volume.Name] {
			continue
		}

		summary.ReleasedVolumes++
		finding := wasteFinding{
			Code: "released-volume", Kind: "PersistentVolume",
			Name:     volume.Name,
			Severity: severityNote,
			Monthly:  money(storagePrice(card, volume.Bytes)),
			Bytes:    volume.Bytes,
			AgeDays:  ageInDays(volume.Created, now),
		}
		if released {
			finding.Title = "Volume was released and still exists"
			finding.Detail = "The claim that owned this volume is gone, but its reclaim policy " +
				"is " + reclaimPolicyOf(volume) + ", so the disk behind it was kept and is " +
				"still billed. Nothing will bind to it again until somebody clears its stale " +
				"claim reference."
			finding.Caveat = "Retain is frequently deliberate: it is how a volume survives a " +
				"namespace being deleted, and how a restore is done. Age is the thing to read here."
		} else {
			finding.Code = "unclaimed-volume"
			finding.Title = "Volume has waited a month for a claim"
			finding.Detail = "This volume is Available — provisioned, billed, and never bound " +
				"to a claim in the last thirty days."
			finding.Caveat = "A pre-provisioned pool of volumes looks exactly like this and is " +
				"a legitimate pattern."
		}
		findings = append(findings, finding)
	}

	for _, service := range services {
		if service.Type != serviceLB {
			continue
		}
		if endpoints[service.Namespace+"/"+service.Name] > 0 {
			continue
		}
		summary.IdleLoadBalancers++
		findings = append(findings, wasteFinding{
			Code: "idle-load-balancer", Kind: "Service",
			Name: service.Name, Namespace: service.Namespace,
			Title: "Load balancer has no ready backends",
			Detail: "This Service is of type LoadBalancer, so the cloud provisioned one and " +
				"charges for it standing there. No endpoint behind it is ready, so anything " +
				"reaching it is being refused.",
			Caveat: "A deployment mid-rollout has no ready endpoints for a few seconds, and a " +
				"service whose pods are all failing readiness is an outage rather than a saving.",
			// This one is a warn rather than a note: unlike an idle disk, a load
			// balancer with nothing behind it is usually also broken.
			Severity: severityWarn,
			Monthly:  money(card.LoadBalancerMonth),
			AgeDays:  ageInDays(service.Created, now),
		})
	}

	slices.SortFunc(findings, func(a, b wasteFinding) int {
		if a.Monthly != b.Monthly {
			if a.Monthly > b.Monthly {
				return -1
			}
			return 1
		}
		if a.AgeDays != b.AgeDays {
			return b.AgeDays - a.AgeDays
		}
		if order := strings.Compare(a.Namespace, b.Namespace); order != 0 {
			return order
		}
		return strings.Compare(a.Name, b.Name)
	})

	var total float64
	for _, finding := range findings {
		total += finding.Monthly
	}
	summary.Findings = len(findings)
	summary.Monthly = money(total)
	return findings, summary
}

// mountedClaims is every claim a running pod has mounted.
func mountedClaims(pods []capacityPod) map[string]bool {
	out := map[string]bool{}
	for _, pod := range pods {
		for _, volume := range pod.Spec.Volumes {
			if volume.PersistentVolumeClaim.ClaimName != "" {
				out[pod.Metadata.Namespace+"/"+volume.PersistentVolumeClaim.ClaimName] = true
			}
		}
	}
	return out
}

// reclaimPolicyOf names the policy in the sentence a finding writes, defaulting
// to Retain — which is what an empty policy behaves as on a manually created
// volume, and the only value that leaves a disk behind at all.
func reclaimPolicyOf(volume volumeRecord) string {
	if volume.ReclaimPolicy == "" {
		return reclaimRetain
	}
	return volume.ReclaimPolicy
}

// storagePrice prices a provisioned size for a month.
func storagePrice(card db.RateCard, bytes int64) float64 {
	if bytes <= 0 {
		return 0
	}
	return float64(bytes) / bytesPerGiB * card.StorageGiBMonth
}

// ageInDays is how old an object is, floored at zero — a creation timestamp in
// the future is a clock skew, not a negative age.
func ageInDays(created, now time.Time) int {
	if created.IsZero() {
		return 0
	}
	days := int(now.Sub(created).Hours() / 24)
	return max(days, 0)
}
