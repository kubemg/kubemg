// Package jit is the just-in-time elevated access workflow: asking for a
// stronger Kubernetes role on a cluster for a bounded window, having somebody
// approve it, and having it end by itself.
//
// It exists as a package of its own rather than as handlers because the workflow
// is the interesting part and it has three entry points, not one. A decision can
// arrive from the console, from a signed chat callback, or — for the expiry —
// from nobody at all, and all three have to produce the same state transition,
// the same grant write and the same audit record. Putting that in the HTTP layer
// would mean three copies of it.
//
// Two rules hold throughout and are worth stating before the code:
//
//   - **Nothing here widens what a role can do.** An approval writes a grant that
//     the proxy then enforces exactly as it enforces a standing one: impersonated
//     down the tunnel, refused by the cluster's own RBAC if the cluster disagrees,
//     and audited. JIT decides *for how long*, not *what*.
//   - **An approval is a two-party act.** A requester cannot approve their own
//     request, whatever their role. That is the whole control: without it this is
//     a button that grants an admin cluster-admin, which they had anyway.
package jit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/bastion"
	"github.com/kubemg/kubemg/backend/pkg/db"
)

// Workflow errors. They are values rather than strings because the HTTP layer maps
// them onto status codes, and a workflow rule that reads as a 500 is a rule
// nobody can act on.
var (
	// ErrInvalid is a request that cannot be made at all: an unknown role, a
	// duration outside the bounds, a missing reason.
	ErrInvalid = errors.New("invalid request")
	// ErrConflict is a request that cannot be made *now*: a duplicate, or a
	// decision somebody else already took.
	ErrConflict = errors.New("conflicting request")
	// ErrForbidden is a caller who may not do this — the self-approval rule, and
	// a decision attempted by somebody with no standing to take it.
	ErrForbidden = errors.New("not permitted")
	// ErrNotFound is an unknown request id.
	ErrNotFound = errors.New("request not found")
)

// Audit verbs for the workflow. They are not Kubernetes verbs — nothing here
// touches a cluster — but they belong in the same trail as the calls the
// elevation goes on to make: "who was given production, by whom, and why" is the
// line an auditor reads *before* the calls, and having to look for it in a
// different system is how that reconstruction gets skipped.
//
// The names are hyphenated and short because the audit table's verb column is
// twenty characters, which is also why they read as verbs rather than as events.
const (
	VerbRequest = "jit-request"
	VerbApprove = "jit-approve"
	VerbReject  = "jit-reject"
	VerbRevoke  = "jit-revoke"
	VerbExpire  = "jit-expire"
)

// auditResource is what these records are about, in the trail's own vocabulary.
const auditResource = "jitrequests"

// Store is the persistence the workflow needs. It is narrow on purpose: the
// engine has no business reading users or clusters for anything except the
// validation and the denormalised names it writes into a request.
type Store interface {
	UserByID(ctx context.Context, id uint) (*db.User, error)
	ClusterByID(ctx context.Context, id uint) (*db.Cluster, error)
	AccessForUser(ctx context.Context, userID uint) (map[uint]db.UserClusterAccess, error)

	CreateJitRequest(ctx context.Context, request *db.JitRequest) error
	JitRequestByID(ctx context.Context, id string) (*db.JitRequest, error)
	ListJitRequests(ctx context.Context, filter db.JitRequestFilter) ([]db.JitRequest, error)
	PendingJitRequestFor(ctx context.Context, userID, clusterID uint) (*db.JitRequest, error)
	ActivateJitRequest(
		ctx context.Context, id string, decision db.JitDecision, grant db.UserClusterAccess,
	) (*db.JitRequest, error)
	FinishJitRequest(
		ctx context.Context, id string, from []string, status string, decision db.JitDecision,
	) (*db.JitRequest, error)
	ExpireJitRequests(ctx context.Context, now time.Time) ([]db.JitRequest, error)
	OrphanedJitRequests(ctx context.Context) ([]db.JitRequest, error)
}

