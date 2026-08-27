package bastion

import (
	"context"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"github.com/kubemg/kubemg/backend/pkg/auditpolicy"
	"github.com/kubemg/kubemg/backend/pkg/auth"
	"github.com/kubemg/kubemg/backend/pkg/credentials"
	"github.com/kubemg/kubemg/backend/pkg/db"
	"github.com/kubemg/kubemg/backend/pkg/guardrails"
)

// GroupPrefix namespaces the Kubernetes groups KubeMG impersonates, so a
// cluster administrator can bind ClusterRoles to "kubemg:view" and friends
// without colliding with groups the cluster already uses.
const GroupPrefix = "kubemg:"

// GroupAllUsers is asserted on every proxied call, giving the cluster one
// subject to hang baseline access off.
const GroupAllUsers = GroupPrefix + "users"

// maxRequestBody caps what a client may push through the tunnel in one call.
const maxRequestBody = 8 << 20

// proxyTimeout bounds a single proxied call. Long-running verbs — watch, exec,
// port-forward — are streams and never reach this path, so nothing legitimate
// is waiting on it.
const proxyTimeout = 60 * time.Second

// hopByHopHeaders never survive a proxy hop; forwarding them corrupts the
// connection semantics on the far side.
var hopByHopHeaders = []string{
	"Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Te",
	"Trailer",
	"Transfer-Encoding",
	"Upgrade",
}

// impersonationHeaders are stripped from anything a client sends. A client that
// could set these would choose its own identity on the target cluster, which
// would defeat the entire gateway.
var impersonationHeaders = []string{
	"Impersonate-User",
	"Impersonate-Group",
	"Impersonate-Uid",
	"Authorization",
}

// discoveryPrefixes are the paths kubectl must reach before it can do anything
// at all. They are read-only and carry no namespace, so a namespace-scoped
// grant would otherwise lock a user out of their own clusters.
var discoveryPrefixes = []string{"/api", "/apis", "/version", "/openapi", "/.well-known"}

// ProxyStore is the persistence the proxy needs to decide who the caller is and
// what they may reach.
type ProxyStore interface {
	UserByID(ctx context.Context, id uint) (*db.User, error)
	ClusterByID(ctx context.Context, id uint) (*db.Cluster, error)
	AccessForUser(ctx context.Context, userID uint) (map[uint]db.UserClusterAccess, error)
}

// Proxy replays a client's Kubernetes API calls down a cluster's agent tunnel
// under an impersonated identity. Nothing about the caller's own credentials
// reaches the target cluster: KubeMG authenticates as the agent's service
// account and *asserts* who it is acting for.
type Proxy struct {
	store    ProxyStore
	registry *Registry
	auditor  Auditor
	// recorder captures interactive sessions. Nil means recording is off, which
	// changes nothing else about how a session is proxied.
	recorder SessionRecorder
	// policy is the runtime switch in front of that recorder. It can only ever
	// turn recording off — a process with no recorder has nowhere to write, and no
	// database row changes that.
	policy *auditpolicy.Policy
	// guard refuses calls and commands a safety policy names. Nil refuses
	// nothing, which is what a gateway wired without one does — see pkg/guardrails.
	guard *guardrails.Engine
	// issued is the register of handed-out kubeconfigs, read lock-free. Nil
	// revokes nothing, which is what a gateway wired without one does — see
	// pkg/credentials for why an unreadable register must fail open.
	issued *credentials.Register
	// clientUpgrader accepts the browser terminal's / kubectl's WebSocket, built
	// once with this server's origin allowlist. See newClientUpgrader.
	clientUpgrader websocket.Upgrader
}

