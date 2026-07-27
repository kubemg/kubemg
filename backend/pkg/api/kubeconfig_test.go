package api

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/tools/clientcmd"

	"github.com/kubemg/kubemg/backend/pkg/auth"
	"github.com/kubemg/kubemg/backend/pkg/db"
	"github.com/kubemg/kubemg/backend/pkg/k8s"
)

const testPEM = "-----BEGIN CERTIFICATE-----\nMIIBkTCB+w==\n-----END CERTIFICATE-----\n"

func generatePath(id uint) string {
	return "/api/v1/clusters/" + itoa(id) + "/kubeconfig/generate"
}

func TestGenerateKubeconfigAsAdmin(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	cluster := env.store.addCluster("prod-eu", db.EnvProd)
	cluster.CACertData = base64.StdEncoding.EncodeToString([]byte(testPEM))

	rec := env.do(t, http.MethodPost, generatePath(cluster.ID), env.tokenFor(t, admin), map[string]any{
		"ttl_seconds": 28800,
		"namespace":   "payments",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}

	body := decode[generateKubeconfigResponse](t, rec)
	if body.Cluster != "prod-eu" || body.Namespace != "payments" {
		t.Fatalf("unexpected response metadata: %+v", body)
	}
	if body.TTLSeconds != 28800 {
		t.Fatalf("expected ttl_seconds 28800, got %d", body.TTLSeconds)
	}
	if body.Context != "admin@prod-eu" {
		t.Fatalf("unexpected context %q", body.Context)
	}
	if body.Filename != "prod-eu-admin.kubeconfig" {
		t.Fatalf("unexpected filename %q", body.Filename)
	}
	if body.ServiceAcct != "kubemg-admin" {
		t.Fatalf("unexpected service account %q", body.ServiceAcct)
	}
	if body.K8sRole != db.K8sRoleClusterAdmin {
		t.Fatalf("expected admin to report %q, got %q", db.K8sRoleClusterAdmin, body.K8sRole)
	}

	// The payload must be a usable kubeconfig, not just a string.
	cfg, err := clientcmd.Load([]byte(body.Kubeconfig))
	if err != nil {
		t.Fatalf("returned kubeconfig does not parse: %v\n%s", err, body.Kubeconfig)
	}
	kubeCtx := cfg.Contexts[cfg.CurrentContext]
	if kubeCtx == nil || kubeCtx.Namespace != "payments" {
		t.Fatalf("unexpected context in kubeconfig: %+v", cfg.Contexts)
	}
	if cfg.AuthInfos[kubeCtx.AuthInfo].Token != "issued-token" {
		t.Fatal("kubeconfig does not carry the issued token")
	}
	if cfg.Clusters[kubeCtx.Cluster].Server != cluster.APIURL {
		t.Fatalf("unexpected server %q", cfg.Clusters[kubeCtx.Cluster].Server)
	}
	if string(cfg.Clusters[kubeCtx.Cluster].CertificateAuthorityData) != testPEM {
		t.Fatal("kubeconfig does not carry the decoded cluster CA")
	}

	// The cluster's own admin credentials must never reach the caller.
	if strings.Contains(body.Kubeconfig, "secret-token") || strings.Contains(rec.Body.String(), "secret-token") {
		t.Fatal("response leaked the cluster service account token")
	}

	if env.tokens.lastRequest.ServiceAccountNamespace != "kubemg-system" {
		t.Fatalf("unexpected SA namespace %q", env.tokens.lastRequest.ServiceAccountNamespace)
	}
	if env.tokens.lastRequest.TTL != 8*time.Hour {
		t.Fatalf("expected an 8h TTL to reach the issuer, got %s", env.tokens.lastRequest.TTL)
	}
	if env.tokens.lastCluster.ID != cluster.ID {
		t.Fatalf("issuer targeted the wrong cluster: %+v", env.tokens.lastCluster)
	}
}

func TestGenerateKubeconfigDefaultsTTLAndNamespace(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	cluster := env.store.addCluster("prod-eu", db.EnvProd)

	rec := env.do(t, http.MethodPost, generatePath(cluster.ID), env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}

	body := decode[generateKubeconfigResponse](t, rec)
	if body.TTLSeconds != 3600 {
		t.Fatalf("expected default ttl_seconds 3600, got %d", body.TTLSeconds)
	}
	if body.Namespace != "default" {
		t.Fatalf("expected default namespace, got %q", body.Namespace)
	}
}

func TestGenerateKubeconfigAcceptsQueryParams(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	cluster := env.store.addCluster("prod-eu", db.EnvProd)

	rec := env.do(t, http.MethodPost,
		generatePath(cluster.ID)+"?ttl_seconds=86400&namespace=infra", env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}

	body := decode[generateKubeconfigResponse](t, rec)
	if body.TTLSeconds != 86400 || body.Namespace != "infra" {
		t.Fatalf("query parameters were not applied: %+v", body)
	}
}

func TestGenerateKubeconfigForGrantedUser(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("devops", "pw", db.RoleUser)
	cluster := env.store.addCluster("dev-eu", db.EnvDev)
	env.store.grant(user.ID, cluster.ID, db.K8sRoleEdit, []string{"team-a", "team-b"})

	rec := env.do(t, http.MethodPost, generatePath(cluster.ID), env.tokenFor(t, user), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}

	body := decode[generateKubeconfigResponse](t, rec)
	if body.Namespace != "team-a" {
		t.Fatalf("expected the first granted namespace, got %q", body.Namespace)
	}
	if body.K8sRole != db.K8sRoleEdit {
		t.Fatalf("expected k8s_role %q, got %q", db.K8sRoleEdit, body.K8sRole)
	}
	if body.ServiceAcct != "kubemg-devops" {
		t.Fatalf("unexpected service account %q", body.ServiceAcct)
	}
}

func TestGenerateKubeconfigAllowsGrantedNamespace(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("devops", "pw", db.RoleUser)
	cluster := env.store.addCluster("dev-eu", db.EnvDev)
	env.store.grant(user.ID, cluster.ID, db.K8sRoleEdit, []string{"team-a", "team-b"})

	rec := env.do(t, http.MethodPost, generatePath(cluster.ID), env.tokenFor(t, user),
		map[string]any{"namespace": "team-b"})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	if got := decode[generateKubeconfigResponse](t, rec).Namespace; got != "team-b" {
		t.Fatalf("expected namespace \"team-b\", got %q", got)
	}
}

func TestGenerateKubeconfigRejectsNamespaceOutsideGrant(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("devops", "pw", db.RoleUser)
	cluster := env.store.addCluster("dev-eu", db.EnvDev)
	env.store.grant(user.ID, cluster.ID, db.K8sRoleView, []string{"team-a"})

	rec := env.do(t, http.MethodPost, generatePath(cluster.ID), env.tokenFor(t, user),
		map[string]any{"namespace": "kube-system"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusForbidden, rec.Code, rec.Body.String())
	}
	if env.tokens.calls != 0 {
		t.Fatal("a rejected namespace must not mint a token")
	}
}

func TestGenerateKubeconfigUnscopedGrantAllowsAnyNamespace(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("devops", "pw", db.RoleUser)
	cluster := env.store.addCluster("dev-eu", db.EnvDev)
	env.store.grant(user.ID, cluster.ID, db.K8sRoleEdit, nil)

	rec := env.do(t, http.MethodPost, generatePath(cluster.ID), env.tokenFor(t, user),
		map[string]any{"namespace": "anything"})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
}

func TestGenerateKubeconfigRejectsUngrantedCluster(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("devops", "pw", db.RoleUser)
	cluster := env.store.addCluster("prod-eu", db.EnvProd)

	rec := env.do(t, http.MethodPost, generatePath(cluster.ID), env.tokenFor(t, user), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusForbidden, rec.Code, rec.Body.String())
	}
	if env.tokens.calls != 0 {
		t.Fatal("an unauthorized cluster must not mint a token")
	}
}

