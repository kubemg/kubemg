package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/auth"
	"github.com/kubemg/kubemg/backend/pkg/bastion"
)

/*
 * KubeMG's own refusals, in the trail.
 *
 * Everything that reaches bastion.Proxy is recorded, refusals included. What
 * the API layer refuses *before* the tunnel used to be recorded nowhere: a
 * namespace outside the grant, a cluster-wide list for a scoped grant, a kind
 * KubeMG will not author, a manifest addressed at the wrong namespace, a chart
 * the query path will not draw for a scoped caller. Those checks write their
 * answer and return from some forty call sites, so the record is not written at
 * any of them — it is written here, once, over the route groups that reach a
 * cluster or its data, for any 4xx answer whose refusal is not already in the
 * trail.
 *
 * "Already in the trail" is decided by bastion.Attempt rather than by status
 * code: every record written under this request's context notes itself on it,
 * so a cluster's own 403 handed back through Proxy.Call — recorded by Call — is
 * never written a second time, and a refusal of KubeMG's own is never missed
 * because some read before it succeeded.
 *
 * What it does not record: a 401 (no identity to put on the row — the session
 * is missing, expired or revoked before anything here runs), a 5xx (a failure,
 * not a refusal; the proxy records the ones that touched a cluster), and the
 * routes outside these groups (KubeMG's own configuration, which has its own
 * audit verbs where it matters).
 */

// maxRefusalBody bounds how much of a refusal's response body is held to read
// its message from. Refusals are one-line JSON; a body past this is not one.
const maxRefusalBody = 4 << 10

// auditRefusals records the 4xx answers the handlers behind it give on their
// own behalf. It never changes the response and never blocks it: the record is
// written after the handler has answered, through the same auditor — and so the
// same asynchronous, drop-rather-than-block sinks — as every other row.
func (s *server) auditRefusals() gin.HandlerFunc {
	return func(c *gin.Context) {
		if s.auditor == nil {
			c.Next()
			return
		}
		started := time.Now()
		ctx, attempt := bastion.WithAttempt(c.Request.Context())
		c.Request = c.Request.WithContext(ctx)
		writer := &refusalWriter{ResponseWriter: c.Writer}
		c.Writer = writer

		c.Next()

		status := writer.Status()
		if status < http.StatusBadRequest || status >= http.StatusInternalServerError ||
			status == http.StatusUnauthorized || attempt.RefusalRecorded() {
			return
		}
		claims, ok := auth.ClaimsFrom(c)
		if !ok {
			return
		}
		s.auditor.Record(c.Request.Context(), s.refusalEvent(c, claims, started, status, writer.body))
	}
}

// refusalEvent builds the row for one refused request, shaped like the
// refusals the proxy writes so a filter on verb, namespace or status finds both.
func (s *server) refusalEvent(c *gin.Context, claims *auth.Claims, started time.Time,
	status int, body []byte,
) bastion.Event {
	event := bastion.Event{
		At:        started.UTC(),
		UserID:    claims.UserID,
		Username:  claims.Username,
		Method:    c.Request.Method,
		Status:    status,
		Duration:  time.Since(started),
		Error:     refusalMessage(body, status),
		RefusedBy: bastion.RefusedByKubeMG,
	}

	if id, err := strconv.ParseUint(c.Param("id"), 10, 64); err == nil && id != 0 {
		event.ClusterID = uint(id)
		// Read only on this path, which a successful request never takes. A
		// cluster that cannot be read leaves the name empty — the id is the
		// join, and the refusal is still worth more than the lookup.
		if cluster, err := s.store.ClusterByID(c.Request.Context(), uint(id)); err == nil {
			event.Cluster = cluster.Name
		}
	}

	query := bastion.StripCredentials(c.Request.URL.RawQuery)

	// kubectl's own route: the refusal is about a Kubernetes path, so it is
	// described exactly as the proxy describes the calls it carries.
	if path, ok := c.Params.Get("path"); ok && strings.HasSuffix(c.FullPath(), "/proxy/*path") {
		if path == "" {
			path = "/"
		}
		if query != "" {
			path += "?" + query
		}
		parsed := bastion.ParsePath(path)
		event.Path = path
		event.Verb = bastion.VerbFor(c.Request.Method, path)
		event.Namespace = parsed.Namespace
		event.Resource = parsed.Resource
		return event
	}

	event.Path = c.Request.URL.Path
	if query != "" {
		event.Path += "?" + query
	}
	route := consoleRoute(c.FullPath())
	event.Verb = consoleVerb(c, route)
	event.Namespace = c.Query("namespace")
	if event.Namespace == "" {
		event.Namespace = c.Param("namespace")
	}
	event.Resource = c.Query("kind")
	if event.Resource == "" {
		event.Resource = route
	}
	return event
}

