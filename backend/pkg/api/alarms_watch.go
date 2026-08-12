package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/bastion"
	"github.com/kubemg/kubemg/backend/pkg/db"
	"github.com/kubemg/kubemg/backend/pkg/observability"
)

/*
 * Watching the fleet for events worth alarming on.
 *
 * This file used to open by saying there was no push here and there could not
 * be — that a long-lived stream held by the server for its own purposes was a
 * lifetime KubeMG did not have, since every other stream belongs to a request
 * somebody made. That was true when it was written and it is not any more:
 * `events_watch.go` now holds exactly such a stream, one per cluster, because
 * the events *timeline* needed the same data continuously and for the same
 * reason. Given that buffer exists, polling beside it would be reading the same
 * events twice a minute through two mechanisms.
 *
 * So the pass now prefers the buffer and keeps the poll as its fallback. The
 * fallback is not vestigial: a watch can be refused where a list is not, and
 * alarms are the one surface here that must not go quiet because an optimisation
 * did not apply. A cluster whose watch will not establish is polled exactly as
 * it always was.
 *
 * What has not changed, and is the reason this file is careful:
 *
 *   - Nothing runs unless a cluster-event rule exists. A fleet with no alarms
 *     configured still makes no calls at all — the buffer is only asked for on
 *     behalf of a cluster some rule covers.
 *   - A cluster with no attached agent is skipped rather than dialled.
 *   - The first pass on a cluster establishes a watermark and delivers nothing.
 *     Otherwise switching a rule on would page for a week of history, which is
 *     both useless and the fastest way to have the integration muted.
 *   - **Exactly one replica alarms.** A lease decides which process does it (see
 *     pkg/db/lease.go), and that matters more rather than less now: every
 *     replica could hold its own buffer, so the lease is the only thing standing
 *     between three replicas and three copies of every page.
 */

// Cluster event polling.
const (
	// alarmPollInterval is how often each cluster's events are re-read. A minute
	// is chosen against what the events themselves do: the kubelet re-emits a
	// crash loop's event on its own backoff schedule, and Kubernetes keeps events
	// for an hour, so a minute cannot miss one and does not read the same one
	// twenty times.
	alarmPollInterval = time.Minute
	// alarmEventLimit bounds one list. A cluster that produced more warnings than
	// this in one interval has a problem an alarm is not going to help with, and
	// the ones dropped are the older ones.
	alarmEventLimit = 500
	// alarmEventMaxAge ignores anything older than this on a pass, as a second
	// guard beside the watermark: a cluster whose clock is behind, or an event
	// list that arrives out of order, must not resurrect yesterday.
	alarmEventMaxAge = 15 * time.Minute
	// alarmLeaseTTL is how long a claim on the polling job stays live without
	// being renewed. It is a multiple of the interval rather than equal to it: the
	// holder renews on every tick, so a TTL of exactly one interval would expire
	// in the gap between two ticks and hand the job back and forth. Three gives a
	// pass room to be slow, and bounds how long the fleet goes unwatched after a
	// replica dies to the same three minutes.
	alarmLeaseTTL = 3 * alarmPollInterval
)

// alarmWatcherUser is the identity the poller impersonates.
//
// It is a name rather than a real account and it appears in the audit trail as
// one, which is the honest answer: these reads are KubeMG acting on its own
// behalf, not on any operator's, and a trail that attributed them to whichever
// admin happened to configure the rule would be a lie. Authorization comes from
// the impersonated *group* — `kubemg:cluster-admin`, bound in the agent's own
// manifests — so the username never has to exist on the cluster either.
const alarmWatcherUser = "kubemg:alarm-watcher"

// startAlarmWatcher polls cluster events for as long as the context lives.
func (s *server) startAlarmWatcher(ctx context.Context) {
	ticker := time.NewTicker(alarmPollInterval)
	defer ticker.Stop()

	watermarks := &watermarkTable{seen: map[uint]time.Time{}}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.alarmTick(ctx, watermarks)
		}
	}
}

// alarmTick is one pass: take the lease, and poll only if this replica got it.
//
// Every replica keeps ticking rather than one of them deciding at startup that it
// is not the leader — that is what makes the handover automatic when the holder
// is killed, with no election protocol and nothing to configure.
//
// The watermarks are deliberately *not* cleared when a pass is skipped. A replica
// that takes over holds none yet, so it establishes them and delivers nothing on
// its first pass, exactly as a restart does. A replica that briefly lost the lease
// and regained it keeps what it had, which can re-offer events the previous holder
// already sent — bounded to alarmEventMaxAge, and collapsed by the dispatcher's
// own per-rule deduplication. Repeating an alarm is the recoverable failure here;
// silently dropping one is not.
func (s *server) alarmTick(ctx context.Context, watermarks *watermarkTable) {
	held, err := s.store.AcquireLease(ctx, db.LeaseAlarmWatcher, s.instanceID, alarmLeaseTTL)
	if err != nil {
		// Failing closed is the point. If the database cannot answer, every
		// replica gets this error, and treating it as permission to poll would put
		// all of them on the cluster at once — which is the duplication the lease
		// exists to stop, arriving precisely when something is already wrong.
		if ctx.Err() == nil {
			s.log().Warn("alarm watcher could not take its lease",
				slog.String("error", err.Error()))
		}
		return
	}
	if !held {
		return
	}
	s.pollClusterEvents(ctx, watermarks)
}

