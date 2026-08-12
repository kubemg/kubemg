package api

import (
	"testing"
	"time"
)

/*
 * The buffer.
 *
 * Two things here deserve harder testing than the rest of this feature, and for
 * opposite reasons.
 *
 * `visibleTo` is the **security surface**. The ring is filled cluster-wide under
 * a synthetic cluster-admin identity, so for this one surface the thing standing
 * between a namespace-scoped operator and another team's events is a predicate
 * in this process rather than the cluster's own authorizer. That trade was made
 * deliberately, and the price of making it is that this function is tested as if
 * it were the authorizer — because for these rows, it is.
 *
 * The ring's deduplication and the line assembler are the **silent** ones. Both
 * fail by quietly losing or multiplying rows rather than by erroring, both fail
 * only under load, and both would make a page that looks entirely plausible
 * while being wrong.
 */

func eventWithUID(uid, namespace, name string, count int32, at time.Time) eventObject {
	item := eventObject{Type: "Warning", Reason: "BackOff", Message: "back-off", Count: count}
	item.Metadata.UID = uid
	item.Metadata.Namespace = namespace
	item.Metadata.Name = name + ".17abc"
	item.InvolvedObject.Kind = "Pod"
	item.InvolvedObject.Name = name
	item.InvolvedObject.Namespace = namespace
	item.LastTimestamp = &at
	item.FirstTimestamp = &at
	return item
}

/* ------------------------------------------------------------- the filter --- */

func TestVisibleToIsTheAuthorizerForBufferedRows(t *testing.T) {
	// An unscoped grant is what the rest of KubeMG already treats as "everything
	// the impersonated role allows", so the buffer withholds nothing from it.
	if !visibleTo("payments", nil) {
		t.Fatal("an unscoped grant must see every namespace the buffer holds")
	}
	if !visibleTo("payments", []string{}) {
		t.Fatal("an empty allow-list is an unscoped grant, not a deny-all")
	}

	allowed := []string{"team-a", "team-b"}
	if !visibleTo("team-a", allowed) {
		t.Fatal("a granted namespace has to be visible")
	}
	if visibleTo("payments", allowed) {
		t.Fatal("a namespace outside the grant must never be visible from the buffer")
	}
	// No prefix matching, no wildcards, nothing that could be argued about later:
	// a namespace is visible if it is *in* the list and not otherwise.
	if visibleTo("team-a-staging", allowed) {
		t.Fatal("visibility must be exact membership, not a prefix match")
	}
	if visibleTo("", allowed) {
		t.Fatal("an event with no namespace must not leak to a scoped grant")
	}
}

func TestRingSnapshotWithholdsOtherNamespaces(t *testing.T) {
	now := time.Now()
	ring := newEventRing()
	ring.put(eventWithUID("u1", "team-a", "api-0", 1, now))
	ring.put(eventWithUID("u2", "payments", "ledger-0", 1, now))

	scoped := ring.snapshot([]string{"team-a"})
	if len(scoped) != 1 {
		t.Fatalf("scoped snapshot returned %d events, want only the granted namespace", len(scoped))
	}
	if scoped[0].InvolvedObject.Namespace != "team-a" {
		t.Fatalf("scoped snapshot leaked %q", scoped[0].InvolvedObject.Namespace)
	}

	if len(ring.snapshot(nil)) != 2 {
		t.Fatal("an unscoped snapshot has to see the whole buffer")
	}
}

/* --------------------------------------------------------------- the ring --- */

// A repeating event updates one Event object's count rather than creating new
// ones, so a watch sends MODIFIED over and over for the same UID. Appending
// would hold forty copies of one row and count it forty times.
func TestRingReplacesByUID(t *testing.T) {
	now := time.Now()
	ring := newEventRing()
	ring.put(eventWithUID("u1", "shop", "api-0", 1, now))
	ring.put(eventWithUID("u1", "shop", "api-0", 12, now.Add(time.Minute)))

	held := ring.snapshot(nil)
	if len(held) != 1 {
		t.Fatalf("ring holds %d events, want one per UID", len(held))
	}
	if held[0].Count != 12 {
		t.Fatalf("count = %d, want the newest value for that UID", held[0].Count)
	}
}

