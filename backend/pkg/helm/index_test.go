package helm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A repository's index is a document from somebody else's server, so the rules
// that matter are the ones that hold when it is wrong: too large, badly ordered,
// carrying kinds that cannot be installed.

func TestTheNewestVersionIsTheHighestOneAndNotTheLatestPublished(t *testing.T) {
	// The case a date sort gets wrong: a backport published after a major
	// release. Offering 1.2.4 as the newest chart because it was uploaded last
	// is how somebody installs a version they were trying to move away from.
	index := rawIndex{Entries: map[string][]rawEntry{
		"web": {
			{Name: "web", Version: "2.0.0", Created: "2024-01-01T00:00:00Z", URLs: []string{"web-2.0.0.tgz"}},
			{Name: "web", Version: "1.2.4", Created: "2024-06-01T00:00:00Z", URLs: []string{"web-1.2.4.tgz"}},
		},
	}}

	charts := catalogueOf(index)
	if len(charts) != 1 {
		t.Fatalf("expected one chart, got %d", len(charts))
	}
	if got := charts[0].Versions[0].Version; got != "2.0.0" {
		t.Fatalf("newest version = %q, want 2.0.0", got)
	}
}

func TestAPrereleaseIsListedButIsNotWhatAnInstallFormOffers(t *testing.T) {
	chart := Chart{Versions: []ChartVersion{
		{Version: "2.0.0-rc.1"},
		{Version: "1.9.0"},
	}}
	if got := chart.LatestVersion(); got != "1.9.0" {
		t.Fatalf("latest = %q, want the newest stable 1.9.0", got)
	}
}

func TestAChartThatOnlyEverPublishedPrereleasesStillOffersOne(t *testing.T) {
	chart := Chart{Versions: []ChartVersion{{Version: "0.1.0-alpha.2"}, {Version: "0.1.0-alpha.1"}}}
	if got := chart.LatestVersion(); got != "0.1.0-alpha.2" {
		t.Fatalf("latest = %q, want 0.1.0-alpha.2 — refusing to offer anything would make "+
			"a pre-1.0 chart uninstallable", got)
	}
}

func TestOnlyTheNewestFewVersionsSurviveASync(t *testing.T) {
	entries := make([]rawEntry, 0, 40)
	for minor := range 40 {
		entries = append(entries, rawEntry{
			Version: "1." + itoaSmall(minor) + ".0",
			URLs:    []string{"web.tgz"},
		})
	}

	charts := catalogueOf(rawIndex{Entries: map[string][]rawEntry{"web": entries}})
	if got := len(charts[0].Versions); got != MaxVersionsPerChart {
		t.Fatalf("kept %d versions, want %d — a repository's whole history is not a dropdown",
			got, MaxVersionsPerChart)
	}
	if got := charts[0].Versions[0].Version; got != "1.39.0" {
		t.Fatalf("kept %q as newest, want 1.39.0", got)
	}
}

func TestAVersionWithNowhereToFetchItFromIsNotOffered(t *testing.T) {
	// A row that could only ever fail at install time is worse than an absent
	// one: the operator picks it, fills in the values, and finds out last.
	charts := catalogueOf(rawIndex{Entries: map[string][]rawEntry{
		"web": {
			{Version: "1.0.0"},
			{Version: "0.9.0", URLs: []string{"web-0.9.0.tgz"}},
		},
	}})
	if len(charts) != 1 || len(charts[0].Versions) != 1 {
		t.Fatalf("expected only the fetchable version, got %+v", charts)
	}
	if charts[0].Versions[0].Version != "0.9.0" {
		t.Fatalf("kept the wrong version: %+v", charts[0].Versions)
	}
}

func TestALibraryChartIsNotACatalogueEntry(t *testing.T) {
	charts := catalogueOf(rawIndex{Entries: map[string][]rawEntry{
		"common": {{Version: "1.0.0", Type: "library", URLs: []string{"common.tgz"}}},
	}})
	if len(charts) != 0 {
		t.Fatalf("a library chart renders nothing and cannot be installed, got %+v", charts)
	}
}

func TestTheChartsIdentityComesFromItsNewestVersion(t *testing.T) {
	// A chart deprecated last month is deprecated, whatever its 2019 entry said.
	charts := catalogueOf(rawIndex{Entries: map[string][]rawEntry{
		"web": {
			{Version: "1.0.0", Description: "old", URLs: []string{"a.tgz"}},
			{Version: "2.0.0", Description: "current", Deprecated: true, URLs: []string{"b.tgz"}},
		},
	}})
	if charts[0].Description != "current" || !charts[0].Deprecated {
		t.Fatalf("identity read off the wrong version: %+v", charts[0])
	}
}

