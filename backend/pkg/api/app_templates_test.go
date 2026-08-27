package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/kubemg/kubemg/backend/pkg/apptemplate"
	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * A template renders to YAML and stops — creating what it describes is the
 * existing per-object create route. What is pinned here is the read/write
 * split (the same one a chart repository takes, and for the same reason), the
 * name-is-the-address rule, and that a bundle which cannot ever render is
 * refused at save time.
 */

const webServiceManifests = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: ${name}
spec:
  replicas: ${replicas}
  template:
    spec:
      containers:
        - name: ${name}
          image: ${image}
`

var webServiceParams = []apptemplate.Parameter{
	{Name: "name", Type: "string", Required: true, Default: "web"},
	{Name: "image", Type: "string", Required: true, Default: "nginx:1.27"},
	{Name: "replicas", Type: "number", Default: "2"},
}

func TestOnlyAnAdminMayDeclareATemplate(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("dev", "secret123", "user")

	rec := env.do(t, http.MethodPut, "/api/v1/app-templates/web", env.tokenFor(t, user),
		map[string]any{"manifests": webServiceManifests, "parameters": webServiceParams})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected %d, got %d (%s)", http.StatusForbidden, rec.Code, rec.Body.String())
	}
}

func TestAnyoneSignedInMayReadTheTemplateCatalogue(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")
	dev := env.store.addUser("dev", "secret123", "user")

	if rec := env.do(t, http.MethodPut, "/api/v1/app-templates/web", env.tokenFor(t, admin),
		map[string]any{"manifests": webServiceManifests, "parameters": webServiceParams}); rec.Code != http.StatusCreated {
		t.Fatalf("declaring the template: %d (%s)", rec.Code, rec.Body.String())
	}

	if rec := env.do(t, http.MethodGet, "/api/v1/app-templates", env.tokenFor(t, dev), nil); rec.Code != http.StatusOK {
		t.Fatalf("reading the list: %d (%s)", rec.Code, rec.Body.String())
	}
	if rec := env.do(t, http.MethodGet, "/api/v1/app-templates/web", env.tokenFor(t, dev), nil); rec.Code != http.StatusOK {
		t.Fatalf("reading one template: %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestPutRefusesABundleWithAnUndeclaredPlaceholder(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")

	rec := env.do(t, http.MethodPut, "/api/v1/app-templates/web", env.tokenFor(t, admin),
		map[string]any{
			"manifests":  "metadata:\n  name: ${name}\n  extra: ${nope}\n",
			"parameters": []apptemplate.Parameter{{Name: "name", Type: "string", Required: true}},
		})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d (%s)", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
}

func TestPutRefusesABundleWithAMissingRequiredDefault(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")

	// A number parameter with no default at all still validates (the probe
	// fills in "1"); a bad number *default* is what ValidateParameters itself
	// refuses.
	rec := env.do(t, http.MethodPut, "/api/v1/app-templates/web", env.tokenFor(t, admin),
		map[string]any{
			"manifests": "replicas: ${replicas}\n",
			"parameters": []apptemplate.Parameter{
				{Name: "replicas", Type: "number", Default: "not-a-number"},
			},
		})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d (%s)", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
}

func TestTheNameInTheBodyMayNotRenameTheTemplateTheAddressNames(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")

	rec := env.do(t, http.MethodPut, "/api/v1/app-templates/web", env.tokenFor(t, admin),
		map[string]any{
			"name": "somewhere-else", "manifests": webServiceManifests, "parameters": webServiceParams,
		})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d (%s)", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
}

func TestAnUnknownTemplateIsAFourOhFour(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")

	rec := env.do(t, http.MethodGet, "/api/v1/app-templates/nowhere", env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected %d, got %d (%s)", http.StatusNotFound, rec.Code, rec.Body.String())
	}
	rec = env.do(t, http.MethodPost, "/api/v1/app-templates/nowhere/render", env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected %d, got %d (%s)", http.StatusNotFound, rec.Code, rec.Body.String())
	}
}

func TestPuttingATemplateSetsCreatedByOnFirstWriteOnly(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")
	other := env.store.addUser("other-admin", "secret123", "admin")

	env.do(t, http.MethodPut, "/api/v1/app-templates/web", env.tokenFor(t, admin),
		map[string]any{"manifests": webServiceManifests, "parameters": webServiceParams})
	env.do(t, http.MethodPut, "/api/v1/app-templates/web", env.tokenFor(t, other),
		map[string]any{"title": "renamed", "manifests": webServiceManifests, "parameters": webServiceParams})

	stored := env.store.appTemplates["web"]
	if stored.CreatedBy != "admin" {
		t.Fatalf("created_by = %q, want the first writer kept", stored.CreatedBy)
	}
	if stored.Title != "renamed" {
		t.Fatalf("the edit did not land: %+v", stored)
	}
}

func TestPutPreservesSeededOnAnAdminEdit(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")
	env.store.appTemplates["web"] = &db.AppTemplate{
		ID: 5, Name: "web", Manifests: webServiceManifests, Seeded: true,
	}

	rec := env.do(t, http.MethodPut, "/api/v1/app-templates/web", env.tokenFor(t, admin),
		map[string]any{"manifests": webServiceManifests, "parameters": webServiceParams})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	if !env.store.appTemplates["web"].Seeded {
		t.Fatal("an edit of a seeded template must not clear Seeded")
	}
}

func TestASeededTemplateDeletesLikeAnyOther(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")
	env.store.appTemplates["web"] = &db.AppTemplate{ID: 5, Name: "web", Seeded: true}

	rec := env.do(t, http.MethodDelete, "/api/v1/app-templates/web", env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	if _, still := env.store.appTemplates["web"]; still {
		t.Fatal("a seeded template could not be removed")
	}
}

func TestOnlyAnAdminMayDeleteATemplate(t *testing.T) {
	env := newTestEnv(t)
	dev := env.store.addUser("dev", "secret123", "user")
	env.store.appTemplates["web"] = &db.AppTemplate{ID: 5, Name: "web"}

	rec := env.do(t, http.MethodDelete, "/api/v1/app-templates/web", env.tokenFor(t, dev), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected %d, got %d (%s)", http.StatusForbidden, rec.Code, rec.Body.String())
	}
}

func TestRenderingATemplateSplitsIntoTheExpectedObjects(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")
	env.do(t, http.MethodPut, "/api/v1/app-templates/web", env.tokenFor(t, admin),
		map[string]any{"manifests": webServiceManifests, "parameters": webServiceParams})

	rec := env.do(t, http.MethodPost, "/api/v1/app-templates/web/render", env.tokenFor(t, admin),
		map[string]any{"values": map[string]string{"name": "api", "image": "example.com/api:1.0", "replicas": "3"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}

	var body struct {
		Objects   []apptemplate.Object `json:"objects"`
		Manifests string               `json:"manifests"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(body.Objects) != 1 {
		t.Fatalf("expected 1 object, got %d: %+v", len(body.Objects), body.Objects)
	}
	if body.Objects[0].Kind != "Deployment" || body.Objects[0].Name != "api" {
		t.Fatalf("object: %+v", body.Objects[0])
	}
}

