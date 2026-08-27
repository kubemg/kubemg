package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/apptemplate"
	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * A template is a manifest bundle with holes in it, browsed the way a chart
 * catalogue is.
 *
 * It lives under its own top-level path rather than under `/clusters` for the
 * same reason a chart repository does: what templates exist is a fact about
 * this installation, not about any one cluster, and a per-cluster copy would
 * mean an administrator writing the same bundle once per cluster. And it takes
 * the same read/write split as a chart repository for the same reason: writing
 * one decides what a single click in the console offers to create, which is an
 * administrative act, while an install form that could not list what is
 * available is a form nobody can use — reading has to stay open to anyone the
 * console is open to.
 *
 * Rendering (`POST /:name/render`) stops at YAML text. Creating what it
 * describes is the existing per-object `POST /clusters/:id/resources/object`
 * path, one object at a time — this file never talks to a cluster.
 */

// appTemplateName is a slug, matching a chart's own naming: a template is
// addressed the way a release name is, not free text.
var appTemplateName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// templateResponse is a template as any signed-in caller sees it.
type templateResponse struct {
	Name        string                  `json:"name"`
	Title       string                  `json:"title,omitempty"`
	Description string                  `json:"description,omitempty"`
	Manifests   string                  `json:"manifests"`
	Parameters  []apptemplate.Parameter `json:"parameters"`
	Seeded      bool                    `json:"seeded"`
	CreatedBy   string                  `json:"created_by,omitempty"`
	UpdatedAt   time.Time               `json:"updated_at"`
}

func toTemplateResponse(template db.AppTemplate) templateResponse {
	params := template.Params()
	if params == nil {
		params = []apptemplate.Parameter{}
	}
	return templateResponse{
		Name:        template.Name,
		Title:       template.Title,
		Description: template.Description,
		Manifests:   template.Manifests,
		Parameters:  params,
		Seeded:      template.Seeded,
		CreatedBy:   template.CreatedBy,
		UpdatedAt:   template.UpdatedAt,
	}
}

// listAppTemplates answers the catalogue.
func (s *server) listAppTemplates(c *gin.Context) {
	templates, err := s.store.AppTemplates(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "the templates could not be read"})
		return
	}
	out := make([]templateResponse, 0, len(templates))
	for _, template := range templates {
		out = append(out, toTemplateResponse(template))
	}
	c.JSON(http.StatusOK, gin.H{"templates": out})
}

// showAppTemplate reads one.
func (s *server) showAppTemplate(c *gin.Context) {
	name := strings.TrimSpace(c.Param("name"))
	template, err := s.store.AppTemplate(c.Request.Context(), name)
	switch {
	case errors.Is(err, db.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "no template named " + name})
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "the template could not be read"})
	default:
		c.JSON(http.StatusOK, gin.H{"template": toTemplateResponse(*template)})
	}
}

// templateRequest is what an administrator submits.
type templateRequest struct {
	Name        string                  `json:"name"`
	Title       string                  `json:"title"`
	Description string                  `json:"description"`
	Manifests   string                  `json:"manifests"`
	Parameters  []apptemplate.Parameter `json:"parameters"`
}

