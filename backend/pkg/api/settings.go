package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/auditpolicy"
	"github.com/kubemg/kubemg/backend/pkg/db"
	"github.com/kubemg/kubemg/backend/pkg/k8s"
	"github.com/kubemg/kubemg/backend/pkg/shell"
)

// runtimeSettings is the resolved view of the operator-configurable settings:
// the stored override where there is one, the boot-time environment value
// otherwise.
type runtimeSettings struct {
	PublicURL      string `json:"public_url"`
	AgentImage     string `json:"agent_image"`
	AgentNamespace string `json:"agent_namespace"`
	// AuditRetentionDays is how long the pruner keeps a proxied call. Zero in
	// the overrides means "unset", exactly as an empty string does for the
	// others.
	AuditRetentionDays int `json:"audit_retention_days"`
	// SessionRecordingRetentionDays is how long a terminal recording is kept.
	// Zero means "the audit window", which is both the default and the ceiling —
	// see clampRecordingRetention.
	SessionRecordingRetentionDays int `json:"session_recording_retention_days"`
	// AuditVerbs are the verbs that reach the audit table. It is empty when no
	// selection is in force, in which case every verb is recorded — and
	// AuditVerbsSelected is what distinguishes that from a selection that happens
	// to list nothing, which the write path refuses to store.
	AuditVerbs []string `json:"audit_verbs"`
	// AuditVerbsSelected reports whether a selection is in force at all.
	AuditVerbsSelected bool `json:"audit_verbs_selected"`
	// RecordExecSessions is the runtime switch on interactive session recording.
	RecordExecSessions bool `json:"record_exec_sessions"`
	// RecordingAvailable says whether this process *can* record at all — a server
	// started without a recording directory cannot be talked into it from here, and
	// a switch that silently does nothing is worse than one that says why.
	RecordingAvailable bool `json:"recording_available"`
	// RecordManifestDiffs turns on storing the field-level diff of a manifest
	// write on its `update` audit row. It defaults false: see
	// db.SettingRecordManifestDiffs for why this one setting starts off where
	// the others do not.
	RecordManifestDiffs bool `json:"record_manifest_diffs"`
	// KubeconfigMaxTTLHours is the longest a generated kubeconfig may be asked
	// to live. Zero in the overrides means "unset", which takes the build's own
	// default ceiling.
	KubeconfigMaxTTLHours int `json:"kubeconfig_max_ttl_hours"`
	// ShellEnabled is the switch on the browser shell. It follows the process the
	// way recording does: a build started with the shell off cannot be talked
	// into it by a database row, because the image it would run comes from the
	// environment.
	ShellEnabled bool `json:"shell_enabled"`
	// ShellImage is what a shell pod runs.
	ShellImage string `json:"shell_image"`
	// ShellIdleTimeoutMinutes is how long a shell may go unused. Zero in the
	// overrides means unset.
	ShellIdleTimeoutMinutes int `json:"shell_idle_timeout_minutes"`
	// ShellMaxLifetimeHours is the absolute deadline written into the pod.
	ShellMaxLifetimeHours int `json:"shell_max_lifetime_hours"`
}

type settingsResponse struct {
	// Effective is what the server actually uses right now.
	Effective runtimeSettings `json:"effective"`
	// Overrides are the values stored in the database; empty means the
	// corresponding default applies.
	Overrides runtimeSettings `json:"overrides"`
	// Defaults are the environment-supplied fallbacks, shown so an operator can
	// see what clearing a field will restore.
	Defaults runtimeSettings `json:"defaults"`
	// Warnings flag settings that are syntactically fine but cannot work, such
	// as a loopback public URL that no agent in a cluster can dial.
	Warnings []string `json:"warnings"`
}

