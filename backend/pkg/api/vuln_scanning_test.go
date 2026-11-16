package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * Vulnerability alerts, PR 1: connecting a cluster to the scanner in it. What is
 * pinned is what would be quietly wrong — a scanner that is installed but not
 * scanning reading as healthy, a kind the cluster does not serve reading as a
 * failure, a refused list losing the cluster's own words, and any of these
 * routes answering someone other than an administrator.
 */

const (
	vulnWorkloads = trivyAPI + "vulnerabilityreports?limit=1"
	vulnCluster   = trivyAPI + "clustervulnerabilityreports?limit=1"
	vulnNodes     = trivyAPI + "nodevulnerabilityreports?limit=1"
)

func trivyDeploymentsPath() string {
	query := url.Values{}
	query.Set("labelSelector", trivyOperatorSelector)
	query.Set("limit", "5")
	return "/apis/apps/v1/deployments?" + query.Encode()
}

func trivyDeployment(ready int32, env ...map[string]any) map[string]any {
	if env == nil {
		env = []map[string]any{}
	}
	return map[string]any{
		"metadata": map[string]any{
			"name": "trivy-operator", "namespace": "trivy-system",
			"labels": map[string]any{"app.kubernetes.io/name": "trivy-operator", "app.kubernetes.io/version": "0.29.0"},
		},
		"spec": map[string]any{
			"replicas": 1,
			"template": map[string]any{"spec": map[string]any{"containers": []any{
				map[string]any{"name": "trivy-operator", "image": "mirror.gcr.io/aquasec/trivy-operator:0.29.0", "env": env},
			}}},
		},
		"status": map[string]any{"readyReplicas": ready},
	}
}

func reportPage(items int, remaining *int64) map[string]any {
	list := make([]any, items)
	for i := range list {
		list[i] = map[string]any{}
	}
	metadata := map[string]any{}
	if remaining != nil {
		metadata["continue"] = "next"
		metadata["remainingItemCount"] = *remaining
	}
	return map[string]any{"items": list, "metadata": metadata}
}

func detect(t *testing.T, cluster *fakeCluster) vulnDetection {
	t.Helper()
	detection, err := detectTrivyOperator(cluster)
	if err != nil {
		t.Fatalf("detection failed: %v", err)
	}
	return detection
}

func TestAClusterWithoutTrivyIsAnAnswerNotAFailure(t *testing.T) {
	detection := detect(t, newFakeCluster())

	if detection.Installed || detection.Status != db.StatusUnhealthy {
		t.Fatalf("expected not installed / unhealthy, got %+v", detection)
	}
	if !strings.Contains(detection.Message, "not installed") {
		t.Fatalf("message should say it is not installed: %q", detection.Message)
	}
	for _, report := range detection.Reports {
		if report.Served {
			t.Fatalf("%s reported served on an empty cluster", report.Kind)
		}
	}
}

func TestARunningOperatorIsHealthyWithItsVersionAndCount(t *testing.T) {
	cluster := newFakeCluster()
	remaining := int64(41)
	cluster.object(vulnWorkloads, reportPage(1, &remaining))
	cluster.object(vulnCluster, reportPage(0, nil))
	cluster.object(trivyDeploymentsPath(), map[string]any{"items": []any{trivyDeployment(1)}})

	detection := detect(t, cluster)
	if !detection.Installed || detection.Status != db.StatusHealthy {
		t.Fatalf("expected installed / healthy, got %+v", detection)
	}
	if detection.Operator == nil || detection.Operator.Version != "0.29.0" || detection.Operator.Namespace != "trivy-system" {
		t.Fatalf("operator not read: %+v", detection.Operator)
	}
	if !strings.Contains(detection.Message, "about 42 workload reports") {
		t.Fatalf("count missing from message: %q", detection.Message)
	}
	// Which kinds this cluster serves is said, not assumed: the node kind is
	// absent here, the Kubernetes-component kind present with none written.
	byKind := map[string]vulnReportKind{}
	for _, report := range detection.Reports {
		byKind[report.Kind] = report
	}
	if byKind["nodevulnerabilityreports"].Served {
		t.Fatal("node reports should read as not served")
	}
	if k := byKind["clustervulnerabilityreports"]; !k.Served || k.Count == nil || *k.Count != 0 {
		t.Fatalf("cluster reports should be served with zero: %+v", k)
	}
}