// ProxyOptions wires the proxy handler.
type ProxyOptions struct {
	Store    ProxyStore
	Registry *Registry
	// Auditor records every proxied call. A no-op default is not provided on
	// purpose — an unaudited gateway is the thing this product exists to avoid.
	Auditor Auditor
	// Recorder captures exec and attach sessions for replay. Left nil, sessions
	// are proxied exactly as before and only the audit records describe them.
	Recorder SessionRecorder
	// Policy is the runtime audit configuration. Nil records everything, which is
	// what the tests and a server wired without settings do.
	Policy *auditpolicy.Policy
	// Guard holds the command guardrails. Nil refuses nothing: a gateway without
	// one behaves exactly as it did before guardrails existed, which is the right
	// failure mode for a control that stops calls RBAC permits.
	Guard *guardrails.Engine
	// Credentials is the published set of withdrawn kubeconfigs. Nil revokes
	// nothing — the correct failure mode for a control whose unavailability must
	// not lock a fleet out of kubectl.
	Credentials *credentials.Register
	// AllowedOrigins are the browser origins the API's CORS config already
	// trusts, and PublicURL is this server's own address — together they are
	// checked against the Origin header on the browser terminal's WebSocket
	// upgrade. Neither is required: an empty allowlist leaves the upgrader
	// checking nothing, which is what every caller before this option existed
	// got.
	AllowedOrigins []string
	PublicURL      string
}

// NewProxy builds the kubectl proxy handler.
func NewProxy(opts ProxyOptions) *Proxy {
	auditor := opts.Auditor
	if auditor == nil {
		auditor = NewAuditor(nil)
	}
	registry := opts.Registry
	if registry == nil {
		registry = NewRegistry()
	}
	return &Proxy{
		store:          opts.Store,
		registry:       registry,
		auditor:        auditor,
		recorder:       opts.Recorder,
		policy:         opts.Policy,
		guard:          opts.Guard,
		issued:         opts.Credentials,
		clientUpgrader: newClientUpgrader(buildOriginAllowlist(opts.AllowedOrigins, opts.PublicURL)),
	}
}

// buildOriginAllowlist mirrors api.server.consoleOrigins: a wildcard CORS entry
// contributes nothing here (it is a licence for cross-origin fetches, not for
// a WebSocket to accept a handshake from anywhere), and the public URL is
// always added so a console with no explicit CORS origin configured — the
// common case, since it is same-origin — still has one.
func buildOriginAllowlist(configured []string, publicURL string) []string {
	origins := make([]string, 0, len(configured)+1)
	for _, origin := range configured {
		if origin = strings.TrimRight(strings.TrimSpace(origin), "/"); origin != "" && origin != "*" {
			origins = append(origins, origin)
		}
	}
	if trimmed := strings.TrimRight(strings.TrimSpace(publicURL), "/"); trimmed != "" {
		origins = append(origins, trimmed)
	}
	return origins
}

// originAllowed reports whether an Origin header value names one of the
// allowlisted origins. Matching is case-insensitive on the whole value, which
// is what a scheme+host origin string calls for.
func originAllowed(allowlist []string, origin string) bool {
	for _, candidate := range allowlist {
		if strings.EqualFold(candidate, origin) {
			return true
		}
	}
	return false
}

