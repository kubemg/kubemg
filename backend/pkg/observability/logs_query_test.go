package observability

import (
	"strings"
	"testing"
	"time"
)

/*
 * The two log backends share nothing but the shape that comes out, so each
 * query builder is pinned on its own. What matters in both: the selector is
 * built from validated names, and the operator's free-text filter — the one
 * value that legitimately contains arbitrary characters — is carried as a
 * quoted literal rather than as syntax.
 */

func TestVictoriaLogsQueryNarrowsToTheSelector(t *testing.T) {
	query := victoriaLogsQuery(logSelector{
		namespaces: []string{"shop"},
		pod:        "checkout-7d9f",
		container:  "server",
	}, "")

	for _, want := range []string{
		`kubernetes.pod_namespace:"shop"`,
		`kubernetes.pod_name:"checkout-7d9f"`,
		`kubernetes.container_name:"server"`,
	} {
		if !strings.Contains(query, want) {
			t.Fatalf("query = %q, want it to contain %q", query, want)
		}
	}
}

// A scoped caller searching without naming a namespace gets their own
// namespaces, never everything.
func TestVictoriaLogsQueryScopesToGrantedNamespaces(t *testing.T) {
	query := victoriaLogsQuery(logSelector{namespaces: []string{"team-a", "team-b"}}, "")

	if !strings.Contains(query, `kubernetes.pod_namespace:("team-a" OR "team-b")`) {
		t.Fatalf("query = %q, want both granted namespaces alternated", query)
	}
	if query == "*" {
		t.Fatal("a scoped search must never become a match-everything query")
	}
}

func TestLokiQueryAlwaysCarriesANonEmptySelector(t *testing.T) {
	// LogQL refuses a selector that matches every stream, so an unscoped search
	// with no namespace still has to pin something.
	query := lokiQuery(logSelector{}, "")
	if !strings.HasPrefix(query, "{") || strings.Contains(query, "{}") {
		t.Fatalf("query = %q, want a non-empty stream selector", query)
	}

	scoped := lokiQuery(logSelector{namespaces: []string{"shop"}, pod: "checkout"}, "")
	if !strings.Contains(scoped, `namespace="shop"`) || !strings.Contains(scoped, `pod="checkout"`) {
		t.Fatalf("query = %q, want the namespace and pod pinned", scoped)
	}
}

// The filter is the one caller-supplied value that is not a Kubernetes name, so
// it is the one place quoting is used instead of validation — and it has to hold
// for the characters that would otherwise end the literal.
func TestFilterIsCarriedAsAValueNotAsSyntax(t *testing.T) {
	hostile := `" or true or "`

	loki := lokiQuery(logSelector{namespaces: []string{"shop"}}, hostile)
	if strings.Contains(loki, `|= "" or true or ""`) {
		t.Fatalf("query = %q, the filter escaped its literal", loki)
	}
	if !strings.Contains(loki, `\"`) {
		t.Fatalf("query = %q, want the quotes escaped", loki)
	}

	victoria := victoriaLogsQuery(logSelector{namespaces: []string{"shop"}}, hostile)
	if !strings.Contains(victoria, `\"`) {
		t.Fatalf("query = %q, want the quotes escaped", victoria)
	}
}

// A backslash has to be escaped before a quote is, or the escaping re-escapes
// its own output and the literal ends one character early.
func TestQuotePhraseEscapesBackslashesFirst(t *testing.T) {
	if got, want := quotePhrase(`a\"b`), `"a\\\"b"`; got != want {
		t.Fatalf("quotePhrase = %s, want %s", got, want)
	}
}

func TestDecodeVictoriaLogsReadsNDJSON(t *testing.T) {
	body := []byte(`{"_time":"2026-07-28T09:00:00Z","_msg":"started","kubernetes.pod_name":"checkout","kubernetes.pod_namespace":"shop"}
{"_time":"2026-07-28T09:00:01Z","_msg":"ready","kubernetes.pod_name":"checkout","kubernetes.pod_namespace":"shop"}

{ this line is not json at all
{"_time":"2026-07-28T09:00:02Z","_msg":"serving","kubernetes.pod_name":"checkout","kubernetes.pod_namespace":"shop"}`)

	entries, err := decodeVictoriaLogs(body, 100)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The unreadable line is dropped; it does not invalidate the page.
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(entries))
	}
	if entries[0].Message != "started" || entries[0].Pod != "checkout" || entries[0].Namespace != "shop" {
		t.Fatalf("entry = %+v", entries[0])
	}
	if entries[0].At.IsZero() {
		t.Fatal("expected _time to be parsed")
	}
}

