package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/db"
	"github.com/kubemg/kubemg/backend/pkg/observability"
)

/*
 * GitOps drift alarms, the attribution beside them, and the deep link into
 * Argo CD. What is pinned is what goes wrong quietly: a page for drift that was
 * already there, a read nobody asked for, two replicas polling, a writer named
 * to somebody the trail would not show, and a link to a record the caller
 * cannot see.
 */

func argoRead(resources ...gitopsResource) gitopsRead {
	return gitopsRead{
		tools: []gitopsToolStatus{{Tool: gitopsArgoCD, Installed: true, Available: true}},
		records: []gitopsRecord{{
			Tool: gitopsArgoCD, Namespace: "argocd", Name: "shop", Sync: syncSynced,
			Resources: resources,
		}},
	}
}

func deployment(name, sync string) gitopsResource {
	return gitopsResource{Group: "apps", Kind: "Deployment", Namespace: "shop", Name: name, Sync: sync,
		version: "v1"}
}

func driftNames(drifts []gitopsDrift) []string {
	out := []string{}
	for _, drift := range drifts {
		if drift.resource != nil {
			out = append(out, drift.resource.Name)
		} else {
			out = append(out, drift.record.Name)
		}
	}
	return out
}

/* ----------------------------------------------------------- transitions --- */

func TestGitopsTransitionsFireOnlyOnSyncedToOutOfSync(t *testing.T) {
	first, drifts := gitopsTransitions(nil, argoRead(
		deployment("web", syncSynced), deployment("api", syncOutOfSync), deployment("cron", syncUnknown)))
	if len(drifts) != 0 {
		t.Fatalf("a first pass is a watermark, got %v", driftNames(drifts))
	}

	_, drifts = gitopsTransitions(first, argoRead(
		deployment("web", syncOutOfSync),  // synced → out: fires
		deployment("api", syncOutOfSync),  // already out: does not
		deployment("cron", syncOutOfSync), // unknown → out: not a transition from synced
		deployment("new", syncOutOfSync),  // never seen: does not
	))
	if got := strings.Join(driftNames(drifts), ","); got != "web" {
		t.Fatalf("expected only web to fire, got %q", got)
	}
}

func TestGitopsTransitionsFireOnceNotEveryPass(t *testing.T) {
	marks, _ := gitopsTransitions(nil, argoRead(deployment("web", syncSynced)))
	marks, drifts := gitopsTransitions(marks, argoRead(deployment("web", syncOutOfSync)))
	if len(drifts) != 1 {
		t.Fatalf("expected the transition to fire, got %v", driftNames(drifts))
	}
	_, drifts = gitopsTransitions(marks, argoRead(deployment("web", syncOutOfSync)))
	if len(drifts) != 0 {
		t.Fatalf("drift that is still there is not a new transition, got %v", driftNames(drifts))
	}
}

func TestGitopsTransitionsFluxRecordReadyToFailed(t *testing.T) {
	read := func(sync string) gitopsRead {
		return gitopsRead{
			tools: []gitopsToolStatus{{Tool: gitopsFluxKustomize, Installed: true, Available: true}},
			records: []gitopsRecord{{Tool: gitopsFluxKustomize, Namespace: "flux-system", Name: "apps",
				Sync: sync, Resources: []gitopsResource{{Kind: "Deployment", Namespace: "shop", Name: "web",
					Sync: syncUnknown}}}},
		}
	}
	marks, _ := gitopsTransitions(nil, read(syncSynced))
	_, drifts := gitopsTransitions(marks, read(syncOutOfSync))
	if len(drifts) != 1 || drifts[0].resource != nil || drifts[0].record.Name != "apps" {
		t.Fatalf("expected the Kustomization itself to fire, got %+v", drifts)
	}
}

