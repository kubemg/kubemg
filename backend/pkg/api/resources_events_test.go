package api

import (
	"net/http"
	"net/url"
	"testing"
	"time"
)

/*
 * The timeline.
 *
 * The read is the describe's read with the object selector taken off, so what is
 * worth pinning here is the part that is new: the **grouping**. It is what turns
 * a flat list into something readable, and every way it can be subtly wrong is a
 * way the page lies quietly — a count that reports rows rather than firings, a
 * group typed by its newest event so one Warning hides behind the next routine
 * `Pulled`, an ordering that puts an old warning above a fresh failure.
 *
 * The selector validation is pinned for a different reason: those two components
 * are assembled into a `fieldSelector`, whose syntax is commas and equals signs,
 * so a name carrying either would make the API server answer about something
 * nobody asked about.
 */

// at builds a timestamp pointer, since every event field is one.
func at(minute int) *time.Time {
	moment := time.Date(2026, 8, 12, 9, minute, 0, 0, time.UTC)
	return &moment
}

// firing is one Event object as the cluster would list it.
func firing(kind, name, namespace, eventType, reason, message string,
	count int32, first, last *time.Time,
) eventObject {
	item := eventObject{
		Type:           eventType,
		Reason:         reason,
		Message:        message,
		Count:          count,
		FirstTimestamp: first,
		LastTimestamp:  last,
	}
	item.InvolvedObject.Kind = kind
	item.InvolvedObject.Name = name
	item.InvolvedObject.Namespace = namespace
	return item
}

// One object, several Event objects for the same reason. Kubernetes folds
// repeats into one Event with a count, but only per reporting component and only
// until the event ages out — so the same reason routinely arrives as several
// objects, and printing them separately is how a timeline turns into forty lines
// of `BackOff`.
func TestEventGroupingFoldsRepeatsOfOneReason(t *testing.T) {
	collector := newEventCollector()
	collector.add(firing("Pod", "api-0", "shop", "Warning", "BackOff",
		"Back-off restarting failed container api", 12, at(1), at(20)))
	collector.add(firing("Pod", "api-0", "shop", "Warning", "BackOff",
		"Back-off restarting failed container sidecar", 29, at(5), at(31)))

	groups := collector.groups()
	if len(groups) != 1 {
		t.Fatalf("groups = %d, want one per involved object", len(groups))
	}
	group := groups[0]

	if len(group.Entries) != 1 {
		t.Fatalf("entries = %+v, want one per reason", group.Entries)
	}
	// The count is firings, not rows. A row saying 2 for something the cluster
	// said 41 times is the whole failure this grouping exists to avoid.
	if group.Count != 41 || group.Entries[0].Count != 41 {
		t.Fatalf("count = %d/%d, want 41 firings", group.Count, group.Entries[0].Count)
	}
	if group.Warnings != 41 {
		t.Fatalf("warnings = %d, want every warning firing counted", group.Warnings)
	}
	// The window spans every Event object folded in, not just the last one.
	if group.FirstSeen == nil || !group.FirstSeen.Equal(*at(1)) {
		t.Fatalf("first_seen = %v, want the earliest across both", group.FirstSeen)
	}
	if group.LastSeen == nil || !group.LastSeen.Equal(*at(31)) {
		t.Fatalf("last_seen = %v, want the latest across both", group.LastSeen)
	}
	// One reason's messages differ in their details, and the most recent is the
	// one describing the state the object is in now.
	if group.Entries[0].Message != "Back-off restarting failed container sidecar" {
		t.Fatalf("message = %q, want the newest one", group.Entries[0].Message)
	}
}

// Rows collapse by involved object: a failing Deployment produces events from
// the deployment controller, the replica set and every pod, which as rows is
// forty lines describing one problem.
func TestEventGroupingCollapsesByInvolvedObject(t *testing.T) {
	collector := newEventCollector()
	collector.add(firing("Pod", "api-0", "shop", "Normal", "Pulled",
		"Container image already present", 1, at(2), at(2)))
	collector.add(firing("Pod", "api-0", "shop", "Warning", "Failed",
		"Error: ImagePullBackOff", 3, at(3), at(9)))
	collector.add(firing("Pod", "web-1", "shop", "Normal", "Scheduled",
		"Successfully assigned", 1, at(4), at(4)))

	groups := collector.groups()
	if len(groups) != 2 {
		t.Fatalf("groups = %d, want one per object", len(groups))
	}

	// Newest first is the ordering: api-0's last event is at :09, web-1's at :04.
	if groups[0].Object.Name != "api-0" {
		t.Fatalf("first group = %q, want the object with the newest event", groups[0].Object.Name)
	}
	if len(groups[0].Entries) != 2 {
		t.Fatalf("entries = %+v, want one per reason", groups[0].Entries)
	}
	// Within a group the newest reason leads, and it is what the collapsed row
	// shows — the most recent thing the cluster said about the object.
	if groups[0].Reason != "Failed" || groups[0].Message != "Error: ImagePullBackOff" {
		t.Fatalf("collapsed row = %s/%s, want the newest entry",
			groups[0].Reason, groups[0].Message)
	}
}

