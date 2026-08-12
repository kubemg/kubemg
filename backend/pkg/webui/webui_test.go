package webui

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/gin-gonic/gin"
)

// console is a stand-in for what the image build embeds: a shell, a
// fingerprinted bundle and a font, at the paths Vite actually emits.
func console() fstest.MapFS {
	return fstest.MapFS{
		"index.html":              {Data: []byte("<!doctype html><title>KubeMG</title>")},
		"assets/index-a1b2c3.js":  {Data: []byte("console.log(1)")},
		"fonts/inter-latin.woff2": {Data: []byte("woff2")},
	}
}

func serve(t *testing.T, method, path string) *httptest.ResponseRecorder {
	t.Helper()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	// A stand-in for the API surface: the console is only ever reached through
	// NoRoute, so a registered route must keep winning.
	router.GET("/api/v1/clusters", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })
	router.NoRoute(Handler(console()))

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

// A deep link is a route in the browser's router and a file nowhere, so a
// refresh on one has to land on the shell rather than a 404.
func TestDeepLinkServesTheShell(t *testing.T) {
	for _, path := range []string{"/", "/clusters/3/explore", "/settings/audit"} {
		rec := serve(t, http.MethodGet, path)

		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: status %d, want 200", path, rec.Code)
		}
		if got := rec.Body.String(); got != "<!doctype html><title>KubeMG</title>" {
			t.Fatalf("GET %s served %q, want the shell", path, got)
		}
		// The shell names every fingerprinted asset, so a cached copy pins a
		// browser to a release that is no longer installed.
		if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
			t.Fatalf("GET %s Cache-Control %q, want no-cache", path, got)
		}
	}
}

// Answering an unknown API path with the shell would hand HTML to a client that
// asked for JSON — "unexpected token <" instead of a 404.
func TestUnknownAPIPathStaysA404(t *testing.T) {
	for _, path := range []string{"/api/v1/typo", "/agent/v1/nope", "/install/abc", "/health/x"} {
		rec := serve(t, http.MethodGet, path)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("GET %s: status %d, want 404", path, rec.Code)
		}
		if body := rec.Body.String(); body == "<!doctype html><title>KubeMG</title>" {
			t.Fatalf("GET %s served the console shell; want a JSON 404", path)
		}
	}
}

func TestRegisteredRouteStillWins(t *testing.T) {
	rec := serve(t, http.MethodGet, "/api/v1/clusters")

	if rec.Code != http.StatusOK || rec.Body.String() != `{"ok":true}` {
		t.Fatalf("registered route answered %d %q; the console shadowed it", rec.Code, rec.Body.String())
	}
}

// Vite fingerprints what it emits, so those are safe to cache for a year — and
// have to be, or every navigation refetches the bundle.
func TestFingerprintedAssetsAreCachedHard(t *testing.T) {
	for _, path := range []string{"/assets/index-a1b2c3.js", "/fonts/inter-latin.woff2"} {
		rec := serve(t, http.MethodGet, path)

		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: status %d, want 200", path, rec.Code)
		}
		if got := rec.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
			t.Fatalf("GET %s Cache-Control %q, want the immutable year", path, got)
		}
	}
}

// Only a browser navigating can be answered with a page; a write to a path
// nothing registered is a 404.
func TestWriteToAnUnroutedPathIs404(t *testing.T) {
	rec := serve(t, http.MethodPost, "/clusters/3/explore")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("POST to an unrouted path: status %d, want 404", rec.Code)
	}
}

// A source checkout embeds only a .gitkeep. That build has no console, and the
// server has to keep serving the API rather than refusing to start — it is what
// the dev stack runs, with Vite serving the console instead.
func TestSourceCheckoutHasNoConsole(t *testing.T) {
	if _, ok := Assets(); ok {
		t.Fatal("a console is embedded in the source tree; assets/ should hold only .gitkeep")
	}
}