type updateSettingsRequest struct {
	PublicURL      *string `json:"public_url"`
	AgentImage     *string `json:"agent_image"`
	AgentNamespace *string `json:"agent_namespace"`
	// AuditRetentionDays accepts 0 to clear the override back to the default.
	AuditRetentionDays *int `json:"audit_retention_days"`
	// SessionRecordingRetentionDays accepts 0 to fall back to the audit window.
	SessionRecordingRetentionDays *int `json:"session_recording_retention_days"`
	// AuditVerbs replaces the enabled set. Omitting it leaves the stored value
	// alone; sending an **empty** array clears the selection back to "every verb"
	// rather than meaning "no verbs at all".
	//
	// That is a deliberate reading of an ambiguous input. "Record nothing" is not a
	// state a settings form should be able to reach — the floor in auditpolicy
	// would keep recording refusals and sessions regardless, so a server claiming
	// to be silent would be lying about itself. Unticking every box therefore means
	// the same thing as never having ticked one.
	AuditVerbs          *[]string `json:"audit_verbs"`
	RecordExecSessions  *bool     `json:"record_exec_sessions"`
	RecordManifestDiffs *bool     `json:"record_manifest_diffs"`
	// KubeconfigMaxTTLHours accepts 0 to clear the override back to the
	// build's default ceiling.
	KubeconfigMaxTTLHours *int `json:"kubeconfig_max_ttl_hours"`
	ShellEnabled          *bool   `json:"shell_enabled"`
	ShellImage            *string `json:"shell_image"`
	// ShellIdleTimeoutMinutes and ShellMaxLifetimeHours accept 0 to clear the
	// override back to the build's defaults.
	ShellIdleTimeoutMinutes *int `json:"shell_idle_timeout_minutes"`
	ShellMaxLifetimeHours   *int `json:"shell_max_lifetime_hours"`
}

// Audit retention bounds. The floor stops an operator from silently emptying
// the trail with a fat-fingered zero-ish value; the ceiling is ten years, past
// which a retention policy is really an archive and belongs somewhere else.
const (
	minAuditRetentionDays = 1
	maxAuditRetentionDays = 3650
)

// Kubeconfig ceiling bounds, in hours. The floor is an hour rather than
// k8s.MinTTL because a ceiling below the floor a request is measured against
// would refuse every request, which is a setting that reads as a broken
// feature. The ceiling is the build's absolute bound — see k8s.MaxTTL.
var (
	minKubeconfigMaxTTLHours = 1
	maxKubeconfigMaxTTLHours = int(k8s.MaxTTL / time.Hour)
)

// Browser shell bounds, in the units the settings are stored in. They mirror
// pkg/shell's own, which is what the lifecycle actually enforces — stated here
// too so a settings form is refused at the edge rather than silently clamped
// three layers in.
var (
	minShellIdleTimeoutMinutes = int(shell.MinIdleTimeout / time.Minute)
	maxShellIdleTimeoutMinutes = int(shell.MaxIdleTimeout / time.Minute)
	minShellMaxLifetimeHours   = int(shell.MinMaxLifetime / time.Hour)
	maxShellMaxLifetimeHours   = int(shell.MaxMaxLifetime / time.Hour)
)

