package certs

import (
	"crypto/tls"
	"crypto/x509"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestEnsureGeneratesUsableMaterial(t *testing.T) {
	dir := t.TempDir()
	certFile := filepath.Join(dir, "nested", "tls.crt")
	keyFile := filepath.Join(dir, "nested", "tls.key")

	material, err := Ensure(certFile, keyFile, []string{"kubemg.example.com", "127.0.0.1"})
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if !material.Generated {
		t.Fatal("expected a generated pair")
	}

	// It has to load as a serving certificate, not merely parse.
	pair, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		t.Fatalf("generated pair does not load: %v", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}
	if !slices.Contains(leaf.DNSNames, "kubemg.example.com") {
		t.Fatalf("missing DNS SAN: %v", leaf.DNSNames)
	}
	if len(leaf.IPAddresses) == 0 {
		t.Fatal("an IP host must land in the IP SANs, where a name does not match")
	}

	// An agent is handed this PEM as its only trust anchor, so it has to verify
	// against itself.
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(material.CertPEM) {
		t.Fatal("certificate PEM is not appendable to a pool")
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:     pool,
		DNSName:   "kubemg.example.com",
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		t.Fatalf("certificate does not verify as its own anchor: %v", err)
	}

	info, err := os.Stat(keyFile)
	if err != nil {
		t.Fatalf("stat key: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("private key is readable at %v", mode)
	}
}

// An operator's certificate must survive a restart untouched.
func TestEnsureKeepsExistingMaterial(t *testing.T) {
	dir := t.TempDir()
	certFile := filepath.Join(dir, "tls.crt")
	keyFile := filepath.Join(dir, "tls.key")

	first, err := Ensure(certFile, keyFile, []string{"localhost"})
	if err != nil {
		t.Fatalf("first ensure: %v", err)
	}

	second, err := Ensure(certFile, keyFile, []string{"localhost"})
	if err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if second.Generated {
		t.Fatal("an existing pair was regenerated")
	}
	if string(second.CertPEM) != string(first.CertPEM) {
		t.Fatal("an existing certificate was replaced")
	}
}

// Half a pair is a misconfiguration; generating the other half would produce a
// certificate that does not match the key the operator supplied.
func TestEnsureRefusesHalfAPair(t *testing.T) {
	dir := t.TempDir()
	certFile := filepath.Join(dir, "tls.crt")
	keyFile := filepath.Join(dir, "tls.key")

	if err := os.WriteFile(certFile, []byte("not really a certificate"), 0o644); err != nil {
		t.Fatalf("seed cert: %v", err)
	}
	if _, err := Ensure(certFile, keyFile, []string{"localhost"}); err == nil {
		t.Fatal("expected an error when only the certificate exists")
	}
}

func TestHostsForCoversThePublicURLAndLoopback(t *testing.T) {
	hosts := HostsFor("https://kubemg.example.com:8443", []string{"10.0.0.5"})

	for _, want := range []string{"kubemg.example.com", "localhost", "127.0.0.1", "10.0.0.5"} {
		if !slices.Contains(hosts, want) {
			t.Fatalf("expected %q in %v", want, hosts)
		}
	}
}

// An operator's own chain — an internal PKI, typically — is what agents have to
// be handed in the case no inspection of our own certificate can detect.
func TestLoadBundleAcceptsAPEMChain(t *testing.T) {
	dir := t.TempDir()
	certFile := filepath.Join(dir, "tls.crt")
	keyFile := filepath.Join(dir, "tls.key")

	material, err := Ensure(certFile, keyFile, []string{"kubemg.example.com"})
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}

	bundle := filepath.Join(dir, "ca.pem")
	// Two copies concatenated: a bundle is a chain, not one certificate.
	if err := os.WriteFile(bundle, append(material.CertPEM, material.CertPEM...), 0o644); err != nil {
		t.Fatalf("write bundle: %v", err)
	}

	loaded, err := LoadBundle(bundle)
	if err != nil {
		t.Fatalf("load bundle: %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(loaded) {
		t.Fatal("loaded bundle is not usable as a trust pool")
	}
}

// A bundle that is wrong strands the whole fleet with an x509 error pointing at
// the cluster, so it has to fail here instead.
func TestLoadBundleRejectsNonPEM(t *testing.T) {
	bundle := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(bundle, []byte("### not a certificate ###"), 0o644); err != nil {
		t.Fatalf("write bundle: %v", err)
	}
	if _, err := LoadBundle(bundle); err == nil {
		t.Fatal("expected an error for a bundle with no PEM certificate")
	}
}

func TestLoadBundleReportsAMissingFile(t *testing.T) {
	if _, err := LoadBundle(filepath.Join(t.TempDir(), "absent.pem")); err == nil {
		t.Fatal("expected an error for a missing bundle")
	}
}