// An object with one Warning among ten Normals is a warning: that is the one
// somebody has to look at, and a group typed by its newest event would hide it
// behind the next routine `Pulled`.
func TestEventGroupTakesTheWorstType(t *testing.T) {
	collector := newEventCollector()
	collector.add(firing("Pod", "api-0", "shop", "Warning", "Unhealthy",
		"Readiness probe failed", 4, at(1), at(2)))
	// Newer, and Normal.
	collector.add(firing("Pod", "api-0", "shop", "Normal", "Pulled",
		"Container image already present", 1, at(8), at(8)))

	groups := collector.groups()
	if groups[0].Type != "Warning" {
		t.Fatalf("type = %q, want the worst type in the group", groups[0].Type)
	}
	if groups[0].Warnings != 4 {
		t.Fatalf("warnings = %d, want only the warning firings", groups[0].Warnings)
	}
	if groups[0].Count != 5 {
		t.Fatalf("count = %d, want every firing", groups[0].Count)
	}
}

// An event written through `events.k8s.io` arrives on the core list with
// `lastTimestamp` and `count` empty and its time in `eventTime`/`series`.
// Grouping runs on top of the decode, so a cluster writing the new shape has to
// group and sort exactly as one writing the old one — otherwise the newest
// events are the ones that sort last.
func TestEventGroupingReadsTheNewEventShape(t *testing.T) {
	moment := at(30)
	modern := eventObject{
		Type:      "Warning",
		Reason:    "FailedScheduling",
		Message:   "0/3 nodes are available",
		EventTime: moment,
		Series: &struct {
			Count            int32      `json:"count"`
			LastObservedTime *time.Time `json:"lastObservedTime"`
		}{Count: 7, LastObservedTime: moment},
	}
	modern.InvolvedObject.Kind = "Pod"
	modern.InvolvedObject.Name = "queue-0"
	modern.InvolvedObject.Namespace = "shop"

	collector := newEventCollector()
	collector.add(firing("Pod", "api-0", "shop", "Normal", "Pulled", "cached", 1, at(5), at(5)))
	collector.add(modern)

	groups := collector.groups()
	if len(groups) != 2 {
		t.Fatalf("groups = %d", len(groups))
	}
	// The series count is the firing count, and without reading it this row
	// would say 1 for something that happened seven times.
	if groups[0].Object.Name != "queue-0" {
		t.Fatalf("first group = %q, want the newest — the series event", groups[0].Object.Name)
	}
	if groups[0].Count != 7 {
		t.Fatalf("count = %d, want the series count", groups[0].Count)
	}
	if groups[0].LastSeen == nil {
		t.Fatal("expected the series time to survive grouping; a row with no time sorts last")
	}
}

// The distinct reasons in one group are capped, but the cap has to announce
// itself — a group silently showing 20 of 30 reasons reads as though that were
// all of them.
func TestEventGroupBoundsItsEntries(t *testing.T) {
	collector := newEventCollector()
	for i := 0; i < maxGroupEntries+4; i++ {
		collector.add(firing("Pod", "api-0", "shop", "Normal",
			"Reason"+string(rune('A'+i)), "something", 1, at(i), at(i)))
	}

	group := collector.groups()[0]
	if len(group.Entries) != maxGroupEntries {
		t.Fatalf("entries = %d, want the cap of %d", len(group.Entries), maxGroupEntries)
	}
	if !group.EntriesTruncated {
		t.Fatal("expected a capped group to say so")
	}
}

/* ------------------------------------------------------------- the paging --- */

/*
 * The API server pages a list in **key order** and applies a `fieldSelector`
 * afterwards, so it is explicit that a page may come back empty while matches
 * remain: "up to zero items ... clients should only use the presence of the
 * continue field to determine whether more results are available."
 *
 * Reading one page therefore answers "no events" for an object that has plenty,
 * on any cluster busy enough that the first page belongs to somebody else. That
 * is the worst answer a diagnostic surface can give — it is confident, it is
 * wrong, and it ends the investigation. These tests exist because the failure is
 * invisible on any cluster small enough to fit in one page, which is every
 * cluster anybody develops against.
 */