// Handle serves one proxied Kubernetes API request. It is mounted behind the
// normal JWT middleware, so by the time it runs the caller is authenticated —
// what is left to decide is which cluster and which namespaces they may touch.
func (p *Proxy) Handle(c *gin.Context) {
	started := time.Now()

	user, cluster, grant, ok := p.resolve(c)
	if !ok {
		return
	}

	path := c.Param("path")
	if path == "" {
		path = "/"
	}
	// The in-page terminal authenticates with a query parameter because a
	// browser cannot set headers on a WebSocket. Strip it here so the session
	// token reaches neither the target cluster nor the audit trail.
	if raw := strippedQuery(c.Request.URL.RawQuery); raw != "" {
		path += "?" + raw
	}

	event := Event{
		At:                 started.UTC(),
		UserID:             user.ID,
		Username:           user.Username,
		ClusterID:          cluster.ID,
		Cluster:            cluster.Name,
		Verb:               VerbFor(c.Request.Method, path),
		Method:             c.Request.Method,
		Path:               path,
		ImpersonatedUser:   user.Username,
		ImpersonatedGroups: ImpersonationGroups(grant.K8sRole),
	}
	parsed := ParsePath(path)
	event.Namespace = parsed.Namespace
	event.Resource = parsed.Resource

	if allowed, reason := allowedNamespace(grant, parsed, path); !allowed {
		p.fail(c, &event, http.StatusForbidden, reason)
		return
	}

	// The safety policies. They sit here — after the grant is resolved, before a
	// tunnel is looked up — for two reasons: a refusal must not depend on whether
	// an agent happens to be attached, and the record of it must carry the
	// identity the rest of the trail is keyed by.
	decision := p.guardAPIRequest(cluster.ID, c.Request.Method, path)
	if decision.Blocked() {
		p.failGuardrail(c, &event, decision)
		return
	}
	// A non-interactive exec carries its command in the query string and runs it
	// without ever typing anything, so the keystroke guard downstream would never
	// see it. This is the shape a script uses, and the easy way around a rule that
	// only watched a terminal.
	if parsed.Subresource == "exec" {
		if command := execCommand(path); command != "" {
			if verdict := p.guardCommand(cluster.ID, command); verdict.Blocked() {
				p.failGuardrail(c, &event, verdict)
				return
			} else if verdict != nil && decision == nil {
				decision = verdict
			}
		}
	}
	// A warn-only match still has to reach the trail: reading what a rule would
	// have caught is the whole reason to run one in warn before arming it.
	noteGuardrail(&event, decision)

	// port-forward is carried in its WebSocket form only. A SPDY client is
	// refused here rather than left to hang on an upgrade nobody will answer.
	if reason, refused := unsupportedStream(c.Request, parsed); refused {
		p.fail(c, &event, http.StatusNotImplemented, reason)
		return
	}

	tunnel, ok := p.registry.Get(cluster.ID)
	if !ok {
		p.fail(c, &event, http.StatusServiceUnavailable, ErrNoTunnel.Error())
		return
	}

	header := forwardHeaders(c.Request.Header, user, grant)

	// exec and attach need a live channel in both directions; a watch or a
	// followed log needs one that stays open. Both are streams, and neither
	// fits the request/response pair below.
	switch {
	case wantsUpgrade(parsed):
		event.Streaming = true
		// One id for the whole session, carried on both of its audit records and
		// on its recording — that is what makes the trail and the replay one
		// thing rather than two lists a person has to line up by timestamp.
		event.SessionID = newSessionID()
		p.serveUpgradeStream(c, tunnel, &event, header, offeredSubprotocols(parsed), parsed, nil)
		return
	case wantsBodyStream(parsed, path):
		event.Streaming = true
		p.serveBodyStream(c, tunnel, &event, header)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, maxRequestBody))
	if err != nil {
		p.fail(c, &event, http.StatusRequestEntityTooLarge, "request body is too large to proxy")
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), proxyTimeout)
	defer cancel()

	resp, err := tunnel.Do(ctx, &Request{
		Method: c.Request.Method,
		Path:   path,
		Header: header,
		Body:   body,
	})
	if err != nil {
		status, message := tunnelFailure(err)
		p.fail(c, &event, status, message)
		return
	}
	if resp.Error != "" {
		p.fail(c, &event, http.StatusBadGateway, "the in-cluster agent could not reach the API server: "+resp.Error)
		return
	}

	event.Status = resp.Status
	event.Duration = time.Since(started)
	p.auditor.Record(c.Request.Context(), event)

	writeResponse(c, resp)
}

