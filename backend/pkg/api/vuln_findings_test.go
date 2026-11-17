package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * Vulnerability alerts, PR 2: what is shown, and to whom. Pinned here is what
 * would be quietly wrong — a finding reaching someone outside the audience, a
 * retired ReplicaSet's image counted as running, red spent on something that is
 * neither fixable nor reachable, and a partial read passed off as a whole one.
 */

const vulnReportsPage = trivyAPI + "vulnerabilityreports?limit=20"

func vulnReport(namespace, kind, name, container string, critical, high int, findings ...map[string]any) map[string]any {
	if findings == nil {
		findings = []map[string]any{}
	}
	return map[string]any{
		"metadata": map[string]any{
			"name":      strings.ToLower(kind) + "-" + name + "-" + container,
			"namespace": namespace,
			"labels": map[string]any{
				trivyLabelKind: kind, trivyLabelName: name, trivyLabelNamespace: namespace,
				trivyLabelContainer: container,
			},
		},
		"report": map[string]any{
			"updateTimestamp": "2026-11-15T10:00:00Z",
			"registry":        map[string]any{"server": "index.docker.io"},
			"artifact":        map[string]any{"repository": "library/" + container, "tag": "1.0"},
			"summary":         map[string]any{"criticalCount": critical, "highCount": high, "mediumCount": 1},
			"vulnerabilities": findings,
		},
	}
}

func vulnFinding(id, severity, fixed string) map[string]any {
	return map[string]any{"vulnerabilityID": id, "severity": severity, "fixedVersion": fixed,
		"resource": "openssl", "installedVersion": "3.0.1", "primaryLink": "https://avd.aquasec.com/nvd/" + id}
}

func ownedPod(namespace, name string, labels map[string]any, ownerKind, ownerName string) map[string]any {
	metadata := map[string]any{"name": name, "namespace": namespace, "labels": labels}
	if ownerKind != "" {
		metadata["ownerReferences"] = []any{map[string]any{"kind": ownerKind, "name": ownerName, "controller": true}}
	}
	return map[string]any{"metadata": metadata}
}

func vulnService(namespace, name, kind string, selector map[string]any) map[string]any {
	return map[string]any{"metadata": meta(namespace, name), "spec": map[string]any{"type": kind, "selector": selector}}
}

// vulnFixture is a cluster with:
//   - web: a Deployment behind an Ingress, a fixable critical (danger);
//   - api: a Deployment behind a NodePort Service, unfixable critical only (none);
//   - db:  a StatefulSet nobody outside reaches, a fixable critical (warn);
//   - an old ReplicaSet of web with no pods, whose report must not count.
func vulnFixture() *fakeCluster {
	f := newFakeCluster()
	f.object(vulnReportsPage, map[string]any{"items": []any{
		vulnReport("shop", "ReplicaSet", "web-7d9f8", "nginx", 1, 2, vulnFinding("CVE-1", "CRITICAL", "1.2"), vulnFinding("CVE-2", "HIGH", "")),
		vulnReport("shop", "ReplicaSet", "web-7d9f8", "sidecar", 0, 1, vulnFinding("CVE-3", "HIGH", "2.0")),
		vulnReport("shop", "ReplicaSet", "web-5c4b3", "nginx", 9, 9, vulnFinding("CVE-9", "CRITICAL", "1.0")),
		vulnReport("shop", "ReplicaSet", "api-6f5e4", "api", 1, 0, vulnFinding("CVE-4", "CRITICAL", "")),
		vulnReport("data", "StatefulSet", "db", "postgres", 1, 0, vulnFinding("CVE-5", "CRITICAL", "16.2")),
	}})
	f.list("/api/v1/pods", "",
		ownedPod("shop", "web-7d9f8-aaaaa", map[string]any{"app": "web", "pod-template-hash": "7d9f8"}, "ReplicaSet", "web-7d9f8"),
		ownedPod("shop", "api-6f5e4-bbbbb", map[string]any{"app": "api", "pod-template-hash": "6f5e4"}, "ReplicaSet", "api-6f5e4"),
		ownedPod("data", "db-0", map[string]any{"app": "db"}, "StatefulSet", "db"),
	)
	f.list("/api/v1/services", "",
		vulnService("shop", "web", "ClusterIP", map[string]any{"app": "web"}),
		vulnService("shop", "api", "NodePort", map[string]any{"app": "api"}),
		vulnService("data", "db", "ClusterIP", map[string]any{"app": "db"}),
	)
	f.list("/apis/networking.k8s.io/v1/ingresses", "", map[string]any{
		"metadata": meta("shop", "front"),
		"spec": map[string]any{"rules": []any{map[string]any{"http": map[string]any{"paths": []any{
			map[string]any{"path": "/", "backend": map[string]any{"service": map[string]any{"name": "web", "port": map[string]any{"number": 80}}}},
		}}}}},
	})
	return f
}

func vulnRowFor(t *testing.T, pass db.VulnSummaryPass, kind, name string) db.VulnWorkloadSummary {
	t.Helper()
	for _, row := range pass.Rows {
		if row.Kind == kind && row.Name == name {
			return row
		}
	}
	t.Fatalf("no row for %s/%s in %+v", kind, name, pass.Rows)
	return db.VulnWorkloadSummary{}
}

