package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/kubemg/kubemg/backend/pkg/auth"
	"github.com/kubemg/kubemg/backend/pkg/bastion"
	"github.com/kubemg/kubemg/backend/pkg/db"
	"github.com/kubemg/kubemg/backend/pkg/k8s"
)

// The `kubemg` CLI signs in through the console rather than beside it: RFC
// 8628's device authorization grant, approved by a person in a browser session
// that has already passed the second factor. So the CLI never sees a password
// or implements a factor, and what it holds afterwards is a revocable session
// that mints fifteen-minute kubectl credentials — not a kubeconfig file that
// works for months. See implementation_plan.md and docs/access/cli.md.

// What a CLI session's life is called in the trail.
const (
	verbCLILogin         = "cli-login"
	verbCLISessionRevoke = "cli-session-revoke"
)

const (
	// cliDeviceTTL is how long a `kubemg login` waits for its approval.
	cliDeviceTTL = 10 * time.Minute
	// cliPollInterval is how often the CLI is told to ask. Faster earns
	// slow_down, as RFC 8628 has it.
	cliPollInterval = 5 * time.Second
	// cliPendingPerAddress bounds the unanswered requests one address may hold
	// open. The route is unauthenticated by necessity, and this is what stops
	// it from being a way to fill a table.
	cliPendingPerAddress = 10
	// cliAccessTokenTTL is how long one kubectl credential lives. Fifteen
	// minutes, and a constant rather than a setting: revocation reaches a token
	// on its next call through the published snapshot whatever this says, so
	// the lifetime only bounds what a stolen cache file is worth.
	cliAccessTokenTTL = 15 * time.Minute
	// defaultCLISessionHours is a working day: one second factor per day.
	defaultCLISessionHours = 12
	minCLISessionHours     = 1
	maxCLISessionHours     = 720
	// cliDevicePrefix marks the code the CLI polls with.
	cliDevicePrefix = "kmgd_"
)

// The values kubeconfig_issuance takes. Anything else stored reads as allowed:
// a mistyped row must not cut every agent-mode user off kubectl.
const (
	issuanceAllowed = "allowed"
	issuanceCLIOnly = "cli_only"
)

// storedIssuance is the override as stored, "" when unset or unreadable.
func storedIssuance(stored map[string]string) string {
	value := strings.ToLower(strings.TrimSpace(stored[db.SettingKubeconfigIssuance]))
	if value != issuanceAllowed && value != issuanceCLIOnly {
		return ""
	}
	return value
}

func normalizeIssuance(value string) string {
	if strings.ToLower(strings.TrimSpace(value)) == issuanceCLIOnly {
		return issuanceCLIOnly
	}
	return issuanceAllowed
}

// userCodeAlphabet is RFC 8628's suggestion: consonants only, so no code spells
// a word and none of its letters is mistaken for a digit.
const userCodeAlphabet = "BCDFGHJKLMNPQRSTVWXZ"

func newUserCode() (string, error) {
	limit := 256 - 256%len(userCodeAlphabet)
	out := make([]byte, 0, 9)
	var one [1]byte
	for len(out) < 9 {
		if len(out) == 4 {
			out = append(out, '-')
			continue
		}
		if _, err := rand.Read(one[:]); err != nil {
			return "", err
		}
		if int(one[0]) < limit {
			out = append(out, userCodeAlphabet[int(one[0])%len(userCodeAlphabet)])
		}
	}
	return string(out), nil
}

// normalizeUserCode reads a code the way people type it: lower case, without
// the dash, with spaces.
func normalizeUserCode(raw string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(raw) {
		if strings.ContainsRune(userCodeAlphabet, r) {
			b.WriteRune(r)
		}
	}
	code := b.String()
	if len(code) != 8 {
		return ""
	}
	return code[:4] + "-" + code[4:]
}

func newOpaqueToken(prefix string) (token, hash string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("generate token: %w", err)
	}
	token = prefix + base64.RawURLEncoding.EncodeToString(buf)
	return token, hashOpaqueToken(token), nil
}

func hashOpaqueToken(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}

