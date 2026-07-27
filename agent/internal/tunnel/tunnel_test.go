package tunnel

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/kubemg/kubemg/agent/internal/kube"
)

func testClient(t *testing.T, bastionURL string) *Client {
	t.Helper()

	// The agent runs outside a cluster here, so skip the CA it cannot mount.
	kubeClient, err := kube.New(kube.Options{InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("build kube client: %v", err)
	}

	client, err := New(Options{BastionURL: bastionURL, Token: "kmg_test", Kube: kubeClient})
	if err != nil {
		t.Fatalf("build tunnel client: %v", err)
	}
	return client
}

func TestWSURL(t *testing.T) {
	cases := map[string]string{
		"https://kubemg.example.com":       "wss://kubemg.example.com/agent/v1/tunnel",
		"https://kubemg.example.com/":      "wss://kubemg.example.com/agent/v1/tunnel",
		"http://localhost:8080":            "ws://localhost:8080/agent/v1/tunnel",
		"https://example.com/kubemg":       "wss://example.com/kubemg/agent/v1/tunnel",
		"wss://kubemg.example.com":         "wss://kubemg.example.com/agent/v1/tunnel",
		"https://kubemg.example.com:8443/": "wss://kubemg.example.com:8443/agent/v1/tunnel",
	}

	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			got, err := testClient(t, in).wsURL()
			if err != nil {
				t.Fatalf("wsURL: %v", err)
			}
			if got != want {
				t.Fatalf("wsURL() = %q, want %q", got, want)
			}
		})
	}
}

func TestWSURLRejectsAnUnsupportedScheme(t *testing.T) {
	if _, err := testClient(t, "ftp://kubemg.example.com").wsURL(); err == nil {
		t.Fatal("expected an unsupported scheme to be rejected")
	}
}

func TestNewRequiresItsConfiguration(t *testing.T) {
	kubeClient, err := kube.New(kube.Options{InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("build kube client: %v", err)
	}

	cases := map[string]Options{
		"no bastion url": {Token: "kmg_test", Kube: kubeClient},
		"no token":       {BastionURL: "https://kubemg.example.com", Kube: kubeClient},
		"no kube client": {BastionURL: "https://kubemg.example.com", Token: "kmg_test"},
	}

	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := New(opts); err == nil {
				t.Fatal("expected a misconfigured agent to refuse to start")
			}
		})
	}
}

func TestJitterStaysInRange(t *testing.T) {
	// Reconnects are spread so a bastion coming back up is not stampeded, but
	// the delay still has to track the backoff it was given.
	for range 100 {
		got := jitter(10 * time.Second)
		if got < 5*time.Second || got >= 15*time.Second {
			t.Fatalf("jitter(10s) = %s, outside [5s, 15s)", got)
		}
	}
	if got := jitter(0); got != minBackoff {
		t.Fatalf("jitter(0) = %s, want %s", got, minBackoff)
	}
}

func TestNewAgentStartsDisconnected(t *testing.T) {
	if testClient(t, "https://kubemg.example.com").Connected() {
		t.Fatal("readiness must not report ready before a tunnel exists")
	}
}

// An on-prem bastion often serves a certificate no public CA vouches for. The
// agent is handed that certificate in its Secret; pinning it must add trust
// rather than replace the system roots, so a bastion that later moves behind a
// public certificate does not strand the fleet.
func TestBastionTLSPinsTheGivenCA(t *testing.T) {
	certPEM, _ := selfSignedForTest(t)

	cfg, err := bastionTLS(string(certPEM), false)
	if err != nil {
		t.Fatalf("bastionTLS: %v", err)
	}
	if cfg.RootCAs == nil {
		t.Fatal("the CA was not added to the trust pool")
	}
	if cfg.InsecureSkipVerify {
		t.Fatal("pinning a CA must not disable verification")
	}
}

func TestBastionTLSWithoutCAUsesSystemRoots(t *testing.T) {
	cfg, err := bastionTLS("", false)
	if err != nil {
		t.Fatalf("bastionTLS: %v", err)
	}
	if cfg.RootCAs != nil {
		t.Fatal("an empty CA must leave the system roots alone")
	}
}

func TestBastionTLSRejectsGarbage(t *testing.T) {
	if _, err := bastionTLS("this is not a certificate", false); err == nil {
		t.Fatal("expected an error for a CA that is not PEM")
	}
}

// selfSignedForTest mints a throwaway certificate to pin.
func selfSignedForTest(t *testing.T) ([]byte, *ecdsa.PrivateKey) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	template := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "kubemg.test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{"kubemg.test"},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), key
}
