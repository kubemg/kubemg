package api

import (
	"compress/gzip"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/bastion"
	"github.com/kubemg/kubemg/backend/pkg/db"
)

type terminalSessionListResponse struct {
	Sessions         []terminalSessionResponse `json:"sessions"`
	Total            int64                     `json:"total"`
	RecordingEnabled bool                      `json:"recording_enabled"`
	ScopedToSelf     bool                      `json:"scoped_to_self"`
}

// writeCast puts a playable recording on disk and returns its path.
func writeCast(t *testing.T, dir, name, body string) string {
	t.Helper()

	path := filepath.Join(dir, name+".cast.gz")
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create recording: %v", err)
	}
	gz := gzip.NewWriter(file)
	if _, err := gz.Write([]byte(body)); err != nil {
		t.Fatalf("write recording: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("flush recording: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close recording: %v", err)
	}
	return path
}

// recordingEnv is a router that is recording, plus the directory it records to.
func recordingEnv(t *testing.T) (*testEnv, string) {
	t.Helper()
	dir := t.TempDir()
	env := newTestEnvWith(t, func(opts *Options) { opts.RecordingDir = dir })
	return env, dir
}

// auditedRecordingEnv is recordingEnv with the API's own auditor attached, for
// the tests that assert reading a recording leaves a trail.
func auditedRecordingEnv(t *testing.T) (*testEnv, string, *recordingAuditor) {
	t.Helper()
	dir := t.TempDir()
	auditor := &recordingAuditor{}
	env := newTestEnvWith(t, func(opts *Options) {
		opts.RecordingDir = dir
		opts.Auditor = auditor
	})
	return env, dir, auditor
}

// eventsFor picks the records written for one verb.
func eventsFor(auditor *recordingAuditor, verb string) []bastion.Event {
	var out []bastion.Event
	for _, event := range auditor.all() {
		if event.Verb == verb {
			out = append(out, event)
		}
	}
	return out
}

