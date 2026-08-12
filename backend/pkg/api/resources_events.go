package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * What just broke, across a whole cluster.
 *
 * Events are already read, already decoded in both shapes and already rendered —
 * but only inside one object's Describe tab, which answers "why is *this* not
 * ready". That is the second question. The first one, the one somebody opens the
 * console with at 09:05, is "what broke in the last fifteen minutes", and until
 * now nothing in KubeMG could answer it: you had to already suspect an object
 * before you could read the events explaining it.
 *
 * The read itself is the describe's read with the object selector taken off, so
 * everything it already gets right comes along unchanged:
 *
 *   - the **dual decode** (`eventObject.view()`), because an event written
 *     through `events.k8s.io` arrives on the core list with `lastTimestamp` and
 *     `count` empty and its time in `eventTime`/`series` — reading one shape
 *     shows a cluster's newest events with no timestamp at all;
 *   - the **events_available contract**, because events are their own resource
 *     with their own RBAC and a refusal has to narrow the answer rather than
 *     fail it;
 *   - the **scope fan-out**, because "everything I may see" is one cluster-wide
 *     read for an unscoped grant and one read per granted namespace for a scoped
 *     one, and a cluster-wide list would reach past the scope.
 *
 * What makes it a timeline rather than a second table is the grouping below.
 */

const (
	// eventPageSize is one page of a list read. It is a *page* rather than the
	// answer, which is the distinction the first version of this file got wrong:
	// `limit` pages in **key order** (namespace/name), and an Event's name is
	// `<object>.<hex>`, so a single page of a busy cluster is an alphabetical
	// slice by involved object and not the newest anything. A timeline built from
	// one page would order that slice by time and present it as "the newest",
	// which is wrong in a way that only appears once a cluster is big enough to
	// exceed one page — that is, only in production.
	eventPageSize = 500

	// maxEventScan is the default global budget for one request: how many events
	// will be read and folded before the answer is declared partial. It is global
	// rather than per-namespace because an all-namespaces read fans out, and a
	// per-page bound would multiply by the fan-out instead of bounding it.
	// `KUBEMG_EVENT_SCAN_LIMIT` moves it, since the right value is a property of
	// the cluster rather than of this code.
	maxEventScan = 4000

	// defaultEventCacheTTL is how long a timeline answer is held — six times the
	// resource default, for the reason readTTL sets out: no KubeMG write produces
	// an Event, so there is nothing for a stale entry to hide, and this is the
	// page a whole team opens at once during an incident.
	defaultEventCacheTTL = 30 * time.Second

	// maxEventRequests bounds the round trips one page view costs, whatever the
	// budget above would otherwise allow. Every page is a tunnel round trip, an
	// impersonated call and an audit record; a read that quietly cost forty of
	// them would be a worse surface than one that admits it saw part of the
	// cluster.
	maxEventRequests = 12

	// maxEventGroups bounds the answer after grouping. Grouping is what keeps a
	// crash-looping cluster to a readable page, but a cluster with four hundred
	// distinct broken objects is a different kind of problem and this is not the
	// tool for it.
	maxEventGroups = 200

	// maxGroupEntries bounds the reasons carried inside one object's group. An
	// object that has genuinely produced more distinct reasons than this is
	// answered by its own Describe tab, which reads only its events.
	maxGroupEntries = 20
)

// eventEntryView is one *kind* of thing that happened to an object: a reason and
// a type, with every firing of it folded together. Kubernetes already folds
// repeats into one Event object with a `count` and a first/last time — but only
// per reporting component and only until the event ages out, so the same reason
// routinely arrives as several objects, and printing them as separate rows is
// how a timeline turns into forty lines of `BackOff`.
type eventEntryView struct {
	Type   string `json:"type"`
	Reason string `json:"reason"`
	// Message is the newest one seen for this reason. Messages of one reason
	// differ in their details ("Back-off restarting failed container" carries the
	// container), and the most recent is the one describing the state now.
	Message string `json:"message"`
	// Count is every firing folded together, not the number of Event objects.
	Count     int32      `json:"count"`
	Source    string     `json:"source,omitempty"`
	FirstSeen *time.Time `json:"first_seen,omitempty"`
	LastSeen  *time.Time `json:"last_seen,omitempty"`
}

