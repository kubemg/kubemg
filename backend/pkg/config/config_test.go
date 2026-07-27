package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	cfg := Load()

	if cfg.ListenAddr != ":8080" {
		t.Fatalf("expected default listen addr \":8080\", got %q", cfg.ListenAddr)
	}
	if cfg.JWTTTL != 12*time.Hour {
		t.Fatalf("expected default JWT TTL of 12h, got %s", cfg.JWTTTL)
	}
	if cfg.DB.Host != "localhost" || cfg.DB.Port != "5432" || cfg.DB.Name != "kubemg" {
		t.Fatalf("unexpected default DB config: %+v", cfg.DB)
	}
	if cfg.DB.SSLMode != "disable" {
		t.Fatalf("expected default sslmode \"disable\", got %q", cfg.DB.SSLMode)
	}
}

func TestLoadReadsEnvironment(t *testing.T) {
	t.Setenv("DB_HOST", "postgres")
	t.Setenv("DB_PORT", "6543")
	t.Setenv("DB_USER", "svc")
	t.Setenv("DB_PASSWORD", "pw")
	t.Setenv("DB_NAME", "kubemg_test")
	t.Setenv("DB_SSLMODE", "require")
	t.Setenv("JWT_SECRET", "env-secret")
	t.Setenv("JWT_TTL", "45m")
	t.Setenv("KUBEMG_LISTEN_ADDR", ":9090")

	cfg := Load()

	if cfg.ListenAddr != ":9090" {
		t.Fatalf("expected listen addr \":9090\", got %q", cfg.ListenAddr)
	}
	if cfg.JWTSecret != "env-secret" {
		t.Fatalf("expected JWT secret from env, got %q", cfg.JWTSecret)
	}
	if cfg.JWTTTL != 45*time.Minute {
		t.Fatalf("expected TTL 45m, got %s", cfg.JWTTTL)
	}

	dsn := cfg.DB.DSN()
	for _, want := range []string{
		"host=postgres", "port=6543", "user=svc",
		"password=pw", "dbname=kubemg_test", "sslmode=require",
	} {
		if !strings.Contains(dsn, want) {
			t.Fatalf("expected DSN to contain %q, got %q", want, dsn)
		}
	}
}

func TestAllowedOrigins(t *testing.T) {
	defaults := Load().AllowedOrigins
	if len(defaults) == 0 || defaults[0] != "http://localhost:5173" {
		t.Fatalf("expected the Vite dev server to be allowed by default, got %v", defaults)
	}

	t.Setenv("CORS_ALLOWED_ORIGINS", "https://kubemg.internal, https://ops.example.com ,")
	got := Load().AllowedOrigins
	want := []string{"https://kubemg.internal", "https://ops.example.com"}
	if len(got) != len(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected %v, got %v", want, got)
		}
	}

	t.Setenv("CORS_ALLOWED_ORIGINS", "   ")
	if len(Load().AllowedOrigins) != len(defaults) {
		t.Fatal("a blank value must fall back to the defaults")
	}
}

func TestEnvDurationFallbacks(t *testing.T) {
	t.Setenv("JWT_TTL", "3600")
	if got := Load().JWTTTL; got != time.Hour {
		t.Fatalf("expected bare seconds to parse as 1h, got %s", got)
	}

	t.Setenv("JWT_TTL", "not-a-duration")
	if got := Load().JWTTTL; got != 12*time.Hour {
		t.Fatalf("expected fallback of 12h for invalid input, got %s", got)
	}
}
