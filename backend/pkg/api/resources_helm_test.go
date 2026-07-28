package api

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// A Helm release is not an API object KubeMG can ask the cluster to validate —
// it is a blob KubeMG decodes and, on a write, re-encodes. So what is pinned
// here is that the encoding round-trips exactly, that the read tolerates the
// shapes Helm has actually written over the years, and that a release name can
// never widen the label selector or the Secret name it is put into.

// helmSecretData renders a release the way Helm 3 stores it, so the decoder is
// tested against the real encoding rather than against its own output.
func helmSecretData(t *testing.T, release map[string]any) string {
	t.Helper()

	document, err := json.Marshal(release)
	if err != nil {
		t.Fatalf("marshalling the release: %v", err)
	}

	var buffer bytes.Buffer
	writer := gzip.NewWriter(&buffer)
	if _, err := writer.Write(document); err != nil {
		t.Fatalf("compressing the release: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("compressing the release: %v", err)
	}

	inner := base64.StdEncoding.EncodeToString(buffer.Bytes())
	return base64.StdEncoding.EncodeToString([]byte(inner))
}

func TestHelmPayloadRoundTrips(t *testing.T) {
	release := map[string]any{
		"name":      "checkout",
		"namespace": "shop",
		"version":   float64(3),
		"info":      map[string]any{"status": "deployed", "last_deployed": "2026-07-01T10:00:00Z"},
		"chart": map[string]any{
			"metadata": map[string]any{"name": "checkout", "version": "1.4.2", "appVersion": "2.0.0"},
		},
		"config":   map[string]any{"replicaCount": float64(2)},
		"manifest": "apiVersion: v1\nkind: Service\n",
	}

	encoded, err := encodeHelmPayload(mustJSON(t, release))
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}

	document, err := decodeHelmPayload(encoded)
	if err != nil {
		t.Fatalf("decoding: %v", err)
	}

	var back map[string]any
	if err := json.Unmarshal(document, &back); err != nil {
		t.Fatalf("the round-tripped payload is not JSON: %v", err)
	}
	if got, want := back["manifest"], release["manifest"]; got != want {
		t.Fatalf("manifest = %v, want %v", got, want)
	}

	view := helmView(back)
	if view.Name != "checkout" || view.Namespace != "shop" || view.Revision != 3 {
		t.Fatalf("view = %+v, want checkout/shop at revision 3", view)
	}
	if view.ChartVersion != "1.4.2" || view.AppVersion != "2.0.0" || view.Status != "deployed" {
		t.Fatalf("view = %+v, want the chart metadata carried through", view)
	}
	if view.UpdatedAt.IsZero() {
		t.Fatal("expected last_deployed to be parsed")
	}
}

// Helm reads releases written before it compressed them, and so does this.
func TestHelmPayloadReadsUncompressedRelease(t *testing.T) {
	document := mustJSON(t, map[string]any{"name": "legacy", "version": float64(1)})
	inner := base64.StdEncoding.EncodeToString(document)
	encoded := base64.StdEncoding.EncodeToString([]byte(inner))

	back, err := decodeHelmPayload(encoded)
	if err != nil {
		t.Fatalf("decoding an uncompressed release: %v", err)
	}
	if !bytes.Equal(back, document) {
		t.Fatalf("decoded = %s, want %s", back, document)
	}
}

func TestHelmReleaseOfReadsAStoredSecret(t *testing.T) {
	secret := map[string]any{
		"metadata": map[string]any{
			"name":      "sh.helm.release.v1.checkout.v3",
			"namespace": "shop",
			"labels":    map[string]any{"owner": "helm", "name": "checkout", "version": "3"},
		},
		"type": helmSecretType,
		"data": map[string]any{
			"release": helmSecretData(t, map[string]any{
				"name": "checkout", "namespace": "shop", "version": float64(3),
			}),
		},
	}

	release, err := helmReleaseOf(secret)
	if err != nil {
		t.Fatalf("reading the release: %v", err)
	}
	if got := helmRevision(release); got != 3 {
		t.Fatalf("revision = %d, want 3", got)
	}

	// A Secret carrying Helm's label but no readable release is somebody else's,
	// and is skipped rather than failing the list it appears in.
	if _, err := helmReleaseOf(map[string]any{"data": map[string]any{"release": "not base64!"}}); err == nil {
		t.Fatal("expected an unreadable release to be refused")
	}
	if _, err := helmReleaseOf(map[string]any{}); err == nil {
		t.Fatal("expected a secret with no release to be refused")
	}
}