// eventObjectRef names what an event was about.
type eventObjectRef struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
}

// eventGroupView is one row of the timeline: everything the cluster has said
// about one object, newest first.
//
// The collapse is by **involved object** rather than by reason, and that is the
// decision that makes this readable. A failing Deployment produces a
// `ScalingReplicaSet` from the deployment controller, a `FailedCreate` from the
// replica set, and a `BackOff` and a `Failed` per pod — which as rows is forty
// lines describing one problem. As one entry it is one problem, and opening it
// is how you get the forty lines when you actually want them.
type eventGroupView struct {
	// Key identifies the group across refreshes, so an expanded row stays
	// expanded when the page re-reads.
	Key    string         `json:"key"`
	Object eventObjectRef `json:"object"`

	// Type is the worst type in the group: an object with one Warning among ten
	// Normals is a warning, because that is the one somebody has to look at.
	Type string `json:"type"`
	// Reason and Message are the newest entry's, which is what the collapsed row
	// shows — the most recent thing the cluster said about this object.
	Reason  string `json:"reason"`
	Message string `json:"message"`

	// Count is every firing of every entry; Warnings is how many of those were
	// warnings. Both are totals rather than row counts, so a row saying 41 means
	// the cluster said something 41 times.
	Count    int32 `json:"count"`
	Warnings int32 `json:"warnings"`

	FirstSeen *time.Time `json:"first_seen,omitempty"`
	LastSeen  *time.Time `json:"last_seen,omitempty"`

	Entries []eventEntryView `json:"entries"`
	// EntriesTruncated marks a group whose distinct reasons hit the cap.
	EntriesTruncated bool `json:"entries_truncated,omitempty"`
}

// eventTimelineView is the whole answer.
type eventTimelineView struct {
	Groups []eventGroupView `json:"groups"`

	Namespace     string `json:"namespace,omitempty"`
	AllNamespaces bool   `json:"all_namespaces"`

	// Events are their own resource with their own RBAC, so a refusal narrows
	// the answer rather than failing it. False means nothing could be read at
	// all; Reason is the cluster's own words for why.
	EventsAvailable bool   `json:"events_available"`
	Reason          string `json:"reason,omitempty"`
	// UnreadableNamespaces is the other half of that, and the reason the flag
	// above is not enough: an all-namespaces read is many reads, and some of them
	// refusing is neither "available" nor "unavailable". Naming them is what
	// stops the page from quietly presenting a partial cluster as the whole one.
	UnreadableNamespaces []string `json:"unreadable_namespaces,omitempty"`

	// Truncated reports that the cluster had more to say than was read, so the
	// page can say the window is incomplete rather than implying it is the lot.
	//
	// It matters more than it looks. A page of events read in **key order** and
	// then sorted by time is the newest of *that page*, not the newest of the
	// cluster — so on a cluster big enough to truncate, "newest first" is a claim
	// about the sample rather than about the cluster, and the page has to say so
	// instead of quietly presenting a slice as the whole.
	Truncated bool `json:"truncated,omitempty"`
	// Scanned is how many events were actually read and folded, and Available is
	// the API server's own count of how many there were. Together they are the
	// honest "you are looking at 4,000 of 20,431" — Available is zero where the
	// server did not offer a count, which it does not do for a filtered list.
	Scanned   int   `json:"scanned"`
	Available int64 `json:"available,omitempty"`

	// Buffered marks the answer that came from the cluster's own watch-fed
	// buffer rather than from a list. It is worth saying out loud rather than
	// keeping as an implementation detail, because it is the difference between
	// "newest first" being a fact about the cluster and a claim about a sample —
	// and because an operator comparing two clusters deserves to know why one
	// page is complete and the other says it is partial.
	Buffered   bool       `json:"buffered,omitempty"`
	BufferedAt *time.Time `json:"buffered_at,omitempty"`
	// Groups before the group cap applied, so a truncated answer can say how
	// much it is standing in for.
	TotalGroups int `json:"total_groups"`
	// Window is the range the read was narrowed to, echoed back. It matters more
	// here than on other ranged surfaces because the *cluster* has the tighter
	// bound: Kubernetes drops events after an hour by default, so a 7-day window
	// shows what the cluster still has rather than seven days of history, and the
	// page has to be able to say so.
	Window string `json:"window,omitempty"`
}

