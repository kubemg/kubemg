package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * Vulnerability alerts — connecting a cluster to the scanner already in it.
 *
 * KubeMG does not scan, and this file is where that stays true. Trivy Operator
 * runs in the cluster, pulls the images, matches them against its database and
 * writes what it found as `aquasecurity.github.io` custom resources. KubeMG
 * reads those. No registry credential, no CVE feed, nothing new in the agent.
 *
 * This file is PR 1: the administrator's half. It answers "is a scanner here,
 * is it running, is it scanning, and what has it written so far", stores the
 * decision to read it, and points at the Helm catalogue when nothing is there.
 * Showing findings is PR 2.
 *
 * Whose identity reads is the GitOps answer for the GitOps reason: reports live
 * in every namespace and the Kubernetes-component ones in none, so the reads go
 * as KubeMG's own service identity (`kubemg:vuln-reader`, the
 * `kubemg:cluster-admin` group — no agent re-apply) and every route here is
 * admin-only, the read included. Nothing here is narrowed because nothing here
 * is answered to anyone a narrowing would apply to.
 *
 * Detection is a read, never a tick: it runs when an administrator opens the
 * sheet, connects, or asks for a check — not when the cluster dashboard draws
 * the tile, which is opened far more often than the answer changes.
 */

// vulnReaderUser is the identity scanner reports are read as — its own name, so
// the trail can tell these reads from the GitOps reader's and the watchers'.
const vulnReaderUser = "kubemg:vuln-reader"

const trivyAPI = "/apis/aquasecurity.github.io/v1alpha1/"

// trivyReportKinds are the report kinds detection asks about, in the order the
// sheet lists them. Only the first is required to connect: it is the one that
// says anything about what is deployed.
var trivyReportKinds = []struct {
	plural, label, covers string
	required              bool
}{
	{"vulnerabilityreports", "Workload images", "workloads", true},
	{"clustervulnerabilityreports", "Kubernetes components", "kubernetes", false},
	// Found in Deckhouse's distribution of the operator, not confirmed upstream.
	// Asked rather than assumed: the sheet says whether this cluster serves it.
	{"nodevulnerabilityreports", "Node operating systems", "nodes", false},
}

// trivyOperatorSelector finds the operator's own Deployment. The chart labels it
// so; an install that renamed it is still connectable, it just reports no
// version.
const trivyOperatorSelector = "app.kubernetes.io/name=trivy-operator"

// trivyScannerEnv is the operator setting that turns image scanning off. Absent
// means on — the operator's default.
const trivyScannerEnv = "OPERATOR_VULNERABILITY_SCANNER_ENABLED"

// trivyRecommendation is what the sheet offers when no scanner is found: the
// chart, from the repository KubeMG seeds as `aqua`.
var trivyRecommendation = vulnRecommendation{
	Provider:       db.ScannerTrivyOperator,
	Name:           "Trivy Operator",
	License:        "Apache-2.0",
	RepositoryName: "aqua",
	RepositoryURL:  "https://aquasecurity.github.io/helm-charts/",
	Chart:          "trivy-operator",
	Release:        "trivy-operator",
	Namespace:      "trivy-system",
	DocsURL:        "https://aquasecurity.github.io/trivy-operator/latest/",
}

/* ------------------------------------------------------------ detection --- */

type vulnReportKind struct {
	Kind     string `json:"kind"`
	Label    string `json:"label"`
	Covers   string `json:"covers"`
	Required bool   `json:"required"`
	// Served is whether the cluster has the CRD at all.
	Served bool `json:"served"`
	// Count is how many reports exist, absent when the cluster would not say.
	Count       *int64 `json:"count,omitempty"`
	Approximate bool   `json:"approximate,omitempty"`
	// Reason is the cluster's own words when it refused the list.
	Reason string `json:"reason,omitempty"`
}

type trivyOperatorView struct {
	Namespace     string `json:"namespace"`
	Name          string `json:"name"`
	Version       string `json:"version,omitempty"`
	Replicas      int32  `json:"replicas"`
	ReadyReplicas int32  `json:"ready_replicas"`
	// ScannerDisabled is true only when the Deployment says so explicitly.
	ScannerDisabled bool `json:"scanner_disabled"`
}

type vulnDetection struct {
	Provider string `json:"provider"`
	// Installed is whether the workload report CRD is served — the one thing a
	// connection cannot do without.
	Installed bool               `json:"installed"`
	Reports   []vulnReportKind   `json:"reports"`
	Operator  *trivyOperatorView `json:"operator"`
	// OperatorReason says why the operator could not be looked for, when it
	// could not; an absent operator with no reason means none is labelled so.
	OperatorReason string `json:"operator_reason,omitempty"`

	// The verdict, in the datasource vocabulary so the UI reuses its tones.
	Status  string `json:"status"`
	Message string `json:"message"`
}