// clientField keeps what a CLI says about itself printable and bounded. It is
// shown to the person deciding whether to approve, so a control character or
// a screenful of text in it would be a way to dress the request up.
func clientField(value string, limit int) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return -1
	}, strings.TrimSpace(value))
	if runes := []rune(value); len(runes) > limit {
		value = string(runes[:limit])
	}
	return value
}

// cliSessionCap resolves how long a new CLI session may last: the setting,
// never past the kubeconfig ceiling.
func (s *server) cliSessionCap(ctx context.Context) time.Duration {
	limit := time.Duration(s.settings(ctx).CLISessionMaxHours) * time.Hour
	return min(limit, s.kubeconfigMaxTTL(ctx))
}

type deviceStartRequest struct {
	ClientName    string `json:"client_name"`
	ClientHost    string `json:"client_host"`
	ClientOS      string `json:"client_os"`
	ClientVersion string `json:"client_version"`
}

type deviceStartResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// startDeviceLogin opens a `kubemg login`. Unauthenticated by necessity — the
// CLI has nothing yet — and narrow for it: it creates a ten-minute request that
// does nothing until a signed-in person approves it, and an address may hold
// only a handful open.
func (s *server) startDeviceLogin(c *gin.Context) {
	var req deviceStartRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "the request body is not valid JSON"})
		return
	}
	ctx := c.Request.Context()
	publicURL := strings.TrimRight(s.settings(ctx).PublicURL, "/")
	if publicURL == "" {
		c.JSON(http.StatusFailedDependency, gin.H{"error": "no public URL is configured for this server"})
		return
	}
	now := time.Now().UTC()
	source := c.ClientIP()
	open, err := s.store.PendingCLIDeviceCodesFrom(ctx, source, now)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not start the sign-in"})
		return
	}
	if open >= cliPendingPerAddress {
		c.Header("Retry-After", strconv.Itoa(int(cliDeviceTTL.Seconds())))
		c.JSON(http.StatusTooManyRequests, gin.H{
			"error": "too many sign-ins are waiting for approval from this address; approve or let them expire first",
		})
		return
	}

	deviceCode, hash, err := newOpaqueToken(cliDevicePrefix)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not start the sign-in"})
		return
	}
	row := &db.CLIDeviceCode{
		DeviceCodeHash: hash,
		ClientName:     clientField(req.ClientName, 64),
		ClientHost:     clientField(req.ClientHost, 128),
		ClientOS:       clientField(req.ClientOS, 64),
		ClientVersion:  clientField(req.ClientVersion, 64),
		SourceIP:       source,
		Status:         db.CLIDevicePending,
		ExpiresAt:      now.Add(cliDeviceTTL),
	}
	// A collision on the user code is drawn again; three in a row is not luck.
	for attempt := 0; ; attempt++ {
		if row.UserCode, err = newUserCode(); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "could not start the sign-in"})
			return
		}
		err = s.store.CreateCLIDeviceCode(ctx, row)
		if !errors.Is(err, db.ErrDeviceCodeTaken) || attempt == 2 {
			break
		}
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not start the sign-in"})
		return
	}

	verify := publicURL + "/cli/activate"
	c.JSON(http.StatusOK, deviceStartResponse{
		DeviceCode:              deviceCode,
		UserCode:                row.UserCode,
		VerificationURI:         verify,
		VerificationURIComplete: verify + "?code=" + url.QueryEscape(row.UserCode),
		ExpiresIn:               int(cliDeviceTTL.Seconds()),
		Interval:                int(cliPollInterval.Seconds()),
	})
}

// deviceError answers the token endpoint in RFC 8628's vocabulary: a 400 whose
// `error` is a code the CLI branches on, with a sentence beside it.
func deviceError(c *gin.Context, code, description string) {
	c.JSON(http.StatusBadRequest, gin.H{"error": code, "error_description": description})
}

type deviceTokenRequest struct {
	DeviceCode string `json:"device_code"`
}

type cliSessionResponse struct {
	RefreshToken string       `json:"refresh_token"`
	SessionID    string       `json:"session_id"`
	ExpiresAt    time.Time    `json:"expires_at"`
	User         userResponse `json:"user"`
}