// eventTypeFilter is the warnings filter. It is deliberately a *filter* and not
// the ordering: newest first stays the ordering, for the reason the describe
// drawer already has it — the question is what just happened, and an ordering
// that puts an hour-old warning above a thirty-second-old failure answers a
// different one.
func eventTypeFilter(c *gin.Context) (string, bool) {
	switch requested := strings.TrimSpace(c.Query("type")); requested {
	case "", "all":
		return "", true
	case "Warning", "Normal":
		return requested, true
	default:
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "type must be Warning, Normal, or left off for both",
		})
		return "", false
	}
}

/*
 * involvedObjectFilter narrows the timeline to one object — which is what the
 * pilot header's named alerts link into, since "why is this one crash-looping"
 * is the next question after a header raises it.
 *
 * The two components are **validated, never escaped**. They are assembled into a
 * `fieldSelector`, whose syntax is commas and equals signs, so a name carrying
 * either would become selector syntax rather than a value — and the API server
 * would answer about something nobody asked about. A Kubernetes object name is
 * an RFC 1123 subdomain and a Kind is a Go identifier, so validating is both
 * exact and cheap.
 */
func involvedObjectFilter(c *gin.Context) (kind, name string, ok bool) {
	kind = strings.TrimSpace(c.Query("kind"))
	name = strings.TrimSpace(c.Query("name"))

	if kind != "" && !validEventKind(kind) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "that is not a Kubernetes kind"})
		return "", "", false
	}
	if name != "" && !validEventName(name) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "that is not a Kubernetes object name"})
		return "", "", false
	}
	// A name without a kind is legitimate — two kinds rarely share a name, and
	// the alert links carry both anyway — but a kind without a name would narrow
	// to "every Pod", which the namespace scope already does better.
	if kind != "" && name == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "narrowing to a kind needs the name of the object as well",
		})
		return "", "", false
	}
	return kind, name, true
}

// validEventKind accepts a Kubernetes Kind: letters and digits, starting with a
// letter. Every built-in and every CRD-declared Kind is one.
func validEventKind(kind string) bool {
	if len(kind) > 63 {
		return false
	}
	for i, r := range kind {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return kind != ""
}

// validEventName accepts an RFC 1123 subdomain, which is what a Kubernetes
// object name is. It is checked by character set rather than by a regexp for the
// same reason the RBAC names are: the point is that nothing here can become
// selector syntax, and a character-set check states exactly that.
func validEventName(name string) bool {
	if name == "" || len(name) > 253 {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '-' || r == '.':
		default:
			return false
		}
	}
	return true
}

