package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

type auditListResponse struct {
	Events       []auditEventResponse `json:"events"`
	Total        int64                `json:"total"`
	ScopedToSelf bool                 `json:"scoped_to_self"`
}

func (f *fakeStore) addAuditEvent(event db.AuditEvent) db.AuditEvent {
	if event.At.IsZero() {
		event.At = time.Now()
	}
	event.ID = f.nextID
	f.nextID++
	f.audit = append(f.audit, event)
	return event
}

func TestAuditRequiresAuth(t *testing.T) {
	env := newTestEnv(t)

	rec := env.do(t, http.MethodGet, "/api/v1/audit", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestAuditAdminSeesTheWholeFleet(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	other := env.store.addUser("devops", "pw", db.RoleUser)

	env.store.addAuditEvent(db.AuditEvent{UserID: admin.ID, Username: "admin", Verb: "list", Status: 200})
	env.store.addAuditEvent(db.AuditEvent{UserID: other.ID, Username: "devops", Verb: "get", Status: 200})

	rec := env.do(t, http.MethodGet, "/api/v1/audit", env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}

	body := decode[auditListResponse](t, rec)
	if len(body.Events) != 2 {
		t.Fatalf("an admin should see both records, got %d", len(body.Events))
	}
	if body.ScopedToSelf {
		t.Fatal("an admin's view is not scoped to themselves")
	}
}

func TestAuditNonAdminSeesOnlyTheirOwn(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	user := env.store.addUser("devops", "pw", db.RoleUser)

	env.store.addAuditEvent(db.AuditEvent{UserID: admin.ID, Username: "admin", Verb: "delete", Status: 200})
	env.store.addAuditEvent(db.AuditEvent{UserID: user.ID, Username: "devops", Verb: "get", Status: 200})

	rec := env.do(t, http.MethodGet, "/api/v1/audit", env.tokenFor(t, user), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	body := decode[auditListResponse](t, rec)
	if len(body.Events) != 1 || body.Events[0].Username != "devops" {
		t.Fatalf("a user must only see their own actions, got %+v", body.Events)
	}
	if !body.ScopedToSelf {
		t.Fatal("the response should say the view is narrowed")
	}
}

func TestAuditCannotBeWidenedByQueryParameter(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	user := env.store.addUser("devops", "pw", db.RoleUser)

	env.store.addAuditEvent(db.AuditEvent{UserID: admin.ID, Username: "admin", Verb: "delete", Status: 200})

	// Asking for someone else's trail must not grant it.
	rec := env.do(t, http.MethodGet, "/api/v1/audit?user_id="+itoa(admin.ID), env.tokenFor(t, user), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	body := decode[auditListResponse](t, rec)
	if len(body.Events) != 0 {
		t.Fatalf("a user must not be able to read another account's trail: %+v", body.Events)
	}
}

func TestAuditFilters(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	cluster := env.store.addAgentCluster("edge-us", db.EnvStaging, "kmg_t")

	env.store.addAuditEvent(db.AuditEvent{
		UserID: admin.ID, Username: "admin", ClusterID: cluster.ID,
		Verb: "list", Path: "/api/v1/namespaces/team-a/pods", Namespace: "team-a", Status: 200,
	})
	env.store.addAuditEvent(db.AuditEvent{
		UserID: admin.ID, Username: "admin", ClusterID: cluster.ID,
		Verb: "watch", Path: "/api/v1/namespaces/team-b/pods", Namespace: "team-b",
		Status: 200, Streaming: true, Phase: "open",
	})
	env.store.addAuditEvent(db.AuditEvent{
		UserID: admin.ID, Username: "admin", ClusterID: cluster.ID,
		Verb: "get", Path: "/api/v1/namespaces/kube-system/secrets", Namespace: "kube-system",
		Status: 403, Error: "namespace kube-system is outside your granted scope",
	})

	token := env.tokenFor(t, admin)
	cases := map[string]int{
		"?verb=watch":                     1,
		"?namespace=team-a":               1,
		"?streaming=true":                 1,
		"?failed=true":                    1,
		"?q=secrets":                      1,
		"?cluster_id=" + itoa(cluster.ID): 3,
		"":                                3,
	}

	for query, want := range cases {
		t.Run(query, func(t *testing.T) {
			rec := env.do(t, http.MethodGet, "/api/v1/audit"+query, token, nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
			}
			body := decode[auditListResponse](t, rec)
			if len(body.Events) != want {
				t.Fatalf("filter %q returned %d records, want %d", query, len(body.Events), want)
			}
		})
	}
}

func TestAuditRejectsMalformedFilters(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	token := env.tokenFor(t, admin)

	for _, query := range []string{"?cluster_id=abc", "?since=yesterday", "?limit=0", "?offset=-1"} {
		rec := env.do(t, http.MethodGet, "/api/v1/audit"+query, token, nil)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: expected status %d, got %d", query, http.StatusBadRequest, rec.Code)
		}
	}
}

func TestAuditPaginates(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)

	for i := range 5 {
		env.store.addAuditEvent(db.AuditEvent{
			UserID: admin.ID, Username: "admin", Verb: "get", Status: 200,
			At: time.Now().Add(-time.Duration(i) * time.Minute),
		})
	}

	rec := env.do(t, http.MethodGet, "/api/v1/audit?limit=2&offset=2", env.tokenFor(t, admin), nil)
	body := decode[auditListResponse](t, rec)
	if len(body.Events) != 2 {
		t.Fatalf("expected a page of 2, got %d", len(body.Events))
	}
	if body.Total != 5 {
		t.Fatalf("total should count every match, got %d", body.Total)
	}
}

func TestAuditSummary(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)

	env.store.addAuditEvent(db.AuditEvent{UserID: admin.ID, Verb: "get", Status: 200})
	env.store.addAuditEvent(db.AuditEvent{UserID: admin.ID, Verb: "get", Status: 403, Error: "denied"})
	env.store.addAuditEvent(db.AuditEvent{UserID: admin.ID, Verb: "watch", Status: 200, Streaming: true, Phase: "open"})
	env.store.addAuditEvent(db.AuditEvent{UserID: admin.ID, Verb: "watch", Status: 200, Streaming: true, Phase: "close"})
	// Outside the window.
	env.store.addAuditEvent(db.AuditEvent{UserID: admin.ID, Verb: "get", Status: 200, At: time.Now().Add(-48 * time.Hour)})

	rec := env.do(t, http.MethodGet, "/api/v1/audit/summary", env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	body := decode[map[string]int64](t, rec)
	if body["total"] != 4 {
		t.Fatalf("the summary should cover the window only, got %d", body["total"])
	}
	if body["failed"] != 1 {
		t.Fatalf("failed = %d, want 1", body["failed"])
	}
	// A stream writes two records but is one session.
	if body["streams"] != 1 {
		t.Fatalf("streams = %d, want 1 session", body["streams"])
	}
}

func TestAuditFiltersByAVerbSet(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)

	for _, verb := range []string{"get", "list", "delete", "exec"} {
		env.store.addAuditEvent(db.AuditEvent{
			UserID: admin.ID, Username: "admin", Verb: verb, Status: 200,
		})
	}

	// A badge multi-select sends several verbs; a dropdown sends one. Both are the
	// same question and both have to be answered.
	rec := env.do(t, http.MethodGet,
		"/api/v1/audit?verb=delete,exec", env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	body := decode[auditListResponse](t, rec)
	if len(body.Events) != 2 {
		t.Fatalf("expected the two selected verbs, got %d", len(body.Events))
	}
	for _, event := range body.Events {
		if event.Verb != "delete" && event.Verb != "exec" {
			t.Fatalf("unexpected verb %q in the page", event.Verb)
		}
	}

	// Repeated parameters are the other shape a form produces.
	rec = env.do(t, http.MethodGet,
		"/api/v1/audit?verb=delete&verb=exec", env.tokenFor(t, admin), nil)
	if got := decode[auditListResponse](t, rec); len(got.Events) != 2 {
		t.Fatalf("repeated verb parameters should narrow the same way, got %d", len(got.Events))
	}

	// A single verb still takes the singular path.
	rec = env.do(t, http.MethodGet, "/api/v1/audit?verb=exec", env.tokenFor(t, admin), nil)
	if got := decode[auditListResponse](t, rec); len(got.Events) != 1 {
		t.Fatalf("expected one exec record, got %d", len(got.Events))
	}
}

func TestAuditFiltersByStatus(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)

	env.store.addAuditEvent(db.AuditEvent{UserID: admin.ID, Verb: "get", Status: 200})
	env.store.addAuditEvent(db.AuditEvent{UserID: admin.ID, Verb: "delete", Status: 403})
	env.store.addAuditEvent(db.AuditEvent{UserID: admin.ID, Verb: "create", Status: 500})

	rec := env.do(t, http.MethodGet, "/api/v1/audit?status=403", env.tokenFor(t, admin), nil)
	body := decode[auditListResponse](t, rec)
	if len(body.Events) != 1 || body.Events[0].Status != 403 {
		t.Fatalf("expected only the 403, got %+v", body.Events)
	}

	rec = env.do(t, http.MethodGet, "/api/v1/audit?status=99", env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a status outside the HTTP range should be refused, got %d", rec.Code)
	}
}

func TestAuditQuickRangeNarrowsTheWindow(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)

	now := time.Now()
	env.store.addAuditEvent(db.AuditEvent{UserID: admin.ID, Verb: "get", At: now.Add(-5 * time.Minute)})
	env.store.addAuditEvent(db.AuditEvent{UserID: admin.ID, Verb: "get", At: now.Add(-3 * time.Hour)})
	env.store.addAuditEvent(db.AuditEvent{UserID: admin.ID, Verb: "get", At: now.Add(-8 * 24 * time.Hour)})

	// The preset is resolved on the server so "the last hour" means the same
	// window to the count, the page and anything that is not the console.
	for _, tc := range []struct {
		window string
		want   int
	}{
		{"15m", 1},
		{"24h", 2},
		{"30d", 3},
		{"all", 3},
	} {
		rec := env.do(t, http.MethodGet,
			"/api/v1/audit?range="+tc.window, env.tokenFor(t, admin), nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("range=%s: expected %d, got %d", tc.window, http.StatusOK, rec.Code)
		}
		if got := decode[auditListResponse](t, rec); len(got.Events) != tc.want {
			t.Errorf("range=%s: expected %d records, got %d", tc.window, tc.want, len(got.Events))
		}
	}

	rec := env.do(t, http.MethodGet, "/api/v1/audit?range=3y", env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("an unknown preset should be refused, got %d", rec.Code)
	}
}

