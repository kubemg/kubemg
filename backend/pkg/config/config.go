package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/agentpkg"
	"github.com/kubemg/kubemg/backend/pkg/cache"
)

// DB holds PostgreSQL connection settings.
type DB struct {
	Host     string
	Port     string
	User     string
	Password string
	Name     string
	SSLMode  string
}

// DSN renders the settings as a lib/pq connection string.
func (d DB) DSN() string {
	return fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		d.Host, d.Port, d.User, d.Password, d.Name, d.SSLMode,
	)
}

// Config is the fully resolved backend configuration.
type Config struct {
	ListenAddr    string
	JWTSecret     string
	JWTTTL        time.Duration
	DB            DB
	AdminUsername string
	AdminPassword string
	// SANamespace is the namespace on target clusters that holds KubeMG's
	// per-user service accounts.
	SANamespace string
	// AllowedOrigins are the browser origins allowed to call the API.
	AllowedOrigins []string
	// PublicURL is the address agents and operators reach this server on. It is
	// baked into every generated install command, so it has to be the outside
	// view of the bastion rather than its listen address.
	PublicURL string
	// AgentImage is the container image installed into target clusters.
	AgentImage string
	// AgentNamespace is where the agent is installed on target clusters.
	AgentNamespace string
	// AuditRetentionDays is how long proxied calls are kept before the
	// background pruner drops them. Like the settings above it is only the
	// boot-time default; an operator overrides it from the Settings page.
	AuditRetentionDays int
	// ReadCacheTTL is how long a live cluster read is served from memory before
	// being asked of the cluster again. It trades a few seconds of staleness for
	// far fewer tunnel round trips, impersonated API calls and audit records on
	// the reads a console repeats. A negative value turns the cache off.
	ReadCacheTTL time.Duration
	// SessionRecording captures interactive container sessions for replay.
	SessionRecording SessionRecording
	// TLS is how the bastion terminates HTTPS. It is not decoration: client-go
	// refuses to send a bearer token over plain http, so kubectl cannot use the
	// proxy at all without it.
	TLS TLS
}

// SessionRecording configures replay capture for exec and attach sessions.
//
// It is on by default, because a gateway whose whole claim is that it can tell
// you what somebody did in a production shell should be able to show you. The
// directory is what makes it real: recordings have to outlive the container, so
// an unmounted volume means recordings that vanish on the next deploy.
type SessionRecording struct {
	Enabled bool
	// Dir is where .cast.gz recordings are written. Mount it.
	Dir string
	// MaxBytes caps one recording. Zero takes the recorder's own default.
	MaxBytes int64
}

// TLS configures the bastion's own listener.
type TLS struct {
	Enabled  bool
	CertFile string
	KeyFile  string
	// SelfSigned mints a certificate when CertFile/KeyFile do not exist yet,
	// so a fresh install serves HTTPS without an operator having to produce
	// one first. A real deployment drops its own files in and this does
	// nothing.
	SelfSigned bool
	// Hosts are extra SANs for a generated certificate. The public URL's host
	// and loopback are always included.
	Hosts []string
	// AgentCABundle is a path to the PEM chain agents must trust to dial this
	// server. It is read independently of Enabled, because the certificate
	// agents see is not always the one this process serves: an ingress or load
	// balancer in front of KubeMG terminates TLS with its own.
	//
	// Set it whenever that chain is not one the public CAs vouch for — an
	// internal corporate PKI is the common case, and it is invisible to the
	// self-signed detection that covers the generated certificate.
	AgentCABundle string
}

// Load reads configuration from the environment, applying development defaults.
func Load() Config {
	return Config{
		ListenAddr: env("KUBEMG_LISTEN_ADDR", ":8080"),
		JWTSecret:  env("JWT_SECRET", "kubemg_dev_secret_change_me"),
		JWTTTL:     envDuration("JWT_TTL", 12*time.Hour),
		DB: DB{
			Host:     env("DB_HOST", "localhost"),
			Port:     env("DB_PORT", "5432"),
			User:     env("DB_USER", "kubemg"),
			Password: env("DB_PASSWORD", "kubemg_secret"),
			Name:     env("DB_NAME", "kubemg"),
			SSLMode:  env("DB_SSLMODE", "disable"),
		},
		AdminUsername: env("KUBEMG_ADMIN_USERNAME", "admin"),
		AdminPassword: env("KUBEMG_ADMIN_PASSWORD", "admin"),
		SANamespace:   env("KUBEMG_SA_NAMESPACE", "kubemg-system"),
		AllowedOrigins: envList(
			"CORS_ALLOWED_ORIGINS",
			[]string{"http://localhost:5173", "http://127.0.0.1:5173"},
		),
		PublicURL:      strings.TrimRight(env("KUBEMG_PUBLIC_URL", "http://localhost:8080"), "/"),
		AgentImage:     env("KUBEMG_AGENT_IMAGE", agentpkg.DefaultImage),
		AgentNamespace:     env("KUBEMG_AGENT_NAMESPACE", agentpkg.DefaultNamespace),
		AuditRetentionDays: envInt("KUBEMG_AUDIT_RETENTION_DAYS", 30),
		ReadCacheTTL:       envDuration("KUBEMG_RESOURCE_CACHE_TTL", cache.DefaultTTL),
		SessionRecording: SessionRecording{
			Enabled:  envBool("KUBEMG_SESSION_RECORDING_ENABLED", true),
			Dir:      env("KUBEMG_SESSION_RECORDING_DIR", "/var/lib/kubemg/recordings"),
			MaxBytes: int64(envInt("KUBEMG_SESSION_RECORDING_MAX_BYTES", 0)),
		},
		TLS: TLS{
			Enabled:  envBool("KUBEMG_TLS_ENABLED", false),
			CertFile: env("KUBEMG_TLS_CERT_FILE", "/etc/kubemg/tls/tls.crt"),
			KeyFile:  env("KUBEMG_TLS_KEY_FILE", "/etc/kubemg/tls/tls.key"),
			// Defaulting this on only matters when TLS is enabled at all, and
			// there it is the difference between a server that starts and one
			// that stops on a missing file nobody has been asked for yet.
			SelfSigned:    envBool("KUBEMG_TLS_SELF_SIGNED", true),
			Hosts:         envList("KUBEMG_TLS_HOSTS", nil),
			AgentCABundle: env("KUBEMG_AGENT_CA_BUNDLE", ""),
		},
	}
}

// envBool reads a boolean. Anything unparseable falls back rather than failing
// the boot, in keeping with envInt above.
func envBool(key string, fallback bool) bool {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return fallback
	}
	return value
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// envList reads a comma-separated environment variable.
func envList(key string, fallback []string) []string {
	raw := os.Getenv(key)
	if strings.TrimSpace(raw) == "" {
		return fallback
	}

	out := make([]string, 0, strings.Count(raw, ",")+1)
	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	if len(out) == 0 {
		return fallback
	}
	return out
}

// envInt reads a positive integer. A value that is not one at all falls back
// rather than failing the boot: a typo in a retention window should not stop
// the platform from starting.
func envInt(key string, fallback int) int {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func envDuration(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	if d, err := time.ParseDuration(v); err == nil {
		return d
	}
	if secs, err := strconv.Atoi(v); err == nil {
		return time.Duration(secs) * time.Second
	}
	return fallback
}
