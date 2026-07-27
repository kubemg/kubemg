package api

import (
	"context"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/auth"
	"github.com/kubemg/kubemg/backend/pkg/bastion"
	"github.com/kubemg/kubemg/backend/pkg/db"
	"github.com/kubemg/kubemg/backend/pkg/k8s"
)

// defaultSANamespace is where KubeMG provisions per-user service accounts when
// no namespace is configured.
const defaultSANamespace = "kubemg-system"

// defaultPublicURL is the dev stack's backend origin.
const defaultPublicURL = "http://localhost:8080"

// defaultAllowedOrigin is the Vite dev server.
var defaultAllowedOrigins = []string{"http://localhost:5173"}

// Store is the persistence surface the HTTP layer depends on.
type Store interface {
	UserByUsername(ctx context.Context, username string) (*db.User, error)
	UserByID(ctx context.Context, id uint) (*db.User, error)
	ClustersForUser(ctx context.Context, user *db.User) ([]db.Cluster, error)
	AccessForUser(ctx context.Context, userID uint) (map[uint]db.UserClusterAccess, error)
	Clusters(ctx context.Context) ([]db.Cluster, error)
	ClusterByID(ctx context.Context, id uint) (*db.Cluster, error)
	ClusterByAgentToken(ctx context.Context, token string) (*db.Cluster, error)
	CreateCluster(ctx context.Context, cluster *db.Cluster) error
	DeleteCluster(ctx context.Context, id uint) error
	UpdateClusterHealth(ctx context.Context, id uint, health db.ClusterHealth) error

	ListUsers(ctx context.Context) ([]db.User, error)
	CreateUser(ctx context.Context, user *db.User) error
	UpdateUser(ctx context.Context, id uint, update db.UserUpdate) (*db.User, error)
	SetUserActive(ctx context.Context, id uint, active bool) (*db.User, error)
	DeleteUser(ctx context.Context, id uint) error
	TouchLastLogin(ctx context.Context, id uint, at time.Time) error

	ListGroups(ctx context.Context) ([]db.GroupSummary, error)
	GroupByID(ctx context.Context, id uint) (*db.Group, error)
	CreateGroup(ctx context.Context, group *db.Group) error
	DeleteGroup(ctx context.Context, id uint) error
	AddGroupMember(ctx context.Context, groupID, userID uint) error
	RemoveGroupMember(ctx context.Context, groupID, userID uint) error

	ListUserAccess(ctx context.Context) ([]db.UserClusterAccess, error)
	ListGroupAccess(ctx context.Context) ([]db.GroupClusterAccess, error)
	AssignUserAccess(ctx context.Context, grant *db.UserClusterAccess) error
	AssignGroupAccess(ctx context.Context, grant *db.GroupClusterAccess) error
	RevokeUserAccess(ctx context.Context, userID, clusterID uint) error
	RevokeGroupAccess(ctx context.Context, groupID, clusterID uint) error

	ListAuditEvents(ctx context.Context, filter db.AuditFilter) ([]db.AuditEvent, int64, error)
	AuditSummary(ctx context.Context, since time.Time) (db.AuditStats, error)
	PruneAuditEvents(ctx context.Context, before time.Time) (int64, error)

	Settings(ctx context.Context) (map[string]string, error)
	PutSettings(ctx context.Context, values map[string]string, updatedBy uint) error
}

// Options wires the router's dependencies.
type Options struct {
	Store Store
	JWT   *auth.Manager
	// Tokens mints short-lived credentials on target clusters. When nil, the
	// kubeconfig generator route is not registered.
	Tokens k8s.Issuer
	// Health probes target clusters. When nil, the check route is not
	// registered.
	Health k8s.Checker
	// SANamespace is the in-cluster namespace holding KubeMG's per-user service
	// accounts. Defaults to "kubemg-system".
	SANamespace string
	// AllowedOrigins are the browser origins permitted to call the API. A single
	// "*" entry allows any origin. Defaults to the Vite dev server.
	AllowedOrigins []string

	// Bastion accepts agent tunnels. When nil, the tunnel and proxy routes are
	// not registered and KubeMG behaves exactly as it did in Phase 1.
	Bastion *bastion.Server
	// Proxy replays kubectl traffic down those tunnels. Registered only
	// alongside a Bastion.
	Proxy *bastion.Proxy
	// PublicURL is the outside address of this server, baked into generated
	// agent install commands. Defaults to the Vite dev server's API origin.
	PublicURL string
	// AgentImage and AgentNamespace parameterise the generated manifests.
	AgentImage     string
	AgentNamespace string
	// AuditRetentionDays is the boot-time default retention window, overridable
	// at runtime from the Settings page. Zero falls back to
	// defaultAuditRetentionDays.
	AuditRetentionDays int
	// Background scopes the housekeeping goroutines that run alongside the
	// handlers — today just the audit retention pruner. Left nil, as the tests
	// leave it, nothing is started and the router is purely request-driven.
	Background context.Context
	// Logger is where those goroutines report. Defaults to slog's default.
	Logger *slog.Logger
}

