package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/bastion"
	"github.com/kubemg/kubemg/backend/pkg/db"
	"github.com/kubemg/kubemg/backend/pkg/k8s"
)

type clusterResponse struct {
	ID                uint       `json:"id"`
	Name              string     `json:"name"`
	Environment       string     `json:"environment"`
	Description       string     `json:"description,omitempty"`
	APIURL            string     `json:"api_url"`
	Status            string     `json:"status"`
	StatusMessage     string     `json:"status_message,omitempty"`
	KubernetesVersion string     `json:"kubernetes_version,omitempty"`
	LastCheckedAt     *time.Time `json:"last_checked_at,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	K8sRole           string     `json:"k8s_role"`
	Namespaces        []string   `json:"namespaces"`
	ConnectionMode    string     `json:"connection_mode"`
	AgentVersion      string     `json:"agent_version,omitempty"`
	AgentConnectedAt  *time.Time `json:"agent_connected_at,omitempty"`
	// AgentAttached is live tunnel state rather than the stored status, so the
	// registration wizard can tell "the agent is talking to us right now" from
	// "it managed to once".
	AgentAttached bool `json:"agent_attached"`
}

// toClusterResponse renders a cluster for a caller holding the given access.
func toClusterResponse(cluster db.Cluster, k8sRole string, namespaces []string) clusterResponse {
	if namespaces == nil {
		namespaces = []string{}
	}
	return clusterResponse{
		ID:                cluster.ID,
		Name:              cluster.Name,
		Environment:       cluster.Environment,
		Description:       cluster.Description,
		APIURL:            cluster.APIURL,
		Status:            cluster.Status,
		StatusMessage:     cluster.StatusMessage,
		KubernetesVersion: cluster.KubernetesVersion,
		LastCheckedAt:     cluster.LastCheckedAt,
		CreatedAt:         cluster.CreatedAt,
		K8sRole:           k8sRole,
		Namespaces:        namespaces,
		ConnectionMode:    connectionMode(cluster),
		AgentVersion:      cluster.AgentVersion,
		AgentConnectedAt:  cluster.AgentConnectedAt,
	}
}

// connectionMode defaults clusters registered before Phase 2 to direct mode, so
// an existing inventory keeps behaving the way it did.
func connectionMode(cluster db.Cluster) string {
	if cluster.ConnectionMode == "" {
		return db.ModeDirect
	}
	return cluster.ConnectionMode
}

// withTunnelState stamps live agent connectivity onto a rendered cluster.
func (s *server) withTunnelState(out clusterResponse) clusterResponse {
	if s.tunnels != nil && out.ConnectionMode == db.ModeAgent {
		out.AgentAttached = s.tunnels.Connected(out.ID)
	}
	return out
}

type createClusterRequest struct {
	Name        string `json:"name" binding:"required"`
	Environment string `json:"environment" binding:"required,oneof=prod staging dev"`
	Description string `json:"description"`
	// ConnectionMode defaults to direct when omitted, which is what every
	// pre-Phase-2 client sends.
	ConnectionMode string `json:"connection_mode" binding:"omitempty,oneof=agent direct"`
	// The connection fields below are required in direct mode and rejected in
	// agent mode; binding tags cannot express that, so it is checked by hand.
	APIURL              string `json:"api_url" binding:"omitempty,url"`
	CACertData          string `json:"ca_cert_data"`
	ServiceAccountToken string `json:"service_account_token"`
}

// listClusters returns every cluster the caller is authorized to use.
func (s *server) listClusters(c *gin.Context) {
	user, ok := s.currentUser(c)
	if !ok {
		return
	}

	ctx := c.Request.Context()
	clusters, err := s.store.ClustersForUser(ctx, user)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not list clusters"})
		return
	}

	grants := map[uint]db.UserClusterAccess{}
	if !user.IsAdmin() {
		grants, err = s.store.AccessForUser(ctx, user.ID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load cluster access"})
			return
		}
	}

	out := make([]clusterResponse, 0, len(clusters))
	for _, cluster := range clusters {
		if user.IsAdmin() {
			out = append(out, s.withTunnelState(toClusterResponse(cluster, db.K8sRoleClusterAdmin, nil)))
			continue
		}
		grant := grants[cluster.ID]
		out = append(out, s.withTunnelState(
			toClusterResponse(cluster, grant.K8sRole, grant.NamespaceList()),
		))
	}

	c.JSON(http.StatusOK, gin.H{"clusters": out})
}

// loadAuthorizedCluster resolves the :id parameter and checks the caller may
// act on that cluster, writing the error response itself when it fails.
func (s *server) loadAuthorizedCluster(
	c *gin.Context,
) (*db.User, *db.Cluster, db.UserClusterAccess, string, bool) {
	var noGrant db.UserClusterAccess

	user, ok := s.currentUser(c)
	if !ok {
		return nil, nil, noGrant, "", false
	}

	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid cluster id"})
		return nil, nil, noGrant, "", false
	}

	cluster, err := s.store.ClusterByID(c.Request.Context(), uint(id))
	if errors.Is(err, db.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "cluster not found"})
		return nil, nil, noGrant, "", false
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load cluster"})
		return nil, nil, noGrant, "", false
	}

	grant, k8sRole, ok := s.authorizeCluster(c, user, cluster.ID)
	if !ok {
		return nil, nil, noGrant, "", false
	}
	return user, cluster, grant, k8sRole, true
}

// showCluster returns a single cluster the caller is authorized for.
func (s *server) showCluster(c *gin.Context) {
	user, cluster, grant, k8sRole, ok := s.loadAuthorizedCluster(c)
	if !ok {
		return
	}

	var namespaces []string
	if !user.IsAdmin() {
		namespaces = grant.NamespaceList()
	}
	c.JSON(http.StatusOK, s.withTunnelState(toClusterResponse(*cluster, k8sRole, namespaces)))
}

// checkCluster probes the target cluster and records the result.
func (s *server) checkCluster(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid cluster id"})
		return
	}

	ctx := c.Request.Context()
	cluster, err := s.store.ClusterByID(ctx, uint(id))
	if errors.Is(err, db.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "cluster not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load cluster"})
		return
	}

	health := s.probe(ctx, cluster)

	if err := s.store.UpdateClusterHealth(ctx, cluster.ID, health); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not record the check result"})
		return
	}

	cluster.Status = health.Status
	cluster.StatusMessage = health.StatusMessage
	cluster.KubernetesVersion = health.KubernetesVersion
	cluster.LastCheckedAt = &health.CheckedAt

	c.JSON(http.StatusOK, s.withTunnelState(toClusterResponse(*cluster, db.K8sRoleClusterAdmin, nil)))
}

// probe establishes whether a cluster is reachable. For an agent-mode cluster
// that question is answered by the tunnel pool rather than by dialling an API
// server KubeMG has no route to — the whole point of the agent is that there
// is nothing here to dial.
func (s *server) probe(ctx context.Context, cluster *db.Cluster) db.ClusterHealth {
	health := db.ClusterHealth{
		Status:            db.StatusUnhealthy,
		KubernetesVersion: cluster.KubernetesVersion,
		CheckedAt:         time.Now().UTC(),
	}

	if connectionMode(*cluster) == db.ModeAgent {
		if s.tunnels != nil && s.tunnels.Connected(cluster.ID) {
			health.Status = db.StatusHealthy
			return health
		}
		health.StatusMessage = "no agent has connected from this cluster yet"
		if cluster.AgentVersion != "" {
			health.StatusMessage = "the in-cluster agent is not connected"
		}
		return health
	}

	report := s.health.CheckHealth(ctx, cluster)
	health.StatusMessage = report.Message
	if report.Reachable {
		health.Status = db.StatusHealthy
		health.StatusMessage = ""
		health.KubernetesVersion = report.Version
	}
	return health
}

// createCluster registers a new target Kubernetes cluster (admin only).
func (s *server) createCluster(c *gin.Context) {
	var req createClusterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	mode := req.ConnectionMode
	if mode == "" {
		mode = db.ModeDirect
	}

	cluster := db.Cluster{
		Name:           req.Name,
		Environment:    req.Environment,
		Description:    req.Description,
		ConnectionMode: mode,
		Status:         db.StatusPending,
	}

	if mode == db.ModeDirect {
		if req.APIURL == "" || req.ServiceAccountToken == "" {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "api_url and service_account_token are required for a direct connection",
			})
			return
		}
		// Reject an unusable CA at registration time rather than at kubeconfig
		// generation time, when it is far more confusing.
		if _, err := k8s.DecodeCACert(req.CACertData); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "ca_cert_data must be PEM or base64-encoded PEM"})
			return
		}
		cluster.APIURL = req.APIURL
		cluster.CACertData = req.CACertData
		cluster.ServiceAccountToken = req.ServiceAccountToken
	} else {
		// An agent-mode cluster stores no credential *for* the cluster; it
		// stores the one credential the cluster will use to reach us.
		token, err := bastion.NewAgentToken()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "could not mint a registration token"})
			return
		}
		cluster.AgentToken = token
	}

	err := s.store.CreateCluster(c.Request.Context(), &cluster)
	if errors.Is(err, db.ErrConflict) {
		c.JSON(http.StatusConflict, gin.H{"error": "cluster name already registered"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not register cluster"})
		return
	}

	c.JSON(http.StatusCreated, s.withTunnelState(toClusterResponse(cluster, db.K8sRoleClusterAdmin, nil)))
}

// deleteCluster removes a cluster registration (admin only).
func (s *server) deleteCluster(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid cluster id"})
		return
	}

	err = s.store.DeleteCluster(c.Request.Context(), uint(id))
	if errors.Is(err, db.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "cluster not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not delete cluster"})
		return
	}

	c.Status(http.StatusNoContent)
}
