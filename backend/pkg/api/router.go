package api

import (
	"context"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/kubemg/kubemg/backend/pkg/auditpolicy"
	"github.com/kubemg/kubemg/backend/pkg/credentials"
	"github.com/kubemg/kubemg/backend/pkg/auth"
	"github.com/kubemg/kubemg/backend/pkg/bastion"
	"github.com/kubemg/kubemg/backend/pkg/cache"
	"github.com/kubemg/kubemg/backend/pkg/db"
	"github.com/kubemg/kubemg/backend/pkg/guardrails"
	"github.com/kubemg/kubemg/backend/pkg/jit"
	"github.com/kubemg/kubemg/backend/pkg/k8s"
	"github.com/kubemg/kubemg/backend/pkg/observability"
)

// defaultSANamespace is where KubeMG provisions per-user service accounts when
// no namespace is configured.
const defaultSANamespace = "kubemg-system"

// defaultPublicURL is the dev stack's backend origin.
const defaultPublicURL = "http://localhost:8080"

// defaultAllowedOrigin is the Vite dev server.
var defaultAllowedOrigins = []string{"http://localhost:5173"}

// Store is the persistence surface the HTTP layer depends on.
type Store interface {
	UserByUsername(ctx context.Context, username string) (*db.User, error)
	UserByID(ctx context.Context, id uint) (*db.User, error)
	ClustersForUser(ctx context.Context, user *db.User) ([]db.Cluster, error)
	AccessForUser(ctx context.Context, userID uint) (map[uint]db.UserClusterAccess, error)
	Clusters(ctx context.Context) ([]db.Cluster, error)
	ClusterByID(ctx context.Context, id uint) (*db.Cluster, error)
	ClusterByAgentToken(ctx context.Context, token string) (*db.Cluster, error)
	CreateCluster(ctx context.Context, cluster *db.Cluster) error
	DeleteCluster(ctx context.Context, id uint) error
	UpdateClusterHealth(ctx context.Context, id uint, health db.ClusterHealth) error

	ListUsers(ctx context.Context) ([]db.User, error)
	CreateUser(ctx context.Context, user *db.User) error
	UpdateUser(ctx context.Context, id uint, update db.UserUpdate) (*db.User, error)
	SetUserActive(ctx context.Context, id uint, active bool) (*db.User, error)
	DeleteUser(ctx context.Context, id uint) error
	TouchLastLogin(ctx context.Context, id uint, at time.Time) error

	// Programmatic access. A service account is a User row (see db.User's
	// AccountType), so it needs no identity methods of its own — only its
	// credentials do.
	CreateMachineToken(ctx context.Context, token *db.MachineToken) error
	MachineTokenByHash(ctx context.Context, hash string) (*db.MachineToken, error)
	MachineTokenByID(ctx context.Context, id uint) (*db.MachineToken, error)
	ListMachineTokens(ctx context.Context, userID uint) ([]db.MachineToken, error)
	RevokeMachineToken(ctx context.Context, id uint, at time.Time) (*db.MachineToken, error)
	TouchMachineToken(ctx context.Context, id uint, at time.Time) error

	ListGroups(ctx context.Context) ([]db.GroupSummary, error)
	GroupByID(ctx context.Context, id uint) (*db.Group, error)
	CreateGroup(ctx context.Context, group *db.Group) error
	DeleteGroup(ctx context.Context, id uint) error
	AddGroupMember(ctx context.Context, groupID, userID uint) error
	RemoveGroupMember(ctx context.Context, groupID, userID uint) error

	ListUserAccess(ctx context.Context) ([]db.UserClusterAccess, error)
	ListGroupAccess(ctx context.Context) ([]db.GroupClusterAccess, error)
	AssignUserAccess(ctx context.Context, grant *db.UserClusterAccess) error
	AssignGroupAccess(ctx context.Context, grant *db.GroupClusterAccess) error
	RevokeUserAccess(ctx context.Context, userID, clusterID uint) error
	RevokeGroupAccess(ctx context.Context, groupID, clusterID uint) error

	// Alarm channels and rules. The dispatcher reads them too, through its own
	// narrower interface — see observability.AlarmStore.
	ListAlarmChannels(ctx context.Context) ([]db.AlarmChannel, error)
	AlarmChannelByID(ctx context.Context, id uint) (*db.AlarmChannel, error)
	CreateAlarmChannel(ctx context.Context, channel *db.AlarmChannel) error
	UpdateAlarmChannel(ctx context.Context, channel *db.AlarmChannel) error
	DeleteAlarmChannel(ctx context.Context, id uint) error
	ListAlarmRules(ctx context.Context) ([]db.AlarmRule, error)
	AlarmRuleByID(ctx context.Context, id uint) (*db.AlarmRule, error)
	CreateAlarmRule(ctx context.Context, rule *db.AlarmRule) error
	UpdateAlarmRule(ctx context.Context, rule *db.AlarmRule) error
	DeleteAlarmRule(ctx context.Context, id uint) error

	// Just-in-time access requests. The workflow itself lives in pkg/jit and
	// reaches these through its own narrower interface; what the HTTP layer needs
	// on top is the approver lookup, which is UserByUsername above.
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
	PruneJitRequests(ctx context.Context, before time.Time) (int64, error)

	// Command guardrails. The gateway reads them from a compiled snapshot, never
	// from here — this is the write side and the boot-time read that fills it.
	ListGuardrailPolicies(ctx context.Context) ([]db.GuardrailPolicy, error)
	GuardrailPolicyByID(ctx context.Context, id uint) (*db.GuardrailPolicy, error)
	CreateGuardrailPolicy(ctx context.Context, policy *db.GuardrailPolicy) error
	UpdateGuardrailPolicy(ctx context.Context, policy *db.GuardrailPolicy) error
	DeleteGuardrailPolicy(ctx context.Context, id uint) error

	// The register of issued kubeconfigs. Written by the generator, read by the
	// console, and — through RevokedKubeconfigTokenIDs — resolved into the
	// snapshot the gateway consults on every proxied call. See
	// pkg/db/kubeconfig_models.go for why revocation needs a row at all.
	CreateKubeconfigIssuance(ctx context.Context, issuance *db.KubeconfigIssuance) error
	ListKubeconfigIssuances(
		ctx context.Context, filter db.KubeconfigFilter,
	) ([]db.KubeconfigIssuance, int64, error)
	KubeconfigIssuanceByID(ctx context.Context, id uint) (*db.KubeconfigIssuance, error)
	RevokeKubeconfigIssuance(
		ctx context.Context, id uint, at time.Time, by uint, byName string,
	) (*db.KubeconfigIssuance, error)
	RevokeKubeconfigsForUser(
		ctx context.Context, userID uint, at time.Time, by uint, byName string,
	) ([]db.KubeconfigIssuance, error)
	RevokedKubeconfigTokenIDs(ctx context.Context, now time.Time) ([]string, error)
	TouchKubeconfigIssuance(ctx context.Context, tokenID string, at time.Time) error
	PruneKubeconfigIssuances(ctx context.Context, before time.Time) (int64, error)

	ListAuditEvents(ctx context.Context, filter db.AuditFilter) ([]db.AuditEvent, int64, error)
	AuditSummary(ctx context.Context, since time.Time) (db.AuditStats, error)
	PruneAuditEvents(ctx context.Context, before time.Time) (int64, error)

	// Recorded interactive sessions. The rows are the index; the recordings
	// themselves are files, which is why pruning hands them back rather than
	// only counting them.
	ListTerminalSessions(
		ctx context.Context, filter db.TerminalSessionFilter,
	) ([]db.TerminalSession, int64, error)
	TerminalSessionByID(ctx context.Context, id uint) (*db.TerminalSession, error)
	DeleteTerminalSession(ctx context.Context, id uint) error
	PruneTerminalSessions(ctx context.Context, before time.Time) ([]db.TerminalSession, error)

	Settings(ctx context.Context) (map[string]string, error)
	PutSettings(ctx context.Context, values map[string]string, updatedBy uint) error

	// Where charts may be installed from, and what those repositories hold.
	// Server-wide rather than per-cluster: see pkg/db/helm_models.go. The
	// credential never leaves the row, so nothing in this interface returns it
	// separately — a caller that needs it has the whole repository.
	HelmRepositories(ctx context.Context) ([]db.HelmRepository, error)
	HelmRepository(ctx context.Context, name string) (*db.HelmRepository, error)
	PutHelmRepository(ctx context.Context, repository *db.HelmRepository) error
	DeleteHelmRepository(ctx context.Context, name string) error
	UpdateHelmRepositoryHealth(
		ctx context.Context, id uint, status, message string, charts int, syncedAt *time.Time,
	) error
	ReplaceHelmCharts(ctx context.Context, repositoryID uint, charts []db.HelmChart) error
	HelmCharts(ctx context.Context, repositoryID uint, search string, limit int) ([]db.HelmChart, error)
	HelmChart(ctx context.Context, repositoryID uint, name string) (*db.HelmChart, error)

	// The application catalogue: named manifest bundles with a declared
	// parameter set. Server-wide rather than per-cluster, for the same reason
	// a chart repository is — see pkg/db/app_template_models.go.
	AppTemplates(ctx context.Context) ([]db.AppTemplate, error)
	AppTemplate(ctx context.Context, name string) (*db.AppTemplate, error)
	PutAppTemplate(ctx context.Context, template *db.AppTemplate) error
	DeleteAppTemplate(ctx context.Context, name string) error

	// AcquireLease is how a background job that reads a cluster decides whether
	// *this* replica is the one doing it. See pkg/db/lease.go: a false return
	// means somebody else holds it, an error means do not run.
	AcquireLease(ctx context.Context, name, holder string, ttl time.Duration) (bool, error)

	// Identity federation: the providers, the rules that say what an external
	// group is worth, and the sync that applies them on every federated sign-in.
	ListSSOProviders(ctx context.Context) ([]db.SSOProviderConfig, error)
	SSOProviderByID(ctx context.Context, id uint) (*db.SSOProviderConfig, error)
	CreateSSOProvider(ctx context.Context, provider *db.SSOProviderConfig) error
	UpdateSSOProvider(ctx context.Context, provider *db.SSOProviderConfig) error
	UpdateSSOProviderHealth(ctx context.Context, id uint, status, message string) error
	DeleteSSOProvider(ctx context.Context, id uint) error
	ListSSOMappings(ctx context.Context, providerID uint) ([]db.SSOGroupMapping, error)
	CreateSSOMapping(ctx context.Context, mapping *db.SSOGroupMapping) error
	UpdateSSOMapping(ctx context.Context, mapping *db.SSOGroupMapping) error
	DeleteSSOMapping(ctx context.Context, id uint) error
	SyncSSOUserAndGroups(
		ctx context.Context, provider *db.SSOProviderConfig, identity db.SSOIdentity,
	) (*db.SSOSyncResult, error)

	ObservabilitySources(ctx context.Context, clusterID uint) ([]db.ObservabilitySource, error)
	ObservabilitySource(ctx context.Context, clusterID uint, kind string) (*db.ObservabilitySource, error)
	PutObservabilitySource(ctx context.Context, source *db.ObservabilitySource) error
	UpdateSourceHealth(ctx context.Context, id uint, health db.SourceHealth) error
	DeleteObservabilitySource(ctx context.Context, clusterID uint, kind string) error

	// Which of a cluster's custom resources its sidebar offers. Rows are the
	// hidden set, so an install that never curated anything reads as empty.
	HiddenCRDs(ctx context.Context, clusterID uint) ([]string, error)
	SetHiddenCRDs(ctx context.Context, clusterID uint, resources []string, by uint) error

	ClusterConsoles(ctx context.Context, clusterID uint) ([]db.ClusterConsole, error)
	PutClusterConsole(ctx context.Context, console *db.ClusterConsole) error
	DeleteClusterConsole(ctx context.Context, clusterID uint, kind string) error

	// Workload security posture acknowledgements — see pkg/api/resources_posture.go.
	// The scan itself is stateless; this is the one thing it cannot recompute.
	ListPostureAcknowledgements(ctx context.Context, clusterID uint) ([]db.PostureAcknowledgement, error)
	AcknowledgePostureFinding(ctx context.Context, ack *db.PostureAcknowledgement) error
	UnacknowledgePostureFinding(ctx context.Context, clusterID uint, kind, namespace, name, rule string) error
}

