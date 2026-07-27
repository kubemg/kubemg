package k8s

import (
	"encoding/base64"
	"strings"
	"testing"

	"k8s.io/client-go/tools/clientcmd"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

const testPEM = "-----BEGIN CERTIFICATE-----\nMIIBkTCB+w==\n-----END CERTIFICATE-----\n"

func TestBuildKubeconfigIsParseable(t *testing.T) {
	raw, err := BuildKubeconfig(KubeconfigInput{
		ClusterName: "prod-eu",
		Server:      "https://prod-eu.example.com:6443",
		CAData:      []byte(testPEM),
		Username:    "devops",
		Token:       "issued-token",
		Namespace:   "team-a",
	})
	if err != nil {
		t.Fatalf("build kubeconfig: %v", err)
	}

	cfg, err := clientcmd.Load(raw)
	if err != nil {
		t.Fatalf("generated kubeconfig does not parse: %v\n%s", err, raw)
	}

	if cfg.CurrentContext != "devops@prod-eu" {
		t.Fatalf("expected current-context \"devops@prod-eu\", got %q", cfg.CurrentContext)
	}

	kubeCtx, ok := cfg.Contexts[cfg.CurrentContext]
	if !ok {
		t.Fatalf("current context missing from contexts: %+v", cfg.Contexts)
	}
	if kubeCtx.Namespace != "team-a" {
		t.Fatalf("expected namespace \"team-a\", got %q", kubeCtx.Namespace)
	}
	if kubeCtx.Cluster != "prod-eu" {
		t.Fatalf("expected cluster \"prod-eu\", got %q", kubeCtx.Cluster)
	}

	cluster, ok := cfg.Clusters[kubeCtx.Cluster]
	if !ok {
		t.Fatal("cluster entry missing")
	}
	if cluster.Server != "https://prod-eu.example.com:6443" {
		t.Fatalf("unexpected server %q", cluster.Server)
	}
	if string(cluster.CertificateAuthorityData) != testPEM {
		t.Fatalf("unexpected CA data %q", cluster.CertificateAuthorityData)
	}

	authInfo, ok := cfg.AuthInfos[kubeCtx.AuthInfo]
	if !ok {
		t.Fatal("auth info missing")
	}
	if authInfo.Token != "issued-token" {
		t.Fatalf("unexpected token %q", authInfo.Token)
	}
}

func TestBuildKubeconfigDefaultsNamespace(t *testing.T) {
	raw, err := BuildKubeconfig(KubeconfigInput{
		ClusterName: "dev", Server: "https://dev:6443", Username: "u", Token: "t",
	})
	if err != nil {
		t.Fatalf("build kubeconfig: %v", err)
	}

	cfg, err := clientcmd.Load(raw)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := cfg.Contexts[cfg.CurrentContext].Namespace; got != "default" {
		t.Fatalf("expected namespace \"default\", got %q", got)
	}
}

func TestBuildKubeconfigRequiresToken(t *testing.T) {
	if _, err := BuildKubeconfig(KubeconfigInput{ClusterName: "dev", Server: "https://dev:6443"}); err == nil {
		t.Fatal("expected an error when the token is missing")
	}
}

func TestServiceAccountName(t *testing.T) {
	tests := map[string]string{
		"devops":                 "kubemg-devops",
		"Ada.Lovelace":           "kubemg-ada-lovelace",
		"user@example.com":       "kubemg-user-example-com",
		"  Spaced  Name  ":       "kubemg-spaced-name",
		"":                       "kubemg-user",
		"---":                    "kubemg-user",
		strings.Repeat("a", 300): "kubemg-" + strings.Repeat("a", 246),
	}

	for input, want := range tests {
		if got := ServiceAccountName(input); got != want {
			t.Fatalf("ServiceAccountName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestServiceAccountNameLengthCap(t *testing.T) {
	if got := len(ServiceAccountName(strings.Repeat("x", 500))); got > 253 {
		t.Fatalf("expected name capped at 253 chars, got %d", got)
	}
}

func TestDecodeCACert(t *testing.T) {
	t.Run("empty means system roots", func(t *testing.T) {
		got, err := DecodeCACert("   ")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != nil {
			t.Fatalf("expected nil CA data, got %q", got)
		}
	})

	t.Run("raw PEM passes through", func(t *testing.T) {
		got, err := DecodeCACert(testPEM)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(got) != testPEM {
			t.Fatalf("expected PEM to pass through, got %q", got)
		}
	})

	t.Run("base64 is decoded", func(t *testing.T) {
		got, err := DecodeCACert(base64.StdEncoding.EncodeToString([]byte(testPEM)))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(got) != testPEM {
			t.Fatalf("expected decoded PEM, got %q", got)
		}
	})

	t.Run("base64 with whitespace is decoded", func(t *testing.T) {
		encoded := base64.StdEncoding.EncodeToString([]byte(testPEM))
		got, err := DecodeCACert(encoded[:10] + "\n  " + encoded[10:])
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(got) != testPEM {
			t.Fatalf("expected decoded PEM, got %q", got)
		}
	})

	t.Run("garbage is rejected", func(t *testing.T) {
		if _, err := DecodeCACert("not a cert!!"); err == nil {
			t.Fatal("expected an error for non-PEM, non-base64 input")
		}
	})

	t.Run("base64 of non-PEM is rejected", func(t *testing.T) {
		if _, err := DecodeCACert(base64.StdEncoding.EncodeToString([]byte("hello"))); err == nil {
			t.Fatal("expected an error when the decoded payload is not PEM")
		}
	})
}

func TestRestConfig(t *testing.T) {
	cluster := &db.Cluster{
		Name:                "prod-eu",
		APIURL:              "https://prod-eu.example.com:6443",
		CACertData:          base64.StdEncoding.EncodeToString([]byte(testPEM)),
		ServiceAccountToken: "sa-token",
	}

	cfg, err := RestConfig(cluster)
	if err != nil {
		t.Fatalf("rest config: %v", err)
	}
	if cfg.Host != cluster.APIURL {
		t.Fatalf("unexpected host %q", cfg.Host)
	}
	if cfg.BearerToken != "sa-token" {
		t.Fatalf("unexpected bearer token %q", cfg.BearerToken)
	}
	if string(cfg.TLSClientConfig.CAData) != testPEM {
		t.Fatalf("unexpected CA data %q", cfg.TLSClientConfig.CAData)
	}
	if cfg.TLSClientConfig.Insecure {
		t.Fatal("generated rest config must not skip TLS verification")
	}
	if cfg.Timeout == 0 {
		t.Fatal("expected a request timeout to be set")
	}
}

func TestRestConfigRequiresCredentials(t *testing.T) {
	for name, cluster := range map[string]*db.Cluster{
		"nil":         nil,
		"no url":      {ServiceAccountToken: "t"},
		"no token":    {APIURL: "https://x:6443"},
		"blank token": {APIURL: "https://x:6443", ServiceAccountToken: "   "},
	} {
		if _, err := RestConfig(cluster); err == nil {
			t.Fatalf("%s: expected ErrMissingCredentials", name)
		}
	}
}