func TestAuditFromAndToAreAcceptedAlongsideSinceAndUntil(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)

	now := time.Now().UTC()
	env.store.addAuditEvent(db.AuditEvent{UserID: admin.ID, Verb: "get", At: now.Add(-2 * time.Hour)})
	env.store.addAuditEvent(db.AuditEvent{UserID: admin.ID, Verb: "get", At: now.Add(-30 * time.Minute)})

	from := now.Add(-time.Hour).Format(time.RFC3339)
	rec := env.do(t, http.MethodGet, "/api/v1/audit?from="+from, env.tokenFor(t, admin), nil)
	if got := decode[auditListResponse](t, rec); len(got.Events) != 1 {
		t.Fatalf("from should narrow like since, got %d", len(got.Events))
	}

	// An explicit boundary wins over a preset: it is the more specific statement.
	rec = env.do(t, http.MethodGet, "/api/v1/audit?from="+from+"&range=30d", env.tokenFor(t, admin), nil)
	if got := decode[auditListResponse](t, rec); len(got.Events) != 1 {
		t.Fatalf("an explicit from should beat a preset, got %d", len(got.Events))
	}

	rec = env.do(t, http.MethodGet, "/api/v1/audit?from=yesterday", env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a malformed timestamp should be refused, got %d", rec.Code)
	}
}