func TestTerminalSessionsRequireAuth(t *testing.T) {
	env := newTestEnv(t)

	rec := env.do(t, http.MethodGet, "/api/v1/audit/terminal-sessions", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestTerminalSessionsNonAdminSeesOnlyTheirOwn(t *testing.T) {
	env, _ := recordingEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	user := env.store.addUser("devops", "pw", db.RoleUser)

	env.store.addTerminalSession(db.TerminalSession{
		SessionID: "admin-session", UserID: admin.ID, Username: "admin", PodName: "api-0",
	})
	env.store.addTerminalSession(db.TerminalSession{
		SessionID: "own-session", UserID: user.ID, Username: "devops", PodName: "web-1",
	})

	rec := env.do(t, http.MethodGet, "/api/v1/audit/terminal-sessions", env.tokenFor(t, user), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}

	body := decode[terminalSessionListResponse](t, rec)
	if len(body.Sessions) != 1 || body.Sessions[0].SessionID != "own-session" {
		t.Fatalf("a user must only see their own sessions, got %+v", body.Sessions)
	}
	if !body.ScopedToSelf {
		t.Fatal("the response should say the view is narrowed")
	}
	if !body.RecordingEnabled {
		t.Fatal("this server is recording and should say so")
	}
}

func TestTerminalSessionsCannotBeWidenedByQueryParameter(t *testing.T) {
	env, _ := recordingEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	user := env.store.addUser("devops", "pw", db.RoleUser)

	env.store.addTerminalSession(db.TerminalSession{
		SessionID: "admin-session", UserID: admin.ID, Username: "admin",
	})

	rec := env.do(t, http.MethodGet,
		"/api/v1/audit/terminal-sessions?user_id="+itoa(admin.ID), env.tokenFor(t, user), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	if body := decode[terminalSessionListResponse](t, rec); len(body.Sessions) != 0 {
		t.Fatalf("a user must not reach another account's recordings: %+v", body.Sessions)
	}
}

func TestTerminalSessionFoundByItsAuditCorrelationID(t *testing.T) {
	env, _ := recordingEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)

	env.store.addTerminalSession(db.TerminalSession{
		SessionID: "wanted", UserID: admin.ID, Username: "admin", PodName: "api-0",
	})
	env.store.addTerminalSession(db.TerminalSession{
		SessionID: "other", UserID: admin.ID, Username: "admin", PodName: "web-1",
	})

	rec := env.do(t, http.MethodGet,
		"/api/v1/audit/terminal-sessions?session_id=wanted", env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	body := decode[terminalSessionListResponse](t, rec)
	if len(body.Sessions) != 1 || body.Sessions[0].SessionID != "wanted" {
		t.Fatalf("a row in the trail must find its own recording, got %+v", body.Sessions)
	}
}

func TestTerminalSessionStreamServesTheDecompressedCast(t *testing.T) {
	env, dir := recordingEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)

	cast := "{\"version\":2,\"width\":80,\"height\":24}\n[0.1,\"o\",\"hello\"]\n"
	session := env.store.addTerminalSession(db.TerminalSession{
		SessionID:   "abc",
		UserID:      admin.ID,
		Username:    "admin",
		StoragePath: writeCast(t, dir, "abc", cast),
	})

	rec := env.do(t, http.MethodGet,
		"/api/v1/audit/terminal-sessions/"+itoa(session.ID)+"/stream", env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	if rec.Body.String() != cast {
		t.Fatalf("the player must get the recording verbatim, got %q", rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store, private" {
		t.Fatalf("a recording must not be cached by a shared proxy, got %q", got)
	}
}

func TestTerminalSessionStreamRefusesSomeoneElsesSession(t *testing.T) {
	env, dir := recordingEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	user := env.store.addUser("devops", "pw", db.RoleUser)

	session := env.store.addTerminalSession(db.TerminalSession{
		SessionID:   "admin-shell",
		UserID:      admin.ID,
		Username:    "admin",
		StoragePath: writeCast(t, dir, "admin-shell", "{\"version\":2}\n"),
	})

	// 404 rather than 403: whether a recording of somebody else's shell exists is
	// itself not theirs to learn.
	for _, path := range []string{
		"/api/v1/audit/terminal-sessions/" + itoa(session.ID),
		"/api/v1/audit/terminal-sessions/" + itoa(session.ID) + "/stream",
	} {
		rec := env.do(t, http.MethodGet, path, env.tokenFor(t, user), nil)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: expected status %d, got %d", path, http.StatusNotFound, rec.Code)
		}
	}
}

func TestTerminalSessionStreamRefusesAPathOutsideTheRecordingDirectory(t *testing.T) {
	env, dir := recordingEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)

	elsewhere := writeCast(t, t.TempDir(), "escaped", "{\"version\":2}\n")
	session := env.store.addTerminalSession(db.TerminalSession{
		SessionID: "escaped", UserID: admin.ID, Username: "admin", StoragePath: elsewhere,
	})
	if filepath.Dir(elsewhere) == dir {
		t.Fatal("the fixture is meant to live outside the recording directory")
	}

	rec := env.do(t, http.MethodGet,
		"/api/v1/audit/terminal-sessions/"+itoa(session.ID)+"/stream", env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusConflict, rec.Code, rec.Body.String())
	}
}

func TestTerminalSessionStreamSaysWhenRecordingIsOff(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	session := env.store.addTerminalSession(db.TerminalSession{
		SessionID: "abc", UserID: admin.ID, Username: "admin", StoragePath: "/nowhere/abc.cast.gz",
	})

	rec := env.do(t, http.MethodGet,
		"/api/v1/audit/terminal-sessions/"+itoa(session.ID)+"/stream", env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status %d, got %d", http.StatusServiceUnavailable, rec.Code)
	}
}

func TestTerminalSessionStreamReportsAMissingRecording(t *testing.T) {
	env, dir := recordingEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	session := env.store.addTerminalSession(db.TerminalSession{
		SessionID:   "gone",
		UserID:      admin.ID,
		Username:    "admin",
		StoragePath: filepath.Join(dir, "gone.cast.gz"),
	})

	rec := env.do(t, http.MethodGet,
		"/api/v1/audit/terminal-sessions/"+itoa(session.ID)+"/stream", env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, rec.Code)
	}
}