// Notifier is told about requests and decisions so they reach the people who are
// not looking at the console. It is an interface here rather than a dependency on
// the alarm dispatcher because a notification failing must never fail a workflow
// step: the implementation is expected to return immediately and deliver on its
// own time.
type Notifier interface {
	NotifyAccessRequest(ctx context.Context, note ApprovalNote)
	NotifyAccessDecision(ctx context.Context, note DecisionNote)
}

// ApprovalNote is one pending request as a chat message needs it: what is being
// asked for, why, and the two things a recipient can act with — a link into the
// console and a signed token that lets an interactive button decide without one.
type ApprovalNote struct {
	RequestID  string
	Requester  string
	Cluster    string
	Role       string
	Namespaces []string
	Duration   time.Duration
	Reason     string
	// ConsoleURL opens the request in KubeMG, where the decision is taken under a
	// real session. It is the path that always works.
	ConsoleURL string
	// ApproveToken and RejectToken authorise one decision each through the webhook
	// callback. They are signed and expiring; see callback.go for why they are not
	// sufficient on their own.
	ApproveToken string
	RejectToken  string
	CallbackURL  string
}

// DecisionNote is what happened to a request, for the same audience.
type DecisionNote struct {
	RequestID string
	Requester string
	Cluster   string
	Role      string
	Status    string
	Decider   string
	Comment   string
	ExpiresAt *time.Time
}

// Options wires the engine.
type Options struct {
	Store Store
	// Auditor records the workflow. Nil turns those records off, which is what a
	// test that does not assert on them leaves it as — the workflow still works,
	// it is simply unobserved, and that is a wiring mistake rather than a mode.
	Auditor bastion.Auditor
	// Notify delivers approval requests and decisions to chat. Nil means the
	// console is the only place a request appears, which is a complete product —
	// so it is optional rather than required.
	Notify Notifier
	// CallbackSecret signs the approval tokens in a notification. Empty means no
	// token is minted and the notification carries console links only, which is
	// the right behaviour for a server with no secret to sign with: a link is
	// useless to an attacker and an unsigned token would be useless to us.
	CallbackSecret []byte
	// ConsoleURL is where a person goes to act on a request.
	ConsoleURL string
	// Logger is where the background sweeper reports.
	Logger *slog.Logger
	// Now is the clock, overridable for tests. One clock decides the expiry that is
	// written and the expiry that is enforced.
	Now func() time.Time
}

// Engine is the workflow.
type Engine struct {
	store   Store
	auditor bastion.Auditor
	notify  Notifier
	secret  []byte
	console string
	logger  *slog.Logger
	now     func() time.Time
}

// New builds an engine.
func New(opts Options) *Engine {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Engine{
		store:   opts.Store,
		auditor: opts.Auditor,
		notify:  opts.Notify,
		secret:  opts.CallbackSecret,
		console: strings.TrimRight(opts.ConsoleURL, "/"),
		logger:  logger,
		now:     now,
	}
}

// Input is a submitted request, before validation.
type Input struct {
	Requester       *db.User
	ClusterID       uint
	Role            string
	Namespaces      []string
	DurationMinutes int
	Reason          string
	// Context is what the HTTP layer knows and the workflow does not: the method
	// and path to record, so the audit record reads like the rest of the trail.
	Method string
	Path   string
}

// reasonBounds are what a justification has to be to be worth storing. The floor
// is low but not zero: "asdf" is not a reason, and a mandatory field people fill
// with one character is a field that has been switched off by convention.
const (
	minReasonLength = 10
	maxReasonLength = 1000
)

