package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/db"
	"github.com/kubemg/kubemg/backend/pkg/guardrails"
)

/*
 * Configuring the command guardrails.
 *
 * The whole surface is administrative, and for a stronger reason than most of
 * this file: these rules decide what the platform refuses to do for people who
 * are otherwise entitled to it. Writing one is delegating a refusal to a regular
 * expression, and getting it wrong takes a capability away from an entire fleet.
 *
 * The read is administrative too, which is a departure from how the audit trail
 * is treated. The reasoning is thin either way — a rule is not a credential, and
 * arguably the person who meets one should be able to look it up. But the whole
 * set, read at once, is a map of exactly which spellings are watched for, and
 * that is more useful to somebody working around the rules than to somebody
 * obeying them. The person who hits one is told which rule fired, by name, at the
 * moment it fires; that is the disclosure that actually matters.
 */

const (
	maxGuardrailNameLength        = 120
	maxGuardrailDescriptionLength = 1000
)

type guardrailPolicyResponse struct {
	ID          uint   `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// ClusterID is 0 for a fleet-wide rule. ClusterName is filled in for a
	// cluster-scoped one so the list does not need a second lookup to render a
	// badge — and says so plainly when the cluster is gone.
	ClusterID   uint   `json:"cluster_id"`
	ClusterName string `json:"cluster_name,omitempty"`
	Pattern     string `json:"pattern"`
	Target      string `json:"target"`
	Action      string `json:"action"`
	Enabled     bool   `json:"enabled"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type guardrailPolicyRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	ClusterID   uint   `json:"cluster_id"`
	Pattern     string `json:"pattern"`
	Target      string `json:"target"`
	Action      string `json:"action"`
	// Enabled defaults to true on create — a rule somebody just wrote is one they
	// want in force — and is honoured as sent on an update.
	Enabled *bool `json:"enabled"`
}

func toGuardrailPolicyResponse(policy db.GuardrailPolicy, clusterName string) guardrailPolicyResponse {
	return guardrailPolicyResponse{
		ID:          policy.ID,
		Name:        policy.Name,
		Description: policy.Description,
		ClusterID:   policy.ClusterID,
		ClusterName: clusterName,
		Pattern:     policy.Pattern,
		Target:      policy.Target,
		Action:      policy.Action,
		Enabled:     policy.Enabled,
		CreatedAt:   policy.CreatedAt,
		UpdatedAt:   policy.UpdatedAt,
	}
}

// listGuardrailPolicies returns the rule set, optionally narrowed to one scope
// (admin only).
//
// `?cluster_id=0` is the fleet-wide set and is deliberately distinguishable from
// the parameter being absent, which is everything: "show me only the global
// rules" is a real question, and one a falsy-means-unset reading could not ask.
func (s *server) listGuardrailPolicies(c *gin.Context) {
	policies, err := s.store.ListGuardrailPolicies(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not read the guardrail policies"})
		return
	}

	if raw, ok := c.GetQuery("cluster_id"); ok {
		wanted, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "cluster_id must be a number"})
			return
		}
		policies = slices.DeleteFunc(policies, func(policy db.GuardrailPolicy) bool {
			return policy.ClusterID != uint(wanted)
		})
	}

	names := s.clusterNames(c.Request.Context())
	out := make([]guardrailPolicyResponse, 0, len(policies))
	for _, policy := range policies {
		out = append(out, toGuardrailPolicyResponse(policy, names[policy.ClusterID]))
	}

	// What the gateway is actually enforcing, as opposed to what is stored. They
	// can differ — a rule whose pattern stopped compiling is skipped — and an
	// operator reading a list of armed rules deserves to know the count the
	// gateway agrees with.
	c.JSON(http.StatusOK, gin.H{
		"policies":  out,
		"targets":   db.GuardrailTargets,
		"actions":   db.GuardrailActions,
		"enforcing": s.guardrails.Snapshot().Rules(),
	})
}

// guardrailTemplates returns the preset catalogue (admin only).
func (s *server) guardrailTemplates(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"templates": db.GuardrailTemplates})
}

// createGuardrailPolicy stores a new rule (admin only).
func (s *server) createGuardrailPolicy(c *gin.Context) {
	var req guardrailPolicyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	policy, ok := s.guardrailFrom(c, req, nil)
	if !ok {
		return
	}
	if err := s.store.CreateGuardrailPolicy(c.Request.Context(), policy); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not save the guardrail policy"})
		return
	}

	s.publishGuardrails(c.Request.Context())
	c.JSON(http.StatusCreated, toGuardrailPolicyResponse(*policy, s.clusterName(c.Request.Context(), policy.ClusterID)))
}

// updateGuardrailPolicy replaces a rule (admin only).
func (s *server) updateGuardrailPolicy(c *gin.Context) {
	id, ok := guardrailID(c)
	if !ok {
		return
	}

	existing, err := s.store.GuardrailPolicyByID(c.Request.Context(), id)
	if errors.Is(err, db.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "guardrail policy not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load the guardrail policy"})
		return
	}

	var req guardrailPolicyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	policy, ok := s.guardrailFrom(c, req, existing)
	if !ok {
		return
	}
	if err := s.store.UpdateGuardrailPolicy(c.Request.Context(), policy); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "guardrail policy not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not save the guardrail policy"})
		return
	}

	s.publishGuardrails(c.Request.Context())
	c.JSON(http.StatusOK, toGuardrailPolicyResponse(*policy, s.clusterName(c.Request.Context(), policy.ClusterID)))
}

