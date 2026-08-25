package agentpkg

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"io"
	"strings"
	"testing"
)

func testOptions() Options {
	return Options{
		BastionURL:   "https://kubemg.example.com/",
		ClusterToken: "kmg_test-token",
	}
}

func TestRenderSubstitutesEveryPlaceholder(t *testing.T) {
	files, err := Render(testOptions())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("expected the embedded package to contain files")
	}

	// A placeholder that survives rendering installs a broken agent, so the
	// whole point is that none can.
	placeholders := []string{
		placeholderNamespace, placeholderBastion, placeholderToken, placeholderImage,
	}
	for name, body := range files {
		for _, placeholder := range placeholders {
			if strings.Contains(body, placeholder) {
				t.Errorf("%s still contains %s", name, placeholder)
			}
		}
	}
}

func TestRenderAppliesDefaultsAndTrimsURL(t *testing.T) {
	files, err := Render(testOptions())
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	secret := files["secret.yaml"]
	if !strings.Contains(secret, `bastion-url: "https://kubemg.example.com"`) {
		t.Fatalf("expected the trailing slash to be trimmed from the bastion URL:\n%s", secret)
	}
	if !strings.Contains(secret, `cluster-token: "kmg_test-token"`) {
		t.Fatalf("registration token was not injected:\n%s", secret)
	}
	if !strings.Contains(files["deployment.yaml"], `image: "`+DefaultImage+`"`) {
		t.Fatal("expected the default agent image")
	}
	if !strings.Contains(files["namespace.yaml"], `name: "`+DefaultNamespace+`"`) {
		t.Fatal("expected the default agent namespace")
	}
}

func TestRenderHonoursOverrides(t *testing.T) {
	opts := testOptions()
	opts.Namespace = "platform-tools"
	opts.Image = "registry.internal/kubemg-agent:2.1.0"

	files, err := Render(opts)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(files["namespace.yaml"], `name: "platform-tools"`) {
		t.Fatal("namespace override was ignored")
	}
	if !strings.Contains(files["rbac.yaml"], `namespace: "platform-tools"`) {
		t.Fatal("namespace override did not reach the service account")
	}
	if !strings.Contains(files["deployment.yaml"], `image: "registry.internal/kubemg-agent:2.1.0"`) {
		t.Fatal("image override was ignored")
	}
}

func TestRenderRequiresBastionURLAndToken(t *testing.T) {
	cases := map[string]Options{
		"no bastion url": {ClusterToken: "kmg_x"},
		"no token":       {BastionURL: "https://kubemg.example.com"},
		"blank token":    {BastionURL: "https://kubemg.example.com", ClusterToken: "   "},
	}

	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Render(opts); err == nil {
				t.Fatal("expected an error rather than a package with a hole in it")
			}
		})
	}
}

func TestRenderRejectsAnInvalidNamespace(t *testing.T) {
	cases := map[string]string{
		"embedded newline and a second document": "kubemg-system\n---\napiVersion: v1\nkind: Secret",
		"uppercase":                               "Kubemg-System",
		"underscore":                              "kubemg_system",
		"leading hyphen":                          "-kubemg",
	}

	for name, namespace := range cases {
		t.Run(name, func(t *testing.T) {
			opts := testOptions()
			opts.Namespace = namespace
			if _, err := Render(opts); err == nil {
				t.Fatalf("expected %q to be rejected as an invalid namespace", namespace)
			}
		})
	}
}

func TestManifestIsApplyOrderedAndDropsKustomization(t *testing.T) {
	manifest, err := Manifest(testOptions())
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}

	if strings.Contains(manifest, "kind: Kustomization") {
		t.Fatal("the kustomization is not a Kubernetes object and must not be applied")
	}

	// The namespace has to exist before anything is created inside it, and the
	// secret before the deployment that mounts it.
	order := []string{"kind: Namespace", "kind: ServiceAccount", "kind: Secret", "kind: Deployment"}
	last := -1
	for _, kind := range order {
		at := strings.Index(manifest, kind)
		if at < 0 {
			t.Fatalf("manifest is missing %q", kind)
		}
		if at < last {
			t.Fatalf("%q is applied out of order", kind)
		}
		last = at
	}
}

func TestArchiveContainsTheRenderedPackage(t *testing.T) {
	archive, err := Archive(testOptions())
	if err != nil {
		t.Fatalf("archive: %v", err)
	}

	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatalf("open gzip: %v", err)
	}
	defer gz.Close()

	found := map[string]string{}
	reader := tar.NewReader(gz)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read tar: %v", err)
		}
		body, err := io.ReadAll(reader)
		if err != nil {
			t.Fatalf("read %s: %v", header.Name, err)
		}
		found[header.Name] = string(body)
	}

	// `kubectl apply -k` needs the kustomization, which the flat manifest omits.
	for _, name := range []string{"kustomization.yaml", "namespace.yaml", "rbac.yaml", "secret.yaml", "deployment.yaml"} {
		if _, ok := found[PackageDir+"/"+name]; !ok {
			t.Errorf("archive is missing %s", name)
		}
	}
	if !strings.Contains(found[PackageDir+"/secret.yaml"], "kmg_test-token") {
		t.Fatal("the archived secret does not carry the registration token")
	}
}

func TestQuoteEscapesEmbeddedQuotes(t *testing.T) {
	got := quote(`a"b\c`)
	if want := `"a\"b\\c"`; got != want {
		t.Fatalf("quote() = %s, want %s", got, want)
	}
}

// A self-signed bastion is only reachable if its certificate rides along in the
// install package: the agent has nothing else to verify the tunnel against.
func TestRenderCarriesTheBastionCA(t *testing.T) {
	const pemBlock = "-----BEGIN CERTIFICATE-----\nMIIBkTCB+w==\n-----END CERTIFICATE-----"

	files, err := Render(Options{
		BastionURL:   "https://kubemg.example.com:8443",
		ClusterToken: "kmg_token",
		BastionCA:    pemBlock,
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	secret := files["secret.yaml"]
	encoded := base64.StdEncoding.EncodeToString([]byte(pemBlock + "\n"))
	if !strings.Contains(secret, "bastion-ca: "+encoded) {
		t.Fatalf("secret does not carry the CA:\n%s", secret)
	}
	// The PEM must never land raw in a YAML scalar; it is multi-line.
	if strings.Contains(secret, "BEGIN CERTIFICATE") {
		t.Fatalf("CA was embedded unencoded:\n%s", secret)
	}
}

// A bastion behind a publicly-trusted certificate pins nothing, and the
// manifest keeps one shape either way rather than sprouting a conditional key.
func TestRenderWithoutCALeavesTheKeyEmpty(t *testing.T) {
	files, err := Render(Options{
		BastionURL:   "https://kubemg.example.com",
		ClusterToken: "kmg_token",
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(files["secret.yaml"], "bastion-ca: \n") &&
		!strings.Contains(files["secret.yaml"], "bastion-ca:\n") {
		t.Fatalf("expected an empty bastion-ca key:\n%s", files["secret.yaml"])
	}
	if strings.Contains(files["secret.yaml"], "__BASTION_CA__") {
		t.Fatal("placeholder survived rendering")
	}
}
