package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/db"
	"github.com/kubemg/kubemg/backend/pkg/helm"
)

/*
 * Declaring where charts come from, and browsing what they hold.
 *
 * Two audiences, one table, and the split between them is the interesting part.
 *
 * **Writing is admin-only**, because adding a repository is an outbound-egress
 * decision: it tells the bastion to reach a host on a schedule and execute what
 * it downloads as a Go template. That is the same class of act as registering an
 * alarm channel, and it is not one a developer with a cluster grant makes.
 *
 * **Reading the catalogue is open to anyone the console is open to.** This is
 * the recording-policy precedent rather than an oversight: an install form has
 * to offer a list of repositories and charts, and a form that discovers the list
 * by being refused is a form nobody can use. What is readable is a list of
 * public chart names and versions — it says nothing about the fleet, and the one
 * thing in the row that would, the credential, is `json:"-"` on the model and
 * reported as a boolean by the DTO.
 *
 * Nothing here is per-cluster. See `pkg/db/helm_models.go`.
 */

const (
	// maxCatalogueResults bounds one catalogue read. A catalogue is searched
	// rather than scrolled — bitnami alone publishes several hundred charts —
	// and an unbounded list would put the whole index through the response.
	maxCatalogueResults = 200
	// defaultCatalogueResults is what a search with no limit gets.
	defaultCatalogueResults = 50
)

// repositoryResponse is a repository as a client sees it: everything about it
// except what it authenticates with.
type repositoryResponse struct {
	Name          string     `json:"name"`
	URL           string     `json:"url"`
	Description   string     `json:"description,omitempty"`
	Username      string     `json:"username,omitempty"`
	HasCredential bool       `json:"has_credential"`
	Seeded        bool       `json:"seeded"`
	Status        string     `json:"status"`
	StatusMessage string     `json:"status_message,omitempty"`
	ChartCount    int        `json:"chart_count"`
	SyncedAt      *time.Time `json:"synced_at,omitempty"`
}

func toRepositoryResponse(repository db.HelmRepository) repositoryResponse {
	return repositoryResponse{
		Name:          repository.Name,
		URL:           repository.URL,
		Description:   repository.Description,
		Username:      repository.Username,
		HasCredential: repository.HasCredential(),
		Seeded:        repository.Seeded,
		Status:        repository.Status,
		StatusMessage: repository.StatusMessage,
		ChartCount:    repository.ChartCount,
		SyncedAt:      repository.SyncedAt,
	}
}

// repositoryRequest is what an admin submits.
//
// `Credential` is a pointer for exactly the reason it is one on an observability
// source: three states have to be distinguishable, and only a pointer
// distinguishes them. Absent means "keep what is stored" — the form never
// received the credential and cannot echo it back. An empty string means "clear
// it", which is what an operator does when a repository stops being private. A
// value means "replace it".
type repositoryRequest struct {
	Name        string  `json:"name"`
	URL         string  `json:"url"`
	Description string  `json:"description"`
	Username    string  `json:"username"`
	Credential  *string `json:"credential"`
}

// credentialFor resolves the submitted credential against the stored one.
func (r repositoryRequest) credentialFor(stored *db.HelmRepository) string {
	switch {
	case r.Credential != nil:
		return *r.Credential
	case stored != nil:
		return stored.Credential
	default:
		return ""
	}
}

// listHelmRepositories answers the catalogue's top level.
func (s *server) listHelmRepositories(c *gin.Context) {
	repositories, err := s.store.HelmRepositories(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "the repositories could not be read"})
		return
	}

	out := make([]repositoryResponse, 0, len(repositories))
	for _, repository := range repositories {
		out = append(out, toRepositoryResponse(repository))
	}
	c.JSON(http.StatusOK, gin.H{"repositories": out})
}

// putHelmRepository declares a repository, or edits one.
//
// The index is fetched **synchronously here**, before the row is reported as
// good, for the same reason a datasource save runs a real probe: a form that
// accepts a URL and reports success, leaving the operator to discover from a
// status column three minutes later that it was a typo, is a form that lies. A
// repository that cannot be read is still *stored* — the operator may be adding
// it before the network is open to it — but it is stored with its own reason
// attached rather than as a success.
func (s *server) putHelmRepository(c *gin.Context) {
	name := strings.TrimSpace(c.Param("name"))

	var request repositoryRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "the repository could not be read"})
		return
	}
	// The name is the address. A body naming a different one is refused rather
	// than silently renaming the row the URL addressed — the same rule the
	// create path applies to a namespace.
	if submitted := strings.TrimSpace(request.Name); submitted != "" && submitted != name {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "the repository in the body is not the one in the address",
		})
		return
	}

	ctx := c.Request.Context()
	stored, err := s.store.HelmRepository(ctx, name)
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "the repository could not be read"})
		return
	}

	repository := db.HelmRepository{
		Name:        name,
		URL:         strings.TrimSpace(request.URL),
		Description: strings.TrimSpace(request.Description),
		Username:    strings.TrimSpace(request.Username),
		Credential:  request.credentialFor(stored),
	}
	if stored != nil {
		repository.ID = stored.ID
		repository.Seeded = stored.Seeded
	}
	if err := repository.Repository().Validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// The fetch happens against the resolved row rather than the request, so an
	// edit that omitted the credential is checked with the stored one — which
	// is the case that would otherwise report a private repository as broken
	// every time somebody changed its description.
	charts, fetchErr := helm.FetchIndex(ctx, s.helmClient(), repository.Repository())
	if fetchErr != nil {
		repository.Status = db.HelmRepoError
		repository.StatusMessage = fetchErr.Error()
	} else {
		repository.Status = db.HelmRepoOK
		repository.StatusMessage = ""
	}

	if err := s.store.PutHelmRepository(ctx, &repository); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "the repository could not be saved"})
		return
	}

	if fetchErr == nil {
		if err := s.storeCatalogue(ctx, &repository, charts); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "the chart list could not be saved"})
			return
		}
	}

	status := http.StatusOK
	if stored == nil {
		status = http.StatusCreated
	}
	response := gin.H{"repository": toRepositoryResponse(repository)}
	if fetchErr != nil {
		// A 200 with a reason rather than a 4xx: the row was written, which is
		// what the caller asked for, and the reason belongs beside it.
		response["warning"] = "Saved, but the chart list could not be read: " + fetchErr.Error()
	}
	c.JSON(status, response)
}