// pollDeviceLogin is the CLI asking whether its request was approved. The
// session is created here, at the moment the secret is handed over — not at
// the approval — because only its hash is ever stored and this is the one
// response that can carry it.
func (s *server) pollDeviceLogin(c *gin.Context) {
	var req deviceTokenRequest
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.DeviceCode) == "" {
		deviceError(c, "invalid_request", "device_code is required")
		return
	}
	ctx := c.Request.Context()
	hash := hashOpaqueToken(req.DeviceCode)
	row, err := s.store.CLIDeviceCodeByHash(ctx, hash)
	if errors.Is(err, db.ErrNotFound) {
		deviceError(c, "expired_token", "this sign-in has expired or was already used; run kubemg login again")
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "server_error", "error_description": "could not read the sign-in"})
		return
	}
	now := time.Now().UTC()
	if !now.Before(row.ExpiresAt) {
		deviceError(c, "expired_token", "this sign-in has expired; run kubemg login again")
		return
	}

	switch row.Status {
	case db.CLIDevicePending:
		// The interval is enforced with a little slack for network jitter, and a
		// poll that comes too soon is not stamped, so a CLI that backs off is not
		// punished for the poll that earned the warning.
		if row.LastPolledAt != nil && now.Sub(*row.LastPolledAt) < cliPollInterval-time.Second {
			deviceError(c, "slow_down", "polling faster than the interval")
			return
		}
		if err := s.store.PollCLIDeviceCode(ctx, hash, now); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "server_error", "error_description": "could not read the sign-in"})
			return
		}
		deviceError(c, "authorization_pending", "waiting for the sign-in to be approved in the browser")
		return
	case db.CLIDeviceDenied:
		_, _ = s.store.TakeCLIDeviceCode(ctx, hash)
		deviceError(c, "access_denied", "the sign-in was denied in the browser")
		return
	}

	taken, err := s.store.TakeCLIDeviceCode(ctx, hash)
	if errors.Is(err, db.ErrNotFound) {
		deviceError(c, "expired_token", "this sign-in was already used; run kubemg login again")
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "server_error", "error_description": "could not finish the sign-in"})
		return
	}
	user, err := s.store.UserByID(ctx, taken.UserID)
	if err != nil || !user.IsActive {
		deviceError(c, "access_denied", "the account that approved this sign-in cannot sign in")
		return
	}
	user.Normalize()

	refresh, refreshHash, err := newOpaqueToken(auth.CLIRefreshPrefix)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "server_error", "error_description": "could not create the session"})
		return
	}
	session := &db.CLISession{
		ID:            uuid.NewString(),
		UserID:        user.ID,
		Username:      user.Username,
		RefreshHash:   refreshHash,
		ClientName:    taken.ClientName,
		ClientHost:    taken.ClientHost,
		ClientOS:      taken.ClientOS,
		ClientVersion: taken.ClientVersion,
		SourceIP:      taken.SourceIP,
		ExpiresAt:     now.Add(s.cliSessionCap(ctx)),
		CreatedAt:     now,
	}
	if err := s.store.CreateCLISession(ctx, session); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "server_error", "error_description": "could not create the session"})
		return
	}
	c.JSON(http.StatusOK, cliSessionResponse{
		RefreshToken: refresh,
		SessionID:    session.ID,
		ExpiresAt:    session.ExpiresAt,
		User:         toUserResponse(user),
	})
}

// deviceRequestResponse is what the approval page shows: who is asking, from
// where, and whether that is where the browser is.
type deviceRequestResponse struct {
	UserCode      string    `json:"user_code"`
	ClientName    string    `json:"client_name,omitempty"`
	ClientHost    string    `json:"client_host,omitempty"`
	ClientOS      string    `json:"client_os,omitempty"`
	ClientVersion string    `json:"client_version,omitempty"`
	SourceIP      string    `json:"source_ip"`
	SameAddress   bool      `json:"same_address"`
	Status        string    `json:"status"`
	CreatedAt     time.Time `json:"created_at"`
	ExpiresAt     time.Time `json:"expires_at"`
	// SessionHours is how long the session approving this would last.
	SessionHours int `json:"session_hours"`
}