// CreateRequest validates and stores a request for elevated access.
func (e *Engine) CreateRequest(ctx context.Context, input Input) (*db.JitRequest, error) {
	if input.Requester == nil {
		return nil, fmt.Errorf("%w: no requester", ErrInvalid)
	}

	role := strings.TrimSpace(input.Role)
	if !slices.Contains(
		[]string{db.K8sRoleView, db.K8sRoleEdit, db.K8sRoleClusterAdmin}, role,
	) {
		return nil, fmt.Errorf("%w: role must be one of view, edit, cluster-admin", ErrInvalid)
	}

	if input.DurationMinutes < db.MinJitDurationMinutes ||
		input.DurationMinutes > db.MaxJitDurationMinutes {
		return nil, fmt.Errorf("%w: duration must be between %d minutes and %d hours",
			ErrInvalid, db.MinJitDurationMinutes, db.MaxJitDurationMinutes/60)
	}

	reason := strings.TrimSpace(input.Reason)
	if len(reason) < minReasonLength {
		return nil, fmt.Errorf(
			"%w: say why this access is needed — at least %d characters, and it is kept with the record",
			ErrInvalid, minReasonLength)
	}
	if len(reason) > maxReasonLength {
		return nil, fmt.Errorf("%w: the reason is too long", ErrInvalid)
	}

	cluster, err := e.store.ClusterByID(ctx, input.ClusterID)
	if errors.Is(err, db.ErrNotFound) {
		return nil, fmt.Errorf("%w: that cluster does not exist", ErrInvalid)
	}
	if err != nil {
		return nil, fmt.Errorf("load cluster: %w", err)
	}

	// One open ask per cluster. Without this the queue fills with the same
	// request from somebody who pressed the button twice, and an approver cannot
	// tell which one grants the window.
	if _, err := e.store.PendingJitRequestFor(ctx, input.Requester.ID, cluster.ID); err == nil {
		return nil, fmt.Errorf("%w: you already have a request waiting for %s",
			ErrConflict, cluster.Name)
	} else if !errors.Is(err, db.ErrNotFound) {
		return nil, fmt.Errorf("check for an existing request: %w", err)
	}

	// And nothing to ask for while an elevation is already live: re-requesting
	// would either extend a window an approver bounded or replace a role with a
	// weaker one, and neither is what anybody means by it.
	live, err := e.LiveFor(ctx, input.Requester.ID)
	if err != nil {
		return nil, err
	}
	for _, request := range live {
		if request.ClusterID == cluster.ID {
			return nil, fmt.Errorf(
				"%w: you already hold temporary %s on %s; hand it back first to ask for something else",
				ErrConflict, request.RequestedRole, cluster.Name)
		}
	}

	namespaces := db.JoinNamespaces(input.Namespaces)
	// Asking for what you already have permanently is not a request, it is a
	// no-op with an approver's time attached.
	if covered, err := e.alreadyCovered(ctx, input.Requester.ID, cluster.ID, role, namespaces); err != nil {
		return nil, err
	} else if covered {
		return nil, fmt.Errorf("%w: your standing access to %s already covers %s",
			ErrConflict, cluster.Name, role)
	}

	request := &db.JitRequest{
		ID:                newRequestID(),
		RequesterID:       input.Requester.ID,
		RequesterUsername: input.Requester.Username,
		ClusterID:         cluster.ID,
		ClusterName:       cluster.Name,
		RequestedRole:     role,
		Namespaces:        namespaces,
		DurationMinutes:   input.DurationMinutes,
		Reason:            reason,
		Status:            db.JitStatusPending,
	}
	if err := e.store.CreateJitRequest(ctx, request); err != nil {
		return nil, fmt.Errorf("store request: %w", err)
	}

	e.record(ctx, auditRecord{
		user:    input.Requester,
		request: request,
		verb:    VerbRequest,
		method:  input.Method,
		path:    input.Path,
		status:  201,
		detail:  reason,
	})
	e.announce(ctx, request)
	return request, nil
}

// alreadyCovered reports whether the requester's standing access is at least what
// they are asking for. It reads the resolved grant, so a role inherited from a
// group counts — the question is what they can do, not where it came from.
func (e *Engine) alreadyCovered(
	ctx context.Context, userID, clusterID uint, role, namespaces string,
) (bool, error) {
	access, err := e.store.AccessForUser(ctx, userID)
	if err != nil {
		return false, fmt.Errorf("resolve current access: %w", err)
	}
	current, ok := access[clusterID]
	if !ok {
		return false, nil
	}
	if roleRank(current.K8sRole) < roleRank(role) {
		return false, nil
	}
	// An unscoped standing grant covers any namespace selection; a scoped one
	// only covers a selection it contains, and covers nothing at all if the
	// request is unscoped.
	if current.Namespaces == "" {
		return true, nil
	}
	if namespaces == "" {
		return false, nil
	}
	held := current.NamespaceList()
	wantedList := db.UserClusterAccess{Namespaces: namespaces}.NamespaceList()
	for _, wanted := range wantedList {
		if !slices.Contains(held, wanted) {
			return false, nil
		}
	}
	return true, nil
}

