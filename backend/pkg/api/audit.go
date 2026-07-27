package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

// auditWindow is how far back the summary counts.
const auditWindow = 24 * time.Hour

type auditEventResponse struct {
	ID                 uint      `json:"id"`
	At                 time.Time `json:"at"`
	UserID             uint      `json:"user_id"`
	Username           string    `json:"username"`
	ClusterID          uint      `json:"cluster_id"`
	Cluster            string    `json:"cluster"`
	Verb               string    `json:"verb"`
	Method             string    `json:"method"`
	Path               string    `json:"path"`
	Namespace          string    `json:"namespace,omitempty"`
	Resource           string    `json:"resource,omitempty"`
	ImpersonatedUser   string    `json:"impersonated_user,omitempty"`
	ImpersonatedGroups []string  `json:"impersonated_groups"`
	Status             int       `json:"status"`
	DurationMS         int64     `json:"duration_ms"`
	Streaming          bool      `json:"streaming"`
	Phase              string    `json:"phase,omitempty"`
	BytesOut           int64     `json:"bytes_out,omitempty"`
	BytesIn            int64     `json:"bytes_in,omitempty"`
	Error              string    `json:"error,omitempty"`
}

func toAuditResponse(event db.AuditEvent) auditEventResponse {
	groups := event.ImpersonatedGroupList()
	if groups == nil {
		groups = []string{}
	}
	return auditEventResponse{
		ID:                 event.ID,
		At:                 event.At,
		UserID:             event.UserID,
		Username:           event.Username,
		ClusterID:          event.ClusterID,
		Cluster:            event.Cluster,
		Verb:               event.Verb,
		Method:             event.Method,
		Path:               event.Path,
		Namespace:          event.Namespace,
		Resource:           event.Resource,
		ImpersonatedUser:   event.ImpersonatedUser,
		ImpersonatedGroups: groups,
		Status:             event.Status,
		DurationMS:         event.DurationMS,
		Streaming:          event.Streaming,
		Phase:              event.Phase,
		BytesOut:           event.BytesOut,
		BytesIn:            event.BytesIn,
		Error:              event.Error,
	}
}

// listAudit returns a page of the audit trail. Admins see the whole fleet;
// everyone else sees only what they themselves did, which is a useful thing to
// be able to check and not a disclosure.
func (s *server) listAudit(c *gin.Context) {
	user, ok := s.currentUser(c)
	if !ok {
		return
	}

	filter, ok := auditFilterFrom(c)
	if !ok {
		return
	}
	if !user.IsAdmin() {
		filter.UserID = user.ID
	}

	events, total, err := s.store.ListAuditEvents(c.Request.Context(), filter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not read the audit trail"})
		return
	}

	out := make([]auditEventResponse, 0, len(events))
	for _, event := range events {
		out = append(out, toAuditResponse(event))
	}

	c.JSON(http.StatusOK, gin.H{
		"events": out,
		"total":  total,
		"limit":  filter.Limit,
		"offset": filter.Offset,
		// A non-admin is looking at their own trail; say so rather than letting
		// them wonder why the fleet looks so quiet.
		"scoped_to_self": !user.IsAdmin(),
	})
}

// auditSummary returns the headline counts for the last day.
func (s *server) auditSummary(c *gin.Context) {
	if _, ok := s.currentUser(c); !ok {
		return
	}

	stats, err := s.store.AuditSummary(c.Request.Context(), time.Now().Add(-auditWindow))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not summarize the audit trail"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"total":        stats.Total,
		"failed":       stats.Failed,
		"streams":      stats.Streams,
		"window_hours": int(auditWindow.Hours()),
	})
}

// auditFilterFrom parses the query string, writing the error response itself on
// anything malformed.
func auditFilterFrom(c *gin.Context) (db.AuditFilter, bool) {
	var filter db.AuditFilter

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

	filter.Verb = strings.TrimSpace(c.Query("verb"))
	filter.Namespace = strings.TrimSpace(c.Query("namespace"))
	filter.Search = strings.TrimSpace(c.Query("q"))
	filter.Streaming = c.Query("streaming") == "true"
	filter.FailedOnly = c.Query("failed") == "true"

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