// putAppTemplate declares a template, or edits one.
//
// Validation runs before anything is written, exactly as a chart repository's
// index fetch does — a bundle that cannot ever produce valid YAML is refused
// here, at save time, rather than discovered the first time someone renders
// it.
func (s *server) putAppTemplate(c *gin.Context) {
	name := strings.TrimSpace(c.Param("name"))
	if !appTemplateName.MatchString(name) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "the template name is not a valid slug"})
		return
	}

	var request templateRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "the template could not be read"})
		return
	}
	// The name is the address, exactly as a chart repository's is: a body
	// naming a different template is refused rather than silently renaming the
	// row the URL addressed.
	if submitted := strings.TrimSpace(request.Name); submitted != "" && submitted != name {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "the template in the body is not the one in the address",
		})
		return
	}

	if request.Parameters == nil {
		request.Parameters = []apptemplate.Parameter{}
	}
	if err := apptemplate.ValidateBundle(request.Manifests, request.Parameters); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := c.Request.Context()
	stored, err := s.store.AppTemplate(ctx, name)
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "the template could not be read"})
		return
	}

	caller, ok := s.currentUser(c)
	if !ok {
		return
	}

	template := db.AppTemplate{
		Name:        name,
		Title:       strings.TrimSpace(request.Title),
		Description: strings.TrimSpace(request.Description),
		Manifests:   request.Manifests,
		Parameters:  encodeParams(request.Parameters),
		CreatedBy:   caller.Username,
	}
	if stored != nil {
		template.ID = stored.ID
		template.Seeded = stored.Seeded
		// CreatedBy is set on first write and left alone afterwards — an edit
		// records who last saved the bundle nowhere else, and overwriting it
		// on every save would erase who actually wrote it.
		template.CreatedBy = stored.CreatedBy
	}

	if err := s.store.PutAppTemplate(ctx, &template); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "the template could not be saved"})
		return
	}

	status := http.StatusOK
	if stored == nil {
		status = http.StatusCreated
	}
	c.JSON(status, gin.H{"template": toTemplateResponse(template)})
}

// deleteAppTemplate removes a template. A seeded row deletes like any other.
func (s *server) deleteAppTemplate(c *gin.Context) {
	name := strings.TrimSpace(c.Param("name"))
	err := s.store.DeleteAppTemplate(c.Request.Context(), name)
	switch {
	case errors.Is(err, db.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "no template named " + name})
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "the template could not be removed"})
	default:
		c.JSON(http.StatusOK, gin.H{"message": name + " removed"})
	}
}

// renderRequest is what a render call submits.
type renderRequest struct {
	Values map[string]string `json:"values"`
}

// renderAppTemplate resolves a bundle's parameters into YAML and stops —
// creating what it describes is the existing per-object create route.
func (s *server) renderAppTemplate(c *gin.Context) {
	name := strings.TrimSpace(c.Param("name"))
	template, err := s.store.AppTemplate(c.Request.Context(), name)
	switch {
	case errors.Is(err, db.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "no template named " + name})
		return
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "the template could not be read"})
		return
	}

	// A missing or empty body renders with every default, which is a
	// legitimate request — Gin's JSON binder only errors when a body is
	// present and malformed, so a bind error here is a real one.
	var request renderRequest
	if c.Request.ContentLength != 0 {
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "the render request could not be read"})
			return
		}
	}

	rendered, err := apptemplate.Render(template.Manifests, template.Params(), request.Values)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	objects, err := apptemplate.Objects(rendered)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"objects": objects, "manifests": rendered})
}

// draftRequest is what the "start from a live object" form submits.
type draftRequest struct {
	YAML string `json:"yaml"`
}

// draftAppTemplate turns one live object's manifest into a starter bundle. It
// writes nothing — the caller still has to review and save it through
// putAppTemplate, exactly as a chart's rendered objects still have to be
// created through the per-object route.
func (s *server) draftAppTemplate(c *gin.Context) {
	var request draftRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "the object could not be read"})
		return
	}
	manifests, params, err := apptemplate.Draft(request.YAML)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"manifests": manifests, "parameters": params})
}

// encodeParams stores a parameter declaration the way HelmChartRowsOf stores a
// version list: as JSON, because it is never queried, only ever read back
// whole.
func encodeParams(params []apptemplate.Parameter) string {
	if len(params) == 0 {
		return "[]"
	}
	// Parameter is a struct of plain strings and a bool — there is nothing in
	// the type that json.Marshal can fail on, unlike a value straight from an
	// unvalidated request body.
	encoded, _ := json.Marshal(params)
	return string(encoded)
}