// The cluster deletes an event when it ages out, and honouring that is what
// keeps the buffer from showing what the cluster has forgotten.
func TestRingDropsDeletedEvents(t *testing.T) {
	now := time.Now()
	ring := newEventRing()
	item := eventWithUID("u1", "shop", "api-0", 1, now)
	ring.put(item)
	ring.drop(item)

	if len(ring.snapshot(nil)) != 0 {
		t.Fatal("a deleted event must leave the buffer")
	}
}

// Both bounds exist because either alone is insufficient: age alone lets one bad
// minute cost unbounded memory, and count alone shows events the cluster has
// already discarded.
func TestRingDropsEventsPastTheAgeBound(t *testing.T) {
	ring := newEventRing()
	ring.put(eventWithUID("old", "shop", "api-0", 1, time.Now().Add(-2*eventBufferAge)))
	ring.put(eventWithUID("new", "shop", "api-1", 1, time.Now()))

	held := ring.snapshot(nil)
	if len(held) != 1 {
		t.Fatalf("ring holds %d events, want the aged-out one dropped", len(held))
	}
	if held[0].InvolvedObject.Name != "api-1" {
		t.Fatalf("kept %q, want the recent event", held[0].InvolvedObject.Name)
	}
}

func TestRingBoundsItsSize(t *testing.T) {
	now := time.Now()
	ring := newEventRing()
	for i := 0; i < eventBufferSize+50; i++ {
		ring.put(eventWithUID(itoaUID(i), "shop", "api", 1, now))
	}

	if held := len(ring.snapshot(nil)); held > eventBufferSize {
		t.Fatalf("ring holds %d events, want at most %d", held, eventBufferSize)
	}
}

func itoaUID(i int) string { return "uid-" + time.Duration(i).String() }

// A re-sync replaces the buffer rather than merging into it: the list is the
// cluster's current truth, and anything held that is not in it is gone.
func TestRingResetClearsForAFreshList(t *testing.T) {
	ring := newEventRing()
	ring.put(eventWithUID("u1", "shop", "api-0", 1, time.Now()))
	ring.reset()

	if len(ring.snapshot(nil)) != 0 {
		t.Fatal("a reset ring has to be empty, or a re-sync merges stale rows in")
	}
}

// A cold ring must not be served: the timeline falls back to the paginated read
// until a list has completed, which is what makes the first page view work.
func TestRingReportsWhetherItIsWorthReading(t *testing.T) {
	ring := newEventRing()
	if synced, _, _ := ring.state(); synced {
		t.Fatal("a fresh ring must not report itself as synced")
	}

	ring.markSynced()
	synced, at, err := ring.state()
	if !synced || at.IsZero() || err != nil {
		t.Fatalf("state = %v/%v/%v, want a synced ring", synced, at, err)
	}

	ring.markFailed(errFake)
	if synced, _, got := ring.state(); synced || got == nil {
		t.Fatal("a failed sync has to stop the ring being served")
	}
}

var errFake = &fakeErr{}

type fakeErr struct{}

func (*fakeErr) Error() string { return "the cluster refused the watch" }

/* ---------------------------------------------------- the stream assembler --- */

/*
 * A watch is newline-delimited JSON and the tunnel chops it at arbitrary byte
 * boundaries, so a frame routinely arrives split across two chunks. Parsing each
 * chunk on its own drops exactly the events that straddle a boundary — silently,
 * and only under the load that makes chunks fill up. It is the kind of bug that
 * shows as "the timeline sometimes misses things".
 */
func TestLineAssemblerRejoinsASplitFrame(t *testing.T) {
	assembler := newLineAssembler()

	if lines := assembler.push([]byte(`{"type":"ADDED","obj`)); len(lines) != 0 {
		t.Fatalf("half a line yielded %d lines, want none", len(lines))
	}
	lines := assembler.push([]byte("ect\":{}}\n"))
	if len(lines) != 1 {
		t.Fatalf("lines = %d, want the rejoined frame", len(lines))
	}
	if string(lines[0]) != `{"type":"ADDED","object":{}}` {
		t.Fatalf("line = %q, want the two halves joined", lines[0])
	}
}

