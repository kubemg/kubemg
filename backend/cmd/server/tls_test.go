package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/kubemg/kubemg/backend/pkg/certs"
	"github.com/kubemg/kubemg/backend/pkg/config"
)

// The case that made the supplied directory worth having: an install past its
// first boot already has a minted certificate on disk, and the operator has just
// put a real one in place. The real one has to win, or they watch the self-signed
// certificate keep serving with nothing saying why.
func TestResolveTLSPrefersASuppliedCertificateOverTheGeneratedOne(t *testing.T) {
	cfg := tlsConfigIn(t)

	// First boot: nothing supplied, so a pair is minted and pinned.
	first, err := resolveTLS(cfg, quietLogger())
	if err != nil {
		t.Fatalf("first boot: %v", err)
	}
	if !first.selfSigned || first.agentCA == "" {
		t.Fatal("a minted certificate is its own trust anchor and has to be pinned")
	}
	if first.certFile != cfg.TLS.CertFile {
		t.Fatalf("expected the configured path, got %s", first.certFile)
	}

	// The operator drops a certificate in. It is self-signed here only because a
	// test cannot produce a publicly-trusted one; what is being asserted is which
	// file the listener is handed.
	writeSuppliedPair(t, cfg.TLS.SuppliedDir)

	second, err := resolveTLS(cfg, quietLogger())
	if err != nil {
		t.Fatalf("second boot: %v", err)
	}
	if !second.supplied {
		t.Fatal("a certificate in the supplied directory is the one to serve")
	}
	if second.certFile != filepath.Join(cfg.TLS.SuppliedDir, "tls.crt") {
		t.Fatalf("the listener was handed %s", second.certFile)
	}
	if second.suppliedDir != cfg.TLS.SuppliedDir {
		t.Fatal("the directory is reported either way: a check that says where to put a certificate " +
			"is worth more than one that says there is none")
	}
}

// An explicit CA bundle is what an install behind a TLS-terminating ingress sets,
// and it used to return before any certificate was provisioned — leaving the
// listener to start on a file nothing had created. It wins as the thing agents
// trust; it does not stop this process needing something to serve.
func TestResolveTLSStillProvisionsAListenerBehindACABundle(t *testing.T) {
	cfg := tlsConfigIn(t)

	bundleDir := t.TempDir()
	writeSuppliedPair(t, bundleDir)
	bundle := filepath.Join(bundleDir, "tls.crt")
	cfg.TLS.AgentCABundle = bundle

	material, err := resolveTLS(cfg, quietLogger())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if material.certFile == "" {
		t.Fatal("TLS is on, so something has to be served")
	}
	if _, err := os.Stat(material.certFile); err != nil {
		t.Fatalf("the listener would start on a file that does not exist: %v", err)
	}

	pinned, err := os.ReadFile(bundle)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if material.agentCA != string(pinned) {
		t.Fatal("the operator's bundle is what agents are given, ahead of anything inferred here")
	}
}

// Off is off: with self-signing refused and nothing supplied, the boot stops
// rather than minting a certificate that would be pinned into every agent package.
func TestResolveTLSRefusesToMintWhenSelfSigningIsOff(t *testing.T) {
	cfg := tlsConfigIn(t)
	cfg.TLS.SelfSigned = false

	if _, err := resolveTLS(cfg, quietLogger()); err == nil {
		t.Fatal("expected a refusal")
	}
	// And it must have refused without minting anything: a certificate left
	// behind here is one the next boot finds, honours and pins, with the setting
	// that forbade it still off.
	if _, err := os.Stat(cfg.TLS.CertFile); !os.IsNotExist(err) {
		t.Fatalf("the refusal left a certificate on disk (stat: %v)", err)
	}

	writeSuppliedPair(t, cfg.TLS.SuppliedDir)
	if _, err := resolveTLS(cfg, quietLogger()); err != nil {
		t.Fatalf("a supplied certificate is exactly what that setting asks for: %v", err)
	}
}

// tlsConfigIn is a TLS-terminating configuration rooted in a temporary
// directory: a generated pair goes in one subdirectory, a supplied one in the
// other, which is the arrangement the compose deployment mounts.
func tlsConfigIn(t *testing.T) config.Config {
	t.Helper()
	root := t.TempDir()
	suppliedDir := filepath.Join(root, "ssl")
	if err := os.MkdirAll(suppliedDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	cfg := config.Config{PublicURL: "https://kubemg.example.com:8443"}
	cfg.TLS = config.TLS{
		Enabled:     true,
		CertFile:    filepath.Join(root, "tls", "tls.crt"),
		KeyFile:     filepath.Join(root, "tls", "tls.key"),
		SuppliedDir: suppliedDir,
		SelfSigned:  true,
	}
	return cfg
}

func writeSuppliedPair(t *testing.T, dir string) {
	t.Helper()
	if _, err := certs.Ensure(filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key"),
		[]string{"kubemg.example.com"}); err != nil {
		t.Fatalf("supply a certificate: %v", err)
	}
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
