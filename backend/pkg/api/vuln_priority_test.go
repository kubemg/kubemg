package api

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/db"
	"github.com/kubemg/kubemg/backend/pkg/observability"
)

/*
 * Vulnerability alerts, PR 3: what comes first. Pinned here is what would be
 * quietly wrong — an exploited CVE left uncoloured, an accepted one still red,
 * a first KEV download paging everyone, a malformed list emptying the
 * catalogue, and someone outside the audience accepting a risk.
 */

func kevIntel(ids ...string) vulnIntel {
	intel := vulnIntel{kev: map[string]db.VulnKEVEntry{}, accepted: map[vulnAcceptKey]bool{}}
	for _, id := range ids {
		intel.kev[id] = db.VulnKEVEntry{CVE: id}
	}
	return intel
}

func TestAKnownExploitedFindingIsRedWhateverElseIsTrue(t *testing.T) {
	// db is unexposed; api's critical has no fix. Neither is danger without KEV.
	pass, err := summarizeVulnReports(vulnFixture(), time.Now(), kevIntel("CVE-4"))
	if err != nil {
		t.Fatal(err)
	}
	api := vulnRowFor(t, pass, "Deployment", "api")
	if api.Level != db.VulnLevelDanger || api.KEV != 1 || strings.Join(api.KEVIDs, ",") != "CVE-4" {
		t.Fatalf("an exploited, unfixable critical is red: %+v", api)
	}
	if !pass.KEVKnown || !strings.Contains(pass.Message, "1 run a known-exploited vulnerability") {
		t.Fatalf("the pass says it knew KEV: %+v", pass)
	}

	without, _ := summarizeVulnReports(vulnFixture(), time.Now(), vulnIntel{})
	if without.KEVKnown || strings.Contains(without.Message, "known-exploited") {
		t.Fatalf("no catalogue, no claim: %q", without.Message)
	}
}

func TestAnAcceptedFindingLeavesEveryCount(t *testing.T) {
	intel := kevIntel("CVE-1")
	intel.accepted[vulnAcceptKey{workload: vulnWorkloadKey{"shop", "Deployment", "web"}, cve: "CVE-1"}] = true
	pass, err := summarizeVulnReports(vulnFixture(), time.Now(), intel)
	if err != nil {
		t.Fatal(err)
	}
	web := vulnRowFor(t, pass, "Deployment", "web")
	// web had one critical (CVE-1, fixable, KEV) and highs CVE-2 (no fix) + CVE-3 (fix).
	if web.Critical != 0 || web.FixableCritical != 0 || web.KEV != 0 || web.Accepted != 1 {
		t.Fatalf("the accepted finding is out of every count: %+v", web)
	}
	if web.Level != db.VulnLevelWarn {
		t.Fatalf("what is left decides the colour — the fixable high: %+v", web)
	}
	if !strings.Contains(pass.Message, "1 accepted findings are not counted") {
		t.Fatalf("said: %q", pass.Message)
	}
}

func TestTheKEVAlarmFiresOnATransitionOnly(t *testing.T) {
	cluster := &db.Cluster{ID: 7, Name: "edge"}
	now := time.Now()
	pass, _ := summarizeVulnReports(vulnFixture(), now, kevIntel("CVE-1", "CVE-5"))

	if got := vulnKEVSignals(cluster, vulnKEVBaseline{}, pass); len(got) != 0 {
		t.Fatalf("the first pass with KEV in hand is where things stand: %+v", got)
	}

	baseline := vulnKEVBaseline{watermark: true, kev: map[vulnWorkloadKey][]string{
		{"shop", "Deployment", "web"}: {"CVE-1"},
	}}
	got := vulnKEVSignals(cluster, baseline, pass)
	if len(got) != 1 {
		t.Fatalf("only db gained one: %+v", got)
	}
	signal := got[0]
	if signal.Source != observability.SourceVulnerabilityKEV || signal.Namespace != "data" ||
		signal.Object != "statefulset/db" || !strings.Contains(signal.Message, "CVE-5") || signal.ClusterID != 7 {
		t.Fatalf("signal: %+v", signal)
	}

	unknown, _ := summarizeVulnReports(vulnFixture(), now, vulnIntel{})
	if got := vulnKEVSignals(cluster, baseline, unknown); len(got) != 0 {
		t.Fatalf("a pass without KEV fires nothing: %+v", got)
	}

	rule := db.AlarmRule{Trigger: db.TriggerVulnerabilityKEV, Namespaces: "data"}
	if !observability.Matches(rule, signal) {
		t.Fatal("a vulnerability_kev rule on the namespace matches")
	}
	rule.Namespaces = "shop"
	if observability.Matches(rule, signal) {
		t.Fatal("another namespace does not")
	}
}

