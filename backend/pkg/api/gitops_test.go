package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * GitOps sync state. What is pinned here is the part that is quiet when wrong:
 * that a scoped grant sees only its own namespaces' entries out of records
 * KubeMG read as itself, that the secret-bearing fields of those records never
 * reach a response, that a controller which is not installed is an answer
 * rather than a failure, that Flux is never credited with a per-object `synced`
 * it did not write, and that the record behind one object comes from the
 * object's own metadata.
 */

const (
	argoApps     = "/apis/argoproj.io/v1alpha1/applications"
	fluxKsV1     = "/apis/kustomize.toolkit.fluxcd.io/v1/kustomizations"
	fluxKsBeta   = "/apis/kustomize.toolkit.fluxcd.io/v1beta2/kustomizations"
	fluxHelmV2   = "/apis/helm.toolkit.fluxcd.io/v2/helmreleases"
	pagedSuffix  = "?limit=250"
	secretMarker = "s3cr3t-value"
)

var adminGrant = db.UserClusterAccess{K8sRole: db.K8sRoleClusterAdmin}

func viewOn(namespaces ...string) db.UserClusterAccess {
	return db.UserClusterAccess{K8sRole: db.K8sRoleView, Namespaces: db.JoinNamespaces(namespaces)}
}

func argoApp(namespace, name string, sync, health string, resources ...map[string]any) map[string]any {
	if resources == nil {
		resources = []map[string]any{}
	}
	return map[string]any{
		"metadata": meta(namespace, name),
		"spec": map[string]any{
			"project": "default",
			"source": map[string]any{
				"repoURL":        "https://deploy:" + secretMarker + "@git.example.com/shop.git",
				"path":           "overlays/prod",
				"targetRevision": "main",
				"helm": map[string]any{
					"values":       "password: " + secretMarker,
					"valuesObject": map[string]any{"password": secretMarker},
					"parameters":   []any{map[string]any{"name": "token", "value": secretMarker}},
				},
				"plugin": map[string]any{"env": []any{map[string]any{"name": "KEY", "value": secretMarker}}},
			},
			"destination": map[string]any{"server": "https://kubernetes.default.svc", "namespace": "shop"},
			"syncPolicy":  map[string]any{"automated": map[string]any{"selfHeal": true}},
		},
		"status": map[string]any{
			"sync":      map[string]any{"status": sync, "revision": "abc123"},
			"health":    map[string]any{"status": health},
			"resources": resources,
		},
	}
}

func argoEntry(group, kind, namespace, name, sync, health string) map[string]any {
	return map[string]any{
		"group": group, "kind": kind, "namespace": namespace, "name": name,
		"status": sync, "health": map[string]any{"status": health},
	}
}

func fluxKsFixture(namespace, name string, ready string, inventory ...string) map[string]any {
	entries := []any{}
	for _, id := range inventory {
		entries = append(entries, map[string]any{"id": id, "v": "v1"})
	}
	return map[string]any{
		"metadata": meta(namespace, name),
		"spec": map[string]any{
			"path":      "./apps",
			"sourceRef": map[string]any{"kind": "GitRepository", "name": "flux-system"},
			"postBuild": map[string]any{"substitute": map[string]any{"DB_PASSWORD": secretMarker}},
		},
		"status": map[string]any{
			"lastAppliedRevision":   "main@sha1:aaa",
			"lastAttemptedRevision": "main@sha1:aaa",
			"conditions":            []any{map[string]any{"type": "Ready", "status": ready, "message": "Applied revision"}},
			"inventory":             map[string]any{"entries": entries},
		},
	}
}

func fluxHelmFixture(namespace, name string) map[string]any {
	return map[string]any{
		"metadata": meta(namespace, name),
		"spec": map[string]any{
			"chart": map[string]any{"spec": map[string]any{
				"chart": "redis", "version": "18.x",
				"sourceRef": map[string]any{"kind": "HelmRepository", "name": "bitnami"},
			}},
			"values":         map[string]any{"auth": map[string]any{"password": secretMarker}},
			"driftDetection": map[string]any{"mode": "warn"},
		},
		"status": map[string]any{
			"conditions": []any{map[string]any{"type": "Ready", "status": "True"}},
			"history":    []any{map[string]any{"chartName": "redis", "chartVersion": "18.6.1"}},
		},
	}
}

func readAll(t *testing.T, cluster *fakeCluster) gitopsRead {
	t.Helper()
	read, err := readGitopsRecords(cluster)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	return read
}

