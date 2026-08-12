// Package webui carries the built console inside the server binary.
//
// KubeMG ships as one image on purpose. The console and the gateway are one
// product and one origin: the SPA calls the API it was served from, which is
// why there is no CORS configuration to get wrong in a production install, no
// second container to keep at the same version, and — for the air-gapped
// installs this exists for — one artefact to mirror instead of two.
//
// The assets are embedded rather than mounted so that a running server cannot
// be missing half of itself. `assets/` is empty in a source checkout and is
// filled by the frontend build during the image build; a binary built without
// that step simply has no console, says so once at boot, and serves the API
// exactly as before. That is what keeps the dev stack — where Vite serves the
// console and proxies /api here — working unchanged.
package webui

import (
	"embed"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strings"

	"github.com/gin-gonic/gin"
)

// The `all:` prefix matters: it includes the .gitkeep that holds the directory
// in a source checkout, so the package compiles when no console has been built
// into it. Without the prefix this is a compile error on a bare tree.
//
//go:embed all:assets
var embedded embed.FS

// reserved are the prefixes the API owns. An unknown path under one of them is
// a genuine 404 and must stay one: answering /api/v1/typo with the console's
// index.html would hand an HTML page to a client that asked for JSON, and the
// resulting "unexpected token <" is a considerably worse error message than the
// 404 it replaced.
var reserved = []string{"/api/", "/agent/", "/install/", "/health"}

// Assets returns the embedded console and reports whether one is present.
func Assets() (fs.FS, bool) {
	sub, err := fs.Sub(embedded, "assets")
	if err != nil {
		return nil, false
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil, false
	}
	return sub, true
}

// Mount serves the embedded console from the router's NoRoute handler and
// reports whether there was one to serve. It is registered last, after every
// API route, so it can only ever answer paths the API did not claim.
func Mount(router *gin.Engine, logger *slog.Logger) bool {
	assets, ok := Assets()
	if !ok {
		logger.Warn("no console embedded in this binary; serving the API only",
			slog.String("expected", "backend/pkg/webui/assets/index.html"),
			slog.String("note", "a dev stack serves the console from Vite instead"),
		)
		return false
	}

	router.NoRoute(Handler(assets))
	return true
}

// Handler answers a request no API route claimed, out of the supplied console
// build. It is separate from Mount so it can be exercised against a synthetic
// console rather than only against whatever the image build embedded.
func Handler(assets fs.FS) gin.HandlerFunc {
	files := http.FileServer(http.FS(assets))

	return func(c *gin.Context) {
		request := c.Request

		// A write to a path nothing registered is a 404 rather than the console:
		// only a browser navigating can be answered with a page.
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}

		for _, prefix := range reserved {
			if strings.HasPrefix(request.URL.Path, prefix) {
				c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
				return
			}
		}

		name := strings.TrimPrefix(path.Clean(request.URL.Path), "/")
		info, err := fs.Stat(assets, name)

		// Anything that is not a file the build produced is a client-side route:
		// /clusters/3/explore exists in the browser's router and nowhere on disk,
		// so a deep link or a refresh has to land on index.html rather than 404.
		if name == "" || name == "." || err != nil || info.IsDir() {
			serveIndex(c, assets)
			return
		}

		// Vite fingerprints what it emits, so those files are safe to cache for a
		// year — the name changes when the content does. index.html is the one
		// that must not be, or a browser keeps loading the previous release's
		// asset names after an upgrade.
		if strings.HasPrefix(name, "assets/") || strings.HasPrefix(name, "fonts/") {
			c.Header("Cache-Control", "public, max-age=31536000, immutable")
		}

		files.ServeHTTP(c.Writer, request)
	}
}

// serveIndex answers with the SPA shell. It is explicitly uncacheable: it is
// the document naming every fingerprinted asset, so a stale copy pins a browser
// to a release that is no longer installed.
func serveIndex(c *gin.Context, assets fs.FS) {
	index, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	c.Header("Cache-Control", "no-cache")
	c.Data(http.StatusOK, "text/html; charset=utf-8", index)
}