// Options wires the router's dependencies.
type Options struct {
	Store Store
	JWT   *auth.Manager
	// Tokens mints short-lived credentials on target clusters. When nil, the
	// kubeconfig generator route is not registered.
	Tokens k8s.Issuer
	// Health probes target clusters. When nil, the check route is not
	// registered.
	Health k8s.Checker
	// SANamespace is the in-cluster namespace holding KubeMG's per-user service
	// accounts. Defaults to "kubemg-system".
	SANamespace string
	// AllowedOrigins are the browser origins permitted to call the API. A single
	// "*" entry allows any origin. Defaults to the Vite dev server.
	AllowedOrigins []string

	// Bastion accepts agent tunnels. When nil, the tunnel and proxy routes are
	// not registered and KubeMG behaves exactly as it did in Phase 1.
	Bastion *bastion.Server
	// Proxy replays kubectl traffic down those tunnels. Registered only
	// alongside a Bastion.
	Proxy *bastion.Proxy
	// PublicURL is the outside address of this server, baked into generated
	// agent install commands. Defaults to the Vite dev server's API origin.
	PublicURL string
	// AgentImage and AgentNamespace parameterise the generated manifests.
	AgentImage     string
	AgentNamespace string
	// BastionCA is the certificate an agent has to trust to dial this server,
	// baked into every rendered install package. Set it when the bastion serves
	// a certificate the public CAs do not vouch for — a self-signed one — and
	// leave it empty otherwise.
	BastionCA string
	// RecordingDir is where terminal session recordings live. It is the directory
	// a stored path is confined to before anything reads it, so the replay routes
	// answer "recording is not enabled" while it is empty rather than trusting
	// whatever a row happens to name.
	RecordingDir string
	// RecordingKey decrypts recordings on the way out. It has to be the key they
	// were written with; a recording written without one is read without one, so
	// this being empty is not an error until an encrypted file is opened.
	RecordingKey []byte
	// RecordingInput reports whether this server is collecting keystrokes as well
	// as output, so the console can tell an operator what is being recorded
	// *before* they type into a shell.
	RecordingInput bool
	// Auditor records what this server itself did, as opposed to what it proxied.
	// Today that is one thing and it is the sensitive one: who replayed or deleted
	// a recording of somebody else's session. Nil turns those records off, which
	// is what the tests that do not assert on them leave it as.
	Auditor bastion.Auditor
	// AuditRetentionDays is the boot-time default retention window, overridable
	// at runtime from the Settings page. Zero falls back to
	// defaultAuditRetentionDays.
	AuditRetentionDays int
	// AuditPolicy is the shared snapshot of which verbs reach the audit table and
	// whether sessions are recorded. This router resolves it from the settings and
	// publishes it; the gateway reads it. Nil means the gateway records everything,
	// which is what a server wired without it does.
	AuditPolicy *auditpolicy.Policy
	// Credentials is the shared snapshot of which issued kubeconfigs have been
	// withdrawn. This router resolves it from the register and publishes it; the
	// gateway reads it lock-free. Nil leaves the register readable and the revoke
	// routes registered but nothing enforced at the gateway, which is what a
	// server wired without it does — see pkg/credentials on why that direction is
	// the safe one.
	Credentials *credentials.Register
	// Guardrails is the shared snapshot of which calls and commands the gateway
	// refuses. This router resolves it from the database and publishes it; the
	// gateway reads it lock-free. Nil leaves the routes unregistered and nothing
	// enforced — a server without one refuses nothing, which is how every install
	// behaved before guardrails existed.
	Guardrails *guardrails.Engine
	// JIT is the just-in-time elevated access workflow. Nil leaves its routes
	// unregistered and nothing sweeping — a fleet that has not opted into approval
	// workflows then behaves exactly as it did before they existed, with standing
	// grants and nothing else.
	JIT *jit.Engine
	// JITCallbackSecret verifies the signed approval tokens a chat integration
	// posts back. It is the same secret the engine signs with; empty means the
	// callback refuses everything, which is the correct answer for a server that
	// cannot have minted a token.
	JITCallbackSecret []byte
	// Alarms delivers cluster events and refused actions to their configured
	// destinations. Nil leaves the alarm routes unregistered and nothing polling —
	// the console then says the dispatcher is not running rather than offering rules
	// that could never fire.
	Alarms *observability.Dispatcher
	// ReadCacheTTL is how long a live read is served from memory before it is
	// asked of the cluster again. Zero takes cache.DefaultTTL; a negative value
	// turns the cache off, so every read is a tunnel call as it was before.
	ReadCacheTTL time.Duration
	// EventCacheTTL is the same window for the events timeline, which earns one
	// of its own: nothing an operator does through KubeMG writes an Event, so
	// there is no write for a stale entry to hide, and it is the one page a whole
	// team opens at the same moment — during an incident, on the API server
	// already having a bad day. Zero takes defaultEventCacheTTL.
	EventCacheTTL time.Duration
	// EventScanLimit is how many events one timeline read will walk before it
	// declares the answer partial. It is the knob that decides what a page view
	// costs on a cluster holding tens of thousands of them; zero takes
	// maxEventScan.
	EventScanLimit int
	// Version is the release this binary was built as, stamped at link time. It
	// is reported to any signed-in caller so the console can name what it is
	// talking to: the one fact an operator has to have to hand before reading a
	// changelog or opening an issue. Empty falls back to unknownVersion rather
	// than to a number this build cannot vouch for.
	Version string
	// Deployment describes the parts of this install that were settled before the
	// router existed and cannot be changed from inside it — TLS, the signing key's
	// origin, whether recordings are encrypted. The setup wizard reports them
	// rather than pretending to own them; see setup.go.
	Deployment Deployment
	// Background scopes the housekeeping goroutines that run alongside the
	// handlers — today just the audit retention pruner. Left nil, as the tests
	// leave it, nothing is started and the router is purely request-driven.
	Background context.Context
	// Logger is where those goroutines report. Defaults to slog's default.
	Logger *slog.Logger
}

