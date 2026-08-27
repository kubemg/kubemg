package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/db"
	"github.com/kubemg/kubemg/backend/pkg/k8s"
)

type generateKubeconfigRequest struct {
	TTLSeconds int64  `json:"ttl_seconds"`
	Namespace  string `json:"namespace"`
}

type generateKubeconfigResponse struct {
	Cluster    string    `json:"cluster"`
	Context    string    `json:"context"`
	Namespace  string    `json:"namespace"`
	TTLSeconds int64     `json:"ttl_seconds"`
	ExpiresAt  time.Time `json:"expires_at"`
	Filename   string    `json:"filename"`
	Kubeconfig string    `json:"kubeconfig"`
	K8sRole    string    `json:"k8s_role"`
	// ServiceAcct is the in-cluster identity the credential authenticates as.
	// Only direct mode has one; through the bastion the caller is impersonated
	// and no service account is involved.
	ServiceAcct string `json:"service_account"`
	// ConnectionMode says which of the two credentials this is, so the UI can
	// describe honestly what the file does.
	ConnectionMode string `json:"connection_mode"`
	// Server is what kubectl will dial: the cluster's API server in direct
	// mode, KubeMG's own proxy in agent mode.
	Server string `json:"server"`
	// Warning flags a kubeconfig that is rendered but will not work as-is.
	Warning string `json:"warning,omitempty"`
}

// generateKubeconfig mints a short-lived token on the target cluster and returns
// a ready-to-use kubeconfig for it.
func (s *server) generateKubeconfig(c *gin.Context) {
	user, cluster, grant, k8sRole, ok := s.loadAuthorizedCluster(c)
	if !ok {
		return
	}

	var req generateKubeconfigRequest
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	// Query parameters are accepted as a convenience for CLI use.
	if req.TTLSeconds == 0 {
		if raw := c.Query("ttl_seconds"); raw != "" {
			parsed, parseErr := strconv.ParseInt(raw, 10, 64)
			if parseErr != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "ttl_seconds must be an integer"})
				return
			}
			req.TTLSeconds = parsed
		}
	}
	if req.Namespace == "" {
		req.Namespace = c.Query("namespace")
	}

	ctx := c.Request.Context()
	namespace, ok := resolveNamespace(c, req.Namespace, grant)
	if !ok {
		return
	}

	ttl := k8s.DefaultTTL
	if req.TTLSeconds != 0 {
		ttl = time.Duration(req.TTLSeconds) * time.Second
	}
	// The ceiling is an operator setting rather than a build constant: an
	// install that hands out a quarter's access and one that refuses anything
	// past a shift are both legitimate, and neither should need a redeploy.
	maxTTL := s.kubeconfigMaxTTL(ctx)
	if ttl < k8s.MinTTL || ttl > maxTTL {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf(
				"ttl_seconds must be between %d and %d",
				int64(k8s.MinTTL.Seconds()), int64(maxTTL.Seconds()),
			),
		})
		return
	}

	// An agent cluster has no API URL and no stored credential by design —
	// KubeMG reaches it only through the tunnel — so there is nothing to mint a
	// service account token on. Its kubeconfig points at KubeMG's own proxy
	// instead, which is the path that carries impersonation, namespace scope
	// and the audit trail anyway.
	if cluster.UsesAgent() {
		s.agentKubeconfig(c, user, cluster, namespace, k8sRole, ttl)
		return
	}
	if s.tokens == nil {
		c.JSON(http.StatusFailedDependency, gin.H{
			"error": "this server cannot mint tokens on target clusters",
		})
		return
	}

	serviceAccount := k8s.ServiceAccountName(user.Username)
	issued, err := s.tokens.IssueToken(ctx, cluster, k8s.TokenRequest{
		ServiceAccount:          serviceAccount,
		ServiceAccountNamespace: s.saNamespace,
		TTL:                     ttl,
	})
	if err != nil {
		status, message := upstreamFailure(err)
		c.JSON(status, gin.H{"error": message})
		return
	}

	caData, err := k8s.DecodeCACert(cluster.CACertData)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "stored cluster CA certificate is invalid"})
		return
	}

	input := k8s.KubeconfigInput{
		ClusterName: cluster.Name,
		Server:      cluster.APIURL,
		CAData:      caData,
		Username:    user.Username,
		Token:       issued.Token,
		Namespace:   namespace,
	}
	kubeconfig, err := k8s.BuildKubeconfig(input)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not render kubeconfig"})
		return
	}

	// The cluster's own API server enforces a ceiling of its own
	// (--service-account-max-token-expiration) and answers a longer request with
	// an earlier expiry rather than an error. Reporting the TTL that was asked
	// for would make the console count down from a window the token does not
	// have, so what is reported is the window the cluster granted.
	granted, shortened := grantedTTL(ttl, issued.ExpiresAt)

	// The register row, written by the generator itself. A direct-mode credential
	// cannot be revoked from here — see db.KubeconfigIssuance.RevocableHere — but
	// it is recorded all the same, because "who holds access to production right
	// now, and since when" is a question the register answers and the mode does
	// not change.
	expiresAt := issued.ExpiresAt
	if expiresAt.IsZero() {
		expiresAt = time.Now().UTC().Add(granted)
	}
	s.recordKubeconfigIssuance(c, newIssuance(
		user, user, cluster, "", db.ModeDirect, namespace, k8sRole, serviceAccount, expiresAt,
	), user, user, cluster)

	c.JSON(http.StatusOK, generateKubeconfigResponse{
		Cluster:        cluster.Name,
		Context:        input.ContextName(),
		Namespace:      namespace,
		TTLSeconds:     int64(granted.Seconds()),
		ExpiresAt:      issued.ExpiresAt,
		Filename:       fmt.Sprintf("%s-%s.kubeconfig", cluster.Name, user.Username),
		Kubeconfig:     string(kubeconfig),
		K8sRole:        k8sRole,
		ServiceAcct:    serviceAccount,
		ConnectionMode: db.ModeDirect,
		Server:         cluster.APIURL,
		Warning:        shortened,
	})
}

