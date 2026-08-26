package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * A chart repository is the one thing in this product an operator declares that
 * makes the bastion reach a host on the public internet and execute what it
 * downloads. So what is pinned here is the split — admin writes, everyone reads
 * — the credential rule, and the fact that a repository which cannot be reached
 * is stored with its reason rather than silently accepted or silently dropped.
 */

// chartIndex serves an index.yaml, so a repository save exercises the real fetch
// rather than a stub of it.
func chartIndex(t *testing.T, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/index.yaml" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

const oneChartIndex = `apiVersion: v1
entries:
  web:
    - name: web
      version: 1.1.0
      appVersion: "2"
      description: A web server
      urls: ["web-1.1.0.tgz"]
    - name: web
      version: 1.0.0
      urls: ["web-1.0.0.tgz"]
`

func TestOnlyAnAdminMayDeclareARepository(t *testing.T) {
	// Adding one is an outbound-egress decision, the same class of act as
	// registering an alarm channel.
	env := newTestEnv(t)
	user := env.store.addUser("dev", "secret123", "user")
	index := chartIndex(t, oneChartIndex)

	rec := env.do(t, http.MethodPut, "/api/v1/helm/repositories/charts", env.tokenFor(t, user),
		map[string]any{"url": index.URL})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected %d, got %d (%s)", http.StatusForbidden, rec.Code, rec.Body.String())
	}
}