// settings resolves the effective configuration. A database failure falls back
// to the environment values rather than erroring: generating an install command
// with the boot-time address is far better than not generating one at all.
func (s *server) settings(ctx context.Context) runtimeSettings {
	out := runtimeSettings{
		PublicURL:          s.publicURL,
		AgentImage:         s.agentImage,
		AgentNamespace:     s.agentNamespace,
		AuditRetentionDays: s.auditRetentionDays,
		// Recording follows the process: a server started with no recording
		// directory cannot record, and the switch below can only turn that off.
		RecordExecSessions: s.recordings != "",
		RecordingAvailable: s.recordings != "",
		// There is no environment variable behind this one: the default is the
		// build's own ceiling, and an operator who wants another one is making a
		// policy decision that belongs in the database where it can be audited
		// and changed without a redeploy.
		KubeconfigMaxTTLHours: int(k8s.DefaultMaxTTL / time.Hour),
		// The shell follows the process for the same reason recording does: the
		// image and the switch it was started with are what it can run.
		ShellEnabled:            s.shellEnabled,
		ShellImage:              s.shellImage,
		ShellIdleTimeoutMinutes: int(shell.DefaultIdleTimeout / time.Minute),
		ShellMaxLifetimeHours:   int(shell.DefaultMaxLifetime / time.Hour),
	}
	stored, err := s.store.Settings(ctx)
	if err != nil {
		return out
	}
	out.AuditVerbs, out.AuditVerbsSelected = storedAuditVerbs(stored)
	if v := strings.TrimSpace(stored[db.SettingRecordExecSessions]); v != "" {
		if enabled, err := strconv.ParseBool(v); err == nil {
			out.RecordExecSessions = out.RecordingAvailable && enabled
		}
	}
	if v := strings.TrimSpace(stored[db.SettingRecordManifestDiffs]); v != "" {
		if enabled, err := strconv.ParseBool(v); err == nil {
			out.RecordManifestDiffs = enabled
		}
	}
	if v := strings.TrimSpace(stored[db.SettingPublicURL]); v != "" {
		out.PublicURL = strings.TrimRight(v, "/")
	}
	if v := strings.TrimSpace(stored[db.SettingAgentImage]); v != "" {
		out.AgentImage = v
	}
	if v := strings.TrimSpace(stored[db.SettingAgentNamespace]); v != "" {
		out.AgentNamespace = v
	}
	if v := storedRetentionDays(stored); v > 0 {
		out.AuditRetentionDays = v
	}
	if v := storedKubeconfigMaxTTLHours(stored); v > 0 {
		out.KubeconfigMaxTTLHours = v
	}
	if v := strings.TrimSpace(stored[db.SettingShellEnabled]); v != "" {
		if enabled, err := strconv.ParseBool(v); err == nil {
			// A row can turn the shell off, never on: an install with no shell
			// image configured has nothing to run.
			out.ShellEnabled = out.ShellEnabled && enabled
		}
	}
	if v := strings.TrimSpace(stored[db.SettingShellImage]); v != "" {
		out.ShellImage = v
	}
	if v := storedBounded(stored, db.SettingShellIdleTimeoutMinutes,
		minShellIdleTimeoutMinutes, maxShellIdleTimeoutMinutes); v > 0 {
		out.ShellIdleTimeoutMinutes = v
	}
	if v := storedBounded(stored, db.SettingShellMaxLifetimeHours,
		minShellMaxLifetimeHours, maxShellMaxLifetimeHours); v > 0 {
		out.ShellMaxLifetimeHours = v
	}
	out.SessionRecordingRetentionDays = clampRecordingRetention(
		storedDays(stored, db.SettingSessionRecordingRetentionDays), out.AuditRetentionDays)
	return out
}

// clampRecordingRetention resolves how long recordings are kept. Unset takes the
// audit window, and a value longer than it is clamped down to it rather than
// refused on the way in.
//
// The ceiling is the point: a recording is evidence *about* a line in the trail,
// so a replay that outlives the record saying the shell was ever opened is
// orphaned evidence. Clamping rather than refusing matters because the audit
// window is itself editable — shortening audit retention has to pull recordings
// in with it, and a stored recording window that was legal when it was written
// must not become a validation error nobody can see.
func clampRecordingRetention(stored, auditDays int) int {
	if stored <= 0 || stored > auditDays {
		return auditDays
	}
	return stored
}