// listClusterEvents is the timeline.
func (s *server) listClusterEvents(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	scope, ok := s.resourceScope(c, grant)
	if !ok {
		return
	}
	typeFilter, ok := eventTypeFilter(c)
	if !ok {
		return
	}
	kind, name, ok := involvedObjectFilter(c)
	if !ok {
		return
	}
	// The window, resolved from this process's clock like every other ranged
	// surface — the browser never computes the boundary, so "the last fifteen
	// minutes" means the same instant here as in the audit trail. `all` is no
	// lower bound, which on this surface is the honest reading: the real ceiling
	// is the cluster's own event TTL, not anything KubeMG decides.
	window, ok := rangeSpan(c, 0)
	if !ok {
		return
	}
	var floor time.Time
	if window > 0 {
		floor = time.Now().Add(-window)
	}

	query := url.Values{}
	query.Set("limit", strconv.Itoa(eventPageSize))
	if selector := eventSelector(kind, name, typeFilter); selector != "" {
		query.Set("fieldSelector", selector)
	}

	view := eventTimelineView{
		Groups:        []eventGroupView{},
		Namespace:     scope.Namespace,
		AllNamespaces: scope.All,
		Window:        strings.TrimSpace(c.Query("range")),
	}

	collector := newEventCollector()

	/*
	 * The buffered path, where there is one.
	 *
	 * A warm ring holds every event the cluster has produced in the last hour, so
	 * "newest first" is a fact about the cluster rather than about a page of it —
	 * and it costs no API call at all. It is tried first and falls through to the
	 * paginated read whenever it cannot answer: a cluster whose watch has not
	 * synced yet, a server built without one, or a cluster the watch is being
	 * refused on. The fallback is not a nicety; it is what makes the first page
	 * view (which starts the watch) return something.
	 */
	if ring := s.eventRingFor(cluster); ring != nil {
		if synced, syncedAt, _ := ring.state(); synced {
			// The filter that now stands where the API server's authorizer stood
			// for this surface: the buffer is filled cluster-wide, and a scoped
			// caller sees exactly the namespaces their grant lists.
			for _, item := range ring.snapshot(grant.NamespaceList()) {
				if !matchesEventNarrowing(item, kind, name, scope) {
					continue
				}
				foldEvent(collector, item, typeFilter, floor)
			}

			view.EventsAvailable = true
			view.Buffered = true
			view.BufferedAt = &syncedAt
			finishTimeline(c, &view, collector)
			return
		}
	}

	budget := &eventBudget{scan: s.eventScanLimit}
	reads := 0
	refusals := 0

	fold := func(item eventObject) { foldEvent(collector, item, typeFilter, floor) }

	for _, path := range scope.paths(resourceListPath{"/api/v1", "events"}) {
		reads++
		// A fan-out shares one budget, so an all-namespaces read over a scoped
		// grant cannot cost twenty-five times what a single-namespace one does.
		// The namespaces that got nothing are as much a part of the honest answer
		// as the ones that did, which is what `exhausted` ends up saying.
		if budget.spent() {
			budget.exhausted = true
			break
		}

		reason, callOK := s.readEventPages(c, user, cluster, grant, path, query, budget, fold)
		if !callOK {
			// The call itself already answered the request — a transport failure
			// or a refusal from the bastion, neither of which is an RBAC answer
			// about events.
			return
		}
		if reason != "" {
			refusals++
			if view.Reason == "" {
				view.Reason = reason
			}
			// Which namespace refused is the useful half when only some did. A
			// cluster-wide read has no namespace to name, and there is only one of
			// them, so the flag below carries it instead.
			if namespace := namespaceOfPath(path); namespace != "" {
				view.UnreadableNamespaces = append(view.UnreadableNamespaces, namespace)
			}
		}
	}

	// Nothing could be read at all: that is the describe's contract — the answer
	// narrows and says why, rather than the page failing.
	if reads > 0 && refusals == reads {
		view.EventsAvailable = false
		if view.Reason == "" {
			view.Reason = "the cluster refused to list events"
		}
		c.JSON(http.StatusOK, view)
		return
	}

	view.EventsAvailable = true
	view.Scanned = budget.scanned
	view.Available = budget.remaining
	// Truncated means "this is part of the cluster", and it has exactly one
	// cause worth reporting: the walk stopped before the events did. The group
	// cap in finishTimeline is a second, independent one.
	view.Truncated = budget.exhausted

	finishTimeline(c, &view, collector)
}

/*
 * foldEvent applies the two narrowings that cannot ride on a field selector.
 *
 * The type *can* — and does, so a warnings-only read spends its budget on
 * warnings — but it is re-applied here because the buffered path has no selector
 * at all, and because a filter that silently does nothing on one of two code
 * paths is exactly the kind of difference nobody notices until it matters.
 */
func foldEvent(collector *eventCollector, item eventObject, typeFilter string, floor time.Time) {
	if typeFilter != "" && item.Type != typeFilter {
		return
	}
	// An event is in the window if its *last* firing is: something that started
	// an hour ago and fired ten seconds ago is exactly what "the last fifteen
	// minutes" is asking about. An event carrying no time at all is kept rather
	// than dropped — a row with no timestamp is a decode gap worth seeing, and
	// hiding it would hide the gap too.
	if !floor.IsZero() {
		last := eventAt(item.view())
		if !last.IsZero() && last.Before(floor) {
			return
		}
	}
	collector.add(item)
}

