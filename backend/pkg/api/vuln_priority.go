package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/bastion"
	"github.com/kubemg/kubemg/backend/pkg/db"
	"github.com/kubemg/kubemg/backend/pkg/observability"
)

/*
 * What decides which findings come first: CISA's KEV catalogue, FIRST's EPSS
 * score, and the risks somebody accepted. See db/vuln_intel.go and
 * db/vuln_acceptance.go for the stores, vuln_intel.go for keeping the lists.
 *
 * The vulnerability_kev alarm fires on a *transition*: a workload that was not
 * running a known-exploited vulnerability at the last pass and is now — a new
 * deployment, or, the case it exists for, CISA adding a CVE the cluster already
 * runs. The first pass with a KEV list in hand is where things stand, not news,
 * so it fires nothing (db.ClusterVulnScanning.KEVWatermark).
 */

var (
	// cveIDPattern is a CVE identifier — the only kind KEV and EPSS carry.
	cveIDPattern = regexp.MustCompile(`^CVE-[0-9]{4}-[0-9]{4,}$`)
	// vulnIDPattern is any identifier a scanner reports (CVE, GHSA, a distro
	// advisory), which is what an acceptance may name.
	vulnIDPattern = regexp.MustCompile(`^[A-Z0-9][A-Z0-9._:-]{2,59}$`)
)

const (
	verbRiskAccept   = "risk-accept"
	verbRiskWithdraw = "risk-withdraw"

	// maxAcceptanceDays bounds an expiry. An acceptance with none is allowed,
	// and said; one with a date further out than this is a date nobody will
	// remember setting.
	maxAcceptanceDays = 365
)

/* ---------------------------------------------------------- summariser --- */

type vulnAcceptKey struct {
	workload vulnWorkloadKey
	cve      string
}

// vulnIntel is what a summary pass knows beyond the reports. A nil kev means
// no catalogue has arrived: nothing is called exploited, and nothing fires.
type vulnIntel struct {
	kev      map[string]db.VulnKEVEntry
	accepted map[vulnAcceptKey]bool
}

func (i vulnIntel) kevKnown() bool { return len(i.kev) > 0 }

func (i vulnIntel) exploited(id string) bool {
	_, ok := i.kev[id]
	return ok
}

func (i vulnIntel) accepts(workload vulnWorkloadKey, id string) bool {
	return i.accepted[vulnAcceptKey{workload: workload, cve: id}]
}

func (s *server) loadVulnIntel(ctx context.Context, clusterID uint, now time.Time) (vulnIntel, error) {
	intel := vulnIntel{accepted: map[vulnAcceptKey]bool{}}
	acceptances, err := s.store.VulnAcceptances(ctx, clusterID)
	if err != nil {
		return intel, err
	}
	for _, a := range acceptances {
		if a.Live(now) {
			intel.accepted[vulnAcceptKey{workload: vulnWorkloadKey{a.Namespace, a.Kind, a.Name}, cve: a.CVE}] = true
		}
	}
	kev, err := s.store.KEVCatalogue(ctx)
	if err != nil {
		// Read as "not known": a pass without KEV fires nothing and moves the
		// watermark back, so a database blip cannot become a burst of pages.
		s.log().Warn("vulnerability summary could not read the KEV catalogue", "error", err.Error())
		return intel, nil
	}
	if len(kev) > 0 {
		intel.kev = kev
	}
	return intel, nil
}

// vulnKEVBaseline is the last pass, as the alarm compares against it.
type vulnKEVBaseline struct {
	watermark bool
	kev       map[vulnWorkloadKey][]string
}

func (s *server) vulnKEVBaseline(ctx context.Context, clusterID uint) (vulnKEVBaseline, error) {
	baseline := vulnKEVBaseline{kev: map[vulnWorkloadKey][]string{}}
	row, err := s.store.ClusterVulnScanning(ctx, clusterID)
	if err != nil {
		return baseline, err
	}
	baseline.watermark = row.KEVWatermark
	if !baseline.watermark {
		return baseline, nil
	}
	summaries, err := s.store.VulnSummaries(ctx, clusterID)
	if err != nil {
		return baseline, err
	}
	for _, summary := range summaries {
		baseline.kev[vulnWorkloadKey{summary.Namespace, summary.Kind, summary.Name}] = summary.KEVIDs
	}
	return baseline, nil
}