// storedAuditVerbs reads the enabled verb selection, reporting whether one is in
// force at all. An unrecognised verb in the stored list is dropped, and a list
// that ends up empty reads as "no selection" — a hand-edited row must not be able
// to switch the trail off.
func storedAuditVerbs(stored map[string]string) ([]string, bool) {
	raw := strings.TrimSpace(stored[db.SettingAuditVerbs])
	if raw == "" {
		return nil, false
	}
	out := make([]string, 0, len(auditpolicy.Verbs))
	seen := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		verb := strings.ToLower(strings.TrimSpace(part))
		if verb == "" || seen[verb] || !slices.Contains(auditpolicy.Verbs, verb) {
			continue
		}
		seen[verb] = true
		out = append(out, verb)
	}
	if len(out) == 0 {
		return nil, false
	}
	slices.Sort(out)
	return out, true
}

// storedDays reads a day count, treating anything outside the retention bounds
// as unset for the same reason storedRetentionDays does: a window read wrong is
// a trail deleted.
func storedDays(stored map[string]string, key string) int {
	raw := strings.TrimSpace(stored[key])
	if raw == "" {
		return 0
	}
	days, err := strconv.Atoi(raw)
	if err != nil || days < minAuditRetentionDays || days > maxAuditRetentionDays {
		return 0
	}
	return days
}

