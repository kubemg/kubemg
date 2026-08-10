package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/db"
	"github.com/kubemg/kubemg/backend/pkg/observability"
)

/*
 * The other consoles a cluster is operated from.
 *
 * Reading follows the datasource rule exactly — anyone the cluster is granted to
 * can see where its Grafana is, because you cannot be shown a chart or told to
 * go and look at a dashboard in a place you are not allowed to know exists —
 * while registering one is administrative.
 *
 * Nothing here is a credential and nothing here is access. KubeMG stores an
 * address, opens nothing, and the operator signs in to the other tool as
 * themselves; that is why a link is safe to show as widely as the cluster is.
 */

// consoleResponse is one registered console as the UI sees it.
type consoleResponse struct {
	Kind string `json:"kind"`
	URL  string `json:"url"`
	Ref  string `json:"ref,omitempty"`

	UpdatedAt time.Time `json:"updated_at"`
}

func toConsoleResponse(console db.ClusterConsole) consoleResponse {
	return consoleResponse{
		Kind:      console.Kind,
		URL:       console.URL,
		Ref:       console.Ref,
		UpdatedAt: console.UpdatedAt,
	}
}

// consoleRequest is the console form. There is very little to it on purpose:
// an address, and at most the one identifier that opens the target on the right
// thing rather than on its front page.
type consoleRequest struct {
	URL string `json:"url" binding:"required"`
	Ref string `json:"ref"`
}

func consoleKind(c *gin.Context) (string, bool) {
	kind := c.Param("kind")
	if !db.ValidConsoleKind(kind) {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "a console is either grafana or argocd",
		})
		return "", false
	}
	return kind, true
}

// listClusterConsoles returns the consoles registered for a cluster, alongside
// the datasource UIs derived from what it already declares.
func (s *server) listClusterConsoles(c *gin.Context) {
	user, cluster, _, _, ok := s.loadAuthorizedCluster(c)
	if !ok {
		return
	}

	ctx := c.Request.Context()
	consoles, err := s.store.ClusterConsoles(ctx, cluster.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load the consoles"})
		return
	}

	out := make([]consoleResponse, 0, len(consoles))
	for _, console := range consoles {
		out = append(out, toConsoleResponse(console))
	}

	c.JSON(http.StatusOK, gin.H{
		"consoles": out,
		// The datasource's own UI is *derived*, never stored: it is the address
		// the cluster already declared with the provider's UI path on the end, so
		// storing it would be storing the same fact twice and letting the two
		// disagree the first time somebody moves a Prometheus.
		"datasource_uis": s.datasourceUIs(ctx, cluster.ID),
		"editable":       user.IsAdmin(),
	})
}

// datasourceUIView is one datasource's own query UI.
type datasourceUIView struct {
	Kind     string `json:"kind"`
	Provider string `json:"provider"`
	URL      string `json:"url"`
}

// datasourceUIs renders the datasource UIs a cluster's registered sources reach.
// A source failing to load is reported as no UI rather than as an error: the
// links are an aid on a page whose subject is something else.
func (s *server) datasourceUIs(ctx context.Context, clusterID uint) []datasourceUIView {
	views := []datasourceUIView{}
	sources, err := s.store.ObservabilitySources(ctx, clusterID)
	if err != nil {
		return views
	}
	for _, source := range sources {
		url := observability.DatasourceUI(source)
		if url == "" {
			continue
		}
		views = append(views, datasourceUIView{
			Kind:     source.Kind,
			Provider: source.Provider,
			URL:      url,
		})
	}
	return views
}

// putClusterConsole registers or replaces one console (admin only).
func (s *server) putClusterConsole(c *gin.Context) {
	_, cluster, _, _, ok := s.loadAuthorizedCluster(c)
	if !ok {
		return
	}
	kind, ok := consoleKind(c)
	if !ok {
		return
	}

	var req consoleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	url, err := observability.NormalizeConsoleURL(req.URL)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	ref, err := observability.NormalizeConsoleRef(req.Ref)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	console := db.ClusterConsole{ClusterID: cluster.ID, Kind: kind, URL: url, Ref: ref}
	if err := s.store.PutClusterConsole(c.Request.Context(), &console); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not save the console"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"console": toConsoleResponse(console)})
}

// deleteClusterConsole removes one console (admin only).
func (s *server) deleteClusterConsole(c *gin.Context) {
	_, cluster, _, _, ok := s.loadAuthorizedCluster(c)
	if !ok {
		return
	}
	kind, ok := consoleKind(c)
	if !ok {
		return
	}

	err := s.store.DeleteClusterConsole(c.Request.Context(), cluster.ID, kind)
	if errors.Is(err, db.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "this cluster has no " + kind + " console"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not remove the console"})
		return
	}

	c.Status(http.StatusNoContent)
}

// clusterConsole loads one console for a cluster, or nil when there is none.
// A failure to read is nil too — every caller is decorating a response with a
// link, and none of them should fail for the want of one.
func (s *server) clusterConsole(ctx context.Context, clusterID uint, kind string) *db.ClusterConsole {
	consoles, err := s.store.ClusterConsoles(ctx, clusterID)
	if err != nil {
		return nil
	}
	for _, console := range consoles {
		if console.Kind == kind {
			return &console
		}
	}
	return nil
}

// grafanaExploreFor builds the Explore link that opens the query KubeMG just
// ran, or "" when the cluster has no Grafana registered or the datasource has no
// uid in it.
//
// It is built here rather than in the browser because the query is the server's:
// a caller names a chart from the catalogue and the server writes the PromQL
// around their scope, so a browser assembling its own Explore link would be a
// browser writing a query — the one thing the whole query path exists to
// prevent.
func (s *server) grafanaExploreFor(ctx context.Context, clusterID uint,
	source db.ObservabilitySource, query string, start, end time.Time,
) string {
	if source.GrafanaDatasource == "" || query == "" {
		return ""
	}
	console := s.clusterConsole(ctx, clusterID, db.ConsoleGrafana)
	if console == nil {
		return ""
	}
	return observability.GrafanaExplore(console.URL, source.GrafanaDatasource,
		source.Provider, query, start, end)
}
