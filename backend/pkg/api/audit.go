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
	// SessionID is set on an interactive session, and is what a recording of
	// that session is filed under — the join between this row and its replay.
	SessionID string `json:"session_id,omitempty"`
	// GuardrailPolicy names the safety policy this call matched and
	// GuardrailAction says what the match did. They are what makes the trail
	// answer "what has this rule caught?" — which is the question asked of a rule
	// running in warn, before anyone dares arm it. Without them the only trace is
	// a sentence inside the error string, which nothing can filter on.
	GuardrailPolicy string `json:"guardrail_policy,omitempty"`
	GuardrailAction string `json:"guardrail_action,omitempty"`
	Error           string `json:"error,omitempty"`
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
		SessionID:          event.SessionID,
		GuardrailPolicy:    event.GuardrailPolicy,
		GuardrailAction:    event.GuardrailAction,
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

	filter.Namespace = strings.TrimSpace(c.Query("namespace"))
	filter.Search = strings.TrimSpace(c.Query("q"))
	filter.Streaming = c.Query("streaming") == "true"
	filter.FailedOnly = c.Query("failed") == "true"

	// A verb may arrive singular or as a comma-separated set, because those are
	// the two ways the question gets asked: a dropdown sends one and a badge
	// multi-select sends several. A single value stays on the singular field so an
	// existing caller's query is answered by exactly the SQL it was answered by
	// before.
	verbs := listParam(c, "verb")
	switch len(verbs) {
	case 0:
	case 1:
		filter.Verb = verbs[0]
	default:
		filter.Verbs = verbs
	}

	if raw := strings.TrimSpace(c.Query("status")); raw != "" {
		status, err := strconv.Atoi(raw)
		if err != nil || status < 100 || status > 599 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "status must be an HTTP status code"})
			return filter, false
		}
		filter.Status = status
	}

	timeParam := func(name string) (*time.Time, bool) {
		raw := strings.TrimSpace(c.Query(name))
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

	// `from`/`to` are the names the filter UI uses and `since`/`until` are the
	// ones the API shipped with. Both are accepted rather than one being renamed:
	// a saved link into the audit trail is a thing auditors keep, and breaking it
	// to tidy a parameter name is not worth it.
	since, ok := firstTime(c, timeParam, "since", "from")
	if !ok {
		return filter, false
	}
	until, ok := firstTime(c, timeParam, "until", "to")
	if !ok {
		return filter, false
	}
	filter.Since = since
	filter.Until = until

	// A quick range is resolved here rather than in the browser so that "the last
	// hour" means the same window to the row count, the page and any client that
	// is not the console. An explicit `from` wins, since it is the more specific
	// statement of the two.
	if filter.Since == nil {
		if window, ok := rangeWindow(c); !ok {
			return filter, false
		} else if window > 0 {
			since := time.Now().UTC().Add(-window)
			filter.Since = &since
		}
	}

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

// listParam reads a repeatable, comma-separated query parameter into a
// deduplicated lowercase set. Verbs are the only thing it is used for and they
// are a closed vocabulary, so an unrecognised one is dropped rather than
// refused — a stale bookmark naming a verb this build no longer produces should
// narrow to nothing, not fail.
func listParam(c *gin.Context, name string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, raw := range c.QueryArray(name) {
		for _, part := range strings.Split(raw, ",") {
			part = strings.ToLower(strings.TrimSpace(part))
			if part == "" || seen[part] {
				continue
			}
			seen[part] = true
			out = append(out, part)
		}
	}
	return out
}

// firstTime reads whichever of the given parameter names is present, preferring
// the earlier one.
func firstTime(
	c *gin.Context, parse func(string) (*time.Time, bool), names ...string,
) (*time.Time, bool) {
	for _, name := range names {
		if strings.TrimSpace(c.Query(name)) == "" {
			continue
		}
		return parse(name)
	}
	return nil, true
}

// auditRanges are the presets the audit page leads with. They are a fixed table
// rather than a parsed duration so the set the UI offers and the set the API
// accepts cannot drift apart, and so no caller can ask for a window wide enough
// to be a table scan dressed as a filter.
var auditRanges = map[string]time.Duration{
	"15m": 15 * time.Minute,
	"1h":  time.Hour,
	"6h":  6 * time.Hour,
	"24h": 24 * time.Hour,
	"7d":  7 * 24 * time.Hour,
	"30d": 30 * 24 * time.Hour,
}

// rangeWindow resolves the `range` parameter. Zero with ok means "no preset",
// which includes the explicit "all" the UI sends when a preset is cleared.
func rangeWindow(c *gin.Context) (time.Duration, bool) {
	raw := strings.ToLower(strings.TrimSpace(c.Query("range")))
	if raw == "" || raw == "all" {
		return 0, true
	}
	window, known := auditRanges[raw]
	if !known {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "range must be one of 15m, 1h, 6h, 24h, 7d, 30d or all",
		})
		return 0, false
	}
	return window, true
}