func TestReplicaSetReportsFoldIntoTheirDeploymentAndRetiredOnesAreLeftOut(t *testing.T) {
	pass, err := summarizeVulnReports(vulnFixture(), time.Now(), vulnIntel{})
	if err != nil {
		t.Fatal(err)
	}
	if len(pass.Rows) != 3 {
		t.Fatalf("expected web, api and db, got %+v", pass.Rows)
	}
	web := vulnRowFor(t, pass, "Deployment", "web")
	if web.Critical != 1 || web.High != 3 || web.FixableCritical != 1 || web.FixableHigh != 1 {
		t.Fatalf("both containers' counts, and only the live revision's: %+v", web)
	}
	if len(web.Reports) != 2 || len(web.Images) != 2 || web.Images[0] != "library/nginx:1.0" {
		t.Fatalf("reports and images kept per container: %+v", web)
	}
	if !strings.Contains(pass.Message, "1 reports on ReplicaSets with no running pods were left out") {
		t.Fatalf("the retired revision is said: %s", pass.Message)
	}
}

func TestRedIsOnlyAFixableCriticalSomethingOutsideCanReach(t *testing.T) {
	pass, err := summarizeVulnReports(vulnFixture(), time.Now(), vulnIntel{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct {
		kind, name, level string
		exposed           bool
	}{
		{"Deployment", "web", db.VulnLevelDanger, true}, // Ingress → Service → pods
		{"Deployment", "api", db.VulnLevelNone, true},   // reachable, but no fix exists
		{"StatefulSet", "db", db.VulnLevelWarn, false},  // fixable, not reachable
	} {
		row := vulnRowFor(t, pass, want.kind, want.name)
		if row.Level != want.level || row.Exposed != want.exposed {
			t.Fatalf("%s/%s: expected %s exposed=%v, got %s exposed=%v", want.kind, want.name,
				want.level, want.exposed, row.Level, row.Exposed)
		}
	}
}

func TestAnHTTPRouteBackendIsExposureToo(t *testing.T) {
	f := vulnFixture()
	f.list("/apis/networking.k8s.io/v1/ingresses", "")
	f.list("/apis/gateway.networking.k8s.io/v1/httproutes", "", map[string]any{
		"metadata": meta("shop", "front"),
		"spec": map[string]any{"rules": []any{map[string]any{"backendRefs": []any{
			map[string]any{"name": "web", "port": 80},
		}}}},
	})
	pass, err := summarizeVulnReports(f, time.Now(), vulnIntel{})
	if err != nil {
		t.Fatal(err)
	}
	if row := vulnRowFor(t, pass, "Deployment", "web"); !row.Exposed || row.Level != db.VulnLevelDanger {
		t.Fatalf("an HTTPRoute's Service is reachable: %+v", row)
	}
}

func TestARefusedExposureReadIsPartialNotAFailure(t *testing.T) {
	f := vulnFixture()
	f.refuse("/api/v1/services?limit=250", http.StatusForbidden)
	pass, err := summarizeVulnReports(f, time.Now(), vulnIntel{})
	if err != nil {
		t.Fatalf("exposure qualifies the findings, it is not them: %v", err)
	}
	if !strings.Contains(pass.Message, "Exposure is partial") || !strings.Contains(pass.Message, "forbidden") {
		t.Fatalf("the partial read is said in the cluster's words: %s", pass.Message)
	}
	if row := vulnRowFor(t, pass, "Deployment", "web"); row.Exposed || row.Level != db.VulnLevelWarn {
		t.Fatalf("unknown exposure is not exposure: %+v", row)
	}
}

func TestARefusedReportListFailsThePass(t *testing.T) {
	f := vulnFixture()
	f.refuse(vulnReportsPage, http.StatusForbidden)
	if _, err := summarizeVulnReports(f, time.Now(), vulnIntel{}); err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("expected the cluster's refusal, got %v", err)
	}
}

func TestReportsArePagedSmall(t *testing.T) {
	f := vulnFixture()
	f.object(vulnReportsPage, map[string]any{
		"metadata": map[string]any{"continue": "next"},
		"items":    []any{vulnReport("data", "StatefulSet", "db", "postgres", 1, 0)},
	})
	f.object(trivyAPI+"vulnerabilityreports?continue=next&limit=20", map[string]any{
		"items": []any{vulnReport("data", "StatefulSet", "cache", "redis", 0, 1, vulnFinding("CVE-7", "HIGH", "7.2"))},
	})
	pass, err := summarizeVulnReports(f, time.Now(), vulnIntel{})
	if err != nil {
		t.Fatal(err)
	}
	if len(pass.Rows) != 2 || vulnRowFor(t, pass, "StatefulSet", "cache").Level != db.VulnLevelWarn {
		t.Fatalf("the second page is read: %+v", pass.Rows)
	}
}

func TestTheDrawerPutsTheUrgentFirstAndDropsNonWebLinks(t *testing.T) {
	var report trivyReport
	raw, _ := json.Marshal(vulnReport("shop", "Deployment", "web", "nginx", 1, 1,
		vulnFinding("CVE-LOW", "LOW", "1"),
		vulnFinding("CVE-HIGH-NOFIX", "HIGH", ""),
		vulnFinding("CVE-HIGH-FIX", "HIGH", "2"),
		map[string]any{"vulnerabilityID": "CVE-CRIT", "severity": "CRITICAL", "primaryLink": "javascript:alert(1)"},
	))
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	view := vulnContainer(report, vulnDrawerIntel{})
	var order []string
	for _, f := range view.Findings {
		order = append(order, f.ID)
	}
	if strings.Join(order, ",") != "CVE-CRIT,CVE-HIGH-FIX,CVE-HIGH-NOFIX,CVE-LOW" {
		t.Fatalf("severity, then fixable first: %v", order)
	}
	if view.Findings[0].Link != "" || view.Findings[1].Link == "" {
		t.Fatalf("only web links survive: %+v", view.Findings[:2])
	}
	if view.Container != "nginx" || view.Total != 4 {
		t.Fatalf("container and total: %+v", view)
	}
}

func findingsPath(cluster *db.Cluster, suffix string) string {
	return "/api/v1/clusters/" + itoa(cluster.ID) + "/vulnerabilities" + suffix
}

func TestFindingsReachOnlyAdminsAndUnscopedClusterAdmins(t *testing.T) {
	env := newTestEnv(t)
	cluster := env.store.addAgentCluster("edge", "prod", "agent-token")
	env.store.vulnScanning[cluster.ID] = db.ClusterVulnScanning{ClusterID: cluster.ID, Provider: db.ScannerTrivyOperator, Enabled: true}
	env.store.vulnSummaries[cluster.ID] = []db.VulnWorkloadSummary{
		{Namespace: "shop", Kind: "Deployment", Name: "web", Critical: 1, FixableCritical: 1, Exposed: true, Level: db.VulnLevelDanger},
	}

	admin := env.store.addUser("root", "secret123", "admin")
	owner := env.store.addUser("owner", "secret123", "user")
	env.store.grant(owner.ID, cluster.ID, "cluster-admin", nil)
	scoped := env.store.addUser("scoped", "secret123", "user")
	env.store.grant(scoped.ID, cluster.ID, "cluster-admin", []string{"shop"})
	viewer := env.store.addUser("viewer", "secret123", "user")
	env.store.grant(viewer.ID, cluster.ID, "view", nil)

	for _, who := range []struct {
		user    *db.User
		visible bool
	}{{admin, true}, {owner, true}, {scoped, false}, {viewer, false}} {
		token := env.tokenFor(t, who.user)
		for _, suffix := range []string{"", "/workload?kind=deployments&namespace=shop&name=web"} {
			rec := env.do(t, http.MethodGet, findingsPath(cluster, suffix), token, nil)
			if who.visible && suffix == "" && rec.Code != http.StatusOK {
				t.Fatalf("%s: expected 200, got %d (%s)", who.user.Username, rec.Code, rec.Body.String())
			}
			if !who.visible && (rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "web")) {
				t.Fatalf("%s%s: expected a bare 404, got %d (%s)", who.user.Username, suffix, rec.Code, rec.Body.String())
			}
		}
		rec := env.do(t, http.MethodGet, "/api/v1/vulnerabilities/clusters", token, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s counts: %d", who.user.Username, rec.Code)
		}
		listed := strings.Contains(rec.Body.String(), `"danger":1`)
		if listed != who.visible {
			t.Fatalf("%s counts: visible=%v, body %s", who.user.Username, who.visible, rec.Body.String())
		}
	}
}