// matchesEventNarrowing is the object and namespace narrowing the *selector*
// does on the read path. The buffer holds the whole cluster, so the buffered
// path has to apply both by hand — and it has to agree exactly with what the
// selector would have matched, or the same question answers differently
// depending on whether a watch happens to be warm.
func matchesEventNarrowing(item eventObject, kind, name string, scope readScope) bool {
	if name != "" && item.InvolvedObject.Name != name {
		return false
	}
	if kind != "" && item.InvolvedObject.Kind != kind {
		return false
	}
	// An unscoped, all-namespaces read reads the whole buffer; anything else is
	// bounded by the namespaces the scope resolved to, which is the same set the
	// paginated path would have issued one call each for.
	if len(scope.Namespaces) == 0 {
		return true
	}
	namespace := item.InvolvedObject.Namespace
	if namespace == "" {
		namespace = item.Metadata.Namespace
	}
	return slices.Contains(scope.Namespaces, namespace)
}

// finishTimeline groups, bounds and writes the answer. Both paths end here so
// the group cap and the ordering cannot drift between them.
func finishTimeline(c *gin.Context, view *eventTimelineView, collector *eventCollector) {
	groups := collector.groups()
	view.TotalGroups = len(groups)
	if len(groups) > maxEventGroups {
		groups = groups[:maxEventGroups]
		view.Truncated = true
	}
	view.Groups = groups
	c.JSON(http.StatusOK, *view)
}

// eventSelector builds the field selector for the narrowings the API server can
// do for us. The type is included so a warnings-only read over a busy cluster
// spends its limit on warnings rather than filtering them out afterwards.
func eventSelector(kind, name, eventType string) string {
	parts := make([]string, 0, 3)
	if name != "" {
		parts = append(parts, "involvedObject.name="+name)
	}
	if kind != "" {
		parts = append(parts, "involvedObject.kind="+kind)
	}
	if eventType != "" {
		parts = append(parts, "type="+eventType)
	}
	return strings.Join(parts, ",")
}

// namespaceOfPath recovers the namespace from a list path the scope built, for
// naming which of several reads was refused. The path is one this file
// constructed rather than anything a caller sent.
func namespaceOfPath(path string) string {
	const marker = "/namespaces/"
	start := strings.Index(path, marker)
	if start < 0 {
		return ""
	}
	rest := path[start+len(marker):]
	end := strings.Index(rest, "/")
	if end < 0 {
		return ""
	}
	namespace, err := url.PathUnescape(rest[:end])
	if err != nil {
		return rest[:end]
	}
	return namespace
}

/* ---------------------------------------------------------------- reading --- */

/*
 * Reading an event list, in pages.
 *
 * `limit` alone is not a read — it is the first page of one, and the API server
 * is explicit that a page may come back **empty while more results exist**:
 * "setting a limit may return fewer than the requested amount of items (up to
 * zero items) in the event all requested objects are filtered out, and clients
 * should only use the presence of the continue field to determine whether more
 * results are available."
 *
 * That sentence is the whole reason this exists. A `fieldSelector` is applied
 * *after* the page is taken from etcd, so asking for one object's events on a
 * cluster with twenty thousand of them can legitimately answer "none" for an
 * object that has plenty — the first page simply contained other objects'
 * events. Reading a single page therefore produces a confident, wrong "nothing
 * was recorded", which is the worst answer a diagnostic surface can give: it
 * ends the investigation.
 *
 * So the read follows the continue token, under a budget it reports honestly.
 * The budget is what keeps this from becoming the thing it is guarding against —
 * a page view that walks a cluster's entire event history.
 */

// eventBudget is what one request may spend, shared across a fan-out.
type eventBudget struct {
	// scan is the ceiling on `scanned`, from the server's configuration. Zero
	// takes maxEventScan, so a zero-valued budget is bounded rather than
	// unlimited — the failure mode of the other default would be a page view
	// that walks a whole cluster.
	scan int
	// scanned is how many events have been read and folded so far.
	scanned int
	// requests is how many round trips have been spent.
	requests int
	// remaining is the API server's own count of what it had left after the
	// first page, where it offered one. It is the honest denominator for
	// "you are looking at part of this cluster", and it is only offered on an
	// unfiltered list — with a selector the server omits it, which is itself
	// worth knowing.
	remaining int64
	// exhausted marks a read that stopped because it ran out of budget rather
	// than because the cluster ran out of events.
	exhausted bool
}

func (b *eventBudget) spent() bool {
	scan := b.scan
	if scan <= 0 {
		scan = maxEventScan
	}
	return b.scanned >= scan || b.requests >= maxEventRequests
}

