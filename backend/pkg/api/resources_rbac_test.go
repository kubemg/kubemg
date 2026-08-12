package api

import (
	"net/http"
	"slices"
	"strings"
	"testing"
)

/*
 * The cluster's own RBAC.
 *
 * Two things are pinned here, and they are pinned for different reasons.
 *
 * The normalisation is pinned because it is what the console *reads*: a
 * wildcard rule that does not surface as `wildcard` is a role that looks narrow
 * and is not, and a subject list silently truncated to its cap is a wrong answer
 * rather than a shortened one. Both are the kind of mistake nobody notices in a
 * screenshot.
 *
 * The guard chain is pinned because these lists are more sensitive than the ones
 * beside them. A namespace-scoped grant reading ClusterRoles cluster-wide, or a
 * review asked about a namespace outside that grant, would let somebody map
 * access they were never given — and the review in particular is a call the
 * cluster will happily answer if it is asked, so the scope check here is the
 * thing standing in the way.
 */

func TestRoleViewSummarisesThePolicy(t *testing.T) {
	role := rbacRole{
		Metadata: objectMeta{Name: "editor", Namespace: "shop"},
		Rules: []struct {
			Verbs           []string `json:"verbs"`
			APIGroups       []string `json:"apiGroups"`
			Resources       []string `json:"resources"`
			ResourceNames   []string `json:"resourceNames"`
			NonResourceURLs []string `json:"nonResourceURLs"`
		}{
			{Verbs: []string{"get", "list"}, APIGroups: []string{""}, Resources: []string{"pods"}},
			{Verbs: []string{"update"}, APIGroups: []string{"apps"}, Resources: []string{"deployments"}},
		},
	}

	view := role.view()

	if view.RuleCount != 2 {
		t.Fatalf("rule_count = %d, want 2", view.RuleCount)
	}
	// The union across the rules is what one row can show, and it is the reading
	// somebody scanning forty roles is actually doing.
	if !slices.Equal(view.Verbs, []string{"get", "list", "update"}) {
		t.Fatalf("verbs = %v, want the sorted union", view.Verbs)
	}
	if !slices.Equal(view.Resources, []string{"deployments", "pods"}) {
		t.Fatalf("resources = %v, want the sorted union", view.Resources)
	}
	if view.Wildcard {
		t.Fatal("a role with no * rule must not be marked wildcard")
	}
	if view.Namespace != "shop" {
		t.Fatalf("namespace = %q, want it carried through", view.Namespace)
	}
}

// A wildcard is the one property of a role worth a colour: it is how a role
// that reads as narrow turns out to grant everything, and it is invisible in
// every other column a list has room for.
func TestRoleViewMarksWildcards(t *testing.T) {
	for name, rule := range map[string]struct {
		verbs     []string
		resources []string
	}{
		"wildcard verb":     {verbs: []string{"*"}, resources: []string{"pods"}},
		"wildcard resource": {verbs: []string{"get"}, resources: []string{"*"}},
	} {
		t.Run(name, func(t *testing.T) {
			role := rbacRole{Metadata: objectMeta{Name: "wide"}}
			role.Rules = append(role.Rules, struct {
				Verbs           []string `json:"verbs"`
				APIGroups       []string `json:"apiGroups"`
				Resources       []string `json:"resources"`
				ResourceNames   []string `json:"resourceNames"`
				NonResourceURLs []string `json:"nonResourceURLs"`
			}{Verbs: rule.verbs, Resources: rule.resources})

			if !role.view().Wildcard {
				t.Fatalf("expected %s to be marked wildcard", name)
			}
		})
	}
}

