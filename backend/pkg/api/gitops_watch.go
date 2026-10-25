package api

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/db"
	"github.com/kubemg/kubemg/backend/pkg/observability"
)

/*
 * Alarming on GitOps drift.
 *
 * The page answers "what has drifted" for whoever opens it; this is for the
 * drift nobody is looking at. It reads the same records the page does — Argo CD's
 * per-resource sync status, Flux's Ready condition — as the same service
 * identity, and fires when one of them *changes*: an Argo CD entry going from
 * Synced to OutOfSync, a Flux Kustomization or HelmRelease going from ready to
 * not. KubeMG still decides nothing about drift itself; the controller did.
 *
 * It is the cluster-event watcher's shape, for the same reasons:
 *
 *   - Nothing is read unless an enabled gitops_drift rule exists, and only the
 *     clusters a rule covers are read.
 *   - The first pass on a cluster records where everything stands and delivers
 *     nothing. Drift that was already there when a rule was switched on is on
 *     the page; arriving as a burst of pages would get the channel muted.
 *   - Exactly one replica polls, behind db.LeaseGitOpsWatcher. A store error on
 *     the lease is not permission to poll.
 *
 * A helm-controller drift *event* in `warn` mode does not change the release's
 * Ready condition, so it is not seen here: it is a Kubernetes Event (reason
 * DriftDetected) and a cluster_event rule matches it.
 */

const (
	// gitopsPollInterval is how often each covered cluster's records are read.
	// Argo CD itself re-compares every three minutes by default; a minute
	// catches its answer without reading the same one many times over.
	gitopsPollInterval = time.Minute
	// gitopsLeaseTTL is three intervals, for alarmLeaseTTL's reason.
	gitopsLeaseTTL = 3 * gitopsPollInterval
)

// startGitopsWatcher polls for drift for as long as the context lives.
func (s *server) startGitopsWatcher(ctx context.Context) {
	ticker := time.NewTicker(gitopsPollInterval)
	defer ticker.Stop()

	watch := s.gitopsDriftWatch()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.gitopsTick(ctx, watch)
		}
	}
}

// gitopsTick is one pass: take the lease, and poll only if this replica got it.
func (s *server) gitopsTick(ctx context.Context, watch *gitopsDriftWatch) {
	held, err := s.store.AcquireLease(ctx, db.LeaseGitOpsWatcher, s.instanceID, gitopsLeaseTTL)
	if err != nil {
		if ctx.Err() == nil {
			s.log().Warn("gitops drift watcher could not take its lease",
				slog.String("error", err.Error()))
		}
		return
	}
	if !held {
		return
	}
	watch.poll(ctx)
}

// gitopsDriftWatch is the poll, with what it reaches as values so a test can
// count the reads it makes and see the signals it offers.
type gitopsDriftWatch struct {
	rules    func() []db.AlarmRule
	observe  func(observability.Signal)
	clusters func(ctx context.Context) ([]db.Cluster, error)
	// reader returns the service reader for a cluster, or false when the
	// cluster cannot be read now (no agent, or not agent mode).
	reader func(ctx context.Context, cluster db.Cluster) (trafficReader, bool)
	trail  auditTrailReader
	log    *slog.Logger
	now    func() time.Time

	// marks holds, per cluster, the last state of every key a pass saw. Only
	// the lease holder's single goroutine touches it. In memory on purpose: a
	// restart re-establishes it and delivers nothing.
	marks map[uint]map[string]string
}

func (s *server) gitopsDriftWatch() *gitopsDriftWatch {
	return &gitopsDriftWatch{
		rules:    func() []db.AlarmRule { return s.alarms.Rules(db.TriggerGitOpsDrift) },
		observe:  s.alarms.Observe,
		clusters: s.store.Clusters,
		reader: func(ctx context.Context, cluster db.Cluster) (trafficReader, bool) {
			if connectionMode(cluster) != db.ModeAgent || s.tunnels == nil || !s.tunnels.Connected(cluster.ID) {
				return nil, false
			}
			return gitopsServiceReader{s: s, ctx: ctx, cluster: &cluster}, true
		},
		trail: s.store,
		log:   s.log(),
		now:   func() time.Time { return time.Now().UTC() },
		marks: map[uint]map[string]string{},
	}
}

// poll reads one round across the clusters any rule covers.
func (w *gitopsDriftWatch) poll(ctx context.Context) {
	rules := w.rules()
	if len(rules) == 0 {
		// Forgetting everything means a rule switched on later starts from a
		// fresh watermark, not from what was true when the last one went away.
		clear(w.marks)
		return
	}
	clusters, err := w.clusters(ctx)
	if err != nil {
		if ctx.Err() == nil {
			w.log.Warn("gitops drift watcher could not list clusters", slog.String("error", err.Error()))
		}
		return
	}
	for _, cluster := range clusters {
		if ctx.Err() != nil {
			return
		}
		if !watched(rules, cluster.ID) {
			delete(w.marks, cluster.ID)
			continue
		}
		read, ok := w.reader(ctx, cluster)
		if !ok {
			delete(w.marks, cluster.ID)
			continue
		}
		w.pollCluster(ctx, cluster, read)
	}
}