// Call performs one proxied API request on behalf of a user without an inbound
// HTTP request driving it. The resource endpoints use it to read a cluster's
// state on demand — same impersonation, same namespace enforcement and same
// audit trail as a kubectl call, because a read from the UI is not a special
// case that deserves less scrutiny.
//
// auditDiff is carried on the resulting audit row's Event.Diff, but only if
// the call actually succeeds — see the comment on Event.Diff for why that
// check lives here rather than in whoever calls Call. Every caller but the
// manifest write passes nil.
func (p *Proxy) Call(
	ctx context.Context,
	user *db.User,
	cluster *db.Cluster,
	grant db.UserClusterAccess,
	method, path string,
	body []byte,
	auditDiff []byte,
) (*Response, error) {
	started := time.Now()
	parsed := ParsePath(path)

	event := Event{
		At:                 started.UTC(),
		UserID:             user.ID,
		Username:           user.Username,
		ClusterID:          cluster.ID,
		Cluster:            cluster.Name,
		Verb:               VerbFor(method, path),
		Method:             method,
		Path:               path,
		Namespace:          parsed.Namespace,
		Resource:           parsed.Resource,
		ImpersonatedUser:   user.Username,
		ImpersonatedGroups: ImpersonationGroups(grant.K8sRole),
	}

	record := func(status int, err error) {
		event.Status = status
		event.Duration = time.Since(started)
		if err != nil {
			event.Error = err.Error()
		}
		p.auditor.Record(ctx, event)
	}

	if allowed, reason := allowedNamespace(grant, parsed, path); !allowed {
		err := errors.New(reason)
		record(http.StatusForbidden, err)
		return nil, &CallError{Status: http.StatusForbidden, Message: reason}
	}

	// The same policies apply to a read the console makes on a user's behalf. A
	// guardrail that only covered kubectl would be a control the UI walks around,
	// and every write in the resource API — scale, restart, apply — comes through
	// here rather than through Handle.
	decision := p.guardAPIRequest(cluster.ID, method, path)
	noteGuardrail(&event, decision)
	if decision.Blocked() {
		message := decision.Message()
		record(http.StatusForbidden, errors.New(message))
		return nil, &CallError{Status: http.StatusForbidden, Message: message, Policy: decision.Policy, Scope: decision.Scope}
	}

	tunnel, ok := p.registry.Get(cluster.ID)
	if !ok {
		record(http.StatusServiceUnavailable, ErrNoTunnel)
		return nil, &CallError{Status: http.StatusServiceUnavailable, Message: ErrNoTunnel.Error()}
	}

	ctx, cancel := context.WithTimeout(ctx, proxyTimeout)
	defer cancel()

	header := map[string][]string{
		"Accept":            {"application/json"},
		"Impersonate-User":  {user.Username},
		"Impersonate-Group": ImpersonationGroups(grant.K8sRole),
	}
	if len(body) > 0 {
		header["Content-Type"] = []string{contentTypeFor(method)}
	}

	resp, err := tunnel.Do(ctx, &Request{Method: method, Path: path, Header: header, Body: body})
	if err != nil {
		status, message := tunnelFailure(err)
		record(status, err)
		return nil, &CallError{Status: status, Message: message}
	}
	if resp.Error != "" {
		record(http.StatusBadGateway, errors.New(resp.Error))
		return nil, &CallError{
			Status:  http.StatusBadGateway,
			Message: "the in-cluster agent could not reach the API server: " + resp.Error,
		}
	}

	// The diff is attached only once the round trip is known to have
	// succeeded — see diffForAuditRow.
	event.Diff = diffForAuditRow(auditDiff, resp.Status)
	record(resp.Status, nil)
	return resp, nil
}

/*
 * Watch is Call's long-lived sibling: one streaming GET, held open for as long
 * as the caller wants it, under the same impersonated identity.
 *
 * It exists for the one read whose cost is wrong as a request. Everything else
 * in the resource API is a question somebody asked once — a list, a manifest, a
 * describe — and paying a round trip for it is the right price. A cluster's
 * Events are the opposite: the question ("what is happening") is continuous, the
 * answer changes constantly, and asking it as a list means either a repeated
 * cluster-wide read on every page view or a background poll that runs whether
 * anybody is looking. A watch is one call that keeps answering, which is the
 * shape the API server is built for and the shape every controller uses.
 *
 * Everything Call establishes still holds and for the same reasons: the
 * namespace scope is checked before the tunnel is looked up, guardrails apply
 * (a watch is an API call like any other), and the audit trail gets a record.
 * The record is written at **open** rather than at close: a stream that runs for
 * six hours must appear in the trail when it starts, not when it ends, or the
 * one call worth noticing is the one the trail is missing.
 */