func TestTerminalSessionDeleteIsAdminOnlyAndTakesTheFile(t *testing.T) {
	env, dir := recordingEnv(t)
	admin := env.store.addRecordingViewer("admin", "pw")
	plain := env.store.addUser("ops-admin", "pw", db.RoleAdmin)
	user := env.store.addUser("devops", "pw", db.RoleUser)

	path := writeCast(t, dir, "own", "{\"version\":2}\n")
	session := env.store.addTerminalSession(db.TerminalSession{
		SessionID: "own", UserID: user.ID, Username: "devops", StoragePath: path,
	})

	// The subject of a recording does not get to end its existence.
	rec := env.do(t, http.MethodDelete,
		"/api/v1/audit/terminal-sessions/"+itoa(session.ID), env.tokenFor(t, user), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d", http.StatusForbidden, rec.Code)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the recording should still be there: %v", err)
	}

	// Nor does an administrator who may not watch it: destroying evidence you
	// are not cleared to see is not a lesser act than seeing it.
	rec = env.do(t, http.MethodDelete,
		"/api/v1/audit/terminal-sessions/"+itoa(session.ID), env.tokenFor(t, plain), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d for an admin without the capability, got %d",
			http.StatusForbidden, rec.Code)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the recording should still be there: %v", err)
	}

	rec = env.do(t, http.MethodDelete,
		"/api/v1/audit/terminal-sessions/"+itoa(session.ID), env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusNoContent, rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the recording outlived its row: %v", err)
	}
	if len(env.store.recordings) != 0 {
		t.Fatalf("the row should be gone, got %+v", env.store.recordings)
	}
}

func TestRetentionPassRemovesRecordingsWithTheirRows(t *testing.T) {
	store := newFakeStore()
	dir := t.TempDir()

	stale := writeCast(t, dir, "stale", "{\"version\":2}\n")
	fresh := writeCast(t, dir, "fresh", "{\"version\":2}\n")
	store.addTerminalSession(db.TerminalSession{
		SessionID:   "stale",
		StartedAt:   time.Now().AddDate(0, 0, -400),
		StoragePath: stale,
	})
	store.addTerminalSession(db.TerminalSession{
		SessionID: "fresh", StartedAt: time.Now(), StoragePath: fresh,
	})

	// The window is the audit trail's own: a replay must not outlive the record
	// that says the session happened.
	pruner := newPruneServer(store, 30)
	pruner.recordings = dir
	pruner.pruneAudit(context.Background())

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("a recording past retention is still on disk: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("a recording inside retention was removed: %v", err)
	}
	if len(store.recordings) != 1 || store.recordings[0].SessionID != "fresh" {
		t.Fatalf("the wrong rows survived: %+v", store.recordings)
	}
}


