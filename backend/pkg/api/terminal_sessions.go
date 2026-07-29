package api

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/db"
	"github.com/kubemg/kubemg/backend/pkg/terminal"
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
	Open  bool   `json:"open"`
	Error string `json:"error,omitempty"`
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
		Error:           session.Error,
	}
}

// listTerminalSessions returns a page of recorded sessions. It follows the audit
// trail's rule exactly: an admin sees the fleet, and everyone else sees only
// their own sessions — a recording of somebody else's shell is the single most
// sensitive thing this product stores.
func (s *server) listTerminalSessions(c *gin.Context) {
	user, ok := s.currentUser(c)
	if !ok {
		return
	}

	filter, ok := terminalSessionFilterFrom(c)
	if !ok {
		return
	}
	if !user.IsAdmin() {
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
		"scoped_to_self":    !user.IsAdmin(),
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

// streamTerminalSession serves the decompressed asciinema stream the player
// reads. The stored path is confined to the configured recording directory
// before anything is opened: it comes out of a database row, and a row is not a
// filesystem instruction.
func (s *server) streamTerminalSession(c *gin.Context) {
	session, ok := s.terminalSession(c)
	if !ok {
		return
	}
	if s.recordings == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "session recording is not enabled on this server, so recordings cannot be read",
		})
		return
	}

	reader, err := terminal.Open(s.recordings, session.StoragePath)
	switch {
	case errors.Is(err, terminal.ErrMissing):
		c.JSON(http.StatusNotFound, gin.H{
			"error": "this session's recording is no longer on disk",
		})
		return
	case errors.Is(err, terminal.ErrOutsideDir):
		s.log().Error("refused to read a recording stored outside the recording directory",
			"session_id", session.SessionID)
		c.JSON(http.StatusConflict, gin.H{
			"error": "this recording is not stored where this server keeps recordings",
		})
		return
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not read this recording"})
		return
	}
	defer func() { _ = reader.Close() }()

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
func (s *server) deleteTerminalSession(c *gin.Context) {
	if _, ok := s.currentUser(c); !ok {
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
	// A non-admin may replay their own sessions and nothing else. 404 rather
	// than 403: whether a recording of someone else's shell exists is itself
	// something they have no business learning.
	if !user.IsAdmin() && session.UserID != user.ID {
		c.JSON(http.StatusNotFound, gin.H{"error": "recording not found"})
		return nil, false
	}
	return session, true
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
