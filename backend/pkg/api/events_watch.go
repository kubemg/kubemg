package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * Holding a cluster's events instead of asking for them.
 *
 * The timeline's paginated read (resources_events.go) is honest but it cannot be
 * *right* on a large cluster, and the reason is structural rather than a matter
 * of tuning: the API server pages an event list in **key order**, so reading
 * "the newest" genuinely means reading all of them. On a cluster holding twenty
 * thousand events that is somewhere north of fifteen megabytes — past the eight
 * the agent will return in one response — so the read is bounded, and a bounded
 * read of a key-ordered collection is an alphabetical sample. The page says so,
 * which is the best a request-shaped read can do.
 *
 * A watch is the shape that fits. One call per cluster keeps answering forever,
 * every event that happens arrives exactly once, and because we then hold every
 * event we saw, "newest first" becomes a fact rather than a claim about a
 * sample. It is also what the API server is built for: one watch is
 * substantially cheaper than one list, and enormously cheaper than a list per
 * page view.
 *
 * Four decisions shape this file.
 *
 * **The watch lives in the backend, not in the agent.** The agent is a stateless
 * relay that has never initiated anything, holds no client-go, and — this is the
 * load-bearing part — its own ServiceAccount holds no permission on any resource
 * at all, only `impersonate`. An agent-side watcher would need a standing,
 * cluster-wide grant on events, which would be the first standing privilege the
 * agent has ever had, in a product whose whole argument is that the agent holds
 * none. Opening the watch from here needs no agent change, no protocol change
 * and no new grant: it rides the tunnel's existing stream machinery, which
 * already carries followed logs.
 *
 * **It is lazy.** A watch starts when somebody first opens a timeline on that
 * cluster and stops when nobody has for a while. A fleet where nobody looks at
 * events costs nothing, which is the same rule the alarm watcher already
 * follows — nothing polls unless something needs it.
 *
 * **The buffer is bounded twice**, by count and by age. Kubernetes discards
 * events after about an hour anyway, so an hour is not a policy this invents; it
 * is the shape of the data. The count bound is what stops one cluster having a
 * very bad day from costing the whole process its memory.
 *
 * **It is filled cluster-wide and filtered per caller.** This is the one genuine
 * trade-off, and it should be read with open eyes: the events a namespace-scoped
 * operator sees are selected by KubeMG rather than refused by the API server.
 * The filter is deliberately one small function over the grant's namespace list
 * (`visibleTo`) so that it can be read and tested in one sitting, because it is
 * now the thing standing where the cluster's own authorizer used to stand.
 */

const (
	// eventBufferSize is how many events one cluster's ring holds. Twenty
	// thousand is the number that prompted this file; the ring holds enough to
	// answer a timeline several times over without holding a whole cluster's
	// history.
	eventBufferSize = 5000

	// eventBufferAge is how far back the ring keeps anything. It matches the
	// default `--event-ttl`, because past that the cluster itself has forgotten
	// and holding more would mean showing an operator events the cluster can no
	// longer corroborate.
	eventBufferAge = time.Hour

	// eventWatchIdle is how long a cluster's watch outlives the last person
	// looking at it. Long enough that closing a tab and reopening it does not
	// re-sync, short enough that a fleet does not accumulate watches for clusters
	// nobody has opened since Tuesday.
	eventWatchIdle = 15 * time.Minute

	// eventWatchRetry is the pause before a dropped watch is re-established. A
	// watch ends for ordinary reasons — the API server rotates them, the tunnel
	// reconnects — so this is a normal path rather than an error path.
	eventWatchRetry = 5 * time.Second

	// eventResyncBackoff is the longer pause after a failed *open*, which is a
	// different thing from a watch that ran and ended: it usually means the
	// cluster is refusing, and retrying at the same rate would be a poll.
	eventResyncBackoff = time.Minute

	// eventWatchTimeout asks the API server to end the watch itself after a
	// while. A watch that is never renewed accumulates state on the server, and
	// re-listing periodically is how every controller bounds that.
	eventWatchTimeout = 30 * time.Minute
)