func TestAnOperatorThatIsNotRunningIsNotHealthy(t *testing.T) {
	// The CRDs outlive the operator; reports that are there but no longer
	// refreshed are the case a green tile would hide.
	cluster := newFakeCluster()
	cluster.object(vulnWorkloads, reportPage(3, nil))
	cluster.object(trivyDeploymentsPath(), map[string]any{"items": []any{trivyDeployment(0)}})

	detection := detect(t, cluster)
	if detection.Status != db.StatusUnhealthy || !strings.Contains(detection.Message, "not running") {
		t.Fatalf("expected an unhealthy not-running verdict, got %s: %q", detection.Status, detection.Message)
	}
}

func TestScanningTurnedOffInTheOperatorIsNamed(t *testing.T) {
	cluster := newFakeCluster()
	cluster.object(vulnWorkloads, reportPage(0, nil))
	cluster.object(trivyDeploymentsPath(), map[string]any{"items": []any{
		trivyDeployment(1, map[string]any{"name": trivyScannerEnv, "value": "False"}),
	}})

	detection := detect(t, cluster)
	if detection.Status != db.StatusUnhealthy || !strings.Contains(detection.Message, trivyScannerEnv) {
		t.Fatalf("expected the disabled scanner named, got %s: %q", detection.Status, detection.Message)
	}
}

func TestARefusedReportListKeepsTheClustersWords(t *testing.T) {
	cluster := newFakeCluster()
	cluster.refuse(vulnWorkloads, http.StatusForbidden)

	detection := detect(t, cluster)
	if !detection.Installed || detection.Status != db.StatusUnhealthy {
		t.Fatalf("a refused list is served but unhealthy, got %+v", detection)
	}
	if !strings.Contains(detection.Message, "is forbidden") {
		t.Fatalf("the cluster's words are lost: %q", detection.Message)
	}
}

func TestNoLabelledOperatorStillConnectsAndSaysWhatIsUnknown(t *testing.T) {
	cluster := newFakeCluster()
	cluster.object(vulnWorkloads, reportPage(2, nil))
	cluster.object(trivyDeploymentsPath(), map[string]any{"items": []any{}})

	detection := detect(t, cluster)
	if detection.Status != db.StatusHealthy || detection.Operator != nil {
		t.Fatalf("expected healthy without an operator, got %+v", detection)
	}
	if !strings.Contains(detection.Message, "cannot be said") {
		t.Fatalf("message should say what cannot be known: %q", detection.Message)
	}
}

func TestImageTag(t *testing.T) {
	for image, want := range map[string]string{
		"aquasec/trivy-operator:0.29.0":                   "0.29.0",
		"registry.local:5000/aquasec/trivy-operator":      "",
		"registry.local:5000/aquasec/trivy-operator:1.2":  "1.2",
		"aquasec/trivy-operator:0.29.0@sha256:abcdef0123": "0.29.0",
		"aquasec/trivy-operator@sha256:abcdef0123":        "",
	} {
		if got := imageTag(image); got != want {
			t.Errorf("%s: got %q, want %q", image, got, want)
		}
	}
}

/* ------------------------------------------------------------- handlers --- */

func vulnPath(cluster *db.Cluster, suffix string) string {
	return "/api/v1/clusters/" + itoa(cluster.ID) + "/vulnerability-scanning" + suffix
}