func recordNamed(records []gitopsRecord, name string) *gitopsRecord {
	for i := range records {
		if records[i].Name == name {
			return &records[i]
		}
	}
	return nil
}

func TestGitopsArgoRecordReadsTheControllersOwnAnswer(t *testing.T) {
	cluster := newFakeCluster()
	cluster.list(argoApps, "", argoApp("argocd", "shop", "OutOfSync", "Degraded",
		argoEntry("apps", "Deployment", "shop", "web", "OutOfSync", "Degraded"),
		argoEntry("", "Service", "shop", "web", "Synced", "Healthy"),
	))

	read := readAll(t, cluster)
	shop := recordNamed(read.records, "shop")
	if shop == nil {
		t.Fatalf("expected the application in the read, got %+v", read.records)
	}
	if shop.Sync != syncOutOfSync || shop.Health != healthDegraded {
		t.Fatalf("expected out_of_sync/degraded, got %s/%s", shop.Sync, shop.Health)
	}
	if shop.AutoHeal == nil || !*shop.AutoHeal {
		t.Fatalf("expected selfHeal to read as auto_heal, got %v", shop.AutoHeal)
	}
	if shop.Revision != "abc123" || shop.Remote != "" {
		t.Fatalf("expected revision abc123 and a local destination, got %q / %q", shop.Revision, shop.Remote)
	}
	if got := shop.Sources[0].Repo; got != "https://git.example.com/shop.git" {
		t.Fatalf("expected the repository URL without its credentials, got %q", got)
	}
	if len(shop.Resources) != 2 || shop.Resources[0].Sync != syncOutOfSync {
		t.Fatalf("expected both entries with their own sync state, got %+v", shop.Resources)
	}
}

func TestGitopsArgoRecordNamesARemoteDestination(t *testing.T) {
	app := argoApp("argocd", "edge", "Synced", "Healthy")
	app["spec"].(map[string]any)["destination"] = map[string]any{"name": "edge-eu", "namespace": "shop"}
	cluster := newFakeCluster()
	cluster.list(argoApps, "", app)

	if got := recordNamed(readAll(t, cluster).records, "edge").Remote; got != "edge-eu" {
		t.Fatalf("expected the destination cluster to be named, got %q", got)
	}
}

// The records are read as KubeMG, so the response struct is the only thing
// between a values file and every user with a grant. None of these fields has
// a place in it; this pins that they stay out.
func TestGitopsRecordsNeverCarrySecretBearingFields(t *testing.T) {
	cluster := newFakeCluster()
	cluster.list(argoApps, "", argoApp("argocd", "shop", "Synced", "Healthy"))
	cluster.list(fluxKsV1, "", fluxKsFixture("flux-system", "apps", "True"))
	cluster.list(fluxHelmV2, "", fluxHelmFixture("cache", "redis"))

	read := readAll(t, cluster)
	if len(read.records) != 3 {
		t.Fatalf("expected three records, got %d", len(read.records))
	}
	body, _ := json.Marshal(read.records)
	for _, forbidden := range []string{secretMarker, "values", "valuesObject", "parameters", "postBuild", "substitute", "plugin"} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("a response carried %q: %s", forbidden, body)
		}
	}
}

func TestGitopsAToolThatIsNotInstalledIsAnAnswer(t *testing.T) {
	cluster := newFakeCluster()
	// Argo CD absent; kustomize-controller on its older API only; helm-controller refused.
	cluster.list(fluxKsBeta, "", fluxKsFixture("flux-system", "apps", "True"))
	cluster.refuse(fluxHelmV2+pagedSuffix, http.StatusForbidden)

	read := readAll(t, cluster)
	byTool := map[string]gitopsToolStatus{}
	for _, status := range read.tools {
		byTool[status.Tool] = status
	}

	if argo := byTool[gitopsArgoCD]; argo.Installed || argo.Available || !strings.Contains(argo.Reason, "not installed") {
		t.Fatalf("expected Argo CD reported as not installed, got %+v", argo)
	}
	if ks := byTool[gitopsFluxKustomize]; !ks.Available {
		t.Fatalf("expected the v1beta2 fallback to answer, got %+v", ks)
	}
	if hr := byTool[gitopsFluxHelmRelease]; !hr.Installed || hr.Available || !strings.Contains(hr.Reason, "forbidden") {
		t.Fatalf("expected the refusal in the cluster's words, got %+v", hr)
	}
	if len(read.records) != 1 {
		t.Fatalf("expected the one readable record, got %+v", read.records)
	}
}

