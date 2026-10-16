// Package store keeps what the CLI holds between runs: one session per kubemg
// server, and the short-lived credentials kubectl was last handed.
//
// Both are files only their owner can read (0600 in a 0700 directory). The
// session's refresh token is the one long-ish secret on disk — bounded by the
// server's session cap and revocable on its next use — and nothing in here is
// a kubeconfig token that outlives it.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"
)

// ErrNoSession means `kubemg login` has not been run for that server.
var ErrNoSession = errors.New("not signed in")

// Session is one signed-in server.
type Session struct {
	Server       string    `json:"server"`
	RefreshToken string    `json:"refresh_token"`
	SessionID    string    `json:"session_id"`
	ExpiresAt    time.Time `json:"expires_at"`
	Username     string    `json:"username"`
	// CAPEM is the bundle --ca-file pointed at, copied in so the session keeps
	// working if the original file moves.
	CAPEM string `json:"ca_pem,omitempty"`
}

// Credential is one cached kubectl token.
type Credential struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Store is rooted at a config directory (sessions) and a cache directory
// (credentials), which the OS conventions keep apart: a cache may be wiped
// without signing anybody out.
type Store struct {
	ConfigDir string
	CacheDir  string
}

// Default is the per-user store: os.UserConfigDir/kubemg and
// os.UserCacheDir/kubemg, overridable with KUBEMG_CONFIG_DIR / KUBEMG_CACHE_DIR.
func Default() (Store, error) {
	config := os.Getenv("KUBEMG_CONFIG_DIR")
	if config == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return Store{}, fmt.Errorf("find the config directory: %w", err)
		}
		config = filepath.Join(base, "kubemg")
	}
	cache := os.Getenv("KUBEMG_CACHE_DIR")
	if cache == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			return Store{}, fmt.Errorf("find the cache directory: %w", err)
		}
		cache = filepath.Join(base, "kubemg")
	}
	return Store{ConfigDir: config, CacheDir: cache}, nil
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// Key is the file-name form of a server URL: scheme, host and port, nothing
// that could climb out of the directory. The scheme is part of it so a session
// signed in over https can never be looked up — and its refresh token sent —
// under the same host spelled with another scheme.
func Key(server string) string {
	key := server
	if parsed, err := url.Parse(server); err == nil && parsed.Host != "" {
		key = parsed.Scheme + "_" + parsed.Host
	}
	return unsafeName.ReplaceAllString(key, "_")
}

func (s Store) sessionPath(server string) string {
	return filepath.Join(s.ConfigDir, "sessions", Key(server)+".json")
}

func (s Store) credentialPath(server string, clusterID uint) string {
	return filepath.Join(s.CacheDir, "credentials", Key(server)+"-"+strconv.FormatUint(uint64(clusterID), 10)+".json")
}

// SaveSession writes a session, replacing any for the same server.
func (s Store) SaveSession(session Session) error {
	return writeJSON(s.sessionPath(session.Server), session)
}

// LoadSession reads the session for a server.
func (s Store) LoadSession(server string) (Session, error) {
	var session Session
	if err := readJSON(s.sessionPath(server), &session); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Session{}, ErrNoSession
		}
		return Session{}, err
	}
	return session, nil
}

// Sessions lists every stored session, for picking the server when only one
// is signed in.
func (s Store) Sessions() ([]Session, error) {
	entries, err := os.ReadDir(filepath.Join(s.ConfigDir, "sessions"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Session
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		var session Session
		if err := readJSON(filepath.Join(s.ConfigDir, "sessions", entry.Name()), &session); err == nil {
			out = append(out, session)
		}
	}
	return out, nil
}

// ForgetSession removes a server's session and every credential cached under it.
func (s Store) ForgetSession(server string) error {
	if err := os.Remove(s.sessionPath(server)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	matches, _ := filepath.Glob(filepath.Join(s.CacheDir, "credentials", Key(server)+"-*.json"))
	for _, match := range matches {
		_ = os.Remove(match)
	}
	return nil
}

// SaveCredential caches a credential. Two kubectl processes may write the same
// file at once; each writes whole through a rename, so the worst case is one
// mint wasted, never a torn file.
func (s Store) SaveCredential(server string, clusterID uint, credential Credential) error {
	return writeJSON(s.credentialPath(server, clusterID), credential)
}

// LoadCredential reads a cached credential. A missing or unreadable cache is
// simply a miss.
func (s Store) LoadCredential(server string, clusterID uint) (Credential, bool) {
	var credential Credential
	if err := readJSON(s.credentialPath(server, clusterID), &credential); err != nil || credential.Token == "" {
		return Credential{}, false
	}
	return credential, true
}

// ForgetCredential drops one cached credential, after the server refused it.
func (s Store) ForgetCredential(server string, clusterID uint) {
	_ = os.Remove(s.credentialPath(server, clusterID))
}

func readJSON(path string, into any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, into)
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(path, append(data, '\n'))
}

// WriteFileAtomic writes through a temporary file in the same directory and a
// rename, owner-only, creating the directory owner-only if it is missing.
func WriteFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
