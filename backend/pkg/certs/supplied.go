package certs

import (
	"crypto/tls"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// SuppliedNames are the filename pairs Supplied recognises, in the order it
// tries them. Two, not a search: a directory scanned for "something that looks
// like a certificate" is a directory in which renaming a file silently changes
// what the bastion serves.
//
// The first pair is the Kubernetes convention, which is what the documentation
// asks for. The second is what certbot writes, so pointing this directory at a
// Let's Encrypt live directory works without renaming anything — the case where
// the files are not KubeMG's to name.
var SuppliedNames = [][2]string{
	{"tls.crt", "tls.key"},
	{"fullchain.pem", "privkey.pem"},
}

// Supplied looks for a certificate an operator dropped into dir, and reports
// whether there is one. It is how a real certificate reaches the listener
// without an environment variable: the deployment mounts a directory, and
// whatever is in it wins over anything this process would mint for itself.
//
// The three answers are deliberately distinct. A directory that is absent or
// holds no recognised pair is *not* an error — it is the default install, and
// the caller goes on to mint a self-signed certificate. Half a pair, or a pair
// that does not go together, is an error that stops the boot: an operator who
// mounted a certificate believes it is being served, and quietly falling back
// to a self-signed one would pin that fallback into every agent package they
// then hand out.
func Supplied(dir string) (Material, bool, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return Material{}, false, nil
	}

	for _, pair := range SuppliedNames {
		certFile := filepath.Join(dir, pair[0])
		keyFile := filepath.Join(dir, pair[1])

		certExists, err := exists(certFile)
		if err != nil {
			return Material{}, false, err
		}
		keyExists, err := exists(keyFile)
		if err != nil {
			return Material{}, false, err
		}
		switch {
		case !certExists && !keyExists:
			continue
		case certExists != keyExists:
			return Material{}, false, fmt.Errorf(
				"found only one of %s and %s; supply both or neither", certFile, keyFile)
		}

		// Loading the pair together is the check that matters: a certificate and
		// a key that do not match fail at the handshake, which is a browser error
		// on somebody else's screen rather than a line in this log.
		loaded, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			// Permission is the failure worth naming: the container runs as an
			// unprivileged user, and certbot writes its key readable only by
			// root — so the most likely reason a mounted certificate does not
			// load is a mode nobody thought to look at.
			if errors.Is(err, fs.ErrPermission) {
				return Material{}, false, fmt.Errorf(
					"cannot read the certificate in %s: %w (the server runs unprivileged; "+
						"the files have to be readable by uid %d)", dir, err, os.Getuid())
			}
			return Material{}, false, fmt.Errorf("load the certificate in %s: %w", dir, err)
		}
		if len(loaded.Certificate) == 0 {
			return Material{}, false, fmt.Errorf("%s contains no certificate", certFile)
		}

		// The leaf, re-encoded rather than read back off disk, because the file
		// may be a full chain and only the leaf is ever pinned.
		return Material{
			CertFile: certFile,
			KeyFile:  keyFile,
			CertPEM:  encodeCert(loaded.Certificate[0]),
		}, true, nil
	}

	return Material{}, false, nil
}

// HasPair reports whether both halves of a certificate are already on disk,
// without creating anything. It exists for the caller that has to refuse *before*
// Ensure rather than after: Ensure writes the pair it reports as generated, so a
// refusal taken on its answer leaves a self-signed certificate behind that the
// next boot finds and honours as though an operator had supplied it.
//
// Half a pair is an error here for the same reason it is there.
func HasPair(certFile, keyFile string) (bool, error) {
	certExists, err := exists(certFile)
	if err != nil {
		return false, err
	}
	keyExists, err := exists(keyFile)
	if err != nil {
		return false, err
	}
	if certExists != keyExists {
		return false, fmt.Errorf(
			"found only one of %s and %s; supply both or neither", certFile, keyFile)
	}
	return certExists, nil
}

// encodeCert wraps a DER certificate back into PEM, which is the form agents are
// handed one in.
func encodeCert(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
