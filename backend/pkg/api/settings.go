package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/db"
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
}

// Audit retention bounds. The floor stops an operator from silently emptying
// the trail with a fat-fingered zero-ish value; the ceiling is ten years, past
// which a retention policy is really an archive and belongs somewhere else.
const (
	minAuditRetentionDays = 1
	maxAuditRetentionDays = 3650
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
	}
	stored, err := s.store.Settings(ctx)
	if err != nil {
		return out
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
	return out
}

// storedRetentionDays reads the retention override. A value that is not a
// usable number reads as unset, so a hand-edited row cannot turn the pruner
// into something that deletes everything.
func storedRetentionDays(stored map[string]string) int {
	raw := strings.TrimSpace(stored[db.SettingAuditRetentionDays])
	if raw == "" {
		return 0
	}
	days, err := strconv.Atoi(raw)
	if err != nil || days < minAuditRetentionDays || days > maxAuditRetentionDays {
		return 0
	}
	return days
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
	c.JSON(http.StatusOK, settingsResponse{
		Effective: effective,
		Overrides: runtimeSettings{
			PublicURL:          strings.TrimSpace(stored[db.SettingPublicURL]),
			AgentImage:         strings.TrimSpace(stored[db.SettingAgentImage]),
			AgentNamespace:     strings.TrimSpace(stored[db.SettingAgentNamespace]),
			AuditRetentionDays: storedRetentionDays(stored),
		},
		Defaults: runtimeSettings{
			PublicURL:          s.publicURL,
			AgentImage:         s.agentImage,
			AgentNamespace:     s.agentNamespace,
			AuditRetentionDays: s.auditRetentionDays,
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

	if err := s.store.PutSettings(c.Request.Context(), values, caller.ID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not save the settings"})
		return
	}

	s.getSettings(c)
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
