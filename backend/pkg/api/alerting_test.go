package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/kubemg/kubemg/backend/pkg/db"
	"github.com/kubemg/kubemg/backend/pkg/observability"
)

// Alertmanager has never heard of the caller, so everything asserted here is
// KubeMG's own narrowing: what a scoped grant is shown, who may mute what, and
// that a silence is built from the alert rather than from the request.

// fakeAlertmanager is a direct-mode Alertmanager holding a fixed alert set.
type fakeAlertmanager struct {
	mu       sync.Mutex
	posted   []map[string]any
	expired  []string
	silences string
}

const fakeAlerts = `[
 {"fingerprint":"a1","startsAt":"2026-10-01T10:00:00Z","labels":{"alertname":"KubePodCrashLooping","namespace":"team-a","pod":"api-7f9c5d-x2kq9","severity":"warning"},"annotations":{"summary":"crash"},"status":{"state":"active","silencedBy":[],"inhibitedBy":[]}},
 {"fingerprint":"a2","startsAt":"2026-10-01T10:00:00Z","labels":{"alertname":"KubePodCrashLooping","namespace":"team-a","pod":"api-gateway-6d4b8-p0q1w","severity":"warning"},"annotations":{},"status":{"state":"active","silencedBy":[],"inhibitedBy":[]}},
 {"fingerprint":"b1","startsAt":"2026-10-01T10:00:00Z","labels":{"alertname":"KubeDeploymentReplicasMismatch","namespace":"team-b","deployment":"web","severity":"critical"},"annotations":{},"status":{"state":"active","silencedBy":[],"inhibitedBy":[]}},
 {"fingerprint":"c1","startsAt":"2026-10-01T10:00:00Z","labels":{"alertname":"Watchdog","severity":"none"},"annotations":{},"status":{"state":"active","silencedBy":[],"inhibitedBy":[]}}
]`

const fakeSilences = `[
 {"id":"s-a","status":{"state":"active"},"matchers":[{"name":"namespace","value":"team-a","isRegex":false,"isEqual":true}],"startsAt":"2026-10-01T10:00:00Z","endsAt":"2026-10-01T12:00:00Z","createdBy":"x","comment":"c"},
 {"id":"s-re","status":{"state":"active"},"matchers":[{"name":"namespace","value":"team-a|team-b","isRegex":true}],"startsAt":"2026-10-01T10:00:00Z","endsAt":"2026-10-01T12:00:00Z","createdBy":"x","comment":"c"},
 {"id":"s-b","status":{"state":"active"},"matchers":[{"name":"namespace","value":"team-b","isRegex":false}],"startsAt":"2026-10-01T10:00:00Z","endsAt":"2026-10-01T12:00:00Z","createdBy":"x","comment":"c"},
 {"id":"s-old","status":{"state":"expired"},"matchers":[{"name":"namespace","value":"team-a","isRegex":false}],"startsAt":"2026-09-01T10:00:00Z","endsAt":"2026-09-01T12:00:00Z","createdBy":"x","comment":"c"}
]`

func (f *fakeAlertmanager) serve(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.URL.Path == "/api/v2/status":
			_, _ = w.Write([]byte(`{"versionInfo":{"version":"0.28.1"}}`))
		case r.URL.Path == "/api/v2/alerts":
			_, _ = w.Write([]byte(fakeAlerts))
		case r.URL.Path == "/api/v2/silences" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(fakeSilences))
		case r.URL.Path == "/api/v2/silences" && r.Method == http.MethodPost:
			body, _ := io.ReadAll(r.Body)
			var payload map[string]any
			_ = json.Unmarshal(body, &payload)
			f.posted = append(f.posted, payload)
			_, _ = w.Write([]byte(`{"silenceID":"new-1"}`))
		case strings.HasPrefix(r.URL.Path, "/api/v2/silence/"):
			id := strings.TrimPrefix(r.URL.Path, "/api/v2/silence/")
			if r.Method == http.MethodDelete {
				f.expired = append(f.expired, id)
				return
			}
			var all []map[string]any
			_ = json.Unmarshal([]byte(fakeSilences), &all)
			for _, item := range all {
				if item["id"] == id {
					_ = json.NewEncoder(w).Encode(item)
					return
				}
			}
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

