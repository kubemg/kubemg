package observability

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * Reading a cluster's logs out of whichever aggregator it registered.
 *
 * The pod log view reads live from the pod through the tunnel, which is the
 * right thing when the pod is there — but a pod that has been replaced took its
 * logs with it, and there is no way to ask "which pod logged this error" across
 * a namespace. That is what this answers.
 *
 * The two providers have nothing in common. VictoriaLogs speaks LogsQL and
 * answers NDJSON — one JSON object per line, streamed; Loki speaks LogQL and
 * answers a JSON envelope of streams, each holding [nanoseconds, line] pairs.
 * So unlike the metrics four, each gets its own query builder and its own
 * decoder, and the only thing shared is the shape that comes out.
 *
 * The scope rule is the metrics one, for the same reason: an aggregator has
 * never heard of the caller. A selector is **built** from validated namespace,
 * pod and container names, and the caller's free-text filter is carried as a
 * value rather than as syntax — see `queryPhrase`, which is the one place a
 * caller's own characters reach a query language and is therefore the one place
 * that quotes rather than validates.
 */

const (
	// maxLogLimit bounds a page. A log search that wants more than this wants a
	// different tool; the answer says it was capped.
	maxLogLimit = 1000
	// defaultLogLimit is a screenful and then some.
	defaultLogLimit = 200
	// maxLogBody caps the response read. Lines are long and a thousand of them
	// is not small, but this is far past a legitimate page.
	maxLogBody = 16 << 20
	// maxLogLine truncates one line. A single log line carrying a megabyte of
	// stack trace is real, and sending it to a browser verbatim is not useful.
	maxLogLine = 8 << 10
)

// LogRequest is one search.
type LogRequest struct {
	Namespace string
	Pod       string
	Container string
	// Filter is the operator's own free text — a phrase to look for in the
	// message. It is the only caller-supplied value that is not a Kubernetes
	// name, and it is quoted into the query rather than validated, because the
	// whole point of it is to contain arbitrary characters.
	Filter string
	Window Window
	Limit  int
}

// LogEntry is one line as the explorer shows it.
type LogEntry struct {
	At        time.Time `json:"at"`
	Message   string    `json:"message"`
	Namespace string    `json:"namespace,omitempty"`
	Pod       string    `json:"pod,omitempty"`
	Container string    `json:"container,omitempty"`
	// Truncated marks a line this cut short.
	Truncated bool `json:"truncated,omitempty"`
}

// LogResult is a page of logs.
type LogResult struct {
	Entries []LogEntry `json:"entries"`
	Start   time.Time  `json:"start"`
	End     time.Time  `json:"end"`
	// Limited says the page hit the limit, so there is more to see in a
	// narrower window. It is not the same as "no more logs exist".
	Limited bool `json:"limited,omitempty"`
	// Query is what KubeMG actually asked, for the same reason the metrics
	// result carries it: an empty result is usually a labelling mismatch, and
	// there is no way to see that without seeing the query.
	Query string `json:"query"`
}

// QueryLogs runs one search against a cluster's logs datasource.
func QueryLogs(ctx context.Context, target Target, tunnel TunnelCall,
	scope Scope, req LogRequest,
) (LogResult, error) {
	if target.Kind != db.SourceLogs {
		return LogResult{}, fmt.Errorf("this datasource does not serve logs")
	}
	if err := target.Validate(); err != nil {
		return LogResult{}, err
	}
	if target.AccessMode == db.AccessInCluster && tunnel == nil {
		return LogResult{}, fmt.Errorf(
			"an in-cluster datasource can only be read through a connected agent")
	}

	sel, err := buildLogSelector(scope, req)
	if err != nil {
		return LogResult{}, err
	}

	window, err := req.Window.Normalize(time.Now().UTC())
	if err != nil {
		return LogResult{}, err
	}

	limit := req.Limit
	if limit <= 0 {
		limit = defaultLogLimit
	}
	if limit > maxLogLimit {
		limit = maxLogLimit
	}

	var query, path string
	switch target.Provider {
	case db.ProviderVictoriaLogs:
		query = victoriaLogsQuery(sel, req.Filter)
		path = victoriaLogsPath(query, window, limit)
	case db.ProviderLoki:
		query = lokiQuery(sel, req.Filter)
		path = lokiPath(query, window, limit)
	default:
		return LogResult{}, fmt.Errorf("%s is not a log backend KubeMG queries",
			providerLabel(target.Provider))
	}

	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	status, body, err := callLimited(ctx, target, path, tunnel, maxLogBody, queryTimeout)
	if err != nil {
		return LogResult{}, err
	}
	if status < 200 || status >= 300 {
		return LogResult{}, fmt.Errorf("%s", explain(target, status, body))
	}

	var entries []LogEntry
	switch target.Provider {
	case db.ProviderVictoriaLogs:
		entries, err = decodeVictoriaLogs(body, limit)
	case db.ProviderLoki:
		entries, err = decodeLoki(body, limit)
	}
	if err != nil {
		return LogResult{}, err
	}

	// Newest first. The pod log view tails, because a stream is read forwards;
	// a search is asked what happened, and the answer starts with the most
	// recent thing that did.
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].At.After(entries[j].At) })

	return LogResult{
		Entries: entries,
		Start:   window.Start,
		End:     window.End,
		Limited: len(entries) >= limit,
		Query:   query,
	}, nil
}