func TestRecordingsNeedTheViewerCapability(t *testing.T) {
	env, dir := recordingEnv(t)
	// An ordinary administrator: may run KubeMG, may not watch what a colleague
	// typed into production.
	admin := env.store.addUser("ops-admin", "pw", db.RoleAdmin)
	user := env.store.addUser("devops", "pw", db.RoleUser)

	own := env.store.addTerminalSession(db.TerminalSession{
		SessionID: "admin-own", UserID: admin.ID, Username: "ops-admin",
		StoragePath: writeCast(t, dir, "admin-own", "{\"version\":2}\n"),
	})
	theirs := env.store.addTerminalSession(db.TerminalSession{
		SessionID: "devops-shell", UserID: user.ID, Username: "devops",
		StoragePath: writeCast(t, dir, "devops-shell", "{\"version\":2}\n"),
	})

	rec := env.do(t, http.MethodGet, "/api/v1/audit/terminal-sessions", env.tokenFor(t, admin), nil)
	body := decode[terminalSessionListResponse](t, rec)
	if len(body.Sessions) != 1 || body.Sessions[0].SessionID != "admin-own" {
		t.Fatalf("an admin without the capability must see only their own sessions, got %+v",
			body.Sessions)
	}
	if !body.ScopedToSelf {
		t.Fatal("the response must say the view is narrowed")
	}

	// Their own replays still work — the capability governs other people's.
	rec = env.do(t, http.MethodGet,
		"/api/v1/audit/terminal-sessions/"+itoa(own.ID)+"/stream", env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("an admin must still replay their own session: %d (%s)", rec.Code, rec.Body.String())
	}

	// 404, not 403: whether a colleague's recording exists is not theirs to learn.
	rec = env.do(t, http.MethodGet,
		"/api/v1/audit/terminal-sessions/"+itoa(theirs.ID)+"/stream", env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, rec.Code)
	}

	// And with the capability, the same request is answered.
	viewer := env.store.addRecordingViewer("security", "pw")
	rec = env.do(t, http.MethodGet,
		"/api/v1/audit/terminal-sessions/"+itoa(theirs.ID)+"/stream", env.tokenFor(t, viewer), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("a recording viewer must be able to replay: %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestSuperAdminHoldsTheRecordingCapabilityImplicitly(t *testing.T) {
	env, dir := recordingEnv(t)
	root := env.store.addSuperAdmin("root", "pw")
	user := env.store.addUser("devops", "pw", db.RoleUser)

	session := env.store.addTerminalSession(db.TerminalSession{
		SessionID: "devops-shell", UserID: user.ID, Username: "devops",
		StoragePath: writeCast(t, dir, "devops-shell", "{\"version\":2}\n"),
	})

	// Nothing grants it to this account: the account that can grant the
	// capability can already take it, so withholding it from a super admin would
	// be theatre.
	rec := env.do(t, http.MethodGet,
		"/api/v1/audit/terminal-sessions/"+itoa(session.ID)+"/stream", env.tokenFor(t, root), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
}

func TestWatchingARecordingIsAudited(t *testing.T) {
	env, dir, auditor := auditedRecordingEnv(t)
	viewer := env.store.addRecordingViewer("security", "pw")
	user := env.store.addUser("devops", "pw", db.RoleUser)
	cluster := env.store.addCluster("prod-eu", "production")

	session := env.store.addTerminalSession(db.TerminalSession{
		SessionID: "devops-shell", UserID: user.ID, Username: "devops",
		ClusterID: cluster.ID, Cluster: cluster.Name, Namespace: "payments", PodName: "api-0",
		StoragePath: writeCast(t, dir, "devops-shell", "{\"version\":2}\n"),
	})

	rec := env.do(t, http.MethodGet,
		"/api/v1/audit/terminal-sessions/"+itoa(session.ID)+"/stream", env.tokenFor(t, viewer), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}

	replays := eventsFor(auditor, verbReplay)
	if len(replays) != 1 {
		t.Fatalf("watching a recording must leave exactly one record, got %d", len(replays))
	}
	event := replays[0]
	// The viewer is the subject of the record and the session id is the subject
	// of the recording: neither half answers "who watched whose shell" alone.
	if event.UserID != viewer.ID || event.Username != "security" {
		t.Fatalf("the record must name the viewer, got %d/%q", event.UserID, event.Username)
	}
	if event.SessionID != "devops-shell" {
		t.Fatalf("the record must carry the session that was watched, got %q", event.SessionID)
	}
	if event.ClusterID != cluster.ID || event.Namespace != "payments" {
		t.Fatalf("the record must sit with the cluster it came from, got %+v", event)
	}
	if event.Status != http.StatusOK || event.Error != "" {
		t.Fatalf("a successful replay must read as one, got %+v", event)
	}
}

func TestARefusedReplayIsAudited(t *testing.T) {
	env, dir, auditor := auditedRecordingEnv(t)
	admin := env.store.addUser("ops-admin", "pw", db.RoleAdmin)
	user := env.store.addUser("devops", "pw", db.RoleUser)

	session := env.store.addTerminalSession(db.TerminalSession{
		SessionID: "devops-shell", UserID: user.ID, Username: "devops",
		StoragePath: writeCast(t, dir, "devops-shell", "{\"version\":2}\n"),
	})

	rec := env.do(t, http.MethodGet,
		"/api/v1/audit/terminal-sessions/"+itoa(session.ID)+"/stream", env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, rec.Code)
	}

	// An attempt on a colleague's session is the most interesting line on this
	// route, so it is recorded rather than only answered.
	replays := eventsFor(auditor, verbReplay)
	if len(replays) != 1 {
		t.Fatalf("a refused replay must be recorded, got %d records", len(replays))
	}
	if replays[0].Status != http.StatusNotFound || replays[0].Error == "" {
		t.Fatalf("the record must say it was refused, got %+v", replays[0])
	}
	if replays[0].UserID != admin.ID {
		t.Fatalf("the record must name who tried, got %d", replays[0].UserID)
	}
}

func TestDeletingARecordingIsAudited(t *testing.T) {
	env, dir, auditor := auditedRecordingEnv(t)
	viewer := env.store.addRecordingViewer("security", "pw")
	user := env.store.addUser("devops", "pw", db.RoleUser)

	session := env.store.addTerminalSession(db.TerminalSession{
		SessionID: "devops-shell", UserID: user.ID, Username: "devops",
		StoragePath: writeCast(t, dir, "devops-shell", "{\"version\":2}\n"),
	})

	rec := env.do(t, http.MethodDelete,
		"/api/v1/audit/terminal-sessions/"+itoa(session.ID), env.tokenFor(t, viewer), nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusNoContent, rec.Code, rec.Body.String())
	}

	// The record has to outlive what it describes, or the trail is deniable.
	deletes := eventsFor(auditor, verbRecordingWrite)
	if len(deletes) != 1 {
		t.Fatalf("deleting a recording must be recorded, got %d records", len(deletes))
	}
	if deletes[0].SessionID != "devops-shell" || deletes[0].UserID != viewer.ID {
		t.Fatalf("the record must name who removed what, got %+v", deletes[0])
	}
}

