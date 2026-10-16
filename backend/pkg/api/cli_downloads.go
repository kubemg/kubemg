package api

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// The kubemg CLI, served by the server it signs in to.
//
// An air-gapped site cannot reach GitHub's releases page, and the CLI is what
// its people need to use kubectl at all once kubeconfig files are withheld
// (cli_only). So the management-plane image carries the six binaries and this
// route hands them out. They are the open-source build of the same release, so
// the route is unauthenticated: somebody who has not signed in yet is exactly
// who needs it. What it can serve is a fixed list of platforms, each a path
// built from the list — never from the request.

// cliReleasesURL is where the binaries live when this server has none.
const cliReleasesURL = "https://github.com/kubemg/kubemg/releases"

var cliPlatforms = []struct{ OS, Arch string }{
	{"linux", "amd64"}, {"linux", "arm64"},
	{"darwin", "amd64"}, {"darwin", "arm64"},
	{"windows", "amd64"}, {"windows", "arm64"},
}

func cliBinaryName(goos string) string {
	if goos == "windows" {
		return "kubemg.exe"
	}
	return "kubemg"
}

// cliBinaryPath is the file for one platform, or "" for one the list does not
// name.
func (s *server) cliBinaryPath(goos, arch string) string {
	if s.cliDir == "" {
		return ""
	}
	for _, platform := range cliPlatforms {
		if platform.OS == goos && platform.Arch == arch {
			return filepath.Join(s.cliDir, goos+"-"+arch, cliBinaryName(goos))
		}
	}
	return ""
}

// cliDigests remembers each binary's SHA-256 by its size and modification
// time, so the list is not a 40 MB read per page view.
type cliDigests struct {
	mu   sync.Mutex
	seen map[string]cliDigest
}

type cliDigest struct {
	size    int64
	modTime time.Time
	sum     string
}

func (d *cliDigests) of(path string, info fs.FileInfo) (string, error) {
	d.mu.Lock()
	cached, ok := d.seen[path]
	d.mu.Unlock()
	if ok && cached.size == info.Size() && cached.modTime.Equal(info.ModTime()) {
		return cached.sum, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	sum := hex.EncodeToString(hash.Sum(nil))
	d.mu.Lock()
	if d.seen == nil {
		d.seen = map[string]cliDigest{}
	}
	d.seen[path] = cliDigest{size: info.Size(), modTime: info.ModTime(), sum: sum}
	d.mu.Unlock()
	return sum, nil
}

type cliDownload struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type cliDownloadsResponse struct {
	// Available is false on a server built without the binaries — a source
	// checkout, the dev stack — and the console then points at ReleasesURL.
	Available   bool          `json:"available"`
	Downloads   []cliDownload `json:"downloads"`
	ReleasesURL string        `json:"releases_url"`
}

// listCLIDownloads says which binaries this server carries.
func (s *server) listCLIDownloads(c *gin.Context) {
	out := cliDownloadsResponse{Downloads: []cliDownload{}, ReleasesURL: cliReleasesURL}
	for _, platform := range cliPlatforms {
		path := s.cliBinaryPath(platform.OS, platform.Arch)
		if path == "" {
			continue
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		sum, err := s.cliSums.of(path, info)
		if err != nil {
			continue
		}
		out.Downloads = append(out.Downloads, cliDownload{
			OS: platform.OS, Arch: platform.Arch,
			Path: "/downloads/cli/" + platform.OS + "/" + platform.Arch,
			Size: info.Size(), SHA256: sum,
		})
	}
	out.Available = len(out.Downloads) > 0
	c.JSON(http.StatusOK, out)
}

// downloadCLI serves one binary.
func (s *server) downloadCLI(c *gin.Context) {
	goos, arch := c.Param("os"), c.Param("arch")
	path := s.cliBinaryPath(goos, arch)
	if path == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "no kubemg CLI is published for " + goos + "/" + arch})
		return
	}
	file, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "this server was built without the kubemg CLI binaries — download it from " + cliReleasesURL,
		})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not read the binary"})
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not read the binary"})
		return
	}
	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Disposition", `attachment; filename="`+cliBinaryName(goos)+`"`)
	c.Header("X-Content-Type-Options", "nosniff")
	http.ServeContent(c.Writer, c.Request, cliBinaryName(goos), info.ModTime(), file)
}