// Flux corrects drift rather than recording it. Crediting it with a per-object
// `synced` would be a claim nobody made.
func TestGitopsFluxNeverClaimsAPerObjectSync(t *testing.T) {
	ks := fluxKsFixture("flux-system", "apps", "True",
		"shop_web_apps_Deployment", "_shop__Namespace", "shop_settings__ConfigMap")
	ks["status"].(map[string]any)["lastAttemptedRevision"] = "main@sha1:bbb"
	cluster := newFakeCluster()
	cluster.list(fluxKsV1, "", ks)

	record := recordNamed(readAll(t, cluster).records, "apps")
	if record.Sync != syncOutOfSync || record.AttemptedRevision != "main@sha1:bbb" {
		t.Fatalf("expected an unapplied revision to read as out_of_sync, got %s / %q",
			record.Sync, record.AttemptedRevision)
	}
	if len(record.Resources) != 3 {
		t.Fatalf("expected the three inventory entries, got %+v", record.Resources)
	}
	for _, resource := range record.Resources {
		if resource.Sync != syncUnknown {
			t.Fatalf("expected every Flux entry unknown, got %+v", resource)
		}
	}
	namespace := record.Resources[1]
	if namespace.Kind != "Namespace" || namespace.Namespace != "" || namespace.Group != "" {
		t.Fatalf("expected a cluster-scoped core entry, got %+v", namespace)
	}
}

func TestGitopsFluxSuspendedAndFailedRecords(t *testing.T) {
	failed := fluxKsFixture("flux-system", "broken", "False")
	failed["status"].(map[string]any)["conditions"] = []any{map[string]any{
		"type": "Ready", "status": "False", "message": "kustomize build failed",
	}}
	suspended := fluxKsFixture("flux-system", "paused", "True")
	suspended["spec"].(map[string]any)["suspend"] = true

	cluster := newFakeCluster()
	cluster.list(fluxKsV1, "", failed, suspended)
	read := readAll(t, cluster)

	if r := recordNamed(read.records, "broken"); r.Sync != syncOutOfSync || r.Health != healthDegraded ||
		r.Message != "kustomize build failed" {
		t.Fatalf("expected a failed apply to read as out_of_sync with its reason, got %+v", r)
	}
	if r := recordNamed(read.records, "paused"); !r.Suspended || r.Health != healthSuspended {
		t.Fatalf("expected a suspended record, got %+v", r)
	}
}

func TestGitopsNarrowingToAScopedGrant(t *testing.T) {
	record := gitopsRecord{
		Tool: gitopsArgoCD, Namespace: "argocd", Name: "platform", Message: "Deployment team-b/api failed",
		Resources: []gitopsResource{
			{Kind: "Deployment", Namespace: "team-a", Name: "web"},
			{Kind: "Deployment", Namespace: "team-b", Name: "api"},
			{Kind: "ClusterRole", Name: "platform"},
		},
	}

	t.Run("unscoped sees everything", func(t *testing.T) {
		got, visible := narrowGitopsRecord(record, adminGrant)
		if !visible || len(got.Resources) != 3 || got.ResourcesHidden != 0 || got.Message == "" {
			t.Fatalf("expected the record untouched, got %+v", got)
		}
	})

	t.Run("scoped sees its own entries and a count of the rest", func(t *testing.T) {
		got, visible := narrowGitopsRecord(record, viewOn("team-a"))
		if !visible {
			t.Fatal("expected the record visible through its team-a entry")
		}
		if len(got.Resources) != 1 || got.Resources[0].Namespace != "team-a" {
			t.Fatalf("expected only the team-a entry, got %+v", got.Resources)
		}
		// team-b's Deployment and the cluster-scoped ClusterRole are both cut.
		if got.ResourcesHidden != 2 {
			t.Fatalf("expected two hidden entries, got %d", got.ResourcesHidden)
		}
		if got.Message != "" {
			t.Fatalf("expected the message dropped once anything was cut, got %q", got.Message)
		}
	})

	t.Run("no entry and no namespace of the caller's hides the record", func(t *testing.T) {
		if _, visible := narrowGitopsRecord(record, viewOn("team-c")); visible {
			t.Fatal("expected the record invisible to a grant it touches nothing of")
		}
	})

	t.Run("a destination namespace in the grant keeps an empty record visible", func(t *testing.T) {
		empty := gitopsRecord{Tool: gitopsFluxHelmRelease, Namespace: "flux-system", Name: "redis",
			destinationNamespace: "team-c"}
		if _, visible := narrowGitopsRecord(empty, viewOn("team-c")); !visible {
			t.Fatal("expected a release installing into team-c visible to team-c")
		}
	})

	t.Run("a remote record is invisible to a scoped grant", func(t *testing.T) {
		remote := record
		remote.Remote = "edge-eu"
		if _, visible := narrowGitopsRecord(remote, viewOn("team-a")); visible {
			t.Fatal("expected another cluster's namespaces never matched against this grant")
		}
	})
}