// detectTrivyOperator asks the cluster what Trivy Operator has left in it. A
// kind the cluster does not serve, or refuses to list, is an answer about that
// kind; only a transport failure fails the detection.
func detectTrivyOperator(read trafficReader) (vulnDetection, error) {
	out := vulnDetection{Provider: db.ScannerTrivyOperator, Reports: []vulnReportKind{}}

	for _, kind := range trivyReportKinds {
		report := vulnReportKind{Kind: kind.plural, Label: kind.label, Covers: kind.covers, Required: kind.required}
		status, body, err := read.get(pagedPath(trivyAPI+kind.plural, 1, ""))
		if err != nil {
			return vulnDetection{}, err
		}
		switch {
		case status == http.StatusNotFound:
			// Not served. Nothing more to say about it.
		case status < 200 || status >= 300:
			report.Served = true
			report.Reason = kubeErrorMessage(body, status)
		default:
			report.Served = true
			var page listPage[struct{}]
			if json.Unmarshal(body, &page) == nil {
				counted := countFromPage(page)
				report.Count, report.Approximate = counted.count, counted.approximate
			}
		}
		if kind.required {
			out.Installed = report.Served
		}
		out.Reports = append(out.Reports, report)
	}

	operator, reason, err := findTrivyOperator(read)
	if err != nil {
		return vulnDetection{}, err
	}
	out.Operator, out.OperatorReason = operator, reason
	out.Status, out.Message = vulnVerdict(out)
	return out, nil
}

// deploymentItem is the slice of a Deployment detection reads.
type deploymentItem struct {
	Metadata struct {
		Name      string            `json:"name"`
		Namespace string            `json:"namespace"`
		Labels    map[string]string `json:"labels"`
	} `json:"metadata"`
	Spec struct {
		Replicas *int32 `json:"replicas"`
		Template struct {
			Spec struct {
				Containers []struct {
					Name  string `json:"name"`
					Image string `json:"image"`
					Env   []struct {
						Name  string `json:"name"`
						Value string `json:"value"`
					} `json:"env"`
				} `json:"containers"`
			} `json:"spec"`
		} `json:"template"`
	} `json:"spec"`
	Status struct {
		ReadyReplicas int32 `json:"readyReplicas"`
	} `json:"status"`
}