// tunnels is the slice of the bastion registry the HTTP layer needs: whether a
// given cluster has an agent attached right now.
type tunnels interface {
	Connected(clusterID uint) bool
}

type server struct {
	store Store
	// instanceID identifies this process for the duration of its life. It is what
	// a background job holds a lease as, so a restart is a new holder rather than
	// something that renews a claim its previous incarnation took.
	instanceID     string
	jwt            *auth.Manager
	tokens         k8s.Issuer
	health         k8s.Checker
	tunnels        tunnels
	proxy          *bastion.Proxy
	saNamespace    string
	publicURL      string
	agentImage     string
	agentNamespace string
	bastionCA      string
	// recordings is the directory terminal recordings are read back from. Empty
	// means this server is not recording sessions.
	recordings     string
	recordingKey   []byte
	recordingInput bool
	// auditor records this server's own sensitive reads. See Options.Auditor.
	auditor            bastion.Auditor
	auditRetentionDays int
	// auditPolicy is published from the settings; see Options.AuditPolicy.
	auditPolicy *auditpolicy.Policy
	// guardrails is the compiled rule set the gateway enforces; see
	// Options.Guardrails. Nil means this server does not manage them.
	guardrails *guardrails.Engine
	// credentials is the published set of withdrawn kubeconfigs; see
	// Options.Credentials.
	credentials *credentials.Register
	// alarms is the dispatcher, or nil when this server runs without one.
	alarms *observability.Dispatcher
	// jit is the elevated-access workflow, or nil when this server runs without
	// it; see Options.JIT. jitCallbackSecret verifies a decision that arrived from
	// chat rather than from a session.
	jit               *jit.Engine
	jitCallbackSecret []byte
	logger            *slog.Logger
	// reads holds recently-answered live reads, keyed by caller and question.
	// Nil turns caching off entirely; see cachedRead.
	reads *cache.Cache[cachedResponse]
	// eventCacheTTL and eventScanLimit are what one events read is allowed to
	// cost; see Options and readTTL.
	eventCacheTTL  time.Duration
	eventScanLimit int
	// events holds one watch-fed buffer per cluster, started lazily the first
	// time somebody opens a timeline on it; see events_watch.go. Nil means this
	// server answers timelines with the paginated read alone.
	events *eventWatcher
	// background is what a watch's lifetime is scoped to — it outlives the
	// request that started it, so it cannot hang off a gin context.
	background context.Context
	// allowedOrigins is where a browser app may live. Federation reads it as the
	// set of consoles a finished sign-in may be handed back to, which is what
	// keeps the callback from being an open redirect for session tokens.
	allowedOrigins []string
	// ssoFlows holds sign-ins that have left for an IdP and not yet come back.
	ssoFlows *flowStore
	// deployment is what this process was started with; see Options.Deployment.
	deployment Deployment
	// version is the release this binary was built as; see Options.Version.
	version string
}

