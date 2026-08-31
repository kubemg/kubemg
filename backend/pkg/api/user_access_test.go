package api

import (
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

func accessPath(id uint) string {
	return "/api/v1/users/" + strconv.FormatUint(uint64(id), 10) + "/access"
}

func TestUserAccessIsAdminOnly(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("devops", "pw", db.RoleUser)

	// Deliberately not "narrowed to your own", unlike the audit trail and the
	// kubeconfig register: this is the surface for reading about somebody.
	rec := env.do(t, http.MethodGet, accessPath(user.ID), env.tokenFor(t, user), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d", http.StatusForbidden, rec.Code)
	}
}

func TestUserAccessUnknownUserIsNotFound(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)

	rec := env.do(t, http.MethodGet, accessPath(404), env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, rec.Code)
	}
}

func TestUserAccessWithNothingGrantedIsEmptyRatherThanNull(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	user := env.store.addUser("devops", "pw", db.RoleUser)

	rec := env.do(t, http.MethodGet, accessPath(user.ID), env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}

	body := decode[userAccessResponse](t, rec)
	if body.User.Username != "devops" {
		t.Fatalf("expected the subject to be named, got %q", body.User.Username)
	}
	if body.Clusters == nil || len(body.Clusters) != 0 {
		t.Fatalf("expected an empty cluster list, got %#v", body.Clusters)
	}
	if body.Groups == nil || len(body.Groups) != 0 {
		t.Fatalf("expected an empty group list, got %#v", body.Groups)
	}
}

// The load-bearing assertion: the effective answer is db.MergeAccess's, and the
// grants that produced it are reported alongside it. A page that said `view`
// while the proxy allowed `edit` would be worse than no page.
func TestUserAccessMergesDirectAndGroupGrantsAndSaysWhy(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	user := env.store.addUser("devops", "pw", db.RoleUser)
	cluster := env.store.addCluster("prod-eu", db.EnvProd)
	group := env.store.addGroup("platform")

	// Direct: view, narrowed to one namespace. Inherited: edit, cluster-wide.
	env.store.grant(user.ID, cluster.ID, db.K8sRoleView, []string{"team-a"})
	env.store.members[group.ID] = map[uint]bool{user.ID: true}
	env.store.groupAccess[group.ID] = map[uint]db.GroupClusterAccess{
		cluster.ID: {GroupID: group.ID, ClusterID: cluster.ID, K8sRole: db.K8sRoleEdit},
	}

	rec := env.do(t, http.MethodGet, accessPath(user.ID), env.tokenFor(t, admin), nil)
	body := decode[userAccessResponse](t, rec)

	if len(body.Clusters) != 1 {
		t.Fatalf("expected one cluster, got %d", len(body.Clusters))
	}
	got := body.Clusters[0]

	// More permissive wins on the role, and a cluster-wide grant erases the
	// namespace narrowing — both are MergeAccess's rules, not this page's.
	if got.K8sRole != db.K8sRoleEdit {
		t.Fatalf("expected the effective role %q, got %q", db.K8sRoleEdit, got.K8sRole)
	}
	if len(got.Namespaces) != 0 {
		t.Fatalf("expected cluster-wide access, got namespaces %v", got.Namespaces)
	}

	if len(got.Grants) != 2 {
		t.Fatalf("expected two contributing grants, got %d: %#v", len(got.Grants), got.Grants)
	}
	var sawDirect, sawGroup bool
	for _, grant := range got.Grants {
		switch grant.Origin {
		case grantOriginDirect:
			sawDirect = true
			if grant.K8sRole != db.K8sRoleView || len(grant.Namespaces) != 1 {
				t.Fatalf("the direct grant must be reported as written: %#v", grant)
			}
		case grantOriginGroup:
			sawGroup = true
			if grant.Group != "platform" {
				t.Fatalf("an inherited grant must name the group it came through: %#v", grant)
			}
		}
	}
	if !sawDirect || !sawGroup {
		t.Fatalf("both origins must be reported: %#v", got.Grants)
	}
}

