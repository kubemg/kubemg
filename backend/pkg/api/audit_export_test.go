package api

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * An export is evidence, and the two things that make it evidence rather than a
 * download are that it cannot be widened and that it cannot lie about being
 * whole. Both are asserted here, along with the narrowing rule the trail itself
 * follows — a non-admin exports their own rows and nothing else, and a
 * `user_id` naming somebody else does not change that.
 */

func exportRows(t *testing.T, body string) [][]string {
	t.Helper()
	rows, err := csv.NewReader(strings.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatalf("the export is not readable as CSV: %v", err)
	}
	return rows
}

func TestAuditExportCarriesTheColumnsAndTheRows(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)

	env.store.addAuditEvent(db.AuditEvent{
		UserID: admin.ID, Username: "admin", Verb: "delete", Method: "DELETE",
		Path: "/api/v1/namespaces/prod/pods/api-7f", Namespace: "prod", Status: 200,
		SourceAddr: "10.4.2.9", UserAgent: "kubectl/v1.31.0",
	})

	rec := env.do(t, http.MethodGet, "/api/v1/audit/export", env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/csv") {
		t.Fatalf("expected a CSV content type, got %q", got)
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.Contains(got, "attachment") {
		t.Fatalf("an export has to arrive as a file, got %q", got)
	}

	rows := exportRows(t, rec.Body.String())
	if len(rows) != 2 {
		t.Fatalf("expected a header and one record, got %d rows", len(rows))
	}
	if strings.Join(rows[0], ",") != strings.Join(auditExportColumns, ",") {
		t.Fatalf("header row drifted from the column list: %v", rows[0])
	}
	// Where the call came from is the reason this column set exists; a header
	// that carried it and a row that did not would be the worst outcome.
	record := strings.Join(rows[1], ",")
	for _, want := range []string{"10.4.2.9", "kubectl/v1.31.0", "prod", "delete"} {
		if !strings.Contains(record, want) {
			t.Fatalf("expected the record to carry %q: %s", want, record)
		}
	}
}

func TestAuditExportIsNarrowedForANonAdminAndCannotBeWidened(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	user := env.store.addUser("devops", "pw", db.RoleUser)

	env.store.addAuditEvent(db.AuditEvent{UserID: admin.ID, Username: "admin", Verb: "delete", Status: 200})
	env.store.addAuditEvent(db.AuditEvent{UserID: user.ID, Username: "devops", Verb: "get", Status: 200})

	// Asking for the admin's rows by id must not hand them over, exactly as it
	// does not on the page this file is taken from.
	rec := env.do(t,
		http.MethodGet, "/api/v1/audit/export?user_id="+itoa(admin.ID),
		env.tokenFor(t, user), nil,
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	rows := exportRows(t, rec.Body.String())
	if len(rows) != 2 {
		t.Fatalf("expected a header and the caller's own single record, got %d rows", len(rows))
	}
	if !strings.Contains(strings.Join(rows[1], ","), "devops") {
		t.Fatalf("a non-admin's export must hold only their own rows: %v", rows[1])
	}
}

func TestAuditExportAnswersTheSameFilterAsThePage(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)

	env.store.addAuditEvent(db.AuditEvent{UserID: admin.ID, Username: "admin", Verb: "delete", Status: 200})
	env.store.addAuditEvent(db.AuditEvent{UserID: admin.ID, Username: "admin", Verb: "get", Status: 200})
	env.store.addAuditEvent(db.AuditEvent{UserID: admin.ID, Username: "admin", Verb: "get", Status: 403})

	rec := env.do(t, http.MethodGet, "/api/v1/audit/export?verb=get&failed=true", env.tokenFor(t, admin), nil)
	rows := exportRows(t, rec.Body.String())
	if len(rows) != 2 {
		t.Fatalf("expected the one refused get, got %d rows", len(rows))
	}
	if !strings.Contains(strings.Join(rows[1], ","), "403") {
		t.Fatalf("expected the refused record: %v", rows[1])
	}
}

func TestAuditExportIgnoresThePageTheReaderWasOn(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)

	for range 5 {
		env.store.addAuditEvent(db.AuditEvent{UserID: admin.ID, Username: "admin", Verb: "get", Status: 200})
	}

	// An offset carried over from the page would silently drop the rows above
	// it, which is the quietest way an export could be wrong.
	rec := env.do(t,
		http.MethodGet, "/api/v1/audit/export?limit=2&offset=3",
		env.tokenFor(t, admin), nil,
	)
	rows := exportRows(t, rec.Body.String())
	if len(rows) != 6 {
		t.Fatalf("expected a header and all five records, got %d rows", len(rows))
	}
}

func TestAuditExportRequiresAuth(t *testing.T) {
	env := newTestEnv(t)

	rec := env.do(t, http.MethodGet, "/api/v1/audit/export", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

// TestARecordedActCarriesWhereItCameFrom is the other half of the export: the
// column only means something if something fills it. It goes through a real
// request rather than calling the middleware, because what is being asserted is
// that the stamp survives the whole chain down to the auditor.
func TestARecordedActCarriesWhereItCameFrom(t *testing.T) {
	auditor := &recordingAuditor{}
	env := newTestEnvWith(t, func(o *Options) { o.Auditor = auditor })
	user := env.store.addUser("devops", "old-password", db.RoleUser)

	body, err := json.Marshal(map[string]any{
		"current_password": "old-password",
		"new_password":     "new-password",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, passwordPath, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+env.tokenFor(t, user))
	req.Header.Set("User-Agent", "Mozilla/5.0 (console)")
	req.RemoteAddr = "203.0.113.7:51544"
	env.router.ServeHTTP(httptest.NewRecorder(), req)

	sources := auditor.allSources()
	if len(sources) == 0 {
		t.Fatal("the act was not recorded at all")
	}
	if sources[0].Addr != "203.0.113.7" {
		t.Fatalf("expected the caller's address without its port, got %q", sources[0].Addr)
	}
	if sources[0].UserAgent != "Mozilla/5.0 (console)" {
		t.Fatalf("expected the caller's user agent, got %q", sources[0].UserAgent)
	}
}