func findTrivyOperator(read trafficReader) (*trivyOperatorView, string, error) {
	query := url.Values{}
	query.Set("labelSelector", trivyOperatorSelector)
	query.Set("limit", "5")
	status, body, err := read.get("/apis/apps/v1/deployments?" + query.Encode())
	if err != nil {
		return nil, "", err
	}
	if status < 200 || status >= 300 {
		return nil, kubeErrorMessage(body, status), nil
	}
	var list struct {
		Items []deploymentItem `json:"items"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, "the cluster returned an unreadable Deployment list", nil
	}
	if len(list.Items) == 0 {
		return nil, "", nil
	}

	item := list.Items[0]
	view := &trivyOperatorView{
		Namespace:     item.Metadata.Namespace,
		Name:          item.Metadata.Name,
		Version:       item.Metadata.Labels["app.kubernetes.io/version"],
		Replicas:      1,
		ReadyReplicas: item.Status.ReadyReplicas,
	}
	if item.Spec.Replicas != nil {
		view.Replicas = *item.Spec.Replicas
	}
	for _, container := range item.Spec.Template.Spec.Containers {
		if view.Version == "" {
			view.Version = imageTag(container.Image)
		}
		for _, env := range container.Env {
			if env.Name == trivyScannerEnv && strings.EqualFold(strings.TrimSpace(env.Value), "false") {
				view.ScannerDisabled = true
			}
		}
	}
	return view, "", nil
}

// imageTag is the tag of an image reference, or "" when it has none (a digest
// alone says nothing a reader recognises).
func imageTag(image string) string {
	if at := strings.Index(image, "@"); at >= 0 {
		image = image[:at]
	}
	slash := strings.LastIndex(image, "/")
	if colon := strings.LastIndex(image, ":"); colon > slash {
		return image[colon+1:]
	}
	return ""
}

// vulnVerdict turns a detection into the one line a tile carries.
func vulnVerdict(d vulnDetection) (string, string) {
	if !d.Installed {
		return db.StatusUnhealthy, "Trivy Operator is not installed in this cluster - its VulnerabilityReport resource is not served."
	}
	workloads := d.Reports[0]
	if workloads.Reason != "" {
		return db.StatusUnhealthy, "The cluster refused to list VulnerabilityReports: " + workloads.Reason
	}
	if d.Operator != nil {
		if d.Operator.ScannerDisabled {
			return db.StatusUnhealthy, fmt.Sprintf("Image scanning is turned off in the operator (%s=false on %s/%s).",
				trivyScannerEnv, d.Operator.Namespace, d.Operator.Name)
		}
		if d.Operator.ReadyReplicas == 0 {
			return db.StatusUnhealthy, fmt.Sprintf("The operator %s/%s is not running, so its reports are not being refreshed.",
				d.Operator.Namespace, d.Operator.Name)
		}
	}

	count := "No workload reports yet - the first scan writes them as images are pulled."
	if workloads.Count != nil && *workloads.Count > 0 {
		noun := "workload reports"
		if *workloads.Count == 1 {
			noun = "workload report"
		}
		prefix := ""
		if workloads.Approximate {
			prefix = "about "
		}
		count = fmt.Sprintf("%s%d %s.", prefix, *workloads.Count, noun)
	} else if workloads.Count == nil {
		count = "Workload reports are served."
	}

	switch {
	case d.Operator == nil && d.OperatorReason != "":
		return db.StatusHealthy, count + " The operator's Deployment could not be read: " + d.OperatorReason
	case d.Operator == nil:
		return db.StatusHealthy, count + " No Deployment labelled " + trivyOperatorSelector +
			" was found, so whether the scanner is running cannot be said here."
	case d.Operator.Version != "":
		return db.StatusHealthy, "Trivy Operator " + d.Operator.Version + " is running. " + count
	default:
		return db.StatusHealthy, "Trivy Operator is running. " + count
	}
}

// vulnServiceReader reads as kubemg:vuln-reader. Like gitopsServiceReader it
// hands a gateway refusal back as an error: a missing tunnel or a spent budget
// fails the detection, not one report kind.
type vulnServiceReader struct {
	s       *server
	ctx     context.Context
	cluster *db.Cluster
}

func (r vulnServiceReader) get(path string) (int, []byte, error) {
	user := &db.User{Username: vulnReaderUser}
	grant := db.UserClusterAccess{ClusterID: r.cluster.ID, K8sRole: db.K8sRoleClusterAdmin}
	resp, err := r.s.proxy.Call(r.ctx, user, r.cluster, grant, http.MethodGet, path, nil, nil)
	if err != nil {
		return 0, nil, err
	}
	return resp.Status, resp.Body, nil
}

/* ------------------------------------------------------------- handlers --- */

type vulnRecommendation struct {
	Provider       string `json:"provider"`
	Name           string `json:"name"`
	License        string `json:"license"`
	RepositoryName string `json:"repository_name"`
	RepositoryURL  string `json:"repository_url"`
	Chart          string `json:"chart"`
	Release        string `json:"release"`
	Namespace      string `json:"namespace"`
	DocsURL        string `json:"docs_url"`
	// Catalogue is the declared repository serving that URL, when there is one —
	// which may not be named `aqua` if an administrator declared it themselves.
	Catalogue *vulnCatalogue `json:"catalogue"`
}

type vulnCatalogue struct {
	Repository string `json:"repository"`
	Status     string `json:"status"`
	// ChartVersion is the newest version the last sync recorded; empty when the
	// repository has not synced or does not carry the chart.
	ChartVersion string `json:"chart_version,omitempty"`
}

// vulnScanningCluster loads the cluster for a scanning route: an administrator's
// call (the router says so), on an agent-mode cluster.
func (s *server) vulnScanningCluster(c *gin.Context) (*db.Cluster, bool) {
	_, cluster, _, _, ok := s.loadAuthorizedCluster(c)
	if !ok {
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

// showVulnScanning answers what is stored and what would be installed. It never
// reads the cluster: the tile it feeds is drawn on every dashboard visit.
func (s *server) showVulnScanning(c *gin.Context) {
	cluster, ok := s.vulnScanningCluster(c)
	if !ok {
		return
	}
	row, err := s.store.ClusterVulnScanning(c.Request.Context(), cluster.ID)
	switch {
	case errors.Is(err, db.ErrNotFound):
		row = nil
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load the scanning settings"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"scanning":       row,
		"recommendation": s.vulnRecommendationFor(c.Request.Context()),
	})
}

// vulnRecommendationFor fills in whether the catalogue can install the chart.
// A store error reads as "not in the catalogue": the sheet then offers to add
// it, and adding an existing name is an edit, not a duplicate.
func (s *server) vulnRecommendationFor(ctx context.Context) vulnRecommendation {
	out := trivyRecommendation
	repositories, err := s.store.HelmRepositories(ctx)
	if err != nil {
		return out
	}
	want := normalizeRepoURL(out.RepositoryURL)
	for _, repository := range repositories {
		if normalizeRepoURL(repository.URL) != want {
			continue
		}
		catalogue := &vulnCatalogue{Repository: repository.Name, Status: repository.Status}
		if chart, err := s.store.HelmChart(ctx, repository.ID, out.Chart); err == nil {
			catalogue.ChartVersion = chart.Chart().LatestVersion()
		}
		out.Catalogue = catalogue
		break
	}
	return out
}

func normalizeRepoURL(raw string) string {
	return strings.ToLower(strings.TrimRight(strings.TrimSpace(raw), "/"))
}

// detectVulnScanning reads the cluster now, writing nothing.
func (s *server) detectVulnScanning(c *gin.Context) {
	cluster, ok := s.vulnScanningCluster(c)
	if !ok {
		return
	}
	detection, err := detectTrivyOperator(vulnServiceReader{s: s, ctx: c.Request.Context(), cluster: cluster})
	if err != nil {
		writeGitopsReadError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"detection": detection})
}

type vulnScanningRequest struct {
	Provider string `json:"provider"`
	// Enabled is a pointer so a connect that says nothing turns alerts on.
	Enabled *bool `json:"enabled"`
}

// putVulnScanning connects a cluster, or flips its switch. It detects first and
// refuses a scanner that is not there: a connection to nothing would show an
// administrator a tile that says "on" over a cluster nobody is scanning.
func (s *server) putVulnScanning(c *gin.Context) {
	cluster, ok := s.vulnScanningCluster(c)
	if !ok {
		return
	}
	var request vulnScanningRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "the request could not be read"})
		return
	}
	provider := strings.TrimSpace(request.Provider)
	if provider == "" {
		provider = db.ScannerTrivyOperator
	}
	if !db.ValidScanner(provider) {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("%q is not a scanner KubeMG reads; supported: %s", provider, db.ScannerTrivyOperator),
		})
		return
	}

	detection, err := detectTrivyOperator(vulnServiceReader{s: s, ctx: c.Request.Context(), cluster: cluster})
	if err != nil {
		writeGitopsReadError(c, err)
		return
	}
	if !detection.Installed {
		c.JSON(http.StatusConflict, gin.H{"error": detection.Message, "detection": detection})
		return
	}

	enabled := true
	if request.Enabled != nil {
		enabled = *request.Enabled
	}
	now := time.Now().UTC()
	row := &db.ClusterVulnScanning{
		ClusterID:       cluster.ID,
		Provider:        provider,
		Enabled:         enabled,
		LastCheckedAt:   &now,
		LastStatus:      detection.Status,
		LastMessage:     detection.Message,
		DetectedVersion: operatorVersion(detection),
	}
	if err := s.store.PutClusterVulnScanning(c.Request.Context(), row); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not save the scanning settings"})
		return
	}
	s.invalidateClusterReads(cluster.ID)
	c.JSON(http.StatusOK, gin.H{"scanning": row, "detection": detection})
}

// checkVulnScanning re-detects for a connected cluster and records the verdict.
func (s *server) checkVulnScanning(c *gin.Context) {
	cluster, ok := s.vulnScanningCluster(c)
	if !ok {
		return
	}
	if _, err := s.store.ClusterVulnScanning(c.Request.Context(), cluster.ID); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "vulnerability alerts are not connected on this cluster"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load the scanning settings"})
		return
	}
	detection, err := detectTrivyOperator(vulnServiceReader{s: s, ctx: c.Request.Context(), cluster: cluster})
	if err != nil {
		writeGitopsReadError(c, err)
		return
	}
	health := db.SourceHealth{
		Status:          detection.Status,
		Message:         detection.Message,
		DetectedVersion: operatorVersion(detection),
		CheckedAt:       time.Now().UTC(),
	}
	if err := s.store.UpdateClusterVulnScanningHealth(c.Request.Context(), cluster.ID, health); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not record the check"})
		return
	}
	row, err := s.store.ClusterVulnScanning(c.Request.Context(), cluster.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load the scanning settings"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"scanning": row, "detection": detection})
}

// deleteVulnScanning disconnects a cluster. The scanner stays installed: it was
// the cluster's before it was KubeMG's, and uninstalling it is a Helm release
// action an administrator takes on purpose.
func (s *server) deleteVulnScanning(c *gin.Context) {
	cluster, ok := s.vulnScanningCluster(c)
	if !ok {
		return
	}
	err := s.store.DeleteClusterVulnScanning(c.Request.Context(), cluster.ID)
	if errors.Is(err, db.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "vulnerability alerts are not connected on this cluster"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not disconnect"})
		return
	}
	s.invalidateClusterReads(cluster.ID)
	c.Status(http.StatusNoContent)
}

func operatorVersion(d vulnDetection) string {
	if d.Operator == nil {
		return ""
	}
	return d.Operator.Version
}