// grantedTTL reconciles the window that was asked for with the expiry the
// cluster reported. A difference under a minute is the round trip rather than a
// policy, and an API server that reports no expiry at all is left alone — the
// issuer already substitutes a fallback there.
func grantedTTL(asked time.Duration, expiresAt time.Time) (time.Duration, string) {
	if expiresAt.IsZero() {
		return asked, ""
	}
	granted := time.Until(expiresAt).Round(time.Second)
	if granted <= 0 || granted >= asked-time.Minute {
		return asked, ""
	}
	return granted, fmt.Sprintf(
		"This cluster's API server caps service account tokens at about %s, so it issued %s instead of "+
			"the %s that was requested. Raising it means raising the API server's own "+
			"--service-account-max-token-expiration, or registering the cluster in agent mode, where the "+
			"credential is KubeMG's rather than the cluster's.",
		roughDuration(granted), roughDuration(granted), roughDuration(asked))
}

// roughDuration is a duration said the way an operator says it. Go's own
// formatting renders three months as "2160h0m0s".
func roughDuration(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return fmt.Sprintf("%d days", int(d.Hours())/24)
	case d >= 2*time.Hour:
		return fmt.Sprintf("%d hours", int(d.Hours()))
	case d >= time.Hour:
		return "1 hour"
	default:
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	}
}

// kubeconfigMaxTTL resolves the longest credential this install will issue. A
// stored value out of bounds or a database that cannot be read both fall back to
// the build's default rather than to no ceiling at all.
func (s *server) kubeconfigMaxTTL(ctx context.Context) time.Duration {
	hours := s.settings(ctx).KubeconfigMaxTTLHours
	ceiling := time.Duration(hours) * time.Hour
	if ceiling < time.Hour || ceiling > k8s.MaxTTL {
		return k8s.DefaultMaxTTL
	}
	return ceiling
}

// kubeconfigPolicy reports the window a caller may ask for. It is readable by
// anyone who can generate a kubeconfig — which is anyone with a grant — for the
// same reason the recording policy is readable by anyone who might be recorded:
// the surface offering the choice has to know what the choices are, and the
// alternative is a form that discovers the ceiling by being refused.
func (s *server) kubeconfigPolicy(c *gin.Context) {
	if _, ok := s.currentUser(c); !ok {
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"min_ttl_seconds":     int64(k8s.MinTTL.Seconds()),
		"default_ttl_seconds": int64(k8s.DefaultTTL.Seconds()),
		"max_ttl_seconds":     int64(s.kubeconfigMaxTTL(c.Request.Context()).Seconds()),
	})
}