// roleRank orders the three Kubernetes roles. It is a copy of the ranking in the
// db package rather than a call into it, because that one is unexported and this
// is a different question — "is this an elevation" rather than "which grant wins".
func roleRank(role string) int {
	switch role {
	case db.K8sRoleClusterAdmin:
		return 3
	case db.K8sRoleEdit:
		return 2
	case db.K8sRoleView:
		return 1
	default:
		return 0
	}
}

// Decision is who is deciding, and what they said.
type Decision struct {
	// Actor is the account taking the decision. It is never inferred from the
	// request being decided.
	Actor   *db.User
	Comment string
	// Via names a non-console origin — "slack" — and lands in the stored comment.
	// An approval that arrived over a webhook is a different fact from one taken
	// in the console, and the record should not have to be reconstructed from
	// timing.
	Via    string
	Method string
	Path   string
}

// ApproveRequest grants the window and activates the elevation.
func (e *Engine) ApproveRequest(
	ctx context.Context, id string, decision Decision,
) (*db.JitRequest, error) {
	request, err := e.load(ctx, id)
	if err != nil {
		return nil, err
	}
	if decision.Actor == nil {
		return nil, fmt.Errorf("%w: no approver", ErrForbidden)
	}
	// The whole control. An administrator asking for production still needs a
	// colleague, and a super admin is not exempt: the point is that two people
	// know, not that one of them is trusted.
	if decision.Actor.ID == request.RequesterID {
		return nil, fmt.Errorf("%w: you cannot approve your own access request", ErrForbidden)
	}
	if !decision.Actor.IsAdmin() {
		return nil, fmt.Errorf("%w: only an administrator may approve access requests", ErrForbidden)
	}
	if request.Decided() {
		return nil, fmt.Errorf("%w: this request is already %s", ErrConflict, request.Status)
	}

	now := e.now()
	expiresAt := now.Add(time.Duration(request.DurationMinutes) * time.Minute)
	grant := db.UserClusterAccess{
		UserID:     request.RequesterID,
		ClusterID:  request.ClusterID,
		K8sRole:    request.RequestedRole,
		Namespaces: request.Namespaces,
		Source:     db.GrantSourceJIT,
		ExpiresAt:  &expiresAt,
	}

	updated, err := e.store.ActivateJitRequest(ctx, id, db.JitDecision{
		ApproverID:       decision.Actor.ID,
		ApproverUsername: decision.Actor.Username,
		Comment:          comment(decision),
		At:               now,
		ExpiresAt:        &expiresAt,
	}, grant)
	if errors.Is(err, db.ErrConflict) {
		return nil, fmt.Errorf("%w: this request has already been decided", ErrConflict)
	}
	if err != nil {
		return nil, fmt.Errorf("activate request: %w", err)
	}

	e.record(ctx, auditRecord{
		user:    decision.Actor,
		request: updated,
		verb:    VerbApprove,
		method:  decision.Method,
		path:    decision.Path,
		status:  200,
		detail: fmt.Sprintf("granted %s to %s for %d minutes: %s",
			updated.RequestedRole, updated.RequesterUsername, updated.DurationMinutes, updated.Reason),
	})
	e.announceDecision(ctx, updated, decision)
	return updated, nil
}