// eventPage is one decoded page.
type eventPage struct {
	Metadata struct {
		Continue           string `json:"continue"`
		RemainingItemCount *int64 `json:"remainingItemCount"`
	} `json:"metadata"`
	Items []eventObject `json:"items"`
}

/*
 * readEventPages walks one list path, handing every event to `fold` until the
 * cluster runs out or the budget does.
 *
 * The three outcomes are deliberately distinct. `ok=false` means the call itself
 * already answered the request — a transport failure or a refusal from the
 * bastion — and the caller must simply return. A non-2xx from the *cluster* is
 * returned as a reason, because events are their own resource with their own
 * RBAC and a refusal has to narrow the answer rather than fail it. Anything else
 * is a successful read, partial or not, and the budget says which.
 */
func (s *server) readEventPages(c *gin.Context, user *db.User, cluster *db.Cluster,
	grant db.UserClusterAccess, path string, query url.Values, budget *eventBudget,
	fold func(eventObject),
) (reason string, ok bool) {
	return walkEventPages(func(page url.Values) (int, []byte, bool) {
		resp, callOK := s.callResource(c, user, cluster, grant, path+"?"+page.Encode())
		if !callOK {
			return 0, nil, false
		}
		return resp.Status, resp.Body, true
	}, query, budget, fold)
}

// eventPageFetcher fetches one page of a list.
//
// It exists as a seam so the paging loop below can be exercised without a
// cluster. That is not testing for its own sake: the loop's contract — that an
// **empty page with a continue token is not the end of the collection** — is the
// single thing in this file most worth pinning, because getting it wrong
// produces a confident "nothing was recorded" rather than an error, and only on
// clusters large enough that nobody sees it before production.
//
// `ok=false` means the fetch already answered the HTTP request.
type eventPageFetcher func(query url.Values) (status int, body []byte, ok bool)

// walkEventPages is the paging loop.
func walkEventPages(fetch eventPageFetcher, query url.Values, budget *eventBudget,
	fold func(eventObject),
) (reason string, ok bool) {
	token := ""
	for {
		page := query
		if token != "" {
			// A continue token travels with an otherwise identical query, which is
			// why the base query is copied rather than mutated: the same query is
			// reused for the next namespace of a fan-out.
			page = cloneQuery(query)
			page.Set("continue", token)
		}

		budget.requests++
		status, body, fetchOK := fetch(page)
		if !fetchOK {
			return "", false
		}
		if status < 200 || status >= 300 {
			return kubeErrorMessage(body, status), true
		}

		var decoded eventPage
		if err := json.Unmarshal(body, &decoded); err != nil {
			return "the cluster returned an unreadable event list", true
		}

		// The server's own count of what is left, taken from the first page —
		// later pages report the remainder of the walk rather than of the
		// collection, and the first number is the one that means "this is how much
		// cluster there is". It is omitted entirely for a filtered list, which is
		// why the page cannot simply always show a denominator.
		if token == "" && decoded.Metadata.RemainingItemCount != nil {
			budget.remaining += *decoded.Metadata.RemainingItemCount + int64(len(decoded.Items))
		}

		for _, item := range decoded.Items {
			fold(item)
			budget.scanned++
		}

		// An empty continue is the only statement that the collection is finished.
		// An empty page is not — see the note on the fetcher above.
		token = decoded.Metadata.Continue
		if token == "" {
			return "", true
		}
		if budget.spent() {
			budget.exhausted = true
			return "", true
		}
	}
}

// cloneQuery copies a query so a continue token can be added to one page without
// changing the query the next namespace in a fan-out will send.
func cloneQuery(in url.Values) url.Values {
	out := make(url.Values, len(in)+1)
	for key, values := range in {
		out[key] = append([]string(nil), values...)
	}
	return out
}

/* --------------------------------------------------------------- grouping --- */

// eventCollector folds a flat event list into the timeline's two levels: one
// group per involved object, and one entry per reason inside it.
type eventCollector struct {
	// order is insertion order, which the final sort is stable over — so two
	// groups whose newest event carries the same timestamp keep the order the
	// cluster listed them in rather than a map's.
	order []string
	byKey map[string]*eventGroupView
}

func newEventCollector() *eventCollector {
	return &eventCollector{byKey: map[string]*eventGroupView{}}
}