// deleteGuardrailPolicy removes a rule (admin only).
func (s *server) deleteGuardrailPolicy(c *gin.Context) {
	id, ok := guardrailID(c)
	if !ok {
		return
	}

	if err := s.store.DeleteGuardrailPolicy(c.Request.Context(), id); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "guardrail policy not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not delete the guardrail policy"})
		return
	}

	s.publishGuardrails(c.Request.Context())
	c.Status(http.StatusNoContent)
}

// guardrailFrom validates a submitted rule. On an update, existing carries what
// is stored so an omitted Enabled keeps its current value rather than silently
// arming a rule that was off.
func (s *server) guardrailFrom(
	c *gin.Context, req guardrailPolicyRequest, existing *db.GuardrailPolicy,
) (*db.GuardrailPolicy, bool) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "a name is required"})
		return nil, false
	}
	if len(name) > maxGuardrailNameLength {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "the name is too long; " + strconv.Itoa(maxGuardrailNameLength) + " characters at most",
		})
		return nil, false
	}

	description := strings.TrimSpace(req.Description)
	if len(description) > maxGuardrailDescriptionLength {
		c.JSON(http.StatusBadRequest, gin.H{"error": "the description is too long"})
		return nil, false
	}

	// The pattern is checked here rather than only at compile time so an operator
	// finds out at the form. A rule stored with a pattern that does not compile is
	// a rule that looks armed in the list and enforces nothing.
	pattern := strings.TrimSpace(req.Pattern)
	if err := guardrails.ValidatePattern(pattern); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return nil, false
	}

	target := strings.TrimSpace(req.Target)
	if target == "" {
		target = db.GuardrailTargetBoth
	}
	if !slices.Contains(db.GuardrailTargets, target) {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "target must be one of " + strings.Join(db.GuardrailTargets, ", "),
		})
		return nil, false
	}

	action := strings.TrimSpace(req.Action)
	if action == "" {
		action = db.GuardrailActionBlock
	}
	if !slices.Contains(db.GuardrailActions, action) {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "action must be one of " + strings.Join(db.GuardrailActions, ", "),
		})
		return nil, false
	}

	// A rule naming a cluster that does not exist enforces nothing and reads in
	// the list as though it does, which is the worst of both.
	if req.ClusterID != 0 {
		if _, err := s.store.ClusterByID(c.Request.Context(), req.ClusterID); err != nil {
			if errors.Is(err, db.ErrNotFound) {
				c.JSON(http.StatusBadRequest, gin.H{"error": "that cluster does not exist"})
				return nil, false
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load the cluster"})
			return nil, false
		}
	}

	enabled := true
	if existing != nil {
		enabled = existing.Enabled
	}
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	policy := &db.GuardrailPolicy{
		Name:        name,
		Description: description,
		ClusterID:   req.ClusterID,
		Pattern:     pattern,
		Target:      target,
		Action:      action,
		Enabled:     enabled,
	}
	if existing != nil {
		policy.ID = existing.ID
		policy.CreatedAt = existing.CreatedAt
	}
	return policy, true
}

// publishGuardrails compiles the stored rules and hands them to the gateway.
//
// It is called at boot, after every write, and on a timer — the timer is what
// makes a second replica pick up a rule one of its siblings saved, and it matters
// more here than for the audit policy: a replica enforcing a rule set somebody
// deleted refuses calls for a reason no longer visible anywhere.
//
// A read failure leaves the previous set in force rather than clearing it.
// Dropping every rule because the database blinked would turn a transient outage
// into an unguarded fleet.
func (s *server) publishGuardrails(ctx context.Context) {
	if s.guardrails == nil {
		return
	}

	policies, err := s.store.ListGuardrailPolicies(ctx)
	if err != nil {
		s.log().Warn("could not refresh the guardrail policies", slog.String("error", err.Error()))
		return
	}

	snapshot, problems := guardrails.Compile(policies)
	for _, problem := range problems {
		// Loud, and repeated on every publish: the rule is in the list looking
		// armed, and the only place its silence is visible is here.
		s.log().Error("guardrail policy skipped", slog.String("error", problem.Error()))
	}
	s.guardrails.Store(snapshot)
}

// startGuardrailRefresher republishes the rule set on a timer.
func (s *server) startGuardrailRefresher(ctx context.Context) {
	s.publishGuardrails(ctx)

	ticker := time.NewTicker(guardrailRefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.publishGuardrails(ctx)
		}
	}
}

// guardrailRefreshInterval is how long a replica may lag a change made on
// another one. A guardrail is edited a few times a year and read on every call,
// so this is about eventual agreement between replicas, not latency.
const guardrailRefreshInterval = 30 * time.Second

// clusterNames maps ids to names for rendering scope badges. A failure is not
// worth refusing the list over: an unnamed cluster id is a worse response than a
// named one and a much better one than an error.
func (s *server) clusterNames(ctx context.Context) map[uint]string {
	clusters, err := s.store.Clusters(ctx)
	if err != nil {
		return map[uint]string{}
	}
	names := make(map[uint]string, len(clusters))
	for _, cluster := range clusters {
		names[cluster.ID] = cluster.Name
	}
	return names
}

func (s *server) clusterName(ctx context.Context, id uint) string {
	if id == 0 {
		return ""
	}
	cluster, err := s.store.ClusterByID(ctx, id)
	if err != nil {
		return ""
	}
	return cluster.Name
}

func guardrailID(c *gin.Context) (uint, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid guardrail policy id"})
		return 0, false
	}
	return uint(id), true
}