func TestRenderingRefusesAnUndeclaredValue(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")
	env.do(t, http.MethodPut, "/api/v1/app-templates/web", env.tokenFor(t, admin),
		map[string]any{"manifests": webServiceManifests, "parameters": webServiceParams})

	rec := env.do(t, http.MethodPost, "/api/v1/app-templates/web/render", env.tokenFor(t, admin),
		map[string]any{"values": map[string]string{"nope": "x"}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d (%s)", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
}

func TestDraftingATemplateFromALiveObject(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")

	object := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cfg\n  namespace: default\n  uid: abc\n"
	rec := env.do(t, http.MethodPost, "/api/v1/app-templates/draft", env.tokenFor(t, admin),
		map[string]any{"yaml": object})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}

	var body struct {
		Manifests  string                  `json:"manifests"`
		Parameters []apptemplate.Parameter `json:"parameters"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(body.Parameters) != 1 || body.Parameters[0].Name != "name" {
		t.Fatalf("parameters: %+v", body.Parameters)
	}
	if err := apptemplate.ValidateBundle(body.Manifests, body.Parameters); err != nil {
		t.Fatalf("draft output must satisfy ValidateBundle: %v", err)
	}
}

func TestNonAdminMayDraftAndRenderButNotWrite(t *testing.T) {
	// Drafting and rendering are read-shaped: they don't touch what the
	// catalogue offers, so they follow the read half of the split.
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")
	dev := env.store.addUser("dev", "secret123", "user")
	env.do(t, http.MethodPut, "/api/v1/app-templates/web", env.tokenFor(t, admin),
		map[string]any{"manifests": webServiceManifests, "parameters": webServiceParams})

	object := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cfg\n"
	if rec := env.do(t, http.MethodPost, "/api/v1/app-templates/draft", env.tokenFor(t, dev),
		map[string]any{"yaml": object}); rec.Code != http.StatusOK {
		t.Fatalf("draft: %d (%s)", rec.Code, rec.Body.String())
	}
	if rec := env.do(t, http.MethodPost, "/api/v1/app-templates/web/render", env.tokenFor(t, dev),
		map[string]any{"values": map[string]string{"name": "api", "image": "x"}}); rec.Code != http.StatusOK {
		t.Fatalf("render: %d (%s)", rec.Code, rec.Body.String())
	}
}