// deleteHelmRepository removes a repository and its charts.
//
// A seeded row deletes like any other. That is the whole point of seeding rows
// rather than hard-coding a list: an air-gapped site removes all six and adds
// its mirror, and the seed marker means they stay removed.
func (s *server) deleteHelmRepository(c *gin.Context) {
	name := strings.TrimSpace(c.Param("name"))
	err := s.store.DeleteHelmRepository(c.Request.Context(), name)
	switch {
	case errors.Is(err, db.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "no repository named " + name})
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "the repository could not be removed"})
	default:
		c.JSON(http.StatusOK, gin.H{"message": name + " removed"})
	}
}

// syncHelmRepository re-reads one repository now.
//
// The scheduled sync is what keeps a catalogue current; this is what an operator
// presses after fixing a proxy, because waiting out an interval to find out
// whether a fix worked is how a five-second check becomes a ten-minute one.
func (s *server) syncHelmRepository(c *gin.Context) {
	name := strings.TrimSpace(c.Param("name"))
	ctx := c.Request.Context()

	repository, err := s.store.HelmRepository(ctx, name)
	switch {
	case errors.Is(err, db.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "no repository named " + name})
		return
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "the repository could not be read"})
		return
	}

	if err := s.syncRepository(ctx, repository); err != nil {
		// The row now carries the reason, so the response reports the refreshed
		// row rather than only the error: the console re-renders one row either
		// way, and a failed sync leaves a repository still serving its last
		// catalogue.
		refreshed, readErr := s.store.HelmRepository(ctx, name)
		if readErr == nil {
			repository = refreshed
		}
		c.JSON(http.StatusOK, gin.H{
			"repository": toRepositoryResponse(*repository),
			"warning":    "The chart list could not be read: " + err.Error(),
		})
		return
	}

	refreshed, err := s.store.HelmRepository(ctx, name)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "the repository could not be read"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"repository": toRepositoryResponse(*refreshed)})
}

/* ------------------------------------------------------------- catalogue --- */

// chartResponse is one chart in a catalogue listing.
type chartResponse struct {
	Repository  string              `json:"repository"`
	Name        string              `json:"name"`
	Description string              `json:"description,omitempty"`
	Icon        string              `json:"icon,omitempty"`
	Home        string              `json:"home,omitempty"`
	Deprecated  bool                `json:"deprecated,omitempty"`
	Latest      string              `json:"latest_version,omitempty"`
	Versions    []helm.ChartVersion `json:"versions"`
}

// listHelmCharts searches one repository's catalogue.
func (s *server) listHelmCharts(c *gin.Context) {
	ctx := c.Request.Context()

	repository, err := s.store.HelmRepository(ctx, strings.TrimSpace(c.Param("name")))
	switch {
	case errors.Is(err, db.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "no repository named " + c.Param("name")})
		return
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "the repository could not be read"})
		return
	}

	charts, err := s.store.HelmCharts(ctx, repository.ID,
		c.Query("q"), catalogueLimit(c.Query("limit")))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "the chart list could not be read"})
		return
	}

	out := make([]chartResponse, 0, len(charts))
	for _, stored := range charts {
		chart := stored.Chart()
		out = append(out, chartResponse{
			Repository:  repository.Name,
			Name:        chart.Name,
			Description: chart.Description,
			Icon:        chart.Icon,
			Home:        chart.Home,
			Deprecated:  chart.Deprecated,
			Latest:      chart.LatestVersion(),
			Versions:    chart.Versions,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"repository": toRepositoryResponse(*repository),
		"charts":     out,
		// A catalogue that was cut off has to say so for the same reason a
		// truncated list does: a search that silently returned the first fifty
		// of two hundred reads as "this repository does not have it".
		"truncated": len(charts) >= catalogueLimit(c.Query("limit")),
	})
}

// catalogueLimit reads the requested page size, bounded. An unreadable or
// out-of-range value is the default rather than an error: it is a page size,
// and refusing a request over one would be a 400 nobody can act on.
func catalogueLimit(raw string) int {
	limit, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || limit <= 0 {
		return defaultCatalogueResults
	}
	return min(limit, maxCatalogueResults)
}

// storeCatalogue writes a fetched catalogue and the health that goes with it.
func (s *server) storeCatalogue(ctx context.Context, repository *db.HelmRepository,
	charts []helm.Chart,
) error {
	rows := db.HelmChartRowsOf(repository.ID, charts)
	if err := s.store.ReplaceHelmCharts(ctx, repository.ID, rows); err != nil {
		return err
	}

	now := time.Now().UTC()
	repository.ChartCount = len(rows)
	repository.SyncedAt = &now
	return s.store.UpdateHelmRepositoryHealth(ctx, repository.ID,
		repository.Status, repository.StatusMessage, len(rows), &now)
}