func TestGenerateKubeconfigRequiresAuth(t *testing.T) {
	env := newTestEnv(t)
	cluster := env.store.addCluster("prod-eu", db.EnvProd)

	rec := env.do(t, http.MethodPost, generatePath(cluster.ID), "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestGenerateKubeconfigUnknownCluster(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)

	rec := env.do(t, http.MethodPost, generatePath(4242), env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, rec.Code)
	}
}

func TestGenerateKubeconfigRejectsInvalidTTL(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	cluster := env.store.addCluster("prod-eu", db.EnvProd)

	for _, ttl := range []int64{1, 60, 86401, -3600} {
		rec := env.do(t, http.MethodPost, generatePath(cluster.ID), env.tokenFor(t, admin),
			map[string]any{"ttl_seconds": ttl})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("ttl %d: expected status %d, got %d", ttl, http.StatusBadRequest, rec.Code)
		}
	}
	if env.tokens.calls != 0 {
		t.Fatal("invalid TTLs must not reach the cluster")
	}
}

func TestGenerateKubeconfigRejectsNonNumericTTLQuery(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	cluster := env.store.addCluster("prod-eu", db.EnvProd)

	rec := env.do(t, http.MethodPost, generatePath(cluster.ID)+"?ttl_seconds=soon", env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
	}
}

func TestGenerateKubeconfigUpstreamFailure(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	cluster := env.store.addCluster("prod-eu", db.EnvProd)
	env.tokens.err = &k8s.UpstreamError{Cluster: "prod-eu", Op: "token request", Err: errors.New("forbidden")}

	rec := env.do(t, http.MethodPost, generatePath(cluster.ID), env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusBadGateway, rec.Code, rec.Body.String())
	}
}

func TestGenerateKubeconfigMissingClusterCredentials(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	cluster := env.store.addCluster("prod-eu", db.EnvProd)
	env.tokens.err = k8s.ErrMissingCredentials

	rec := env.do(t, http.MethodPost, generatePath(cluster.ID), env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusFailedDependency {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusFailedDependency, rec.Code, rec.Body.String())
	}
}