func (s *server) loadDeviceRequest(c *gin.Context) (*db.CLIDeviceCode, bool) {
	code := normalizeUserCode(c.Param("code"))
	if code == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "no sign-in is waiting for that code"})
		return nil, false
	}
	row, err := s.store.CLIDeviceCodeByUserCode(c.Request.Context(), code, time.Now().UTC())
	if errors.Is(err, db.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "no sign-in is waiting for that code — it may have expired"})
		return nil, false
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not read the sign-in"})
		return nil, false
	}
	return row, true
}

// describeDeviceLogin reads a pending request for the approval page.
func (s *server) describeDeviceLogin(c *gin.Context) {
	if _, ok := s.currentUser(c); !ok {
		return
	}
	row, ok := s.loadDeviceRequest(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, deviceRequestResponse{
		UserCode:      row.UserCode,
		ClientName:    row.ClientName,
		ClientHost:    row.ClientHost,
		ClientOS:      row.ClientOS,
		ClientVersion: row.ClientVersion,
		SourceIP:      row.SourceIP,
		SameAddress:   row.SourceIP == c.ClientIP(),
		Status:        row.Status,
		CreatedAt:     row.CreatedAt,
		ExpiresAt:     row.ExpiresAt,
		SessionHours:  int(s.cliSessionCap(c.Request.Context()).Hours()),
	})
}

// approveDeviceLogin gives the waiting CLI a session as the approving person.
//
// A session that did not pass a second factor cannot approve when the account
// holds one or must hold one: otherwise a session minted before enforcement — or
// stolen — would mint CLI sessions without ever meeting the factor this whole
// flow exists to carry over.
func (s *server) approveDeviceLogin(c *gin.Context) {
	s.decideDeviceLogin(c, db.CLIDeviceApproved)
}

// denyDeviceLogin refuses a request, which the CLI hears as access_denied.
func (s *server) denyDeviceLogin(c *gin.Context) {
	s.decideDeviceLogin(c, db.CLIDeviceDenied)
}

func (s *server) decideDeviceLogin(c *gin.Context, status string) {
	caller, ok := s.currentUser(c)
	if !ok {
		return
	}
	if caller.IsMachine() {
		c.JSON(http.StatusForbidden, gin.H{"error": "a machine account signs the CLI in with KUBEMG_TOKEN, not by approval"})
		return
	}
	row, ok := s.loadDeviceRequest(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if status == db.CLIDeviceApproved && caller.MFAApplies() {
		claims, _ := auth.ClaimsFrom(c)
		state, err := s.store.MFAState(ctx, caller.ID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "could not check the second factor"})
			return
		}
		owed := state.Enrolled() || mfaRequiredFor(s.settings(ctx).MFAEnforcement, caller)
		if owed && (claims == nil || !claims.MFA) {
			c.JSON(http.StatusForbidden, gin.H{
				"error": "this browser session did not pass your second factor — sign out, sign in again, and approve from the new session",
			})
			return
		}
	}
	decided, err := s.store.DecideCLIDeviceCode(ctx, row.UserCode, status, caller.ID, time.Now().UTC())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not record the decision"})
		return
	}
	if !decided {
		c.JSON(http.StatusConflict, gin.H{"error": "this sign-in was already answered"})
		return
	}
	if status == db.CLIDeviceApproved {
		s.recordCLILogin(c, caller, row)
	}
	c.JSON(http.StatusOK, gin.H{"status": status})
}

// recordCLILogin puts the approval in the trail, with what the CLI said about
// itself and where it ran in the path query — agent-displaced's shape — so the
// row answers "which laptop got a session" without a second table.
func (s *server) recordCLILogin(c *gin.Context, caller *db.User, row *db.CLIDeviceCode) {
	if s.auditor == nil {
		return
	}
	query := url.Values{}
	query.Set("client", row.ClientName)
	query.Set("host", row.ClientHost)
	query.Set("os", row.ClientOS)
	query.Set("version", row.ClientVersion)
	query.Set("from", row.SourceIP)
	s.auditor.Record(c.Request.Context(), bastion.Event{
		At:       time.Now().UTC(),
		UserID:   caller.ID,
		Username: caller.Username,
		Verb:     verbCLILogin,
		Method:   c.Request.Method,
		Path:     c.Request.URL.Path + "?" + query.Encode(),
		Resource: "cli-sessions",
		Status:   http.StatusOK,
	})
}

