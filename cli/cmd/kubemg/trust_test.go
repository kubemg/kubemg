package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kubemg/kubemg/cli/internal/client"
	"github.com/kubemg/kubemg/cli/internal/store"
)

func serverFingerprint(f *fakeServer) string { return client.Fingerprint(f.Certificate()) }

// otherCertificate is a self-signed certificate for the same address that the
// fake server does not present — a renewal, or somebody in the middle.
func otherCertificate(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "other"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true,
		KeyUsage:    x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestLoginPinsAnUntrustedCertificateOnlyWhenConfirmed(t *testing.T) {
	srv := newFakeServer(t)
	h := newHarness(t)
	fingerprint := serverFingerprint(srv)

	// A script is never asked: it is told what to do instead.
	if code := h.run("login", "--server", srv.URL, "--no-browser"); code != 1 ||
		!strings.Contains(h.stderr.String(), fingerprint) || !strings.Contains(h.stderr.String(), "--ca-file") {
		t.Fatalf("no terminal: exit %d, %s", code, h.stderr)
	}
	if _, err := h.app.store.LoadSession(srv.URL); err == nil {
		t.Fatal("a session was saved without the certificate being trusted")
	}

	h.tty = true
	h.answer("n")
	if code := h.run("login", "--server", srv.URL, "--no-browser"); code != 1 ||
		!strings.Contains(h.stderr.String(), "not trusted; nothing was saved") {
		t.Fatalf("declined: exit %d, %s", code, h.stderr)
	}
	if _, err := h.app.store.LoadSession(srv.URL); err == nil {
		t.Fatal("a declined certificate left a session behind")
	}

	h.answer("y")
	if code := h.run("login", "--server", srv.URL, "--no-browser"); code != 0 {
		t.Fatalf("accepted: exit %d, %s", code, h.stderr)
	}
	if !strings.Contains(h.stderr.String(), "SHA-256:     "+fingerprint) {
		t.Fatalf("the fingerprint was not shown:\n%s", h.stderr)
	}
	session, err := h.app.store.LoadSession(srv.URL)
	if err != nil || session.CAPEM != string(trustPEM(srv)) {
		t.Fatalf("pinned %q, %v", session.CAPEM, err)
	}

	// The pin is what is trusted from then on: the next login asks nothing,
	// and the CLI's own calls verify against it.
	h.answer()
	if code := h.run("login", "--server", srv.URL, "--no-browser"); code != 0 || strings.Contains(h.stderr.String(), "Trust this") {
		t.Fatalf("second login: exit %d, %s", code, h.stderr)
	}
	if code := h.run("clusters"); code != 0 {
		t.Fatalf("clusters over the pinned certificate: exit %d, %s", code, h.stderr)
	}
}

func TestAChangedCertificateIsCalledOut(t *testing.T) {
	srv := newFakeServer(t)
	h := newHarness(t)
	earlier := store.Session{
		Server: srv.URL, RefreshToken: "kmgr_secret", ExpiresAt: time.Now().Add(time.Hour),
		Username: "dana", CAPEM: string(otherCertificate(t)),
	}
	if err := h.app.store.SaveSession(earlier); err != nil {
		t.Fatal(err)
	}
	h.tty = true
	h.answer("n")
	if code := h.run("login", "--server", srv.URL, "--no-browser"); code != 1 ||
		!strings.Contains(h.stderr.String(), "WARNING: the certificate") ||
		!strings.Contains(h.stderr.String(), "not the one you trusted at your last sign-in") {
		t.Fatalf("changed certificate: exit %d, %s", code, h.stderr)
	}
	if kept, _ := h.app.store.LoadSession(srv.URL); kept.CAPEM != earlier.CAPEM {
		t.Fatal("declining a changed certificate replaced the pin")
	}
}

func TestACertificateForAnotherNameIsRefusedNotOffered(t *testing.T) {
	srv := newFakeServer(t)
	h := newHarness(t)
	h.tty = true
	h.answer("y")
	// The fake server's certificate covers 127.0.0.1 and example.com, not localhost.
	other := strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)
	if code := h.run("login", "--server", other, "--no-browser"); code != 1 ||
		!strings.Contains(h.stderr.String(), "is not for localhost") || strings.Contains(h.stderr.String(), "Trust this") {
		t.Fatalf("exit %d, %s", code, h.stderr)
	}
}

func TestTheKubeconfigTrustsWhatTheSessionTrusts(t *testing.T) {
	srv := newFakeServer(t)
	srv.noCAData = true
	h := newHarness(t)
	h.run("login", "--server", srv.URL, "--no-browser", "--ca-file", caFile(t, srv))
	kubeconfig := filepath.Join(h.dir, "kube", "config")
	if code := h.run("update-kubeconfig", "edge-us", "--kubeconfig", kubeconfig); code != 0 {
		t.Fatalf("exit %d: %s", code, h.stderr)
	}
	data, _ := os.ReadFile(kubeconfig)
	if want := "certificate-authority-data: " + base64.StdEncoding.EncodeToString(trustPEM(srv)); !strings.Contains(string(data), want) {
		t.Fatalf("missing the session's CA in:\n%s", data)
	}
}

