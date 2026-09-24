package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/agentpkg"
	"github.com/kubemg/kubemg/backend/pkg/bastion"
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
	// DownloadExpiresAt is when the one download ticket both URLs carry dies if
	// nobody has fetched either of them. Whichever is fetched first spends it.
	DownloadExpiresAt time.Time `json:"download_expires_at"`
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
//
// Every JSON read mints a fresh single-use download URL for the install
// commands — see agent_install.go. It never touches the tunnel credential:
// rotating that is its own route, because it takes the agent down.
func (s *server) clusterKustomize(c *gin.Context) {
	cluster, ok := s.agentCluster(c)
	if !ok {
		return
	}

	if c.Query("format") == "yaml" {
		manifest, err := agentpkg.Manifest(s.agentOptions(c.Request.Context(), cluster.AgentToken))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.Header("Cache-Control", "no-store")
		c.Header("Content-Disposition",
			fmt.Sprintf("attachment; filename=%q", cluster.Name+"-kubemg-agent.yaml"))
		c.Data(http.StatusOK, "application/yaml; charset=utf-8", []byte(manifest))
		return
	}

	envelope, err := s.agentInstallEnvelope(c.Request.Context(), cluster)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, envelope)
}

// agentCluster resolves the :id of an admin agent route and refuses a cluster
// that has no agent to install. It writes the error response itself.
func (s *server) agentCluster(c *gin.Context) (*db.Cluster, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid cluster id"})
		return nil, false
	}

	cluster, err := s.store.ClusterByID(c.Request.Context(), uint(id))
	if errors.Is(err, db.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "cluster not found"})
		return nil, false
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load cluster"})
		return nil, false
	}
	if connectionMode(*cluster) != db.ModeAgent {
		c.JSON(http.StatusConflict, gin.H{
			"error": "this cluster is registered for direct API access and has no agent to install",
		})
		return nil, false
	}
	if cluster.AgentToken == "" {
		c.JSON(http.StatusConflict, gin.H{
			"error": "this cluster has no registration token; re-register it in agent mode",
		})
		return nil, false
	}
	return cluster, true
}

// agentInstallEnvelope renders the package for a cluster and mints the
// single-use download ticket its commands carry.
func (s *server) agentInstallEnvelope(ctx context.Context, cluster *db.Cluster) (agentInstallResponse, error) {
	opts := s.agentOptions(ctx, cluster.AgentToken)
	files, err := agentpkg.Render(opts)
	if err != nil {
		return agentInstallResponse{}, err
	}
	manifest, err := agentpkg.Manifest(opts)
	if err != nil {
		return agentInstallResponse{}, err
	}
	ticket, expiresAt, err := s.mintInstallTicket(ctx, cluster.ID)
	if err != nil {
		s.log().Error("could not mint an agent install download",
			slog.Uint64("cluster_id", uint64(cluster.ID)),
			slog.String("error", err.Error()))
		return agentInstallResponse{}, errors.New("could not mint an install download URL")
	}

	manifestURL := s.installURL(ctx, ticket, "agent.yaml")
	archiveURL := s.installURL(ctx, ticket, "kustomize.tar.gz")

	return agentInstallResponse{
		ClusterID:         cluster.ID,
		Cluster:           cluster.Name,
		Namespace:         opts.Namespace,
		Image:             opts.Image,
		BastionURL:        opts.BastionURL,
		PackageDir:        agentpkg.PackageDir,
		AgentToken:        cluster.AgentToken,
		ManifestURL:       manifestURL,
		ArchiveURL:        archiveURL,
		DownloadExpiresAt: expiresAt,
		ApplyCommand:      applyCommand(manifestURL, opts.BastionCA != ""),
		// Kustomize only accepts local paths and Git specs as remote targets,
		// so the package is fetched and extracted before `apply -k` sees it.
		KustomizeCommand: fmt.Sprintf(
			"curl -sfL%s %s | tar -xz\nkubectl apply -k %s",
			curlInsecureFlag(opts.BastionCA != ""), archiveURL, agentpkg.PackageDir),
		Manifest: manifest,
		Files:    files,
	}, nil
}

// applyCommand renders the one-liner an operator pastes. `kubectl apply -f
// <url>` fetches over the operator's own trust store, which a self-signed
// bastion is not in — and kubectl has no flag for that hop, since
// --insecure-skip-tls-verify applies to the cluster's API server, not to a
// manifest URL. So the fetch moves to curl, which does have one.
func applyCommand(manifestURL string, selfSigned bool) string {
	if !selfSigned {
		return fmt.Sprintf("kubectl apply -f %s", manifestURL)
	}
	return fmt.Sprintf("curl -sfL%s %s | kubectl apply -f -",
		curlInsecureFlag(true), manifestURL)
}

// curlInsecureFlag is the bootstrap concession: the manifest carries the CA the
// agent will pin, so this one fetch is the only hop that cannot verify it yet.
// It is scoped to that fetch rather than being a setting anyone can leave on.
func curlInsecureFlag(selfSigned bool) string {
	if selfSigned {
		return "k"
	}
	return ""
}

// installManifest serves the flat manifest that `kubectl apply -f` fetches.
// It authenticates on the single-use download ticket in the path, because
// kubectl cannot carry a KubeMG session.
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

// installTarget redeems the download ticket in the path and builds the render
// options for the cluster it was minted for. It writes the error response
// itself when it refuses.
func (s *server) installTarget(c *gin.Context) (agentpkg.Options, *db.Cluster, bool) {
	// Caching an installer keyed by a secret would be a good way to leak it
	// through a shared proxy — and a cached copy would outlive the single use.
	c.Header("Cache-Control", "no-store")

	ticket := c.Param("token")
	if ticket == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "an install download ticket is required"})
		return agentpkg.Options{}, nil, false
	}
	// An install URL from before download tickets carried the tunnel credential
	// itself. It is refused by its shape, without ever being looked up: every
	// such URL that was ever pasted somewhere must stop working, and answering
	// "valid" or "unknown" differently would still be an oracle for the token.
	if bastion.LooksLikeAgentToken(ticket) {
		c.JSON(http.StatusGone, gin.H{"error": legacyInstallURLMessage})
		return agentpkg.Options{}, nil, false
	}

	ctx := c.Request.Context()
	clusterID, found, err := s.store.TakeAgentInstallTicket(ctx, bastion.HashInstallTicket(ticket))
	if err != nil {
		// A store that cannot be read refuses: a download it cannot vouch for
		// hands out a tunnel credential.
		s.log().Error("could not redeem an agent install download", slog.String("error", err.Error()))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not redeem the install download URL"})
		return agentpkg.Options{}, nil, false
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": spentInstallURLMessage})
		return agentpkg.Options{}, nil, false
	}

	cluster, err := s.store.ClusterByID(ctx, clusterID)
	if err != nil || connectionMode(*cluster) != db.ModeAgent || cluster.AgentToken == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": spentInstallURLMessage})
		return agentpkg.Options{}, nil, false
	}
	return s.agentOptions(ctx, cluster.AgentToken), cluster, true
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
		// The CA is the server's own listener certificate, so it is boot-time
		// configuration rather than a runtime setting: changing it means
		// restarting with different TLS material anyway.
		BastionCA: s.bastionCA,
	}
}

func (s *server) installURL(ctx context.Context, ticket, file string) string {
	return fmt.Sprintf("%s/install/%s/%s", s.settings(ctx).PublicURL, ticket, file)
}