// bufferedEvent is one event as the ring holds it: the decoded object plus the
// namespace it belongs to, which is what the per-caller filter reads.
type bufferedEvent struct {
	object    eventObject
	namespace string
	at        time.Time
}

/*
 * eventRing is one cluster's recent events.
 *
 * Keyed by the event's UID rather than appended blindly, because a watch sends
 * MODIFIED for every repeat of an event that is already in the ring — a
 * crash-looping pod updates one Event object's count rather than creating a new
 * one — so appending would hold forty copies of the same row and count it forty
 * times. Replacing in place is also what makes the ring's size mean something:
 * it bounds distinct events, not firings.
 */
type eventRing struct {
	mu     sync.RWMutex
	byUID  map[string]*bufferedEvent
	order  []string
	synced bool
	// syncedAt is when the ring last completed a list, which is what tells a
	// caller whether it is looking at a warm buffer or a cold one.
	syncedAt time.Time
	// lastErr is why the watch is not running, if it is not.
	lastErr error
}

func newEventRing() *eventRing {
	return &eventRing{byUID: map[string]*bufferedEvent{}}
}

// put files or replaces one event.
func (r *eventRing) put(item eventObject) {
	uid := item.Metadata.UID
	if uid == "" {
		// An event with no UID cannot be deduplicated, so it is keyed by the
		// thing that identifies it otherwise. This does not happen against a real
		// API server; it keeps a malformed feed from collapsing every row onto
		// one key.
		uid = item.Metadata.Namespace + "/" + item.Metadata.Name
	}

	namespace := item.InvolvedObject.Namespace
	if namespace == "" {
		namespace = item.Metadata.Namespace
	}
	entry := &bufferedEvent{object: item, namespace: namespace, at: eventAt(item.view())}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, found := r.byUID[uid]; !found {
		r.order = append(r.order, uid)
	}
	r.byUID[uid] = entry
	r.trimLocked()
}

// drop removes a deleted event. The API server deletes an event when it ages
// out, and honouring that is what keeps the ring from showing an operator
// something the cluster has forgotten.
func (r *eventRing) drop(item eventObject) {
	uid := item.Metadata.UID
	if uid == "" {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.byUID, uid)
}

// reset empties the ring for a fresh list. A re-sync replaces the contents
// rather than merging into them: the list is the cluster's current truth, and
// anything held that is not in it is gone.
func (r *eventRing) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byUID = map[string]*bufferedEvent{}
	r.order = r.order[:0]
}

// markSynced records that a list completed.
func (r *eventRing) markSynced() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.synced = true
	r.syncedAt = time.Now()
	r.lastErr = nil
}

// markFailed records why the ring is not being kept up to date.
func (r *eventRing) markFailed(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.synced = false
	r.lastErr = err
}

// trimLocked enforces both bounds. The count bound walks the insertion order,
// which is not the same as time order — an event modified long after it was
// first seen keeps its original slot — but it is the order things entered the
// ring, and evicting the oldest arrival is the property that matters.
func (r *eventRing) trimLocked() {
	floor := time.Now().Add(-eventBufferAge)

	kept := r.order[:0]
	for _, uid := range r.order {
		entry, found := r.byUID[uid]
		if !found {
			continue
		}
		if !entry.at.IsZero() && entry.at.Before(floor) {
			delete(r.byUID, uid)
			continue
		}
		kept = append(kept, uid)
	}
	r.order = kept

	for len(r.order) > eventBufferSize {
		delete(r.byUID, r.order[0])
		r.order = r.order[1:]
	}
}

