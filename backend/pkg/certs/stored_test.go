package certs

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// memoryStore is the database's part in these tests: one value, an insert that
// only lands when nothing is there, and a count of every write.
type memoryStore struct {
	pair   string
	writes int
	err    error
	// racer, when set, is a pair another replica stored between this one's
	// read and its insert.
	racer string
}

func (m *memoryStore) StoredTLSPair(context.Context) (string, bool, error) {
	if m.err != nil {
		return "", false, m.err
	}
	return m.pair, m.pair != "", nil
}

func (m *memoryStore) EnsureTLSPair(_ context.Context, generate func() (string, error)) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	if m.pair != "" {
		return m.pair, nil
	}
	minted, err := generate()
	if err != nil {
		return "", err
	}
	if m.racer != "" {
		m.pair = m.racer
		m.writes++
		return m.pair, nil
	}
	m.pair = minted
	m.writes++
	return m.pair, nil
}

func (m *memoryStore) KeepTLSPair(_ context.Context, pair string) error {
	if m.err != nil {
		return m.err
	}
	m.pair = pair
	m.writes++
	return nil
}

func pairPaths(t *testing.T) (string, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "tls")
	return filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
}

var testHosts = []string{"kubemg.example.com", "127.0.0.1"}

// The case this exists for: the pod is replaced and its volume with it. The
// certificate the fleet pinned has to come back, byte for byte, rather than a
// new one being minted over it.
func TestEnsureKeptRestoresTheCertificateALostVolumeTookWithIt(t *testing.T) {
	ctx := context.Background()
	store := &memoryStore{}

	certFile, keyFile := pairPaths(t)
	first, err := EnsureKept(ctx, certFile, keyFile, testHosts, store)
	if err != nil {
		t.Fatalf("first boot: %v", err)
	}
	if !first.Generated || first.Restored {
		t.Fatalf("an empty disk and an empty store is a first boot: %+v", first)
	}
	if store.pair == "" {
		t.Fatal("the minted pair was not kept")
	}

	// A new pod: fresh, empty directory; same database.
	certFile, keyFile = pairPaths(t)
	second, err := EnsureKept(ctx, certFile, keyFile, testHosts, store)
	if err != nil {
		t.Fatalf("second boot: %v", err)
	}
	if second.Generated || !second.Restored {
		t.Fatalf("a stored pair is restored, not re-minted: %+v", second)
	}
	if !bytes.Equal(first.CertPEM, second.CertPEM) {
		t.Fatal("the restored certificate is not the one agents pinned")
	}
	onDisk, err := os.ReadFile(certFile)
	if err != nil || !bytes.Equal(onDisk, first.CertPEM) {
		t.Fatalf("the restored certificate was not written back for the listener (err %v)", err)
	}
	if info, err := os.Stat(keyFile); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the restored key must be owner-only (%v, %v)", info, err)
	}
	if store.writes != 1 {
		t.Fatalf("a restore writes nothing to the store; %d writes", store.writes)
	}
}

// An install that predates the stored copy has its pair on a volume and nothing
// in the database. Its first boot on this version adopts that pair — which is
// what makes the upgrade itself safe to lose a volume after.
func TestEnsureKeptAdoptsThePairAnExistingInstallIsServing(t *testing.T) {
	ctx := context.Background()
	certFile, keyFile := pairPaths(t)
	existing, err := Ensure(certFile, keyFile, testHosts)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	store := &memoryStore{}
	material, err := EnsureKept(ctx, certFile, keyFile, testHosts, store)
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	if material.Generated || material.Restored || !material.Kept {
		t.Fatalf("the served pair is adopted, not replaced: %+v", material)
	}
	if !bytes.Equal(material.CertPEM, existing.CertPEM) {
		t.Fatal("the pair on disk is the one agents pinned, and the one to serve")
	}
	cert, _, err := SplitPair([]byte(store.pair))
	if err != nil || !bytes.Equal(cert, existing.CertPEM) {
		t.Fatalf("the store did not keep the served pair (err %v)", err)
	}

	// The next boot finds the two in agreement and writes nothing.
	if _, err := EnsureKept(ctx, certFile, keyFile, testHosts, store); err != nil {
		t.Fatalf("next boot: %v", err)
	}
	if store.writes != 1 {
		t.Fatalf("an unchanged pair is not rewritten on every boot; %d writes", store.writes)
	}
}

// Where the disk and the store disagree, the disk is what has been served — and
// so what agents pinned. The store follows it; the disk is never overwritten.
func TestEnsureKeptLetsTheServedPairWinOverAnOlderStoredOne(t *testing.T) {
	ctx := context.Background()
	store := &memoryStore{}
	oldCert, oldKey := pairPaths(t)
	if _, err := EnsureKept(ctx, oldCert, oldKey, testHosts, store); err != nil {
		t.Fatalf("seed store: %v", err)
	}
	stale := store.pair

	certFile, keyFile := pairPaths(t)
	served, err := Ensure(certFile, keyFile, testHosts)
	if err != nil {
		t.Fatalf("seed disk: %v", err)
	}

	material, err := EnsureKept(ctx, certFile, keyFile, testHosts, store)
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	if !bytes.Equal(material.CertPEM, served.CertPEM) {
		t.Fatal("the disk's pair has to keep serving")
	}
	if store.pair == stale || !material.Kept {
		t.Fatal("the stored copy has to follow what is served")
	}
}