func (p *Proxy) Watch(
	ctx context.Context,
	user *db.User,
	cluster *db.Cluster,
	grant db.UserClusterAccess,
	path string,
) (*Stream, error) {
	started := time.Now()
	parsed := ParsePath(path)

	event := Event{
		At:        started.UTC(),
		UserID:    user.ID,
		Username:  user.Username,
		ClusterID: cluster.ID,
		Cluster:   cluster.Name,
		// A watch is its own verb in Kubernetes' vocabulary and in the audit
		// policy's, so it is recorded as one rather than folded into a list.
		Verb:               VerbFor(http.MethodGet, path),
		Method:             http.MethodGet,
		Path:               path,
		Namespace:          parsed.Namespace,
		Resource:           parsed.Resource,
		ImpersonatedUser:   user.Username,
		ImpersonatedGroups: ImpersonationGroups(grant.K8sRole),
	}

	record := func(status int, err error) {
		event.Status = status
		event.Duration = time.Since(started)
		if err != nil {
			event.Error = err.Error()
		}
		p.auditor.Record(ctx, event)
	}

	if allowed, reason := allowedNamespace(grant, parsed, path); !allowed {
		err := errors.New(reason)
		record(http.StatusForbidden, err)
		return nil, &CallError{Status: http.StatusForbidden, Message: reason}
	}

	decision := p.guardAPIRequest(cluster.ID, http.MethodGet, path)
	noteGuardrail(&event, decision)
	if decision.Blocked() {
		message := decision.Message()
		record(http.StatusForbidden, errors.New(message))
		return nil, &CallError{Status: http.StatusForbidden, Message: message}
	}

	tunnel, ok := p.registry.Get(cluster.ID)
	if !ok {
		record(http.StatusServiceUnavailable, ErrNoTunnel)
		return nil, &CallError{Status: http.StatusServiceUnavailable, Message: ErrNoTunnel.Error()}
	}

	// Only the open is bounded — that is the whole point of the call. The stream
	// then lives as long as `ctx` does, which is the caller's business.
	stream, head, err := tunnel.OpenStream(ctx, &StreamOpen{
		Method: http.MethodGet,
		Path:   path,
		Header: map[string][]string{
			"Accept":            {"application/json"},
			"Impersonate-User":  {user.Username},
			"Impersonate-Group": ImpersonationGroups(grant.K8sRole),
		},
	})
	if err != nil {
		status, message := tunnelFailure(err)
		record(status, err)
		return nil, &CallError{Status: status, Message: message}
	}
	if head.Status < 200 || head.Status >= 300 {
		// The API server refused it — a grant that may not watch events, most
		// likely. The stream is closed here so the caller only ever holds an open
		// one, and the refusal travels as the status it was.
		stream.Close(nil)
		record(head.Status, errors.New("the cluster refused the watch"))
		return nil, &CallError{Status: head.Status, Message: "the cluster refused the watch"}
	}

	record(head.Status, nil)
	return stream, nil
}

// diffForAuditRow decides whether a manifest write's diff should ride along on
// this call's audit row. It exists as its own function, rather than an inline
// condition, so the one rule the roadmap calls load-bearing — a stored diff
// is "never stored for a refused write" — is something a test can pin without
// standing up a live tunnel: a guardrail block or a tunnel failure never
// reaches the call site at all, and a refusal the cluster itself sends back —
// a 403 from its own RBAC, a 409 on a stale resourceVersion — is exactly the
// case a bare 2xx check below has to catch.
func diffForAuditRow(auditDiff []byte, status int) []byte {
	if len(auditDiff) == 0 || status < 200 || status >= 300 {
		return nil
	}
	return auditDiff
}

