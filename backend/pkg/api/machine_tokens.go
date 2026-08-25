package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/auth"
	"github.com/kubemg/kubemg/backend/pkg/bastion"
	"github.com/kubemg/kubemg/backend/pkg/db"
	"github.com/kubemg/kubemg/backend/pkg/k8s"
)

// Audit verbs for the credential's own lifecycle. They are KubeMG's own acts
// rather than proxied ones, recorded here the way replaying a recording is:
// issuing a long-lived credential for a production cluster is the most
// consequential thing this console does that never touches the cluster.
const (
	verbMachineTokenIssue  = "machine-token-issue"
	verbMachineTokenRevoke = "machine-token-revoke"
)

// defaultMachineTokenTTL is what an issuance with nothing said about time gets.
// A quarter is long enough that a pipeline is not re-credentialled every sprint
// and short enough that a forgotten one closes itself.
const defaultMachineTokenTTL = 90 * 24 * time.Hour

// maxMachineTokenTTL bounds a *bounded* token. It is not a policy about how long
// access may last — a token with no expiry at all is allowed below — but a guard
// against a number typed with an extra digit: past this, "expires" is a claim
// nobody in the room will be around to see tested.
const maxMachineTokenTTL = 10 * 365 * 24 * time.Hour

type issueMachineTokenRequest struct {
	Name      string `json:"name" binding:"required"`
	ClusterID uint   `json:"cluster_id" binding:"required"`
	Namespace string `json:"namespace"`
	// TTLSeconds is the window. Zero means "the default" rather than "forever",
	// because forever has to be asked for rather than fallen into.
	TTLSeconds int64 `json:"ttl_seconds"`
	// NeverExpires issues a credential with no expiry. It is a separate field
	// rather than a sentinel TTL so that a client which omits everything cannot
	// produce one by accident.
	NeverExpires bool `json:"never_expires"`
}

// machineTokenResponse is a token row as an administrator reads it back. The
// secret is never in it — see issueMachineTokenResponse, which carries it once.
type machineTokenResponse struct {
	ID          uint       `json:"id"`
	UserID      uint       `json:"user_id"`
	Name        string     `json:"name"`
	Hint        string     `json:"hint"`
	ClusterID   uint       `json:"cluster_id"`
	ClusterName string     `json:"cluster_name,omitempty"`
	Namespace   string     `json:"namespace,omitempty"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	RevokedAt   *time.Time `json:"revoked_at,omitempty"`
	LastUsedAt  *time.Time `json:"last_used_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	// Status is the row's own reading — active, expired or revoked — so a list
	// does not make every client re-derive it from three nullable columns.
	Status string `json:"status"`
}

type issueMachineTokenResponse struct {
	Token machineTokenResponse `json:"token"`
	// Secret is the credential itself, returned on this one response and never
	// again. Nothing here can show it a second time: what is stored is a hash.
	Secret string `json:"secret"`
	// Kubeconfig is the same credential rendered as a file, because that is the
	// form a CI job actually consumes.
	Kubeconfig string `json:"kubeconfig"`
	Filename   string `json:"filename"`
	Context    string `json:"context"`
	Server     string `json:"server"`
	K8sRole    string `json:"k8s_role"`
	// Warning states what is true about the credential and cannot be read off
	// the file: that it never expires, or that this server is not on TLS.
	Warning string `json:"warning,omitempty"`
}

// listMachineTokens returns one account's credentials (admin only).
func (s *server) listMachineTokens(c *gin.Context) {
	account, ok := s.loadMachineAccount(c)
	if !ok {
		return
	}

	ctx := c.Request.Context()
	tokens, err := s.store.ListMachineTokens(ctx, account.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not list tokens"})
		return
	}
	clusters, err := s.store.Clusters(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load clusters"})
		return
	}
	names := make(map[uint]string, len(clusters))
	for _, cluster := range clusters {
		names[cluster.ID] = cluster.Name
	}

	now := time.Now().UTC()
	out := make([]machineTokenResponse, 0, len(tokens))
	for i := range tokens {
		out = append(out, toMachineTokenResponse(&tokens[i], names[tokens[i].ClusterID], now))
	}
	c.JSON(http.StatusOK, gin.H{"tokens": out})
}