// storedBounded reads a stored integer, treating anything outside the bounds as
// unset — the rule every numeric setting here follows, so a hand-edited row can
// only ever fall back to a default rather than take effect as something the
// build would refuse.
func storedBounded(stored map[string]string, key string, low, high int) int {
	raw := strings.TrimSpace(stored[key])
	if raw == "" {
		return 0
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < low || value > high {
		return 0
	}
	return value
}

// storedKubeconfigMaxTTLHours reads the kubeconfig ceiling override. A value
// outside the bounds reads as unset for the same reason a retention window
// does: a ceiling read wrong is either every request refused or a credential
// living longer than this build is willing to sign for.
func storedKubeconfigMaxTTLHours(stored map[string]string) int {
	raw := strings.TrimSpace(stored[db.SettingKubeconfigMaxTTLHours])
	if raw == "" {
		return 0
	}
	hours, err := strconv.Atoi(raw)
	if err != nil || hours < minKubeconfigMaxTTLHours || hours > maxKubeconfigMaxTTLHours {
		return 0
	}
	return hours
}

// auditPolicySnapshot is the settings above reduced to the two questions the
// gateway's hot path asks.
func (s *server) auditPolicySnapshot(ctx context.Context) auditpolicy.Snapshot {
	resolved := s.settings(ctx)
	var verbs []string
	if resolved.AuditVerbsSelected {
		verbs = resolved.AuditVerbs
	}
	return auditpolicy.NewSnapshot(verbs, resolved.RecordExecSessions)
}

// publishAuditPolicy resolves the audit settings and hands them to the gateway.
// It is called at boot, after every settings write, and on a timer — the timer is
// what makes a second replica pick up a change one of its siblings saved.
func (s *server) publishAuditPolicy(ctx context.Context) {
	if s.auditPolicy == nil {
		return
	}
	s.auditPolicy.Store(s.auditPolicySnapshot(ctx))
}

// storedRetentionDays reads the audit retention override. A value that is not a
// usable number reads as unset, so a hand-edited row cannot turn the pruner
// into something that deletes everything.
func storedRetentionDays(stored map[string]string) int {
	return storedDays(stored, db.SettingAuditRetentionDays)
}

// getSettings returns the effective settings alongside the stored overrides and
// the environment defaults behind them (admin only).
func (s *server) getSettings(c *gin.Context) {
	stored, err := s.store.Settings(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load the settings"})
		return
	}

	effective := s.settings(c.Request.Context())
	overrideVerbs, verbsSelected := storedAuditVerbs(stored)
	overrides := runtimeSettings{
		PublicURL:                     strings.TrimSpace(stored[db.SettingPublicURL]),
		AgentImage:                    strings.TrimSpace(stored[db.SettingAgentImage]),
		AgentNamespace:                strings.TrimSpace(stored[db.SettingAgentNamespace]),
		AuditRetentionDays:            storedRetentionDays(stored),
		SessionRecordingRetentionDays: storedDays(stored, db.SettingSessionRecordingRetentionDays),
		AuditVerbs:                    overrideVerbs,
		AuditVerbsSelected:            verbsSelected,
		RecordExecSessions:            effective.RecordExecSessions,
		RecordingAvailable:            effective.RecordingAvailable,
		RecordManifestDiffs:           effective.RecordManifestDiffs,
		KubeconfigMaxTTLHours:         storedKubeconfigMaxTTLHours(stored),
		ShellEnabled:                  effective.ShellEnabled,
		ShellImage:                    strings.TrimSpace(stored[db.SettingShellImage]),
		ShellIdleTimeoutMinutes: storedBounded(stored, db.SettingShellIdleTimeoutMinutes,
			minShellIdleTimeoutMinutes, maxShellIdleTimeoutMinutes),
		ShellMaxLifetimeHours: storedBounded(stored, db.SettingShellMaxLifetimeHours,
			minShellMaxLifetimeHours, maxShellMaxLifetimeHours),
	}

	c.JSON(http.StatusOK, settingsResponse{
		Effective: effective,
		Overrides: overrides,
		Defaults: runtimeSettings{
			PublicURL:          s.publicURL,
			AgentImage:         s.agentImage,
			AgentNamespace:     s.agentNamespace,
			AuditRetentionDays: s.auditRetentionDays,
			// The recording window's default is not an environment variable: it is
			// whatever the audit window resolves to, because that is its ceiling.
			SessionRecordingRetentionDays: effective.AuditRetentionDays,
			RecordExecSessions:            effective.RecordingAvailable,
			RecordingAvailable:            effective.RecordingAvailable,
			// Off by default and there is no environment override for it — see
			// db.SettingRecordManifestDiffs.
			RecordManifestDiffs:     false,
			KubeconfigMaxTTLHours:   int(k8s.DefaultMaxTTL / time.Hour),
			ShellEnabled:            s.shellEnabled,
			ShellImage:              s.shellImage,
			ShellIdleTimeoutMinutes: int(shell.DefaultIdleTimeout / time.Minute),
			ShellMaxLifetimeHours:   int(shell.DefaultMaxLifetime / time.Hour),
		},
		Warnings: settingsWarnings(effective),
	})
}

// updateSettings stores overrides (admin only). A field the caller omits is left
// untouched; a field sent empty is cleared back to its environment default.
func (s *server) updateSettings(c *gin.Context) {
	caller, ok := s.currentUser(c)
	if !ok {
		return
	}

	var req updateSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	values := map[string]string{}
	if req.PublicURL != nil {
		publicURL := strings.TrimRight(strings.TrimSpace(*req.PublicURL), "/")
		if publicURL != "" {
			if err := validatePublicURL(publicURL); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
		}
		values[db.SettingPublicURL] = publicURL
	}
	if req.AgentImage != nil {
		values[db.SettingAgentImage] = strings.TrimSpace(*req.AgentImage)
	}
	if req.AgentNamespace != nil {
		namespace := strings.TrimSpace(*req.AgentNamespace)
		if namespace != "" && !validNamespaceName(namespace) {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "agent namespace must be a valid Kubernetes name (lowercase letters, digits and dashes)",
			})
			return
		}
		values[db.SettingAgentNamespace] = namespace
	}
	if req.AuditRetentionDays != nil {
		days := *req.AuditRetentionDays
		switch {
		case days == 0:
			// Clearing it: the same "empty means default" rule the string
			// settings follow.
			values[db.SettingAuditRetentionDays] = ""
		case days < minAuditRetentionDays || days > maxAuditRetentionDays:
			c.JSON(http.StatusBadRequest, gin.H{
				"error": fmt.Sprintf("audit retention must be between %d and %d days, or 0 to use the default",
					minAuditRetentionDays, maxAuditRetentionDays),
			})
			return
		default:
			values[db.SettingAuditRetentionDays] = strconv.Itoa(days)
		}
	}
	if req.SessionRecordingRetentionDays != nil {
		days := *req.SessionRecordingRetentionDays
		switch {
		case days == 0:
			values[db.SettingSessionRecordingRetentionDays] = ""
		case days < minAuditRetentionDays || days > maxAuditRetentionDays:
			c.JSON(http.StatusBadRequest, gin.H{
				"error": fmt.Sprintf(
					"recording retention must be between %d and %d days, or 0 to follow the audit window",
					minAuditRetentionDays, maxAuditRetentionDays),
			})
			return
		default:
			// Stored as asked even when it exceeds the audit window; the read side
			// clamps it. See clampRecordingRetention — the audit window moves, and a
			// value that was legal when it was saved must not become an error.
			values[db.SettingSessionRecordingRetentionDays] = strconv.Itoa(days)
		}
	}
	if req.AuditVerbs != nil {
		verbs, err := normalizeAuditVerbs(*req.AuditVerbs)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		values[db.SettingAuditVerbs] = strings.Join(verbs, ",")
	}
	if req.RecordExecSessions != nil {
		values[db.SettingRecordExecSessions] = strconv.FormatBool(*req.RecordExecSessions)
	}
	if req.RecordManifestDiffs != nil {
		values[db.SettingRecordManifestDiffs] = strconv.FormatBool(*req.RecordManifestDiffs)
	}
	if req.KubeconfigMaxTTLHours != nil {
		hours := *req.KubeconfigMaxTTLHours
		switch {
		case hours == 0:
			values[db.SettingKubeconfigMaxTTLHours] = ""
		case hours < minKubeconfigMaxTTLHours || hours > maxKubeconfigMaxTTLHours:
			c.JSON(http.StatusBadRequest, gin.H{
				"error": fmt.Sprintf(
					"the kubeconfig ceiling must be between %d and %d hours, or 0 to use the default of %d",
					minKubeconfigMaxTTLHours, maxKubeconfigMaxTTLHours,
					int(k8s.DefaultMaxTTL/time.Hour)),
			})
			return
		default:
			values[db.SettingKubeconfigMaxTTLHours] = strconv.Itoa(hours)
		}
	}

	if req.ShellEnabled != nil {
		values[db.SettingShellEnabled] = strconv.FormatBool(*req.ShellEnabled)
	}
	if req.ShellImage != nil {
		values[db.SettingShellImage] = strings.TrimSpace(*req.ShellImage)
	}
	if req.ShellIdleTimeoutMinutes != nil {
		minutes := *req.ShellIdleTimeoutMinutes
		switch {
		case minutes == 0:
			values[db.SettingShellIdleTimeoutMinutes] = ""
		case minutes < minShellIdleTimeoutMinutes || minutes > maxShellIdleTimeoutMinutes:
			c.JSON(http.StatusBadRequest, gin.H{
				"error": fmt.Sprintf(
					"the shell idle timeout must be between %d and %d minutes, or 0 to use the default of %d",
					minShellIdleTimeoutMinutes, maxShellIdleTimeoutMinutes,
					int(shell.DefaultIdleTimeout/time.Minute)),
			})
			return
		default:
			values[db.SettingShellIdleTimeoutMinutes] = strconv.Itoa(minutes)
		}
	}
	if req.ShellMaxLifetimeHours != nil {
		hours := *req.ShellMaxLifetimeHours
		switch {
		case hours == 0:
			values[db.SettingShellMaxLifetimeHours] = ""
		case hours < minShellMaxLifetimeHours || hours > maxShellMaxLifetimeHours:
			c.JSON(http.StatusBadRequest, gin.H{
				"error": fmt.Sprintf(
					"the shell lifetime must be between %d and %d hours, or 0 to use the default of %d",
					minShellMaxLifetimeHours, maxShellMaxLifetimeHours,
					int(shell.DefaultMaxLifetime/time.Hour)),
			})
			return
		default:
			values[db.SettingShellMaxLifetimeHours] = strconv.Itoa(hours)
		}
	}

	if err := s.store.PutSettings(c.Request.Context(), values, caller.ID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not save the settings"})
		return
	}

	// The gateway reads the policy from memory on every proxied call, so a save
	// that did not republish it would take effect at the next refresh tick instead
	// of now — which reads as a settings page that does not work.
	s.publishAuditPolicy(c.Request.Context())

	s.getSettings(c)
}