// consoleRoute is the part of a route template after the cluster, with its
// path parameters dropped: "resources/pods/:pod/logs" reads as "pods/logs", the
// thing that was asked for rather than the address it was asked at.
func consoleRoute(full string) string {
	if _, tail, ok := strings.Cut(full, "/clusters/:id/"); ok {
		full = tail
	}
	full = strings.TrimPrefix(full, "resources/")
	kept := make([]string, 0, 4)
	for _, segment := range strings.Split(full, "/") {
		if segment == "" || strings.HasPrefix(segment, ":") || strings.HasPrefix(segment, "*") {
			continue
		}
		kept = append(kept, segment)
	}
	return strings.Join(kept, "/")
}

// consoleActionVerbs are the console's write routes whose HTTP method does not
// name the Kubernetes verb they perform. Each is the verb the call it makes down
// the tunnel would have been recorded under, so a refused scale and an allowed
// one sit under the same filter.
var consoleActionVerbs = map[string]string{
	"scale":                  "update",
	"restart":                "update",
	"suspend":                "update",
	"workload/rollback":      "update",
	"node/schedulable":       "update",
	"pods/debug":             "update",
	"helm/releases/upgrade":  "update",
	"helm/releases/rollback": "update",
	"cronjob/run":            "create",
	"object/diff":            "get",
	"access-review":          "create",
}

// consoleVerb names the Kubernetes verb a console route stands for.
func consoleVerb(c *gin.Context, route string) string {
	if verb, ok := consoleActionVerbs[route]; ok {
		return verb
	}
	// A silence is KubeMG's own write, recorded under its own verbs by the
	// handler that makes it; a refusal of one is filed beside them.
	if route == "observability/silences" {
		switch c.Request.Method {
		case http.MethodPost:
			return verbSilenceCreate
		case http.MethodDelete:
			return verbSilenceExpire
		}
	}
	switch route {
	case "pods/logs", "observability/logs/query":
		return "log"
	case "shell/attach":
		return "attach"
	}
	switch c.Request.Method {
	case http.MethodPost:
		return "create"
	case http.MethodPut:
		return "update"
	case http.MethodPatch:
		return "patch"
	case http.MethodDelete:
		return "delete"
	case http.MethodGet:
		if c.Query("name") != "" || len(c.Params) > 1 {
			return "get"
		}
		return "list"
	default:
		return strings.ToLower(c.Request.Method)
	}
}

// refusalMessage is the explanation the caller was given: the `error` field of
// the JSON body every handler answers with, or the status's own text when the
// body says nothing readable.
func refusalMessage(body []byte, status int) string {
	var payload struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &payload) == nil && strings.TrimSpace(payload.Error) != "" {
		return payload.Error
	}
	return http.StatusText(status)
}

// refusalWriter keeps the head of a 4xx body so its message can be recorded.
// Every other write passes straight through untouched, and nothing is held for
// a response that is not a refusal — a log stream or a large list costs nothing.
type refusalWriter struct {
	gin.ResponseWriter
	body []byte
}

func (w *refusalWriter) Write(data []byte) (int, error) {
	w.capture(data)
	return w.ResponseWriter.Write(data)
}

func (w *refusalWriter) WriteString(data string) (int, error) {
	w.capture([]byte(data))
	return w.ResponseWriter.WriteString(data)
}

func (w *refusalWriter) capture(data []byte) {
	status := w.ResponseWriter.Status()
	if status < http.StatusBadRequest || status >= http.StatusInternalServerError {
		return
	}
	if room := maxRefusalBody - len(w.body); room > 0 {
		if len(data) > room {
			data = data[:room]
		}
		w.body = append(w.body, data...)
	}
}
