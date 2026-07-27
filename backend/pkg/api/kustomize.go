package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/agentpkg"
	"github.com/kubemg/kubemg/backend/pkg/db"
)

// agentInstallResponse is everything the registration wizard needs to show step
// two: the commands to paste, and the manifests behind them so an operator can
// read what they are about to apply before they apply it.
type agentInstallResponse struct {
	ClusterID   uint   `json:"cluster_id"`
	Cluster     string `json:"cluster"`
	Namespace   string `json:"namespace"`
	Image       string `json:"image"`
	BastionURL  string `json:"bastion_url"`
	PackageDir  string `json:"package_dir"`
	AgentToken  string `json:"agent_token"`
	ManifestURL string `json:"manifest_url"`
	ArchiveURL  string `json:"archive_url"`
	// ApplyCommand is the one-liner; KustomizeCommand is the two-step form for
	// people who want the Kustomize package on disk.
	ApplyCommand     string            `json:"apply_command"`
	KustomizeCommand string            `json:"kustomize_command"`
	Manifest         string            `json:"manifest"`
	Files            map[string]string `json:"files"`
}

// clusterKustomize serves the rendered agent installation package for a cluster
// (admin only). `?format=yaml` returns the flat manifest as a download instead
// of the JSON envelope.
func (s *server) clusterKustomize(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid cluster id"})
		return
	}

	cluster, err := s.store.ClusterByID(c.Request.Context(), uint(id))
	if errors.Is(err, db.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "cluster not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load cluster"})
		return
	}
	if connectionMode(*cluster) != db.ModeAgent {
		c.JSON(http.StatusConflict, gin.H{
			"error": "this cluster is registered for direct API access and has no agent to install",
		})
		return
	}
	if cluster.AgentToken == "" {
		c.JSON(http.StatusConflict, gin.H{
			"error": "this cluster has no registration token; re-register it in agent mode",
		})
		return
	}

	opts := s.agentOptions(c.Request.Context(), cluster.AgentToken)
	files, err := agentpkg.Render(opts)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	manifest, err := agentpkg.Manifest(opts)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if c.Query("format") == "yaml" {
		c.Header("Content-Disposition",
			fmt.Sprintf("attachment; filename=%q", cluster.Name+"-kubemg-agent.yaml"))
		c.Data(http.StatusOK, "application/yaml; charset=utf-8", []byte(manifest))
		return
	}

	manifestURL := s.installURL(c.Request.Context(), cluster.AgentToken, "agent.yaml")
	archiveURL := s.installURL(c.Request.Context(), cluster.AgentToken, "kustomize.tar.gz")

	c.JSON(http.StatusOK, agentInstallResponse{
		ClusterID:   cluster.ID,
		Cluster:     cluster.Name,
		Namespace:   opts.Namespace,
		Image:       opts.Image,
		BastionURL:  opts.BastionURL,
		PackageDir:  agentpkg.PackageDir,
		AgentToken:  cluster.AgentToken,
		ManifestURL: manifestURL,
		ArchiveURL:  archiveURL,
		ApplyCommand: fmt.Sprintf(
			"kubectl apply -f %s", manifestURL),
		// Kustomize only accepts local paths and Git specs as remote targets,
		// so the package is fetched and extracted before `apply -k` sees it.
		KustomizeCommand: fmt.Sprintf(
			"curl -sfL %s | tar -xz\nkubectl apply -k %s", archiveURL, agentpkg.PackageDir),
		Manifest: manifest,
		Files:    files,
	})
}

// installManifest serves the flat manifest that `kubectl apply -f` fetches.
// It authenticates on the registration token in the path, because kubectl
// cannot carry a KubeMG session — the token is the credential, and it is the
// same one the installed agent will use.
func (s *server) installManifest(c *gin.Context) {
	opts, _, ok := s.installTarget(c)
	if !ok {
		return
	}

	manifest, err := agentpkg.Manifest(opts)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.Data(http.StatusOK, "application/yaml; charset=utf-8", []byte(manifest))
}

// installArchive serves the Kustomize package as a tarball.
func (s *server) installArchive(c *gin.Context) {
	opts, cluster, ok := s.installTarget(c)
	if !ok {
		return
	}

	archive, err := agentpkg.Archive(opts)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.Header("Content-Disposition",
		fmt.Sprintf("attachment; filename=%q", cluster.Name+"-kubemg-agent.tar.gz"))
	c.Data(http.StatusOK, "application/gzip", archive)
}

// installTarget resolves the cluster behind a registration token and builds its
// render options. It writes the error response itself when it refuses.
func (s *server) installTarget(c *gin.Context) (agentpkg.Options, *db.Cluster, bool) {
	token := c.Param("token")
	if token == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "a registration token is required"})
		return agentpkg.Options{}, nil, false
	}

	cluster, err := s.store.ClusterByAgentToken(c.Request.Context(), token)
	if errors.Is(err, db.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "unknown registration token"})
		return agentpkg.Options{}, nil, false
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not resolve the registration token"})
		return agentpkg.Options{}, nil, false
	}
	if connectionMode(*cluster) != db.ModeAgent {
		c.JSON(http.StatusNotFound, gin.H{"error": "unknown registration token"})
		return agentpkg.Options{}, nil, false
	}

	// Caching an installer keyed by a secret would be a good way to leak it
	// through a shared proxy.
	c.Header("Cache-Control", "no-store")
	return s.agentOptions(c.Request.Context(), cluster.AgentToken), cluster, true
}

// agentOptions renders against the *effective* settings rather than the
// boot-time environment, so an operator who fixes the server URL in the console
// fixes every install command issued from then on without a redeploy.
func (s *server) agentOptions(ctx context.Context, token string) agentpkg.Options {
	settings := s.settings(ctx)
	return agentpkg.Options{
		BastionURL:   settings.PublicURL,
		ClusterToken: token,
		Namespace:    settings.AgentNamespace,
		Image:        settings.AgentImage,
	}
}

func (s *server) installURL(ctx context.Context, token, file string) string {
	return fmt.Sprintf("%s/install/%s/%s", s.settings(ctx).PublicURL, token, file)
}