type alertingEnv struct {
	env     *testEnv
	am      *fakeAlertmanager
	auditor *recordingAuditor
	cluster *db.Cluster
}

func newAlertingEnv(t *testing.T, withSource bool) alertingEnv {
	t.Helper()
	auditor := &recordingAuditor{}
	env := newTestEnvWith(t, func(o *Options) { o.Auditor = auditor })
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	am := &fakeAlertmanager{}
	if withSource {
		server := am.serve(t)
		_ = env.store.PutObservabilitySource(t.Context(), &db.ObservabilitySource{
			ClusterID: cluster.ID, Kind: db.SourceAlerts, Provider: db.ProviderAlertmanager,
			AccessMode: db.AccessDirect, URL: server.URL, AuthMode: db.AuthNone, Enabled: true,
			RuleLabels: "release=kube-prom",
		})
	}
	return alertingEnv{env: env, am: am, auditor: auditor, cluster: cluster}
}

func (a alertingEnv) user(t *testing.T, name, role string, namespaces ...string) string {
	t.Helper()
	user := a.env.store.addUser(name, "pw", db.RoleUser)
	a.env.store.grant(user.ID, a.cluster.ID, role, namespaces)
	return a.env.tokenFor(t, user)
}

func (a alertingEnv) url(path string) string {
	return "/api/v1/clusters/" + itoa(a.cluster.ID) + path
}

func fingerprints(alerts []observability.Alert) []string {
	out := []string{}
	for _, alert := range alerts {
		out = append(out, alert.Fingerprint)
	}
	return out
}

func TestAlertsAreNarrowedToTheGrant(t *testing.T) {
	a := newAlertingEnv(t, true)
	scoped := a.user(t, "dev", db.K8sRoleView, "team-a")
	admin := a.env.tokenFor(t, a.env.store.addUser("admin", "pw", db.RoleAdmin))

	type answer struct {
		Alerts     []observability.Alert `json:"alerts"`
		CanSilence bool                  `json:"can_silence"`
	}
	rec := a.env.do(t, http.MethodGet, a.url("/observability/alerts"), scoped, nil)
	got := decode[answer](t, rec)
	// The cluster-level Watchdog and team-b's alert are not this caller's.
	if strings.Join(fingerprints(got.Alerts), ",") != "a1,a2" {
		t.Fatalf("scoped alerts = %v, want a1,a2 (%s)", fingerprints(got.Alerts), rec.Body.String())
	}
	if got.CanSilence {
		t.Fatal("a view grant must not be offered Silence")
	}

	rec = a.env.do(t, http.MethodGet, a.url("/observability/alerts"), admin, nil)
	if all := decode[answer](t, rec); len(all.Alerts) != 4 || all.Alerts[0].Severity != "critical" {
		t.Fatalf("admin alerts = %v, want all four, critical first", fingerprints(all.Alerts))
	}

	rec = a.env.do(t, http.MethodGet, a.url("/observability/alerts?namespace=team-b"), scoped, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("another namespace: status = %d, want 403", rec.Code)
	}
}

// A Deployment's alerts include the ones raised about its pods, found by the
// fixed suffix shape — "api" claims api-7f9c5d-x2kq9, not api-gateway-….
func TestAlertsForAnObjectFindItsPods(t *testing.T) {
	a := newAlertingEnv(t, true)
	token := a.user(t, "dev", db.K8sRoleEdit, "team-a")

	rec := a.env.do(t, http.MethodGet,
		a.url("/observability/alerts?namespace=team-a&kind=deployments&name=api"), token, nil)
	got := decode[struct {
		Alerts     []observability.Alert `json:"alerts"`
		CanSilence bool                  `json:"can_silence"`
	}](t, rec)
	if strings.Join(fingerprints(got.Alerts), ",") != "a1" {
		t.Fatalf("deployment api alerts = %v, want a1", fingerprints(got.Alerts))
	}
	if !got.CanSilence {
		t.Fatal("an edit grant over the namespace should be offered Silence")
	}
}

