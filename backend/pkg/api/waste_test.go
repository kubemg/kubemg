package api

import (
	"testing"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * Idle resource triage.
 *
 * What is pinned here is which objects are reported and which are deliberately
 * not, because the false positives are the whole risk of this feature: every
 * finding here is something somebody might delete, and three of the four shapes
 * have a perfectly ordinary reason to look exactly like this.
 */

var wasteClock = time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)

func daysAgo(days int) time.Time { return wasteClock.AddDate(0, 0, -days) }

// claimed builds a pod mounting one PersistentVolumeClaim.
func claimed(node, namespace, name, claim string) capacityPod {
	pod := podOn(node, namespace, name)
	var volume podVolume
	volume.PersistentVolumeClaim.ClaimName = claim
	pod.Spec.Volumes = append(pod.Spec.Volumes, volume)
	return pod
}

func triage(pods []capacityPod, claims []claimRecord, volumes []volumeRecord,
	services []serviceRecord, endpoints map[string]int,
) ([]wasteFinding, wasteSummary) {
	return buildWaste(rates(), pods, claims, volumes, services, endpoints, wasteClock)
}

/* ----------------------------------------------------------------- claims -- */

func TestBuildWasteFindsABoundClaimNothingMounts(t *testing.T) {
	claims := []claimRecord{{
		Name: "data", Namespace: "shop", Phase: claimBound,
		Volume: "pv-1", Bytes: 100 << 30, Created: daysAgo(90),
	}}

	findings, summary := triage(nil, claims, nil, nil, nil)

	if summary.UnmountedClaims != 1 {
		t.Fatalf("unmounted claims = %d, want 1", summary.UnmountedClaims)
	}
	got := findings[0]
	if got.Code != "unmounted-claim" || got.Namespace != "shop" || got.Name != "data" {
		t.Errorf("finding = %+v, want the shop/data claim", got)
	}
	// A hundred GiB at a dollar a GiB a month.
	if got.Monthly != 100 {
		t.Errorf("monthly = %v, want 100", got.Monthly)
	}
	if got.AgeDays != 90 {
		t.Errorf("age = %d days, want 90", got.AgeDays)
	}
	if got.Caveat == "" {
		t.Error("every finding must carry why it might legitimately look like this")
	}
}

func TestBuildWasteSkipsAClaimARunningPodMounts(t *testing.T) {
	claims := []claimRecord{{
		Name: "data", Namespace: "shop", Phase: claimBound, Volume: "pv-1", Bytes: 10 << 30,
	}}
	pods := []capacityPod{claimed("n1", "shop", "api", "data")}

	findings, _ := triage(pods, claims, nil, nil, nil)

	if len(findings) != 0 {
		t.Errorf("findings = %+v, want none — the claim is mounted", findings)
	}
}

func TestBuildWasteMatchesAClaimWithinItsOwnNamespace(t *testing.T) {
	// A pod in one namespace must not vouch for a same-named claim in another.
	claims := []claimRecord{{
		Name: "data", Namespace: "shop", Phase: claimBound, Volume: "pv-1", Bytes: 10 << 30,
	}}
	pods := []capacityPod{claimed("n1", "warehouse", "api", "data")}

	findings, _ := triage(pods, claims, nil, nil, nil)

	if len(findings) != 1 {
		t.Errorf("findings = %+v, want the shop claim reported", findings)
	}
}

func TestBuildWasteIgnoresAPendingClaim(t *testing.T) {
	// A Pending claim is a provisioning problem, not a cost one: there is no
	// volume behind it yet and nobody is billing for it.
	claims := []claimRecord{{Name: "data", Namespace: "shop", Phase: "Pending", Bytes: 10 << 30}}

	findings, _ := triage(nil, claims, nil, nil, nil)

	if len(findings) != 0 {
		t.Errorf("findings = %+v, want none for a Pending claim", findings)
	}
}

/* ---------------------------------------------------------------- volumes -- */

func TestBuildWasteFindsAReleasedVolume(t *testing.T) {
	volumes := []volumeRecord{{
		Name: "pv-old", Phase: volumeReleased, ReclaimPolicy: reclaimRetain,
		Bytes: 50 << 30, Created: daysAgo(200),
	}}

	findings, summary := triage(nil, nil, volumes, nil, nil)

	if summary.ReleasedVolumes != 1 {
		t.Fatalf("released volumes = %d, want 1", summary.ReleasedVolumes)
	}
	if findings[0].Code != "released-volume" || findings[0].Monthly != 50 {
		t.Errorf("finding = %+v, want a released volume costing 50", findings[0])
	}
}

func TestBuildWasteLeavesARecentlyAvailableVolumeAlone(t *testing.T) {
	// A pre-provisioned pool waiting for claims is a legitimate pattern, so
	// Available is only reported once it is genuinely old.
	volumes := []volumeRecord{{
		Name: "pv-new", Phase: volumeAvailable, Bytes: 10 << 30, Created: daysAgo(3),
	}}

	findings, _ := triage(nil, nil, volumes, nil, nil)

	if len(findings) != 0 {
		t.Errorf("findings = %+v, want none for a volume available for three days", findings)
	}
}