// add folds one event in.
func (g *eventCollector) add(item eventObject) {
	view := item.view()
	object := eventObjectRef{
		Kind:      item.InvolvedObject.Kind,
		Name:      item.InvolvedObject.Name,
		Namespace: item.InvolvedObject.Namespace,
	}
	// An event whose involved object is missing its name is not attributable to
	// anything, so it is grouped under the event's own namespace and reason
	// rather than dropped — a cluster that emits one is saying something.
	if object.Name == "" {
		object.Name = item.Metadata.Name
		object.Namespace = item.Metadata.Namespace
	}
	if object.Namespace == "" {
		object.Namespace = item.Metadata.Namespace
	}

	key := object.Namespace + "/" + object.Kind + "/" + object.Name
	group, found := g.byKey[key]
	if !found {
		group = &eventGroupView{Key: key, Object: object, Entries: []eventEntryView{}}
		g.byKey[key] = group
		g.order = append(g.order, key)
	}

	group.Count += view.Count
	if view.Type == "Warning" {
		group.Warnings += view.Count
		// The worst type in the group wins: one warning among ten normals is
		// what somebody has to look at, and a group typed by its newest event
		// would hide it behind the next routine `Pulled`.
		group.Type = "Warning"
	} else if group.Type == "" {
		group.Type = view.Type
	}
	group.FirstSeen = earliest(group.FirstSeen, view.FirstSeen)
	group.LastSeen = latest(group.LastSeen, view.LastSeen)

	g.addEntry(group, view)
}

// addEntry folds one event into its reason's entry.
func (g *eventCollector) addEntry(group *eventGroupView, view eventView) {
	i := slices.IndexFunc(group.Entries, func(entry eventEntryView) bool {
		return entry.Reason == view.Reason && entry.Type == view.Type
	})
	if i < 0 {
		if len(group.Entries) >= maxGroupEntries {
			group.EntriesTruncated = true
			return
		}
		group.Entries = append(group.Entries, eventEntryView{
			Type:      view.Type,
			Reason:    view.Reason,
			Message:   view.Message,
			Count:     view.Count,
			Source:    view.Source,
			FirstSeen: view.FirstSeen,
			LastSeen:  view.LastSeen,
		})
		return
	}

	entry := &group.Entries[i]
	entry.Count += view.Count
	// The newest message wins: one reason's messages differ in their details, and
	// the most recent is the one describing the state the object is in now.
	if after(view.LastSeen, entry.LastSeen) {
		entry.Message = view.Message
		entry.Source = view.Source
	}
	entry.FirstSeen = earliest(entry.FirstSeen, view.FirstSeen)
	entry.LastSeen = latest(entry.LastSeen, view.LastSeen)
}

// groups renders the collected timeline, newest first at both levels.
func (g *eventCollector) groups() []eventGroupView {
	out := make([]eventGroupView, 0, len(g.order))
	for _, key := range g.order {
		group := g.byKey[key]

		sort.SliceStable(group.Entries, func(a, b int) bool {
			return after(group.Entries[a].LastSeen, group.Entries[b].LastSeen)
		})
		// The collapsed row shows the newest thing the cluster said about the
		// object, which is what the group is sorted by anyway.
		if len(group.Entries) > 0 {
			group.Reason = group.Entries[0].Reason
			group.Message = group.Entries[0].Message
		}
		out = append(out, *group)
	}

	// Newest first — the ordering, as opposed to the warnings filter. A warning
	// that stopped forty minutes ago above a failure from thirty seconds ago
	// answers a question nobody asked.
	sort.SliceStable(out, func(a, b int) bool {
		return after(out[a].LastSeen, out[b].LastSeen)
	})
	return out
}

func after(a, b *time.Time) bool {
	if a == nil {
		return false
	}
	if b == nil {
		return true
	}
	return a.After(*b)
}

func earliest(current, candidate *time.Time) *time.Time {
	if candidate == nil || candidate.IsZero() {
		return current
	}
	if current == nil || candidate.Before(*current) {
		return candidate
	}
	return current
}

func latest(current, candidate *time.Time) *time.Time {
	if candidate == nil || candidate.IsZero() {
		return current
	}
	if current == nil || candidate.After(*current) {
		return candidate
	}
	return current
}