func TestGitopsTransitionsKeepAToolsMarksWhileItsListIsRefused(t *testing.T) {
	marks, _ := gitopsTransitions(nil, argoRead(deployment("web", syncSynced)))

	refused := gitopsRead{tools: []gitopsToolStatus{{Tool: gitopsArgoCD, Installed: true}}}
	marks, drifts := gitopsTransitions(marks, refused)
	if len(drifts) != 0 || len(marks) != 1 {
		t.Fatalf("a refused list must keep the marks, got %d marks, %v", len(marks), driftNames(drifts))
	}

	_, drifts = gitopsTransitions(marks, argoRead(deployment("web", syncOutOfSync)))
	if len(drifts) != 1 {
		t.Fatalf("drift across a refused pass must still fire, got %v", driftNames(drifts))
	}
}

/* ------------------------------------------------------------------ poll --- */

type driftProbe struct {
	reads    int
	signals  []observability.Signal
	rules    []db.AlarmRule
	clusters []db.Cluster
}

func (p *driftProbe) watch(trail auditTrailReader, cluster *fakeCluster) *gitopsDriftWatch {
	return &gitopsDriftWatch{
		rules:    func() []db.AlarmRule { return p.rules },
		observe:  func(signal observability.Signal) { p.signals = append(p.signals, signal) },
		clusters: func(context.Context) ([]db.Cluster, error) { return p.clusters, nil },
		reader: func(context.Context, db.Cluster) (trafficReader, bool) {
			p.reads++
			return cluster, true
		},
		trail: trail,
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		now:   func() time.Time { return time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC) },
		marks: map[uint]map[string]string{},
	}
}

func driftCluster(sync string) *fakeCluster {
	cluster := newFakeCluster()
	app := argoApp("argocd", "shop", "OutOfSync", "Healthy",
		map[string]any{"group": "apps", "version": "v1", "kind": "Deployment", "namespace": "shop",
			"name": "web", "status": sync, "health": map[string]any{"status": "Healthy"}})
	app["status"].(map[string]any)["operationState"] = map[string]any{
		"phase": "Succeeded", "finishedAt": "2026-10-24T10:00:00Z",
	}
	cluster.list(argoApps, "", app)
	cluster.object("/apis/apps/v1", map[string]any{"resources": []any{
		map[string]any{"name": "deployments", "kind": "Deployment"},
		map[string]any{"name": "deployments/scale", "kind": "Scale"},
	}})
	return cluster
}

func TestGitopsPollReadsNothingWithoutAnEnabledRule(t *testing.T) {
	probe := &driftProbe{clusters: []db.Cluster{{ID: 1, Name: "prod"}}}
	cluster := driftCluster("Synced")
	watch := probe.watch(nil, cluster)

	watch.poll(context.Background())

	if probe.reads != 0 || len(cluster.requested) != 0 {
		t.Fatalf("no rule must mean no read, got %d reader calls and %v", probe.reads, cluster.requested)
	}
}

func TestGitopsPollSkipsAClusterNoRuleCovers(t *testing.T) {
	probe := &driftProbe{
		clusters: []db.Cluster{{ID: 1, Name: "prod"}},
		rules:    []db.AlarmRule{{Trigger: db.TriggerGitOpsDrift, ClusterID: 2, Enabled: true}},
	}
	cluster := driftCluster("Synced")
	probe.watch(nil, cluster).poll(context.Background())
	if len(cluster.requested) != 0 {
		t.Fatalf("a rule for another cluster must not read this one, got %v", cluster.requested)
	}
}