func TestSwitchedOffAlertsShowNothingAndSaySo(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("root", "secret123", "admin")
	cluster := env.store.addAgentCluster("edge", "prod", "agent-token")
	env.store.vulnScanning[cluster.ID] = db.ClusterVulnScanning{ClusterID: cluster.ID, Provider: db.ScannerTrivyOperator}
	env.store.vulnSummaries[cluster.ID] = []db.VulnWorkloadSummary{{Namespace: "shop", Kind: "Deployment", Name: "web", Level: db.VulnLevelDanger}}

	rec := env.do(t, http.MethodGet, findingsPath(cluster, ""), env.tokenFor(t, admin), nil)
	var body struct {
		Connected bool              `json:"connected"`
		Enabled   bool              `json:"enabled"`
		Workloads []json.RawMessage `json:"workloads"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("got %d (%s)", rec.Code, rec.Body.String())
	}
	if !body.Connected || body.Enabled || len(body.Workloads) != 0 {
		t.Fatalf("off is said, and nothing is shown: %s", rec.Body.String())
	}
}

func TestANeverConnectedClusterIsSaidNotEmpty(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("root", "secret123", "admin")
	cluster := env.store.addAgentCluster("edge", "prod", "agent-token")
	rec := env.do(t, http.MethodGet, findingsPath(cluster, ""), env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"connected":false`) {
		t.Fatalf("got %d (%s)", rec.Code, rec.Body.String())
	}
}
