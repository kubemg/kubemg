package api

import (
	"encoding/json"
	"fmt"
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
 * `kubectl describe`, for whatever the Explore sidebar can address.
 *
 * The YAML editor already returns the whole object, and the lists already return
 * the few columns a table shows. This is the middle surface, and it exists
 * because of the part neither of the other two has: the **events**. When
 * something is wrong, the object almost never says so — its spec is exactly what
 * was asked for, and its status says "not ready" without saying why. The reason
 * is in the events the API server recorded against it, and until now KubeMG had
 * no way to show them at all.
 *
 * It is deliberately *not* a per-kind describer. kubectl has one for every kind,
 * hand-written and hundreds of lines each; reproducing twenty of them here would
 * be a maintenance surface out of all proportion to a drawer tab, and would be
 * wrong for the CRDs whose kinds KubeMG has never heard of. So three things are
 * extracted generically instead, chosen because they hold for every Kubernetes
 * object rather than for the ones somebody wrote a describer for:
 *
 *   - metadata: labels and annotations, which is how objects are wired to each
 *     other and where half of an operator's own bookkeeping lives;
 *   - `status.conditions`, the one structured statement of health the API
 *     machinery has, carried by almost every kind including well-written CRDs;
 *   - a bounded flatten of spec and status, so the fields that fit on a line are
 *     on a line. The YAML tab is the complete view and this never pretends to be.
 *
 * The object is addressed exactly the way the YAML editor addresses it — the
 * same fixed kind table, the same validated CRD components, the same namespace
 * check against the grant — so this adds a read, not a way to reach anything new.
 */

const (
	// maxSummaryFields caps a flattened spec or status. Past this the summary
	// stops being a summary; the YAML tab is the complete object.
	maxSummaryFields = 60
	// maxSummaryDepth is how far into a nested spec the flatten walks. Three
	// levels reaches `spec.selector.matchLabels.app` and stops before a
	// Deployment's whole embedded pod template.
	maxSummaryDepth = 3
	// maxSummaryValue truncates one value. A field that does not fit on a line is
	// not a summary line.
	maxSummaryValue = 240
	// maxAnnotationValue truncates an annotation. Operators park entire documents
	// in annotations, and one of them can be larger than the object it is on.
	maxAnnotationValue = 2048
	// maxEvents bounds the event list. A crash-looping pod produces events
	// forever; the recent ones are the ones that explain it.
	maxEvents = 100
)

// eventView is one Kubernetes Event as the drawer's table shows it.
type eventView struct {
	Type    string `json:"type"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
	Count   int32  `json:"count"`
	// Source is what reported it — the scheduler, the kubelet on a named node,
	// a controller — which is usually half the answer on its own.
	Source    string     `json:"source,omitempty"`
	FirstSeen *time.Time `json:"first_seen,omitempty"`
	LastSeen  *time.Time `json:"last_seen,omitempty"`
}

// conditionView is one entry of `status.conditions`.
type conditionView struct {
	Type             string     `json:"type"`
	Status           string     `json:"status"`
	Reason           string     `json:"reason,omitempty"`
	Message          string     `json:"message,omitempty"`
	LastTransitionAt *time.Time `json:"last_transition_at,omitempty"`
}

// fieldView is one line of a flattened spec or status: the dotted path to a
// value, and the value rendered as text.
type fieldView struct {
	Path  string `json:"path"`
	Value string `json:"value"`
}

// describeView is the whole answer.
type describeView struct {
	Kind        string            `json:"kind"`
	APIVersion  string            `json:"api_version,omitempty"`
	Name        string            `json:"name"`
	Namespace   string            `json:"namespace,omitempty"`
	Created     time.Time         `json:"created_at"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`

	Conditions []conditionView `json:"conditions"`

	Spec          []fieldView `json:"spec_summary"`
	SpecTruncated bool        `json:"spec_truncated,omitempty"`

	Status          []fieldView `json:"status_summary"`
	StatusTruncated bool        `json:"status_truncated,omitempty"`

	Events []eventView `json:"events"`
	// EventsAvailable is false when the events could not be read. Describe is
	// still worth showing without them, so a refusal narrows the answer rather
	// than failing it — but it says so instead of showing an empty table that
	// reads as "nothing happened".
	EventsAvailable bool   `json:"events_available"`
	EventsReason    string `json:"events_reason,omitempty"`
}

// describeResource reads one object and everything the cluster has recorded
// about it.
func (s *server) describeResource(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	kind, name, namespace, ok := s.resourceObjectTarget(c, grant)
	if !ok {
		return
	}

	body, ok := s.readObject(c, user, cluster, grant, kind, namespace, name)
	if !ok {
		return
	}

	var object map[string]any
	if err := json.Unmarshal(body, &object); err != nil || object == nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "the cluster returned an unreadable object"})
		return
	}

	view := describeObject(object, name, namespace)
	view.Events, view.EventsAvailable, view.EventsReason =
		s.objectEvents(c, user, cluster, grant, kind, view.Kind, namespace, name)
	if !view.EventsAvailable && view.EventsReason == "" {
		return
	}
	c.JSON(http.StatusOK, view)
}

// describeObject extracts everything that holds for any Kubernetes object.
func describeObject(object map[string]any, name, namespace string) describeView {
	view := describeView{Name: name, Namespace: namespace, Events: []eventView{}}
	view.Kind, _ = object["kind"].(string)
	view.APIVersion, _ = object["apiVersion"].(string)

	if metadata, ok := object["metadata"].(map[string]any); ok {
		if got, _ := metadata["name"].(string); got != "" {
			view.Name = got
		}
		if got, _ := metadata["namespace"].(string); got != "" {
			view.Namespace = got
		}
		if created, _ := metadata["creationTimestamp"].(string); created != "" {
			if at, err := time.Parse(time.RFC3339, created); err == nil {
				view.Created = at
			}
		}
		view.Labels = stringMap(metadata["labels"], 0)
		// kubectl's copy of the last applied manifest is a duplicate of the
		// object it is attached to and routinely longer than it; it is dropped
		// here for the same reason the YAML editor drops it.
		view.Annotations = stringMap(metadata["annotations"], maxAnnotationValue)
		delete(view.Annotations, lastAppliedAnnotation)
	}

	status, _ := object["status"].(map[string]any)
	view.Conditions = conditionsOf(status)

	spec, _ := object["spec"].(map[string]any)
	view.Spec, view.SpecTruncated = summarize(spec)
	// Conditions are shown in full above, so repeating them in the flatten would
	// spend most of the status budget saying the same thing twice.
	view.Status, view.StatusTruncated = summarize(without(status, "conditions"))

	return view
}

// without copies a map minus one key, so the caller can drop a field it renders
// separately without mutating what it was given.
func without(node map[string]any, key string) map[string]any {
	if node == nil {
		return nil
	}
	out := make(map[string]any, len(node))
	for name, value := range node {
		if name != key {
			out[name] = value
		}
	}
	return out
}

// stringMap renders a metadata map, truncating values past a limit. Zero means
// no limit, which is right for labels: a label value is bounded at 63
// characters by Kubernetes itself.
func stringMap(node any, limit int) map[string]string {
	values, ok := node.(map[string]any)
	if !ok || len(values) == 0 {
		return nil
	}

	out := make(map[string]string, len(values))
	for key, value := range values {
		text, ok := value.(string)
		if !ok {
			text = scalarText(value)
		}
		if limit > 0 {
			text = truncate(text, limit)
		}
		out[key] = text
	}
	return out
}

// conditionsOf pulls `status.conditions` out of any object that has them. The
// shape is the same across the built-in kinds and every CRD that follows the API
// conventions, which is what makes this worth extracting generically.
func conditionsOf(status map[string]any) []conditionView {
	raw, _ := status["conditions"].([]any)
	out := make([]conditionView, 0, len(raw))

	for _, entry := range raw {
		condition, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		view := conditionView{}
		view.Type, _ = condition["type"].(string)
		view.Status, _ = condition["status"].(string)
		view.Reason, _ = condition["reason"].(string)
		view.Message, _ = condition["message"].(string)
		if at, _ := condition["lastTransitionTime"].(string); at != "" {
			if parsed, err := time.Parse(time.RFC3339, at); err == nil {
				view.LastTransitionAt = &parsed
			}
		}
		if view.Type != "" {
			out = append(out, view)
		}
	}
	return out
}

/* ------------------------------------------------------------- flattening --- */

// summarize flattens a spec or status into dotted paths and rendered values. It
// is bounded in three directions — depth, count and value length — because the
// point is a page of lines an operator can scan, not a second copy of the
// object. It reports whether it stopped early, so the UI can say the YAML tab
// has the rest rather than implying this is everything.
func summarize(node map[string]any) ([]fieldView, bool) {
	out := []fieldView{}
	truncated := flatten(&out, "", node, 0)
	return out, truncated
}

// flatten walks one level, in sorted key order so the same object always renders
// the same way. It returns true if it ran out of budget.
func flatten(out *[]fieldView, prefix string, node map[string]any, depth int) bool {
	keys := make([]string, 0, len(node))
	for key := range node {
		keys = append(keys, key)
	}
	slices.Sort(keys)

	for _, key := range keys {
		if len(*out) >= maxSummaryFields {
			return true
		}

		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		if flattenValue(out, path, node[key], depth) {
			return true
		}
	}
	return false
}

// flattenValue emits one value, recursing into a nested object while there is
// depth left and describing it by size once there is not.
func flattenValue(out *[]fieldView, path string, value any, depth int) bool {
	switch typed := value.(type) {
	case nil:
		return false

	case map[string]any:
		if len(typed) == 0 {
			return false
		}
		if depth+1 >= maxSummaryDepth {
			*out = append(*out, fieldView{Path: path, Value: plural(len(typed), "field")})
			return false
		}
		return flatten(out, path, typed, depth+1)

	case []any:
		if len(typed) == 0 {
			return false
		}
		// A list of scalars is a value — ports, access modes, finalizers. A list
		// of objects is a structure, and its length is the honest summary of it.
		if text, ok := scalarList(typed); ok {
			*out = append(*out, fieldView{Path: path, Value: text})
			return false
		}
		*out = append(*out, fieldView{Path: path, Value: plural(len(typed), "item")})
		return false

	default:
		*out = append(*out, fieldView{Path: path, Value: truncate(scalarText(typed), maxSummaryValue)})
		return false
	}
}

// scalarList joins a list of scalars, reporting false if any entry is not one.
func scalarList(values []any) (string, bool) {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		switch value.(type) {
		case map[string]any, []any, nil:
			return "", false
		}
		parts = append(parts, scalarText(value))
	}
	return truncate(strings.Join(parts, ", "), maxSummaryValue), true
}

// scalarText renders a JSON scalar the way it was written. Numbers arrive as
// float64, so a replica count has to come back out as `3` rather than `3.0`.
func scalarText(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case bool:
		return strconv.FormatBool(typed)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		return fmt.Sprintf("%v", typed)
	}
}

func plural(count int, noun string) string {
	if count == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", count, noun)
}

func truncate(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "…"
}

/* ----------------------------------------------------------------- events --- */

// eventObject covers both shapes a Kubernetes Event comes in. The core `v1`
// Event carries firstTimestamp/lastTimestamp/count; an event written through the
// newer `events.k8s.io` API carries eventTime and a series instead, and arrives
// on the same core list with the old fields empty. Reading only one shape means
// a cluster's newest events silently show no timestamp at all.
type eventObject struct {
	Type               string     `json:"type"`
	Reason             string     `json:"reason"`
	Message            string     `json:"message"`
	Count              int32      `json:"count"`
	FirstTimestamp     *time.Time `json:"firstTimestamp"`
	LastTimestamp      *time.Time `json:"lastTimestamp"`
	EventTime          *time.Time `json:"eventTime"`
	ReportingComponent string     `json:"reportingComponent"`
	Series             *struct {
		Count            int32      `json:"count"`
		LastObservedTime *time.Time `json:"lastObservedTime"`
	} `json:"series"`
	Source struct {
		Component string `json:"component"`
		Host      string `json:"host"`
	} `json:"source"`

	// The fields below are unused by the describe view, which already knows which
	// object it asked about. The alarm watcher reads the same list cluster-wide and
	// has to be told — see alarms_watch.go.
	Metadata struct {
		Name              string     `json:"name"`
		Namespace         string     `json:"namespace"`
		UID               string     `json:"uid"`
		CreationTimestamp *time.Time `json:"creationTimestamp"`
	} `json:"metadata"`
	InvolvedObject struct {
		Kind      string `json:"kind"`
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"involvedObject"`
}

func (e eventObject) view() eventView {
	view := eventView{
		Type:      e.Type,
		Reason:    e.Reason,
		Message:   e.Message,
		Count:     e.Count,
		FirstSeen: e.FirstTimestamp,
	}

	if e.Series != nil && e.Series.Count > view.Count {
		view.Count = e.Series.Count
	}
	if view.Count < 1 {
		view.Count = 1
	}

	// Newest first among the fields that could carry it, in the order that is
	// most specific about when it last happened.
	for _, candidate := range []*time.Time{
		e.LastTimestamp,
		seriesTime(e.Series),
		e.EventTime,
		e.FirstTimestamp,
	} {
		if candidate != nil && !candidate.IsZero() {
			view.LastSeen = candidate
			break
		}
	}
	if view.FirstSeen == nil || view.FirstSeen.IsZero() {
		view.FirstSeen = e.EventTime
	}

	view.Source = e.Source.Component
	if view.Source == "" {
		view.Source = e.ReportingComponent
	}
	if e.Source.Host != "" {
		view.Source = strings.TrimSpace(view.Source + ", " + e.Source.Host)
	}
	return view
}

func seriesTime(series *struct {
	Count            int32      `json:"count"`
	LastObservedTime *time.Time `json:"lastObservedTime"`
}) *time.Time {
	if series == nil {
		return nil
	}
	return series.LastObservedTime
}

// objectEvents reads what the cluster recorded against one object.
//
// A refusal here is not a failed describe. Events are their own resource with
// their own RBAC, and a grant that can read a Deployment but not the events in
// its namespace should still see the Deployment — so the reason is reported and
// the rest of the answer stands. The empty string as a reason is reserved for
// "the call itself already answered the request", which is how a transport
// failure gets out.
func (s *server) objectEvents(c *gin.Context, user *db.User, cluster *db.Cluster,
	grant db.UserClusterAccess, kind objectKind, objectKindName, namespace, name string,
) ([]eventView, bool, string) {
	selector := "involvedObject.name=" + name
	if objectKindName != "" {
		selector += ",involvedObject.kind=" + objectKindName
	}

	query := url.Values{}
	query.Set("fieldSelector", selector)
	query.Set("limit", strconv.Itoa(maxEvents))

	// A cluster-scoped object's events are written into whichever namespace the
	// reporting controller chose — `default`, usually — so they are found by a
	// cluster-wide list rather than by guessing. A namespace-scoped grant never
	// reaches here for such a kind: requireClusterScope refused it already.
	path := "/api/v1/events?" + query.Encode()
	if kind.namespaced {
		path = fmt.Sprintf("/api/v1/namespaces/%s/events?%s", url.PathEscape(namespace), query.Encode())
	}

	resp, ok := s.callResource(c, user, cluster, grant, path)
	if !ok {
		return nil, false, ""
	}
	if resp.Status < 200 || resp.Status >= 300 {
		return []eventView{}, false, kubeErrorMessage(resp.Body, resp.Status)
	}

	var list struct {
		Items []eventObject `json:"items"`
	}
	if err := json.Unmarshal(resp.Body, &list); err != nil {
		return []eventView{}, false, "the cluster returned an unreadable event list"
	}

	out := make([]eventView, 0, len(list.Items))
	for _, item := range list.Items {
		out = append(out, item.view())
	}

	// Newest first: in a drawer the question is what just happened, not what
	// happened when the object was created. kubectl orders the other way because
	// it is printing a log, and this is not one.
	sort.SliceStable(out, func(a, b int) bool {
		return eventAt(out[a]).After(eventAt(out[b]))
	})
	return out, true, ""
}

func eventAt(event eventView) time.Time {
	if event.LastSeen != nil {
		return *event.LastSeen
	}
	if event.FirstSeen != nil {
		return *event.FirstSeen
	}
	return time.Time{}
}