// issueMachineToken mints a long-lived credential for one machine account
// against one cluster, and renders the kubeconfig that carries it (admin only).
func (s *server) issueMachineToken(c *gin.Context) {
	caller, ok := s.currentUser(c)
	if !ok {
		return
	}
	account, ok := s.loadMachineAccount(c)
	if !ok {
		return
	}
	if !account.IsActive {
		c.JSON(http.StatusConflict, gin.H{
			"error": "this machine account is disabled, so a credential issued for it would not work",
		})
		return
	}

	var req issueMachineTokenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "a token needs a name saying what holds it"})
		return
	}

	ctx := c.Request.Context()
	cluster, err := s.store.ClusterByID(ctx, req.ClusterID)
	if errors.Is(err, db.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "cluster not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load the cluster"})
		return
	}

	// Direct mode is refused rather than served, and the refusal names the
	// reason. There, a credential is a service account token minted on the
	// cluster itself: KubeMG cannot withdraw it, no RoleBinding is provisioned
	// for it, and the cluster's own --service-account-max-token-expiration
	// usually refuses the window a pipeline wants anyway. A long-lived
	// credential this console cannot revoke is the one thing this feature exists
	// to avoid.
	if !cluster.UsesAgent() {
		c.JSON(http.StatusConflict, gin.H{
			"error": "programmatic access needs a cluster registered in agent mode. In direct mode the " +
				"credential is minted on the cluster itself, so KubeMG cannot revoke it and the " +
				"cluster's RBAC has nothing bound to it.",
		})
		return
	}
	if s.proxy == nil {
		c.JSON(http.StatusFailedDependency, gin.H{"error": "the agent proxy is not enabled on this server"})
		return
	}

	publicURL := s.settings(ctx).PublicURL
	if publicURL == "" {
		c.JSON(http.StatusFailedDependency, gin.H{"error": "no public URL is configured for this server"})
		return
	}

	// The account's own grant decides what the credential may reach, so a token
	// issued before any grant exists is a file that answers 403 to everything.
	// Refusing here is what turns that into an instruction.
	grants, err := s.store.AccessForUser(ctx, account.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load cluster access"})
		return
	}
	grant, held := grants[cluster.ID]
	if !held {
		c.JSON(http.StatusConflict, gin.H{
			"error": "this machine account has no access to that cluster yet — grant it a role first, " +
				"otherwise the credential authenticates and is then refused by the cluster",
		})
		return
	}

	namespace, ok := resolveNamespace(c, strings.TrimSpace(req.Namespace), grant)
	if !ok {
		return
	}

	expiresAt, warning, ok := machineTokenExpiry(c, req)
	if !ok {
		return
	}

	secret, hash, hint, err := auth.NewMachineToken()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not generate a token"})
		return
	}

	token := db.MachineToken{
		UserID:    account.ID,
		ClusterID: cluster.ID,
		Name:      name,
		Namespace: namespace,
		TokenHash: hash,
		Hint:      hint,
		ExpiresAt: expiresAt,
		CreatedBy: caller.ID,
	}
	if err := s.store.CreateMachineToken(ctx, &token); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not store the token"})
		return
	}

	server := proxyServerURL(publicURL, cluster.ID)
	input := k8s.KubeconfigInput{
		ClusterName: cluster.Name,
		Server:      server,
		Username:    account.Username,
		Token:       secret,
		Namespace:   namespace,
		// The bastion's own CA, for the same reason a generated kubeconfig
		// carries it: through the tunnel the "cluster" kubectl dials is KubeMG.
		CAData: []byte(s.bastionCA),
	}
	kubeconfig, err := k8s.BuildKubeconfig(input)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not render kubeconfig"})
		return
	}

	if insecure := insecureProxyWarning(server); insecure != "" {
		warning = strings.TrimSpace(warning + " " + insecure)
	}

	s.recordMachineTokenEvent(c, caller, account, cluster, &token, verbMachineTokenIssue, http.StatusCreated)

	c.JSON(http.StatusCreated, issueMachineTokenResponse{
		Token:      toMachineTokenResponse(&token, cluster.Name, time.Now().UTC()),
		Secret:     secret,
		Kubeconfig: string(kubeconfig),
		Filename:   fmt.Sprintf("%s-%s.kubeconfig", cluster.Name, account.Username),
		Context:    input.ContextName(),
		Server:     server,
		K8sRole:    grant.K8sRole,
		Warning:    warning,
	})
}