func TestGitopsPollFirstPassIsAWatermarkThenTheTransitionFires(t *testing.T) {
	store := newFakeStore()
	store.audit = append(store.audit,
		db.AuditEvent{ID: 1, At: time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC), UserID: 7, Username: "early",
			ClusterID: 1, Verb: "patch", Status: 200, Namespace: "shop", Resource: "deployments", Name: "web"},
		db.AuditEvent{ID: 2, At: time.Date(2026, 10, 24, 11, 0, 0, 0, time.UTC), UserID: 8, Username: "alice",
			ClusterID: 1, Verb: "patch", Status: 200, Namespace: "shop", Resource: "deployments", Name: "web"},
		db.AuditEvent{ID: 3, At: time.Date(2026, 10, 24, 11, 30, 0, 0, time.UTC), UserID: 9, Username: "mallory",
			ClusterID: 1, Verb: "patch", Status: 403, Namespace: "shop", Resource: "deployments", Name: "web"},
	)
	probe := &driftProbe{
		clusters: []db.Cluster{{ID: 1, Name: "prod"}},
		rules:    []db.AlarmRule{{Trigger: db.TriggerGitOpsDrift, Enabled: true}},
	}
	watch := probe.watch(store, driftCluster("Synced"))
	watch.poll(context.Background())
	if len(probe.signals) != 0 {
		t.Fatalf("the first pass must deliver nothing, got %+v", probe.signals)
	}

	drifted := driftCluster("OutOfSync")
	watch.reader = func(context.Context, db.Cluster) (trafficReader, bool) { return drifted, true }
	watch.poll(context.Background())

	if len(probe.signals) != 1 {
		t.Fatalf("expected one alarm, got %+v", probe.signals)
	}
	signal := probe.signals[0]
	if signal.Source != observability.SourceGitOpsDrift || signal.Tool != gitopsArgoCD ||
		signal.Application != "shop" || signal.Namespace != "shop" || signal.Object != "deployment/web" {
		t.Fatalf("unexpected signal %+v", signal)
	}
	// The newest accepted write since the last sync: not the refused one, not
	// the one before the sync.
	if signal.Username != "alice" || !strings.Contains(signal.Message, "patch by alice") {
		t.Fatalf("expected the write to be attributed to alice, got %q / %q", signal.Username, signal.Message)
	}
	if signal.Fingerprint != "gitops/prod/argocd/argocd/shop/shop/deployment/web" {
		t.Fatalf("fingerprint = %q", signal.Fingerprint)
	}
}

func TestGitopsDriftSignalSaysWhenNothingWentThroughKubeMG(t *testing.T) {
	resource := deployment("web", syncOutOfSync)
	signal := gitopsDriftSignal(db.Cluster{ID: 1, Name: "prod"},
		gitopsDrift{record: gitopsRecord{Tool: gitopsArgoCD, Namespace: "argocd", Name: "shop"}, resource: &resource},
		nil, time.Now())
	if signal.Username != "" || !strings.Contains(signal.Message, "outside KubeMG leaves no record here") {
		t.Fatalf("expected the outside-KubeMG wording, got %q / %q", signal.Username, signal.Message)
	}
}

func TestGitopsTickDoesNotPollWithoutTheLease(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*fakeStore)
	}{
		{"held elsewhere", func(store *fakeStore) { store.leases[db.LeaseGitOpsWatcher] = "another-replica" }},
		{"unreadable", func(store *fakeStore) { store.leaseErr = errors.New("database is down") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			tc.setup(store)
			probe := &driftProbe{
				clusters: []db.Cluster{{ID: 1, Name: "prod"}},
				rules:    []db.AlarmRule{{Trigger: db.TriggerGitOpsDrift, Enabled: true}},
			}
			cluster := driftCluster("Synced")
			s := watchServer(store)
			s.gitopsTick(context.Background(), probe.watch(nil, cluster))
			if store.leaseCalls != 1 || len(cluster.requested) != 0 {
				t.Fatalf("expected the lease consulted and nothing read, got %d calls, %v",
					store.leaseCalls, cluster.requested)
			}
		})
	}
}

func TestGitopsTickPollsWithTheLease(t *testing.T) {
	store := newFakeStore()
	probe := &driftProbe{
		clusters: []db.Cluster{{ID: 1, Name: "prod"}},
		rules:    []db.AlarmRule{{Trigger: db.TriggerGitOpsDrift, Enabled: true}},
	}
	cluster := driftCluster("Synced")
	watchServer(store).gitopsTick(context.Background(), probe.watch(nil, cluster))
	if store.leases[db.LeaseGitOpsWatcher] != "replica-under-test" || len(cluster.requested) == 0 {
		t.Fatalf("expected the lease taken and the records read, got %v", cluster.requested)
	}
}

func TestPluralResolverAsksDiscoveryOnce(t *testing.T) {
	cluster := driftCluster("Synced")
	plurals := newPluralResolver(cluster)
	if got := plurals.plural("apps", "v1", "Deployment"); got != "deployments" {
		t.Fatalf("plural = %q", got)
	}
	if got := plurals.plural("apps", "v1", "Scale"); got != "" {
		t.Fatalf("a subresource must not answer for a kind, got %q", got)
	}
	if len(cluster.requested) != 1 {
		t.Fatalf("expected one discovery read, got %v", cluster.requested)
	}
}