func TestGitopsRecordsSortWhatNeedsALookFirst(t *testing.T) {
	records := []gitopsRecord{
		{Name: "calm", Sync: syncSynced, Health: healthHealthy},
		{Name: "sick", Sync: syncSynced, Health: healthDegraded},
		{Name: "drifted", Sync: syncOutOfSync, Health: healthHealthy},
	}
	sortGitopsRecords(records)
	if records[0].Name != "drifted" || records[1].Name != "sick" || records[2].Name != "calm" {
		t.Fatalf("expected drifted, sick, calm; got %s, %s, %s", records[0].Name, records[1].Name, records[2].Name)
	}
}

func TestGitopsResourcesAreCappedAfterTheDriftedSortFirst(t *testing.T) {
	record := gitopsRecord{}
	for i := 0; i < maxGitopsResources+10; i++ {
		record.Resources = append(record.Resources, gitopsResource{Kind: "ConfigMap", Name: itoa(uint(i)), Sync: syncSynced})
	}
	record.Resources = append(record.Resources, gitopsResource{Kind: "Deployment", Name: "web", Sync: syncOutOfSync})
	record.finishResources()

	if len(record.Resources) != maxGitopsResources || !record.ResourcesTruncated {
		t.Fatalf("expected %d entries and truncation, got %d / %v",
			maxGitopsResources, len(record.Resources), record.ResourcesTruncated)
	}
	if record.Resources[0].Name != "web" {
		t.Fatalf("expected the drifted entry kept at the top, got %+v", record.Resources[0])
	}
}

/* ------------------------------------------------------------ one object --- */

func trackedObject(annotations, labels map[string]string) gitopsObjectMeta {
	var object gitopsObjectMeta
	object.Kind, object.APIVersion = "Deployment", "apps/v1"
	object.Metadata.Name, object.Metadata.Namespace = "web", "shop"
	object.Metadata.Annotations, object.Metadata.Labels = annotations, labels
	return object
}

func TestGitopsObjectRecordComesFromTheObjectsOwnTracking(t *testing.T) {
	cluster := newFakeCluster()
	cluster.object(argoApps[:len(argoApps)-len("/applications")]+"/namespaces/argocd/applications/shop",
		argoApp("argocd", "shop", "OutOfSync", "Healthy",
			argoEntry("apps", "Deployment", "shop", "web", "OutOfSync", "Healthy")))

	object := trackedObject(map[string]string{argoTrackingAnnotation: "shop:apps/Deployment:shop/web"}, nil)
	view, err := resolveGitopsObject(cluster, object, adminGrant)
	if err != nil {
		t.Fatal(err)
	}
	if !view.Tracked || view.Record == nil || view.Record.Name != "shop" {
		t.Fatalf("expected the shop application, got %+v", view)
	}
	if view.Entry == nil || view.Entry.Sync != syncOutOfSync {
		t.Fatalf("expected this object's own entry, got %+v", view.Entry)
	}
	if len(cluster.requested) != 1 {
		t.Fatalf("expected one direct read of the named application, got %v", cluster.requested)
	}
}

// Argo CD's default namespace is a guess; when it misses, every Application is
// listed and the one listing this object wins over a namesake elsewhere.
func TestGitopsObjectFallsBackToTheApplicationThatListsIt(t *testing.T) {
	cluster := newFakeCluster()
	cluster.list(argoApps, "",
		argoApp("team-x", "shop", "Synced", "Healthy"),
		argoApp("gitops", "shop", "OutOfSync", "Healthy",
			argoEntry("apps", "Deployment", "shop", "web", "OutOfSync", "Healthy")),
	)

	object := trackedObject(nil, map[string]string{argoInstanceLabel: "shop"})
	view, err := resolveGitopsObject(cluster, object, adminGrant)
	if err != nil {
		t.Fatal(err)
	}
	if view.Record == nil || view.Record.Namespace != "gitops" {
		t.Fatalf("expected the application that lists the object, got %+v", view.Record)
	}
}

