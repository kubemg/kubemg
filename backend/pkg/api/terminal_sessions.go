package api

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/bastion"
	"github.com/kubemg/kubemg/backend/pkg/db"
	"github.com/kubemg/kubemg/backend/pkg/terminal"
)

// The verbs this server records for its own reads of recordings. They are not
// Kubernetes verbs — nothing here touches a cluster — but they belong in the same
// trail: "who watched a recording of somebody else's production shell" is a more
// sensitive line than most of what the proxy writes, and an auditor should not
// have to look for it somewhere else.
const (
	verbReplay         = "replay"
	verbRecordingRead  = "recording-get"
	verbRecordingWrite = "recording-delete"
)

// maxCastResponse bounds what one replay may stream out. The recorder caps a
// recording as it is written, so this is a second line of defence against a file
// that grew some other way — not the primary limit.
const maxCastResponse = 64 << 20

type terminalSessionResponse struct {
	ID        uint   `json:"id"`
	SessionID string `json:"session_id"`
	UserID    uint   `json:"user_id"`
	Username  string `json:"username"`
	ClusterID uint   `json:"cluster_id"`
	Cluster   string `json:"cluster"`
	Namespace string `json:"namespace,omitempty"`
	Pod       string `json:"pod_name,omitempty"`
	Container string `json:"container_name,omitempty"`
	Shell     string `json:"shell,omitempty"`
	Verb      string `json:"verb,omitempty"`

	StartedAt       time.Time  `json:"started_at"`
	EndedAt         *time.Time `json:"ended_at,omitempty"`
	DurationSeconds int64      `json:"duration_seconds"`
	ByteCount       int64      `json:"byte_count"`
	Truncated       bool       `json:"truncated"`
	// Open is true while the session is still running, which is what lets the
	// list distinguish a finished recording from a shell somebody is in now.
	Open bool `json:"open"`
	// Encrypted and InputRecorded describe how this particular file was written,
	// not how the server is configured now. A recording made before a key was
	// configured stays unencrypted, and one made while keystrokes were not being
	// collected has none — an empty keystroke view has to be distinguishable from
	// a session in which nothing was typed.
	Encrypted     bool   `json:"encrypted"`
	InputRecorded bool   `json:"input_recorded"`
	Error         string `json:"error,omitempty"`
}

func toTerminalSessionResponse(session db.TerminalSession) terminalSessionResponse {
	return terminalSessionResponse{
		ID:              session.ID,
		SessionID:       session.SessionID,
		UserID:          session.UserID,
		Username:        session.Username,
		ClusterID:       session.ClusterID,
		Cluster:         session.Cluster,
		Namespace:       session.Namespace,
		Pod:             session.PodName,
		Container:       session.ContainerName,
		Shell:           session.Shell,
		Verb:            session.Verb,
		StartedAt:       session.StartedAt,
		EndedAt:         session.EndedAt,
		DurationSeconds: session.DurationSeconds,
		ByteCount:       session.ByteCount,
		Truncated:       session.Truncated,
		Open:            session.IsOpen(),
		Encrypted:       session.Encrypted,
		InputRecorded:   session.InputRecorded,
		Error:           session.Error,
	}
}

// listTerminalSessions returns a page of recorded sessions.
//
// It follows the audit trail's rule, with one addition: everyone sees their own
// sessions, and seeing anybody else's needs the recording-viewer capability on
// top of the admin role. Being able to administer KubeMG is not the same claim as
// being able to watch what a colleague typed into production, and an auditor
// asking who could see these files deserves a shorter answer than "every admin".
func (s *server) listTerminalSessions(c *gin.Context) {
	user, ok := s.currentUser(c)
	if !ok {
		return
	}

	filter, ok := terminalSessionFilterFrom(c)
	if !ok {
		return
	}
	if !user.MayViewAllRecordings() {
		filter.UserID = user.ID
	}

	sessions, total, err := s.store.ListTerminalSessions(c.Request.Context(), filter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not read the recorded sessions"})
		return
	}

	out := make([]terminalSessionResponse, 0, len(sessions))
	for _, session := range sessions {
		out = append(out, toTerminalSessionResponse(session))
	}

	c.JSON(http.StatusOK, gin.H{
		"sessions": out,
		"total":    total,
		"limit":    filter.Limit,
		"offset":   filter.Offset,
		// Whether this server is recording at all. Without it an empty list is
		// ambiguous — nobody opened a shell, or nobody was recording when they
		// did — and those call for entirely different actions.
		"recording_enabled": s.recordings != "",
		"scoped_to_self":    !user.MayViewAllRecordings(),
	})
}