// vulnKEVSignals is one signal per workload that gained a known-exploited
// vulnerability since the last pass.
func vulnKEVSignals(cluster *db.Cluster, previous vulnKEVBaseline, pass db.VulnSummaryPass) []observability.Signal {
	if !previous.watermark || !pass.KEVKnown {
		return nil
	}
	var out []observability.Signal
	for _, row := range pass.Rows {
		before := previous.kev[vulnWorkloadKey{row.Namespace, row.Kind, row.Name}]
		var gained []string
		for _, id := range row.KEVIDs {
			if !slices.Contains(before, id) {
				gained = append(gained, id)
			}
		}
		if len(gained) == 0 {
			continue
		}
		object := strings.ToLower(row.Kind) + "/" + row.Name
		message := fmt.Sprintf("%s in %s runs %s, on CISA's Known Exploited Vulnerabilities catalogue",
			object, row.Namespace, strings.Join(gained, ", "))
		if exposed := row.Exposed; exposed {
			message += "; it is reachable from outside the cluster"
		}
		out = append(out, observability.Signal{
			Source:      observability.SourceVulnerabilityKEV,
			At:          pass.At,
			ClusterID:   cluster.ID,
			Cluster:     cluster.Name,
			Namespace:   row.Namespace,
			Reason:      "KnownExploitedVulnerability",
			Object:      object,
			Message:     message + ".",
			Fingerprint: fmt.Sprintf("%s/%d/%s/%s/%s", observability.SourceVulnerabilityKEV, cluster.ID, row.Namespace, object, strings.Join(gained, ",")),
		})
	}
	return out
}

/* -------------------------------------------------------------- drawer --- */

// vulnDrawerIntel is what the drawer adds to each finding.
type vulnDrawerIntel struct {
	kev       map[string]db.VulnKEVEntry
	epss      map[string]db.VulnEPSSEntry
	accepted  map[string]db.VulnAcceptance
	kevKnown  bool
	epssKnown bool
}

func findingID(f trivyFinding) string { return strings.ToUpper(strings.TrimSpace(f.VulnerabilityID)) }

func (i vulnDrawerIntel) isKEV(f trivyFinding) bool {
	_, ok := i.kev[findingID(f)]
	return ok
}

func (i vulnDrawerIntel) isAccepted(f trivyFinding) bool {
	_, ok := i.accepted[findingID(f)]
	return ok
}

func (i vulnDrawerIntel) epssOf(f trivyFinding) float64 { return i.epss[findingID(f)].Score }

func (i vulnDrawerIntel) decorate(view vulnFindingView) vulnFindingView {
	id := strings.ToUpper(view.ID)
	if entry, ok := i.kev[id]; ok {
		view.KEV = &vulnKEVView{Added: entry.DateAdded, Due: entry.DueDate, Ransomware: entry.Ransomware, Name: entry.Name}
	}
	if entry, ok := i.epss[id]; ok {
		score, percentile := entry.Score, entry.Percentile
		view.EPSS, view.EPSSPercentile = &score, &percentile
	}
	if entry, ok := i.accepted[id]; ok {
		accepted := entry
		view.Accepted = &accepted
	}
	return view
}