func TestLineAssemblerSplitsSeveralFramesInOneChunk(t *testing.T) {
	assembler := newLineAssembler()
	lines := assembler.push([]byte("{\"type\":\"ADDED\"}\n{\"type\":\"MODIFIED\"}\n"))

	if len(lines) != 2 {
		t.Fatalf("lines = %d, want both frames", len(lines))
	}
	if string(lines[1]) != `{"type":"MODIFIED"}` {
		t.Fatalf("second line = %q", lines[1])
	}
}

/* ------------------------------------------------------------ watch frames --- */

func TestApplyWatchLineFoldsTheFrameKinds(t *testing.T) {
	ring := newEventRing()

	added := `{"type":"ADDED","object":{"metadata":{"uid":"u1","namespace":"shop"},
		"involvedObject":{"kind":"Pod","name":"api-0","namespace":"shop"},
		"type":"Warning","reason":"BackOff","count":1}}`
	if err := applyWatchLine(ring, []byte(added)); err != nil {
		t.Fatalf("ADDED: %v", err)
	}
	if len(ring.snapshot(nil)) != 1 {
		t.Fatal("ADDED has to file the event")
	}

	// MODIFIED is the common case rather than the exception, which is exactly why
	// the ring is keyed by UID.
	modified := `{"type":"MODIFIED","object":{"metadata":{"uid":"u1","namespace":"shop"},
		"involvedObject":{"kind":"Pod","name":"api-0","namespace":"shop"},
		"type":"Warning","reason":"BackOff","count":9}}`
	if err := applyWatchLine(ring, []byte(modified)); err != nil {
		t.Fatalf("MODIFIED: %v", err)
	}
	held := ring.snapshot(nil)
	if len(held) != 1 || held[0].Count != 9 {
		t.Fatalf("held = %+v, want one event at the updated count", held)
	}

	deleted := `{"type":"DELETED","object":{"metadata":{"uid":"u1"}}}`
	if err := applyWatchLine(ring, []byte(deleted)); err != nil {
		t.Fatalf("DELETED: %v", err)
	}
	if len(ring.snapshot(nil)) != 0 {
		t.Fatal("DELETED has to remove the event")
	}

	// A bookmark carries position and no object; filing it would put an empty
	// row in the buffer.
	if err := applyWatchLine(ring, []byte(`{"type":"BOOKMARK","object":{}}`)); err != nil {
		t.Fatalf("BOOKMARK: %v", err)
	}
	if len(ring.snapshot(nil)) != 0 {
		t.Fatal("a bookmark must not become a row")
	}

	// Blank keep-alive lines are normal on a quiet watch.
	if err := applyWatchLine(ring, []byte("  ")); err != nil {
		t.Fatalf("blank line: %v", err)
	}
}

/* ------------------------------------------------------- the two code paths --- */

/*
 * The buffered path applies the object and namespace narrowing by hand, because
 * the buffer holds the whole cluster and there is no field selector to do it.
 * It has to agree exactly with what the selector would have matched — otherwise
 * the same question answers differently depending on whether a watch happens to
 * be warm, which is the worst kind of inconsistency to debug.
 */
func TestBufferedNarrowingMatchesTheSelector(t *testing.T) {
	item := eventWithUID("u1", "shop", "api-0", 1, time.Now())
	all := readScope{All: true}

	if !matchesEventNarrowing(item, "", "", all) {
		t.Fatal("an unnarrowed cluster-wide read has to match everything")
	}
	if !matchesEventNarrowing(item, "Pod", "api-0", all) {
		t.Fatal("the object's own kind and name have to match")
	}
	if matchesEventNarrowing(item, "Pod", "api-1", all) {
		t.Fatal("a different name must not match")
	}
	if matchesEventNarrowing(item, "Deployment", "api-0", all) {
		t.Fatal("a different kind must not match")
	}

	// A scope naming namespaces is the same set the paginated path would have
	// issued one call each for.
	if !matchesEventNarrowing(item, "", "", readScope{Namespaces: []string{"shop"}}) {
		t.Fatal("an event in a scoped namespace has to match")
	}
	if matchesEventNarrowing(item, "", "", readScope{Namespaces: []string{"other"}}) {
		t.Fatal("an event outside the scope must not match")
	}
}
