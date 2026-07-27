package bastion

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
)

// Event is one proxied API call, recorded whether it succeeded or not. It is
// deliberately flat: an audit trail is read by a SIEM far more often than by a
// person, and nesting buys nothing.
type Event struct {
	At        time.Time
	UserID    uint
	Username  string
	ClusterID uint
	Cluster   string
	// Verb is the Kubernetes verb the request maps onto ("get", "create", …),
	// which is what an auditor reasons in. Method keeps the raw HTTP truth.
	Verb   string
	Method string
	// Path is the API path with its query string, so a `kubectl get pods -w`
	// is distinguishable from a plain list.
	Path string
	// Namespace and Resource are parsed out of the path when it looks like a
	// Kubernetes API URL, and empty when it does not.
	Namespace string
	Resource  string
	// Impersonated* is the identity KubeMG asserted to the API server. This is
	// the crux of the record: it ties a KubeMG account to the Kubernetes
	// subject that actually performed the action.
	ImpersonatedUser   string
	ImpersonatedGroups []string
	Status             int
	Duration           time.Duration
	// Error explains a call that never reached the API server.
	Error string

	// Streaming marks a long-lived call — exec, attach, watch, logs -f. Such a
	// call is recorded twice, once when it opens and once when it ends, because
	// a session that runs for an hour must not be invisible until it stops.
	Streaming bool
	Phase     string
	// BytesOut and BytesIn are filled on the closing record: what came back
	// from the cluster, and what the user typed into it.
	BytesOut int64
	BytesIn  int64
}

// Audit phases for a streaming call. A non-streaming call carries neither.
const (
	PhaseOpen  = "open"
	PhaseClose = "close"
)

// Auditor records proxied API actions.
type Auditor interface {
	Record(ctx context.Context, event Event)
}

// SlogAuditor writes audit records as structured logs. Shipping them to stdout
// keeps the container's log stream the single collection point; a database sink
// can implement Auditor later without touching the proxy.
type SlogAuditor struct {
	logger *slog.Logger
}

// NewAuditor builds an auditor over the given logger, defaulting to JSON on
// stderr so records survive without any configuration.
func NewAuditor(logger *slog.Logger) *SlogAuditor {
	if logger == nil {
		logger = slog.New(slog.NewJSONHandler(os.Stderr, nil))
	}
	return &SlogAuditor{logger: logger}
}

// Record emits one audit line. A failed call logs at warn so the stream can be
// filtered down to refusals without reprocessing every successful read.
func (a *SlogAuditor) Record(ctx context.Context, event Event) {
	attrs := []any{
		slog.String("audit", "kubemg.proxy"),
		slog.Time("timestamp", event.At),
		slog.Uint64("user_id", uint64(event.UserID)),
		slog.String("username", event.Username),
		slog.Uint64("cluster_id", uint64(event.ClusterID)),
		slog.String("cluster", event.Cluster),
		slog.String("verb", event.Verb),
		slog.String("method", event.Method),
		slog.String("uri", event.Path),
		slog.String("impersonate_user", event.ImpersonatedUser),
		slog.String("impersonate_groups", strings.Join(event.ImpersonatedGroups, ",")),
		slog.Int("status_code", event.Status),
		slog.Int64("duration_ms", event.Duration.Milliseconds()),
	}
	if event.Namespace != "" {
		attrs = append(attrs, slog.String("namespace", event.Namespace))
	}
	if event.Resource != "" {
		attrs = append(attrs, slog.String("resource", event.Resource))
	}
	if event.Error != "" {
		attrs = append(attrs, slog.String("error", event.Error))
	}
	if event.Streaming {
		attrs = append(attrs, slog.Bool("streaming", true), slog.String("phase", event.Phase))
	}
	if event.BytesOut != 0 || event.BytesIn != 0 {
		attrs = append(attrs,
			slog.Int64("bytes_out", event.BytesOut),
			slog.Int64("bytes_in", event.BytesIn),
		)
	}

	level := slog.LevelInfo
	if event.Error != "" || event.Status >= http.StatusBadRequest {
		level = slog.LevelWarn
	}
	a.logger.Log(ctx, level, "proxied kubernetes api call", attrs...)
}

// VerbFor maps an HTTP method onto the Kubernetes verb it performs. A GET is
// reported as "list" when the path addresses a collection rather than a named
// object, because "get secrets" and "list secrets" are very different lines to
// find in an audit trail.
func VerbFor(method, path string) string {
	// An interactive session is reported by what it is. Recording a shell in a
	// production pod as a "get" would bury the single most sensitive thing in
	// the trail under the most common one.
	parsed := ParsePath(path)
	switch parsed.Subresource {
	case "exec", "attach", "portforward":
		return parsed.Subresource
	}

	switch method {
	case http.MethodPost:
		return "create"
	case http.MethodPut:
		return "update"
	case http.MethodPatch:
		return "patch"
	case http.MethodDelete:
		return "delete"
	case http.MethodGet:
		if watchRequested(path) {
			return "watch"
		}
		if parsed.Subresource == "log" {
			return "log"
		}
		if parsed.Name == "" {
			return "list"
		}
		return "get"
	default:
		return strings.ToLower(method)
	}
}

func watchRequested(path string) bool {
	query := ""
	if i := strings.IndexByte(path, '?'); i >= 0 {
		query = path[i+1:]
	}
	for _, param := range strings.Split(query, "&") {
		if param == "watch" || param == "watch=true" || param == "watch=1" {
			return true
		}
	}
	return false
}

// APIPath is the Kubernetes-shaped decomposition of a request path. Fields are
// empty when the path does not match the API layout, which is the case for
// /version, /healthz and the discovery endpoints.
type APIPath struct {
	Namespace string
	Resource  string
	Name      string
	// Subresource is the trailing segment for calls like pods/log or pods/exec,
	// the ones an auditor cares about most.
	Subresource string
}

// ParsePath decomposes a Kubernetes API path. It handles both the legacy core
// group (/api/v1/…) and the grouped APIs (/apis/<group>/<version>/…), with or
// without a namespace scope.
func ParsePath(path string) APIPath {
	if i := strings.IndexByte(path, '?'); i >= 0 {
		path = path[:i]
	}

	parts := []string{}
	for _, part := range strings.Split(path, "/") {
		if part != "" {
			parts = append(parts, part)
		}
	}

	// Drop the API root so both /api/v1/… and /apis/g/v1/… leave the same tail.
	switch {
	case len(parts) >= 2 && parts[0] == "api":
		parts = parts[2:]
	case len(parts) >= 3 && parts[0] == "apis":
		parts = parts[3:]
	default:
		return APIPath{}
	}

	var out APIPath
	if len(parts) >= 2 && parts[0] == "namespaces" && len(parts) != 2 {
		// /namespaces/<ns>/<resource>… — a namespace-scoped call. A bare
		// /namespaces/<ns> is an operation *on* the namespace object instead,
		// so it falls through to the cluster-scoped branch below.
		out.Namespace = parts[1]
		parts = parts[2:]
	}

	if len(parts) > 0 {
		out.Resource = parts[0]
	}
	if len(parts) > 1 {
		out.Name = parts[1]
	}
	if len(parts) > 2 {
		out.Subresource = parts[2]
	}
	return out
}