// snapshot returns the events a caller may see, newest first.
//
// The filter is the whole security surface of this design, so it is one
// predicate applied to one field: an unscoped grant sees everything the buffer
// holds, and a scoped one sees exactly the namespaces its grant lists. There is
// deliberately no cleverness here — no prefix matching, no wildcard, nothing
// that could be argued about later.
func (r *eventRing) snapshot(allowed []string) []eventObject {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]eventObject, 0, len(r.byUID))
	for _, entry := range r.byUID {
		if !visibleTo(entry.namespace, allowed) {
			continue
		}
		out = append(out, entry.object)
	}
	return out
}

// visibleTo is the filter that now stands where the cluster's authorizer used to
// for this one surface. An empty allow-list is an unscoped grant, which the rest
// of KubeMG already treats as "everything the impersonated role allows".
func visibleTo(namespace string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	return slices.Contains(allowed, namespace)
}

// state reports whether the ring is worth reading.
func (r *eventRing) state() (synced bool, at time.Time, err error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.synced, r.syncedAt, r.lastErr
}

/* --------------------------------------------------------------- watching --- */

// eventWatcher keeps one ring per cluster, each fed by one watch.
type eventWatcher struct {
	mu       sync.Mutex
	clusters map[uint]*watchedCluster
}

type watchedCluster struct {
	ring   *eventRing
	cancel context.CancelFunc
	// lastRead is when somebody last asked for this cluster's events. It is what
	// the idle stop reads, and it is the reason a fleet nobody is watching costs
	// nothing.
	lastRead time.Time
}

func newEventWatcher() *eventWatcher {
	return &eventWatcher{clusters: map[uint]*watchedCluster{}}
}

/*
 * ring returns a cluster's buffer, starting its watch if this is the first ask.
 *
 * The laziness is the point. A timeline nobody opens costs nothing; the first
 * person to open one pays a list, and everybody after them — including that same
 * person refreshing — pays nothing at all. It also means a cluster whose events
 * cannot be watched degrades to the paginated read for that cluster alone rather
 * than for the fleet.
 */
// It deliberately takes no context. The watch it may start outlives whatever
// asked for it — a page load, an alarm pass — and a parameter suggesting
// otherwise would invite somebody to pass a request scope, which would cancel
// the watch the moment the page finished loading.
func (s *server) eventRingFor(cluster *db.Cluster) *eventRing {
	if s.events == nil {
		return nil
	}

	s.events.mu.Lock()
	defer s.events.mu.Unlock()

	watched, found := s.events.clusters[cluster.ID]
	if found {
		watched.lastRead = time.Now()
		return watched.ring
	}

	// The watch outlives the request that started it, so it is scoped to the
	// server's background context rather than to this HTTP call — otherwise it
	// would be cancelled the moment the page finished loading.
	base := s.background
	if base == nil {
		return nil
	}
	runCtx, cancel := context.WithCancel(base)

	watched = &watchedCluster{ring: newEventRing(), cancel: cancel, lastRead: time.Now()}
	s.events.clusters[cluster.ID] = watched

	// A copy, because the caller's cluster is a request-scoped value and this
	// goroutine outlives it.
	target := *cluster
	go s.runEventWatch(runCtx, target, watched.ring)

	return watched.ring
}

// releaseIdleEventWatches stops the watches nobody has read from lately. It is
// called on the same schedule as the other housekeeping rather than on a timer
// of its own.
func (s *server) releaseIdleEventWatches() {
	if s.events == nil {
		return
	}

	s.events.mu.Lock()
	defer s.events.mu.Unlock()

	for id, watched := range s.events.clusters {
		if time.Since(watched.lastRead) < eventWatchIdle {
			continue
		}
		watched.cancel()
		delete(s.events.clusters, id)
		s.log().Debug("stopped an idle cluster event watch", slog.Uint64("cluster_id", uint64(id)))
	}
}

// startEventWatchSweeper runs the idle stop for as long as the server lives.
func (s *server) startEventWatchSweeper(ctx context.Context) {
	if ctx == nil || s.events == nil {
		return
	}

	ticker := time.NewTicker(eventWatchIdle / 3)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.releaseIdleEventWatches()
		}
	}
}