func TestSilencingTakesAnEditGrantAndCopiesTheAlert(t *testing.T) {
	a := newAlertingEnv(t, true)
	viewer := a.user(t, "viewer", db.K8sRoleView, "team-a")
	editor := a.user(t, "editor", db.K8sRoleEdit, "team-a")
	body := map[string]any{"fingerprint": "a1", "duration": "4h", "comment": "deploy in progress"}

	if rec := a.env.do(t, http.MethodPost, a.url("/observability/silences"), viewer, body); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer: status = %d, want 403", rec.Code)
	}
	// Somebody else's namespace reads as gone, not as forbidden.
	other := map[string]any{"fingerprint": "b1", "duration": "4h", "comment": "x"}
	if rec := a.env.do(t, http.MethodPost, a.url("/observability/silences"), editor, other); rec.Code != http.StatusNotFound {
		t.Fatalf("other namespace: status = %d, want 404", rec.Code)
	}
	if rec := a.env.do(t, http.MethodPost, a.url("/observability/silences"), editor,
		map[string]any{"fingerprint": "a1", "duration": "4h"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("no reason: status = %d, want 400", rec.Code)
	}
	if rec := a.env.do(t, http.MethodPost, a.url("/observability/silences"), editor,
		map[string]any{"fingerprint": "a1", "duration": "30d", "comment": "x"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("unoffered duration: status = %d, want 400", rec.Code)
	}

	rec := a.env.do(t, http.MethodPost, a.url("/observability/silences"), editor, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("editor: status = %d, want 201 (%s)", rec.Code, rec.Body.String())
	}
	if len(a.am.posted) != 1 {
		t.Fatalf("posted %d silences, want 1", len(a.am.posted))
	}
	posted := a.am.posted[0]
	matchers, _ := posted["matchers"].([]any)
	if len(matchers) != 4 {
		t.Fatalf("matchers = %v, want one per alert label", matchers)
	}
	for _, raw := range matchers {
		m := raw.(map[string]any)
		if m["isRegex"] != false {
			t.Fatalf("matcher %v is a regex; a silence is exactly the alert", m)
		}
	}
	if !strings.HasPrefix(posted["createdBy"].(string), "editor") {
		t.Fatalf("createdBy = %v, want the KubeMG user", posted["createdBy"])
	}

	verbs := []string{}
	for _, event := range a.auditor.all() {
		verbs = append(verbs, event.Verb+":"+itoaInt(event.Status))
	}
	if strings.Join(verbs, ",") != "silence-create:403,silence-create:201" {
		t.Fatalf("audit = %v, want the refusal and the silence", verbs)
	}
}

func itoaInt(n int) string { return itoa(uint(n)) }

func TestSilencesAreShownOnlyWhenPinnedToTheGrant(t *testing.T) {
	a := newAlertingEnv(t, true)
	editor := a.user(t, "editor", db.K8sRoleEdit, "team-a")

	rec := a.env.do(t, http.MethodGet, a.url("/observability/silences"), editor, nil)
	got := decode[struct {
		Silences []struct {
			ID        string `json:"id"`
			CanExpire bool   `json:"can_expire"`
		} `json:"silences"`
	}](t, rec)
	// The regex silence spans team-b too, and the expired one is history.
	if len(got.Silences) != 1 || got.Silences[0].ID != "s-a" || !got.Silences[0].CanExpire {
		t.Fatalf("silences = %+v, want only s-a, expirable", got.Silences)
	}

	if rec := a.env.do(t, http.MethodDelete, a.url("/observability/silences/s-b"), editor, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("expire another namespace's silence: status = %d, want 404", rec.Code)
	}
	if rec := a.env.do(t, http.MethodDelete, a.url("/observability/silences/s-a"), editor, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("expire own silence: status = %d, want 204 (%s)", rec.Code, rec.Body.String())
	}
	if strings.Join(a.am.expired, ",") != "s-a" {
		t.Fatalf("expired = %v, want s-a", a.am.expired)
	}
}

func TestAnAlertsSourceKeepsItsRuleLabels(t *testing.T) {
	a := newAlertingEnv(t, false)
	admin := a.env.tokenFor(t, a.env.store.addUser("admin", "pw", db.RoleAdmin))
	server := a.am.serve(t)
	payload := map[string]any{
		"provider": db.ProviderAlertmanager, "access_mode": db.AccessDirect, "url": server.URL,
		"rule_labels": map[string]string{"release": "kube-prom"},
	}

	rec := a.env.do(t, http.MethodPut, a.url("/observability/sources/alerts"), admin, payload)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
	}
	got := decode[struct {
		Source sourceResponse `json:"source"`
	}](t, rec)
	if got.Source.RuleLabels["release"] != "kube-prom" || got.Source.DetectedVersion != "0.28.1" {
		t.Fatalf("source = %+v, want the rule label and the detected version", got.Source)
	}

	for _, labels := range []map[string]string{
		{"app.kubernetes.io/managed-by": "x"},
		{"kubemg.io/x": "y"},
		{"release": "a,b=c"},
	} {
		payload["rule_labels"] = labels
		if rec := a.env.do(t, http.MethodPut, a.url("/observability/sources/alerts"), admin, payload); rec.Code != http.StatusBadRequest {
			t.Fatalf("labels %v: status = %d, want 400", labels, rec.Code)
		}
	}
}