// watermarkTable remembers how far through each cluster's event stream the
// watcher has read. It is in memory rather than in the database on purpose: a
// restart should re-establish a watermark and deliver nothing, not replay
// whatever accumulated while the process was down — the events are an hour old by
// then and the incident either resolved itself or has already paged someone.
type watermarkTable struct {
	mu   sync.Mutex
	seen map[uint]time.Time
}

// advance reports whether an event is new, and records it as read. The first
// call for a cluster establishes the mark and reports nothing new.
func (w *watermarkTable) advance(clusterID uint, at time.Time) bool {
	w.mu.Lock()
	defer w.mu.Unlock()

	mark, known := w.seen[clusterID]
	if at.After(mark) {
		w.seen[clusterID] = at
	}
	return known && at.After(mark)
}

// establish records the pass boundary for a cluster the watcher has not read
// before, so its next pass has something to compare against.
func (w *watermarkTable) establish(clusterID uint, at time.Time) bool {
	w.mu.Lock()
	defer w.mu.Unlock()

	if _, known := w.seen[clusterID]; known {
		return false
	}
	w.seen[clusterID] = at
	return true
}

// forget drops a cluster's mark, so a cluster that goes away and comes back is
// treated as new rather than replaying whatever it produced while it was gone.
func (w *watermarkTable) forget(clusterID uint) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.seen, clusterID)
}

// pollClusterEvents reads one round of events across the clusters any rule cares
// about.
func (s *server) pollClusterEvents(ctx context.Context, watermarks *watermarkTable) {
	if s.alarms == nil || s.proxy == nil {
		return
	}
	rules := s.alarms.Rules(db.TriggerClusterEvent)
	if len(rules) == 0 {
		return
	}

	clusters, err := s.store.Clusters(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.log().Warn("alarm watcher could not list clusters", slog.String("error", err.Error()))
		}
		return
	}

	for _, cluster := range clusters {
		if ctx.Err() != nil {
			return
		}
		// A rule scoped to another cluster is not a reason to read this one.
		if !watched(rules, cluster.ID) {
			continue
		}
		if connectionMode(cluster) != db.ModeAgent || s.tunnels == nil || !s.tunnels.Connected(cluster.ID) {
			watermarks.forget(cluster.ID)
			continue
		}
		s.pollOneCluster(ctx, cluster, watermarks)
	}
}

// watched reports whether any rule covers this cluster.
func watched(rules []db.AlarmRule, clusterID uint) bool {
	for _, rule := range rules {
		if rule.ClusterID == 0 || rule.ClusterID == clusterID {
			return true
		}
	}
	return false
}

// pollOneCluster reads a cluster's recent events and offers the new ones.
func (s *server) pollOneCluster(
	ctx context.Context, cluster db.Cluster, watermarks *watermarkTable,
) {
	items, ok := s.alarmEvents(ctx, cluster)
	if !ok {
		return
	}

	now := time.Now().UTC()
	if watermarks.establish(cluster.ID, now) {
		s.log().Info("alarm watcher is now watching cluster events",
			slog.String("cluster", cluster.Name),
			slog.Int("events_in_window", len(items)))
		return
	}

	s.offerEvents(cluster, items, watermarks, now)
}

/*
 * alarmEvents is where this pass gets its events: the cluster's own buffer if
 * one is warm, and the list it always used if not.
 *
 * Asking for the ring is also what *keeps* it warm. The idle sweeper stops a
 * watch nobody has read from in a while, and this pass reading it every minute
 * is a read — so a cluster carrying an alarm rule holds its watch open for as
 * long as the rule exists, and one carrying none is left to the timeline's
 * laziness. That falls out of the existing mechanism rather than needing a
 * second one, which is why there is no "pinned" flag anywhere.
 *
 * Only the lease holder runs this, so only the lease holder keeps a buffer warm
 * for alarming — the other replicas let theirs idle out. On a handover the new
 * holder starts cold: its first tick starts a watch and reads nothing, its
 * second establishes the watermark, its third delivers. That is one tick slower
 * than the poll used to be, on a signal whose events live for an hour, and it is
 * the same shape of delay a restart has always had.
 */