/*
 * runEventWatch keeps one cluster's ring current: list, then watch, then list
 * again whenever the watch ends.
 *
 * The list-then-watch pair is the standard shape and it is standard because both
 * halves are necessary. The list establishes what is there now and gives the
 * resourceVersion the watch resumes from; the watch delivers everything after
 * it. Watching without listing first would show only what happened since the
 * page was opened, which for a surface whose whole job is "what broke" is the
 * wrong half of the answer.
 *
 * A watch ending is normal — the API server rotates them, tunnels reconnect —
 * so the loop treats it as a cycle rather than as a failure, and only an open
 * that fails backs off.
 */
func (s *server) runEventWatch(ctx context.Context, cluster db.Cluster, ring *eventRing) {
	// The identity the buffer is filled under. It is the same synthetic
	// cluster-admin the alarm watcher already uses, for the same reason: a shared
	// buffer cannot be filled as any one user, and this is an existing posture
	// rather than a new one. What a *caller* then sees is narrowed by their own
	// grant in `snapshot`.
	user := &db.User{Username: eventWatcherUser}
	grant := db.UserClusterAccess{ClusterID: cluster.ID, K8sRole: db.K8sRoleClusterAdmin}

	for {
		if ctx.Err() != nil {
			return
		}

		version, err := s.syncEventRing(ctx, user, &cluster, grant, ring)
		if err != nil {
			ring.markFailed(err)
			s.log().Debug("could not sync cluster events",
				slog.String("cluster", cluster.Name), slog.String("error", err.Error()))
			if !sleepCtx(ctx, eventResyncBackoff) {
				return
			}
			continue
		}
		ring.markSynced()

		if err := s.streamEvents(ctx, user, &cluster, grant, ring, version); err != nil {
			// A watch that ended is not a watch that failed. Only the *open*
			// failing is worth a long pause; a stream that ran and stopped is
			// re-established promptly, because the gap is a gap in what the
			// operator can see.
			s.log().Debug("cluster event watch ended",
				slog.String("cluster", cluster.Name), slog.String("error", err.Error()))
		}
		if !sleepCtx(ctx, eventWatchRetry) {
			return
		}
	}
}

// eventWatcherUser is the identity the shared buffer is filled under. It is
// separate from the alarm watcher's so the audit trail can tell "the timeline
// buffer re-synced" from "the alarm poll ran".
const eventWatcherUser = "kubemg:event-watcher"

// syncEventRing lists the cluster's events into the ring and returns the
// resourceVersion the watch should resume from.
func (s *server) syncEventRing(ctx context.Context, user *db.User, cluster *db.Cluster,
	grant db.UserClusterAccess, ring *eventRing,
) (string, error) {
	query := url.Values{}
	query.Set("limit", strconv.Itoa(eventPageSize))

	ring.reset()

	version := ""
	budget := &eventBudget{scan: eventBufferSize}
	token := ""
	for {
		page := cloneQuery(query)
		if token != "" {
			page.Set("continue", token)
		}

		budget.requests++
		resp, err := s.proxy.Call(ctx, user, cluster, grant,
			"GET", "/api/v1/events?"+page.Encode(), nil, nil)
		if err != nil {
			return "", err
		}
		if resp.Status < 200 || resp.Status >= 300 {
			return "", errors.New(kubeErrorMessage(resp.Body, resp.Status))
		}

		var decoded struct {
			Metadata struct {
				Continue        string `json:"continue"`
				ResourceVersion string `json:"resourceVersion"`
			} `json:"metadata"`
			Items []eventObject `json:"items"`
		}
		if err := json.Unmarshal(resp.Body, &decoded); err != nil {
			return "", errors.New("the cluster returned an unreadable event list")
		}

		// The collection's version comes from the first page and is what makes
		// the watch continuous: resuming from it means no event between the list
		// and the watch is missed.
		if version == "" {
			version = decoded.Metadata.ResourceVersion
		}
		for _, item := range decoded.Items {
			ring.put(item)
			budget.scanned++
		}

		token = decoded.Metadata.Continue
		if token == "" || budget.spent() {
			break
		}
	}

	if version == "" {
		return "", errors.New("the cluster did not report a resource version")
	}
	return version, nil
}