// agentKubeconfig renders the bastion-mode credential: kubectl talks to KubeMG,
// KubeMG replays the call down the cluster's tunnel under the caller's
// impersonated identity. The token is scoped to this one cluster's proxy, so a
// leaked kubeconfig cannot be replayed against the rest of the API.
func (s *server) agentKubeconfig(
	c *gin.Context,
	user *db.User,
	cluster *db.Cluster,
	namespace, k8sRole string,
	ttl time.Duration,
) {
	if s.proxy == nil {
		c.JSON(http.StatusFailedDependency, gin.H{
			"error": "the agent proxy is not enabled on this server",
		})
		return
	}

	publicURL := s.settings(c.Request.Context()).PublicURL
	if publicURL == "" {
		c.JSON(http.StatusFailedDependency, gin.H{
			"error": "no public URL is configured for this server",
		})
		return
	}

	token, tokenID, expiresAt, err := s.jwt.GenerateProxyToken(
		user.ID, user.Username, user.Role, cluster.ID, ttl,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not issue an access token"})
		return
	}

	server := fmt.Sprintf("%s/api/v1/clusters/%d/proxy", strings.TrimRight(publicURL, "/"), cluster.ID)
	input := k8s.KubeconfigInput{
		ClusterName: cluster.Name,
		Server:      server,
		Username:    user.Username,
		Token:       token,
		Namespace:   namespace,
		// In agent mode the "cluster" kubectl dials is KubeMG, so the CA it has
		// to trust is KubeMG's own — not the target cluster's. Without this a
		// self-signed or internal-PKI bastion hands out kubeconfigs that fail
		// on x509 at the first call, which the operator can only fix by editing
		// the file or turning verification off. Empty when the bastion is
		// publicly trusted, which is what the system roots are for.
		CAData: []byte(s.bastionCA),
	}
	kubeconfig, err := k8s.BuildKubeconfig(input)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not render kubeconfig"})
		return
	}

	// The register row. tokenID is the `jti` in the credential above, which is
	// what makes revoking it something the token cannot argue with: the gateway
	// matches that id against a published set on every call.
	s.recordKubeconfigIssuance(c, newIssuance(
		user, user, cluster, tokenID, db.ModeAgent, namespace, k8sRole, "", expiresAt,
	), user, user, cluster)

	c.JSON(http.StatusOK, generateKubeconfigResponse{
		Cluster:        cluster.Name,
		Context:        input.ContextName(),
		Namespace:      namespace,
		TTLSeconds:     int64(ttl.Seconds()),
		ExpiresAt:      expiresAt,
		Filename:       fmt.Sprintf("%s-%s.kubeconfig", cluster.Name, user.Username),
		Kubeconfig:     string(kubeconfig),
		K8sRole:        k8sRole,
		ConnectionMode: db.ModeAgent,
		Server:         server,
		Warning:        insecureProxyWarning(server),
	})
}

// insecureProxyWarning names the one configuration that renders a valid
// kubeconfig kubectl still refuses to use: client-go will not send a bearer
// token over plain http, so a non-TLS public URL fails at the first call with
// nothing to explain it.
func insecureProxyWarning(server string) string {
	if strings.HasPrefix(server, "https://") {
		return ""
	}
	return "This server's public URL is not HTTPS. kubectl refuses to send a bearer token over " +
		"plain HTTP, so put TLS in front of KubeMG before using this kubeconfig."
}

// authorizeCluster checks that the user may act on the cluster, returning their
// grant (zero-valued for admins) and effective Kubernetes role. It writes the
// error response itself when access is denied.
func (s *server) authorizeCluster(c *gin.Context, user *db.User, clusterID uint) (db.UserClusterAccess, string, bool) {
	if user.IsAdmin() {
		return db.UserClusterAccess{}, db.K8sRoleClusterAdmin, true
	}

	grants, err := s.store.AccessForUser(c.Request.Context(), user.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load cluster access"})
		return db.UserClusterAccess{}, "", false
	}

	grant, ok := grants[clusterID]
	if !ok {
		c.JSON(http.StatusForbidden, gin.H{"error": "no access to this cluster"})
		return db.UserClusterAccess{}, "", false
	}
	return grant, grant.K8sRole, true
}

// resolveNamespace picks the kubeconfig's default namespace, enforcing the
// namespace scope attached to the user's grant.
func resolveNamespace(c *gin.Context, requested string, grant db.UserClusterAccess) (string, bool) {
	allowed := grant.NamespaceList()

	if requested == "" {
		if len(allowed) > 0 {
			return allowed[0], true
		}
		return "default", true
	}
	if len(allowed) > 0 && !slices.Contains(allowed, requested) {
		c.JSON(http.StatusForbidden, gin.H{"error": "namespace is outside your granted scope"})
		return "", false
	}
	return requested, true
}

// upstreamFailure maps a token issuance error onto an HTTP status.
func upstreamFailure(err error) (int, string) {
	var upstream *k8s.UpstreamError
	switch {
	case errors.Is(err, k8s.ErrInvalidTTL):
		return http.StatusBadRequest, err.Error()
	case errors.Is(err, k8s.ErrMissingCredentials):
		return http.StatusFailedDependency, "cluster registration is missing an API URL or service account token"
	case errors.As(err, &upstream):
		return http.StatusBadGateway, fmt.Sprintf("target cluster rejected the request: %s", upstream.Op)
	default:
		return http.StatusBadGateway, "could not generate a token on the target cluster"
	}
}