// CallError is a proxied call that failed, carrying the status the HTTP layer
// should hand back.
//
// Policy and Scope are set only when the refusal came from a guardrail rather
// than from the cluster's own RBAC or the tunnel itself — the same distinction
// failGuardrail draws for the streaming path. Without them, callResourceWith
// could only hand the manifest editor a bare "forbidden" string, and the
// editor's confirmation step (roadmap "a diff before the write") has nothing
// to explain against the offending field without knowing which policy fired.
type CallError struct {
	Status  int
	Message string
	Policy  string
	Scope   string
}

func (e *CallError) Error() string { return e.Message }

// resolve identifies the caller and the cluster, and loads the grant that
// governs the call. It writes the error response itself when it refuses.
func (p *Proxy) resolve(c *gin.Context) (*db.User, *db.Cluster, db.UserClusterAccess, bool) {
	var noGrant db.UserClusterAccess

	claims, ok := auth.ClaimsFrom(c)
	if !ok {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing bearer token"})
		return nil, nil, noGrant, false
	}

	// A generated kubeconfig is a file on somebody's laptop and its token is a
	// JWT, which cannot be withdrawn by its own means. So the credential's id is
	// matched against the published register before anything else is read: it is
	// a lock-free map lookup, and a withdrawn file has no business costing this
	// server two database reads. A credential that carries no id — a session, a
	// machine token, or a kubeconfig minted before the register existed — is not
	// revoked here, because nothing in the register can name it.
	if p.issued.Observe(claims.ID) {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
			"error": "this kubeconfig has been revoked; generate a new one from the KubeMG console",
		})
		return nil, nil, noGrant, false
	}

	ctx := c.Request.Context()
	user, err := p.store.UserByID(ctx, claims.UserID)
	if errors.Is(err, db.ErrNotFound) {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "user no longer exists"})
		return nil, nil, noGrant, false
	}
	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "could not load user"})
		return nil, nil, noGrant, false
	}
	if !user.IsActive {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "this account is disabled"})
		return nil, nil, noGrant, false
	}
	user.Normalize()

	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid cluster id"})
		return nil, nil, noGrant, false
	}

	cluster, err := p.store.ClusterByID(ctx, uint(id))
	if errors.Is(err, db.ErrNotFound) {
		c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "cluster not found"})
		return nil, nil, noGrant, false
	}
	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "could not load cluster"})
		return nil, nil, noGrant, false
	}
	if !cluster.UsesAgent() {
		c.AbortWithStatusJSON(http.StatusConflict, gin.H{
			"error": "this cluster is registered for direct API access; generate a kubeconfig instead",
		})
		return nil, nil, noGrant, false
	}

	grant, ok := p.grantFor(c, user, cluster.ID)
	if !ok {
		return nil, nil, noGrant, false
	}
	return user, cluster, grant, true
}

// grantFor returns the access governing this call. An admin is granted
// cluster-admin over every cluster, matching how the rest of KubeMG reads
// admin privilege.
func (p *Proxy) grantFor(c *gin.Context, user *db.User, clusterID uint) (db.UserClusterAccess, bool) {
	if user.IsAdmin() {
		return db.UserClusterAccess{
			UserID:    user.ID,
			ClusterID: clusterID,
			K8sRole:   db.K8sRoleClusterAdmin,
		}, true
	}

	grants, err := p.store.AccessForUser(c.Request.Context(), user.ID)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "could not load cluster access"})
		return db.UserClusterAccess{}, false
	}

	grant, ok := grants[clusterID]
	if !ok {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "no access to this cluster"})
		return db.UserClusterAccess{}, false
	}
	return grant, true
}

// fail records the refusal in the audit trail before answering. A denied call
// is exactly the kind of thing an audit trail exists to hold.
func (p *Proxy) fail(c *gin.Context, event *Event, status int, message string) {
	event.Status = status
	event.Error = message
	event.Duration = time.Since(event.At)
	p.auditor.Record(c.Request.Context(), *event)

	c.AbortWithStatusJSON(status, gin.H{"error": message})
}