// revokeMachineToken closes a credential out (admin only). The row stays, so
// the trail it produced still resolves to something.
func (s *server) revokeMachineToken(c *gin.Context) {
	caller, ok := s.currentUser(c)
	if !ok {
		return
	}
	account, ok := s.loadMachineAccount(c)
	if !ok {
		return
	}
	tokenID, ok := parseIDParam(c, "tokenId", "token")
	if !ok {
		return
	}

	ctx := c.Request.Context()
	existing, err := s.store.MachineTokenByID(ctx, tokenID)
	if errors.Is(err, db.ErrNotFound) || (err == nil && existing.UserID != account.ID) {
		// Whose token it is, is not something the address may disclose: a token
		// belonging to another account answers as one that does not exist.
		c.JSON(http.StatusNotFound, gin.H{"error": "token not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load the token"})
		return
	}

	token, err := s.store.RevokeMachineToken(ctx, tokenID, time.Now().UTC())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not revoke the token"})
		return
	}

	cluster, _ := s.store.ClusterByID(ctx, token.ClusterID)
	s.recordMachineTokenEvent(c, caller, account, cluster, token, verbMachineTokenRevoke, http.StatusOK)

	name := ""
	if cluster != nil {
		name = cluster.Name
	}
	c.JSON(http.StatusOK, toMachineTokenResponse(token, name, time.Now().UTC()))
}

// machineTokenExpiry settles the window, and says what is true about it. A
// credential with no expiry is allowed — a release pipeline that stops at 3am on
// a quarter boundary is an outage nobody scheduled — but it is disclosed rather
// than issued quietly, because what replaces the expiry as a control is somebody
// noticing the row.
func machineTokenExpiry(c *gin.Context, req issueMachineTokenRequest) (*time.Time, string, bool) {
	if req.NeverExpires {
		if req.TTLSeconds > 0 {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "a token either expires or it does not — ttl_seconds and never_expires " +
					"cannot both be given",
			})
			return nil, "", false
		}
		return nil, "This credential never expires. It stops working when it is revoked here or the " +
			"machine account is disabled, so review it against its last-used time rather than waiting " +
			"for a clock.", true
	}

	ttl := defaultMachineTokenTTL
	if req.TTLSeconds != 0 {
		ttl = time.Duration(req.TTLSeconds) * time.Second
	}
	if ttl < k8s.MinTTL || ttl > maxMachineTokenTTL {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("ttl_seconds must be between %d and %d, or never_expires must be set",
				int64(k8s.MinTTL.Seconds()), int64(maxMachineTokenTTL.Seconds())),
		})
		return nil, "", false
	}
	expiresAt := time.Now().UTC().Add(ttl)
	return &expiresAt, "", true
}

// proxyServerURL is the address kubectl dials for one cluster. It is shared with
// the generated kubeconfig so the two credentials can never point at different
// spellings of the same route.
func proxyServerURL(publicURL string, clusterID uint) string {
	return fmt.Sprintf("%s/api/v1/clusters/%d/proxy", strings.TrimRight(publicURL, "/"), clusterID)
}

func toMachineTokenResponse(token *db.MachineToken, clusterName string, now time.Time) machineTokenResponse {
	status := "active"
	switch {
	case token.Revoked():
		status = "revoked"
	case token.Expired(now):
		status = "expired"
	}
	return machineTokenResponse{
		ID:          token.ID,
		UserID:      token.UserID,
		Name:        token.Name,
		Hint:        token.Hint,
		ClusterID:   token.ClusterID,
		ClusterName: clusterName,
		Namespace:   token.Namespace,
		ExpiresAt:   token.ExpiresAt,
		RevokedAt:   token.RevokedAt,
		LastUsedAt:  token.LastUsedAt,
		CreatedAt:   token.CreatedAt,
		Status:      status,
	}
}

// recordMachineTokenEvent puts the credential's lifecycle in the trail. The
// identities are crossed the way a recording replay crosses them: the record's
// user is the administrator who acted, and the token's account is named in the
// resource, because neither half answers "who issued production access to what"
// alone.
func (s *server) recordMachineTokenEvent(
	c *gin.Context,
	caller *db.User,
	account *db.User,
	cluster *db.Cluster,
	token *db.MachineToken,
	verb string,
	status int,
) {
	if s.auditor == nil || token == nil {
		return
	}
	event := bastion.Event{
		At:        time.Now().UTC(),
		UserID:    caller.ID,
		Username:  caller.Username,
		ClusterID: token.ClusterID,
		Verb:      verb,
		Method:    c.Request.Method,
		Path:      c.Request.URL.Path,
		Namespace: token.Namespace,
		Resource:  "servicetokens",
		Status:    status,
	}
	// The identity the credential will assert inside the cluster. It is the
	// same column a proxied call fills, which is what lets an auditor follow one
	// name from "who was given this" through to every call it then made.
	if account != nil {
		event.ImpersonatedUser = account.Username
	}
	if cluster != nil {
		event.Cluster = cluster.Name
	}
	s.auditor.Record(c.Request.Context(), event)
}