// NewRouter builds the KubeMG HTTP router. Authenticated routes are only
// registered when both a store and a token manager are supplied.
func NewRouter(opts Options) *gin.Engine {
	router := gin.New()
	router.Use(gin.Recovery())
	router.Use(cors.New(corsConfig(opts.AllowedOrigins)))
	router.Use(securityHeaders(opts.Deployment.TLSEnabled))

	router.GET("/health", healthHandler)

	if opts.Store == nil || opts.JWT == nil {
		return router
	}

	saNamespace := opts.SANamespace
	if saNamespace == "" {
		saNamespace = defaultSANamespace
	}
	publicURL := strings.TrimRight(opts.PublicURL, "/")
	if publicURL == "" {
		publicURL = defaultPublicURL
	}

	retention := opts.AuditRetentionDays
	if retention < minAuditRetentionDays {
		retention = defaultAuditRetentionDays
	}

	s := &server{
		store:              opts.Store,
		instanceID:         uuid.NewString(),
		jwt:                opts.JWT,
		tokens:             opts.Tokens,
		health:             opts.Health,
		proxy:              opts.Proxy,
		saNamespace:        saNamespace,
		publicURL:          publicURL,
		agentImage:         opts.AgentImage,
		agentNamespace:     opts.AgentNamespace,
		bastionCA:          opts.BastionCA,
		recordings:         strings.TrimSpace(opts.RecordingDir),
		recordingKey:       opts.RecordingKey,
		recordingInput:     opts.RecordingInput,
		auditor:            opts.Auditor,
		auditRetentionDays: retention,
		auditPolicy:        opts.AuditPolicy,
		guardrails:         opts.Guardrails,
		credentials:        opts.Credentials,
		alarms:             opts.Alarms,
		jit:                opts.JIT,
		jitCallbackSecret:  opts.JITCallbackSecret,
		logger:             opts.Logger,
		allowedOrigins:     opts.AllowedOrigins,
		ssoFlows:           newFlowStore(),
		deployment:         opts.Deployment,
		version:            strings.TrimSpace(opts.Version),
	}
	if opts.Bastion != nil {
		s.tunnels = opts.Bastion.Registry()
	}
	// The register learns that a credential is still in use from the gateway, not
	// from a route. Installing the writer here keeps pkg/credentials ignorant of
	// persistence and keeps the write off the proxied call's own goroutine.
	if s.credentials != nil {
		s.credentials.SetToucher(s.credentialToucher())
	}
	// A repeated read costs a tunnel round trip, an impersonated API call and an
	// audit record. Holding the answer for a few seconds is what makes the
	// console feel like one surface rather than forty round trips.
	if opts.ReadCacheTTL >= 0 {
		s.reads = cache.New[cachedResponse](opts.ReadCacheTTL)
	}
	// The events timeline's own two bounds. They are read here rather than at the
	// call site so a deployment tuning them for a large cluster changes one place,
	// and so a zero value means "the default" rather than "no budget at all" —
	// which for the scan limit would be a page view that walks the whole cluster.
	s.eventCacheTTL = opts.EventCacheTTL
	if s.eventCacheTTL <= 0 {
		s.eventCacheTTL = defaultEventCacheTTL
	}
	s.eventScanLimit = opts.EventScanLimit
	if s.eventScanLimit <= 0 {
		s.eventScanLimit = maxEventScan
	}
	// The timeline's buffer. It needs both a proxy to watch through and a
	// background context to outlive the request that starts a watch — without
	// either, timelines fall back to the paginated read, which is what the tests
	// and any server built without a proxy get.
	if opts.Proxy != nil && opts.Background != nil {
		s.events = newEventWatcher()
		s.background = opts.Background
	}
	if opts.Background != nil {
		// The audit table is the one thing here that grows without an operator
		// touching it, so enforcing its retention is a server responsibility
		// rather than a cron job someone has to remember to install.
		go s.startAuditPruner(opts.Background)
		// What reaches that table is a setting, and the gateway reads it from
		// memory on every call — so it has to be resolved here and republished.
		if s.auditPolicy != nil {
			go s.startAuditPolicyRefresher(opts.Background)
		}
		// The same handoff for the safety policies. It publishes once before
		// ticking, so a restarted server enforces its rules from the first
		// request rather than from the first tick.
		if s.guardrails != nil {
			go s.startGuardrailRefresher(opts.Background)
		}
		// And for the credential register. A revoke republishes on the replica
		// that served it; this is what carries it to the others, and what makes a
		// restarted server honour the revocations it already holds from its first
		// proxied call.
		if s.credentials != nil {
			go s.startCredentialRefresher(opts.Background)
		}
		// Cluster events only reach an alarm if something goes and reads them.
		// Nothing is read until a cluster-event rule exists; see alarms_watch.go.
		if s.alarms != nil && opts.Proxy != nil {
			go s.startAlarmWatcher(opts.Background)
		}
		// Stops the event watches nobody is reading. It is a sweeper rather than
		// a timer per cluster, for the reason the JIT expirer is: one loop that
		// knows about all of them is easier to reason about than N that each know
		// about one.
		if s.events != nil {
			go s.startEventWatchSweeper(opts.Background)
		}
		// Chart repositories go stale on their own. One replica refreshes them;
		// see helm_index.go for why that is a lease rather than a timer per
		// process.
		go s.startHelmIndexSync(opts.Background)
		// An elevation ends by itself, and the resolver already refuses it the
		// moment its window passes. This is what closes the rows out and stops the
		// console counting down something that has finished.
		if s.jit != nil {
			go s.jit.RunExpirer(opts.Background)
		}
	}
	// A service account's credential is verified against a stored row rather
	// than parsed, which is what makes it revocable — see
	// machineTokenVerifier. Everything past the middleware sees one shape of
	// claims and cannot tell the two credentials apart.
	requireAuth := auth.RequireAuth(s.jwt, newMachineTokenVerifier(s.store))
	requireAdmin := auth.RequireRole(db.RoleAdmin)

	if opts.Bastion != nil {
		// Agents authenticate on their own registration token, so this route
		// sits outside the JWT middleware. It is the only inbound entry point
		// the Phase 2 architecture adds.
		router.GET("/agent/v1/tunnel", opts.Bastion.HandleAgent)

		// The installer is fetched by kubectl, which cannot carry a KubeMG
		// session; the registration token in the path is the credential.
		install := router.Group("/install/:token")
		install.GET("/agent.yaml", s.installManifest)
		install.GET("/kustomize.tar.gz", s.installArchive)
	}

	v1 := router.Group("/api/v1")
	{
		v1.POST("/auth/login", s.login)
		v1.GET("/auth/me", requireAuth, s.me)
		// Rotating your own password is not an administrative act, so it does not
		// live under /users/:id — that route is an administrator editing somebody
		// else's account. The handler requires the current password, which is why
		// a live session is not enough to change it.
		v1.POST("/auth/password", requireAuth, s.changePassword)

		// First-run setup. The state route is unauthenticated by necessity — the
		// sign-in page has to know whether this server has been configured before
		// anybody has a session — and is narrow for the same reason the public SSO
		// list is: it answers one boolean and carries nothing else. The other two
		// are admin-only and read or stamp what the wizard needs.
		setup := v1.Group("/setup")
		setup.GET("/state", s.setupState)
		setup.GET("/preflight", requireAuth, requireAdmin, s.setupPreflight)
		setup.POST("/complete", requireAuth, requireAdmin, s.completeSetup)

		// Federated sign-in. Every route here is unauthenticated by necessity —
		// nobody has a session yet — so each one is narrow: the list carries no
		// configuration, the callbacks are single-use against a server-held
		// flow, and the SP metadata is a public document by design.
		//
		// They hang off a static "providers" segment rather than off
		// /auth/sso/:id, because gin cannot have a static and a param child at
		// the same level and the list has to live somewhere.
		sso := v1.Group("/auth/sso/providers")
		sso.GET("", s.listSSOProvidersPublic)
		sso.GET("/:id/login", s.startSSOLogin)
		// LDAP has no redirect: the credentials are posted here and checked
		// against the directory.
		sso.POST("/:id/login", s.ldapLogin)
		// OIDC comes back as a GET with a code, SAML as a POST with an assertion.
		sso.GET("/:id/callback", s.ssoCallback)
		sso.POST("/:id/callback", s.ssoCallback)
		sso.GET("/:id/metadata", s.ssoMetadata)

		clusters := v1.Group("/clusters", requireAuth)
		clusters.GET("", s.listClusters)
		clusters.GET("/:id", s.showCluster)
		clusters.POST("", requireAdmin, s.createCluster)
		clusters.DELETE("/:id", requireAdmin, s.deleteCluster)
		if s.health != nil {
			clusters.POST("/:id/check", requireAdmin, s.checkCluster)
		}
		// Direct mode mints a token on the cluster; agent mode issues a
		// proxy-scoped KubeMG token instead. Either dependency is enough to
		// register the route — the handler branches on the cluster's mode and
		// answers 424 for the combination it cannot serve.
		if s.tokens != nil || opts.Proxy != nil {
			clusters.POST("/:id/kubeconfig/generate", s.generateKubeconfig)
		}
		// How long a credential may be asked to live is server-wide rather than
		// per cluster, so it is not under /clusters/:id — a per-cluster path
		// would read as a per-cluster policy.
		v1.GET("/kubeconfig/policy", requireAuth, s.kubeconfigPolicy)

		// The register of what has actually been handed out. Reading follows the
		// audit trail's rule — everybody may read, a non-admin sees only their own
		// rows and the query parameter cannot widen that — because revoking a file
		// you know you lost must not require finding an administrator, and you
		// cannot revoke what you cannot see. The handlers enforce the other half:
		// revoking somebody else's is administrative, revoking your own never is.
		kubeconfigs := v1.Group("/kubeconfigs", requireAuth)
		kubeconfigs.GET("", s.listKubeconfigs)
		// The blanket action is registered before the parameterised route so
		// "revoke-all" is never read as a credential id.
		kubeconfigs.POST("/revoke-all", s.revokeAllKubeconfigs)
		kubeconfigs.POST("/:id/revoke", s.revokeKubeconfig)

		// Which release this is, for the console's footer. Signed-in rather than
		// public: see serverVersion.
		v1.GET("/version", requireAuth, s.serverVersion)
		if opts.Bastion != nil {
			clusters.GET("/:id/kustomize", requireAdmin, s.clusterKustomize)
		}

		// Where this cluster's metrics and logs actually come from. The Metrics
		// API read below answers "right now"; a series backend is what answers
		// "since when", and it is registered per cluster because that is where
		// it lives. Reading the configuration is open to anyone the cluster is
		// granted to — you cannot be shown a chart from a source you cannot know
		// exists — while changing it is administrative, and the credential never
		// travels back out.
		// Where charts may be installed from. Deliberately **not** under
		// /clusters: what this installation is allowed to reach out to and
		// download templates from is a fact about the installation, not about
		// any one cluster. Writing is an egress decision and so admin-only;
		// reading the catalogue is open to anyone signed in, because an install
		// form cannot discover the list by being refused.
		repositories := v1.Group("/helm/repositories", requireAuth)
		repositories.GET("", s.listHelmRepositories)
		repositories.GET("/:name/charts", s.listHelmCharts)
		repositories.PUT("/:name", requireAdmin, s.putHelmRepository)
		repositories.DELETE("/:name", requireAdmin, s.deleteHelmRepository)
		repositories.POST("/:name/sync", requireAdmin, s.syncHelmRepository)

		// A template is a fact about the installation, not about a cluster —
		// the same reasoning that keeps a chart repository out of
		// /clusters/:id — so it sits beside /helm/repositories rather than
		// under one. It takes the same read/write split for the same reason:
		// declaring one is administrative, and an install form that could not
		// list what is available would be a form nobody could use.
		templates := v1.Group("/app-templates", requireAuth)
		templates.GET("", s.listAppTemplates)
		// Registered before /:name so "draft" can never be mistaken for a
		// template's own name.
		templates.POST("/draft", s.draftAppTemplate)
		templates.GET("/:name", s.showAppTemplate)
		templates.PUT("/:name", requireAdmin, s.putAppTemplate)
		templates.DELETE("/:name", requireAdmin, s.deleteAppTemplate)
		templates.POST("/:name/render", s.renderAppTemplate)

		sources := clusters.Group("/:id/observability")
		sources.GET("", s.listObservabilitySources)
		sources.PUT("/sources/:kind", requireAdmin, s.putObservabilitySource)
		sources.DELETE("/sources/:kind", requireAdmin, s.deleteObservabilitySource)
		// test checks a draft nobody has saved yet, which is what makes the
		// wizard's "check connection" honest; check re-runs the stored one and
		// records the verdict.
		sources.POST("/sources/:kind/test", requireAdmin, s.testObservabilitySource)
		sources.POST("/sources/:kind/check", requireAdmin, s.checkObservabilitySource)

		// Where the *other* consoles for this cluster are — the Grafana that
		// answers a question the fixed catalogue cannot, the Argo CD that owns
		// half the workloads in Explore. Same rule as the datasources: readable
		// by anyone the cluster is granted to, registered by an admin. KubeMG
		// stores an address and no session, and never proxies either tool.
		// Which custom resources this cluster's Explore sidebar offers. Read as
		// wide as the cluster is granted — you have to be able to tell "no Istio
		// here" from "somebody took it off the list" — and written by an admin.
		visibility := clusters.Group("/:id/crd-visibility")
		visibility.GET("", s.showClusterCRDVisibility)
		visibility.PUT("", requireAdmin, s.putClusterCRDVisibility)

		consoles := clusters.Group("/:id/consoles")
		consoles.GET("", s.listClusterConsoles)
		consoles.PUT("/:kind", requireAdmin, s.putClusterConsole)
		consoles.DELETE("/:kind", requireAdmin, s.deleteClusterConsole)

		// The query path: reading history out of the backend the rows above
		// name. Open to anyone the cluster is granted to, because the scope is
		// enforced *inside* the query — a caller never sends one, they name a
		// chart from a fixed catalogue or a set of Kubernetes names, and KubeMG
		// builds the query around the namespaces their grant covers. There is no
		// cluster RBAC to fall back on here: a metrics backend has never heard of
		// the caller and answers whatever it is asked.
		// A chart is the one read a browser repeats without being asked to: a
		// resize, a legend toggle or a tab coming back re-renders the same
		// window. A range query is also the most expensive read here, so it is
		// cached on the same terms — a chart genuinely asking for a different
		// window still misses, because the window is part of the key.
		sources.GET("/metrics/query", s.cachedRead(), s.queryMetrics)
		// The comparison table, cached on the same terms and for the same
		// reason — it costs two queries rather than one, and a page carrying a
		// chart row and a table asks for both on every render.
		sources.GET("/metrics/compare", s.cachedRead(), s.compareMetrics)
		sources.GET("/logs/query", s.queryLogs)

		if opts.Proxy != nil {
			// Most clusters are already running one of these. Looking first is
			// the difference between metrics that get connected and metrics that
			// stay a task nobody does.
			sources.GET("/discover", requireAdmin, s.discoverObservabilitySources)
		}
		if opts.Proxy != nil {
			// kubectl's server URL points here, so every verb has to land on
			// the same handler.
			clusters.Any("/:id/proxy/*path", opts.Proxy.Handle)

			// Live cluster state, read on demand through the same tunnel and
			// under the same impersonated identity as a kubectl call — the UI
			// gets no privileged shortcut.
			// cachedRead sits in front of the whole group: every read here is
			// keyed by caller and question and served from memory for a few
			// seconds, and every write here drops the cluster's entries so a
			// scale or a restart is visible in the next list.
			resources := clusters.Group("/:id/resources", s.cachedRead())

			// How many of each kind the caller can see, read at limit=1 so the
			// cost is flat in the size of the cluster rather than proportional
			// to it. One batched route rather than a field on every list: the
			// sidebar asks about thirty kinds at once and must not pay thirty
			// round trips, and a number beside a nav entry must never cost what
			// opening the list costs.
			resources.GET("/counts", s.countResourceKinds)

			resources.GET("/namespaces", s.listNamespaces)
			resources.GET("/workloads", s.listWorkloads)
			resources.GET("/pods", s.listPods)
			resources.GET("/pods/:pod", s.showPod)
			resources.GET("/pods/:pod/logs", s.podLogs)

			// Which pods a workload owns, so their logs can be read together.
			// It resolves the workload's own selector rather than accepting one
			// from the caller, and the logs themselves are still the per-pod
			// reads above — one per pod, audited one per pod.
			resources.GET("/workload/pods", s.listWorkloadPods)

			// A Deployment/StatefulSet/DaemonSet's own rollout history —
			// `kubectl rollout history` over the ReplicaSets or
			// ControllerRevisions it owns, resolved the same ownership-first
			// way workload/pods is. See resources_rollout.go.
			resources.GET("/workload/history", s.showWorkloadHistory)

			// The rest of the inventory behind the Explore sidebar: one route
			// per list an operator can be looking at. The cluster-scoped ones
			// refuse a namespace-scoped grant, since a cluster-wide list would
			// reach past it.
			resources.GET("/deployments", s.listWorkloadsOf("Deployment"))
			resources.GET("/statefulsets", s.listWorkloadsOf("StatefulSet"))
			resources.GET("/daemonsets", s.listWorkloadsOf("DaemonSet"))
			resources.GET("/jobs", s.listJobs)
			resources.GET("/cronjobs", s.listCronJobs)

			resources.GET("/services", s.listServices)
			resources.GET("/ingresses", s.listIngresses)
			// Gateway API and Istio are optional: a cluster without them
			// answers with an empty list marked unavailable, not an error.
			resources.GET("/httproutes", s.listHTTPRoutes)
			resources.GET("/virtualservices", s.listVirtualServices)

			// NetworkPolicies: the fixed-inventory list, plus the derivation
			// worth building beside it — a workload's own view of the
			// policies that select it, and a namespace's own coverage
			// summary. Both are read-only reads over the same policy objects
			// the list above shows; neither traces a live connection or
			// speaks for the cluster's CNI, which each response states.
			resources.GET("/networkpolicies", s.listNetworkPolicies)
			resources.GET("/networkpolicies/reachability", s.networkPolicyReachability)
			resources.GET("/networkpolicies/coverage", s.networkPolicyCoverage)

			// ReplicaSets are what a stuck rollout is actually about — the
			// Deployment above them only states an intention — and they were
			// already being read on the rollout-history path without ever
			// being listable.
			resources.GET("/replicasets", s.listReplicaSets)

			// The three objects that explain a refusal. A pod a quota rejects
			// never becomes a pod, so nothing in the workload lists shows it
			// and the event that said why has scrolled away; a PDB is what a
			// drain blocks on and what a rollout stalls against. All three are
			// reads like every other list here.
			resources.GET("/resourcequotas", s.listResourceQuotas)
			resources.GET("/limitranges", s.listLimitRanges)
			resources.GET("/poddisruptionbudgets", s.listPodDisruptionBudgets)

			// HorizontalPodAutoscalers, and the workload one of them owns. The
			// second route is what keeps the scale control honest: it wrote a
			// replica count an autoscaler reverts within the minute, and
			// nothing anywhere said an autoscaler was involved.
			resources.GET("/horizontalpodautoscalers", s.listAutoscalers)
			resources.GET("/workload/autoscaler", s.workloadAutoscaler)

			resources.GET("/persistentvolumes", s.listPersistentVolumes)
			resources.GET("/persistentvolumeclaims", s.listPersistentVolumeClaims)
			resources.GET("/storageclasses", s.listStorageClasses)
			resources.GET("/configmaps", s.listConfigMaps)
			// Secrets are listed as metadata only; no value reaches a response.
			resources.GET("/secrets", s.listSecrets)

			// One key of one Secret, in the clear. It is deliberately *not* in
			// the cached group and never will be: the value is the one thing
			// this product has always kept out of a response, and a cache is a
			// second copy of it in a second place. It needs a capability of its
			// own on top of the cluster's RBAC, and it writes its own audit
			// record before the bytes leave. See resources_secret_value.go.
			clusters.GET("/:id/resources/secret/value", s.revealSecretValue)

			resources.GET("/crds", s.listCRDs)
			resources.GET("/nodes", s.listNodes)

			// The target cluster's own RBAC. KubeMG's permissions matrix governs
			// KubeMG's authorization; what a grant is *worth* is decided here, by
			// the cluster, and until these routes existed the console could not
			// show it. They are reads like every other list — KubeMG never writes
			// a Role or a Binding, because a tool with its own separate permission
			// model writing the cluster's is how the two silently diverge.
			resources.GET("/roles", s.listRoles)
			resources.GET("/clusterroles", s.listClusterRoles)
			resources.GET("/rolebindings", s.listRoleBindings)
			resources.GET("/clusterrolebindings", s.listClusterRoleBindings)
			resources.GET("/serviceaccounts", s.listServiceAccounts)

			// The answer an inventory cannot give: the authorizer's own verdict on
			// a named subject. It is a POST because a SubjectAccessReview is a
			// `create` in RBAC's eyes — so a grant that may not create one is
			// refused by the cluster, which is correct and is surfaced as the
			// cluster's refusal. Deliberately outside the cached group: it is a
			// write down the tunnel, and the middleware would drop the cluster's
			// cached reads on every question asked.
			clusters.POST("/:id/resources/access-review", s.accessReview)
			resources.GET("/access-review/verbs", s.accessReviewVerbs)
			// Who KubeMG impersonates for this caller on this cluster — the
			// subject to ask a review about to find out what your own grant is
			// actually worth here.
			resources.GET("/access-review/identity", s.grantIdentity)

			// Anything a CRD serves. There cannot be a route per kind here —
			// which CRDs exist is a property of the cluster, discovered from its
			// own CRD list — so this one names the API instead, built from three
			// validated components and read down the same impersonated tunnel.
			resources.GET("/custom", s.listCustomResources)

			// Helm keeps its releases as labelled Secrets and nothing else, so
			// these are the secrets list read through the same impersonated
			// tunnel with the payload decoded here. Writing values appends a
			// revision the way an upgrade does; it records what Helm will start
			// from and renders nothing, which every response says.
			helm := resources.Group("/helm/releases")
			helm.GET("", s.listHelmReleases)
			// Installing is a create on the collection, which is why it is a
			// POST here rather than a route of its own: a release is an object
			// this cluster did not have and now does. Nothing about it is
			// admin-only — the rendered objects go down the impersonated tunnel
			// one at a time and the cluster's own RBAC decides each of them,
			// exactly as it does for a manifest an operator types.
			helm.POST("", s.installHelmRelease)
			helm.POST("/:name/upgrade", s.upgradeHelmRelease)
			helm.GET("/:name/values", s.showHelmReleaseValues)
			helm.PUT("/:name/values", s.updateHelmReleaseValues)
			// History is the other half of the list: the list dedupes to the
			// current revision because that is what is installed, and this is
			// what the release has been. Rollback is the values write with its
			// values read out of that history rather than off the wire.
			helm.GET("/:name/history", s.showHelmReleaseHistory)
			helm.POST("/:name/rollback", s.rollbackHelmRelease)
			// And removing one. The release's own recorded manifest names the
			// objects it installed; each is deleted on its own call, with its
			// own RBAC answer and its own audit record, and the release's
			// Secrets go last so a partial failure leaves a release that still
			// describes what is there. See resources_helm_uninstall.go.
			helm.DELETE("/:name", s.uninstallHelmRelease)

			// One object in full, as the YAML an operator already reads. The
			// PUT is the only write path in the resource API; it goes down the
			// same impersonated tunnel, so the cluster's RBAC decides whether
			// the caller may actually change anything.
			resources.GET("/object", s.showResourceObject)
			resources.PUT("/object", s.updateResourceObject)
			// And bringing one into existence: the same kind key, addressed at
			// the collection rather than at an object, because there is no
			// object yet. `kubectl create -f` was the one thing the tunnel
			// carried that the console could not do — see resources_create.go.
			resources.POST("/object", s.createResourceObject)
			// And removing it. Same address, same fixed kind table, same
			// impersonated call — so it reaches nothing the read does not, and
			// the cluster's RBAC and the command guardrails both still decide.
			resources.DELETE("/object", s.deleteResourceObject)
			// The confirmation step's diff, computed fresh against whatever the
			// cluster holds right now rather than against the object the editor
			// opened on. It is a POST because a manifest of arbitrary size travels
			// in the body, but it writes nothing — deliberately outside the cached
			// resources group all the same, since a diff has to be computed on the
			// live object every time it is asked for, the same reasoning as
			// access-review below.
			clusters.POST("/:id/resources/object/diff", s.previewResourceObjectDiff)

			// The workload writes that are not worth hand-editing a manifest
			// for. All three are read-modify-writes down the same impersonated
			// tunnel, conditional on the resourceVersion they read, so they add
			// no reach the manifest editor did not have. Suspend is the
			// CronJob's own control: it owns Jobs rather than pods, so there is
			// nothing to scale and nothing to roll.
			resources.POST("/scale", s.scaleWorkload)
			resources.POST("/restart", s.restartWorkload)
			resources.POST("/suspend", s.suspendWorkload)
			// Firing a schedule now. It is a create with no new reach — the Job
			// is built from the CronJob's own jobTemplate and posted to the
			// Jobs collection down the same impersonated tunnel — which is why
			// it sits beside the other workload writes rather than beside the
			// generic create route.
			resources.POST("/cronjob/run", s.runCronJob)
			// A node's own off switch: spec.unschedulable, the same
			// read-modify-write shape as suspend, refused up front for a
			// namespace-scoped grant because a Node is cluster-scoped. Drain
			// is deliberately not this route — it stops new pods landing,
			// nothing already running is moved.
			resources.POST("/node/schedulable", s.setNodeSchedulable)
			// Undoing one — `kubectl rollout undo`. The same read-modify-write
			// shape as the three above, restoring a revision's pod template
			// from workload/history's own list rather than from anything the
			// caller sends.
			resources.POST("/workload/rollback", s.rollbackWorkload)

			// `kubectl describe`: the same object, addressed the same way, plus
			// the events the cluster recorded against it. Those events are the
			// part neither the list nor the manifest has — a spec is what was
			// asked for, and only an event says why it did not happen.
			resources.GET("/describe", s.describeResource)

			// The same events, without an object to ask about. Describe answers
			// "why is *this* not ready"; this answers "what broke in the last
			// fifteen minutes", which is the question somebody opens the console
			// with — and it was unanswerable while events could only be reached
			// through an object you already suspected. Grouped by involved
			// object, so a failing Deployment is one entry rather than forty.
			resources.GET("/events", s.listClusterEvents)

			// Workload security posture: seven fixed rules over fields these
			// same lists already fetch (privileged containers, hostPath,
			// hostNetwork/hostPID, an undeclared non-root user, missing
			// resource limits, an automounted default ServiceAccount token,
			// and a namespace with no NetworkPolicy). A read like every other
			// one above — impersonated, scoped, cached — that adds no
			// permission and no dependency; see resources_posture.go.
			resources.GET("/posture", s.postureScan)
			// Acknowledging a finding is a write against KubeMG's own database
			// rather than the cluster, but it mutes a security control until
			// someone reverses it — the same reasoning that keeps
			// access-review and the object diff outside the cached group
			// applies here too, and doubly so: caching a write's own response
			// makes no sense, and this one needs no cache invalidation on the
			// cluster's resources at all since nothing on the cluster changed.
			clusters.POST("/:id/resources/posture/ack", s.acknowledgePostureFinding)
			clusters.DELETE("/:id/resources/posture/ack", s.unacknowledgePostureFinding)

			// Live utilisation from the cluster's own Metrics API. It rides the
			// same tunnel, grant and audit trail as the lists above; a cluster
			// with no metrics-server answers "unavailable" rather than failing.
			// Cached on the same terms as the lists above. metrics-server
			// resamples every 15s or so, so a reading a few seconds old is
			// the same reading — and the console polls these on a timer.
			metrics := clusters.Group("/:id/metrics", s.cachedRead())
			metrics.GET("/nodes", s.nodeMetrics)
			metrics.GET("/pods", s.podMetrics)
			metrics.GET("/pods/:pod", s.showPodMetrics)
			// Allocation rather than consumption: what the scheduler has
			// already promised away, against what the node has. It sits in
			// this group because it reads the same two lists through the same
			// tunnel and answers the question the usage figures cannot — a
			// node at 30% CPU that will not take another pod. Cluster-wide by
			// nature, so a namespace-scoped grant is refused, exactly as it is
			// on node metrics above.
			metrics.GET("/capacity", s.clusterCapacity)
		}

		// Identity and access management is an administrative surface only.
		// Programmatic identities: a CI pipeline's release stage, a release bot,
		// anything that holds a credential for months rather than a session. They
		// are administrative in both directions — creating one is creating an
		// account, and issuing its credential is handing out standing access to a
		// cluster — so the whole surface is admin-only.
		machineAccounts := v1.Group("/machine-accounts", requireAuth, requireAdmin)
		machineAccounts.GET("", s.listMachineAccounts)
		machineAccounts.POST("", s.createMachineAccount)
		machineAccounts.PATCH("/:id/status", s.setMachineAccountStatus)
		machineAccounts.DELETE("/:id", s.deleteMachineAccount)
		machineAccounts.GET("/:id/tokens", s.listMachineTokens)
		machineAccounts.POST("/:id/tokens", s.issueMachineToken)
		machineAccounts.DELETE("/:id/tokens/:tokenId", s.revokeMachineToken)

		users := v1.Group("/users", requireAuth, requireAdmin)
		users.GET("", s.listUsers)
		users.POST("", s.createUser)
		users.PUT("/:id", s.updateUser)
		users.PATCH("/:id/status", s.setUserStatus)
		users.DELETE("/:id", s.deleteUser)

		groups := v1.Group("/groups", requireAuth, requireAdmin)
		groups.GET("", s.listGroups)
		groups.POST("", s.createGroup)
		groups.DELETE("/:id", s.deleteGroup)
		groups.POST("/:id/members", s.addGroupMember)
		groups.DELETE("/:id/members/:userId", s.removeGroupMember)

		permissions := v1.Group("/permissions", requireAuth, requireAdmin)
		permissions.GET("", s.listPermissions)
		permissions.POST("/assign", s.assignPermission)
		permissions.POST("/revoke", s.revokePermission)

		// Just-in-time elevated access: asking for a stronger role on a cluster
		// for a bounded window, and somebody else agreeing to it.
		//
		// Unlike the permissions matrix this is *not* an admin-only surface, and
		// deliberately so — the people who need it are the people who do not have
		// standing access. Reading follows the audit trail's rule (a non-admin sees
		// their own requests, narrowed by the handler), while approving is
		// administrative and can never be one's own request, which is enforced in
		// the workflow so the console and the webhook cannot disagree.
		if s.jit != nil {
			jitGroup := v1.Group("/jit", requireAuth)
			jitGroup.POST("/requests", s.createJitRequest)
			jitGroup.GET("/requests", s.listJitRequests)
			jitGroup.POST("/requests/:id/approve", requireAdmin, s.approveJitRequest)
			// Reject and revoke are not requireAdmin: cancelling your own request
			// and handing your own elevation back are things nobody should need
			// permission for. The workflow refuses somebody else's.
			jitGroup.POST("/requests/:id/reject", s.rejectJitRequest)
			jitGroup.POST("/requests/:id/revoke", s.revokeJitRequest)

			// The chat callback. It sits outside the JWT middleware because a Slack
			// app carries no KubeMG session, and authenticates on a signed action
			// token *plus* an identity that resolves to a KubeMG administrator —
			// see jitWebhookCallback for why neither half is enough alone.
			v1.POST(jitCallbackRoute, s.jitWebhookCallback)
		}

		// The audit trail is readable by everyone, but a non-admin only ever
		// sees their own actions — the handler narrows the filter itself.
		audit := v1.Group("/audit", requireAuth)
		audit.GET("", s.listAudit)
		audit.GET("/summary", s.auditSummary)

		// Recorded interactive sessions. They live under /audit because that is
		// what they are — the trail says a shell was opened in production, and
		// this is what was done in it — and they follow the same rule: a
		// non-admin sees their own sessions and nothing else, enforced on the
		// single recording as well as on the list. Deleting one is
		// administrative, because the person a recording is evidence about must
		// not be the one who decides it stops existing.
		// What this server records, readable by anyone who might be recorded —
		// which is everyone. A console that puts a terminal in front of an
		// operator has to be able to tell them what is being captured before they
		// type into it, and that cannot be an admin-only fact. It is a sibling of
		// the recordings collection rather than a child of it, because gin cannot
		// hold a static and a parameterised segment at the same level.
		audit.GET("/recording-policy", s.recordingPolicy)
		audit.GET("/terminal-sessions", s.listTerminalSessions)
		audit.GET("/terminal-sessions/:id", s.showTerminalSession)
		audit.GET("/terminal-sessions/:id/stream", s.streamTerminalSession)
		// Deleting is administrative *and* needs the recording-viewer capability:
		// an admin who may not watch a recording has no business destroying one
		// either, and the handler checks it.
		audit.DELETE("/terminal-sessions/:id", requireAdmin, s.deleteTerminalSession)

		// Configuring federation: who may sign in at all, and what an external
		// group is worth once they have. Both decide platform-wide access, so
		// both are administrative.
		admin := v1.Group("/admin/sso", requireAuth, requireAdmin)
		admin.GET("/providers", s.listSSOProviders)
		admin.POST("/providers", s.createSSOProvider)
		admin.PUT("/providers/:id", s.updateSSOProvider)
		admin.DELETE("/providers/:id", s.deleteSSOProvider)
		// Proving the configuration reaches the directory, so an operator finds
		// out here rather than from the first person who cannot sign in.
		admin.POST("/providers/:id/check", s.checkSSOProvider)

		admin.GET("/mappings", s.listSSOMappings)
		admin.POST("/mappings", s.createSSOMapping)
		admin.PUT("/mappings/:id", s.updateSSOMapping)
		admin.DELETE("/mappings/:id", s.deleteSSOMapping)

		// Alarms: where a cluster event or a refused action goes when somebody has
		// to know about it now. Administrative throughout, and not only because it
		// is fleet-wide configuration — a channel is an outbound destination for
		// audit records, so adding one is a data-egress decision.
		alarms := v1.Group("/alarms", requireAuth, requireAdmin)
		alarms.GET("/channels", s.listAlarmChannels)
		alarms.POST("/channels", s.createAlarmChannel)
		alarms.PUT("/channels/:id", s.updateAlarmChannel)
		alarms.DELETE("/channels/:id", s.deleteAlarmChannel)
		// Proving the endpoint accepts KubeMG's payload, so an operator finds out
		// here rather than from the incident nobody was paged for.
		alarms.POST("/channels/:id/test", s.testAlarmChannel)

		alarms.GET("/rules", s.listAlarmRules)
		alarms.POST("/rules", s.createAlarmRule)
		alarms.PUT("/rules/:id", s.updateAlarmRule)
		alarms.DELETE("/rules/:id", s.deleteAlarmRule)

		// Command guardrails: the calls and commands this platform refuses to
		// pass on, whatever the cluster's RBAC would have allowed. Administrative
		// throughout — writing one takes a capability away from a whole fleet,
		// and the set read at once is a map of which spellings are watched for.
		if s.guardrails != nil {
			guard := v1.Group("/guardrails", requireAuth, requireAdmin)
			guard.GET("", s.listGuardrailPolicies)
			// The presets. A guardrail is a regular expression matched against a
			// subject most operators have never had to write down, so a blank box
			// is a feature nobody turns on.
			guard.GET("/templates", s.guardrailTemplates)
			guard.POST("", s.createGuardrailPolicy)
			guard.PUT("/:id", s.updateGuardrailPolicy)
			guard.DELETE("/:id", s.deleteGuardrailPolicy)
		}

		// Server-wide settings. The public URL lands inside manifests applied on
		// other people's clusters, so this is an administrative surface.
		settings := v1.Group("/settings", requireAuth, requireAdmin)
		settings.GET("", s.getSettings)
		settings.PUT("", s.updateSettings)
		// Read-only, and read-only by nature: these are the boot-time facts the
		// setup wizard reports, on a page that outlives the wizard. Nothing here
		// can be written from a browser — see Deployment.
		settings.GET("/deployment", s.deploymentPosture)
	}

	return router
}