// The context keys requireCLISession sets.
const (
	cliSessionKey = "kubemg.cli.session"
	cliUserKey    = "kubemg.cli.user"
)

// requireCLISession authenticates the CLI's own routes by refresh token. It is
// not RequireAuth: that one refuses a kmgr_ token by name, so the credential a
// laptop holds reaches these routes and no others.
func (s *server) requireCLISession(c *gin.Context) {
	header := strings.TrimSpace(c.GetHeader("Authorization"))
	token, ok := strings.CutPrefix(header, "Bearer ")
	token = strings.TrimSpace(token)
	if !ok || !strings.HasPrefix(token, auth.CLIRefreshPrefix) {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "a kubemg CLI session is required; run kubemg login"})
		return
	}
	ctx := c.Request.Context()
	session, err := s.store.CLISessionByRefreshHash(ctx, hashOpaqueToken(token))
	if errors.Is(err, db.ErrNotFound) {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "this CLI session does not exist; run kubemg login"})
		return
	}
	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "could not read the CLI session"})
		return
	}
	now := time.Now().UTC()
	if !session.Live(now) {
		reason := "this CLI session has expired; run kubemg login"
		if session.RevokedAt != nil {
			reason = "this CLI session was revoked; run kubemg login"
		}
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": reason})
		return
	}
	user, err := s.store.UserByID(ctx, session.UserID)
	if errors.Is(err, db.ErrNotFound) {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "the account behind this CLI session no longer exists"})
		return
	}
	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "could not load the account"})
		return
	}
	if !user.IsActive {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "this account is disabled"})
		return
	}
	user.Normalize()
	c.Set(cliSessionKey, session)
	c.Set(cliUserKey, user)
	c.Next()
}

func cliFrom(c *gin.Context) (*db.CLISession, *db.User) {
	session, _ := c.Get(cliSessionKey)
	user, _ := c.Get(cliUserKey)
	return session.(*db.CLISession), user.(*db.User)
}

// cliCluster is one cluster as the CLI writes it into a kubeconfig. Every name
// and address is the server's, so the console's download and the CLI produce
// the same context for the same person and cluster.
type cliCluster struct {
	ID             uint     `json:"id"`
	Name           string   `json:"name"`
	Environment    string   `json:"environment,omitempty"`
	ConnectionMode string   `json:"connection_mode"`
	Role           string   `json:"role"`
	Namespaces     []string `json:"namespaces,omitempty"`
	// Server, ContextName and CAData are set for an agent-mode cluster only.
	// A direct-mode one is reached with a token the cluster mints, which this
	// path does not hand out; Note says so.
	Server      string `json:"server,omitempty"`
	ContextName string `json:"context_name,omitempty"`
	CAData      string `json:"ca_data,omitempty"`
	Note        string `json:"note,omitempty"`
}

// cliClusters lists what the session's account can reach.
func (s *server) cliClusters(c *gin.Context) {
	_, user := cliFrom(c)
	ctx := c.Request.Context()
	clusters, err := s.store.ClustersForUser(ctx, user)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not list clusters"})
		return
	}
	grants := map[uint]db.UserClusterAccess{}
	if !user.IsAdmin() {
		if grants, err = s.store.AccessForUser(ctx, user.ID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load cluster access"})
			return
		}
	}
	publicURL := strings.TrimRight(s.settings(ctx).PublicURL, "/")
	out := make([]cliCluster, 0, len(clusters))
	for _, cluster := range clusters {
		entry := cliCluster{
			ID: cluster.ID, Name: cluster.Name, Environment: cluster.Environment,
			ConnectionMode: cluster.ConnectionMode, Role: db.K8sRoleClusterAdmin,
		}
		if !user.IsAdmin() {
			grant, ok := grants[cluster.ID]
			if !ok {
				continue
			}
			entry.Role = grant.K8sRole
			entry.Namespaces = grant.NamespaceList()
		}
		if cluster.UsesAgent() && publicURL != "" {
			entry.Server = fmt.Sprintf("%s/api/v1/clusters/%d/proxy", publicURL, cluster.ID)
			entry.ContextName = k8s.KubeconfigInput{ClusterName: cluster.Name, Username: user.Username}.ContextName()
			if s.bastionCA != "" {
				entry.CAData = base64.StdEncoding.EncodeToString([]byte(s.bastionCA))
			}
		} else if !cluster.UsesAgent() {
			entry.Note = "registered for direct API access — download its kubeconfig from the console"
		}
		out = append(out, entry)
	}
	c.JSON(http.StatusOK, gin.H{"clusters": out})
}