// logSelector is the validated set of Kubernetes names a log query narrows to.
type logSelector struct {
	namespaces []string
	pod        string
	container  string
}

func buildLogSelector(scope Scope, req LogRequest) (logSelector, error) {
	var sel logSelector

	if err := validateName("pod", req.Pod); err != nil {
		return sel, err
	}
	if err := validateName("container", req.Container); err != nil {
		return sel, err
	}

	namespaces, err := scope.resolveNamespace(req.Namespace)
	if err != nil {
		return sel, err
	}
	if req.Pod != "" && len(namespaces) != 1 {
		return sel, fmt.Errorf("searching one pod's logs needs the namespace it is in")
	}

	sel.namespaces = namespaces
	sel.pod = req.Pod
	sel.container = req.Container
	return sel, nil
}

/*
 * VictoriaLogs. Promtail, vector and VictoriaLogs' own agent all write the
 * Kubernetes metadata as `kubernetes.pod_namespace` / `pod_name` /
 * `container_name` stream fields, which is what the selector below matches on.
 * A cluster that ships logs under different field names will get an empty
 * answer — which is why the query travels back with the result.
 */

func victoriaLogsQuery(sel logSelector, filter string) string {
	terms := make([]string, 0, 4)

	switch {
	case len(sel.namespaces) == 1:
		terms = append(terms, fmt.Sprintf("kubernetes.pod_namespace:%s", quotePhrase(sel.namespaces[0])))
	case len(sel.namespaces) > 1:
		alternatives := make([]string, 0, len(sel.namespaces))
		for _, namespace := range sel.namespaces {
			alternatives = append(alternatives, quotePhrase(namespace))
		}
		terms = append(terms,
			fmt.Sprintf("kubernetes.pod_namespace:(%s)", strings.Join(alternatives, " OR ")))
	}
	if sel.pod != "" {
		terms = append(terms, fmt.Sprintf("kubernetes.pod_name:%s", quotePhrase(sel.pod)))
	}
	if sel.container != "" {
		terms = append(terms, fmt.Sprintf("kubernetes.container_name:%s", quotePhrase(sel.container)))
	}
	if phrase := strings.TrimSpace(filter); phrase != "" {
		terms = append(terms, quotePhrase(phrase))
	}

	if len(terms) == 0 {
		// LogsQL's "everything", which is what an unscoped search with no filter
		// legitimately means.
		return "*"
	}
	return strings.Join(terms, " AND ")
}

func victoriaLogsPath(query string, window Window, limit int) string {
	params := url.Values{}
	params.Set("query", query)
	params.Set("limit", strconv.Itoa(limit))
	params.Set("start", window.Start.Format(time.RFC3339))
	params.Set("end", window.End.Format(time.RFC3339))
	return "/select/logsql/query?" + params.Encode()
}

// decodeVictoriaLogs reads NDJSON: one object per line, each carrying `_time`
// and `_msg` alongside whatever stream fields were indexed.
func decodeVictoriaLogs(body []byte, limit int) ([]LogEntry, error) {
	entries := make([]LogEntry, 0, limit)

	scanner := bufio.NewScanner(bytes.NewReader(body))
	// A single log line can be far longer than bufio's default 64K ceiling, and
	// hitting it silently ends the scan — so the buffer is raised to the same
	// bound the whole body is capped at.
	scanner.Buffer(make([]byte, 0, 64<<10), maxLogLine+(1<<20))

	for scanner.Scan() && len(entries) < limit {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}

		var record map[string]any
		if err := json.Unmarshal(line, &record); err != nil {
			// One unreadable line does not invalidate the page.
			continue
		}

		entry := LogEntry{
			Message:   stringField(record, "_msg"),
			Namespace: stringField(record, "kubernetes.pod_namespace"),
			Pod:       stringField(record, "kubernetes.pod_name"),
			Container: stringField(record, "kubernetes.container_name"),
		}
		if at, ok := parseLogTime(stringField(record, "_time")); ok {
			entry.At = at
		}
		entry.Message, entry.Truncated = clampLine(entry.Message)
		entries = append(entries, entry)
	}

	if err := scanner.Err(); err != nil {
		// Whatever was read before the failure is still worth showing.
		return entries, nil
	}
	return entries, nil
}

