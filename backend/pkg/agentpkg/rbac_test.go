package agentpkg_test

import (
	"slices"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/kubemg/kubemg/backend/pkg/agentpkg"
	"github.com/kubemg/kubemg/backend/pkg/bastion"
	"github.com/kubemg/kubemg/backend/pkg/db"
)

type policyRule struct {
	APIGroups     []string `json:"apiGroups"`
	Resources     []string `json:"resources"`
	Verbs         []string `json:"verbs"`
	ResourceNames []string `json:"resourceNames"`
}

// It lives outside package agentpkg because it reads the gateway's own
// ImpersonationGroups, and bastion already imports this package.

// impersonatorRules returns the rules of the agent's own ClusterRole as the
// package ships it.
func impersonatorRules(t *testing.T) []policyRule {
	t.Helper()
	files, err := agentpkg.Render(agentpkg.Options{
		BastionURL:   "https://kubemg.example.com/",
		ClusterToken: "kmg_test-token",
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, doc := range strings.Split(files["rbac.yaml"], "\n---") {
		var role struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Rules []policyRule `json:"rules"`
		}
		if err := yaml.Unmarshal([]byte(doc), &role); err != nil {
			t.Fatalf("rbac.yaml does not parse: %v", err)
		}
		if role.Kind == "ClusterRole" && role.Metadata.Name == "kubemg-agent-impersonator" {
			return role.Rules
		}
	}
	t.Fatal("kubemg-agent-impersonator is missing from rbac.yaml")
	return nil
}

// The agent may impersonate any user — usernames cannot be enumerated — but
// only the groups KubeMG actually asserts, and never a ServiceAccount. An
// unrestricted group grant is what let `Impersonate-Group: system:masters`
// through the agent's identity answer 200 to everything.
func TestImpersonatorIsNarrowedToKubeMGGroups(t *testing.T) {
	var asserted []string
	for _, role := range []string{db.K8sRoleView, db.K8sRoleEdit, db.K8sRoleClusterAdmin} {
		for _, group := range bastion.ImpersonationGroups(role) {
			if !slices.Contains(asserted, group) {
				asserted = append(asserted, group)
			}
		}
	}

	var groupsGranted []string
	for _, rule := range impersonatorRules(t) {
		if !slices.Contains(rule.Verbs, "impersonate") {
			continue
		}
		for _, resource := range rule.Resources {
			switch resource {
			case "users":
				// Unavoidably open; the gateway's kubemg:u: prefix carries it.
			case "groups":
				if len(rule.ResourceNames) == 0 {
					t.Fatal("group impersonation must be limited by resourceNames")
				}
				groupsGranted = append(groupsGranted, rule.ResourceNames...)
			default:
				t.Fatalf("the agent may impersonate %q, which KubeMG never asserts", resource)
			}
		}
	}

	slices.Sort(asserted)
	slices.Sort(groupsGranted)
	if !slices.Equal(asserted, groupsGranted) {
		t.Fatalf("impersonable groups = %v, want exactly what the gateway asserts: %v", groupsGranted, asserted)
	}
}