func TestGitopsObjectNotFoundHereSaysWhereItMayBe(t *testing.T) {
	object := trackedObject(nil, map[string]string{argoInstanceLabel: "shop"})
	view, err := resolveGitopsObject(newFakeCluster(), object, adminGrant)
	if err != nil {
		t.Fatal(err)
	}
	if !view.Tracked || view.Record != nil || !strings.Contains(view.Reason, "another cluster") {
		t.Fatalf("expected a tracked object whose application is elsewhere, got %+v", view)
	}
}

func TestGitopsObjectUntrackedReadsNothing(t *testing.T) {
	cluster := newFakeCluster()
	view, err := resolveGitopsObject(cluster, trackedObject(nil, nil), adminGrant)
	if err != nil {
		t.Fatal(err)
	}
	if view.Tracked || len(cluster.requested) != 0 {
		t.Fatalf("expected untracked with no read, got %+v after %v", view, cluster.requested)
	}
}

// A tracking value is the object's metadata, and anybody who can edit the
// object writes it. It becomes a path only once it is a valid name.
func TestGitopsObjectRefusesATrackingValueThatIsNotAName(t *testing.T) {
	cluster := newFakeCluster()
	object := trackedObject(map[string]string{argoTrackingAnnotation: "../../secrets:x/y:z/w"}, nil)
	view, err := resolveGitopsObject(cluster, object, adminGrant)
	if err != nil {
		t.Fatal(err)
	}
	if view.Record != nil || len(cluster.requested) != 0 {
		t.Fatalf("expected no read for an invalid name, got %+v after %v", view, cluster.requested)
	}
}

func TestGitopsObjectFluxKustomizationIsReadByItsLabels(t *testing.T) {
	cluster := newFakeCluster()
	cluster.object("/apis/kustomize.toolkit.fluxcd.io/v1/namespaces/flux-system/kustomizations/apps",
		fluxKsFixture("flux-system", "apps", "True", "shop_web_apps_Deployment", "team-b_api_apps_Deployment"))

	object := trackedObject(nil, map[string]string{
		fluxKustomizeName: "apps", fluxKustomizeNamespace: "flux-system",
	})
	view, err := resolveGitopsObject(cluster, object, viewOn("shop"))
	if err != nil {
		t.Fatal(err)
	}
	if view.Entry == nil || view.Entry.Sync != syncUnknown {
		t.Fatalf("expected this object's entry, unknown, got %+v", view.Entry)
	}
	if len(view.Record.Resources) != 1 || view.Record.ResourcesHidden != 1 {
		t.Fatalf("expected the record narrowed to the caller's namespace, got %+v", view.Record)
	}
}

/* ---------------------------------------------------------------- routes --- */

func TestGitopsRoutesRefuseDirectClustersAndRequireAuth(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")
	direct := env.store.addCluster("legacy", "dev")
	token := env.tokenFor(t, admin)

	for _, path := range []string{"/gitops/applications", "/gitops/object?kind=deployments&namespace=shop&name=web"} {
		rec := env.do(t, http.MethodGet, "/api/v1/clusters/"+itoa(direct.ID)+path, token, nil)
		if rec.Code != http.StatusConflict {
			t.Fatalf("%s: expected %d for a direct-mode cluster, got %d (%s)",
				path, http.StatusConflict, rec.Code, rec.Body.String())
		}
		rec = env.do(t, http.MethodGet, "/api/v1/clusters/"+itoa(direct.ID)+path, "", nil)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: expected %d without a token, got %d", path, http.StatusUnauthorized, rec.Code)
		}
	}
}

func TestGitopsObjectRefusesANamespaceOutsideTheGrant(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("scoped", "secret123", "user")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	env.store.grant(user.ID, cluster.ID, "view", []string{"team-a"})
	token := env.tokenFor(t, user)

	rec := env.do(t, http.MethodGet, "/api/v1/clusters/"+itoa(cluster.ID)+
		"/gitops/object?kind=deployments&namespace=team-b&name=web", token, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected %d, got %d (%s)", http.StatusForbidden, rec.Code, rec.Body.String())
	}
}