/*
 * Loki. The Kubernetes metadata is stream labels rather than fields, and the
 * selector has to be non-empty — LogQL refuses a query that matches every
 * stream, which is why an unscoped search with no namespace still pins
 * `namespace=~".+"` rather than sending nothing.
 */

func lokiQuery(sel logSelector, filter string) string {
	matchers := make([]string, 0, 3)

	switch {
	case len(sel.namespaces) == 1:
		matchers = append(matchers, fmt.Sprintf(`namespace=%q`, sel.namespaces[0]))
	case len(sel.namespaces) > 1:
		matchers = append(matchers,
			fmt.Sprintf(`namespace=~"%s"`, promLabelAlternation(sel.namespaces)))
	default:
		matchers = append(matchers, `namespace=~".+"`)
	}
	if sel.pod != "" {
		matchers = append(matchers, fmt.Sprintf(`pod=%q`, sel.pod))
	}
	if sel.container != "" {
		matchers = append(matchers, fmt.Sprintf(`container=%q`, sel.container))
	}

	query := "{" + strings.Join(matchers, ",") + "}"
	if phrase := strings.TrimSpace(filter); phrase != "" {
		// A line filter, with the phrase as a quoted string rather than as
		// syntax. LogQL takes Go string escaping, which is what quotePhrase
		// produces.
		query += " |= " + quotePhrase(phrase)
	}
	return query
}

func lokiPath(query string, window Window, limit int) string {
	params := url.Values{}
	params.Set("query", query)
	params.Set("limit", strconv.Itoa(limit))
	params.Set("start", strconv.FormatInt(window.Start.UnixNano(), 10))
	params.Set("end", strconv.FormatInt(window.End.UnixNano(), 10))
	// Newest first, so a limited page is the most recent lines rather than the
	// oldest ones in the window.
	params.Set("direction", "backward")
	return "/loki/api/v1/query_range?" + params.Encode()
}

// lokiResponse is Loki's stream envelope.
type lokiResponse struct {
	Status string `json:"status"`
	Data   struct {
		Result []struct {
			Stream map[string]string `json:"stream"`
			// Values are [nanoseconds as a string, line] pairs.
			Values [][2]string `json:"values"`
		} `json:"result"`
	} `json:"data"`
}

func decodeLoki(body []byte, limit int) ([]LogEntry, error) {
	var payload lokiResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("the datasource returned an unreadable answer")
	}

	entries := make([]LogEntry, 0, limit)
	for _, stream := range payload.Data.Result {
		for _, value := range stream.Values {
			if len(entries) >= limit {
				return entries, nil
			}
			entry := LogEntry{
				Namespace: stream.Stream["namespace"],
				Pod:       stream.Stream["pod"],
				Container: stream.Stream["container"],
			}
			if nanos, err := strconv.ParseInt(value[0], 10, 64); err == nil {
				entry.At = time.Unix(0, nanos).UTC()
			}
			entry.Message, entry.Truncated = clampLine(value[1])
			entries = append(entries, entry)
		}
	}
	return entries, nil
}

/* ------------------------------------------------------------- shared bits --- */

// quotePhrase renders an arbitrary string as a quoted literal for both query
// languages, which share Go's string escaping. This is the one place a caller's
// own characters reach a query, so it escapes the two that end a literal —
// backslash first, or it would re-escape its own output.
func quotePhrase(value string) string {
	escaped := strings.ReplaceAll(value, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return `"` + escaped + `"`
}

// stringField reads a flat field, tolerating both the flattened key a log
// shipper writes and the nested object another one writes for the same thing.
func stringField(record map[string]any, key string) string {
	if value, ok := record[key].(string); ok {
		return value
	}

	// `kubernetes.pod_name` may arrive as a nested object instead of a flat key.
	prefix, rest, found := strings.Cut(key, ".")
	if !found {
		return ""
	}
	nested, ok := record[prefix].(map[string]any)
	if !ok {
		return ""
	}
	value, _ := nested[rest].(string)
	return value
}

// parseLogTime reads whichever timestamp format the shipper wrote.
func parseLogTime(value string) (time.Time, bool) {
	if value == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if at, err := time.Parse(layout, value); err == nil {
			return at.UTC(), true
		}
	}
	// Some shippers write epoch nanoseconds as a string.
	if nanos, err := strconv.ParseInt(value, 10, 64); err == nil {
		return time.Unix(0, nanos).UTC(), true
	}
	return time.Time{}, false
}

// clampLine bounds one message.
func clampLine(message string) (string, bool) {
	if len(message) <= maxLogLine {
		return message, false
	}
	return message[:maxLogLine], true
}
