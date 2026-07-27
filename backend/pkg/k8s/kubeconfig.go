package k8s

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// KubeconfigInput is everything needed to render a single-cluster kubeconfig.
type KubeconfigInput struct {
	ClusterName string
	Server      string
	CAData      []byte
	Username    string
	Token       string
	Namespace   string
}

// ContextName is the context (and user) name used inside generated kubeconfigs.
func (in KubeconfigInput) ContextName() string {
	return fmt.Sprintf("%s@%s", in.Username, in.ClusterName)
}

// BuildKubeconfig renders a ready-to-use kubeconfig for one cluster.
func BuildKubeconfig(in KubeconfigInput) ([]byte, error) {
	if in.ClusterName == "" || in.Server == "" || in.Token == "" {
		return nil, errors.New("cluster name, server, and token are required")
	}

	contextName := in.ContextName()
	namespace := in.Namespace
	if namespace == "" {
		namespace = "default"
	}

	cfg := clientcmdapi.Config{
		CurrentContext: contextName,
		Clusters: map[string]*clientcmdapi.Cluster{
			in.ClusterName: {
				Server:                   in.Server,
				CertificateAuthorityData: in.CAData,
			},
		},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{
			contextName: {
				Token: in.Token,
			},
		},
		Contexts: map[string]*clientcmdapi.Context{
			contextName: {
				Cluster:   in.ClusterName,
				AuthInfo:  contextName,
				Namespace: namespace,
			},
		},
	}

	out, err := clientcmd.Write(cfg)
	if err != nil {
		return nil, fmt.Errorf("render kubeconfig: %w", err)
	}
	return out, nil
}

var invalidNameChars = regexp.MustCompile(`[^a-z0-9-]+`)

// ServiceAccountName derives an RFC 1123 service account name for a KubeMG
// user, so each user's generated tokens map to a distinct in-cluster identity.
func ServiceAccountName(username string) string {
	name := invalidNameChars.ReplaceAllString(strings.ToLower(strings.TrimSpace(username)), "-")
	name = strings.Trim(name, "-")
	if name == "" {
		name = "user"
	}
	name = "kubemg-" + name
	if len(name) > 253 {
		name = strings.Trim(name[:253], "-")
	}
	return name
}
