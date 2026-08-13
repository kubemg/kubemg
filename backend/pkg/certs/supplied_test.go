package certs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The ordinary install: nothing mounted, so nothing found — and, crucially, no
// error, because this is the path that goes on to mint a self-signed pair.
func TestSuppliedIsSilentWhenThereIsNothingThere(t *testing.T) {
	for name, dir := range map[string]string{
		"missing": filepath.Join(t.TempDir(), "absent"),
		"empty":   t.TempDir(),
		"unset":   "",
	} {
		t.Run(name, func(t *testing.T) {
			material, ok, err := Supplied(dir)
			if err != nil {
				t.Fatalf("an unfurnished directory is not an error: %v", err)
			}
			if ok || material.CertFile != "" {
				t.Fatalf("expected nothing found, got %+v", material)
			}
		})
	}
}

// A pair dropped in is served, under either name — the Kubernetes one the
// documentation asks for, and certbot's, so a Let's Encrypt live directory can be
// mounted as it stands.
func TestSuppliedFindsBothRecognisedPairs(t *testing.T) {
	for _, names := range SuppliedNames {
		t.Run(names[0], func(t *testing.T) {
			dir := t.TempDir()
			writePair(t, dir, names[0], names[1], "kubemg.example.com")

			material, ok, err := Supplied(dir)
			if err != nil || !ok {
				t.Fatalf("supplied: ok=%v err=%v", ok, err)
			}
			if material.CertFile != filepath.Join(dir, names[0]) {
				t.Fatalf("wrong certificate: %s", material.CertFile)
			}
			if !strings.Contains(string(material.CertPEM), "BEGIN CERTIFICATE") {
				t.Fatal("the leaf has to come back as PEM: it is what an agent is handed")
			}
		})
	}
}

// tls.crt wins over certbot's names when both are present, so which certificate
// is being served never depends on directory iteration order.
func TestSuppliedPrefersTheDocumentedNames(t *testing.T) {
	dir := t.TempDir()
	writePair(t, dir, "tls.crt", "tls.key", "documented.example.com")
	writePair(t, dir, "fullchain.pem", "privkey.pem", "certbot.example.com")

	material, ok, err := Supplied(dir)
	if err != nil || !ok {
		t.Fatalf("supplied: ok=%v err=%v", ok, err)
	}
	if filepath.Base(material.CertFile) != "tls.crt" {
		t.Fatalf("expected tls.crt to win, got %s", material.CertFile)
	}
}

// Half a pair stops the boot. Falling back to a self-signed certificate here
// would pin that fallback into every agent package the operator then hands out,
// while they believe the certificate they mounted is the one in force.
func TestSuppliedRefusesHalfAPair(t *testing.T) {
	dir := t.TempDir()
	writePair(t, dir, "tls.crt", "tls.key", "kubemg.example.com")
	if err := os.Remove(filepath.Join(dir, "tls.key")); err != nil {
		t.Fatalf("remove: %v", err)
	}

	if _, ok, err := Supplied(dir); err == nil || ok {
		t.Fatalf("expected a refusal, got ok=%v err=%v", ok, err)
	}
}

// A certificate and a key that do not go together fail at the handshake, which
// is an error on somebody else's screen. It is caught here instead.
func TestSuppliedRefusesAMismatchedPair(t *testing.T) {
	dir := t.TempDir()
	writePair(t, dir, "tls.crt", "tls.key", "kubemg.example.com")

	other := t.TempDir()
	writePair(t, other, "tls.crt", "tls.key", "other.example.com")
	stray, err := os.ReadFile(filepath.Join(other, "tls.key"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tls.key"), stray, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, ok, err := Supplied(dir); err == nil || ok {
		t.Fatalf("expected a refusal, got ok=%v err=%v", ok, err)
	}
}

// writePair mints a certificate under the given filenames, which is how a test
// stands in for an operator dropping one into the directory.
func writePair(t *testing.T, dir, certName, keyName, host string) {
	t.Helper()
	certPEM, keyPEM, err := generate([]string{host})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, certName), certPEM, 0o644); err != nil {
		t.Fatalf("write %s: %v", certName, err)
	}
	if err := os.WriteFile(filepath.Join(dir, keyName), keyPEM, 0o600); err != nil {
		t.Fatalf("write %s: %v", keyName, err)
	}
}
