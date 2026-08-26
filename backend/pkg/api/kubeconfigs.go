package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/kubemg/kubemg/backend/pkg/bastion"
	"github.com/kubemg/kubemg/backend/pkg/credentials"
	"github.com/kubemg/kubemg/backend/pkg/db"
)

// Audit verbs for a kubeconfig's own lifecycle. Issuing one is the act this
// product performs that hands somebody standing access to a cluster without
// touching the cluster, and until the register existed it was the only such act
// that left no trace at all.
const (
	verbKubeconfigIssue  = "kubeconfig-issue"
	verbKubeconfigRevoke = "kubeconfig-revoke"
)

// credentialRefreshInterval is how long a replica may lag a revocation made on
// another one. A revoke republishes on the replica that served it immediately;
// this tick is what carries the change to its siblings, and it is the guardrail
// refresher's interval for the same reason — the question is eventual agreement
// between replicas, not latency on the write.
const credentialRefreshInterval = 30 * time.Second

// kubeconfigResponse is one register row as a caller reads it back.
type kubeconfigResponse struct {
	ID             uint       `json:"id"`
	UserID         uint       `json:"user_id"`
	Username       string     `json:"username"`
	ClusterID      uint       `json:"cluster_id"`
	ClusterName    string     `json:"cluster_name"`
	ConnectionMode string     `json:"connection_mode"`
	Namespace      string     `json:"namespace,omitempty"`
	K8sRole        string     `json:"k8s_role,omitempty"`
	ServiceAccount string     `json:"service_account,omitempty"`
	IssuedBy       uint       `json:"issued_by,omitempty"`
	IssuedByName   string     `json:"issued_by_username,omitempty"`
	ExpiresAt      time.Time  `json:"expires_at"`
	RevokedAt      *time.Time `json:"revoked_at,omitempty"`
	RevokedByName  string     `json:"revoked_by_username,omitempty"`
	LastUsedAt     *time.Time `json:"last_used_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	// Status is the row's own reading — active, expired or revoked.
	Status string `json:"status"`
	// Revocable says whether the Revoke button on this row does anything, and
	// Explanation says why when it does not. The pair is the whole honesty of
	// this surface: a direct-mode credential is the cluster's token, and an
	// administrator who believes a revoke landed when it did not is in a worse
	// position than one who knows the file has four hours left.
	Revocable   bool   `json:"revocable"`
	Explanation string `json:"explanation,omitempty"`
}

// directModeExplanation is what a direct-mode row says instead of offering a
// button. It names the one lever that exists and states that the lever is
// all-or-nothing, because every one of that user's direct-mode kubeconfigs on
// that cluster is bound to the same service account.
func directModeExplanation(serviceAccount string) string {
	account := serviceAccount
	if account == "" {
		account = "the per-user service account"
	}
	return "This cluster is registered for direct API access, so the credential is a token the " +
		"cluster itself minted and KubeMG cannot withdraw it — it stays valid until it expires. The " +
		"only lever is on the cluster: deleting the " + account + " ServiceAccount invalidates every " +
		"token bound to it, which is all of this user's direct-mode kubeconfigs for this cluster."
}

func toKubeconfigResponse(row *db.KubeconfigIssuance, now time.Time) kubeconfigResponse {
	out := kubeconfigResponse{
		ID:             row.ID,
		UserID:         row.UserID,
		Username:       row.Username,
		ClusterID:      row.ClusterID,
		ClusterName:    row.ClusterName,
		ConnectionMode: row.ConnectionMode,
		Namespace:      row.Namespace,
		K8sRole:        row.K8sRole,
		ServiceAccount: row.ServiceAccount,
		IssuedBy:       row.IssuedBy,
		IssuedByName:   row.IssuedByUsername,
		ExpiresAt:      row.ExpiresAt,
		RevokedAt:      row.RevokedAt,
		RevokedByName:  row.RevokedByName,
		LastUsedAt:     row.LastUsedAt,
		CreatedAt:      row.CreatedAt,
		Status:         row.Status(now),
		Revocable:      row.RevocableHere() && row.Status(now) == "active",
	}
	if !row.RevocableHere() {
		out.Explanation = directModeExplanation(row.ServiceAccount)
	}
	return out
}

// recordKubeconfigIssuance writes the register row and the audit record, and is
// deliberately called from the generator itself rather than from a wrapper: a
// credential that is handed out without being written down is precisely the
// state this feature exists to end, and the only way to guarantee the two happen
// together is for them to be the same function call.
//
// A failure to record is logged and does not fail the issuance. That is the
// uncomfortable half of the decision and it is the right way round: the caller
// has already been authorized, and refusing to hand over a credential because a
// bookkeeping row would not write turns a database blip into an outage. What it
// must never do is fail silently, hence the log.
func (s *server) recordKubeconfigIssuance(
	c *gin.Context, issuance *db.KubeconfigIssuance, holder, issuer *db.User, cluster *db.Cluster,
) {
	ctx := c.Request.Context()
	if err := s.store.CreateKubeconfigIssuance(ctx, issuance); err != nil {
		s.log().Error("could not record an issued kubeconfig",
			slog.String("error", err.Error()),
			slog.String("username", holder.Username),
			slog.Uint64("cluster_id", uint64(cluster.ID)))
	}
	if s.auditor == nil {
		return
	}
	event := bastion.Event{
		At:        time.Now().UTC(),
		UserID:    issuer.ID,
		Username:  issuer.Username,
		ClusterID: cluster.ID,
		Cluster:   cluster.Name,
		Verb:      verbKubeconfigIssue,
		Method:    c.Request.Method,
		Path:      c.Request.URL.Path,
		Namespace: issuance.Namespace,
		Resource:  "kubeconfigs",
		Status:    http.StatusOK,
		// The identities are crossed the way the machine token's are: the record's
		// user is whoever asked, and the holder is named as the impersonated
		// identity, because an administrator generating a file for somebody else is
		// exactly the row an auditor is looking for and neither half says it alone.
		ImpersonatedUser: holder.Username,
	}
	s.auditor.Record(ctx, event)
}

// newIssuance builds the register row for a credential about to be returned.
// tokenID is the `jti` the gateway will match a revocation against; a
// direct-mode row gets one of its own that no credential carries, so that every
// read of this table has a single identity column to key on.
func newIssuance(
	holder, issuer *db.User,
	cluster *db.Cluster,
	tokenID, mode, namespace, k8sRole, serviceAccount string,
	expiresAt time.Time,
) *db.KubeconfigIssuance {
	if tokenID == "" {
		tokenID = uuid.NewString()
	}
	return &db.KubeconfigIssuance{
		TokenID:          tokenID,
		UserID:           holder.ID,
		Username:         holder.Username,
		ClusterID:        cluster.ID,
		ClusterName:      cluster.Name,
		ConnectionMode:   mode,
		Namespace:        namespace,
		K8sRole:          k8sRole,
		ServiceAccount:   serviceAccount,
		IssuedBy:         issuer.ID,
		IssuedByUsername: issuer.Username,
		ExpiresAt:        expiresAt.UTC(),
	}
}

// listKubeconfigs reads the register.
//
// It follows the audit trail's rule exactly: a non-admin is narrowed to their
// own rows by the handler, and the query parameter can only narrow that further
// — it can never widen it. Reading is open to everybody rather than
// administrative because revoking a file you know you lost must not require
// finding an administrator first, and you cannot revoke what you cannot see.
func (s *server) listKubeconfigs(c *gin.Context) {
	caller, ok := s.currentUser(c)
	if !ok {
		return
	}

	filter := db.KubeconfigFilter{Now: time.Now().UTC()}
	if raw := strings.TrimSpace(c.Query("user_id")); raw != "" {
		parsed, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "user_id must be an integer"})
			return
		}
		filter.UserID = uint(parsed)
	}
	if !caller.IsAdmin() {
		filter.UserID = caller.ID
	}
	if raw := strings.TrimSpace(c.Query("cluster_id")); raw != "" {
		parsed, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "cluster_id must be an integer"})
			return
		}
		filter.ClusterID = uint(parsed)
	}
	filter.ActiveOnly = c.Query("status") == "active"
	filter.Limit, filter.Offset = pageParams(c)

	rows, total, err := s.store.ListKubeconfigIssuances(c.Request.Context(), filter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not read the credential register"})
		return
	}

	now := time.Now().UTC()
	out := make([]kubeconfigResponse, 0, len(rows))
	for i := range rows {
		out = append(out, toKubeconfigResponse(&rows[i], now))
	}
	c.JSON(http.StatusOK, gin.H{"kubeconfigs": out, "total": total})
}

// pageParams reads the two paging query parameters, bounded the way the audit
// list bounds them.
func pageParams(c *gin.Context) (limit, offset int) {
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			limit = parsed
		}
	}
	if raw := strings.TrimSpace(c.Query("offset")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			offset = parsed
		}
	}
	return limit, offset
}

// revokeKubeconfig withdraws one credential.
//
// Revoking your own is never administrative — it is the whole point of the
// surface being visible to its holder — and revoking somebody else's always is.
func (s *server) revokeKubeconfig(c *gin.Context) {
	caller, ok := s.currentUser(c)
	if !ok {
		return
	}
	id, ok := parseIDParam(c, "id", "credential")
	if !ok {
		return
	}

	ctx := c.Request.Context()
	row, err := s.store.KubeconfigIssuanceByID(ctx, id)
	if errors.Is(err, db.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "credential not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not read the credential register"})
		return
	}
	if row.UserID != caller.ID && !caller.IsAdmin() {
		// Whose credential it is, is not something the address may disclose:
		// somebody else's answers as one that does not exist, the terminal
		// recording's rule.
		c.JSON(http.StatusNotFound, gin.H{"error": "credential not found"})
		return
	}
	// Direct mode is refused rather than served, and the refusal says why. A
	// button that reports success while the token keeps working for four hours
	// would be worse than no button at all.
	if !row.RevocableHere() {
		c.JSON(http.StatusConflict, gin.H{"error": directModeExplanation(row.ServiceAccount)})
		return
	}

	revoked, err := s.store.RevokeKubeconfigIssuance(ctx, id, time.Now().UTC(), caller.ID, caller.Username)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not revoke the credential"})
		return
	}

	// Republish before answering: a caller told the credential is withdrawn must
	// not be able to make a call with it afterwards on this replica.
	s.publishRevokedCredentials(ctx)
	s.recordKubeconfigRevocation(c, caller, []db.KubeconfigIssuance{*revoked})

	c.JSON(http.StatusOK, toKubeconfigResponse(revoked, time.Now().UTC()))
}

// revokeAllKubeconfigsRequest names whose credentials are being withdrawn. An
// absent user is the caller's own, which is what makes "I lost my laptop" a
// single call nobody needs permission for.
type revokeAllKubeconfigsRequest struct {
	UserID uint `json:"user_id"`
}

// revokeAllKubeconfigsResponse says what the action actually reached.
type revokeAllKubeconfigsResponse struct {
	// Revoked is how many credentials this stopped — agent-mode rows, which
	// stop on their next call.
	Revoked int `json:"revoked"`
	// StillValid is how many were withdrawn in the register but keep working,
	// which is every direct-mode row, and Clusters names the clusters they are
	// on so the cluster-side lever can actually be pulled.
	StillValid int      `json:"still_valid"`
	Clusters   []string `json:"clusters_not_reached,omitempty"`
	// Explanation is the sentence a console shows beside those numbers. Empty
	// when everything was genuinely stopped.
	Explanation string `json:"explanation,omitempty"`
}

// revokeAllKubeconfigs withdraws everything one person holds.
//
// It is its own action rather than N row writes because it is what an incident
// calls for, and it states which clusters it could actually reach rather than
// reporting a flat success: in agent mode the credential is KubeMG's and stops
// at once, and in direct mode it is the cluster's and does not stop at all.
func (s *server) revokeAllKubeconfigs(c *gin.Context) {
	caller, ok := s.currentUser(c)
	if !ok {
		return
	}

	var req revokeAllKubeconfigsRequest
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	target := req.UserID
	if target == 0 {
		target = caller.ID
	}
	if target != caller.ID && !caller.IsAdmin() {
		c.JSON(http.StatusForbidden, gin.H{
			"error": "only an administrator may revoke another account's credentials",
		})
		return
	}

	ctx := c.Request.Context()
	revoked, err := s.store.RevokeKubeconfigsForUser(ctx, target, time.Now().UTC(), caller.ID, caller.Username)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not revoke the credentials"})
		return
	}
	s.publishRevokedCredentials(ctx)
	s.recordKubeconfigRevocation(c, caller, revoked)

	c.JSON(http.StatusOK, summariseBlanketRevoke(revoked))
}

// summariseBlanketRevoke splits what was stopped from what was only recorded as
// stopped. The direct-mode half is disclosed rather than buried, because the
// administrator reading this is deciding whether the incident is over.
func summariseBlanketRevoke(revoked []db.KubeconfigIssuance) revokeAllKubeconfigsResponse {
	out := revokeAllKubeconfigsResponse{}
	unreached := map[string]bool{}
	for _, row := range revoked {
		if row.RevocableHere() {
			out.Revoked++
			continue
		}
		out.StillValid++
		name := row.ClusterName
		if name == "" {
			name = "an unnamed cluster"
		}
		unreached[name] = true
	}
	if out.StillValid == 0 {
		return out
	}
	for name := range unreached {
		out.Clusters = append(out.Clusters, name)
	}
	sort.Strings(out.Clusters)
	out.Explanation = "These clusters are registered for direct API access, so their credentials are " +
		"tokens the clusters themselves minted and KubeMG cannot withdraw them — each stays valid " +
		"until it expires. Delete this account's kubemg service account on those clusters to " +
		"invalidate them now."
	return out
}

// recordKubeconfigRevocation puts each withdrawal in the trail, one record per
// credential, with the identities crossed the way the issuance record crosses
// them — an administrator revoking somebody else's access is the row an auditor
// wants, and a blanket revoke that collapsed to one record would lose which
// clusters it touched.
func (s *server) recordKubeconfigRevocation(c *gin.Context, caller *db.User, rows []db.KubeconfigIssuance) {
	if s.auditor == nil {
		return
	}
	ctx := c.Request.Context()
	for _, row := range rows {
		event := bastion.Event{
			At:               time.Now().UTC(),
			UserID:           caller.ID,
			Username:         caller.Username,
			ClusterID:        row.ClusterID,
			Cluster:          row.ClusterName,
			Verb:             verbKubeconfigRevoke,
			Method:           c.Request.Method,
			Path:             c.Request.URL.Path,
			Namespace:        row.Namespace,
			Resource:         "kubeconfigs",
			Status:           http.StatusOK,
			ImpersonatedUser: row.Username,
		}
		// A revoke this console could not actually land is recorded as what it
		// was. The trail is the one place that difference must not be smoothed
		// over, since it is the record somebody reads back during the incident.
		if !row.RevocableHere() {
			event.Error = "credential is a direct-mode service account token and remains valid until it expires"
		}
		s.auditor.Record(ctx, event)
	}
}

// startCredentialRefresher keeps the published revocation set in step with the
// register. It publishes once immediately, so a restarted server honours the
// revocations it already holds from its first proxied call rather than from the
// first tick.
func (s *server) startCredentialRefresher(ctx context.Context) {
	ticker := time.NewTicker(credentialRefreshInterval)
	defer ticker.Stop()

	for {
		s.publishRevokedCredentials(ctx)

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// publishRevokedCredentials resolves the withdrawn set and hands it to the
// gateway.
//
// A read that fails leaves the previous snapshot in place and is logged. That
// is the fail-open rule stated as code: an unreadable register means no *new*
// revocations are known, never that every credential is refused — a database
// blip that locks a whole fleet out of kubectl is worse than one that briefly
// honours a withdrawn file, which is still expiring on its own clock.
func (s *server) publishRevokedCredentials(ctx context.Context) {
	if s.credentials == nil {
		return
	}
	ids, err := s.store.RevokedKubeconfigTokenIDs(ctx, time.Now().UTC())
	if err != nil {
		if ctx.Err() == nil {
			s.log().Warn("could not refresh the revoked credential set",
				slog.String("error", err.Error()))
		}
		return
	}
	s.credentials.Store(credentials.NewSnapshot(ids))
}

// credentialToucher is what the gateway calls to record a credential's use. It
// is installed on the register rather than reached through it, so pkg/credentials
// stays ignorant of persistence and a failure here can never fail the proxied
// call it was observing.
func (s *server) credentialToucher() credentials.Toucher {
	return func(ctx context.Context, tokenID string, at time.Time) {
		_ = s.store.TouchKubeconfigIssuance(ctx, tokenID, at)
	}
}