// pager replays a fixed set of pages, recording what it was asked for.
func pager(pages ...string) (eventPageFetcher, *[]string) {
	asked := &[]string{}
	i := 0
	return func(query url.Values) (int, []byte, bool) {
		*asked = append(*asked, query.Get("continue"))
		if i >= len(pages) {
			return 200, []byte(`{"items":[]}`), true
		}
		body := pages[i]
		i++
		return 200, []byte(body), true
	}, asked
}

func TestWalkEventPagesFollowsAnEmptyPage(t *testing.T) {
	// The shape that breaks a single-page read: the object's events exist, but
	// they are on the second page because the first belongs to other objects.
	fetch, asked := pager(
		`{"metadata":{"continue":"token-1"},"items":[]}`,
		`{"metadata":{"continue":""},"items":[
			{"type":"Warning","reason":"BackOff","message":"back-off","count":7,
			 "involvedObject":{"kind":"Pod","name":"api-0","namespace":"shop"}}]}`,
	)

	seen := 0
	budget := &eventBudget{}
	reason, ok := walkEventPages(fetch, url.Values{}, budget, func(eventObject) { seen++ })

	if !ok || reason != "" {
		t.Fatalf("walk = %q/%v, want a clean read", reason, ok)
	}
	if seen != 1 {
		t.Fatalf("folded %d events; an empty first page must not end the read", seen)
	}
	if len(*asked) != 2 || (*asked)[0] != "" || (*asked)[1] != "token-1" {
		t.Fatalf("continue tokens sent = %v, want the second page requested", *asked)
	}
}

// An empty continue is the only statement that the collection is finished.
func TestWalkEventPagesStopsOnEmptyContinue(t *testing.T) {
	fetch, asked := pager(`{"metadata":{"continue":""},"items":[
		{"type":"Normal","reason":"Pulled","involvedObject":{"kind":"Pod","name":"a"}}]}`)

	budget := &eventBudget{}
	if _, ok := walkEventPages(fetch, url.Values{}, budget, func(eventObject) {}); !ok {
		t.Fatal("expected a clean read")
	}
	if len(*asked) != 1 {
		t.Fatalf("requests = %d, want one — an empty continue ends the collection", len(*asked))
	}
	if budget.exhausted {
		t.Fatal("a completed collection is not an exhausted budget")
	}
}

// The budget is what keeps this from becoming the thing it guards against: a
// page view that walks a cluster's whole event history.
func TestWalkEventPagesStopsAtTheBudget(t *testing.T) {
	// A cluster that always has more to give.
	endless := func(query url.Values) (int, []byte, bool) {
		return 200, []byte(`{"metadata":{"continue":"more"},"items":[
			{"type":"Normal","reason":"Pulled","involvedObject":{"kind":"Pod","name":"a"}}]}`), true
	}

	budget := &eventBudget{scan: 3}
	if _, ok := walkEventPages(endless, url.Values{}, budget, func(eventObject) {}); !ok {
		t.Fatal("expected a clean read")
	}
	if budget.scanned != 3 {
		t.Fatalf("scanned = %d, want the configured budget of 3", budget.scanned)
	}
	// And it has to *say* it stopped early, or the page presents a slice of the
	// cluster as the whole of it.
	if !budget.exhausted {
		t.Fatal("expected a budget-stopped walk to mark itself exhausted")
	}
}

// A zero-valued budget is bounded rather than unlimited: the other default
// would make a forgotten field into a walk of the whole cluster.
func TestEventBudgetDefaultsToBounded(t *testing.T) {
	budget := &eventBudget{scanned: maxEventScan}
	if !budget.spent() {
		t.Fatal("a zero scan limit must fall back to maxEventScan, not to unlimited")
	}
	if (&eventBudget{requests: maxEventRequests}).spent() != true {
		t.Fatal("the request ceiling has to bound the walk on its own")
	}
}

// `remainingItemCount` is the server's own denominator, and it is taken from the
// first page only — later pages report the remainder of the walk rather than of
// the collection.
func TestWalkEventPagesRecordsTheClusterTotal(t *testing.T) {
	fetch, _ := pager(
		`{"metadata":{"continue":"t","remainingItemCount":19998},"items":[
			{"type":"Normal","reason":"A","involvedObject":{"kind":"Pod","name":"a"}},
			{"type":"Normal","reason":"B","involvedObject":{"kind":"Pod","name":"b"}}]}`,
		`{"metadata":{"continue":"","remainingItemCount":5},"items":[]}`,
	)

	budget := &eventBudget{}
	walkEventPages(fetch, url.Values{}, budget, func(eventObject) {})

	if budget.remaining != 20000 {
		t.Fatalf("remaining = %d, want 20000 from the first page alone", budget.remaining)
	}
}