func (s *server) loadVulnDrawerIntel(ctx context.Context, clusterID uint, workload vulnWorkloadKey, reports []trivyReport) (vulnDrawerIntel, error) {
	intel := vulnDrawerIntel{kev: map[string]db.VulnKEVEntry{}, epss: map[string]db.VulnEPSSEntry{}, accepted: map[string]db.VulnAcceptance{}}
	seen := map[string]bool{}
	var cves []string
	for _, report := range reports {
		for _, finding := range report.Report.Vulnerabilities {
			if id := findingID(finding); cveIDPattern.MatchString(id) && !seen[id] {
				seen[id] = true
				cves = append(cves, id)
			}
		}
	}
	sources, err := s.store.VulnIntelSources(ctx)
	if err != nil {
		return intel, err
	}
	for _, source := range sources {
		switch source.Kind {
		case db.IntelKEV:
			intel.kevKnown = source.Entries > 0
		case db.IntelEPSS:
			intel.epssKnown = source.Entries > 0
		}
	}
	if intel.kevKnown {
		catalogue, err := s.store.KEVCatalogue(ctx)
		if err != nil {
			return intel, err
		}
		for _, id := range cves {
			if entry, ok := catalogue[id]; ok {
				intel.kev[id] = entry
			}
		}
	}
	if intel.epssKnown {
		if intel.epss, err = s.store.EPSSFor(ctx, cves); err != nil {
			return intel, err
		}
	}
	acceptances, err := s.store.VulnAcceptances(ctx, clusterID)
	if err != nil {
		return intel, err
	}
	now := time.Now()
	for _, a := range acceptances {
		if a.Namespace == workload.namespace && a.Kind == workload.kind && a.Name == workload.name && a.Live(now) {
			intel.accepted[a.CVE] = a
		}
	}
	return intel, nil
}

/* ---------------------------------------------------------- acceptance --- */

type vulnAcceptRequest struct {
	Kind      string     `json:"kind"`
	Namespace string     `json:"namespace"`
	Name      string     `json:"name"`
	CVE       string     `json:"cve"`
	Reason    string     `json:"reason"`
	ExpiresAt *time.Time `json:"expires_at"`
}

// vulnAcceptTarget validates the workload and identifier an acceptance names.
func vulnAcceptTarget(c *gin.Context, resource, namespace, name, cve string) (vulnWorkloadKey, string, bool) {
	kind, known := vulnWorkloadKinds[strings.TrimSpace(resource)]
	namespace, name = strings.TrimSpace(namespace), strings.TrimSpace(name)
	cve = strings.ToUpper(strings.TrimSpace(cve))
	// Kubernetes' own components: one cluster-scoped row, no namespace.
	components := strings.TrimSpace(resource) == "kubernetes"
	if components {
		kind, known, namespace = vulnKindKubernetes, true, ""
	}
	switch {
	case !known:
		c.JSON(http.StatusBadRequest, gin.H{"error": "an accepted risk belongs to a workload: kind must be one of " +
			"deployments, statefulsets, daemonsets, cronjobs, jobs, pods, replicasets or kubernetes"})
	case (!components && !alarmObjectName.MatchString(namespace)) || !alarmObjectName.MatchString(name):
		c.JSON(http.StatusBadRequest, gin.H{"error": "namespace and name must be Kubernetes names"})
	case !vulnIDPattern.MatchString(cve):
		c.JSON(http.StatusBadRequest, gin.H{"error": "cve must be a vulnerability identifier, such as CVE-2024-3094"})
	default:
		return vulnWorkloadKey{namespace: namespace, kind: kind, name: name}, cve, true
	}
	return vulnWorkloadKey{}, "", false
}

func (s *server) recordRiskAudit(c *gin.Context, user *db.User, cluster *db.Cluster, verb string, key vulnWorkloadKey, cve, reason string) {
	if s.auditor == nil {
		return
	}
	s.auditor.Record(c.Request.Context(), bastion.Event{
		At: time.Now().UTC(), UserID: user.ID, Username: user.Username,
		ClusterID: cluster.ID, Cluster: cluster.Name, Verb: verb,
		Method: c.Request.Method, Path: c.Request.URL.Path, Namespace: key.namespace,
		Resource: "vulnerabilities/" + strings.ToLower(key.kind) + "/" + key.name + "/" + cve,
		Status:   http.StatusOK,
		// The reason, in the one free-text field an audit row has — the
		// posture acknowledgement's precedent.
		Error: reason,
	})
}

