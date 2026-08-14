// Package certs provisions the bastion's TLS material.
//
// KubeMG needs TLS in front of it to be usable at all, not just to be prudent:
// client-go refuses to send a bearer token over plain http, so `kubectl exec`
// and every generated kubeconfig fail against a plaintext bastion. A production
// install points at a real certificate; this package covers the case where
// there is not one yet, by minting a self-signed certificate the operator can
// distribute and later replace without touching anything else.
package certs

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

// selfSignedValidity is how long a generated certificate lasts. A year is long
// enough not to be a recurring chore and short enough that a development
// certificate does not quietly become a permanent one.
const selfSignedValidity = 365 * 24 * time.Hour

// Material is a resolved certificate and key pair on disk.
type Material struct {
	CertFile string
	KeyFile  string
	// CertPEM is the certificate itself. It is also the trust anchor for a
	// self-signed pair, which is what an agent has to be given to dial in.
	CertPEM []byte
	// Generated reports whether this run created the pair.
	Generated bool
}

// Ensure returns the certificate at certFile/keyFile, generating a self-signed
// pair for hosts when either file is missing. An existing pair is never
// overwritten: replacing an operator's certificate because a file went briefly
// unreadable would be far worse than failing to start.
func Ensure(certFile, keyFile string, hosts []string) (Material, error) {
	out := Material{CertFile: certFile, KeyFile: keyFile}
	if strings.TrimSpace(certFile) == "" || strings.TrimSpace(keyFile) == "" {
		return out, errors.New("both a certificate and a key file are required for TLS")
	}

	certExists, err := exists(certFile)
	if err != nil {
		return out, err
	}
	keyExists, err := exists(keyFile)
	if err != nil {
		return out, err
	}
	switch {
	case certExists && keyExists:
		pemBytes, err := os.ReadFile(certFile)
		if err != nil {
			return out, fmt.Errorf("read %s: %w", certFile, err)
		}
		out.CertPEM = pemBytes
		return out, nil
	case certExists != keyExists:
		// Half a pair is a misconfiguration, not something to paper over by
		// generating the other half against a key nobody expects.
		return out, fmt.Errorf("found only one of %s and %s; supply both or neither", certFile, keyFile)
	}

	certPEM, keyPEM, err := generate(hosts)
	if err != nil {
		return out, err
	}
	if err := write(certFile, certPEM, 0o644); err != nil {
		return out, err
	}
	if err := write(keyFile, keyPEM, 0o600); err != nil {
		return out, err
	}

	out.CertPEM = certPEM
	out.Generated = true
	return out, nil
}

// LoadBundle reads a PEM chain from disk and checks it actually contains
// certificates. A bundle that is wrong is worse than one that is missing: every
// agent in the fleet would fail its handshake with an x509 error pointing at
// the cluster rather than at this file, so it is validated at boot and the
// server refuses to start on a bad one.
func LoadBundle(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read agent CA bundle %s: %w", path, err)
	}
	if !x509.NewCertPool().AppendCertsFromPEM(raw) {
		return nil, fmt.Errorf("agent CA bundle %s contains no PEM certificate", path)
	}
	return raw, nil
}

// generate mints a self-signed certificate covering hosts. It is marked as a CA
// so it can serve as its own trust anchor: an agent given this PEM has to be
// able to verify a chain that ends at the same certificate.
func generate(hosts []string) (certPEM, keyPEM []byte, err error) {
	dnsNames, ips := splitHosts(hosts)
	if len(dnsNames) == 0 && len(ips) == 0 {
		return nil, nil, errors.New("a self-signed certificate needs at least one host")
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, fmt.Errorf("generate serial: %w", err)
	}

	now := time.Now()
	template := x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   dnsNames[0],
			Organization: []string{"KubeMG"},
		},
		NotBefore: now.Add(-time.Hour),
		NotAfter:  now.Add(selfSignedValidity),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment |
			x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              dnsNames,
		IPAddresses:           ips,
	}

	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, fmt.Errorf("create certificate: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal key: %w", err)
	}

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
		nil
}

// splitHosts sorts the requested hosts into DNS names and IP addresses, since a
// certificate carries them in different SANs and a name in the wrong one does
// not match.
func splitHosts(hosts []string) ([]string, []net.IP) {
	var (
		names []string
		ips   []net.IP
	)
	for _, host := range hosts {
		host = strings.TrimSpace(host)
		if host == "" {
			continue
		}
		if ip := net.ParseIP(host); ip != nil {
			if !slices.ContainsFunc(ips, ip.Equal) {
				ips = append(ips, ip)
			}
			continue
		}
		if !slices.Contains(names, host) {
			names = append(names, host)
		}
	}
	sort.Strings(names)
	return names, ips
}

// HostsFor derives the SANs a bastion certificate needs. The public URL is the
// address agents and kubectl actually dial, so a certificate that does not
// cover it is useless however well-formed it is; loopback is added because the
// container's own health check goes there.
func HostsFor(publicURL string, extra []string) []string {
	hosts := []string{"localhost", "127.0.0.1", "::1"}
	if host := hostOf(publicURL); host != "" {
		hosts = append(hosts, host)
	}
	hosts = append(hosts, extra...)

	names, ips := splitHosts(hosts)
	for _, ip := range ips {
		names = append(names, ip.String())
	}
	return names
}

func hostOf(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return parsed.Hostname()
}

func exists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, fmt.Errorf("stat %s: %w", path, err)
}

func write(path string, content []byte, mode os.FileMode) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		// Owner-only: this is the tls-certs volume, and nothing else on the box
		// shares it. It holds the private key that every rendered agent package
		// is pinned against, so a group- or world-readable directory would widen
		// exposure of that key for no operational reason — the same process that
		// creates it is the only one that ever needs to read it back.
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(path, content, mode); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
