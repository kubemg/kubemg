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
	return impersonatorRulesFor(t, nil)
}

// impersonatorRulesFor returns them as rendered for a cluster whose granted
// groups are teams.
func impersonatorRulesFor(t *testing.T, teams []string) []policyRule {
	t.Helper()
	files, err := agentpkg.Render(agentpkg.Options{
		BastionURL:   "https://kubemg.example.com/",
		ClusterToken: "kmg_test-token",
		TeamGroups:   teams,
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
		for _, group := range bastion.ImpersonationGroups(db.UserClusterAccess{K8sRole: role}, nil) {
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

// impersonableGroups is every group name the agent's ClusterRole lets it
// impersonate, failing on any open-ended grant.
func impersonableGroups(t *testing.T, rules []policyRule) []string {
	t.Helper()
	var out []string
	for _, rule := range rules {
		if !slices.Contains(rule.Verbs, "impersonate") || !slices.Contains(rule.Resources, "groups") {
			continue
		}
		if len(rule.ResourceNames) == 0 || slices.Contains(rule.ResourceNames, "*") {
			t.Fatalf("group impersonation must be limited to named groups, got %v", rule.ResourceNames)
		}
		out = append(out, rule.ResourceNames...)
	}
	return out
}

// A package rendered for a cluster with team grants lists exactly the four
// fixed groups and each team, by name: no wildcard, no prefix, nothing else.
func TestImpersonatorListsTheClustersTeamsByName(t *testing.T) {
	fixed := []string{"kubemg:view", "kubemg:edit", "kubemg:cluster-admin", "kubemg:users"}
	for _, tc := range []struct {
		teams []string
		want  []string
	}{
		{nil, fixed},
		{[]string{"payments"}, append(slices.Clone(fixed), "kubemg:g:payments")},
		{[]string{"platform", "payments", "platform"}, append(slices.Clone(fixed), "kubemg:g:payments", "kubemg:g:platform")},
	} {
		got := impersonableGroups(t, impersonatorRulesFor(t, tc.teams))
		if !slices.Equal(got, tc.want) {
			t.Fatalf("teams %v: impersonable groups = %v, want %v", tc.teams, got, tc.want)
		}
		for _, group := range got {
			if !strings.HasPrefix(group, bastion.GroupPrefix) || group == "system:masters" {
				t.Fatalf("teams %v: the agent may claim %q, outside the kubemg: prefix", tc.teams, group)
			}
		}
	}
}

// Everything the gateway asserts for a confirmed team is something the package
// rendered for that cluster lets the agent claim — otherwise the API server
// would refuse the call outright.
func TestImpersonatorCoversEveryAssertedTeamGroup(t *testing.T) {
	teams := []string{"payments", "platform"}
	cluster := &db.Cluster{ConnectionMode: db.ModeAgent, AgentTeamGroups: strings.Join(teams, ",")}
	allowed := impersonableGroups(t, impersonatorRulesFor(t, teams))
	for _, role := range []string{db.K8sRoleView, db.K8sRoleEdit, db.K8sRoleClusterAdmin} {
		grant := db.UserClusterAccess{K8sRole: role, TeamGroups: teams}
		for _, group := range bastion.ImpersonationGroups(grant, cluster) {
			if !slices.Contains(allowed, group) {
				t.Fatalf("the gateway asserts %q, which the rendered package does not let the agent claim", group)
			}
		}
	}
}

// A team name is held to the DNS-1123 label rule where it lands in the
// ClusterRole, whatever the caller checked before: a colon could reach another
// kubemg: segment, and a quote or newline could rewrite the document.
func TestRenderRefusesAnInvalidTeamGroup(t *testing.T) {
	for _, name := range []string{"Payments", "a:b", "masters\"]", "pay ments", "x\n- system:masters", ""} {
		_, err := agentpkg.Render(agentpkg.Options{
			BastionURL:   "https://kubemg.example.com/",
			ClusterToken: "kmg_test-token",
			TeamGroups:   []string{name},
		})
		if err == nil {
			t.Fatalf("team group %q was rendered into the agent's ClusterRole", name)
		}
	}
}

// The bastion asserts a team under the same prefix the package allows it under.
func TestTeamGroupPrefixMatchesTheGateway(t *testing.T) {
	if agentpkg.TeamGroupPrefix != bastion.TeamGroupPrefix {
		t.Fatalf("agentpkg allows %q, the gateway asserts %q", agentpkg.TeamGroupPrefix, bastion.TeamGroupPrefix)
	}
}