// The next revision is the previous release with its values, revision and status
// replaced — everything else, the rendered manifest above all, carries across,
// because the cluster is still running exactly that.
func TestNextHelmSecretAppendsARevision(t *testing.T) {
	release := map[string]any{
		"name":      "checkout",
		"namespace": "shop",
		"version":   float64(3),
		"info":      map[string]any{"status": "deployed", "first_deployed": "2026-01-01T00:00:00Z"},
		"chart":     map[string]any{"metadata": map[string]any{"name": "checkout"}},
		"config":    map[string]any{"replicaCount": float64(2)},
		"manifest":  "apiVersion: v1\nkind: Service\n",
	}
	secret := map[string]any{
		"metadata": map[string]any{
			"name":   "sh.helm.release.v1.checkout.v3",
			"labels": map[string]any{"owner": "helm", "name": "checkout", "status": "deployed", "version": "3"},
		},
	}

	next, err := nextHelmSecret(secret, release, map[string]any{"replicaCount": float64(5)},
		"shop", "checkout", 3)
	if err != nil {
		t.Fatalf("building the next revision: %v", err)
	}

	metadata := next["metadata"].(map[string]any)
	if got, want := metadata["name"], "sh.helm.release.v1.checkout.v4"; got != want {
		t.Fatalf("secret name = %v, want %v", got, want)
	}
	if got, want := next["type"], helmSecretType; got != want {
		t.Fatalf("secret type = %v, want %v", got, want)
	}

	labels := metadata["labels"].(map[string]any)
	if labels["version"] != "4" || labels["status"] != "deployed" || labels["owner"] != "helm" {
		t.Fatalf("labels = %v, want the next revision marked deployed", labels)
	}

	data := next["data"].(map[string]any)
	document, err := decodeHelmPayload(data["release"].(string))
	if err != nil {
		t.Fatalf("decoding the new revision: %v", err)
	}
	var written map[string]any
	if err := json.Unmarshal(document, &written); err != nil {
		t.Fatalf("the new revision is not JSON: %v", err)
	}

	if got := helmRevision(written); got != 4 {
		t.Fatalf("revision = %d, want 4", got)
	}
	if got := written["config"].(map[string]any)["replicaCount"]; got != float64(5) {
		t.Fatalf("replicaCount = %v, want 5", got)
	}
	// The chart is not re-rendered here, so the manifest has to be the previous
	// one: it is what the cluster is still running.
	if got, want := written["manifest"], release["manifest"]; got != want {
		t.Fatalf("manifest = %v, want it carried across unchanged", got)
	}
	info := written["info"].(map[string]any)
	if info["status"] != "deployed" || info["description"] != helmUpdateDescription {
		t.Fatalf("info = %v, want a described deployed revision", info)
	}
	if info["first_deployed"] != "2026-01-01T00:00:00Z" {
		t.Fatal("expected the original install time to be kept")
	}

	// The previous release must not have been edited in place: Helm's storage is
	// append-only, and rewriting revision 3 would rewrite history.
	if helmRevision(release) != 3 || release["config"].(map[string]any)["replicaCount"] != float64(2) {
		t.Fatal("expected the previous revision to be left alone")
	}
}

// The release name reaches both a label selector and a Secret name, so it is
// validated rather than escaped: a comma would add a selector term.
func TestHelmReleaseNamePattern(t *testing.T) {
	for _, name := range []string{"checkout", "my-app", "app.v2", "a1"} {
		if !helmReleaseName.MatchString(name) {
			t.Fatalf("expected %q to be a release name", name)
		}
	}
	for _, name := range []string{
		"", "-leading", "trailing-", "UPPER", "with space",
		"owner=helm,name=other", "a/b", "a\nb", "app,status=deployed",
	} {
		if helmReleaseName.MatchString(name) {
			t.Fatalf("expected %q to be refused", name)
		}
	}
}

func TestHelmSecretsPath(t *testing.T) {
	got := helmSecretsPath("shop", "")
	if want := "/api/v1/namespaces/shop/secrets?labelSelector=owner%3Dhelm"; got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}

	got = helmSecretsPath("shop", "checkout")
	if !strings.Contains(got, "owner%3Dhelm%2Cname%3Dcheckout") {
		t.Fatalf("path = %q, want it scoped to one release", got)
	}

	// An unscoped grant's all-namespaces read is one cluster-wide list, still
	// narrowed to Helm's own Secrets by the selector.
	if got, want := helmSecretsPath("", ""), "/api/v1/secrets?labelSelector=owner%3Dhelm"; got != want {
		t.Fatalf("cluster-wide path = %q, want %q", got, want)
	}
}

// A scoped grant's "all namespaces" is one read per granted namespace, never a
// cluster-wide list — the same rule every other list in the inventory follows.
func TestHelmScopePaths(t *testing.T) {
	paths := helmScopePaths(readScope{Namespaces: []string{"team-a", "team-b"}, All: true})
	if len(paths) != 2 {
		t.Fatalf("paths = %v, want one per granted namespace", paths)
	}
	for _, path := range paths {
		if strings.HasPrefix(path, "/api/v1/secrets") {
			t.Fatalf("path %q reaches past the grant", path)
		}
	}

	if paths := helmScopePaths(readScope{All: true}); len(paths) != 1 {
		t.Fatalf("paths = %v, want a single cluster-wide read", paths)
	}
}

func TestHelmValuesYAML(t *testing.T) {
	document, err := helmValuesYAML(map[string]any{"config": map[string]any{"replicaCount": float64(2)}})
	if err != nil {
		t.Fatalf("rendering values: %v", err)
	}
	if !strings.Contains(document, "replicaCount: 2") {
		t.Fatalf("yaml = %q, want the values rendered", document)
	}

	// A release installed with no values renders as an empty mapping, not as
	// `null`: the editor has to open on something that can be typed into.
	if got, err := helmValuesYAML(map[string]any{}); err != nil || got != "{}\n" {
		t.Fatalf("empty values = %q (%v), want %q", got, err, "{}\n")
	}
}

// A release name that is not one never reaches the cluster.
func TestHelmValuesRefusesBadReleaseName(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	token := env.tokenFor(t, admin)

	rec := env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+
			"/resources/helm/releases/Not%20A%20Name/values?namespace=shop",
		token, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d (%s)",
			http.StatusBadRequest, rec.Code, rec.Body.String())
	}
}

// Namespace enforcement is the same here as for every other namespaced read: a
// namespace outside the grant is refused before anything reaches the tunnel.
func TestHelmReleasesRefuseNamespaceOutsideGrant(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("scoped", "secret123", "user")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	env.store.grant(user.ID, cluster.ID, "edit", []string{"team-a"})
	token := env.tokenFor(t, user)

	rec := env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/helm/releases?namespace=team-b",
		token, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d (%s)",
			http.StatusForbidden, rec.Code, rec.Body.String())
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	document, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	return document
}
