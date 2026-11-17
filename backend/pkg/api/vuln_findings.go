package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * Vulnerability alerts, PR 2: showing what the scanner found — to the people a
 * map of what is vulnerable is safe with.
 *
 * Who sees it is decided here and nowhere else: a KubeMG administrator, or a
 * person whose grant on this cluster is cluster-admin with no namespace scope.
 * Anyone else gets a 404 from every route below — not an empty list, which
 * would say "nothing is vulnerable" — and the console asks only because the
 * server will answer. A narrower audience (developers reading their own
 * namespaces' findings) is a later, explicit option.
 *
 * What is shown is a summary a watcher keeps, not the reports themselves. A
 * report is one container image with every finding in it; the watcher reads
 * them behind a lease, every vulnPollInterval, and writes counts per workload.
 * The drawer reads one workload's reports live, by the names the summary kept.
 *
 * Red has to keep meaning something. A real cluster carries hundreds of
 * base-image criticals, and a sidebar permanently rust is a sidebar nobody
 * looks at within a week. So:
 *
 *   - danger: a critical finding that has a fix, on a workload reachable from
 *     outside the cluster (a LoadBalancer or NodePort Service, or a Service an
 *     Ingress or HTTPRoute sends to, selects its pods);
 *   - warn:   a critical or high finding that has a fix;
 *   - none:   everything else — still counted, shown as numbers, never coloured.
 *
 * KEV-listed findings join danger, and accepted risk leaves it, in PR 3.
 */

const (
	// vulnPollInterval is how often a connected cluster's reports are read.
	// Trivy rescans an image when its workload changes and otherwise every 24
	// hours by default, so ten minutes catches a new report without re-reading
	// the same thousand.
	vulnPollInterval = 10 * time.Minute
	vulnLeaseTTL     = 3 * vulnPollInterval

	// vulnReportPageSize is small on purpose: a report with five hundred
	// findings is a few hundred kilobytes, and a page has to fit the agent's
	// response ceiling with room to spare.
	vulnReportPageSize = 20
	// maxVulnReports bounds one pass. A cluster with more containers than this
	// is summarised from the first maxVulnReports and says so.
	maxVulnReports = 5000

	// maxDrawerReports bounds the drawer's live reads for one workload — one
	// report per container.
	maxDrawerReports = 12
	// maxDrawerFindings is how many findings per container the drawer gets,
	// most urgent first. The total is always given alongside.
	maxDrawerFindings = 50
)

// Trivy's labels on a report, naming what it scanned.
const (
	trivyLabelKind      = "trivy-operator.resource.kind"
	trivyLabelName      = "trivy-operator.resource.name"
	trivyLabelNamespace = "trivy-operator.resource.namespace"
	trivyLabelContainer = "trivy-operator.container.name"
)

/* ----------------------------------------------------------- visibility --- */

// vulnVisible is the whole audience rule.
func vulnVisible(user *db.User, grant db.UserClusterAccess) bool {
	if user.IsAdmin() {
		return true
	}
	return grant.K8sRole == db.K8sRoleClusterAdmin && len(grant.NamespaceList()) == 0
}

// vulnFindingsCluster loads the cluster for a findings route and decides the
// audience. Anyone outside it gets the 404 an unknown route would give.
func (s *server) vulnFindingsCluster(c *gin.Context) (*db.Cluster, bool) {
	user, cluster, grant, _, ok := s.loadAuthorizedCluster(c)
	if !ok {
		return nil, false
	}
	if !vulnVisible(user, grant) {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return nil, false
	}
	if connectionMode(*cluster) != db.ModeAgent {
		c.JSON(http.StatusConflict, gin.H{
			"error": "vulnerability reports are read through the agent tunnel - a direct-mode cluster has none",
		})
		return nil, false
	}
	return cluster, true
}

/* ------------------------------------------------------------ the reads --- */

type trivyFinding struct {
	VulnerabilityID  string   `json:"vulnerabilityID"`
	Resource         string   `json:"resource"`
	InstalledVersion string   `json:"installedVersion"`
	FixedVersion     string   `json:"fixedVersion"`
	Severity         string   `json:"severity"`
	Title            string   `json:"title"`
	PrimaryLink      string   `json:"primaryLink"`
	Score            *float64 `json:"score"`
	PublishedDate    string   `json:"publishedDate"`
}

// trivyReport is the part of a VulnerabilityReport KubeMG reads — an
// allowlist, like every response struct that crosses from a controller.
type trivyReport struct {
	Metadata struct {
		Name      string            `json:"name"`
		Namespace string            `json:"namespace"`
		Labels    map[string]string `json:"labels"`
	} `json:"metadata"`
	Report struct {
		UpdateTimestamp *time.Time `json:"updateTimestamp"`
		Registry        struct {
			Server string `json:"server"`
		} `json:"registry"`
		Artifact struct {
			Repository string `json:"repository"`
			Tag        string `json:"tag"`
			Digest     string `json:"digest"`
		} `json:"artifact"`
		Summary struct {
			CriticalCount int `json:"criticalCount"`
			HighCount     int `json:"highCount"`
			MediumCount   int `json:"mediumCount"`
			LowCount      int `json:"lowCount"`
			UnknownCount  int `json:"unknownCount"`
		} `json:"summary"`
		Vulnerabilities []trivyFinding `json:"vulnerabilities"`
	} `json:"report"`
}

// image is the scanned image as somebody would type it.
func (r trivyReport) image() string {
	artifact := r.Report.Artifact
	if artifact.Repository == "" {
		return ""
	}
	out := artifact.Repository
	if server := r.Report.Registry.Server; server != "" && server != "index.docker.io" {
		out = server + "/" + out
	}
	switch {
	case artifact.Tag != "":
		out += ":" + artifact.Tag
	case artifact.Digest != "":
		out += "@" + artifact.Digest
	}
	return out
}

type vulnPod struct {
	Metadata struct {
		Name            string            `json:"name"`
		Namespace       string            `json:"namespace"`
		Labels          map[string]string `json:"labels"`
		OwnerReferences []ownerRef        `json:"ownerReferences"`
	} `json:"metadata"`
}

type vulnWorkloadKey struct{ namespace, kind, name string }

// readVulnReports pages through every workload report, small pages at a time.
func readVulnReports(read trafficReader) ([]trivyReport, bool, error) {
	var out []trivyReport
	cont := ""
	for {
		status, body, err := read.get(pagedPath(trivyAPI+"vulnerabilityreports", vulnReportPageSize, cont))
		if err != nil {
			return nil, false, err
		}
		if status == http.StatusGone && cont != "" {
			return out, true, nil
		}
		if status < 200 || status >= 300 {
			return nil, false, fmt.Errorf("the cluster refused the report list: %s", kubeErrorMessage(body, status))
		}
		var page struct {
			Metadata struct {
				Continue string `json:"continue"`
			} `json:"metadata"`
			Items []trivyReport `json:"items"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, false, errors.New("the cluster returned an unreadable report list")
		}
		out = append(out, page.Items...)
		if len(out) >= maxVulnReports {
			return out[:maxVulnReports], page.Metadata.Continue != "" || len(out) > maxVulnReports, nil
		}
		cont = page.Metadata.Continue
		if cont == "" {
			return out, false, nil
		}
	}
}

// readVulnContext lists a kind cluster-wide. A 404 is "not served" and an empty list;
// any other refusal is handed back as a note rather than failing the pass —
// exposure is a qualifier on the findings, not the findings.
func readVulnContext[T any](read trafficReader, path, label string) ([]T, string, error) {
	var transport error
	fetch := func(limit int, cont string) (int, []byte, bool) {
		code, body, err := read.get(pagedPath(path, limit, cont))
		if err != nil {
			transport = err
			return 0, nil, false
		}
		return code, body, true
	}
	var out []T
	walk, ok := walkListPages(fetch, "", &out)
	switch {
	case !ok:
		return nil, "", transport
	case walk.status == http.StatusNotFound:
		return nil, "", nil
	case walk.status != 0:
		return nil, label + " could not be read (" + kubeErrorMessage(walk.body, walk.status) + ")", nil
	case walk.unreadable:
		return nil, label + " came back unreadable", nil
	case walk.truncated:
		return out, label + " were read only in part", nil
	}
	return out, "", nil
}

// vulnExposure reads what reaches into the cluster and returns the workloads
// whose pods it selects. It also returns each ReplicaSet's Deployment, taken
// from the pods' own pod-template-hash — the same step podOwner takes.
func vulnExposure(read trafficReader) (map[vulnWorkloadKey]bool, map[vulnWorkloadKey]string, []string, error) {
	var notes []string
	pods, note, err := readVulnContext[vulnPod](read, "/api/v1/pods", "pods")
	if err != nil {
		return nil, nil, nil, err
	}
	if note != "" {
		notes = append(notes, note)
	}
	services, note, err := readVulnContext[serviceObject](read, "/api/v1/services", "services")
	if err != nil {
		return nil, nil, nil, err
	}
	if note != "" {
		notes = append(notes, note)
	}
	ingresses, note, err := readVulnContext[ingressObject](read, "/apis/networking.k8s.io/v1/ingresses", "ingresses")
	if err != nil {
		return nil, nil, nil, err
	}
	if note != "" {
		notes = append(notes, note)
	}
	routes, note, err := readVulnContext[httpRouteObject](read, "/apis/gateway.networking.k8s.io/v1/httproutes", "HTTPRoutes")
	if err != nil {
		return nil, nil, nil, err
	}
	if note != "" {
		notes = append(notes, note)
	}

	// Which Services something outside the cluster can reach.
	reached := map[string]bool{}
	for _, service := range services {
		if service.Spec.Type == "LoadBalancer" || service.Spec.Type == "NodePort" {
			reached[service.Metadata.Namespace+"/"+service.Metadata.Name] = true
		}
	}
	for _, ingress := range ingresses {
		namespace := ingress.Metadata.Namespace
		backends := []*ingressBackend{ingress.Spec.DefaultBackend}
		for _, rule := range ingress.Spec.Rules {
			if rule.HTTP == nil {
				continue
			}
			for i := range rule.HTTP.Paths {
				backends = append(backends, &rule.HTTP.Paths[i].Backend)
			}
		}
		for _, backend := range backends {
			if backend != nil && backend.Service != nil && backend.Service.Name != "" {
				reached[namespace+"/"+backend.Service.Name] = true
			}
		}
	}
	for _, route := range routes {
		for _, rule := range route.Spec.Rules {
			for _, ref := range rule.BackendRefs {
				if namespace, ok := ref.serviceTarget(route.Metadata.Namespace); ok && ref.Name != "" {
					reached[namespace+"/"+ref.Name] = true
				}
			}
		}
	}

	podsByNamespace := map[string][]vulnPod{}
	deployments := map[vulnWorkloadKey]string{}
	for _, pod := range pods {
		podsByNamespace[pod.Metadata.Namespace] = append(podsByNamespace[pod.Metadata.Namespace], pod)
		ref := controllerOf(pod.Metadata.OwnerReferences)
		if ref.Kind == "ReplicaSet" {
			if owner := podOwner(pod.Metadata.OwnerReferences, pod.Metadata.Labels); owner != nil && owner.Kind == "Deployment" {
				deployments[vulnWorkloadKey{pod.Metadata.Namespace, "ReplicaSet", ref.Name}] = owner.Name
			}
		}
	}

	exposed := map[vulnWorkloadKey]bool{}
	for _, service := range services {
		if !reached[service.Metadata.Namespace+"/"+service.Metadata.Name] || len(service.Spec.Selector) == 0 {
			continue
		}
		for _, pod := range podsByNamespace[service.Metadata.Namespace] {
			if !selects(service.Spec.Selector, pod.Metadata.Labels) {
				continue
			}
			kind, name := "Pod", pod.Metadata.Name
			if owner := podOwner(pod.Metadata.OwnerReferences, pod.Metadata.Labels); owner != nil {
				kind, name = owner.Kind, owner.Name
			}
			exposed[vulnWorkloadKey{pod.Metadata.Namespace, kind, name}] = true
		}
	}
	return exposed, deployments, notes, nil
}

func selects(selector, labels map[string]string) bool {
	for key, value := range selector {
		if labels[key] != value {
			return false
		}
	}
	return true
}

func fixable(finding trivyFinding) bool { return strings.TrimSpace(finding.FixedVersion) != "" }

// vulnLevel is the red-fatigue rule.
func vulnLevel(row db.VulnWorkloadSummary) string {
	switch {
	case row.FixableCritical > 0 && row.Exposed:
		return db.VulnLevelDanger
	case row.FixableCritical+row.FixableHigh > 0:
		return db.VulnLevelWarn
	default:
		return db.VulnLevelNone
	}
}

/*
 * summarizeVulnReports is one pass over a cluster, separated from reading the
 * store and writing it so the whole rule can be asserted over a fake cluster.
 *
 * A report on a ReplicaSet is folded into its Deployment, found from the pods'
 * own pod-template-hash. A ReplicaSet with no pods is a revision the Deployment
 * has moved past: its image is not running, so its report is not counted —
 * "where are we running it" is the question.
 */
func summarizeVulnReports(read trafficReader, now time.Time) (db.VulnSummaryPass, error) {
	reports, truncated, err := readVulnReports(read)
	if err != nil {
		return db.VulnSummaryPass{}, err
	}
	exposed, deployments, notes, err := vulnExposure(read)
	if err != nil {
		return db.VulnSummaryPass{}, err
	}

	rows := map[vulnWorkloadKey]*db.VulnWorkloadSummary{}
	var order []vulnWorkloadKey
	retired := 0
	for _, report := range reports {
		labels := report.Metadata.Labels
		key := vulnWorkloadKey{
			namespace: cmpOr(labels[trivyLabelNamespace], report.Metadata.Namespace),
			kind:      labels[trivyLabelKind],
			name:      labels[trivyLabelName],
		}
		if key.kind == "" || key.name == "" {
			continue
		}
		if key.kind == "ReplicaSet" {
			deployment, ok := deployments[key]
			if !ok {
				retired++
				continue
			}
			key.kind, key.name = "Deployment", deployment
		}
		row, ok := rows[key]
		if !ok {
			row = &db.VulnWorkloadSummary{
				Namespace: key.namespace, Kind: key.kind, Name: key.name,
				Exposed: exposed[key], Images: []string{}, Reports: []string{},
			}
			rows[key] = row
			order = append(order, key)
		}
		summary := report.Report.Summary
		row.Critical += summary.CriticalCount
		row.High += summary.HighCount
		row.Medium += summary.MediumCount
		row.Low += summary.LowCount
		row.Unknown += summary.UnknownCount
		for _, finding := range report.Report.Vulnerabilities {
			if !fixable(finding) {
				continue
			}
			switch strings.ToUpper(finding.Severity) {
			case "CRITICAL":
				row.FixableCritical++
			case "HIGH":
				row.FixableHigh++
			}
		}
		if image := report.image(); image != "" && !containsString(row.Images, image) {
			row.Images = append(row.Images, image)
		}
		row.Reports = append(row.Reports, report.Metadata.Name)
		if at := report.Report.UpdateTimestamp; at != nil && (row.ScannedAt == nil || at.After(*row.ScannedAt)) {
			stamp := at.UTC()
			row.ScannedAt = &stamp
		}
	}

	pass := db.VulnSummaryPass{At: now, Truncated: truncated, Rows: make([]db.VulnWorkloadSummary, 0, len(order))}
	levels := map[string]int{}
	for _, key := range order {
		row := rows[key]
		row.Level = vulnLevel(*row)
		levels[row.Level]++
		pass.Rows = append(pass.Rows, *row)
	}

	message := fmt.Sprintf("Read %d reports covering %d workloads: %d need attention now, %d have a fix waiting.",
		len(reports), len(pass.Rows), levels[db.VulnLevelDanger], levels[db.VulnLevelWarn])
	if truncated {
		message += fmt.Sprintf(" Stopped at %d reports; the rest are not summarised.", maxVulnReports)
	}
	if retired > 0 {
		message += fmt.Sprintf(" %d reports on ReplicaSets with no running pods were left out.", retired)
	}
	if len(notes) > 0 {
		message += " Exposure is partial: " + strings.Join(notes, "; ") + "."
	}
	pass.Message = message
	return pass, nil
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

/* -------------------------------------------------------------- watcher --- */

// summarizeVulnCluster reads one cluster and writes what it found. A failed
// read records the failure and leaves the last good summary standing.
func (s *server) summarizeVulnCluster(ctx context.Context, cluster *db.Cluster) error {
	if s.tunnels == nil || !s.tunnels.Connected(cluster.ID) {
		err := errors.New("the agent is not connected, so the reports could not be read")
		_ = s.store.RecordVulnSummaryFailure(ctx, cluster.ID, err.Error())
		return err
	}
	pass, err := summarizeVulnReports(vulnServiceReader{s: s, ctx: ctx, cluster: cluster}, time.Now().UTC())
	if err != nil {
		_ = s.store.RecordVulnSummaryFailure(ctx, cluster.ID, "The last read failed: "+err.Error())
		return err
	}
	if err := s.store.ReplaceVulnSummaries(ctx, cluster.ID, pass); err != nil {
		return err
	}
	return nil
}

// startVulnWatcher summarises every connected cluster's reports for as long as
// the context lives, on whichever replica holds the lease.
func (s *server) startVulnWatcher(ctx context.Context) {
	ticker := time.NewTicker(vulnPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.vulnTick(ctx)
		}
	}
}

func (s *server) vulnTick(ctx context.Context) {
	held, err := s.store.AcquireLease(ctx, db.LeaseVulnWatcher, s.instanceID, vulnLeaseTTL)
	if err != nil {
		if ctx.Err() == nil {
			s.log().Warn("vulnerability watcher could not take its lease", slog.String("error", err.Error()))
		}
		return
	}
	if !held {
		return
	}
	rows, err := s.store.EnabledVulnScanning(ctx)
	if err != nil {
		s.log().Warn("vulnerability watcher could not list clusters", slog.String("error", err.Error()))
		return
	}
	for _, row := range rows {
		if ctx.Err() != nil {
			return
		}
		cluster, err := s.store.ClusterByID(ctx, row.ClusterID)
		if err != nil || connectionMode(*cluster) != db.ModeAgent {
			continue
		}
		if err := s.summarizeVulnCluster(ctx, cluster); err != nil && ctx.Err() == nil {
			s.log().Debug("vulnerability watcher could not summarise a cluster",
				slog.String("cluster", cluster.Name), slog.String("error", err.Error()))
		}
	}
}

/* ------------------------------------------------------------- handlers --- */

type vulnTotals struct {
	Workloads int `json:"workloads"`
	Danger    int `json:"danger"`
	Warn      int `json:"warn"`
	Exposed   int `json:"exposed"`
	Critical  int `json:"critical"`
	High      int `json:"high"`
	Fixable   int `json:"fixable"`
}

// showVulnFindings answers the cluster's summary. Not connected, or switched
// off, is said rather than shown as zero findings.
func (s *server) showVulnFindings(c *gin.Context) {
	cluster, ok := s.vulnFindingsCluster(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	row, err := s.store.ClusterVulnScanning(ctx, cluster.ID)
	switch {
	case errors.Is(err, db.ErrNotFound):
		c.JSON(http.StatusOK, gin.H{"connected": false, "enabled": false, "workloads": []any{}})
		return
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load the scanning settings"})
		return
	}
	out := gin.H{
		"connected":         true,
		"enabled":           row.Enabled,
		"provider":          row.Provider,
		"summarized_at":     row.SummarizedAt,
		"summary_message":   row.SummaryMessage,
		"summary_truncated": row.SummaryTruncated,
		"workloads":         []db.VulnWorkloadSummary{},
		"totals":            vulnTotals{},
	}
	if !row.Enabled {
		c.JSON(http.StatusOK, out)
		return
	}
	summaries, err := s.store.VulnSummaries(ctx, cluster.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load the findings"})
		return
	}
	var totals vulnTotals
	for _, summary := range summaries {
		totals.Workloads++
		totals.Critical += summary.Critical
		totals.High += summary.High
		totals.Fixable += summary.FixableCritical + summary.FixableHigh
		if summary.Exposed {
			totals.Exposed++
		}
		switch summary.Level {
		case db.VulnLevelDanger:
			totals.Danger++
		case db.VulnLevelWarn:
			totals.Warn++
		}
	}
	out["workloads"] = summaries
	out["totals"] = totals
	c.JSON(http.StatusOK, out)
}

type vulnFindingView struct {
	ID        string   `json:"id"`
	Severity  string   `json:"severity"`
	Package   string   `json:"package"`
	Installed string   `json:"installed"`
	Fixed     string   `json:"fixed,omitempty"`
	Title     string   `json:"title,omitempty"`
	Link      string   `json:"link,omitempty"`
	Score     *float64 `json:"score,omitempty"`
	Published string   `json:"published,omitempty"`
}

type vulnContainerView struct {
	Container string            `json:"container"`
	Image     string            `json:"image"`
	ScannedAt *time.Time        `json:"scanned_at,omitempty"`
	Critical  int               `json:"critical"`
	High      int               `json:"high"`
	Medium    int               `json:"medium"`
	Low       int               `json:"low"`
	Unknown   int               `json:"unknown"`
	Total     int               `json:"total"`
	Findings  []vulnFindingView `json:"findings"`
}

var vulnSeverityRank = map[string]int{"CRITICAL": 0, "HIGH": 1, "MEDIUM": 2, "LOW": 3}

func rankOf(severity string) int {
	if rank, ok := vulnSeverityRank[strings.ToUpper(severity)]; ok {
		return rank
	}
	return 4
}

// safeLink keeps a finding's advisory link only when it is a web address. It
// comes from a custom resource anyone with write on the namespace could forge.
func safeLink(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
		return ""
	}
	return parsed.String()
}

// vulnContainer orders a report's findings most urgent first — severity, then
// whether a fix exists, then score — and keeps the first maxDrawerFindings.
func vulnContainer(report trivyReport) vulnContainerView {
	findings := append([]trivyFinding(nil), report.Report.Vulnerabilities...)
	sort.SliceStable(findings, func(i, j int) bool {
		a, b := findings[i], findings[j]
		if rankOf(a.Severity) != rankOf(b.Severity) {
			return rankOf(a.Severity) < rankOf(b.Severity)
		}
		if fixable(a) != fixable(b) {
			return fixable(a)
		}
		return deref(a.Score, 0) > deref(b.Score, 0)
	})
	summary := report.Report.Summary
	out := vulnContainerView{
		Container: report.Metadata.Labels[trivyLabelContainer],
		Image:     report.image(),
		ScannedAt: report.Report.UpdateTimestamp,
		Critical:  summary.CriticalCount, High: summary.HighCount, Medium: summary.MediumCount,
		Low: summary.LowCount, Unknown: summary.UnknownCount,
		Total:    len(findings),
		Findings: []vulnFindingView{},
	}
	for i, finding := range findings {
		if i == maxDrawerFindings {
			break
		}
		out.Findings = append(out.Findings, vulnFindingView{
			ID: finding.VulnerabilityID, Severity: strings.ToUpper(finding.Severity),
			Package: finding.Resource, Installed: finding.InstalledVersion, Fixed: finding.FixedVersion,
			Title: finding.Title, Link: safeLink(finding.PrimaryLink), Score: finding.Score,
			Published: finding.PublishedDate,
		})
	}
	return out
}

// vulnWorkloadKinds maps the drawer's resource keys to the Kind a summary row
// carries.
var vulnWorkloadKinds = map[string]string{
	"deployments":  "Deployment",
	"statefulsets": "StatefulSet",
	"daemonsets":   "DaemonSet",
	"cronjobs":     "CronJob",
	"jobs":         "Job",
	"pods":         "Pod",
	"replicasets":  "ReplicaSet",
}

// showVulnWorkload reads one workload's reports live, by the names the last
// pass kept for it. A report gone since — a rollout replaced the ReplicaSet —
// is said, not guessed at: the next pass finds the new one.
func (s *server) showVulnWorkload(c *gin.Context) {
	cluster, ok := s.vulnFindingsCluster(c)
	if !ok {
		return
	}
	kind, known := vulnWorkloadKinds[strings.TrimSpace(c.Query("kind"))]
	namespace := strings.TrimSpace(c.Query("namespace"))
	name := strings.TrimSpace(c.Query("name"))
	if !known {
		c.JSON(http.StatusBadRequest, gin.H{"error": "scanner reports belong to workloads: kind must be one of " +
			"deployments, statefulsets, daemonsets, cronjobs, jobs, pods or replicasets"})
		return
	}
	if namespace == "" || name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "namespace and name are required"})
		return
	}
	if !alarmObjectName.MatchString(namespace) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid namespace"})
		return
	}
	ctx := c.Request.Context()
	row, err := s.store.ClusterVulnScanning(ctx, cluster.ID)
	if errors.Is(err, db.ErrNotFound) || (err == nil && !row.Enabled) {
		c.JSON(http.StatusOK, gin.H{"connected": err == nil, "enabled": false, "found": false})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load the scanning settings"})
		return
	}
	summary, err := s.store.VulnWorkloadSummaryFor(ctx, cluster.ID, namespace, kind, name)
	if errors.Is(err, db.ErrNotFound) {
		c.JSON(http.StatusOK, gin.H{"connected": true, "enabled": true, "found": false,
			"summarized_at": row.SummarizedAt})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load the findings"})
		return
	}

	read := vulnServiceReader{s: s, ctx: ctx, cluster: cluster}
	containers := []vulnContainerView{}
	missing := 0
	for i, report := range summary.Reports {
		if i == maxDrawerReports {
			break
		}
		if !alarmObjectName.MatchString(report) {
			continue
		}
		path := trivyAPI + "namespaces/" + namespace + "/vulnerabilityreports/" + report
		status, body, err := read.get(path)
		if err != nil {
			writeGitopsReadError(c, err)
			return
		}
		if status == http.StatusNotFound {
			missing++
			continue
		}
		if status < 200 || status >= 300 {
			c.JSON(http.StatusBadGateway, gin.H{"error": kubeErrorMessage(body, status)})
			return
		}
		var object trivyReport
		if err := json.Unmarshal(body, &object); err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "the cluster returned an unreadable report"})
			return
		}
		containers = append(containers, vulnContainer(object))
	}
	c.JSON(http.StatusOK, gin.H{
		"connected": true, "enabled": true, "found": true,
		"summary":       summary,
		"summarized_at": row.SummarizedAt,
		"containers":    containers,
		"missing":       missing,
	})
}

// listVulnClusterCounts answers, for the sidebar, how many workloads need a
// look on each cluster the caller is in the audience for — and nothing about
// any other cluster.
func (s *server) listVulnClusterCounts(c *gin.Context) {
	user, ok := s.currentUser(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	counts, err := s.store.VulnCountsByCluster(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load the counts"})
		return
	}
	out := []db.VulnClusterCounts{}
	if user.IsAdmin() {
		out = append(out, counts...)
		c.JSON(http.StatusOK, gin.H{"clusters": out})
		return
	}
	grants, err := s.store.AccessForUser(ctx, user.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load cluster access"})
		return
	}
	for _, count := range counts {
		grant, granted := grants[count.ClusterID]
		if granted && vulnVisible(user, grant) {
			out = append(out, count)
		}
	}
	c.JSON(http.StatusOK, gin.H{"clusters": out})
}
