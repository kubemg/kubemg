package api

import (
	"compress/gzip"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

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
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
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