// The rules carried on a row are a bounded prefix — an aggregated ClusterRole
// runs to hundreds — but the *count* is the real one. A row saying 12 rules for
// an object with 200 would be a lie told to save bytes.
func TestRoleViewBoundsRulesButNotTheCount(t *testing.T) {
	role := rbacRole{Metadata: objectMeta{Name: "aggregated"}}
	for i := 0; i < maxRuleSummary+8; i++ {
		role.Rules = append(role.Rules, struct {
			Verbs           []string `json:"verbs"`
			APIGroups       []string `json:"apiGroups"`
			Resources       []string `json:"resources"`
			ResourceNames   []string `json:"resourceNames"`
			NonResourceURLs []string `json:"nonResourceURLs"`
		}{Verbs: []string{"get"}, Resources: []string{"pods"}})
	}

	view := role.view()
	if len(view.Rules) != maxRuleSummary {
		t.Fatalf("rules = %d, want the cap of %d", len(view.Rules), maxRuleSummary)
	}
	if view.RuleCount != maxRuleSummary+8 {
		t.Fatalf("rule_count = %d, want every rule counted", view.RuleCount)
	}
}

func TestRoleViewMarksAggregatedAndBuiltin(t *testing.T) {
	role := rbacRole{
		Metadata: objectMeta{Name: "edit", Labels: map[string]string{bootstrapLabel: "rbac-defaults"}},
	}
	role.AggregationRule = &struct {
		ClusterRoleSelectors []struct{} `json:"clusterRoleSelectors"`
	}{}

	view := role.view()
	if !view.Aggregated {
		t.Fatal("expected an aggregation rule to mark the role aggregated")
	}
	// Kubernetes' own roles are most of a fresh cluster's ClusterRole list, and
	// separating them is what makes the ones somebody here wrote legible at all.
	if !view.Builtin {
		t.Fatal("expected the bootstrapping label to mark the role built-in")
	}
}

func TestBindingViewResolvesSubjectToRole(t *testing.T) {
	binding := rbacBinding{Metadata: objectMeta{Name: "editors", Namespace: "shop"}}
	binding.RoleRef.Kind = "ClusterRole"
	binding.RoleRef.Name = "edit"
	binding.Subjects = []struct {
		Kind      string `json:"kind"`
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	}{
		{Kind: "ServiceAccount", Name: "deployer", Namespace: "shop"},
		{Kind: "Group", Name: "platform"},
	}

	view := binding.view(false)

	// The roleRef is flattened because the question is "who gets what", and a
	// printed ref is two more lookups before it answers anything.
	if view.RoleKind != "ClusterRole" || view.RoleName != "edit" {
		t.Fatalf("role = %s/%s, want ClusterRole/edit", view.RoleKind, view.RoleName)
	}
	if view.ClusterScoped {
		t.Fatal("a RoleBinding must not be reported as cluster-scoped")
	}
	if view.SubjectCount != 2 || len(view.Subjects) != 2 {
		t.Fatalf("subjects = %+v", view.Subjects)
	}
	// A ServiceAccount's name means nothing without its namespace: the one in
	// `shop` and the one in `dev` are different identities that print alike.
	if view.Subjects[0].Namespace != "shop" {
		t.Fatalf("subject = %+v, want the namespace carried", view.Subjects[0])
	}
	if !slices.Equal(view.Kinds, []string{"Group", "ServiceAccount"}) {
		t.Fatalf("kinds = %v", view.Kinds)
	}
}

// A binding with two hundred subjects is a group mapping, and the count is the
// interesting part — but the count has to be the *real* one, or a row shows 20
// of 200 and reads as though that were all of them.
func TestBindingViewCountsEverySubject(t *testing.T) {
	binding := rbacBinding{Metadata: objectMeta{Name: "everyone"}}
	for i := 0; i < maxSubjects+5; i++ {
		binding.Subjects = append(binding.Subjects, struct {
			Kind      string `json:"kind"`
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		}{Kind: "User", Name: "user"})
	}

	view := binding.view(true)
	if len(view.Subjects) != maxSubjects {
		t.Fatalf("subjects = %d, want the cap of %d", len(view.Subjects), maxSubjects)
	}
	if view.SubjectCount != maxSubjects+5 {
		t.Fatalf("subject_count = %d, want every subject counted", view.SubjectCount)
	}
	if !view.ClusterScoped {
		t.Fatal("a ClusterRoleBinding has to say that its grant covers every namespace")
	}
}