type cliCredentialRequest struct {
	ClusterID uint   `json:"cluster_id" binding:"required"`
	Namespace string `json:"namespace"`
	// Static asks for a whole kubeconfig carrying a token of TTLSeconds instead
	// of an exec credential — the console download, from the CLI. It is a
	// register row like any download and is refused under cli_only.
	Static     bool  `json:"static"`
	TTLSeconds int64 `json:"ttl_seconds"`
}

type cliCredentialResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// cliCredential mints one kubectl credential for one cluster: the exec plugin's
// answer. The grant, the namespace scope and the mode are decided exactly as
// the console's kubeconfig generator decides them.
func (s *server) cliCredential(c *gin.Context) {
	session, user := cliFrom(c)
	var req cliCredentialRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cluster_id is required"})
		return
	}
	ctx := c.Request.Context()
	cluster, err := s.store.ClusterByID(ctx, req.ClusterID)
	if errors.Is(err, db.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "cluster not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load cluster"})
		return
	}
	grant, k8sRole, ok := s.authorizeCluster(c, user, cluster.ID)
	if !ok {
		return
	}
	namespace, ok := resolveNamespace(c, req.Namespace, grant)
	if !ok {
		return
	}
	if !cluster.UsesAgent() {
		c.JSON(http.StatusConflict, gin.H{
			"error": "this cluster is registered for direct API access: its credential is a token the cluster " +
				"mints, which no CLI session governs — download its kubeconfig from the console",
		})
		return
	}

	if req.Static {
		if normalizeIssuance(s.settings(ctx).KubeconfigIssuance) == issuanceCLIOnly {
			c.JSON(http.StatusConflict, gin.H{"error": cliOnlyRefusal})
			return
		}
		ttl := k8s.DefaultTTL
		if req.TTLSeconds != 0 {
			ttl = time.Duration(req.TTLSeconds) * time.Second
		}
		if maxTTL := s.kubeconfigMaxTTL(ctx); ttl < k8s.MinTTL || ttl > maxTTL {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf(
				"ttl_seconds must be between %d and %d", int64(k8s.MinTTL.Seconds()), int64(maxTTL.Seconds()))})
			return
		}
		s.agentKubeconfig(c, user, cluster, namespace, k8sRole, ttl)
		return
	}

	ttl := min(cliAccessTokenTTL, time.Until(session.ExpiresAt))
	if ttl <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "this CLI session has expired; run kubemg login"})
		return
	}
	token, expiresAt, err := s.jwt.GenerateProxyTokenWithID(user.ID, user.Username, user.Role, cluster.ID, ttl, session.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not issue an access token"})
		return
	}
	s.touchCLISession(ctx, session)
	c.JSON(http.StatusOK, cliCredentialResponse{Token: token, ExpiresAt: expiresAt})
}

// cliOnlyRefusal is what an agent-mode download says under cli_only.
const cliOnlyRefusal = "this server issues agent-mode kubectl access through the kubemg CLI only — " +
	"run kubemg login, then kubemg update-kubeconfig"

// touchCLISession stamps a session's use at most every five minutes, the
// register's cadence, and never fails the call.
func (s *server) touchCLISession(ctx context.Context, session *db.CLISession) {
	now := time.Now().UTC()
	if session.LastUsedAt != nil && now.Sub(*session.LastUsedAt) < 5*time.Minute {
		return
	}
	if err := s.store.TouchCLISession(ctx, session.ID, now); err != nil {
		s.log().Warn("could not stamp a CLI session's use", slog.String("error", err.Error()))
	}
}

