package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func withCLIBinaries(t *testing.T, platforms ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, platform := range platforms {
		name := "kubemg"
		if strings.HasPrefix(platform, "windows") {
			name = "kubemg.exe"
		}
		if err := os.MkdirAll(filepath.Join(dir, platform), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, platform, name), []byte("binary for "+platform), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestTheServerHandsOutTheCLIItCarries(t *testing.T) {
	dir := withCLIBinaries(t, "linux-amd64", "windows-amd64")
	env := newTestEnvWith(t, func(o *Options) { o.CLIDir = dir })

	// No session: whoever needs the CLI has not signed in yet.
	rec := env.do(t, http.MethodGet, "/downloads/cli", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d", rec.Code)
	}
	list := decode[cliDownloadsResponse](t, rec)
	if !list.Available || len(list.Downloads) != 2 {
		t.Fatalf("list: %+v", list)
	}
	if list.Downloads[0].Path != "/downloads/cli/linux/amd64" || len(list.Downloads[0].SHA256) != 64 {
		t.Fatalf("first: %+v", list.Downloads[0])
	}

	rec = env.do(t, http.MethodGet, "/downloads/cli/windows/amd64", "", nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "binary for windows-amd64" {
		t.Fatalf("download: %d %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Disposition"); got != `attachment; filename="kubemg.exe"` {
		t.Fatalf("disposition %q", got)
	}
}

func TestOnlyTheListedPlatformsCanBeAskedFor(t *testing.T) {
	dir := withCLIBinaries(t, "linux-amd64")
	// Something beside the binaries that a crafted path might reach for.
	if err := os.WriteFile(filepath.Join(dir, "secret"), []byte("no"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := newTestEnvWith(t, func(o *Options) { o.CLIDir = dir })
	for _, path := range []string{
		"/downloads/cli/plan9/amd64",
		"/downloads/cli/linux/..%2Fsecret",
		"/downloads/cli/..%2F..%2Fetc/passwd",
		"/downloads/cli/darwin/arm64", // listed, but this server does not carry it
	} {
		if rec := env.do(t, http.MethodGet, path, "", nil); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d", path, rec.Code)
		}
	}
}

func TestAServerWithoutTheBinariesPointsAtTheReleases(t *testing.T) {
	env := newTestEnvWith(t, func(o *Options) { o.CLIDir = filepath.Join(t.TempDir(), "absent") })
	list := decode[cliDownloadsResponse](t, env.do(t, http.MethodGet, "/downloads/cli", "", nil))
	if list.Available || list.ReleasesURL == "" {
		t.Fatalf("list: %+v", list)
	}
}