func TestUserAccessReportsALiveElevationWithItsClock(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	user := env.store.addUser("devops", "pw", db.RoleUser)
	cluster := env.store.addCluster("prod-eu", db.EnvProd)

	expires := time.Now().UTC().Add(40 * time.Minute)
	env.store.access[user.ID] = map[uint]db.UserClusterAccess{
		cluster.ID: {
			UserID:    user.ID,
			ClusterID: cluster.ID,
			K8sRole:   db.K8sRoleClusterAdmin,
			Source:    "jit",
			ExpiresAt: &expires,
		},
	}

	rec := env.do(t, http.MethodGet, accessPath(user.ID), env.tokenFor(t, admin), nil)
	body := decode[userAccessResponse](t, rec)

	if len(body.Clusters) != 1 {
		t.Fatalf("expected one cluster, got %d", len(body.Clusters))
	}
	got := body.Clusters[0]
	if got.ExpiresAt == nil {
		t.Fatal("an elevation that ends must be reported as ending")
	}
	// cluster-admin on production for forty minutes reads very differently from
	// cluster-admin somebody wrote in 2024, so the source is the point.
	if len(got.Grants) != 1 || got.Grants[0].Source != "jit" {
		t.Fatalf("expected the grant to name itself as an elevation: %#v", got.Grants)
	}
}

func TestUserAccessOmitsAnExpiredGrant(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	user := env.store.addUser("devops", "pw", db.RoleUser)
	cluster := env.store.addCluster("prod-eu", db.EnvProd)

	// Expiry is enforced on every read rather than by the sweeper, so a window
	// that has run out is closed whether or not a background pass has run. A
	// review showing an expired elevation as live is exactly what that rule
	// exists to stop.
	expired := time.Now().UTC().Add(-time.Minute)
	env.store.access[user.ID] = map[uint]db.UserClusterAccess{
		cluster.ID: {
			UserID:    user.ID,
			ClusterID: cluster.ID,
			K8sRole:   db.K8sRoleClusterAdmin,
			Source:    "jit",
			ExpiresAt: &expired,
		},
	}

	rec := env.do(t, http.MethodGet, accessPath(user.ID), env.tokenFor(t, admin), nil)
	body := decode[userAccessResponse](t, rec)
	if len(body.Clusters) != 0 {
		t.Fatalf("an expired grant is not access: %#v", body.Clusters)
	}
}

func TestUserAccessNamesTheIdentityProvider(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	user := env.store.addUser("devops", "pw", db.RoleUser)

	provider := &db.SSOProviderConfig{ID: 900, Name: "Okta", Protocol: "oidc"}
	env.store.providers[provider.ID] = provider
	user.AuthSource = "sso"
	user.SSOProviderID = provider.ID

	rec := env.do(t, http.MethodGet, accessPath(user.ID), env.tokenFor(t, admin), nil)
	body := decode[userAccessResponse](t, rec)

	// "It is federated" is already on the account; which directory owns it is
	// what an auditor otherwise has to match by hand against the SSO page.
	if body.Provider != "Okta" {
		t.Fatalf("expected the provider named, got %q", body.Provider)
	}
}

func TestUserAccessSurvivesAProviderThatHasBeenDeleted(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	user := env.store.addUser("devops", "pw", db.RoleUser)
	user.AuthSource = "sso"
	user.SSOProviderID = 901 // never created

	rec := env.do(t, http.MethodGet, accessPath(user.ID), env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("a missing label must not fail the review: %d (%s)", rec.Code, rec.Body.String())
	}
	body := decode[userAccessResponse](t, rec)
	if body.Provider != "" {
		t.Fatalf("expected no provider name, got %q", body.Provider)
	}
	if body.User.AuthSource != "sso" {
		t.Fatal("the account is still exactly as federated as it was")
	}
}