// refreshAfterAcceptance re-reads the cluster so the counts reflect the
// change now. Failing that is said, not fatal: the acceptance is stored and
// the next pass counts it.
func (s *server) refreshAfterAcceptance(ctx context.Context, cluster *db.Cluster) string {
	if err := s.summarizeVulnCluster(ctx, cluster); err != nil {
		return "Saved. The counts could not be refreshed now (" + err.Error() + "); the next pass, within ten minutes, includes it."
	}
	return "Saved, and the counts are refreshed."
}

func (s *server) acceptVulnRisk(c *gin.Context) {
	cluster, ok := s.vulnFindingsCluster(c)
	if !ok {
		return
	}
	user, ok := s.currentUser(c)
	if !ok {
		return
	}
	var request vulnAcceptRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "the request could not be read"})
		return
	}
	key, cve, ok := vulnAcceptTarget(c, request.Kind, request.Namespace, request.Name, request.CVE)
	if !ok {
		return
	}
	reason := strings.TrimSpace(request.Reason)
	if reason == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "an accepted risk needs a reason: it is what the next reader of the trail has to go on"})
		return
	}
	if len(reason) > maxAckReasonLength {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("the reason is longer than %d characters", maxAckReasonLength)})
		return
	}
	now := time.Now().UTC()
	if request.ExpiresAt != nil {
		expires := request.ExpiresAt.UTC()
		if !expires.After(now) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "the expiry must be in the future"})
			return
		}
		// A day's grace past the bound, for a browser clock a little ahead.
		if expires.After(now.AddDate(0, 0, maxAcceptanceDays+1)) {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("the expiry must be within %d days", maxAcceptanceDays)})
			return
		}
		request.ExpiresAt = &expires
	}
	ctx := c.Request.Context()
	if _, err := s.store.ClusterVulnScanning(ctx, cluster.ID); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			c.JSON(http.StatusConflict, gin.H{"error": "this cluster is not connected to a scanner"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load the scanning settings"})
		return
	}
	row := db.VulnAcceptance{
		ClusterID: cluster.ID, Namespace: key.namespace, Kind: key.kind, Name: key.name, CVE: cve,
		Reason: reason, ExpiresAt: request.ExpiresAt, AcceptedBy: user.Username, AcceptedID: user.ID,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.PutVulnAcceptance(ctx, &row); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not save the accepted risk"})
		return
	}
	s.recordRiskAudit(c, user, cluster, verbRiskAccept, key, cve, reason)
	c.JSON(http.StatusOK, gin.H{"acceptance": row, "message": s.refreshAfterAcceptance(ctx, cluster)})
}

func (s *server) withdrawVulnRisk(c *gin.Context) {
	cluster, ok := s.vulnFindingsCluster(c)
	if !ok {
		return
	}
	user, ok := s.currentUser(c)
	if !ok {
		return
	}
	key, cve, ok := vulnAcceptTarget(c, c.Query("kind"), c.Query("namespace"), c.Query("name"), c.Query("cve"))
	if !ok {
		return
	}
	ctx := c.Request.Context()
	err := s.store.DeleteVulnAcceptance(ctx, cluster.ID, key.namespace, key.kind, key.name, cve)
	if errors.Is(err, db.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "no accepted risk for that finding"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not withdraw the accepted risk"})
		return
	}
	s.recordRiskAudit(c, user, cluster, verbRiskWithdraw, key, cve, "")
	c.JSON(http.StatusOK, gin.H{"message": s.refreshAfterAcceptance(ctx, cluster)})
}

// listVulnAcceptances answers every acceptance on the cluster, expired ones
// marked, so a review of what was accepted does not need the drawer.
func (s *server) listVulnAcceptances(c *gin.Context) {
	cluster, ok := s.vulnFindingsCluster(c)
	if !ok {
		return
	}
	rows, err := s.store.VulnAcceptances(c.Request.Context(), cluster.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load the accepted risks"})
		return
	}
	type view struct {
		db.VulnAcceptance
		Expired bool `json:"expired"`
	}
	now := time.Now()
	out := make([]view, 0, len(rows))
	for _, row := range rows {
		out = append(out, view{VulnAcceptance: row, Expired: !row.Live(now)})
	}
	c.JSON(http.StatusOK, gin.H{"acceptances": out})
}