// Minting over an unreadable stored pair is the silent fleet-wide re-pin this
// whole arrangement exists to prevent. It refuses, and leaves the disk empty so
// nothing half-written is found and honoured by the next boot.
func TestEnsureKeptRefusesAnUnreadableStoredPair(t *testing.T) {
	store := &memoryStore{pair: "not a certificate"}
	certFile, keyFile := pairPaths(t)

	if _, err := EnsureKept(context.Background(), certFile, keyFile, testHosts, store); err == nil {
		t.Fatal("expected a refusal")
	}
	if _, err := os.Stat(certFile); !os.IsNotExist(err) {
		t.Fatalf("the refusal left a certificate on disk (stat: %v)", err)
	}
	if store.pair != "not a certificate" {
		t.Fatal("the stored value was replaced")
	}
}

// A store that cannot be read is not a store with nothing in it: minting on
// that answer would replace a pinned certificate because of a blip.
func TestEnsureKeptRefusesWhenTheStoreCannotBeRead(t *testing.T) {
	store := &memoryStore{err: errors.New("connection refused")}
	certFile, keyFile := pairPaths(t)

	if _, err := EnsureKept(context.Background(), certFile, keyFile, testHosts, store); err == nil {
		t.Fatal("expected a refusal")
	}
	if _, err := os.Stat(certFile); !os.IsNotExist(err) {
		t.Fatalf("a certificate was minted without the store (stat: %v)", err)
	}
}

// Two replicas on an empty database both mint; the insert keeps one. The loser
// has to serve the winner's pair, not its own, or agents pinned to one fail
// against the other.
func TestEnsureKeptServesTheWinnerOfAFirstBootRace(t *testing.T) {
	ctx := context.Background()
	winnerCert, winnerKey := pairPaths(t)
	winner, err := Ensure(winnerCert, winnerKey, testHosts)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	racer, err := readPair(winnerCert, winnerKey)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	store := &memoryStore{racer: racer}
	certFile, keyFile := pairPaths(t)
	material, err := EnsureKept(ctx, certFile, keyFile, testHosts, store)
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	if material.Generated || !material.Restored {
		t.Fatalf("a lost race is a restore of the winner's pair: %+v", material)
	}
	if !bytes.Equal(material.CertPEM, winner.CertPEM) {
		t.Fatal("the loser is serving its own certificate")
	}
}

// A certificate a CA issued is renewed from that CA, not recovered from here;
// keeping its key in the database would widen where it lives for no gain.
func TestEnsureKeptDoesNotStoreACertificateThatIsNotSelfSigned(t *testing.T) {
	ctx := context.Background()
	certFile, keyFile := pairPaths(t)
	writeCASignedPair(t, certFile, keyFile)

	store := &memoryStore{}
	material, err := EnsureKept(ctx, certFile, keyFile, testHosts, store)
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	if material.Kept || store.writes != 0 {
		t.Fatal("a CA-issued pair was copied into the store")
	}
}

func TestSplitPairRefusesAMismatchedPair(t *testing.T) {
	aCert, aKey := pairPaths(t)
	bCert, bKey := pairPaths(t)
	if _, err := Ensure(aCert, aKey, testHosts); err != nil {
		t.Fatal(err)
	}
	if _, err := Ensure(bCert, bKey, testHosts); err != nil {
		t.Fatal(err)
	}
	certPEM, _ := os.ReadFile(aCert)
	keyPEM, _ := os.ReadFile(bKey)
	if _, _, err := SplitPair(append(certPEM, keyPEM...)); err == nil {
		t.Fatal("a certificate and somebody else's key are not a pair")
	}
}

// A certificate file without a trailing newline — hand-edited, or written by a
// tool that does not add one — still has to read back as two PEM blocks.
func TestReadPairSeparatesAFileWithNoTrailingNewline(t *testing.T) {
	certFile, keyFile := pairPaths(t)
	if _, err := Ensure(certFile, keyFile, testHosts); err != nil {
		t.Fatal(err)
	}
	certPEM, _ := os.ReadFile(certFile)
	if err := os.WriteFile(certFile, bytes.TrimRight(certPEM, "\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pair, err := readPair(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := SplitPair([]byte(pair)); err != nil {
		t.Fatalf("the pair did not split: %v", err)
	}
}

// writeCASignedPair writes a leaf a separate CA signed — the shape of a
// certificate from a corporate PKI or cert-manager's CA issuer.
func writeCASignedPair(t *testing.T, certFile, keyFile string) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, &caTemplate, &caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafTemplate := x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "kubemg.example.com"},
		DNSNames:     []string{"kubemg.example.com"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, &leafTemplate, ca, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(leafKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := write(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := write(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
}