func TestAnIndexLargerThanKubemgWillReadIsRefusedRatherThanLoaded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// One byte past the bound is what distinguishes "at the limit" from
		// "truncated at the limit".
		w.Write([]byte("apiVersion: v1\nentries:\n"))
		chunk := strings.Repeat("x", 1<<20)
		for range (maxIndexBytes >> 20) + 1 {
			w.Write([]byte(chunk))
		}
	}))
	defer server.Close()

	_, err := FetchIndex(context.Background(), server.Client(), Repository{Name: "big", URL: server.URL})
	if err == nil || !strings.Contains(err.Error(), "larger than KubeMG will read") {
		t.Fatalf("expected a bound refusal, got %v", err)
	}
}

func TestARefusedCredentialSaysSoRatherThanReportingTheRepositoryDown(t *testing.T) {
	// The one failure an operator fixes by editing the repository rather than
	// by looking at the network.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	_, err := FetchIndex(context.Background(), server.Client(), Repository{Name: "private", URL: server.URL})
	if err == nil || !strings.Contains(err.Error(), "refused the credential") {
		t.Fatalf("expected a credential refusal, got %v", err)
	}
}

func TestTheCredentialIsSentAsBasicAuth(t *testing.T) {
	var sawUser, sawPassword string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawUser, sawPassword, _ = r.BasicAuth()
		w.Write([]byte("apiVersion: v1\nentries: {}\n"))
	}))
	defer server.Close()

	_, err := FetchIndex(context.Background(), server.Client(), Repository{
		Name: "private", URL: server.URL, Username: "reader", Credential: "secret",
	})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if sawUser != "reader" || sawPassword != "secret" {
		t.Fatalf("basic auth = %q/%q", sawUser, sawPassword)
	}
}

func TestAnEmptyIndexIsARealStateRatherThanAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("apiVersion: v1\nentries: {}\n"))
	}))
	defer server.Close()

	charts, err := FetchIndex(context.Background(), server.Client(),
		Repository{Name: "empty", URL: server.URL})
	if err != nil {
		t.Fatalf("a private repository nobody has pushed to is not broken: %v", err)
	}
	if len(charts) != 0 {
		t.Fatalf("expected no charts, got %d", len(charts))
	}
}

func TestSomethingThatIsNotAnIndexIsRefusedByName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("<html><body>404</body></html>"))
	}))
	defer server.Close()

	_, err := FetchIndex(context.Background(), server.Client(),
		Repository{Name: "wrong", URL: server.URL})
	if err == nil || !strings.Contains(err.Error(), "index.yaml") {
		t.Fatalf("expected the message to name what was expected, got %v", err)
	}
}

func TestATrailingSlashOnTheRepositoryURLDoesNotBecomeADoubleSlash(t *testing.T) {
	var path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.Write([]byte("apiVersion: v1\nentries: {}\n"))
	}))
	defer server.Close()

	if _, err := FetchIndex(context.Background(), server.Client(),
		Repository{Name: "slash", URL: server.URL + "/"}); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if path != "/index.yaml" {
		t.Fatalf("requested %q, want /index.yaml", path)
	}
}

func TestAnOCIRegistryIsRefusedWithItsOwnReason(t *testing.T) {
	// It is not a typo, it is a repository kind this does not implement, and
	// "unsupported scheme" would send an operator looking for the typo.
	err := Repository{Name: "oci", URL: "oci://registry.example.com/charts"}.Validate()
	if err == nil || !strings.Contains(err.Error(), "OCI") {
		t.Fatalf("expected an OCI-specific refusal, got %v", err)
	}
}

func TestAFileURLIsRefused(t *testing.T) {
	// A form that reads the bastion's own filesystem is the one scheme that
	// turns a repository field into a file disclosure.
	if err := (Repository{Name: "local", URL: "file:///etc/passwd"}).Validate(); err == nil {
		t.Fatal("expected file:// to be refused")
	}
}

func TestARelativeArchiveURLResolvesAgainstTheRepository(t *testing.T) {
	repo := Repository{Name: "r", URL: "https://charts.example.com/stable/"}
	got, err := repo.resolve("web-1.0.0.tgz")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != "https://charts.example.com/stable/web-1.0.0.tgz" {
		t.Fatalf("resolved to %q", got)
	}
}

func TestAnAbsoluteArchiveURLIsLeftAlone(t *testing.T) {
	// A repository whose archives live on a CDN.
	repo := Repository{Name: "r", URL: "https://charts.example.com"}
	got, err := repo.resolve("https://cdn.example.net/web-1.0.0.tgz")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != "https://cdn.example.net/web-1.0.0.tgz" {
		t.Fatalf("resolved to %q", got)
	}
}

func TestAnUnparseableTimestampIsNotAFailedRepository(t *testing.T) {
	if got := parseCreated("not a time"); !got.IsZero() {
		t.Fatalf("expected a zero time, got %v", got)
	}
	want := time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC)
	if got := parseCreated("2024-03-01T12:00:00Z"); !got.Equal(want) {
		t.Fatalf("parsed %v, want %v", got, want)
	}
}

// itoaSmall keeps the version-count test readable without pulling strconv into
// a file that otherwise needs none of it.
func itoaSmall(value int) string {
	if value < 10 {
		return string(rune('0' + value))
	}
	return string(rune('0'+value/10)) + string(rune('0'+value%10))
}