// cliLogout ends the calling session.
func (s *server) cliLogout(c *gin.Context) {
	session, user := cliFrom(c)
	revoked, err := s.store.RevokeCLISession(c.Request.Context(), session.ID, time.Now().UTC(), user.ID, user.Username)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not end the session"})
		return
	}
	s.publishRevokedCredentials(c.Request.Context())
	s.recordCLISessionRevocation(c, user, []db.CLISession{*revoked})
	c.Status(http.StatusNoContent)
}

// cliSessionView is one session as the console reads it.
type cliSessionView struct {
	db.CLISession
	Status string `json:"status"`
}

func toCLISessionView(row db.CLISession, now time.Time) cliSessionView {
	return cliSessionView{CLISession: row, Status: row.Status(now)}
}

// listCLISessions is the register's read rule exactly: everybody reads, a
// non-administrator only their own, and user_id can only narrow.
func (s *server) listCLISessions(c *gin.Context) {
	caller, ok := s.currentUser(c)
	if !ok {
		return
	}
	now := time.Now().UTC()
	filter := db.CLISessionFilter{Now: now, ActiveOnly: c.Query("status") == "active"}
	if raw := strings.TrimSpace(c.Query("user_id")); raw != "" {
		parsed, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "user_id must be an integer"})
			return
		}
		filter.UserID = uint(parsed)
	}
	if !caller.IsAdmin() {
		filter.UserID = caller.ID
	}
	filter.Limit, filter.Offset = pageParams(c)
	rows, total, err := s.store.ListCLISessions(c.Request.Context(), filter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not read the CLI sessions"})
		return
	}
	out := make([]cliSessionView, 0, len(rows))
	for _, row := range rows {
		out = append(out, toCLISessionView(row, now))
	}
	c.JSON(http.StatusOK, gin.H{"sessions": out, "total": total})
}

// revokeCLISession ends one session: your own always, somebody else's only as
// an administrator, and somebody else's answers as not found otherwise.
func (s *server) revokeCLISession(c *gin.Context) {
	caller, ok := s.currentUser(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	row, err := s.store.CLISessionByID(ctx, c.Param("id"))
	if errors.Is(err, db.ErrNotFound) || (err == nil && row.UserID != caller.ID && !caller.IsAdmin()) {
		c.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not read the session"})
		return
	}
	revoked, err := s.store.RevokeCLISession(ctx, row.ID, time.Now().UTC(), caller.ID, caller.Username)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not revoke the session"})
		return
	}
	// Republished before answering, the register's rule: a caller told the
	// session is over must not be able to use one of its tokens afterwards here.
	s.publishRevokedCredentials(ctx)
	if row.RevokedAt == nil {
		s.recordCLISessionRevocation(c, caller, []db.CLISession{*row})
	}
	c.JSON(http.StatusOK, toCLISessionView(*revoked, time.Now().UTC()))
}

// revokeCLISessionsFor ends every live session a person holds, for the blanket
// revokes — the credential page's and the password change's.
func (s *server) revokeCLISessionsFor(c *gin.Context, caller *db.User, target uint) (int, error) {
	ctx := c.Request.Context()
	rows, err := s.store.RevokeCLISessionsForUser(ctx, target, time.Now().UTC(), caller.ID, caller.Username)
	if err != nil {
		return 0, err
	}
	s.recordCLISessionRevocation(c, caller, rows)
	return len(rows), nil
}

// recordCLISessionRevocation writes one record per session, identities crossed.
func (s *server) recordCLISessionRevocation(c *gin.Context, caller *db.User, rows []db.CLISession) {
	if s.auditor == nil {
		return
	}
	for _, row := range rows {
		event := bastion.Event{
			At:       time.Now().UTC(),
			UserID:   caller.ID,
			Username: caller.Username,
			Verb:     verbCLISessionRevoke,
			Method:   c.Request.Method,
			Path:     c.Request.URL.Path,
			Resource: "cli-sessions",
			Status:   http.StatusOK,
		}
		if row.UserID != caller.ID {
			event.ImpersonatedUser = row.Username
		}
		s.auditor.Record(c.Request.Context(), event)
	}
}