func TestAnyoneSignedInMayReadTheCatalogue(t *testing.T) {
	// A form offering a chart must not discover the list by being refused.
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")
	dev := env.store.addUser("dev", "secret123", "user")
	index := chartIndex(t, oneChartIndex)

	if rec := env.do(t, http.MethodPut, "/api/v1/helm/repositories/charts",
		env.tokenFor(t, admin), map[string]any{"url": index.URL}); rec.Code != http.StatusCreated {
		t.Fatalf("declaring the repository: %d (%s)", rec.Code, rec.Body.String())
	}

	rec := env.do(t, http.MethodGet, "/api/v1/helm/repositories", env.tokenFor(t, dev), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("reading the list: %d (%s)", rec.Code, rec.Body.String())
	}
	rec = env.do(t, http.MethodGet, "/api/v1/helm/repositories/charts/charts",
		env.tokenFor(t, dev), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("reading the catalogue: %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestDeclaringARepositoryReadsItAndNeverEchoesTheCredential(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")
	index := chartIndex(t, oneChartIndex)

	rec := env.do(t, http.MethodPut, "/api/v1/helm/repositories/charts", env.tokenFor(t, admin),
		map[string]any{"url": index.URL, "username": "reader", "credential": "hunter2"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected %d, got %d (%s)", http.StatusCreated, rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "hunter2") {
		t.Fatalf("the credential was echoed back: %s", rec.Body.String())
	}

	var body struct {
		Repository repositoryResponse `json:"repository"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if !body.Repository.HasCredential {
		t.Fatal("the response does not report that a credential is stored")
	}
	if body.Repository.Status != db.HelmRepoOK || body.Repository.ChartCount != 1 {
		t.Fatalf("the index was not read: %+v", body.Repository)
	}
}

func TestOmittingTheCredentialOnAnEditKeepsTheStoredOne(t *testing.T) {
	// The case that would otherwise break a private repository every time
	// somebody changed its description.
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")
	token := env.tokenFor(t, admin)
	index := chartIndex(t, oneChartIndex)

	env.do(t, http.MethodPut, "/api/v1/helm/repositories/charts", token,
		map[string]any{"url": index.URL, "username": "reader", "credential": "hunter2"})
	env.do(t, http.MethodPut, "/api/v1/helm/repositories/charts", token,
		map[string]any{"url": index.URL, "description": "renamed"})

	stored := env.store.helmRepos["charts"]
	if stored.Credential != "hunter2" {
		t.Fatalf("credential = %q, want the stored one kept", stored.Credential)
	}
	if stored.Description != "renamed" {
		t.Fatalf("the edit did not land: %+v", stored)
	}
}

func TestAnEmptyCredentialClearsTheStoredOne(t *testing.T) {
	// A repository that stopped being private. Absent and empty have to mean
	// different things, which is why the field is a pointer.
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")
	token := env.tokenFor(t, admin)
	index := chartIndex(t, oneChartIndex)

	env.do(t, http.MethodPut, "/api/v1/helm/repositories/charts", token,
		map[string]any{"url": index.URL, "credential": "hunter2"})
	env.do(t, http.MethodPut, "/api/v1/helm/repositories/charts", token,
		map[string]any{"url": index.URL, "credential": ""})

	if got := env.store.helmRepos["charts"].Credential; got != "" {
		t.Fatalf("credential = %q, want cleared", got)
	}
}

func TestARepositoryThatCannotBeReadIsStoredWithItsReason(t *testing.T) {
	// An operator may be adding it before the network is open to it. Refusing
	// the save would make that impossible; reporting success would be a lie.
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer broken.Close()

	rec := env.do(t, http.MethodPut, "/api/v1/helm/repositories/charts",
		env.tokenFor(t, admin), map[string]any{"url": broken.URL})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected the row to be written: %d (%s)", rec.Code, rec.Body.String())
	}

	var body struct {
		Repository repositoryResponse `json:"repository"`
		Warning    string             `json:"warning"`
	}
	json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Repository.Status != db.HelmRepoError || body.Warning == "" {
		t.Fatalf("the failure was not reported: %+v (%q)", body.Repository, body.Warning)
	}
	if env.store.helmRepos["charts"] == nil {
		t.Fatal("the repository was not stored")
	}
}

func TestARepositoryURLThatIsNotAChartRepositoryIsRefused(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")

	for _, url := range []string{"oci://registry.example.com/charts", "file:///etc", "not a url at all"} {
		rec := env.do(t, http.MethodPut, "/api/v1/helm/repositories/charts",
			env.tokenFor(t, admin), map[string]any{"url": url})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: expected %d, got %d (%s)", url, http.StatusBadRequest, rec.Code, rec.Body.String())
		}
	}
}

func TestTheNameInTheBodyMayNotRenameTheRowTheAddressNames(t *testing.T) {
	// The same rule the create path applies to a namespace: an address is an
	// address, and a write that lands somewhere else is worse than a refusal.
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")
	index := chartIndex(t, oneChartIndex)

	rec := env.do(t, http.MethodPut, "/api/v1/helm/repositories/charts", env.tokenFor(t, admin),
		map[string]any{"name": "somewhere-else", "url": index.URL})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d (%s)", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
}

func TestASeededRepositoryDeletesLikeAnyOther(t *testing.T) {
	// The whole point of seeding rows rather than hard-coding a list: an
	// air-gapped site removes all six and adds its mirror.
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")
	env.store.helmRepos["bitnami"] = &db.HelmRepository{
		ID: 90, Name: "bitnami", URL: "https://charts.bitnami.com/bitnami", Seeded: true,
	}

	rec := env.do(t, http.MethodDelete, "/api/v1/helm/repositories/bitnami",
		env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	if _, still := env.store.helmRepos["bitnami"]; still {
		t.Fatal("a seeded repository could not be removed")
	}
}

func TestDeletingARepositoryTakesItsCharts(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")
	token := env.tokenFor(t, admin)
	index := chartIndex(t, oneChartIndex)

	env.do(t, http.MethodPut, "/api/v1/helm/repositories/charts", token,
		map[string]any{"url": index.URL})
	id := env.store.helmRepos["charts"].ID
	if len(env.store.helmCharts[id]) != 1 {
		t.Fatalf("the catalogue was not stored: %+v", env.store.helmCharts)
	}

	env.do(t, http.MethodDelete, "/api/v1/helm/repositories/charts", token, nil)
	if len(env.store.helmCharts[id]) != 0 {
		t.Fatal("a catalogue outlived the repository that could fetch it")
	}
}

func TestTheCatalogueCarriesTheNewestVersionsNewestFirst(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")
	index := chartIndex(t, oneChartIndex)

	env.do(t, http.MethodPut, "/api/v1/helm/repositories/charts", env.tokenFor(t, admin),
		map[string]any{"url": index.URL})

	rec := env.do(t, http.MethodGet, "/api/v1/helm/repositories/charts/charts",
		env.tokenFor(t, admin), nil)
	var body struct {
		Charts []chartResponse `json:"charts"`
	}
	json.Unmarshal(rec.Body.Bytes(), &body)

	if len(body.Charts) != 1 {
		t.Fatalf("expected one chart, got %d", len(body.Charts))
	}
	if body.Charts[0].Latest != "1.1.0" {
		t.Fatalf("latest = %q, want 1.1.0", body.Charts[0].Latest)
	}
	if len(body.Charts[0].Versions) != 2 || body.Charts[0].Versions[0].Version != "1.1.0" {
		t.Fatalf("versions out of order: %+v", body.Charts[0].Versions)
	}
}

func TestTheCatalogueIsSearchedAndBounded(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")
	token := env.tokenFor(t, admin)
	env.store.helmRepos["charts"] = &db.HelmRepository{ID: 7, Name: "charts", URL: "https://example.com"}
	env.store.helmCharts[7] = []db.HelmChart{
		{RepositoryID: 7, Name: "postgres", Versions: `[{"version":"1.0.0"}]`},
		{RepositoryID: 7, Name: "redis", Versions: `[{"version":"1.0.0"}]`},
	}

	rec := env.do(t, http.MethodGet, "/api/v1/helm/repositories/charts/charts?q=post", token, nil)
	var body struct {
		Charts []chartResponse `json:"charts"`
	}
	json.Unmarshal(rec.Body.Bytes(), &body)
	if len(body.Charts) != 1 || body.Charts[0].Name != "postgres" {
		t.Fatalf("search returned %+v", body.Charts)
	}

	if got := catalogueLimit("999999"); got != maxCatalogueResults {
		t.Fatalf("limit = %d, want it bounded at %d", got, maxCatalogueResults)
	}
	if got := catalogueLimit("nonsense"); got != defaultCatalogueResults {
		t.Fatalf("an unreadable limit is a page size, not an error: got %d", got)
	}
}

func TestAnUnknownRepositoryIsAFourOhFourRatherThanAnEmptyCatalogue(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")

	rec := env.do(t, http.MethodGet, "/api/v1/helm/repositories/nowhere/charts",
		env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected %d, got %d (%s)", http.StatusNotFound, rec.Code, rec.Body.String())
	}
}