func TestCreatingAnAlarmNeedsTheAlertmanagerAndTheNamespace(t *testing.T) {
	a := newAlertingEnv(t, false)
	editor := a.user(t, "editor", db.K8sRoleEdit, "team-a")
	body := map[string]any{"namespace": "team-a", "kind": "deployments", "name": "api", "condition": "unavailable"}

	rec := a.env.do(t, http.MethodPost, a.url("/resources/alarms"), editor, body)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "unconfigured") {
		t.Fatalf("no alerts source: status = %d (%s), want 409 unconfigured", rec.Code, rec.Body.String())
	}

	a = newAlertingEnv(t, true)
	editor = a.user(t, "editor", db.K8sRoleEdit, "team-a")
	body["namespace"] = "team-b"
	if rec := a.env.do(t, http.MethodPost, a.url("/resources/alarms"), editor, body); rec.Code != http.StatusForbidden {
		t.Fatalf("outside the grant: status = %d, want 403 (%s)", rec.Code, rec.Body.String())
	}
	body["namespace"] = "team-a"
	body["condition"] = "not-a-condition"
	if rec := a.env.do(t, http.MethodPost, a.url("/resources/alarms"), editor, body); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown condition: status = %d, want 400", rec.Code)
	}
	body["condition"] = "unavailable"
	body["name"] = `api"} or up{`
	if rec := a.env.do(t, http.MethodPost, a.url("/resources/alarms"), editor, body); rec.Code != http.StatusBadRequest {
		t.Fatalf("matcher syntax in a name: status = %d, want 400", rec.Code)
	}
	// Everything KubeMG checks passed: the write reaches the tunnel, where the
	// cluster's RBAC decides. No agent is attached in this test.
	body["name"] = "api"
	if rec := a.env.do(t, http.MethodPost, a.url("/resources/alarms"), editor, body); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("valid alarm: status = %d, want 503 at the tunnel (%s)", rec.Code, rec.Body.String())
	}
}

func TestCanMute(t *testing.T) {
	admin := &db.User{SystemRole: db.SystemRoleAdmin}
	admin.Normalize()
	user := &db.User{SystemRole: db.SystemRoleUser}
	user.Normalize()
	grant := func(role string, namespaces ...string) db.UserClusterAccess {
		return db.UserClusterAccess{K8sRole: role, Namespaces: db.JoinNamespaces(namespaces)}
	}
	cases := []struct {
		user      *db.User
		grant     db.UserClusterAccess
		namespace string
		want      bool
	}{
		{admin, db.UserClusterAccess{}, "", true},
		{user, grant(db.K8sRoleView), "team-a", false},
		{user, grant(db.K8sRoleEdit), "", true},
		{user, grant(db.K8sRoleEdit, "team-a"), "team-a", true},
		{user, grant(db.K8sRoleEdit, "team-a"), "team-b", false},
		{user, grant(db.K8sRoleEdit, "team-a"), "", false},
		{user, grant(db.K8sRoleClusterAdmin, "team-a"), "team-a", true},
	}
	for i, tc := range cases {
		if got := canMute(tc.user, tc.grant, tc.namespace); got != tc.want {
			t.Fatalf("case %d: canMute = %v, want %v", i, got, tc.want)
		}
	}
}