func TestUserAccessSaysWhereAGroupMembershipCameFrom(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	user := env.store.addUser("devops", "pw", db.RoleUser)
	local := env.store.addGroup("platform")
	derived := env.store.addGroup("sre")

	env.store.members[local.ID] = map[uint]bool{user.ID: true}
	env.store.members[derived.ID] = map[uint]bool{user.ID: true}
	env.store.memberSource[derived.ID] = map[uint]string{user.ID: "sso"}

	rec := env.do(t, http.MethodGet, accessPath(user.ID), env.tokenFor(t, admin), nil)
	body := decode[userAccessResponse](t, rec)

	if len(body.Groups) != 2 {
		t.Fatalf("expected two memberships, got %#v", body.Groups)
	}
	// Sorted by name: platform, then sre.
	if body.Groups[0].Name != "platform" || body.Groups[0].Source != "local" {
		t.Fatalf("expected a local membership first: %#v", body.Groups[0])
	}
	// A derived membership is reconciled away when the directory stops asserting
	// it, which is a different fact about how long the access lasts.
	if body.Groups[1].Name != "sre" || body.Groups[1].Source != "sso" {
		t.Fatalf("expected the derived membership named as such: %#v", body.Groups[1])
	}
}

func TestUserAccessOrdersProductionFirst(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	user := env.store.addUser("devops", "pw", db.RoleUser)
	dev := env.store.addCluster("dev-eu", db.EnvDev)
	prod := env.store.addCluster("prod-eu", db.EnvProd)
	staging := env.store.addCluster("staging-eu", db.EnvStaging)

	for _, cluster := range []*db.Cluster{dev, prod, staging} {
		env.store.grant(user.ID, cluster.ID, db.K8sRoleView, nil)
	}

	rec := env.do(t, http.MethodGet, accessPath(user.ID), env.tokenFor(t, admin), nil)
	body := decode[userAccessResponse](t, rec)

	// A review is read top-down, and the rows that decide whether it is signed
	// are the ones on the clusters that matter.
	want := []string{"prod-eu", "staging-eu", "dev-eu"}
	for i, name := range want {
		if body.Clusters[i].Cluster != name {
			t.Fatalf("expected %v, got %q at %d", want, body.Clusters[i].Cluster, i)
		}
	}
}

func TestUserAccessIgnoresAGrantOnAClusterThatIsGone(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	user := env.store.addUser("devops", "pw", db.RoleUser)

	// A grant whose cluster was removed is not access to anything.
	env.store.access[user.ID] = map[uint]db.UserClusterAccess{
		777: {UserID: user.ID, ClusterID: 777, K8sRole: db.K8sRoleClusterAdmin},
	}

	rec := env.do(t, http.MethodGet, accessPath(user.ID), env.tokenFor(t, admin), nil)
	body := decode[userAccessResponse](t, rec)
	if len(body.Clusters) != 0 {
		t.Fatalf("expected nothing, got %#v", body.Clusters)
	}
}

/* ------------------------------------------------- where a sign-in came from --- */

func TestLoginRecordsWhereItCameFrom(t *testing.T) {
	env := newTestEnv(t)
	env.store.addUser("devops", "pw", db.RoleUser)

	rec := env.do(t, http.MethodPost, "/api/v1/auth/login", "", map[string]string{
		"username": "devops",
		"password": "pw",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}

	// "When did they last sign in" was already half an answer: a dormant account
	// waking up from an address nobody recognises is the shape of a compromise,
	// and a date alone cannot show it.
	var stored *db.User
	for _, user := range env.store.users {
		if user.Username == "devops" {
			stored = user
		}
	}
	if stored == nil || stored.LastLoginAt == nil {
		t.Fatal("the sign-in was not recorded at all")
	}
	if stored.LastLoginAddr == "" {
		t.Fatal("expected the sign-in address to be recorded")
	}
}

func TestUserAccessReportsTheLastSignInAddress(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	user := env.store.addUser("devops", "pw", db.RoleUser)
	user.LastLoginAddr = "203.0.113.9"

	rec := env.do(t, http.MethodGet, accessPath(user.ID), env.tokenFor(t, admin), nil)
	body := decode[userAccessResponse](t, rec)
	if body.User.LastLoginAddr != "203.0.113.9" {
		t.Fatalf("expected the address on the review, got %q", body.User.LastLoginAddr)
	}
}