func TestVulnScanningRoutesAreAdminOnlyIncludingTheRead(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("dev", "secret123", "user")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	env.store.grant(user.ID, cluster.ID, "cluster-admin", nil)
	token := env.tokenFor(t, user)

	for _, call := range []struct{ method, suffix string }{
		{http.MethodGet, ""}, {http.MethodPut, ""}, {http.MethodDelete, ""},
		{http.MethodGet, "/detect"}, {http.MethodPost, "/check"},
	} {
		rec := env.do(t, call.method, vulnPath(cluster, call.suffix), token, map[string]any{})
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s %s: expected 403, got %d (%s)", call.method, call.suffix, rec.Code, rec.Body.String())
		}
	}
}

func TestADirectModeClusterHasNoReportsToRead(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("root", "secret123", "admin")
	cluster := env.store.addCluster("direct", "prod")

	rec := env.do(t, http.MethodPut, vulnPath(cluster, ""), env.tokenFor(t, admin), map[string]any{})
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestAConnectWithNoAgentStoresNothing(t *testing.T) {
	// Detection runs before the write; a tunnel that is not there fails it, and
	// nothing is recorded as connected.
	env := newTestEnv(t)
	admin := env.store.addUser("root", "secret123", "admin")
	cluster := env.store.addAgentCluster("edge", "prod", "agent-token")

	rec := env.do(t, http.MethodPut, vulnPath(cluster, ""), env.tokenFor(t, admin),
		map[string]any{"provider": "trivy-operator"})
	if rec.Code < 400 {
		t.Fatalf("expected a failure without an agent, got %d (%s)", rec.Code, rec.Body.String())
	}
	if _, ok := env.store.vulnScanning[cluster.ID]; ok {
		t.Fatal("a connection was stored without a detection")
	}
}

func TestAnUnknownScannerIsRefusedByName(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("root", "secret123", "admin")
	cluster := env.store.addAgentCluster("edge", "prod", "agent-token")

	rec := env.do(t, http.MethodPut, vulnPath(cluster, ""), env.tokenFor(t, admin),
		map[string]any{"provider": "grype"})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "grype") {
		t.Fatalf("expected a 400 naming the scanner, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestTheReadNeverTouchesTheClusterAndFindsTheCatalogue(t *testing.T) {
	// The tile is drawn on every dashboard visit, so the read is stored state
	// only — with no agent at all it still answers. The catalogue match is by
	// URL, not by the seeded name.
	env := newTestEnv(t)
	admin := env.store.addUser("root", "secret123", "admin")
	cluster := env.store.addAgentCluster("edge", "prod", "agent-token")
	env.store.helmRepos["security"] = &db.HelmRepository{
		ID: 7, Name: "security", URL: "https://AquaSecurity.github.io/helm-charts", Status: "ok",
	}
	env.store.helmCharts[7] = []db.HelmChart{
		{RepositoryID: 7, Name: "trivy-operator", Versions: `[{"version":"0.29.0"},{"version":"0.28.1"}]`},
	}

	rec := env.do(t, http.MethodGet, vulnPath(cluster, ""), env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Scanning       *db.ClusterVulnScanning `json:"scanning"`
		Recommendation vulnRecommendation      `json:"recommendation"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Scanning != nil {
		t.Fatalf("nothing is connected yet: %+v", body.Scanning)
	}
	catalogue := body.Recommendation.Catalogue
	if catalogue == nil || catalogue.Repository != "security" || catalogue.ChartVersion != "0.29.0" {
		t.Fatalf("catalogue not matched by URL: %+v", catalogue)
	}
}

func TestCheckAndDisconnectAnswerForAClusterNeverConnected(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("root", "secret123", "admin")
	cluster := env.store.addAgentCluster("edge", "prod", "agent-token")
	token := env.tokenFor(t, admin)

	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		suffix := ""
		if method == http.MethodPost {
			suffix = "/check"
		}
		rec := env.do(t, method, vulnPath(cluster, suffix), token, nil)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: expected 404, got %d (%s)", method, rec.Code, rec.Body.String())
		}
	}
}