// RejectRequest refuses a pending request.
//
// An administrator may reject anyone's; the requester may reject their own, which
// is how a request is cancelled. Those are the same transition and there is no
// reason to build a second one for it.
func (e *Engine) RejectRequest(
	ctx context.Context, id string, decision Decision,
) (*db.JitRequest, error) {
	request, err := e.load(ctx, id)
	if err != nil {
		return nil, err
	}
	if decision.Actor == nil {
		return nil, fmt.Errorf("%w: no approver", ErrForbidden)
	}
	own := decision.Actor.ID == request.RequesterID
	if !own && !decision.Actor.IsAdmin() {
		return nil, fmt.Errorf("%w: only an administrator may reject an access request", ErrForbidden)
	}
	if request.Decided() {
		return nil, fmt.Errorf("%w: this request is already %s", ErrConflict, request.Status)
	}

	now := e.now()
	updated, err := e.store.FinishJitRequest(ctx, id,
		[]string{db.JitStatusPending}, db.JitStatusRejected, db.JitDecision{
			ApproverID:       decision.Actor.ID,
			ApproverUsername: decision.Actor.Username,
			Comment:          comment(decision),
			At:               now,
		})
	if errors.Is(err, db.ErrConflict) {
		return nil, fmt.Errorf("%w: this request has already been decided", ErrConflict)
	}
	if err != nil {
		return nil, fmt.Errorf("reject request: %w", err)
	}

	e.record(ctx, auditRecord{
		user:    decision.Actor,
		request: updated,
		verb:    VerbReject,
		method:  decision.Method,
		path:    decision.Path,
		status:  200,
		detail:  comment(decision),
	})
	e.announceDecision(ctx, updated, decision)
	return updated, nil
}

// RevokeRequest ends a live elevation early.
//
// An administrator may revoke anybody's, and the holder may hand their own back:
// giving up privilege is never something to require permission for, and making it
// easy is what stops an elevation being left running "in case".
func (e *Engine) RevokeRequest(
	ctx context.Context, id string, decision Decision,
) (*db.JitRequest, error) {
	request, err := e.load(ctx, id)
	if err != nil {
		return nil, err
	}
	if decision.Actor == nil {
		return nil, fmt.Errorf("%w: no revoker", ErrForbidden)
	}
	own := decision.Actor.ID == request.RequesterID
	if !own && !decision.Actor.IsAdmin() {
		return nil, fmt.Errorf("%w: only an administrator may revoke somebody else's access", ErrForbidden)
	}
	if !slices.Contains(db.JitLiveStatuses, request.Status) {
		return nil, fmt.Errorf("%w: this request is %s, so there is nothing to revoke",
			ErrConflict, request.Status)
	}

	updated, err := e.store.FinishJitRequest(ctx, id,
		db.JitLiveStatuses, db.JitStatusRevoked, db.JitDecision{
			ApproverID:       decision.Actor.ID,
			ApproverUsername: decision.Actor.Username,
			Comment:          comment(decision),
			At:               e.now(),
		})
	if errors.Is(err, db.ErrConflict) {
		return nil, fmt.Errorf("%w: this elevation has already ended", ErrConflict)
	}
	if err != nil {
		return nil, fmt.Errorf("revoke request: %w", err)
	}

	e.record(ctx, auditRecord{
		user:    decision.Actor,
		request: updated,
		verb:    VerbRevoke,
		method:  decision.Method,
		path:    decision.Path,
		status:  200,
		detail: fmt.Sprintf("withdrew %s on %s from %s: %s",
			updated.RequestedRole, updated.ClusterName, updated.RequesterUsername, comment(decision)),
	})
	e.announceDecision(ctx, updated, decision)
	return updated, nil
}

// List returns requests, newest first.
func (e *Engine) List(ctx context.Context, filter db.JitRequestFilter) ([]db.JitRequest, error) {
	requests, err := e.store.ListJitRequests(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("list requests: %w", err)
	}
	return requests, nil
}

// LiveFor returns the elevations this user is holding right now, expiry included
// in the judgement — a row whose window has passed is not live even if the
// sweeper has not been round yet.
func (e *Engine) LiveFor(ctx context.Context, userID uint) ([]db.JitRequest, error) {
	requests, err := e.store.ListJitRequests(ctx, db.JitRequestFilter{
		Statuses:    db.JitLiveStatuses,
		RequesterID: userID,
	})
	if err != nil {
		return nil, fmt.Errorf("list live requests: %w", err)
	}
	now := e.now()
	return slices.DeleteFunc(requests, func(request db.JitRequest) bool {
		return !request.Live(now)
	}), nil
}