func TestTheDrawerPutsExploitedFirstAndAcceptedLast(t *testing.T) {
	var report trivyReport
	raw, _ := json.Marshal(vulnReport("shop", "Deployment", "web", "nginx", 2, 1,
		vulnFinding("CVE-2024-0001", "CRITICAL", "1"),
		vulnFinding("CVE-2024-0002", "CRITICAL", "1"),
		vulnFinding("CVE-2024-0003", "LOW", ""),
		vulnFinding("CVE-2024-0004", "HIGH", "1"),
	))
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	intel := vulnDrawerIntel{
		kev:      map[string]db.VulnKEVEntry{"CVE-2024-0003": {CVE: "CVE-2024-0003", Ransomware: true}},
		epss:     map[string]db.VulnEPSSEntry{"CVE-2024-0002": {Score: 0.9, Percentile: 0.99}},
		accepted: map[string]db.VulnAcceptance{"CVE-2024-0004": {Reason: "not reachable", AcceptedBy: "root"}},
	}
	view := vulnContainer(report, intel)
	var order []string
	for _, f := range view.Findings {
		order = append(order, f.ID)
	}
	if strings.Join(order, ",") != "CVE-2024-0003,CVE-2024-0002,CVE-2024-0001,CVE-2024-0004" {
		t.Fatalf("KEV, then severity, then EPSS; accepted last: %v", order)
	}
	if view.Findings[0].KEV == nil || !view.Findings[0].KEV.Ransomware {
		t.Fatalf("KEV detail: %+v", view.Findings[0])
	}
	if view.Findings[1].EPSS == nil || *view.Findings[1].EPSS != 0.9 {
		t.Fatalf("EPSS: %+v", view.Findings[1])
	}
	if view.Findings[3].Accepted == nil || view.Findings[3].Accepted.Reason != "not reachable" {
		t.Fatalf("accepted is marked, not hidden: %+v", view.Findings[3])
	}
}

func TestParsingTheListsRefusesWhatIsNotOne(t *testing.T) {
	kev := `{"catalogVersion":"2026.11.16","vulnerabilities":[
		{"cveID":"CVE-2024-3094","vendorProject":"XZ","product":"xz","dateAdded":"2024-04-01","dueDate":"2024-04-22","knownRansomwareCampaignUse":"Known"},
		{"cveID":"not-a-cve"},{"cveID":"CVE-2024-3094"}]}`
	entries, version, err := parseKEV([]byte(kev))
	if err != nil || len(entries) != 1 || version != "2026.11.16" || !entries[0].Ransomware || entries[0].DateAdded == nil {
		t.Fatalf("kev: %+v %q %v", entries, version, err)
	}
	for _, bad := range []string{`<html>`, `{"vulnerabilities":[]}`} {
		if _, _, err := parseKEV([]byte(bad)); err == nil {
			t.Fatalf("%q is refused", bad)
		}
	}

	csv := "#model_version:v2025.03.14,score_date:2026-11-16T00:00:00+0000\ncve,epss,percentile\nCVE-2024-3094,0.85,0.99\nCVE-2020-0001,0.0004,0.1\n"
	var zipped bytes.Buffer
	gz := gzip.NewWriter(&zipped)
	_, _ = gz.Write([]byte(csv))
	_ = gz.Close()
	for _, body := range [][]byte{[]byte(csv), zipped.Bytes()} {
		scores, version, err := parseEPSS(body)
		if err != nil || len(scores) != 1 || scores[0].CVE != "CVE-2024-3094" || !strings.HasPrefix(version, "2026-11-16") {
			t.Fatalf("epss keeps what orders something: %+v %q %v", scores, version, err)
		}
	}
	if _, _, err := parseEPSS([]byte("cve,epss,percentile\n")); err == nil {
		t.Fatal("a header alone is refused")
	}
	if _, _, err := parseEPSS([]byte("<html>")); err == nil {
		t.Fatal("not a CSV is refused")
	}
}

func uploadIntel(env *testEnv, kind, token string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/vulnerabilities/intel/"+kind+"/upload", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)
	return rec
}