func TestGenerateKubeconfigRejectsInvalidStoredCA(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	cluster := env.store.addCluster("prod-eu", db.EnvProd)
	cluster.CACertData = "!!! not a certificate !!!"

	rec := env.do(t, http.MethodPost, generatePath(cluster.ID), env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusInternalServerError, rec.Code, rec.Body.String())
	}
}

func TestGenerateKubeconfigRouteAbsentWithoutIssuer(t *testing.T) {
	store := newFakeStore()
	admin := store.addUser("admin", "pw", db.RoleAdmin)
	cluster := store.addCluster("prod-eu", db.EnvProd)
	manager := authManagerForTest()

	env := &testEnv{
		router: NewRouter(Options{Store: store, JWT: manager}),
		store:  store,
		jwt:    manager,
	}

	rec := env.do(t, http.MethodPost, generatePath(cluster.ID), env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status %d without a token issuer, got %d", http.StatusNotFound, rec.Code)
	}
}

// An agent cluster stores no API URL and no service account token by design, so
// the direct-mode path cannot serve it. Its kubeconfig points at KubeMG's own
// proxy instead of failing with "missing an API URL".
func TestGenerateKubeconfigForAgentCluster(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	cluster := env.store.addAgentCluster("edge-us", db.EnvStaging, "kmg_token")

	rec := env.do(t, http.MethodPost, generatePath(cluster.ID), env.tokenFor(t, admin), map[string]any{
		"namespace": "payments",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}

	body := decode[generateKubeconfigResponse](t, rec)
	wantServer := "https://kubemg.example.com/api/v1/clusters/" + itoa(cluster.ID) + "/proxy"
	if body.ConnectionMode != db.ModeAgent || body.Server != wantServer {
		t.Fatalf("unexpected credential shape: %+v", body)
	}
	if body.ServiceAcct != "" {
		t.Fatalf("agent mode impersonates; no service account should be reported, got %q", body.ServiceAcct)
	}
	if body.Warning != "" {
		t.Fatalf("an https public URL needs no warning, got %q", body.Warning)
	}

	cfg, err := clientcmd.Load([]byte(body.Kubeconfig))
	if err != nil {
		t.Fatalf("returned kubeconfig does not parse: %v\n%s", err, body.Kubeconfig)
	}
	kubeCtx := cfg.Contexts[cfg.CurrentContext]
	if kubeCtx == nil || kubeCtx.Namespace != "payments" {
		t.Fatalf("unexpected context in kubeconfig: %+v", cfg.Contexts)
	}
	if cfg.Clusters[kubeCtx.Cluster].Server != wantServer {
		t.Fatalf("kubeconfig points at %q", cfg.Clusters[kubeCtx.Cluster].Server)
	}

	// The credential must be a KubeMG token confined to this cluster's proxy,
	// not a session key for the rest of the API.
	claims, err := env.jwt.Parse(cfg.AuthInfos[kubeCtx.AuthInfo].Token)
	if err != nil {
		t.Fatalf("kubeconfig token does not verify: %v", err)
	}
	if claims.Scope != auth.ScopeProxy || claims.ClusterID != cluster.ID {
		t.Fatalf("unexpected token claims: %+v", claims)
	}

	// The agent's registration token is a cluster credential and must not leak.
	if strings.Contains(rec.Body.String(), "kmg_token") {
		t.Fatal("response leaked the agent registration token")
	}
}

func TestGenerateKubeconfigForAgentClusterHonoursGrantScope(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("devops", "pw", db.RoleUser)
	cluster := env.store.addAgentCluster("edge-us", db.EnvStaging, "kmg_token")
	env.store.grant(user.ID, cluster.ID, db.K8sRoleView, []string{"payments"})

	rec := env.do(t, http.MethodPost, generatePath(cluster.ID)+"?namespace=infra", env.tokenFor(t, user), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusForbidden, rec.Code, rec.Body.String())
	}
}

// A proxy-scoped token is a file on a laptop: it drives kubectl against its own
// cluster and must not open anything else.
func TestProxyScopedTokenIsConfinedToItsCluster(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	cluster := env.store.addAgentCluster("edge-us", db.EnvStaging, "kmg_token")

	token, _, err := env.jwt.GenerateProxyToken(admin.ID, admin.Username, admin.Role, cluster.ID, time.Hour)
	if err != nil {
		t.Fatalf("issue proxy token: %v", err)
	}

	for _, path := range []string{
		"/api/v1/clusters",
		"/api/v1/users",
		"/api/v1/clusters/" + itoa(cluster.ID+1) + "/proxy/api/v1/pods",
	} {
		rec := env.do(t, http.MethodGet, path, token, nil)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s: expected status %d, got %d (%s)",
				path, http.StatusForbidden, rec.Code, rec.Body.String())
		}
	}

	// Its own cluster's proxy is reachable: no tunnel is attached in this test,
	// so it gets that far and fails on the connection, not on the token.
	rec := env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/proxy/api/v1/pods", token, nil)
	if rec.Code == http.StatusForbidden || rec.Code == http.StatusUnauthorized {
		t.Fatalf("proxy-scoped token was refused on its own cluster: %d (%s)", rec.Code, rec.Body.String())
	}
}