func TestReadingTheIndexIsNotAudited(t *testing.T) {
	env, _, auditor := auditedRecordingEnv(t)
	viewer := env.store.addRecordingViewer("security", "pw")

	rec := env.do(t, http.MethodGet, "/api/v1/audit/terminal-sessions", env.tokenFor(t, viewer), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	// Listing sessions is metadata an administrator needs to do the job.
	// Recording it as well would bury the invasive act among routine ones.
	if events := auditor.all(); len(events) != 0 {
		t.Fatalf("listing recordings should not be audited, got %+v", events)
	}
}

func TestRecordingPolicyIsReadableByAnyone(t *testing.T) {
	dir := t.TempDir()
	env := newTestEnvWith(t, func(opts *Options) {
		opts.RecordingDir = dir
		opts.RecordingInput = true
		opts.RecordingKey = make([]byte, 32)
	})
	user := env.store.addUser("devops", "pw", db.RoleUser)

	// Anyone might be recorded, so anyone may ask what is being kept — a console
	// that opens a shell has to be able to say so before a keystroke is typed.
	rec := env.do(t, http.MethodGet, "/api/v1/audit/recording-policy", env.tokenFor(t, user), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	body := decode[struct {
		Enabled       bool `json:"enabled"`
		InputRecorded bool `json:"input_recorded"`
		Encrypted     bool `json:"encrypted"`
		RetentionDays int  `json:"retention_days"`
	}](t, rec)

	if !body.Enabled || !body.InputRecorded || !body.Encrypted {
		t.Fatalf("the policy must describe this server, got %+v", body)
	}
	if body.RetentionDays <= 0 {
		t.Fatalf("an operator has to be told how long it is kept, got %d", body.RetentionDays)
	}
}

func TestRecordingPolicyClaimsNothingWhenRecordingIsOff(t *testing.T) {
	env := newTestEnvWith(t, func(opts *Options) {
		// No directory: nothing is being recorded, so nothing may be promised
		// about keystrokes or encryption either.
		opts.RecordingInput = true
		opts.RecordingKey = make([]byte, 32)
	})
	user := env.store.addUser("devops", "pw", db.RoleUser)

	rec := env.do(t, http.MethodGet, "/api/v1/audit/recording-policy", env.tokenFor(t, user), nil)
	body := decode[struct {
		Enabled       bool `json:"enabled"`
		InputRecorded bool `json:"input_recorded"`
		Encrypted     bool `json:"encrypted"`
	}](t, rec)
	if body.Enabled || body.InputRecorded || body.Encrypted {
		t.Fatalf("a server that records nothing must say so plainly, got %+v", body)
	}
}