// recordingPolicy reports what this server captures. It is readable by anyone,
// because anyone might be recorded: a console that opens a shell has to be able
// to say what is being kept before a keystroke is typed into it, and "ask an
// administrator" is not an answer that arrives in time.
//
// It reports policy, not content, so it leaks nothing: whether recording is on,
// whether keystrokes are part of it, and whether files are encrypted at rest.
func (s *server) recordingPolicy(c *gin.Context) {
	if _, ok := s.currentUser(c); !ok {
		return
	}
	enabled := s.recordings != ""
	c.JSON(http.StatusOK, gin.H{
		"enabled": enabled,
		// Both are meaningless when nothing is being recorded, and reporting them
		// as true there would read as a promise this server is not keeping.
		"input_recorded":  enabled && s.recordingInput,
		"encrypted":       enabled && terminal.Encrypted(s.recordingKey),
		// The window recordings are pruned on, which is the audit trail's — so
		// an operator can see how long what they are about to type is kept.
		"retention_days": s.settings(c.Request.Context()).AuditRetentionDays,
	})
}

// showTerminalSession returns one recording's details.
func (s *server) showTerminalSession(c *gin.Context) {
	session, ok := s.terminalSession(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, toTerminalSessionResponse(*session))
}

// streamTerminalSession serves the decrypted, decompressed asciinema stream the
// player reads. The stored path is confined to the configured recording directory
// before anything is opened: it comes out of a database row, and a row is not a
// filesystem instruction.
//
// **Watching a recording is itself audited**, and that is the point of the record
// rather than a detail of it. Everything else in this product is audited because
// it reaches a cluster; this reaches a file that holds what somebody typed into
// production, and a surveillance capability with no trail of its own is the first
// thing an auditor asks about. The record names the *viewer* and carries the
// subject's session id, so both ends of "who watched whose shell" are answerable.
func (s *server) streamTerminalSession(c *gin.Context) {
	started := time.Now()
	session, ok := s.terminalSession(c)
	if !ok {
		return
	}
	if s.recordings == "" {
		s.recordRecordingAccess(c, session, verbReplay, http.StatusServiceUnavailable,
			"recording is not enabled on this server", started)
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "session recording is not enabled on this server, so recordings cannot be read",
		})
		return
	}

	reader, err := terminal.Open(s.recordings, session.StoragePath, s.recordingKey)
	switch {
	case errors.Is(err, terminal.ErrMissing):
		s.recordRecordingAccess(c, session, verbReplay, http.StatusNotFound,
			"recording file is missing", started)
		c.JSON(http.StatusNotFound, gin.H{
			"error": "this session's recording is no longer on disk",
		})
		return
	case errors.Is(err, terminal.ErrOutsideDir):
		s.log().Error("refused to read a recording stored outside the recording directory",
			"session_id", session.SessionID)
		s.recordRecordingAccess(c, session, verbReplay, http.StatusConflict,
			"recording path is outside the recording directory", started)
		c.JSON(http.StatusConflict, gin.H{
			"error": "this recording is not stored where this server keeps recordings",
		})
		return
	case errors.Is(err, terminal.ErrKeyRequired):
		// The evidence still exists; what is missing is the key. Saying so is
		// what tells an operator to restore the key rather than conclude the
		// recording was lost.
		s.recordRecordingAccess(c, session, verbReplay, http.StatusConflict,
			"recording is encrypted and no key is configured", started)
		c.JSON(http.StatusConflict, gin.H{
			"error": "this recording is encrypted and this server has no recording key configured. " +
				"Restore KUBEMG_SESSION_RECORDING_KEY to read it.",
		})
		return
	case errors.Is(err, terminal.ErrKeyMismatch):
		s.log().Error("a recording would not decrypt with the configured key",
			"session_id", session.SessionID)
		s.recordRecordingAccess(c, session, verbReplay, http.StatusConflict,
			"recording did not decrypt with the configured key", started)
		c.JSON(http.StatusConflict, gin.H{
			"error": "this recording could not be decrypted with the configured key: " +
				"either the key is not the one it was written with, or the file has been altered.",
		})
		return
	case errors.Is(err, terminal.ErrTruncated):
		s.recordRecordingAccess(c, session, verbReplay, http.StatusConflict,
			"recording is truncated", started)
		c.JSON(http.StatusConflict, gin.H{
			"error": "this recording is incomplete — it was cut off before it was finished, " +
				"so what is on disk cannot be authenticated as a whole session.",
		})
		return
	case err != nil:
		s.recordRecordingAccess(c, session, verbReplay, http.StatusInternalServerError,
			"could not read the recording", started)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not read this recording"})
		return
	}
	defer func() { _ = reader.Close() }()

	// Recorded before the bytes go out rather than after: a replay that is
	// interrupted half way through was still a replay, and a trail that only
	// carried completed ones would be a trail somebody could avoid.
	s.recordRecordingAccess(c, session, verbReplay, http.StatusOK, "", started)

	// A recording is one person's session, and caching it in a shared proxy is
	// not something to leave to a default.
	c.Header("Cache-Control", "no-store, private")
	c.Header("Content-Type", "application/x-asciicast; charset=utf-8")
	c.Status(http.StatusOK)

	// A decompression failure part way through cannot become a status code —
	// the header is already out — so what the player gets is a short stream,
	// which it renders as a recording that stops early. The log carries why.
	if _, err := io.Copy(c.Writer, io.LimitReader(reader, maxCastResponse)); err != nil {
		s.log().Warn("a recording stream ended early",
			"session_id", session.SessionID, "error", err.Error())
	}
}

