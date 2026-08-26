package api

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// unknownVersion is what an unstamped build reports. The version is injected at
// link time (`-ldflags "-X main.version=..."`), so a `go build` in a source
// checkout has none — and the honest answer there is that this binary does not
// know, never a number nobody set.
const unknownVersion = "unknown"

// docsURL is where the manual for this release lives. It is served from here
// rather than written into the console for the reason the version is: the two
// belong together — a link to documentation that describes a different release
// is worse than no link — and this is the process that knows which release it
// is. It is `latest` rather than the version's own tag because Read the Docs
// only builds a version once its tag is activated there, and a 404 in the
// footer is a worse answer than a page describing the newest release.
const docsURL = "https://kubemg.readthedocs.io/en/latest/"

// versionResponse is what the console's footer draws.
type versionResponse struct {
	Version string `json:"version"`
	DocsURL string `json:"docs_url"`
}

// serverVersion names the release this process is. It is readable by any
// signed-in caller, because the footer is on every page for everybody, and it
// is *not* on the unauthenticated /health route on purpose: an exact version is
// the first thing an unauthenticated scanner wants in order to match a
// published advisory against the install.
func (s *server) serverVersion(c *gin.Context) {
	c.JSON(http.StatusOK, versionResponse{Version: s.reportedVersion(), DocsURL: docsURL})
}

// reportedVersion is the stamped version, or unknownVersion when this build
// carries none.
func (s *server) reportedVersion() string {
	if v := strings.TrimSpace(s.version); v != "" {
		return v
	}
	return unknownVersion
}