/* -------------------------------------------------------------- matchers --- */

func TestGitopsDriftRuleMatchers(t *testing.T) {
	signal := observability.Signal{Source: observability.SourceGitOpsDrift, ClusterID: 1, Namespace: "shop",
		Tool: gitopsArgoCD, Application: "shop"}
	cases := []struct {
		name string
		rule db.AlarmRule
		want bool
	}{
		{"any", db.AlarmRule{Trigger: db.TriggerGitOpsDrift}, true},
		{"cluster", db.AlarmRule{Trigger: db.TriggerGitOpsDrift, ClusterID: 2}, false},
		{"namespace", db.AlarmRule{Trigger: db.TriggerGitOpsDrift, Namespaces: "payments"}, false},
		{"tool", db.AlarmRule{Trigger: db.TriggerGitOpsDrift, GitOpsTools: "flux-kustomization"}, false},
		{"tool listed", db.AlarmRule{Trigger: db.TriggerGitOpsDrift, GitOpsTools: "flux-kustomization,argocd"}, true},
		{"application", db.AlarmRule{Trigger: db.TriggerGitOpsDrift, GitOpsApps: "billing"}, false},
		{"application listed", db.AlarmRule{Trigger: db.TriggerGitOpsDrift, GitOpsApps: "shop"}, true},
		{"another trigger", db.AlarmRule{Trigger: db.TriggerClusterEvent, EventType: "Warning"}, false},
	}
	for _, tc := range cases {
		if got := observability.Matches(tc.rule, signal); got != tc.want {
			t.Errorf("%s: Matches = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestAlarmRuleGitopsMatchersAreValidated(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	channel := env.store.addAlarmChannel(db.AlarmChannel{
		Name: "am", Kind: db.ChannelAlertmanager, URL: "https://am.example.com/x", Enabled: true,
	})
	token := env.tokenFor(t, admin)

	rec := env.do(t, http.MethodPost, "/api/v1/alarms/rules", token, map[string]any{
		"name": "drift", "trigger": db.TriggerGitOpsDrift, "channel_id": channel.ID,
		"gitops_tools": []string{"jenkins"},
	})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "jenkins") {
		t.Fatalf("an unknown tool must be refused by name, got %d %s", rec.Code, rec.Body.String())
	}

	rec = env.do(t, http.MethodPost, "/api/v1/alarms/rules", token, map[string]any{
		"name": "drift", "trigger": db.TriggerGitOpsDrift, "channel_id": channel.ID,
		"gitops_tools": []string{"argocd"}, "gitops_apps": []string{"shop"},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected %d, got %d (%s)", http.StatusCreated, rec.Code, rec.Body.String())
	}

	// On another trigger the drift matchers are dropped rather than stored as
	// a narrowing that does nothing.
	rec = env.do(t, http.MethodPost, "/api/v1/alarms/rules", token, map[string]any{
		"name": "audit", "trigger": db.TriggerAudit, "channel_id": channel.ID,
		"gitops_tools": []string{"argocd"},
	})
	if rec.Code != http.StatusCreated || strings.Contains(rec.Body.String(), "gitops_tools") {
		t.Fatalf("expected the drift matchers dropped, got %d %s", rec.Code, rec.Body.String())
	}
}

/* ----------------------------------------------------------- attribution --- */

func TestGitopsAttributionFollowsTheTrailsReadRule(t *testing.T) {
	store := newFakeStore()
	since := time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
	store.audit = append(store.audit, db.AuditEvent{
		ID: 1, At: since.Add(time.Hour), UserID: 8, Username: "alice", ClusterID: 1, Verb: "update",
		Status: 200, Namespace: "shop", Resource: "deployments", Name: "web",
	})
	admin := &db.User{ID: 1, Username: "admin", Role: db.RoleAdmin, SystemRole: db.SystemRoleAdmin}
	bob := &db.User{ID: 2, Username: "bob", Role: db.RoleUser, SystemRole: db.SystemRoleUser}
	alice := &db.User{ID: 8, Username: "alice", Role: db.RoleUser, SystemRole: db.SystemRoleUser}

	got := attributeFor(context.Background(), store, admin, 1, "shop", "deployments", "web", &since)
	if got.Change == nil || got.Change.Username != "alice" || got.ScopedToSelf {
		t.Fatalf("an administrator reads anyone's write, got %+v", got)
	}

	got = attributeFor(context.Background(), store, bob, 1, "shop", "deployments", "web", &since)
	if got.Change != nil || !got.ScopedToSelf ||
		!strings.Contains(got.Note, "shown to an administrator only") ||
		!strings.Contains(got.Note, "outside KubeMG") {
		t.Fatalf("a non-admin must not learn who else wrote, got %+v", got)
	}

	got = attributeFor(context.Background(), store, alice, 1, "shop", "deployments", "web", &since)
	if got.Change == nil || got.Change.Username != "alice" {
		t.Fatalf("a non-admin reads their own write, got %+v", got)
	}

	later := since.Add(2 * time.Hour)
	got = attributeFor(context.Background(), store, admin, 1, "shop", "deployments", "web", &later)
	if got.Change != nil || !strings.Contains(got.Note, "No write to this object went through KubeMG since") {
		t.Fatalf("a write before the last sync did not drift it, got %+v", got)
	}
}

/* ------------------------------------------------------------- deep link --- */

func TestGitopsConsoleLinkIsBuiltAfterNarrowing(t *testing.T) {
	records := []gitopsRecord{
		{Tool: gitopsArgoCD, Namespace: "argocd", Name: "shop", Resources: []gitopsResource{
			{Kind: "Deployment", Namespace: "shop", Name: "web"}}},
		{Tool: gitopsArgoCD, Namespace: "argocd", Name: "billing", Resources: []gitopsResource{
			{Kind: "Deployment", Namespace: "billing", Name: "api"}}},
		{Tool: gitopsFluxKustomize, Namespace: "flux-system", Name: "apps", Resources: []gitopsResource{
			{Kind: "Deployment", Namespace: "shop", Name: "web"}}},
	}

	scoped, _ := narrowGitopsList(records, viewOn("shop"), "https://argo.example.com/")
	if len(scoped) != 2 {
		t.Fatalf("expected two records visible, got %d", len(scoped))
	}
	for _, record := range scoped {
		switch record.Name {
		case "shop":
			if record.ConsoleURL != "https://argo.example.com/applications/argocd/shop" {
				t.Fatalf("console link = %q", record.ConsoleURL)
			}
		case "apps":
			if record.ConsoleURL != "" {
				t.Fatalf("Flux has no console, got %q", record.ConsoleURL)
			}
		default:
			t.Fatalf("a record narrowing hid came back: %s", record.Name)
		}
	}

	unlinked, _ := narrowGitopsList(records, adminGrant, "")
	for _, record := range unlinked {
		if record.ConsoleURL != "" {
			t.Fatalf("no console row must mean no link, got %q", record.ConsoleURL)
		}
	}
}

func TestArgoConsoleBaseIsTheConsoleRowNeverTheDatasource(t *testing.T) {
	store := newFakeStore()
	s := watchServer(store)
	store.sources[1] = map[string]db.ObservabilitySource{
		db.SourceGitOps: {ClusterID: 1, Kind: db.SourceGitOps, Provider: db.ProviderArgoCD,
			URL: "http://argocd-server.argocd.svc"},
	}
	if got := s.argoConsoleBase(context.Background(), 1); got != "" {
		t.Fatalf("the datasource URL must never be the console, got %q", got)
	}

	store.consoles[1] = map[string]db.ClusterConsole{
		db.ConsoleArgoCD: {ClusterID: 1, Kind: db.ConsoleArgoCD, URL: "https://argo.example.com"},
	}
	if got := s.argoConsoleBase(context.Background(), 1); got != "https://argo.example.com" {
		t.Fatalf("console base = %q", got)
	}
}