// normalizeAuditVerbs validates a submitted verb selection. An empty result is
// legal and means "no selection": see updateSettingsRequest.AuditVerbs.
func normalizeAuditVerbs(raw []string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(raw))
	for _, entry := range raw {
		verb := strings.ToLower(strings.TrimSpace(entry))
		if verb == "" || seen[verb] {
			continue
		}
		if !slices.Contains(auditpolicy.Verbs, verb) {
			return nil, fmt.Errorf(
				"%q is not an auditable verb; choose from %s",
				entry, strings.Join(auditpolicy.Verbs, ", "))
		}
		seen[verb] = true
		out = append(out, verb)
	}
	slices.Sort(out)
	return out, nil
}

// validatePublicURL rejects anything an agent could not dial. The address ends
// up verbatim inside a manifest applied on someone else's cluster, so a bad
// value here surfaces as an agent that never connects.
func validatePublicURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return errInvalidPublicURL
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errInvalidPublicURL
	}
	if parsed.Host == "" {
		return errInvalidPublicURL
	}
	return nil
}

var errInvalidPublicURL = errors.New(
	"server URL must be an absolute http:// or https:// address, for example https://kubemg.example.com")

// settingsWarnings reports configurations that are accepted but will not work
// from inside a target cluster.
func settingsWarnings(s runtimeSettings) []string {
	warnings := []string{}
	// A raised ceiling is a policy an administrator chose, so this is a
	// disclosure rather than a complaint — but it has to be made, because the
	// two connection modes differ on the one thing that matters about a
	// long-lived credential.
	if ceiling := time.Duration(s.KubeconfigMaxTTLHours) * time.Hour; ceiling > k8s.DefaultMaxTTL {
		warnings = append(warnings, fmt.Sprintf(
			"Kubeconfigs may be issued for up to %s. Through an agent tunnel that is safe to revoke — "+
				"every call re-reads the caller's grant — but a direct-mode kubeconfig carries a token "+
				"minted on the cluster, which keeps working until it expires however the grant changes.",
			humanHours(s.KubeconfigMaxTTLHours)))
	}
	parsed, err := url.Parse(s.PublicURL)
	if err != nil || parsed.Host == "" {
		return warnings
	}

	host := parsed.Hostname()
	if isLoopbackHost(host) {
		warnings = append(warnings,
			"The server URL is a loopback address. An agent running inside a cluster resolves it to "+
				"its own pod, so it will never reach KubeMG — set the address the cluster can reach.")
	}
	if parsed.Scheme == "http" && !isLoopbackHost(host) {
		warnings = append(warnings,
			"The server URL is plain http. Agent traffic and kubectl exec both need TLS in production.")
	}
	return warnings
}

// humanHours renders a ceiling the way an operator set it: in days once it is a
// whole number of them, since "2160 hours" is not how anyone says a quarter.
func humanHours(hours int) string {
	switch {
	case hours <= 0:
		return "0 hours"
	case hours%24 == 0:
		days := hours / 24
		if days == 1 {
			return "1 day"
		}
		return fmt.Sprintf("%d days", days)
	case hours == 1:
		return "1 hour"
	default:
		return fmt.Sprintf("%d hours", hours)
	}
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// validNamespaceName is the RFC 1123 label rule Kubernetes applies to a
// namespace.
func validNamespaceName(name string) bool {
	if len(name) > 63 {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '-' && i != 0 && i != len(name)-1:
		default:
			return false
		}
	}
	return true
}