// The records are read as KubeMG, which can list Secrets; a view grant cannot.
// A Secret's name must not leave through a read the caller could not make.
func TestGitopsNarrowingHidesSecretEntriesFromAViewGrant(t *testing.T) {
	record := gitopsRecord{
		Tool: gitopsArgoCD, Namespace: "argocd", Name: "shop",
		Resources: []gitopsResource{
			{Kind: "Secret", Namespace: "team-a", Name: "db-password"},
			{Kind: "Deployment", Group: "apps", Namespace: "team-a", Name: "web"},
			{Kind: "Secret", Group: "example.com", Namespace: "team-a", Name: "not-core"},
		},
	}

	for name, grant := range map[string]db.UserClusterAccess{
		"scoped view":   viewOn("team-a"),
		"unscoped view": {K8sRole: db.K8sRoleView},
	} {
		t.Run(name, func(t *testing.T) {
			got, visible := narrowGitopsRecord(record, grant)
			if !visible || len(got.Resources) != 2 || got.ResourcesHidden != 1 {
				t.Fatalf("expected the core Secret cut and counted, got %+v", got)
			}
			for _, resource := range got.Resources {
				if resource.Name == "db-password" {
					t.Fatalf("a view grant saw a Secret entry: %+v", got.Resources)
				}
			}
		})
	}

	edit := db.UserClusterAccess{K8sRole: db.K8sRoleEdit, Namespaces: db.JoinNamespaces([]string{"team-a"})}
	if got, _ := narrowGitopsRecord(record, edit); len(got.Resources) != 3 {
		t.Fatalf("expected an edit grant, which reads Secrets, to see all three, got %+v", got.Resources)
	}
}

// A record outside the grant reads exactly as a record that is not here, so the
// per-object answer is no oracle for what KubeMG can see and the caller cannot.
func TestGitopsObjectOutsideTheGrantReadsAsNotHere(t *testing.T) {
	present := newFakeCluster()
	present.object("/apis/helm.toolkit.fluxcd.io/v2/namespaces/flux-system/helmreleases/redis",
		fluxHelmFixture("flux-system", "redis"))
	absent := newFakeCluster()

	object := trackedObject(nil, map[string]string{fluxHelmName: "redis", fluxHelmNamespace: "flux-system"})
	hidden, err := resolveGitopsObject(present, object, viewOn("shop"))
	if err != nil {
		t.Fatal(err)
	}
	missing, err := resolveGitopsObject(absent, object, viewOn("shop"))
	if err != nil {
		t.Fatal(err)
	}
	if hidden.Record != nil || hidden.Reason != missing.Reason {
		t.Fatalf("expected the same answer for hidden and absent, got %q vs %q", hidden.Reason, missing.Reason)
	}
}

func TestGitopsServiceReadRefusalIsNotHandedOn(t *testing.T) {
	cluster := newFakeCluster()
	cluster.refuse("/apis/kustomize.toolkit.fluxcd.io/v1/namespaces/flux-system/kustomizations/apps", http.StatusForbidden)
	object := trackedObject(nil, map[string]string{fluxKustomizeName: "apps", fluxKustomizeNamespace: "flux-system"})

	view, err := resolveGitopsObject(cluster, object, viewOn("shop"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(view.Reason, "forbidden") || strings.Contains(view.Reason, "kubemg:") {
		t.Fatalf("expected KubeMG's own refusal kept out of the answer, got %q", view.Reason)
	}
}

func TestRedactRepoURLFailsClosed(t *testing.T) {
	for raw, want := range map[string]string{
		"https://deploy:tok@git.example.com/shop.git":          "https://git.example.com/shop.git",
		"https://git.example.com/shop.git?private_token=tok#x": "https://git.example.com/shop.git",
		"git@github.com:org/shop.git":                          "git@github.com:org/shop.git",
		"https://user:p%zz@git.example.com/shop.git":           "git.example.com/shop.git",
		"registry-1.docker.io/bitnamicharts":                   "registry-1.docker.io/bitnamicharts",
		"https://charts.bitnami.com/bitnami":                   "https://charts.bitnami.com/bitnami",
		"https://u:tok@host.example/repo?x=1 bad\x7f":          "",
	} {
		if got := redactRepoURL(raw); got != want {
			t.Errorf("redactRepoURL(%q) = %q, want %q", raw, got, want)
		}
	}
}