// A shipper that writes the Kubernetes metadata as a nested object rather than
// flattened keys describes the same thing, and reading only one shape shows a
// log page with no pod names at all.
func TestDecodeVictoriaLogsReadsNestedMetadata(t *testing.T) {
	body := []byte(`{"_time":"2026-07-28T09:00:00Z","_msg":"hi","kubernetes":{"pod_name":"api","pod_namespace":"shop"}}`)

	entries, err := decodeVictoriaLogs(body, 10)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(entries) != 1 || entries[0].Pod != "api" || entries[0].Namespace != "shop" {
		t.Fatalf("entries = %+v", entries)
	}
}

func TestDecodeLokiReadsStreams(t *testing.T) {
	body := []byte(`{"status":"success","data":{"result":[
		{"stream":{"namespace":"shop","pod":"checkout","container":"server"},
		 "values":[["1785315600000000000","boom"],["1785315601000000000","recovered"]]}
	]}}`)

	entries, err := decodeLoki(body, 100)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	if entries[0].Message != "boom" || entries[0].Pod != "checkout" {
		t.Fatalf("entry = %+v", entries[0])
	}
	if entries[0].At.IsZero() {
		t.Fatal("expected the nanosecond timestamp to be parsed")
	}
}

// The limit bounds what comes back regardless of how many streams the backend
// spread the lines across.
func TestDecodeLokiRespectsTheLimit(t *testing.T) {
	body := []byte(`{"data":{"result":[
		{"stream":{"pod":"a"},"values":[["1","1"],["2","2"],["3","3"]]},
		{"stream":{"pod":"b"},"values":[["4","4"],["5","5"]]}
	]}}`)

	entries, err := decodeLoki(body, 4)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(entries) != 4 {
		t.Fatalf("entries = %d, want the limit respected", len(entries))
	}
}

func TestLongLinesAreClamped(t *testing.T) {
	message, truncated := clampLine(strings.Repeat("x", maxLogLine+1))
	if !truncated || len(message) != maxLogLine {
		t.Fatalf("clamped to %d bytes (truncated=%v)", len(message), truncated)
	}

	if _, truncated := clampLine("short"); truncated {
		t.Fatal("a short line must not be marked truncated")
	}
}

func TestParseLogTimeReadsEveryShipperShape(t *testing.T) {
	for _, value := range []string{
		"2026-07-28T09:00:00Z",
		"2026-07-28T09:00:00.123456789Z",
		"1785315600000000000",
	} {
		if _, ok := parseLogTime(value); !ok {
			t.Fatalf("expected %q to parse", value)
		}
	}
	if _, ok := parseLogTime("not a time"); ok {
		t.Fatal("expected an unreadable timestamp to be refused")
	}
}

func TestLogSelectorRefusesAPodOutsideTheGrant(t *testing.T) {
	_, err := buildLogSelector(Scope{Namespaces: []string{"team-a"}}, LogRequest{
		Namespace: "team-c",
		Pod:       "checkout",
	})
	if err == nil {
		t.Fatal("expected a namespace outside the grant to be refused")
	}

	if _, err := buildLogSelector(Scope{}, LogRequest{Pod: `a"b`}); err == nil {
		t.Fatal("expected a pod name that could become syntax to be refused")
	}
}

func TestLokiPathAsksForTheNewestLinesFirst(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	path := lokiPath(`{namespace="shop"}`, Window{Start: now.Add(-time.Hour), End: now}, 50)

	// A limited page has to be the most recent lines, not the oldest ones in the
	// window — otherwise a search over a day shows yesterday morning.
	if !strings.Contains(path, "direction=backward") {
		t.Fatalf("path = %q, want the newest lines first", path)
	}
	if !strings.Contains(path, "limit=50") {
		t.Fatalf("path = %q, want the limit carried", path)
	}
}