// A refusal is the cluster's own answer about events, not a failed page.
func TestWalkEventPagesReportsARefusal(t *testing.T) {
	refuse := func(url.Values) (int, []byte, bool) {
		return 403, []byte(`{"message":"events is forbidden"}`), true
	}

	reason, ok := walkEventPages(refuse, url.Values{}, &eventBudget{}, func(eventObject) {})
	if !ok {
		t.Fatal("a cluster refusal is an answer, not a handled failure")
	}
	if reason != "events is forbidden" {
		t.Fatalf("reason = %q, want the cluster's own words", reason)
	}
}

// The continue token must not leak into the query the next namespace of a
// fan-out sends.
func TestCloneQueryLeavesTheOriginalAlone(t *testing.T) {
	base := url.Values{}
	base.Set("limit", "500")

	page := cloneQuery(base)
	page.Set("continue", "token")

	if base.Get("continue") != "" {
		t.Fatal("the base query must not carry another page's continue token")
	}
	if page.Get("limit") != "500" {
		t.Fatalf("the copy lost the base query: %v", page)
	}
}

/* ---------------------------------------------------------- the selector --- */

// The two narrowing components go into a fieldSelector, whose syntax is commas
// and equals signs. A name carrying either would become selector syntax rather
// than a value, and the API server would answer about something nobody asked
// about.
func TestInvolvedObjectFilterRefusesSelectorSyntax(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	token := env.tokenFor(t, admin)
	base := "/api/v1/clusters/" + itoa(cluster.ID) + "/resources/events?namespace=shop"

	for name, query := range map[string]string{
		"comma in the name":  "&kind=Pod&name=api-0,type%3DNormal",
		"equals in the name": "&kind=Pod&name=api%3D0",
		"upper-case name":    "&kind=Pod&name=API-0",
		"kind with a comma":  "&kind=Pod,type%3DNormal&name=api-0",
		// A kind with no name would narrow to "every Pod", which the namespace
		// scope already does, and better.
		"kind without a name": "&kind=Pod",
	} {
		t.Run(name, func(t *testing.T) {
			rec := env.do(t, http.MethodGet, base+query, token, nil)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected status %d for %s, got %d (%s)",
					http.StatusBadRequest, name, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestEventTimelineRefusesUnknownType(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	token := env.tokenFor(t, admin)

	rec := env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/events?namespace=shop&type=Bad",
		token, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
}

func TestEventSelectorCombinesTheNarrowings(t *testing.T) {
	if got := eventSelector("", "", ""); got != "" {
		t.Fatalf("selector = %q, want none for an unnarrowed read", got)
	}
	// The type rides on the selector so a warnings-only read over a busy cluster
	// spends its limit on warnings rather than filtering them out afterwards.
	if got := eventSelector("Pod", "api-0", "Warning"); got !=
		"involvedObject.name=api-0,involvedObject.kind=Pod,type=Warning" {
		t.Fatalf("selector = %q", got)
	}
}

// The timeline is namespaced like every other list, so a scoped grant asking
// about somebody else's namespace is refused before anything reaches the cluster.
func TestEventTimelineHonoursTheGrant(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("scoped", "secret123", "user")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	env.store.grant(user.ID, cluster.ID, "view", []string{"team-a"})
	token := env.tokenFor(t, user)

	rec := env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/events?namespace=team-b", token, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusForbidden, rec.Code, rec.Body.String())
	}
}

func TestEventTimelineRefusesDirectClusters(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")
	cluster := env.store.addCluster("legacy", "dev") // direct mode
	token := env.tokenFor(t, admin)

	rec := env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/events?namespace=default", token, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusConflict, rec.Code, rec.Body.String())
	}
}

// Which namespace refused is the useful half when only some of a fan-out did,
// and it is recovered from the path this file built rather than from anything a
// caller sent.
func TestNamespaceOfPath(t *testing.T) {
	if got := namespaceOfPath("/api/v1/namespaces/team-a/events"); got != "team-a" {
		t.Fatalf("namespaceOfPath = %q, want team-a", got)
	}
	if got := namespaceOfPath("/api/v1/events"); got != "" {
		t.Fatalf("namespaceOfPath = %q, want none for a cluster-wide read", got)
	}
}