// The components of a review go into a body the API server will act on, so they
// are validated rather than escaped — but the valid set has to be wide enough
// for the names authenticators actually produce, which are routinely email
// addresses and cloud IAM identities rather than RFC 1123 labels.
func TestValidRBACName(t *testing.T) {
	for _, name := range []string{
		"alice",
		"alice@example.com",
		"system:serviceaccount:payments:deployer",
		"system:masters",
		"pods/exec",
		"*",
	} {
		if !validRBACName(name) {
			t.Fatalf("expected %q to be a valid name", name)
		}
	}

	for _, name := range []string{
		"",
		"alice smith",
		`{"injected":true}`,
		"line\nbreak",
		strings.Repeat("a", 254),
	} {
		if validRBACName(name) {
			t.Fatalf("expected %q to be refused", name)
		}
	}
}

/* ------------------------------------------------------------ the routes --- */

func TestClusterScopedRBACListsRefuseScopedGrant(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("scoped", "secret123", "user")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	env.store.grant(user.ID, cluster.ID, "view", []string{"team-a"})
	token := env.tokenFor(t, user)

	for _, resource := range []string{"clusterroles", "clusterrolebindings"} {
		t.Run(resource, func(t *testing.T) {
			rec := env.do(t, http.MethodGet,
				"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/"+resource, token, nil)

			if rec.Code != http.StatusForbidden {
				t.Fatalf("expected status %d for a scoped grant, got %d (%s)",
					http.StatusForbidden, rec.Code, rec.Body.String())
			}
			if body := decode[map[string]string](t, rec); !strings.Contains(body["error"], "team-a") {
				t.Fatalf("expected the refusal to name the granted namespace, got %q", body["error"])
			}
		})
	}
}