// Request loads one request.
func (e *Engine) Request(ctx context.Context, id string) (*db.JitRequest, error) {
	return e.load(ctx, id)
}

// Now is the engine's clock, exposed so that everything about one elevation is
// timed by the same one.
//
// It matters beyond testability: the window written at approval, the judgement of
// whether a row is still live, and the countdown a response reports are three
// readings of the same fact, and a caller reading its own wall clock instead would
// be able to report seconds remaining on a window this engine considers finished.
func (e *Engine) Now() time.Time { return e.now() }

func (e *Engine) load(ctx context.Context, id string) (*db.JitRequest, error) {
	request, err := e.store.JitRequestByID(ctx, strings.TrimSpace(id))
	if errors.Is(err, db.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load request: %w", err)
	}
	return request, nil
}

// comment renders the stored decision note, naming a non-console origin.
func comment(decision Decision) string {
	note := strings.TrimSpace(decision.Comment)
	if decision.Via == "" {
		return note
	}
	if note == "" {
		return "via " + decision.Via
	}
	return note + " (via " + decision.Via + ")"
}

/* ------------------------------------------------------------ expiry sweep --- */

// sweepInterval is how often the sweeper runs. It is not what closes a window —
// the resolver does that on the next request, to the second — so this is only
// about how quickly the console stops showing a countdown that has finished and
// how quickly the rows are tidied.
const sweepInterval = 30 * time.Second

// RunExpirer closes finished elevations until the context is cancelled.
//
// It sweeps once before its first tick, so a server that was down over an expiry
// tidies up at boot rather than at the end of its first interval.
func (e *Engine) RunExpirer(ctx context.Context) {
	e.Sweep(ctx)

	ticker := time.NewTicker(sweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.Sweep(ctx)
		}
	}
}

// Sweep expires what has run out and reconciles what has been revoked out from
// under a request. A failure is logged and left to the next pass rather than
// killing the goroutine: the enforcement does not depend on this running.
func (e *Engine) Sweep(ctx context.Context) {
	now := e.now()
	expired, err := e.store.ExpireJitRequests(ctx, now)
	if err != nil {
		if ctx.Err() == nil {
			e.logger.Warn("could not expire elevated access grants", slog.String("error", err.Error()))
		}
		return
	}
	for i := range expired {
		request := expired[i]
		e.record(ctx, auditRecord{
			user:    &db.User{ID: request.RequesterID, Username: request.RequesterUsername},
			request: &request,
			verb:    VerbExpire,
			method:  "SYSTEM",
			path:    "/jit/expire/" + request.ID,
			status:  200,
			detail: fmt.Sprintf("temporary %s on %s expired after %d minutes",
				request.RequestedRole, request.ClusterName, request.DurationMinutes),
		})
		e.logger.Info("elevated access expired",
			slog.String("request", request.ID),
			slog.String("username", request.RequesterUsername),
			slog.String("cluster", request.ClusterName),
			slog.String("role", request.RequestedRole))
	}

	// A request claiming to be live with no grant behind it is what an
	// administrator's blanket revoke leaves. Closing it out here rather than
	// pretending it is still running is the difference between the approvals page
	// being a source of truth and being a second guess.
	orphaned, err := e.store.OrphanedJitRequests(ctx)
	if err != nil {
		if ctx.Err() == nil {
			e.logger.Warn("could not reconcile elevated access grants",
				slog.String("error", err.Error()))
		}
		return
	}
	for i := range orphaned {
		request := orphaned[i]
		updated, err := e.store.FinishJitRequest(ctx, request.ID,
			db.JitLiveStatuses, db.JitStatusRevoked, db.JitDecision{
				Comment: "grant no longer present; access was revoked outside this request",
				At:      now,
			})
		if err != nil {
			if ctx.Err() == nil {
				e.logger.Warn("could not close an orphaned access grant",
					slog.String("request", request.ID),
					slog.String("error", err.Error()))
			}
			continue
		}
		e.record(ctx, auditRecord{
			user:    &db.User{ID: updated.RequesterID, Username: updated.RequesterUsername},
			request: updated,
			verb:    VerbRevoke,
			method:  "SYSTEM",
			path:    "/jit/reconcile/" + updated.ID,
			status:  200,
			detail:  updated.ApproverComment,
		})
	}
}