func (s *server) alarmEvents(ctx context.Context, cluster db.Cluster) ([]eventObject, bool) {
	if ring := s.eventRingFor(&cluster); ring != nil {
		synced, _, err := ring.state()
		if synced {
			// The whole buffer: a rule may name any namespace, or none, so the
			// alarm path is the one caller that is never narrowed.
			return ring.snapshot(nil), true
		}
		if err == nil {
			// The watch is starting or re-syncing. Waiting a tick is right —
			// polling *as well* would be the duplicate read this change removes.
			return nil, false
		}
		// The watch cannot be established on this cluster. Alarms are not allowed
		// to go quiet because an optimisation did not apply, so this falls through
		// to the list below and keeps falling through for as long as it fails.
		s.log().Debug("alarm watcher is falling back to polling cluster events",
			slog.String("cluster", cluster.Name), slog.String("error", err.Error()))
	}

	return s.listAlarmEvents(ctx, cluster)
}

// listAlarmEvents is the original read, kept whole as the fallback.
//
// It goes through Proxy.Call like everything else, so it is impersonated and it
// lands in the audit trail — which is why the audit verb selection and this
// feature are worth having together: on a fleet where successful `list` records
// have been switched off, the watcher costs the trail nothing.
func (s *server) listAlarmEvents(ctx context.Context, cluster db.Cluster) ([]eventObject, bool) {
	user := &db.User{Username: alarmWatcherUser}
	grant := db.UserClusterAccess{ClusterID: cluster.ID, K8sRole: db.K8sRoleClusterAdmin}

	// Events are read cluster-wide because a rule may name any namespace, or
	// none.
	path := fmt.Sprintf("/api/v1/events?limit=%d", alarmEventLimit)
	resp, err := s.proxy.Call(ctx, user, &cluster, grant, "GET", path, nil)
	if err != nil {
		if ctx.Err() == nil {
			// Logged at debug: a cluster whose agent dropped between the check above
			// and this call is ordinary, and warning on it every minute would make
			// the log the problem.
			s.log().Debug("alarm watcher could not read cluster events",
				slog.String("cluster", cluster.Name),
				slog.String("error", err.Error()))
		}
		return nil, false
	}

	var list struct {
		Items []eventObject `json:"items"`
	}
	if err := json.Unmarshal(resp.Body, &list); err != nil {
		s.log().Warn("alarm watcher could not decode a cluster event list",
			slog.String("cluster", cluster.Name),
			slog.String("error", err.Error()))
		return nil, false
	}
	return list.Items, true
}

/*
 * offerEvents hands the new events to the dispatcher, oldest first.
 *
 * The ordering is load-bearing and it was wrong before, in a way worth naming
 * because it was silent. `watermarks.advance` both tests an event and raises the
 * mark to it, so whichever event is offered first sets the floor for the rest of
 * the pass — and the list arrives in the API server's key order, which has
 * nothing to do with time. An event at T+50s reaching the loop before one at
 * T+30s therefore dropped the second, even though both were new since the last
 * pass. Sorting oldest-first makes every event in the window pass the mark it is
 * actually being compared against.
 *
 * The events at the far end of the window are dropped by `alarmEventMaxAge`
 * rather than by the mark: a cluster whose clock is behind, or a list that
 * arrives out of order, must not resurrect yesterday.
 */
func (s *server) offerEvents(
	cluster db.Cluster, items []eventObject, watermarks *watermarkTable, now time.Time,
) {
	for _, entry := range selectAlarmEvents(cluster.ID, items, watermarks, now) {
		s.alarms.Observe(eventSignal(cluster, entry.item, entry.view))
	}
}

// alarmCandidate is one event that survived the window and the watermark.
type alarmCandidate struct {
	item eventObject
	view eventView
	at   time.Time
}