func TestUpdateKubeconfigAsksWhichClusters(t *testing.T) {
	srv := newFakeServer(t)
	h := newHarness(t)
	h.run("login", "--server", srv.URL, "--no-browser", "--ca-file", caFile(t, srv))
	kubeconfig := filepath.Join(h.dir, "kube", "config")

	if code := h.run("update-kubeconfig", "--kubeconfig", kubeconfig); code != 2 ||
		!strings.Contains(h.stderr.String(), "name a cluster") {
		t.Fatalf("no terminal, no name: exit %d, %s", code, h.stderr)
	}

	h.tty = true
	h.answer("")
	if code := h.run("update-kubeconfig", "--kubeconfig", kubeconfig); code != 0 || !strings.Contains(h.stderr.String(), "Nothing added") {
		t.Fatalf("nothing picked: exit %d, %s", code, h.stderr)
	}
	if _, err := os.Stat(kubeconfig); err == nil {
		t.Fatal("picking nothing wrote a kubeconfig")
	}
	for _, want := range []string{"1)  edge-us", "2)  edge-eu", "legacy", "not through the CLI"} {
		if !strings.Contains(h.stderr.String(), want) {
			t.Errorf("the list lacks %q:\n%s", want, h.stderr)
		}
	}

	h.answer("9")
	if code := h.run("update-kubeconfig", "--kubeconfig", kubeconfig); code != 2 {
		t.Fatalf("a number off the list: exit %d, %s", code, h.stderr)
	}

	h.answer("2, 1")
	if code := h.run("update-kubeconfig", "--kubeconfig", kubeconfig); code != 0 || !strings.Contains(h.stdout.String(), "Added 2 contexts") {
		t.Fatalf("two picked: exit %d, %s %s", code, h.stdout, h.stderr)
	}
	data, _ := os.ReadFile(kubeconfig)
	for _, want := range []string{"name: " + host(srv) + "/edge-us", "name: " + host(srv) + "/edge-eu", "--cluster-id, \"8\""} {
		if !strings.Contains(string(data), want) {
			t.Errorf("missing %q in:\n%s", want, data)
		}
	}
	// Several clusters leave the current context alone.
	if strings.Contains(string(data), "current-context: "+host(srv)) {
		t.Fatalf("several clusters set a current context:\n%s", data)
	}
}

func TestUpdateKubeconfigAllAndPositionalNames(t *testing.T) {
	srv := newFakeServer(t)
	h := newHarness(t)
	h.run("login", "--server", srv.URL, "--no-browser", "--ca-file", caFile(t, srv))
	kubeconfig := filepath.Join(h.dir, "kube", "config")

	if code := h.run("update-kubeconfig", "--all", "--kubeconfig", kubeconfig); code != 0 ||
		!strings.Contains(h.stderr.String(), "Skipped legacy") || !strings.Contains(h.stdout.String(), "Added 2 contexts") {
		t.Fatalf("--all: exit %d, %s %s", code, h.stdout, h.stderr)
	}
	// A name before the flags, the flags after it.
	if code := h.run("update-kubeconfig", "edge-eu", "--kubeconfig", kubeconfig); code != 0 {
		t.Fatalf("positional: exit %d, %s", code, h.stderr)
	}
	data, _ := os.ReadFile(kubeconfig)
	if !strings.Contains(string(data), "current-context: "+host(srv)+"/edge-eu") {
		t.Fatalf("one named cluster is not the current context:\n%s", data)
	}
	for _, args := range [][]string{
		{"update-kubeconfig", "--all", "edge-us"},
		{"update-kubeconfig", "--all", "--alias", "x"},
		{"update-kubeconfig", "edge-us", "edge-eu", "--alias", "x"},
	} {
		if code := h.run(args...); code != 2 {
			t.Errorf("%v: exit %d", args, code)
		}
	}
	if code := h.run("update-kubeconfig", "legacy", "--kubeconfig", kubeconfig); code != 1 || !strings.Contains(h.stderr.String(), "direct API access") {
		t.Fatalf("direct mode by name: exit %d, %s", code, h.stderr)
	}
}

func TestClusterListSpellingsAndJSON(t *testing.T) {
	srv := newFakeServer(t)
	h := newHarness(t)
	h.run("login", "--server", srv.URL, "--no-browser", "--ca-file", caFile(t, srv))

	if code := h.run("cluster", "list"); code != 0 || !strings.Contains(h.stdout.String(), "kubemg update-kubeconfig edge-us") {
		t.Fatalf("cluster list: exit %d, %s %s", code, h.stdout, h.stderr)
	}
	if code := h.run("cluster"); code != 2 {
		t.Fatalf("cluster alone: exit %d", code)
	}
	if code := h.run("clusters", "-o", "json"); code != 0 {
		t.Fatalf("json: exit %d, %s", code, h.stderr)
	}
	var rows []struct {
		Name       string   `json:"name"`
		CLI        bool     `json:"cli"`
		Namespaces []string `json:"namespaces"`
	}
	if err := json.Unmarshal(h.stdout.Bytes(), &rows); err != nil || len(rows) != 3 {
		t.Fatalf("json %s: %v", h.stdout, err)
	}
	if !rows[0].CLI || rows[2].CLI || rows[1].Namespaces == nil {
		t.Fatalf("rows %+v", rows)
	}
	if code := h.run("clusters", "-o", "yaml"); code != 2 {
		t.Fatalf("unknown output: exit %d", code)
	}
}