// streamEvents holds the watch open, folding each event into the ring as it
// arrives. It returns when the stream ends, which is expected rather than
// exceptional.
func (s *server) streamEvents(ctx context.Context, user *db.User, cluster *db.Cluster,
	grant db.UserClusterAccess, ring *eventRing, version string,
) error {
	query := url.Values{}
	query.Set("watch", "true")
	query.Set("resourceVersion", version)
	query.Set("timeoutSeconds", strconv.Itoa(int(eventWatchTimeout.Seconds())))
	// Bookmarks let the API server advance our position without sending objects,
	// which is what keeps a resume cheap on a quiet cluster.
	query.Set("allowWatchBookmarks", "true")

	stream, err := s.proxy.Watch(ctx, user, cluster, grant, "/api/v1/events?"+query.Encode())
	if err != nil {
		return err
	}
	defer stream.Close(nil)

	// A watch is newline-delimited JSON, and one event can span several tunnel
	// chunks — so the chunks are reassembled rather than parsed one at a time.
	reader := newLineAssembler()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-stream.Done():
			return stream.Err()
		case chunk, open := <-stream.Chunks():
			if !open {
				return stream.Err()
			}
			for _, line := range reader.push(chunk.Data) {
				if err := applyWatchLine(ring, line); err != nil {
					// A single unreadable frame is not a reason to tear down a
					// watch that is otherwise delivering.
					continue
				}
			}
		}
	}
}

// watchEvent is one frame of a watch stream.
type watchEvent struct {
	Type   string      `json:"type"`
	Object eventObject `json:"object"`
}

// applyWatchLine folds one frame into the ring.
func applyWatchLine(ring *eventRing, line []byte) error {
	if len(bytes.TrimSpace(line)) == 0 {
		return nil
	}

	var frame watchEvent
	if err := json.Unmarshal(line, &frame); err != nil {
		return err
	}

	switch frame.Type {
	case "ADDED", "MODIFIED":
		// MODIFIED is the common case rather than the exception: a repeating
		// event updates one object's count instead of creating new ones, which is
		// exactly why the ring is keyed by UID.
		ring.put(frame.Object)
	case "DELETED":
		ring.drop(frame.Object)
	case "BOOKMARK":
		// Position only; no object worth holding.
	case "ERROR":
		return fmt.Errorf("the cluster reported a watch error")
	}
	return nil
}

/*
 * lineAssembler turns tunnel chunks back into whole lines.
 *
 * A watch is newline-delimited JSON and the tunnel chops it at arbitrary byte
 * boundaries, so a frame routinely arrives split across two chunks. Parsing each
 * chunk on its own would drop exactly the events that happened to straddle a
 * boundary — which is both silent and load-dependent, the worst pairing for a
 * bug in a surface people trust to be complete.
 */
type lineAssembler struct {
	buffer bytes.Buffer
}

func newLineAssembler() *lineAssembler { return &lineAssembler{} }

func (a *lineAssembler) push(data []byte) [][]byte {
	a.buffer.Write(data)

	var lines [][]byte
	for {
		content := a.buffer.Bytes()
		i := bytes.IndexByte(content, '\n')
		if i < 0 {
			break
		}
		line := make([]byte, i)
		copy(line, content[:i])
		lines = append(lines, line)
		a.buffer.Next(i + 1)
	}

	// A single frame larger than the buffer would grow without bound if the
	// cluster never sent a newline. Nothing in the Kubernetes watch protocol does
	// that, but the buffer is reset rather than trusted.
	if a.buffer.Len() > bufio.MaxScanTokenSize {
		a.buffer.Reset()
	}
	return lines
}

// sleepCtx waits, reporting false if the context ended first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