// selectAlarmEvents is the decision half of offerEvents, separated from the
// dispatch so the ordering rule above can be asserted directly. Which events a
// pass delivers is the entire behaviour of this feature, and it is not something
// a test should have to reach through a dispatcher to observe.
func selectAlarmEvents(
	clusterID uint, items []eventObject, watermarks *watermarkTable, now time.Time,
) []alarmCandidate {
	floor := now.Add(-alarmEventMaxAge)

	fresh := make([]alarmCandidate, 0, len(items))
	for _, item := range items {
		view := item.view()
		if view.LastSeen == nil || view.LastSeen.Before(floor) {
			continue
		}
		fresh = append(fresh, alarmCandidate{item: item, view: view, at: view.LastSeen.UTC()})
	}

	sort.SliceStable(fresh, func(a, b int) bool { return fresh[a].at.Before(fresh[b].at) })

	out := fresh[:0]
	for _, entry := range fresh {
		if !watermarks.advance(clusterID, entry.at) {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// eventSignal turns one Kubernetes Event into the shape a rule matches against.
func eventSignal(cluster db.Cluster, item eventObject, view eventView) observability.Signal {
	namespace := item.InvolvedObject.Namespace
	if namespace == "" {
		namespace = item.Metadata.Namespace
	}

	object := ""
	if item.InvolvedObject.Name != "" {
		kind := strings.ToLower(item.InvolvedObject.Kind)
		if kind == "" {
			object = item.InvolvedObject.Name
		} else {
			object = kind + "/" + item.InvolvedObject.Name
		}
	}

	at := view.LastSeen.UTC()

	return observability.Signal{
		Source:    observability.SourceClusterEvent,
		At:        at,
		ClusterID: cluster.ID,
		Cluster:   cluster.Name,
		Namespace: namespace,
		Reason:    view.Reason,
		Type:      view.Type,
		Object:    object,
		Message:   view.Message,
		Count:     view.Count,
		// The fingerprint identifies the problem rather than the occurrence, so a
		// pod restarting on a loop collapses into one alarm per cool-off window. The
		// event's own UID would be the opposite: it changes as the series is
		// re-aggregated, which is precisely when a pager must stay quiet.
		Fingerprint: strings.Join([]string{
			"event", cluster.Name, namespace, object, view.Reason,
		}, "/"),
	}
}

/* ------------------------------------------------------------ audit feed --- */

// alarmAuditor forwards audit records to the dispatcher.
//
// It is an Auditor rather than a hook inside the proxy because that is exactly
// what it is — another consumer of the same record, alongside the structured log
// and the table. It sits in this package because this is the only one that already
// depends on both the gateway and the alarm engine; putting it in either of those
// would make them depend on each other.
type alarmAuditor struct {
	dispatcher *observability.Dispatcher
}

// NewAlarmAuditor adapts the alarm dispatcher into the gateway's audit fan-out.
// A nil dispatcher yields nil, which MultiAuditor drops — so a server built
// without alarms is wired exactly as it was before.
func NewAlarmAuditor(dispatcher *observability.Dispatcher) bastion.Auditor {
	if dispatcher == nil {
		return nil
	}
	return &alarmAuditor{dispatcher: dispatcher}
}

// Record offers one audit event as a signal.
//
// The closing record of a stream is skipped: an exec that opened and then ended
// is one action, and alarming on both would page twice for the same shell. The
// opening record is the one that matters, because that is when somebody is still
// in there.
func (a *alarmAuditor) Record(_ context.Context, event bastion.Event) {
	if event.Streaming && event.Phase == bastion.PhaseClose {
		return
	}

	denied := event.Error != "" || event.Status >= 400

	object := event.Resource
	if object != "" && event.Namespace != "" {
		object = event.Namespace + "/" + event.Resource
	}

	a.dispatcher.Observe(observability.Signal{
		Source:    observability.SourceAudit,
		At:        event.At,
		ClusterID: event.ClusterID,
		Cluster:   event.Cluster,
		Namespace: event.Namespace,
		Reason:    event.Verb,
		Object:    object,
		Message:   auditMessage(event, denied),
		Verb:      event.Verb,
		Username:  event.Username,
		UserID:    event.UserID,
		Status:    event.Status,
		Path:      event.Path,
		Denied:    denied,
		Error:     event.Error,
		// One fingerprint per user, cluster, verb and outcome. A developer whose
		// kubectl is being refused retries it — often in a loop — and the useful
		// alarm is "this person is being refused", once, not one per attempt.
		Fingerprint: strings.Join([]string{
			"audit",
			event.Cluster,
			event.Username,
			event.Verb,
			event.Namespace,
			deniedLabel(denied),
		}, "/"),
	})
}

func deniedLabel(denied bool) string {
	if denied {
		return "denied"
	}
	return "allowed"
}

// auditMessage is the line a recipient reads. It leads with what was refused and
// why, because on an audit alarm that is the entire content.
func auditMessage(event bastion.Event, denied bool) string {
	var b strings.Builder
	if denied {
		b.WriteString("KubeMG refused ")
	} else {
		b.WriteString("KubeMG proxied ")
	}
	fmt.Fprintf(&b, "%s %s", strings.ToUpper(event.Method), event.Path)
	if event.Cluster != "" {
		fmt.Fprintf(&b, " on %s", event.Cluster)
	}
	if event.Username != "" {
		fmt.Fprintf(&b, " for %s", event.Username)
	}
	if event.Error != "" {
		fmt.Fprintf(&b, " — %s", event.Error)
	} else if event.Status != 0 {
		fmt.Fprintf(&b, " — HTTP %d", event.Status)
	}
	return b.String()
}