func TestNamespacedRBACListsRefuseNamespaceOutsideGrant(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("scoped", "secret123", "user")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	env.store.grant(user.ID, cluster.ID, "view", []string{"team-a"})
	token := env.tokenFor(t, user)

	for _, resource := range []string{"roles", "rolebindings", "serviceaccounts"} {
		t.Run(resource, func(t *testing.T) {
			rec := env.do(t, http.MethodGet,
				"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/"+resource+"?namespace=team-b",
				token, nil)

			if rec.Code != http.StatusForbidden {
				t.Fatalf("expected status %d for a namespace outside the grant, got %d (%s)",
					http.StatusForbidden, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestRBACListsRefuseDirectClusters(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")
	cluster := env.store.addCluster("legacy", "dev") // direct mode
	token := env.tokenFor(t, admin)

	for _, resource := range []string{
		"roles", "clusterroles", "rolebindings", "clusterrolebindings", "serviceaccounts",
	} {
		t.Run(resource, func(t *testing.T) {
			rec := env.do(t, http.MethodGet,
				"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/"+resource+"?namespace=default",
				token, nil)

			if rec.Code != http.StatusConflict {
				t.Fatalf("expected status %d for a direct-mode cluster, got %d (%s)",
					http.StatusConflict, rec.Code, rec.Body.String())
			}
		})
	}
}

// The review's own validation, before anything reaches the cluster. A verb
// outside RBAC's set is a typo, and answering it with the cluster's "not
// allowed" would read as an answer to a question nobody asked.
func TestAccessReviewValidatesTheQuestion(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	token := env.tokenFor(t, admin)
	path := "/api/v1/clusters/" + itoa(cluster.ID) + "/resources/access-review"

	for name, body := range map[string]accessReviewRequest{
		"no subject":      {Verb: "get", Resource: "pods", Namespace: "shop"},
		"unknown verb":    {Subject: "alice", Verb: "del", Resource: "pods", Namespace: "shop"},
		"no resource":     {Subject: "alice", Verb: "get", Namespace: "shop"},
		"injected name":   {Subject: `{"user":"root"}`, Verb: "get", Resource: "pods"},
		"invalid group":   {Subject: "alice", Verb: "get", Resource: "pods", Group: "a b"},
		"invalid subject": {Subject: "alice smith", Verb: "get", Resource: "pods"},
	} {
		t.Run(name, func(t *testing.T) {
			rec := env.do(t, http.MethodPost, path, token, body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected status %d for %s, got %d (%s)",
					http.StatusBadRequest, name, rec.Code, rec.Body.String())
			}
		})
	}
}

// A review is asked as the caller *about somebody else*, and the cluster will
// answer it. So a scoped grant asking about a namespace it was not given — or
// asking cluster-wide, which is every namespace at once — has to be refused
// here: it is the only thing standing between a scoped user and a map of the
// access they do not have.
func TestAccessReviewHonoursTheCallersScope(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("scoped", "secret123", "user")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	env.store.grant(user.ID, cluster.ID, "view", []string{"team-a"})
	token := env.tokenFor(t, user)
	path := "/api/v1/clusters/" + itoa(cluster.ID) + "/resources/access-review"

	for name, body := range map[string]accessReviewRequest{
		"another namespace": {Subject: "alice", Verb: "get", Resource: "pods", Namespace: "team-b"},
		"cluster-wide":      {Subject: "alice", Verb: "get", Resource: "nodes"},
	} {
		t.Run(name, func(t *testing.T) {
			rec := env.do(t, http.MethodPost, path, token, body)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("expected status %d for %s, got %d (%s)",
					http.StatusForbidden, name, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestAccessReviewRequiresAuth(t *testing.T) {
	env := newTestEnv(t)
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")

	rec := env.do(t, http.MethodPost,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/access-review", "",
		accessReviewRequest{Subject: "alice", Verb: "get", Resource: "pods"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d without a token, got %d", http.StatusUnauthorized, rec.Code)
	}
}

// The verb catalogue is served rather than compiled into the browser, so that
// the form cannot offer a verb the server will refuse. If these two drift, every
// review asked with the extra verb comes back as a 400 the operator cannot act
// on.
func TestAccessReviewVerbsAreServed(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	token := env.tokenFor(t, admin)

	rec := env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/access-review/verbs", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}

	body := decode[map[string][]string](t, rec)
	if !slices.Equal(body["verbs"], reviewVerbs) {
		t.Fatalf("verbs = %v, want %v", body["verbs"], reviewVerbs)
	}
}

// The identity the review is asked *about* by default has to be the one KubeMG
// actually impersonates. A page that offered a different subject would send
// somebody chasing the answer to a question they did not ask.
func TestGrantIdentityReportsTheImpersonatedSubject(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("devops", "secret123", "user")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	env.store.grant(user.ID, cluster.ID, "view", []string{"team-a"})
	token := env.tokenFor(t, user)

	rec := env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/access-review/identity", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}

	body := decode[map[string]any](t, rec)
	if body["subject"] != "devops" {
		t.Fatalf("subject = %v, want the impersonated username", body["subject"])
	}
	if body["k8s_role"] != "view" {
		t.Fatalf("k8s_role = %v, want the grant's role", body["k8s_role"])
	}
}

func TestReviewTargetNamesTheSubresource(t *testing.T) {
	if got := reviewTarget("pods", ""); got != "pods" {
		t.Fatalf("reviewTarget = %q, want pods", got)
	}
	// `pods/exec` is how a Role writes it and how everyone reads it, even though
	// the API carries the two halves in separate fields.
	if got := reviewTarget("pods", "exec"); got != "pods/exec" {
		t.Fatalf("reviewTarget = %q, want pods/exec", got)
	}
}