func TestAnUploadReplacesTheListOnlyWhenItParses(t *testing.T) {
	env := newTestEnv(t)
	admin := env.tokenFor(t, env.store.addUser("root", "secret123", "admin"))
	user := env.tokenFor(t, env.store.addUser("dev", "secret123", "user"))
	good := []byte(`{"vulnerabilities":[{"cveID":"CVE-2024-3094"}]}`)

	if rec := uploadIntel(env, "kev", user, good); rec.Code != http.StatusForbidden {
		t.Fatalf("admin-only: %d", rec.Code)
	}
	if rec := env.do(t, http.MethodGet, "/api/v1/vulnerabilities/intel", user, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("the read too: %d", rec.Code)
	}
	if rec := uploadIntel(env, "kev", admin, good); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"origin":"upload"`) {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body.String())
	}
	if rec := uploadIntel(env, "kev", admin, []byte(`{"vulnerabilities":[]}`)); rec.Code != http.StatusBadRequest {
		t.Fatalf("an empty list is refused: %d", rec.Code)
	}
	if len(env.store.vulnKEV) != 1 {
		t.Fatalf("and the catalogue is kept: %+v", env.store.vulnKEV)
	}
	if rec := uploadIntel(env, "nvd", admin, good); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown kind: %d", rec.Code)
	}
	rec := env.do(t, http.MethodPut, "/api/v1/vulnerabilities/intel/kev", admin, map[string]any{"enabled": true, "url": "ftp://mirror"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a non-web address is refused: %d", rec.Code)
	}
	rec = env.do(t, http.MethodPut, "/api/v1/vulnerabilities/intel/kev", admin, map[string]any{"enabled": true, "url": ""})
	if rec.Code != http.StatusOK || env.store.vulnIntel["kev"].URL != db.DefaultKEVURL || !env.store.vulnIntel["kev"].Enabled {
		t.Fatalf("empty address is CISA's: %d %+v", rec.Code, env.store.vulnIntel["kev"])
	}
}

func TestAcceptingARiskIsForTheAudienceAndNeedsAReason(t *testing.T) {
	auditor := &recordingAuditor{}
	env := newTestEnvWith(t, func(opts *Options) { opts.Auditor = auditor })
	cluster := env.store.addAgentCluster("edge", "prod", "agent-token")
	env.store.vulnScanning[cluster.ID] = db.ClusterVulnScanning{ClusterID: cluster.ID, Provider: db.ScannerTrivyOperator, Enabled: true}
	admin := env.tokenFor(t, env.store.addUser("root", "secret123", "admin"))
	scopedUser := env.store.addUser("scoped", "secret123", "user")
	env.store.grant(scopedUser.ID, cluster.ID, "cluster-admin", []string{"shop"})
	scoped := env.tokenFor(t, scopedUser)

	body := map[string]any{"kind": "deployments", "namespace": "shop", "name": "web", "cve": "cve-2024-3094", "reason": "not reachable: the binary is never invoked"}
	path := findingsPath(cluster, "/acceptances")

	if rec := env.do(t, http.MethodPost, path, scoped, body); rec.Code != http.StatusNotFound {
		t.Fatalf("outside the audience: %d", rec.Code)
	}
	for _, bad := range []map[string]any{
		{"kind": "deployments", "namespace": "shop", "name": "web", "cve": "CVE-2024-3094", "reason": " "},
		{"kind": "secrets", "namespace": "shop", "name": "web", "cve": "CVE-2024-3094", "reason": "x"},
		{"kind": "deployments", "namespace": "shop", "name": "web", "cve": "<script>", "reason": "x"},
		{"kind": "deployments", "namespace": "shop", "name": "web", "cve": "CVE-2024-3094", "reason": "x", "expires_at": time.Now().Add(-time.Hour)},
		{"kind": "deployments", "namespace": "shop", "name": "web", "cve": "CVE-2024-3094", "reason": "x", "expires_at": time.Now().AddDate(2, 0, 0)},
	} {
		if rec := env.do(t, http.MethodPost, path, admin, bad); rec.Code != http.StatusBadRequest {
			t.Fatalf("%v: %d %s", bad, rec.Code, rec.Body.String())
		}
	}

	rec := env.do(t, http.MethodPost, path, admin, body)
	if rec.Code != http.StatusOK || len(env.store.vulnAcceptances) != 1 {
		t.Fatalf("accepted: %d %s", rec.Code, rec.Body.String())
	}
	stored := env.store.vulnAcceptances[0]
	if stored.CVE != "CVE-2024-3094" || stored.Kind != "Deployment" || stored.AcceptedBy != "root" {
		t.Fatalf("stored: %+v", stored)
	}
	if !strings.Contains(rec.Body.String(), "could not be refreshed") {
		t.Fatalf("no tunnel: the refresh is said to wait, not claimed: %s", rec.Body.String())
	}
	risk := func() []string {
		var out []string
		for _, e := range auditor.events {
			if strings.HasPrefix(e.Verb, "risk-") {
				out = append(out, e.Verb+"|"+e.Error)
			}
		}
		return out
	}
	if got := risk(); len(got) != 1 || got[0] != verbRiskAccept+"|"+body["reason"].(string) {
		t.Fatalf("audited with its reason: %v", got)
	}

	withdraw := path + "?kind=deployments&namespace=shop&name=web&cve=CVE-2024-3094"
	if rec := env.do(t, http.MethodDelete, withdraw, admin, nil); rec.Code != http.StatusOK || len(env.store.vulnAcceptances) != 0 {
		t.Fatalf("withdrawn: %d", rec.Code)
	}
	if rec := env.do(t, http.MethodDelete, withdraw, admin, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("nothing to withdraw: %d", rec.Code)
	}
	if got := risk(); len(got) != 2 || got[1] != verbRiskWithdraw+"|" {
		t.Fatalf("withdrawal audited: %v", got)
	}
}

func TestAnExpiredAcceptanceCountsAgain(t *testing.T) {
	past := time.Now().Add(-time.Hour)
	if (db.VulnAcceptance{ExpiresAt: &past}).Live(time.Now()) {
		t.Fatal("expired")
	}
	if !(db.VulnAcceptance{}).Live(time.Now()) {
		t.Fatal("no expiry holds")
	}
}