// contentTypeFor names the media type a body is sent as.
//
// Everything here is `application/json` except a PATCH, which the API server
// refuses outright with a 415 unless the body's *patch strategy* is named in the
// content type — `application/json` is not one of the four it accepts. A merge
// patch is what every caller through Call sends: a document naming the fields it
// changes and leaving the rest of the object alone, which is the whole reason to
// patch rather than to read-modify-write.
//
// This is a one-line rule and it fails silently without it: the two callers that
// patch are both best-effort, so a 415 reads as an annotation that quietly never
// lands rather than as an error anybody sees.
func contentTypeFor(method string) string {
	if method == http.MethodPatch {
		return "application/merge-patch+json"
	}
	return "application/json"
}

// ImpersonationGroups renders the Kubernetes groups asserted for a grant. The
// role travels as a group rather than as a local allow/deny check on purpose:
// the target cluster's RBAC stays the authority on what the role can do.
func ImpersonationGroups(k8sRole string) []string {
	if k8sRole == "" {
		k8sRole = db.K8sRoleView
	}
	return []string{GroupPrefix + k8sRole, GroupAllUsers}
}

// forwardHeaders builds the header set the agent replays. Client-supplied
// credentials and impersonation headers are dropped first, then KubeMG asserts
// the identity it decided on.
func forwardHeaders(src http.Header, user *db.User, grant db.UserClusterAccess) map[string][]string {
	out := map[string][]string{}
	for name, values := range src {
		if slices.ContainsFunc(hopByHopHeaders, func(h string) bool {
			return strings.EqualFold(h, name)
		}) {
			continue
		}
		if slices.ContainsFunc(impersonationHeaders, func(h string) bool {
			return strings.EqualFold(h, name)
		}) {
			continue
		}
		out[http.CanonicalHeaderKey(name)] = slices.Clone(values)
	}

	out["Impersonate-User"] = []string{user.Username}
	out["Impersonate-Group"] = ImpersonationGroups(grant.K8sRole)
	return out
}

// allowedNamespace enforces the namespace scope attached to a grant. An
// unscoped grant allows everything the impersonated role allows; a scoped grant
// refuses anything that does not name one of its namespaces, including
// cluster-wide reads, because a cluster-wide list would return objects from
// namespaces the grant does not cover.
func allowedNamespace(grant db.UserClusterAccess, parsed APIPath, path string) (bool, string) {
	allowed := grant.NamespaceList()
	if len(allowed) == 0 {
		return true, ""
	}
	if isDiscovery(path) {
		return true, ""
	}
	if parsed.Namespace == "" {
		return false, "your access to this cluster is limited to " + strings.Join(allowed, ", ") +
			"; this request is not scoped to a namespace"
	}
	if !slices.Contains(allowed, parsed.Namespace) {
		return false, "namespace " + parsed.Namespace + " is outside your granted scope"
	}
	return true, ""
}

// isDiscovery reports whether a path is one of the read-only endpoints kubectl
// needs before it can resolve any resource at all.
func isDiscovery(path string) bool {
	if i := strings.IndexByte(path, '?'); i >= 0 {
		path = path[:i]
	}
	// Discovery is the root of each prefix and its version listings; a path
	// that continues into a resource is a real API call, not discovery.
	for _, prefix := range discoveryPrefixes {
		if path == prefix || path == prefix+"/" {
			return true
		}
		if strings.HasPrefix(path, prefix+"/") && ParsePath(path).Resource == "" {
			return true
		}
	}
	return false
}

// strippedQuery removes KubeMG's own query parameters from a proxied URL. Only
// the session token qualifies today: it is an artefact of how browsers open a
// WebSocket and means nothing to the Kubernetes API server.
func strippedQuery(raw string) string {
	if raw == "" || !strings.Contains(raw, auth.QueryTokenParam) {
		return raw
	}

	kept := make([]string, 0, strings.Count(raw, "&")+1)
	for _, param := range strings.Split(raw, "&") {
		name, _, _ := strings.Cut(param, "=")
		if name == auth.QueryTokenParam {
			continue
		}
		kept = append(kept, param)
	}
	return strings.Join(kept, "&")
}