// corsConfig permits the browser app to send its bearer token. gin's
// cors.Default() omits the Authorization header, which silently breaks every
// authenticated cross-origin request.
func corsConfig(origins []string) cors.Config {
	if len(origins) == 0 {
		origins = defaultAllowedOrigins
	}

	cfg := cors.Config{
		AllowMethods: []string{
			http.MethodGet,
			http.MethodPost,
			http.MethodPut,
			http.MethodPatch,
			http.MethodDelete,
			http.MethodOptions,
		},
		// Cache-Control is how the console asks for a read the in-memory cache
		// must not answer — the Refresh button — so a browser has to be allowed
		// to send it, and the reply says which way it was answered.
		AllowHeaders: []string{
			"Origin", "Content-Type", "Accept", "Authorization", "Cache-Control", "Pragma",
		},
		ExposeHeaders: []string{"Content-Length", cacheStatusHeader},
		MaxAge:        12 * time.Hour,
	}
	if slices.Contains(origins, "*") {
		cfg.AllowAllOrigins = true
		return cfg
	}
	cfg.AllowOrigins = origins
	return cfg
}

func healthHandler(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// contentSecurityPolicy is scoped to what the console actually loads: itself.
// The console is served same-origin (see webui.Mount), fonts are vendored
// rather than pulled from a CDN, and there is no third-party script or embed
// anywhere in the app — so `'self'` covers every directive but one.
// style-src needs 'unsafe-inline' because React's inline `style={{...}}`
// (the hand-drawn charts, the meter bars) renders as inline style
// attributes, which CSP gates independently of script-src; there is no
// script equivalent, so script-src stays free of it. frame-ancestors 'none'
// is the modern, CSP-based half of clickjacking protection and does not
// depend on a browser's X-Frame-Options support.
const contentSecurityPolicy = "" +
	"default-src 'self'; " +
	"script-src 'self'; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data:; " +
	"font-src 'self'; " +
	"connect-src 'self'; " +
	"object-src 'none'; " +
	"base-uri 'self'; " +
	"form-action 'self'; " +
	"frame-ancestors 'none'"

// securityHeaders adds the response headers a DAST baseline scan checks for:
// a CSP and an anti-clickjacking header (issue-tracked as missing), plus the
// header-hygiene set that goes with them. It runs over every response,
// including the console's own static assets served from webui.Mount and the
// JSON API — a JSON body ignores CSP, but a browser error page rendering an
// API response as HTML (some do, on a misconfigured proxy) should not get a
// free pass either.
//
// HSTS is sent only when this process terminates TLS itself: advertising it
// while serving plaintext would tell a browser to require HTTPS for a host
// that cannot yet serve it, and the header is a no-op anyway when the
// response that carried it did not arrive over HTTPS.
func securityHeaders(tlsEnabled bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		if tlsEnabled {
			h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		}
		c.Next()
	}
}