// tunnels is the slice of the bastion registry the HTTP layer needs: whether a
// given cluster has an agent attached right now.
type tunnels interface {
	Connected(clusterID uint) bool
}

type server struct {
	store              Store
	jwt                *auth.Manager
	tokens             k8s.Issuer
	health             k8s.Checker
	tunnels            tunnels
	proxy              *bastion.Proxy
	saNamespace        string
	publicURL          string
	agentImage         string
	agentNamespace     string
	auditRetentionDays int
	logger             *slog.Logger
}

// NewRouter builds the KubeMG HTTP router. Authenticated routes are only
// registered when both a store and a token manager are supplied.
func NewRouter(opts Options) *gin.Engine {
	router := gin.New()
	router.Use(gin.Recovery())
	router.Use(cors.New(corsConfig(opts.AllowedOrigins)))

	router.GET("/health", healthHandler)

	if opts.Store == nil || opts.JWT == nil {
		return router
	}

	saNamespace := opts.SANamespace
	if saNamespace == "" {
		saNamespace = defaultSANamespace
	}
	publicURL := strings.TrimRight(opts.PublicURL, "/")
	if publicURL == "" {
		publicURL = defaultPublicURL
	}

	retention := opts.AuditRetentionDays
	if retention < minAuditRetentionDays {
		retention = defaultAuditRetentionDays
	}

	s := &server{
		store:              opts.Store,
		jwt:                opts.JWT,
		tokens:             opts.Tokens,
		health:             opts.Health,
		proxy:              opts.Proxy,
		saNamespace:        saNamespace,
		publicURL:          publicURL,
		agentImage:         opts.AgentImage,
		agentNamespace:     opts.AgentNamespace,
		auditRetentionDays: retention,
		logger:             opts.Logger,
	}
	if opts.Bastion != nil {
		s.tunnels = opts.Bastion.Registry()
	}
	if opts.Background != nil {
		// The audit table is the one thing here that grows without an operator
		// touching it, so enforcing its retention is a server responsibility
		// rather than a cron job someone has to remember to install.
		go s.startAuditPruner(opts.Background)
	}
	requireAuth := auth.RequireAuth(s.jwt)
	requireAdmin := auth.RequireRole(db.RoleAdmin)

	if opts.Bastion != nil {
		// Agents authenticate on their own registration token, so this route
		// sits outside the JWT middleware. It is the only inbound entry point
		// the Phase 2 architecture adds.
		router.GET("/agent/v1/tunnel", opts.Bastion.HandleAgent)

		// The installer is fetched by kubectl, which cannot carry a KubeMG
		// session; the registration token in the path is the credential.
		install := router.Group("/install/:token")
		install.GET("/agent.yaml", s.installManifest)
		install.GET("/kustomize.tar.gz", s.installArchive)
	}

	v1 := router.Group("/api/v1")
	{
		v1.POST("/auth/login", s.login)
		v1.GET("/auth/me", requireAuth, s.me)

		clusters := v1.Group("/clusters", requireAuth)
		clusters.GET("", s.listClusters)
		clusters.GET("/:id", s.showCluster)
		clusters.POST("", requireAdmin, s.createCluster)
		clusters.DELETE("/:id", requireAdmin, s.deleteCluster)
		if s.health != nil {
			clusters.POST("/:id/check", requireAdmin, s.checkCluster)
		}
		if s.tokens != nil {
			clusters.POST("/:id/kubeconfig/generate", s.generateKubeconfig)
		}
		if opts.Bastion != nil {
			clusters.GET("/:id/kustomize", requireAdmin, s.clusterKustomize)
		}
		if opts.Proxy != nil {
			// kubectl's server URL points here, so every verb has to land on
			// the same handler.
			clusters.Any("/:id/proxy/*path", opts.Proxy.Handle)

			// Live cluster state, read on demand through the same tunnel and
			// under the same impersonated identity as a kubectl call — the UI
			// gets no privileged shortcut.
			resources := clusters.Group("/:id/resources")
			resources.GET("/namespaces", s.listNamespaces)
			resources.GET("/workloads", s.listWorkloads)
			resources.GET("/pods", s.listPods)
			resources.GET("/pods/:pod", s.showPod)
			resources.GET("/pods/:pod/logs", s.podLogs)

			// The rest of the inventory behind the Explore sidebar: one route
			// per list an operator can be looking at. The cluster-scoped ones
			// refuse a namespace-scoped grant, since a cluster-wide list would
			// reach past it.
			resources.GET("/deployments", s.listWorkloadsOf("Deployment"))
			resources.GET("/statefulsets", s.listWorkloadsOf("StatefulSet"))
			resources.GET("/daemonsets", s.listWorkloadsOf("DaemonSet"))
			resources.GET("/jobs", s.listJobs)
			resources.GET("/cronjobs", s.listCronJobs)

			resources.GET("/services", s.listServices)
			resources.GET("/ingresses", s.listIngresses)
			// Gateway API and Istio are optional: a cluster without them
			// answers with an empty list marked unavailable, not an error.
			resources.GET("/httproutes", s.listHTTPRoutes)
			resources.GET("/virtualservices", s.listVirtualServices)

			resources.GET("/persistentvolumes", s.listPersistentVolumes)
			resources.GET("/persistentvolumeclaims", s.listPersistentVolumeClaims)
			resources.GET("/storageclasses", s.listStorageClasses)
			resources.GET("/configmaps", s.listConfigMaps)
			// Secrets are listed as metadata only; no value reaches a response.
			resources.GET("/secrets", s.listSecrets)

			resources.GET("/crds", s.listCRDs)
			resources.GET("/nodes", s.listNodes)

			// One object in full, as the YAML an operator already reads. The
			// PUT is the only write path in the resource API; it goes down the
			// same impersonated tunnel, so the cluster's RBAC decides whether
			// the caller may actually change anything.
			resources.GET("/object", s.showResourceObject)
			resources.PUT("/object", s.updateResourceObject)

			// Live utilisation from the cluster's own Metrics API. It rides the
			// same tunnel, grant and audit trail as the lists above; a cluster
			// with no metrics-server answers "unavailable" rather than failing.
			metrics := clusters.Group("/:id/metrics")
			metrics.GET("/nodes", s.nodeMetrics)
			metrics.GET("/pods", s.podMetrics)
			metrics.GET("/pods/:pod", s.showPodMetrics)
		}

		// Identity and access management is an administrative surface only.
		users := v1.Group("/users", requireAuth, requireAdmin)
		users.GET("", s.listUsers)
		users.POST("", s.createUser)
		users.PUT("/:id", s.updateUser)
		users.PATCH("/:id/status", s.setUserStatus)
		users.DELETE("/:id", s.deleteUser)

		groups := v1.Group("/groups", requireAuth, requireAdmin)
		groups.GET("", s.listGroups)
		groups.POST("", s.createGroup)
		groups.DELETE("/:id", s.deleteGroup)
		groups.POST("/:id/members", s.addGroupMember)
		groups.DELETE("/:id/members/:userId", s.removeGroupMember)

		permissions := v1.Group("/permissions", requireAuth, requireAdmin)
		permissions.GET("", s.listPermissions)
		permissions.POST("/assign", s.assignPermission)
		permissions.POST("/revoke", s.revokePermission)

		// The audit trail is readable by everyone, but a non-admin only ever
		// sees their own actions — the handler narrows the filter itself.
		audit := v1.Group("/audit", requireAuth)
		audit.GET("", s.listAudit)
		audit.GET("/summary", s.auditSummary)

		// Server-wide settings. The public URL lands inside manifests applied on
		// other people's clusters, so this is an administrative surface.
		settings := v1.Group("/settings", requireAuth, requireAdmin)
		settings.GET("", s.getSettings)
		settings.PUT("", s.updateSettings)
	}

	return router
}

// corsConfig permits the browser app to send its bearer token. gin's
// cors.Default() omits the Authorization header, which silently breaks every
// authenticated cross-origin request.
func corsConfig(origins []string) cors.Config {
	if len(origins) == 0 {
		origins = defaultAllowedOrigins
	}

	cfg := cors.Config{
		AllowMethods: []string{
			http.MethodGet,
			http.MethodPost,
			http.MethodPut,
			http.MethodPatch,
			http.MethodDelete,
			http.MethodOptions,
		},
		AllowHeaders:  []string{"Origin", "Content-Type", "Accept", "Authorization"},
		ExposeHeaders: []string{"Content-Length"},
		MaxAge:        12 * time.Hour,
	}
	if slices.Contains(origins, "*") {
		cfg.AllowAllOrigins = true
		return cfg
	}
	cfg.AllowOrigins = origins
	return cfg
}

func healthHandler(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