// deleteTerminalSession removes a recording. Administrative only: a recording is
// audit evidence, and the person it is evidence about must not be the one who
// decides it stops existing.
//
// It also needs the recording-viewer capability, because destroying evidence you
// are not allowed to look at is not a lesser act than watching it. And it is
// audited before the file goes, so the trail outlives what it describes — a
// deletion that left no record would make the whole trail deniable.
func (s *server) deleteTerminalSession(c *gin.Context) {
	started := time.Now()
	user, ok := s.currentUser(c)
	if !ok {
		return
	}
	if !user.MayViewAllRecordings() {
		c.JSON(http.StatusForbidden, gin.H{
			"error": "deleting a session recording needs access to other people's recordings",
		})
		return
	}
	id, ok := terminalSessionID(c)
	if !ok {
		return
	}

	session, err := s.store.TerminalSessionByID(c.Request.Context(), id)
	if errors.Is(err, db.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "recording not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load this recording"})
		return
	}

	// Recorded first, for the same reason the file goes before the row: what is
	// about to stop existing has to be described while it still does.
	s.recordRecordingAccess(c, session, verbRecordingWrite, http.StatusNoContent, "", started)

	// The file goes first: a row removed while its file survives leaves a
	// recording on disk that nothing references and no retention pass will reach.
	if s.recordings != "" {
		if err := terminal.Remove(s.recordings, session.StoragePath); err != nil &&
			!errors.Is(err, terminal.ErrOutsideDir) {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "could not remove this recording"})
			return
		}
	}
	if err := s.store.DeleteTerminalSession(c.Request.Context(), id); err != nil &&
		!errors.Is(err, db.ErrNotFound) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not remove this recording"})
		return
	}
	c.Status(http.StatusNoContent)
}

// terminalSession loads the addressed recording and decides whether the caller
// may see it. It writes the error response itself when it refuses.
func (s *server) terminalSession(c *gin.Context) (*db.TerminalSession, bool) {
	user, ok := s.currentUser(c)
	if !ok {
		return nil, false
	}
	id, ok := terminalSessionID(c)
	if !ok {
		return nil, false
	}

	session, err := s.store.TerminalSessionByID(c.Request.Context(), id)
	if errors.Is(err, db.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "recording not found"})
		return nil, false
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load this recording"})
		return nil, false
	}
	// Own sessions always; anybody else's only with the recording-viewer
	// capability. 404 rather than 403: whether a recording of someone else's shell
	// exists is itself something they have no business learning.
	if session.UserID != user.ID && !user.MayViewAllRecordings() {
		// A refusal is the most interesting line on this route, so it is recorded
		// rather than only answered — an attempt to reach a colleague's session is
		// exactly what an auditor is looking for.
		s.recordRecordingAccess(c, session, recordingVerbFor(c), http.StatusNotFound,
			"not permitted to read another user's recording", time.Now())
		c.JSON(http.StatusNotFound, gin.H{"error": "recording not found"})
		return nil, false
	}
	return session, true
}