// wantsUpgrade reports whether a call needs a bidirectional session rather than
// a response body. These are the interactive subresources. port-forward is one
// of them only in its WebSocket form; unsupportedStream has already turned away
// the SPDY shape by the time this runs.
func wantsUpgrade(parsed APIPath) bool {
	switch parsed.Subresource {
	case "exec", "attach", "portforward":
		return true
	default:
		return false
	}
}

// offeredSubprotocols is what to ask the cluster for when the client named
// nothing itself. The two families are not interchangeable: a port-forward
// negotiated as a channel protocol is a session neither end can read.
func offeredSubprotocols(parsed APIPath) []string {
	if parsed.Subresource == "portforward" {
		return PortForwardSubprotocols
	}
	return ChannelSubprotocols
}

// wantsBodyStream reports whether a call returns a body that keeps arriving —
// a watch, or a followed log.
func wantsBodyStream(parsed APIPath, path string) bool {
	if parsed.Subresource == "log" && followRequested(path) {
		return true
	}
	return watchRequested(path)
}

// unsupportedStream flags what the tunnel still cannot carry.
//
// port-forward multiplexes arbitrary TCP inside one session, and Kubernetes
// offers two framings for that: the original SPDY/3.1 upgrade, and a WebSocket
// one (v2.portforward.k8s.io) that carries the same channel-prefixed stream.
// KubeMG carries the WebSocket shape, because the whole tunnel is a WebSocket
// and the frames pass through untouched; SPDY would mean implementing a second
// multiplexing protocol inside the first one to reach a transport Kubernetes is
// itself retiring. A SPDY client gets an honest 501 naming the way forward
// rather than a stalled upgrade.
func unsupportedStream(r *http.Request, parsed APIPath) (string, bool) {
	if parsed.Subresource != "portforward" || websocket.IsWebSocketUpgrade(r) {
		return "", false
	}
	return "port-forward over this gateway needs its WebSocket transport; " +
		"run kubectl with KUBECTL_PORT_FORWARD_WEBSOCKETS=true (default on Kubernetes 1.31 and later). " +
		"The SPDY transport is not proxied.", true
}

func followRequested(path string) bool {
	i := strings.IndexByte(path, '?')
	if i < 0 {
		return false
	}
	for _, param := range strings.Split(path[i+1:], "&") {
		if param == "follow" || param == "follow=true" || param == "follow=1" {
			return true
		}
	}
	return false
}

// tunnelFailure maps a tunnel error onto an HTTP status the client can act on.
func tunnelFailure(err error) (int, string) {
	switch {
	case errors.Is(err, ErrTunnelClosed), errors.Is(err, ErrNoTunnel):
		return http.StatusServiceUnavailable, "the in-cluster agent disconnected before the request completed"
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, "the target cluster did not answer in time"
	case errors.Is(err, context.Canceled):
		return http.StatusBadGateway, "the request was cancelled"
	default:
		return http.StatusBadGateway, "could not reach the target cluster through its agent"
	}
}

// writeResponse replays the API server's answer to the client verbatim, minus
// the headers that describe the hop rather than the payload.
func writeResponse(c *gin.Context, resp *Response) {
	for name, values := range resp.Header {
		if slices.ContainsFunc(hopByHopHeaders, func(h string) bool {
			return strings.EqualFold(h, name)
		}) {
			continue
		}
		// Content-Length is re-derived from the body we actually write.
		if strings.EqualFold(name, "Content-Length") {
			continue
		}
		for _, value := range values {
			c.Writer.Header().Add(name, value)
		}
	}

	c.Status(resp.Status)
	if len(resp.Body) > 0 {
		_, _ = c.Writer.Write(resp.Body)
	}
}
