package k8s

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"k8s.io/client-go/rest"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

// pemPrefix marks a certificate stored as raw PEM text.
const pemPrefix = "-----BEGIN"

// defaultRequestTimeout bounds every call made against a target cluster.
const defaultRequestTimeout = 15 * time.Second

// ErrMissingCredentials is returned when a cluster record cannot produce a
// usable connection.
var ErrMissingCredentials = errors.New("cluster is missing an API URL or service account token")

// DecodeCACert normalizes the stored CA certificate. Clusters may be registered
// with either raw PEM text or the base64 form used by kubeconfig files
// (`certificate-authority-data`). An empty value means "trust the system roots".
func DecodeCACert(stored string) ([]byte, error) {
	trimmed := strings.TrimSpace(stored)
	if trimmed == "" {
		return nil, nil
	}
	if strings.HasPrefix(trimmed, pemPrefix) {
		// PEM blocks must end with a newline for parsers to accept them.
		return []byte(trimmed + "\n"), nil
	}

	decoded, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(trimmed), ""))
	if err != nil {
		return nil, fmt.Errorf("ca certificate is neither PEM nor base64: %w", err)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(decoded)), pemPrefix) {
		return nil, errors.New("ca certificate does not contain a PEM block")
	}
	return decoded, nil
}

// RestConfig builds a client-go configuration targeting the given cluster,
// authenticating with the cluster's stored service account token.
func RestConfig(cluster *db.Cluster) (*rest.Config, error) {
	if cluster == nil {
		return nil, ErrMissingCredentials
	}

	host := strings.TrimSpace(cluster.APIURL)
	token := strings.TrimSpace(cluster.ServiceAccountToken)
	if host == "" || token == "" {
		return nil, ErrMissingCredentials
	}

	caData, err := DecodeCACert(cluster.CACertData)
	if err != nil {
		return nil, err
	}

	return &rest.Config{
		Host:        host,
		BearerToken: token,
		TLSClientConfig: rest.TLSClientConfig{
			CAData: caData,
		},
		Timeout: defaultRequestTimeout,
	}, nil
}
