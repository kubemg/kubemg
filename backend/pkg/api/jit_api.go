package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/db"
	"github.com/kubemg/kubemg/backend/pkg/jit"
)

/*
 * The just-in-time access surface.
 *
 * Who may see what here follows the audit trail's rule rather than the
 * permissions matrix's, and for the same reason: a request carries somebody's
 * stated justification for needing production, which is their business and their
 * approvers'. So listing is open to everyone and a non-admin is silently narrowed
 * to their own requests — the query parameter cannot widen that, exactly as it
 * cannot on /audit.
 *
 * Deciding is administrative, with two exceptions that are not concessions:
 * a requester may reject their own pending request, which is what cancelling is,
 * and may revoke their own live one, because handing privilege back must never
 * need permission. Everything else about who may decide lives in pkg/jit, where
 * the workflow is, so that the console and the webhook callback cannot disagree
 * about it.
 */

// jitCallbackRoute is the callback path relative to /api/v1. It is derived from
// the absolute path pkg/jit renders into a notification, so the route served and
// the URL published cannot drift apart.
var jitCallbackRoute = strings.TrimPrefix(jit.CallbackPath(), "/api/v1")

type jitRequestResponse struct {
	ID string `json:"id"`

	RequesterID       uint   `json:"requester_id"`
	RequesterUsername string `json:"requester_username"`

	ClusterID   uint   `json:"cluster_id"`
	ClusterName string `json:"cluster_name"`

	RequestedRole   string   `json:"requested_role"`
	Namespaces      []string `json:"namespaces"`
	DurationMinutes int      `json:"duration_minutes"`
	Reason          string   `json:"reason"`
	Status          string   `json:"status"`

	ApproverID       uint       `json:"approver_id,omitempty"`
	ApproverUsername string     `json:"approver_username,omitempty"`
	ApproverComment  string     `json:"approver_comment,omitempty"`
	ApprovedAt       *time.Time `json:"approved_at,omitempty"`
	ExpiresAt        *time.Time `json:"expires_at,omitempty"`

	// Active and RemainingSeconds are resolved here rather than left to the
	// browser, because the countdown has to agree with the server that will refuse
	// the call. A row that still says "active" past its expiry reads as inactive
	// with zero left, which is what the resolver already believes.
	Active           bool  `json:"active"`
	RemainingSeconds int64 `json:"remaining_seconds"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func toJitRequestResponse(request db.JitRequest, now time.Time) jitRequestResponse {
	return jitRequestResponse{
		ID:                request.ID,
		RequesterID:       request.RequesterID,
		RequesterUsername: request.RequesterUsername,
		ClusterID:         request.ClusterID,
		ClusterName:       request.ClusterName,
		RequestedRole:     request.RequestedRole,
		Namespaces:        request.NamespaceList(),
		DurationMinutes:   request.DurationMinutes,
		Reason:            request.Reason,
		Status:            request.Status,
		ApproverID:        request.ApproverID,
		ApproverUsername:  request.ApproverUsername,
		ApproverComment:   request.ApproverComment,
		ApprovedAt:        request.ApprovedAt,
		ExpiresAt:         request.ExpiresAt,
		Active:            request.Live(now),
		RemainingSeconds:  request.RemainingSeconds(now),
		CreatedAt:         request.CreatedAt,
		UpdatedAt:         request.UpdatedAt,
	}
}

type createJitRequestBody struct {
	ClusterID       uint     `json:"cluster_id"`
	RequestedRole   string   `json:"requested_role"`
	Namespaces      []string `json:"namespaces"`
	DurationMinutes int      `json:"duration_minutes"`
	Reason          string   `json:"reason"`
}

type jitDecisionBody struct {
	Comment string `json:"comment"`
}

// createJitRequest submits a request for time-bound elevated access.
func (s *server) createJitRequest(c *gin.Context) {
	user, ok := s.currentUser(c)
	if !ok {
		return
	}

	var body createJitRequestBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	request, err := s.jit.CreateRequest(c.Request.Context(), jit.Input{
		Requester:       user,
		ClusterID:       body.ClusterID,
		Role:            body.RequestedRole,
		Namespaces:      body.Namespaces,
		DurationMinutes: body.DurationMinutes,
		Reason:          body.Reason,
		Method:          c.Request.Method,
		Path:            c.Request.URL.Path,
	})
	if err != nil {
		s.writeJitError(c, err, "could not submit the access request")
		return
	}

	c.JSON(http.StatusCreated, toJitRequestResponse(*request, s.jit.Now()))
}

// listJitRequests returns the requests this caller may see.
func (s *server) listJitRequests(c *gin.Context) {
	user, ok := s.currentUser(c)
	if !ok {
		return
	}

	filter := db.JitRequestFilter{}
	// Every status named in the query, dropping anything this build does not know:
	// a stale bookmark should narrow to nothing rather than fail, which is how the
	// audit filters behave.
	for _, raw := range c.QueryArray("status") {
		for _, part := range strings.Split(raw, ",") {
			status := strings.ToLower(strings.TrimSpace(part))
			if db.ValidJitStatus(status) && !slices.Contains(filter.Statuses, status) {
				filter.Statuses = append(filter.Statuses, status)
			}
		}
	}
	if raw := strings.TrimSpace(c.Query("cluster_id")); raw != "" {
		id, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "cluster_id must be a number"})
			return
		}
		filter.ClusterID = uint(id)
	}
	if raw := strings.TrimSpace(c.Query("user_id")); raw != "" {
		id, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "user_id must be a number"})
			return
		}
		filter.RequesterID = uint(id)
	}
	// The narrowing, applied after the parameter rather than instead of it: a
	// non-admin asking about somebody else is answered with their own requests, not
	// with an error that confirms the other account exists.
	if !user.IsAdmin() {
		filter.RequesterID = user.ID
	}

	requests, err := s.jit.List(c.Request.Context(), filter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not read the access requests"})
		return
	}

	// The engine's clock, not this handler's: the countdown a browser draws has to
	// be the same reading the server will refuse the call on.
	now := s.jit.Now()
	out := make([]jitRequestResponse, 0, len(requests))
	pending := 0
	for _, request := range requests {
		if request.Status == db.JitStatusPending {
			pending++
		}
		out = append(out, toJitRequestResponse(request, now))
	}

	c.JSON(http.StatusOK, gin.H{
		"requests": out,
		"pending":  pending,
		// What the client may offer, from the server that will enforce it: a form
		// offering a duration the API would refuse is a form that fails on submit.
		"durations": db.JitDurationChoices,
		"statuses":  db.JitStatuses,
		"roles":     []string{db.K8sRoleView, db.K8sRoleEdit, db.K8sRoleClusterAdmin},
		// Whether this caller can decide anything. The console needs it to draw the
		// inbox at all, and it is the server's answer rather than a role check
		// repeated in the browser.
		"can_approve":  user.IsAdmin(),
		"scoped_to_me": !user.IsAdmin(),
	})
}

// approveJitRequest grants the window (admin only, and never one's own).
func (s *server) approveJitRequest(c *gin.Context) {
	s.decideJitRequest(c, s.jit.ApproveRequest, "could not approve the access request")
}

// rejectJitRequest refuses a pending request. Admin, or the requester cancelling.
func (s *server) rejectJitRequest(c *gin.Context) {
	s.decideJitRequest(c, s.jit.RejectRequest, "could not reject the access request")
}

// revokeJitRequest ends a live elevation. Admin, or the holder handing it back.
func (s *server) revokeJitRequest(c *gin.Context) {
	s.decideJitRequest(c, s.jit.RevokeRequest, "could not revoke the access grant")
}

// decideJitRequest is the plumbing behind approve, reject and revoke: read the
// caller, read the comment, hand both to the workflow, and drop the cluster's
// cached reads if the answer changed what the caller may see.
//
// The three differ only in which workflow function applies, and every rule about
// who may do it lives in there — so this deliberately checks nothing beyond the
// session.
func (s *server) decideJitRequest(
	c *gin.Context,
	apply func(ctx context.Context, id string, decision jit.Decision) (*db.JitRequest, error),
	failure string,
) {
	user, ok := s.currentUser(c)
	if !ok {
		return
	}

	var body jitDecisionBody
	// A decision with no body is legal — a comment is optional — so a bind failure
	// is only an error when something was actually sent.
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}

	request, err := apply(c.Request.Context(), strings.TrimSpace(c.Param("id")), jit.Decision{
		Actor:   user,
		Comment: body.Comment,
		Method:  c.Request.Method,
		Path:    c.Request.URL.Path,
	})
	if err != nil {
		s.writeJitError(c, err, failure)
		return
	}

	s.invalidateClusterReads(request.ClusterID)
	c.JSON(http.StatusOK, toJitRequestResponse(*request, s.jit.Now()))
}

/* ------------------------------------------------------- webhook callback --- */

// slackInteraction is the part of a Slack interactivity payload this needs: which
// button was pressed, and who pressed it.
type slackInteraction struct {
	User struct {
		Username string `json:"username"`
		Name     string `json:"name"`
	} `json:"user"`
	Actions []struct {
		ActionID string `json:"action_id"`
		Value    string `json:"value"`
	} `json:"actions"`
}

type jitCallbackBody struct {
	Token  string `json:"token"`
	Action string `json:"action"`
	// ApproverUsername is the KubeMG account the decision is recorded against. It
	// is required, because a chat webhook is not an identity provider and an
	// approval attributed to "slack" is a record that answers nothing.
	ApproverUsername string `json:"approver_username"`
	Comment          string `json:"comment"`
}

// jitWebhookCallback applies a decision that arrived from a chat integration.
//
// It is outside the JWT middleware by necessity — a Slack app carries no KubeMG
// session — so it authenticates on two things at once, and needs both:
//
//   - the **signed action token** from the notification, which proves the decision
//     is about a request KubeMG itself published and has not expired; and
//   - a **KubeMG identity** that resolves to an active administrator who is not the
//     requester, which is what makes the audit record true.
//
// The token alone would let anyone who read a Slack thread grant production
// access. The identity alone would let anyone who guessed a username do it. The
// self-approval rule is enforced in the workflow, so it applies here unchanged.
func (s *server) jitWebhookCallback(c *gin.Context) {
	body, ok := s.readJitCallback(c)
	if !ok {
		return
	}

	action, err := jit.ParseAction(s.jitCallbackSecret, body.Token, s.jit.Now())
	if errors.Is(err, jit.ErrTokenExpired) {
		c.JSON(http.StatusForbidden, gin.H{
			"error": "that approval link has expired; decide it in KubeMG instead",
		})
		return
	}
	if err != nil {
		// Deliberately the same answer for a forged token and a token this server
		// cannot verify at all: which of the two it is, is not the caller's business.
		c.JSON(http.StatusForbidden, gin.H{"error": "that approval token is not valid"})
		return
	}
	// A caller may name an action, but the token is what decides: a payload saying
	// "approve" with a reject token must never approve.
	if body.Action != "" && !strings.EqualFold(body.Action, action.Action) {
		c.JSON(http.StatusForbidden, gin.H{"error": "that token does not authorise this action"})
		return
	}

	approver, err := s.store.UserByUsername(c.Request.Context(), body.ApproverUsername)
	if errors.Is(err, db.ErrNotFound) {
		c.JSON(http.StatusForbidden, gin.H{
			"error": "no KubeMG account matches that user; decide this request in KubeMG",
		})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not resolve the approver"})
		return
	}
	if !approver.IsActive {
		c.JSON(http.StatusForbidden, gin.H{"error": "that account is disabled"})
		return
	}
	approver.Normalize()

	decision := jit.Decision{
		Actor:   approver,
		Comment: body.Comment,
		Via:     "webhook",
		Method:  c.Request.Method,
		Path:    c.Request.URL.Path,
	}

	var request *db.JitRequest
	if action.Action == jit.ActionApprove {
		request, err = s.jit.ApproveRequest(c.Request.Context(), action.RequestID, decision)
	} else {
		request, err = s.jit.RejectRequest(c.Request.Context(), action.RequestID, decision)
	}
	if err != nil {
		s.writeJitError(c, err, "could not apply that decision")
		return
	}

	s.invalidateClusterReads(request.ClusterID)
	// A body Slack renders as a confirmation in the thread, and anything else can
	// read as JSON.
	c.JSON(http.StatusOK, gin.H{
		"text":    "KubeMG: access request " + request.Status + " by " + approver.Username,
		"request": toJitRequestResponse(*request, s.jit.Now()),
	})
}

// readJitCallback accepts the two shapes a decision arrives in: KubeMG's own JSON,
// and Slack's form-encoded `payload`. Slack's shape is handled here rather than
// asked of the operator because it is not configurable — an app posts what it
// posts.
func (s *server) readJitCallback(c *gin.Context) (jitCallbackBody, bool) {
	if raw := c.PostForm("payload"); raw != "" {
		var interaction slackInteraction
		if err := json.Unmarshal([]byte(raw), &interaction); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "could not read that interaction payload"})
			return jitCallbackBody{}, false
		}
		if len(interaction.Actions) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "that interaction carried no action"})
			return jitCallbackBody{}, false
		}
		username := interaction.User.Username
		if username == "" {
			username = interaction.User.Name
		}
		return jitCallbackBody{
			Token:            interaction.Actions[0].Value,
			ApproverUsername: username,
			Comment:          "",
		}, true
	}

	var body jitCallbackBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return jitCallbackBody{}, false
	}
	body.Token = strings.TrimSpace(body.Token)
	body.ApproverUsername = strings.TrimSpace(body.ApproverUsername)
	if body.Token == "" || body.ApproverUsername == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "a signed token and the approver's KubeMG username are both required",
		})
		return jitCallbackBody{}, false
	}
	return body, true
}

/* ----------------------------------------------------------------- shared --- */

// writeJitError maps a workflow error onto a status code. The workflow's own
// messages are passed through: they say which rule was hit — "you cannot approve
// your own access request" — and replacing them with a generic refusal would leave
// the caller with nothing to act on.
func (s *server) writeJitError(c *gin.Context, err error, fallback string) {
	switch {
	case errors.Is(err, jit.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "that access request does not exist"})
	case errors.Is(err, jit.ErrInvalid):
		c.JSON(http.StatusBadRequest, gin.H{"error": jitMessage(err)})
	case errors.Is(err, jit.ErrConflict):
		c.JSON(http.StatusConflict, gin.H{"error": jitMessage(err)})
	case errors.Is(err, jit.ErrForbidden):
		c.JSON(http.StatusForbidden, gin.H{"error": jitMessage(err)})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": fallback})
	}
}

// jitMessage strips the sentinel's own prefix, so a caller reads "duration must be
// between…" rather than "invalid request: duration must be between…".
func jitMessage(err error) string {
	message := err.Error()
	if _, rest, found := strings.Cut(message, ": "); found {
		return rest
	}
	return message
}

// invalidateClusterReads drops the cached answers for one cluster after a grant
// changed. Without it an elevation that was just approved would be invisible for
// up to a cache TTL — the authorization itself is read per request, so this is
// about the console showing what the caller can now reach, not about enforcement.
func (s *server) invalidateClusterReads(clusterID uint) {
	if s.reads == nil {
		return
	}
	s.reads.InvalidateScope("cluster:" + strconv.FormatUint(uint64(clusterID), 10))
}