func (w *gitopsDriftWatch) pollCluster(ctx context.Context, cluster db.Cluster, read trafficReader) {
	state, err := readGitopsRecords(read)
	if err != nil {
		// A dropped tunnel mid-pass: keep the marks and try again next tick.
		w.log.Debug("gitops drift watcher could not read records",
			slog.String("cluster", cluster.Name), slog.String("error", err.Error()))
		return
	}

	prev, known := w.marks[cluster.ID]
	next, drifts := gitopsTransitions(prev, state)
	w.marks[cluster.ID] = next
	if !known {
		w.log.Info("gitops drift watcher is now watching",
			slog.String("cluster", cluster.Name), slog.Int("entries", len(next)))
		return
	}

	plurals := newPluralResolver(read)
	at := w.now()
	for _, drift := range drifts {
		var change *gitopsChange
		if drift.resource != nil && drift.record.Remote == "" && w.trail != nil {
			plural := plurals.plural(drift.resource.Group, drift.resource.version, drift.resource.Kind)
			// Every write's: the channel is an administrator's, and the trail's
			// read rule gives an administrator everyone's.
			change, _ = lastKubemgWrite(ctx, w.trail, cluster.ID, drift.resource.Namespace, plural,
				drift.resource.Name, drift.record.lastSyncAt, 0)
		}
		w.observe(gitopsDriftSignal(cluster, drift, change, at))
	}
}

// gitopsDrift is one transition into drift.
type gitopsDrift struct {
	record gitopsRecord
	// resource is the Argo CD entry that went out of sync; nil for a Flux
	// record, whose state is the record's own.
	resource *gitopsResource
}

/*
 * gitopsTransitions compares a pass with the last one and names what went out
 * of sync between them. It is the whole behaviour of the feature, separated
 * from reading and delivering so it can be asserted on its own.
 *
 *   - An Argo CD entry is keyed per object, and fires Synced → OutOfSync.
 *   - A Flux record is keyed on itself, and fires synced → out of sync, which
 *     is its Ready condition turning False (or a revision attempted but not
 *     applied).
 *   - A key not seen last pass never fires, whatever its state: something new
 *     that arrives drifted is on the page, and is not a transition anybody
 *     caused here.
 *   - A tool whose list could not be read this pass keeps its previous keys,
 *     so a refused list does not read as "everything is new" when it comes back.
 */
func gitopsTransitions(prev map[string]string, state gitopsRead) (map[string]string, []gitopsDrift) {
	next := map[string]string{}
	read := map[string]bool{}
	for _, tool := range state.tools {
		read[tool.Tool] = tool.Available
	}
	for key, sync := range prev {
		tool, _, _ := strings.Cut(key, "|")
		if !read[tool] {
			next[key] = sync
		}
	}

	drifts := []gitopsDrift{}
	fired := func(key, sync string) bool {
		before, seen := prev[key]
		next[key] = sync
		return seen && before == syncSynced && sync == syncOutOfSync
	}
	for _, record := range state.records {
		if record.Tool != gitopsArgoCD {
			if fired(gitopsMarkKey(record, nil), record.Sync) {
				drifts = append(drifts, gitopsDrift{record: record})
			}
			continue
		}
		for i := range record.Resources {
			resource := record.Resources[i]
			if fired(gitopsMarkKey(record, &resource), resource.Sync) {
				drifts = append(drifts, gitopsDrift{record: record, resource: &resource})
			}
		}
	}
	return next, drifts
}

// gitopsMarkKey names one watched thing. The tool leads, so a key can be
// attributed to its tool's list.
func gitopsMarkKey(record gitopsRecord, resource *gitopsResource) string {
	parts := []string{record.Tool, record.Namespace, record.Name}
	if resource != nil {
		parts = append(parts, resource.Group, resource.Kind, resource.Namespace, resource.Name)
	}
	return strings.Join(parts, "|")
}

// gitopsDriftSignal is the shape a rule matches against and a channel renders.
func gitopsDriftSignal(cluster db.Cluster, drift gitopsDrift, change *gitopsChange, at time.Time) observability.Signal {
	record := drift.record
	signal := observability.Signal{
		Source:      observability.SourceGitOpsDrift,
		At:          at,
		ClusterID:   cluster.ID,
		Cluster:     cluster.Name,
		Reason:      "OutOfSync",
		Tool:        record.Tool,
		Application: record.Name,
	}

	var b strings.Builder
	if resource := drift.resource; resource != nil {
		signal.Namespace = resource.Namespace
		signal.Object = strings.ToLower(resource.Kind) + "/" + resource.Name
		fmt.Fprintf(&b, "Argo CD reports %s", signal.Object)
		if resource.Namespace != "" {
			fmt.Fprintf(&b, " in %s", resource.Namespace)
		}
		fmt.Fprintf(&b, " out of sync with Application %s/%s", record.Namespace, record.Name)
	} else {
		signal.Namespace = record.destinationNamespace
		if signal.Namespace == "" {
			signal.Namespace = record.Namespace
		}
		signal.Object = strings.ToLower(gitopsKindLabel(record.Tool)) + "/" + record.Name
		fmt.Fprintf(&b, "Flux reports %s %s/%s no longer applied", gitopsKindLabel(record.Tool),
			record.Namespace, record.Name)
		if record.Message != "" {
			fmt.Fprintf(&b, ": %s", record.Message)
		}
	}
	if record.Remote != "" {
		fmt.Fprintf(&b, " (deploys to %s)", record.Remote)
	}
	b.WriteString(".")

	switch {
	case change != nil:
		signal.Username = change.Username
		fmt.Fprintf(&b, " Last write through KubeMG: %s by %s at %s.", change.Verb, change.Username,
			change.At.UTC().Format(time.RFC3339))
	case drift.resource != nil && record.Remote == "":
		b.WriteString(" No write to it went through KubeMG since the last sync; a change made " +
			"outside KubeMG leaves no record here.")
	}
	signal.Message = b.String()

	// One alarm per drifted thing per cool-off: the object for Argo CD, the
	// record for Flux.
	signal.Fingerprint = strings.Join([]string{
		"gitops", cluster.Name, record.Tool, record.Namespace, record.Name, signal.Namespace, signal.Object,
	}, "/")
	return signal
}