// recordingVerbFor names what a request to this route family is doing, so a
// refusal is recorded as the act it was refused for.
func recordingVerbFor(c *gin.Context) string {
	switch {
	case c.Request.Method == http.MethodDelete:
		return verbRecordingWrite
	case strings.HasSuffix(c.Request.URL.Path, "/stream"):
		return verbReplay
	default:
		return verbRecordingRead
	}
}

// recordRecordingAccess writes one audit record for a read of a recording.
//
// The identities are deliberately crossed: the record's user is the *viewer*,
// and the session id is the *subject's* session — that pairing is the whole
// question, and neither half answers it alone. The cluster comes from the
// recording so the record sits alongside the proxied calls from the same cluster
// in the same trail.
//
// Reading the index is not recorded. A list of sessions is metadata an
// administrator needs to do the job; watching one is the invasive act, and a
// trail that records both equally makes the sensitive line harder to find rather
// than easier.
func (s *server) recordRecordingAccess(
	c *gin.Context,
	session *db.TerminalSession,
	verb string,
	status int,
	failure string,
	started time.Time,
) {
	if s.auditor == nil || session == nil {
		return
	}
	user, ok := s.currentUser(c)
	if !ok {
		return
	}

	s.auditor.Record(c.Request.Context(), bastion.Event{
		At:        time.Now().UTC(),
		UserID:    user.ID,
		Username:  user.Username,
		ClusterID: session.ClusterID,
		Cluster:   session.Cluster,
		Verb:      verb,
		Method:    c.Request.Method,
		Path:      c.Request.URL.Path,
		Namespace: session.Namespace,
		Resource:  "terminalsessions",
		Status:    status,
		Duration:  time.Since(started),
		Error:     failure,
		SessionID: session.SessionID,
	})
}

func terminalSessionID(c *gin.Context) (uint, bool) {
	parsed, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || parsed == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid recording id"})
		return 0, false
	}
	return uint(parsed), true
}

// terminalSessionFilterFrom parses the query string, writing the error response
// itself on anything malformed.
func terminalSessionFilterFrom(c *gin.Context) (db.TerminalSessionFilter, bool) {
	var filter db.TerminalSessionFilter

	uintParam := func(name string) (uint, bool) {
		raw := c.Query(name)
		if raw == "" {
			return 0, true
		}
		parsed, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": name + " must be a positive integer"})
			return 0, false
		}
		return uint(parsed), true
	}

	clusterID, ok := uintParam("cluster_id")
	if !ok {
		return filter, false
	}
	userID, ok := uintParam("user_id")
	if !ok {
		return filter, false
	}
	filter.ClusterID = clusterID
	filter.UserID = userID

	filter.Namespace = strings.TrimSpace(c.Query("namespace"))
	filter.Pod = strings.TrimSpace(c.Query("pod"))
	// How the audit trail reaches a replay: a row there carries the correlation
	// id, not the recording's own row id.
	filter.SessionID = strings.TrimSpace(c.Query("session_id"))
	filter.Search = strings.TrimSpace(c.Query("q"))
	filter.OpenOnly = c.Query("open") == "true"

	timeParam := func(name string) (*time.Time, bool) {
		raw := c.Query(name)
		if raw == "" {
			return nil, true
		}
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": name + " must be an RFC3339 timestamp"})
			return nil, false
		}
		return &parsed, true
	}

	since, ok := timeParam("since")
	if !ok {
		return filter, false
	}
	until, ok := timeParam("until")
	if !ok {
		return filter, false
	}
	filter.Since = since
	filter.Until = until

	if raw := c.Query("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "limit must be a positive integer"})
			return filter, false
		}
		filter.Limit = parsed
	}
	if raw := c.Query("offset"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "offset must be zero or more"})
			return filter, false
		}
		filter.Offset = parsed
	}

	return filter, true
}
