package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
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
	Cluster     string    `json:"cluster"`
	Context     string    `json:"context"`
	Namespace   string    `json:"namespace"`
	TTLSeconds  int64     `json:"ttl_seconds"`
	ExpiresAt   time.Time `json:"expires_at"`
	Filename    string    `json:"filename"`
	Kubeconfig  string    `json:"kubeconfig"`
	K8sRole     string    `json:"k8s_role"`
	ServiceAcct string    `json:"service_account"`
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
	if ttl < k8s.MinTTL || ttl > k8s.MaxTTL {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf(
				"ttl_seconds must be between %d and %d",
				int64(k8s.MinTTL.Seconds()), int64(k8s.MaxTTL.Seconds()),
			),
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

	c.JSON(http.StatusOK, generateKubeconfigResponse{
		Cluster:     cluster.Name,
		Context:     input.ContextName(),
		Namespace:   namespace,
		TTLSeconds:  int64(ttl.Seconds()),
		ExpiresAt:   issued.ExpiresAt,
		Filename:    fmt.Sprintf("%s-%s.kubeconfig", cluster.Name, user.Username),
		Kubeconfig:  string(kubeconfig),
		K8sRole:     k8sRole,
		ServiceAcct: serviceAccount,
	})
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