/* ---------------------------------------------------------------- records --- */

// auditRecord is the workflow's own vocabulary for one line in the trail.
type auditRecord struct {
	user    *db.User
	request *db.JitRequest
	verb    string
	method  string
	path    string
	status  int
	detail  string
}

// record writes one line. The cluster is carried on the record so these sit
// alongside the proxied calls the elevation goes on to make, in the same trail,
// filtered by the same cluster.
func (e *Engine) record(ctx context.Context, entry auditRecord) {
	if e.auditor == nil || entry.request == nil {
		return
	}
	user := entry.user
	if user == nil {
		user = &db.User{}
	}
	method := entry.method
	if method == "" {
		method = "POST"
	}
	path := entry.path
	if path == "" {
		path = "/api/v1/jit/requests/" + entry.request.ID
	}

	e.auditor.Record(ctx, bastion.Event{
		At:        e.now(),
		UserID:    user.ID,
		Username:  user.Username,
		ClusterID: entry.request.ClusterID,
		Cluster:   entry.request.ClusterName,
		Verb:      entry.verb,
		Method:    method,
		Path:      path,
		Namespace: strings.Join(entry.request.NamespaceList(), ","),
		Resource:  auditResource,
		Status:    entry.status,
		// The requested role and the reason are the parts worth reading back, and
		// Error is the only free-text field on the record. Using it for a
		// successful workflow step is a stretch of the field's name and a much
		// smaller one than losing the justification.
		Error: entry.detail,
	})
}

/* ---------------------------------------------------------- notifications --- */

// announce offers a new request to the notifier. It is a goroutine because
// delivery is an HTTP call to somebody else's endpoint and the person who pressed
// Request is waiting for a response.
func (e *Engine) announce(ctx context.Context, request *db.JitRequest) {
	if e.notify == nil {
		return
	}
	note := ApprovalNote{
		RequestID:  request.ID,
		Requester:  request.RequesterUsername,
		Cluster:    request.ClusterName,
		Role:       request.RequestedRole,
		Namespaces: request.NamespaceList(),
		Duration:   time.Duration(request.DurationMinutes) * time.Minute,
		Reason:     request.Reason,
		ConsoleURL: e.consoleLink(request.ID),
	}
	if len(e.secret) > 0 {
		expiry := e.now().Add(callbackTTL)
		note.CallbackURL = e.console + callbackPath
		note.ApproveToken = SignAction(e.secret, Action{
			RequestID: request.ID, Action: ActionApprove, Expires: expiry,
		})
		note.RejectToken = SignAction(e.secret, Action{
			RequestID: request.ID, Action: ActionReject, Expires: expiry,
		})
	}

	// A detached context: the request that triggered this is about to be answered,
	// and cancelling the notification with it would mean nobody is told.
	go e.notify.NotifyAccessRequest(context.WithoutCancel(ctx), note)
}

func (e *Engine) announceDecision(ctx context.Context, request *db.JitRequest, decision Decision) {
	if e.notify == nil {
		return
	}
	decider := ""
	if decision.Actor != nil {
		decider = decision.Actor.Username
	}
	go e.notify.NotifyAccessDecision(context.WithoutCancel(ctx), DecisionNote{
		RequestID: request.ID,
		Requester: request.RequesterUsername,
		Cluster:   request.ClusterName,
		Role:      request.RequestedRole,
		Status:    request.Status,
		Decider:   decider,
		Comment:   request.ApproverComment,
		ExpiresAt: request.ExpiresAt,
	})
}

// consoleLink is where a person acts on a request. It carries the id so the page
// opens on the one the message is about rather than on a queue somebody has to
// search.
func (e *Engine) consoleLink(id string) string {
	if e.console == "" {
		return ""
	}
	return e.console + "/access-requests?request=" + id
}
