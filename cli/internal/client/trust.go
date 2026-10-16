package client

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// PeerChain is the certificate chain a server presents, read without verifying
// it — so that a person can be shown what they are asked to trust. The one
// request it makes is GET /health with no credential, and a redirect is not
// followed: the chain is the named server's own, never one a redirect points
// at. What it returns is only ever shown and checked; nothing is sent to a
// server on its strength.
func PeerChain(ctx context.Context, server string) ([]*x509.Certificate, error) {
	server = strings.TrimRight(strings.TrimSpace(server), "/")
	if !strings.HasPrefix(server, "https://") || len(server) == len("https://") {
		return nil, fmt.Errorf("the server must be an https URL, like https://kubemg.example.com")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// Read-only: the chain is verified by VerifyChain before anything is sent
	// under it, and only once a person or a CA file has vouched for it.
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12} //nolint:gosec
	httpClient := &http.Client{
		Transport:     transport,
		Timeout:       15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server+"/health", nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot reach %s: %w", server, err)
	}
	resp.Body.Close()
	transport.CloseIdleConnections()
	if resp.TLS == nil || len(resp.TLS.PeerCertificates) == 0 {
		return nil, fmt.Errorf("%s presented no certificate", server)
	}
	return resp.TLS.PeerCertificates, nil
}

// VerifyChain checks a presented chain for the server's host: against the
// certificates in caPEM when it holds any, else against the system's roots.
func VerifyChain(chain []*x509.Certificate, server string, caPEM []byte) error {
	if len(chain) == 0 {
		return errors.New("no certificate to verify")
	}
	parsed, err := url.Parse(server)
	if err != nil || parsed.Hostname() == "" {
		return fmt.Errorf("%q is not a server URL", server)
	}
	opts := x509.VerifyOptions{DNSName: parsed.Hostname(), Intermediates: x509.NewCertPool()}
	for _, cert := range chain[1:] {
		opts.Intermediates.AddCert(cert)
	}
	if len(caPEM) > 0 {
		opts.Roots = x509.NewCertPool()
		if !opts.Roots.AppendCertsFromPEM(caPEM) {
			return errors.New("the CA holds no PEM certificate")
		}
	}
	_, err = chain[0].Verify(opts)
	return err
}

// Anchor is the certificate to pin from a presented chain: the last one, which
// is the self-signed certificate itself on a server that minted its own, and
// the top of what the server sends otherwise.
func Anchor(chain []*x509.Certificate) *x509.Certificate {
	return chain[len(chain)-1]
}

// PEM encodes one certificate.
func PEM(cert *x509.Certificate) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
}

// Fingerprint is a certificate's SHA-256 the way openssl prints it, which is
// also how the console shows the server's: upper-case pairs joined by colons.
func Fingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	encoded := strings.ToUpper(hex.EncodeToString(sum[:]))
	pairs := make([]string, 0, len(sum))
	for i := 0; i < len(encoded); i += 2 {
		pairs = append(pairs, encoded[i:i+2])
	}
	return strings.Join(pairs, ":")
}
