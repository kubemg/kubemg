package certs

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"strings"
)

/*
 * A minted certificate is pinned into every agent package this server renders,
 * so losing it is not a restart: every agent in the fleet fails its handshake
 * until its package is re-applied. Kept only on disk, it is exactly as durable
 * as the volume under /etc/kubemg/tls — and a volume is the thing a Kubernetes
 * install loses first (a deleted PVC, a new node pool, a namespace recreated).
 *
 * So the pair is kept a second time in the database, beside the signing key the
 * server mints for itself, and the disk becomes a working copy. The database is
 * not a new thing to lose: every agent's registration token already lives in it,
 * so an install that loses its database has to re-apply every agent regardless.
 * Keeping the certificate there adds no failure the install did not already
 * have, and removes one it did.
 *
 * The rules, in the order they are applied:
 *
 *   a pair on disk        is served, and is what the stored copy follows
 *   nothing on disk       the stored pair is written back and served
 *   nothing anywhere      a pair is minted, stored, written and served
 *
 * The disk wins where both exist because it is what this server has been
 * serving, and so what agents pinned. A stored pair that does not parse refuses
 * the boot rather than being replaced: minting a fresh certificate over an
 * unreadable one is the silent fleet-wide re-pin this exists to prevent.
 */

// PairStore keeps the minted certificate and key where they outlive the disk
// they are served from. The pair travels as one value — the certificate PEM
// followed by the key PEM — so the two can never be stored half each.
type PairStore interface {
	// StoredTLSPair returns the kept pair; ok is false when none is stored.
	StoredTLSPair(ctx context.Context) (pair string, ok bool, err error)
	// EnsureTLSPair stores what generate returns unless a pair is already
	// stored, and returns whichever is stored afterwards. Two replicas booting
	// on an empty database both mint; both end up serving the winner's.
	EnsureTLSPair(ctx context.Context, generate func() (string, error)) (string, error)
	// KeepTLSPair replaces the stored pair.
	KeepTLSPair(ctx context.Context, pair string) error
}

// EnsureKept is Ensure with a second copy: the pair at certFile/keyFile is
// served when it exists, restored from store when it does not, and minted for
// hosts only when neither has one. A nil store is Ensure exactly.
func EnsureKept(ctx context.Context, certFile, keyFile string, hosts []string, store PairStore) (Material, error) {
	if store == nil {
		return Ensure(certFile, keyFile, hosts)
	}
	out := Material{CertFile: certFile, KeyFile: keyFile}

	onDisk, err := HasPair(certFile, keyFile)
	if err != nil {
		return out, err
	}
	if onDisk {
		return keepServed(ctx, certFile, keyFile, hosts, store)
	}

	var minted string
	pair, err := store.EnsureTLSPair(ctx, func() (string, error) {
		certPEM, keyPEM, err := generate(hosts)
		if err != nil {
			return "", err
		}
		minted = string(certPEM) + string(keyPEM)
		return minted, nil
	})
	if err != nil {
		return out, fmt.Errorf("keep the self-signed certificate: %w", err)
	}

	certPEM, keyPEM, err := SplitPair([]byte(pair))
	if err != nil {
		return out, fmt.Errorf("the certificate stored for this install is unreadable (%w); "+
			"refusing to mint a replacement every installed agent would have to re-pin", err)
	}
	if err := write(certFile, certPEM, 0o644); err != nil {
		return out, err
	}
	if err := write(keyFile, keyPEM, 0o600); err != nil {
		return out, err
	}

	out.CertPEM = certPEM
	// A racer's pair won the insert when what came back is not what this run
	// minted: that is a restore as far as this process is concerned.
	out.Generated = minted != "" && pair == minted
	out.Restored = !out.Generated
	return out, nil
}

// keepServed serves the pair already on disk and brings the stored copy into
// line with it. Only a self-signed pair is kept: it is the one agents pin, and
// a certificate a CA issued is renewed from that CA, not recovered from here —
// storing its key would widen where it lives for no gain.
func keepServed(ctx context.Context, certFile, keyFile string, hosts []string, store PairStore) (Material, error) {
	material, err := Ensure(certFile, keyFile, hosts)
	if err != nil {
		return material, err
	}
	if !SelfSigned(material.CertPEM) {
		return material, nil
	}

	served, err := readPair(certFile, keyFile)
	if err != nil {
		return material, err
	}
	stored, ok, err := store.StoredTLSPair(ctx)
	if err != nil {
		return material, fmt.Errorf("read the stored certificate: %w", err)
	}
	if ok && stored == served {
		return material, nil
	}
	if err := store.KeepTLSPair(ctx, served); err != nil {
		return material, fmt.Errorf("keep the self-signed certificate: %w", err)
	}
	material.Kept = true
	return material, nil
}

// SplitPair separates a stored pair into its certificate and key PEM, and
// proves they belong together — the check a handshake would otherwise make on
// somebody else's screen.
func SplitPair(pair []byte) (certPEM, keyPEM []byte, err error) {
	rest := pair
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		switch {
		case block.Type == "CERTIFICATE":
			certPEM = append(certPEM, pem.EncodeToMemory(block)...)
		case strings.HasSuffix(block.Type, "PRIVATE KEY") && keyPEM == nil:
			keyPEM = pem.EncodeToMemory(block)
		}
	}
	if len(certPEM) == 0 || len(keyPEM) == 0 {
		return nil, nil, errors.New("it does not hold both a certificate and a key")
	}
	if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
		return nil, nil, err
	}
	return certPEM, keyPEM, nil
}

// SelfSigned reports whether the leaf certificate vouches for itself, which is
// what decides whether agents have to be handed it explicitly.
func SelfSigned(certPEM []byte) bool {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return false
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return false
	}
	return cert.CheckSignatureFrom(cert) == nil
}

// readPair reads the served pair in the form it is stored in. A certificate
// file without a trailing newline would run its END line into the key's BEGIN
// line, which PEM decoding does not recognise as two blocks.
func readPair(certFile, keyFile string) (string, error) {
	certPEM, err := os.ReadFile(certFile)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", certFile, err)
	}
	keyPEM, err := os.ReadFile(keyFile)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", keyFile, err)
	}
	if len(certPEM) > 0 && certPEM[len(certPEM)-1] != '\n' {
		certPEM = append(certPEM, '\n')
	}
	return string(certPEM) + string(keyPEM), nil
}
