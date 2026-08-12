package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/bastion"
	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * Acknowledging a workload security posture finding.
 *
 * A workload that trips a rule deliberately — a debug pod running privileged on
 * purpose, a DaemonSet that has to be hostNetwork to do its job — needs a way to
 * say so, or the posture list becomes noise nobody reads a second time. This is
 * that way: it does not delete the finding (the scan still reports it on every
 * read) and it does not require fixing anything on the cluster. It records, in
 * KubeMG's own database, that a specific person reviewed a specific finding on a
 * specific object and stood behind it, with a reason — see
 * db.PostureAcknowledgement for why the identity is a full object/rule key
 * rather than a rule id of KubeMG's own.
 *
 * Two decisions here are worth being explicit about:
 *
 *   - **Who may acknowledge.** Reading the whole posture scan needs only the
 *     grant every other resource list needs — a `view` grant answers "what is
 *     wrong here" exactly as fully as an `edit` one. Silencing a finding is a
 *     different question: it is a security control, and a control anyone with
 *     read access can switch off is not a control. requirePostureWriteGrant
 *     refuses a `view` grant here on that basis, mirroring the bar every actual
 *     cluster write (scale, restart, the manifest editor's PUT) already clears
 *     before it reaches the tunnel — even though this write never touches the
 *     tunnel at all.
 *   - **The audit trail.** There is no generic "audit this admin write" path in
 *     this codebase (guardrail policy CRUD, cluster consoles, and cluster
 *     create/delete are none of them audited today) — the one audit sink that
 *     exists, bastion.Auditor, is fed by impersonated cluster calls and by two
 *     precedents for a non-proxy write reusing it anyway: pkg/jit's own
 *     decisions, and a terminal recording's viewers. An acknowledgement is
 *     exactly that shape of write — no cluster call, but a decision worth a
 *     trail — so it follows the same precedent: s.auditor.Record with a
 *     synthetic bastion.Event, the reason carried in Error (the one free-text
 *     field the audit row has, which is a repurposing worth naming rather than
 *     leaving silent).
 */

// maxAckReasonLength bounds the reason text. It is prose an auditor will read,
// not a manifest field, so the bound is generous rather than tight.
const maxAckReasonLength = 2000

// postureAckRequest is the acknowledgement as the console submits it.
type postureAckRequest struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Rule      string `json:"rule"`
	Reason    string `json:"reason"`
}

// requirePostureWriteGrant gates who may acknowledge (or unacknowledge) a
// finding. See the file header: a `view` grant may read every finding a
// scan produces and may not silence any of them.
func (s *server) requirePostureWriteGrant(c *gin.Context, grant db.UserClusterAccess) bool {
	if grant.K8sRole == db.K8sRoleView {
		c.JSON(http.StatusForbidden, gin.H{
			"error": "acknowledging a posture finding needs edit access to this cluster, not just view",
		})
		return false
	}
	return true
}

// recordPostureAudit writes one audit row for an acknowledgement write, on the
// precedent pkg/jit and the terminal-session-recording viewer already
// establish for a non-proxy write reusing the cluster audit trail. A server
// run with no auditor configured (s.auditor == nil, exactly as elsewhere in
// this codebase) simply records nothing rather than failing the write over it.
func (s *server) recordPostureAudit(
	c *gin.Context, user *db.User, cluster *db.Cluster, verb, namespace, kind, name, rule, reason string,
) {
	if s.auditor == nil {
		return
	}
	s.auditor.Record(c.Request.Context(), bastion.Event{
		At:        time.Now().UTC(),
		UserID:    user.ID,
		Username:  user.Username,
		ClusterID: cluster.ID,
		Cluster:   cluster.Name,
		Verb:      verb,
		Method:    c.Request.Method,
		Path:      c.Request.URL.Path,
		Namespace: namespace,
		Resource:  "security-posture/" + kind + "/" + name + "/" + rule,
		Status:    http.StatusOK,
		// Error is the one free-text field an audit row carries. Nothing here
		// failed; this is the acknowledgement's own reason, on the same
		// pragmatic repurposing this file's header explains.
		Error: reason,
	})
}

// acknowledgePostureFinding marks one finding, on one object in this cluster,
// as reviewed and accepted.
func (s *server) acknowledgePostureFinding(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	if !s.requirePostureWriteGrant(c, grant) {
		return
	}

	var req postureAckRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "the acknowledgement could not be read"})
		return
	}
	req.Kind = strings.TrimSpace(req.Kind)
	req.Namespace = strings.TrimSpace(req.Namespace)
	req.Name = strings.TrimSpace(req.Name)
	req.Rule = strings.TrimSpace(req.Rule)
	req.Reason = strings.TrimSpace(req.Reason)

	if req.Kind == "" || req.Name == "" || req.Rule == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "kind, name and rule are all required"})
		return
	}
	if _, known := postureRules[postureRuleID(req.Rule)]; !known {
		c.JSON(http.StatusBadRequest, gin.H{"error": "that is not one of the seven posture rules"})
		return
	}
	if req.Reason == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "a reason is required — an acknowledgement with no reason is a mute button, " +
				"not an audit-able decision",
		})
		return
	}
	if len(req.Reason) > maxAckReasonLength {
		c.JSON(http.StatusBadRequest, gin.H{"error": "that reason is too long"})
		return
	}
	if req.Namespace != "" {
		if _, resolved := s.scopedNamespace(c, grant, req.Namespace); !resolved {
			return
		}
	}

	ack := db.PostureAcknowledgement{
		ClusterID: cluster.ID,
		Kind:      req.Kind,
		Namespace: req.Namespace,
		Name:      req.Name,
		Rule:      req.Rule,
		Reason:    req.Reason,
		AckedByID: user.ID,
		AckedBy:   user.Username,
	}
	if err := s.store.AcknowledgePostureFinding(c.Request.Context(), &ack); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not save the acknowledgement"})
		return
	}

	s.recordPostureAudit(c, user, cluster, "acknowledge", req.Namespace, req.Kind, req.Name, req.Rule, req.Reason)
	c.JSON(http.StatusOK, ack)
}

// unacknowledgePostureFinding removes an acknowledgement, which puts the
// finding it covered back into the plain, unreviewed list on the next scan.
func (s *server) unacknowledgePostureFinding(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	if !s.requirePostureWriteGrant(c, grant) {
		return
	}

	kind := strings.TrimSpace(c.Query("kind"))
	namespace := strings.TrimSpace(c.Query("namespace"))
	name := strings.TrimSpace(c.Query("name"))
	rule := strings.TrimSpace(c.Query("rule"))
	if kind == "" || name == "" || rule == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "kind, name and rule are all required"})
		return
	}

	if err := s.store.UnacknowledgePostureFinding(c.Request.Context(), cluster.ID, kind, namespace, name, rule); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "there is no acknowledgement to remove"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not remove the acknowledgement"})
		return
	}

	s.recordPostureAudit(c, user, cluster, "unacknowledge", namespace, kind, name, rule, "")
	c.Status(http.StatusNoContent)
}