func TestBuildWasteReportsALongUnclaimedVolume(t *testing.T) {
	volumes := []volumeRecord{{
		Name: "pv-forgotten", Phase: volumeAvailable, Bytes: 20 << 30, Created: daysAgo(90),
	}}

	findings, _ := triage(nil, nil, volumes, nil, nil)

	if len(findings) != 1 || findings[0].Code != "unclaimed-volume" {
		t.Errorf("findings = %+v, want one unclaimed-volume finding", findings)
	}
}

func TestBuildWasteNeverReportsAVolumeAClaimStillHolds(t *testing.T) {
	// The volume and the claim would otherwise both be reported, which is one
	// disk counted twice.
	claims := []claimRecord{{
		Name: "data", Namespace: "shop", Phase: claimBound, Volume: "pv-1", Bytes: 10 << 30,
	}}
	volumes := []volumeRecord{{
		Name: "pv-1", Phase: volumeAvailable, Bytes: 10 << 30, Created: daysAgo(90),
	}}
	pods := []capacityPod{claimed("n1", "shop", "api", "data")}

	findings, _ := triage(pods, claims, volumes, nil, nil)

	if len(findings) != 0 {
		t.Errorf("findings = %+v, want none — the volume is bound to a mounted claim", findings)
	}
}

/* ---------------------------------------------------------- load balancers -- */

func TestBuildWasteFindsALoadBalancerWithNoReadyBackends(t *testing.T) {
	services := []serviceRecord{{
		Name: "public", Namespace: "shop", Type: serviceLB, Created: daysAgo(30),
	}}

	findings, summary := triage(nil, nil, nil, services, map[string]int{})

	if summary.IdleLoadBalancers != 1 {
		t.Fatalf("idle load balancers = %d, want 1", summary.IdleLoadBalancers)
	}
	got := findings[0]
	if got.Monthly != 10 {
		t.Errorf("monthly = %v, want the rate card's load balancer charge", got.Monthly)
	}
	// Unlike an idle disk, a load balancer with nothing behind it is usually
	// also an outage.
	if got.Severity != severityWarn {
		t.Errorf("severity = %s, want %s", got.Severity, severityWarn)
	}
}

func TestBuildWasteSkipsAServedLoadBalancer(t *testing.T) {
	services := []serviceRecord{{Name: "public", Namespace: "shop", Type: serviceLB}}
	endpoints := map[string]int{"shop/public": 3}

	findings, _ := triage(nil, nil, nil, services, endpoints)

	if len(findings) != 0 {
		t.Errorf("findings = %+v, want none — it has ready backends", findings)
	}
}

func TestBuildWasteIgnoresAClusterIPService(t *testing.T) {
	// A ClusterIP with no endpoints costs nothing; it is a readiness problem
	// that belongs to a different page.
	services := []serviceRecord{{Name: "internal", Namespace: "shop", Type: "ClusterIP"}}

	findings, _ := triage(nil, nil, nil, services, map[string]int{})

	if len(findings) != 0 {
		t.Errorf("findings = %+v, want none for a ClusterIP", findings)
	}
}

/* -------------------------------------------------------------- the total -- */

func TestBuildWasteRanksByMoneyAndTotals(t *testing.T) {
	claims := []claimRecord{
		{Name: "small", Namespace: "shop", Phase: claimBound, Volume: "a", Bytes: 5 << 30},
		{Name: "large", Namespace: "shop", Phase: claimBound, Volume: "b", Bytes: 500 << 30},
	}

	findings, summary := triage(nil, claims, nil, nil, nil)

	if findings[0].Name != "large" {
		t.Errorf("first finding = %s, want the expensive one first", findings[0].Name)
	}
	if summary.Monthly != 505 {
		t.Errorf("monthly total = %v, want 505", summary.Monthly)
	}
	if summary.Findings != 2 {
		t.Errorf("findings = %d, want 2", summary.Findings)
	}
}

func TestBuildWasteStillTriagesWithoutRates(t *testing.T) {
	// An orphaned volume is worth finding whether or not anybody has priced it.
	claims := []claimRecord{{
		Name: "data", Namespace: "shop", Phase: claimBound, Volume: "pv-1", Bytes: 100 << 30,
	}}

	findings, summary := buildWaste(db.RateCard{}, nil, claims, nil, nil, nil, wasteClock)

	if len(findings) != 1 {
		t.Fatalf("findings = %d, want 1 even with no rate card", len(findings))
	}
	if findings[0].Monthly != 0 || summary.Monthly != 0 {
		t.Errorf("unpriced findings must carry no cost, got %v", findings[0].Monthly)
	}
}

func TestAgeInDaysFloorsAClockSkew(t *testing.T) {
	// A creation timestamp in the future is a clock skew, not a negative age.
	if got := ageInDays(wasteClock.Add(time.Hour), wasteClock); got != 0 {
		t.Errorf("ageInDays = %d, want 0", got)
	}
}

func TestReclaimPolicyDefaultsToRetain(t *testing.T) {
	// An empty policy behaves as Retain on a manually created volume, and
	// Retain is the only value that leaves a disk behind at all.
	if got := reclaimPolicyOf(volumeRecord{}); got != reclaimRetain {
		t.Errorf("reclaimPolicyOf = %q, want %q", got, reclaimRetain)
	}
}
