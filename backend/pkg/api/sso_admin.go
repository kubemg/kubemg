package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/auth"
	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * Configuring federation.
 *
 * Administrative throughout: a provider row decides who may sign in to the whole
 * platform, and a mapping rule decides what they get when they do. Both are
 * behind requireAdmin, and neither ever returns a secret it was given.
 *
 * The write shape is the one the observability datasources established, because
 * the problem is identical: a secret field left out of a request keeps whatever
 * is stored, so changing an LDAP port does not mean re-typing the bind password,
 * and a secret sent empty clears it.
 */

// ssoProviderRequest is the provider form.
type ssoProviderRequest struct {
	Name     string `json:"name" binding:"required"`
	Protocol string `json:"protocol" binding:"required,oneof=oidc saml ldap"`
	Enabled  *bool  `json:"enabled"`

	IssuerURL string `json:"issuer_url"`
	ClientID  string `json:"client_id"`
	// ClientSecret is write-only. Omitted keeps the stored one.
	ClientSecret *string `json:"client_secret"`
	Scopes       string  `json:"scopes"`

	SAMLMetadataURL string  `json:"saml_metadata_url"`
	SAMLMetadataXML *string `json:"saml_metadata_xml"`
	SAMLEntityID    string  `json:"saml_entity_id"`

	LDAPHost         string  `json:"ldap_host"`
	LDAPPort         int     `json:"ldap_port"`
	LDAPUseTLS       *bool   `json:"ldap_use_tls"`
	LDAPStartTLS     *bool   `json:"ldap_start_tls"`
	LDAPSkipVerify   *bool   `json:"ldap_skip_verify"`
	LDAPBindDN       string  `json:"ldap_bind_dn"`
	LDAPBindPassword *string `json:"ldap_bind_password"`
	LDAPBaseDN       string  `json:"ldap_base_dn"`

	LDAPUserFilter         string `json:"ldap_user_filter"`
	LDAPUserAttribute      string `json:"ldap_user_attribute"`
	LDAPEmailAttribute     string `json:"ldap_email_attribute"`
	LDAPGroupAttribute     string `json:"ldap_group_attribute"`
	LDAPGroupFilter        string `json:"ldap_group_filter"`
	LDAPGroupBaseDN        string `json:"ldap_group_base_dn"`
	LDAPGroupNameAttribute string `json:"ldap_group_name_attribute"`

	UsernameClaim string `json:"username_claim"`
	EmailClaim    string `json:"email_claim"`
	GroupsClaim   string `json:"groups_claim"`

	AllowJIT *bool `json:"allow_jit"`
	// DefaultSystemRole is capped at admin: superadmin is the tier that exists to
	// survive an IdP outage, so no directory can confer it.
	DefaultSystemRole string `json:"default_system_role" binding:"omitempty,oneof=user admin"`
}

// apply folds the request onto a provider record, keeping stored secrets the
// caller did not resend.
func (r ssoProviderRequest) apply(provider *db.SSOProviderConfig) {
	provider.Name = strings.TrimSpace(r.Name)
	provider.Protocol = r.Protocol
	if r.Enabled != nil {
		provider.Enabled = *r.Enabled
	}

	provider.IssuerURL = strings.TrimSpace(r.IssuerURL)
	provider.ClientID = strings.TrimSpace(r.ClientID)
	if r.ClientSecret != nil {
		provider.ClientSecret = strings.TrimSpace(*r.ClientSecret)
	}
	provider.Scopes = strings.TrimSpace(r.Scopes)

	provider.SAMLMetadataURL = strings.TrimSpace(r.SAMLMetadataURL)
	if r.SAMLMetadataXML != nil {
		provider.SAMLMetadataXML = strings.TrimSpace(*r.SAMLMetadataXML)
	}
	provider.SAMLEntityID = strings.TrimSpace(r.SAMLEntityID)

	provider.LDAPHost = strings.TrimSpace(r.LDAPHost)
	provider.LDAPPort = r.LDAPPort
	if r.LDAPUseTLS != nil {
		provider.LDAPUseTLS = *r.LDAPUseTLS
	}
	if r.LDAPStartTLS != nil {
		provider.LDAPStartTLS = *r.LDAPStartTLS
	}
	if r.LDAPSkipVerify != nil {
		provider.LDAPSkipVerify = *r.LDAPSkipVerify
	}
	provider.LDAPBindDN = strings.TrimSpace(r.LDAPBindDN)
	if r.LDAPBindPassword != nil {
		provider.LDAPBindPassword = *r.LDAPBindPassword
	}
	provider.LDAPBaseDN = strings.TrimSpace(r.LDAPBaseDN)
	provider.LDAPUserFilter = strings.TrimSpace(r.LDAPUserFilter)
	provider.LDAPUserAttribute = strings.TrimSpace(r.LDAPUserAttribute)
	provider.LDAPEmailAttribute = strings.TrimSpace(r.LDAPEmailAttribute)
	provider.LDAPGroupAttribute = strings.TrimSpace(r.LDAPGroupAttribute)
	provider.LDAPGroupFilter = strings.TrimSpace(r.LDAPGroupFilter)
	provider.LDAPGroupBaseDN = strings.TrimSpace(r.LDAPGroupBaseDN)
	provider.LDAPGroupNameAttribute = strings.TrimSpace(r.LDAPGroupNameAttribute)

	provider.UsernameClaim = strings.TrimSpace(r.UsernameClaim)
	provider.EmailClaim = strings.TrimSpace(r.EmailClaim)
	provider.GroupsClaim = strings.TrimSpace(r.GroupsClaim)

	if r.AllowJIT != nil {
		provider.AllowJIT = *r.AllowJIT
	}
	if r.DefaultSystemRole != "" {
		provider.DefaultSystemRole = r.DefaultSystemRole
	}
	provider.Normalize()
}

// validateProvider refuses a configuration that cannot possibly work, naming the
// field rather than letting it fail at the first person's sign-in.
func validateProvider(provider *db.SSOProviderConfig) error {
	if provider.Name == "" {
		return errors.New("a provider needs a name")
	}

	switch provider.Protocol {
	case db.ProtocolOIDC:
		if provider.IssuerURL == "" {
			return errors.New("an OIDC provider needs an issuer URL")
		}
		if !strings.HasPrefix(provider.IssuerURL, "https://") &&
			!strings.HasPrefix(provider.IssuerURL, "http://") {
			return errors.New("the issuer URL must be an absolute http(s) URL")
		}
		if provider.ClientID == "" {
			return errors.New("an OIDC provider needs a client ID")
		}
	case db.ProtocolSAML:
		if provider.SAMLMetadataURL == "" && provider.SAMLMetadataXML == "" {
			return errors.New("a SAML provider needs an IdP metadata URL or an IdP metadata document")
		}
	case db.ProtocolLDAP:
		if provider.LDAPHost == "" {
			return errors.New("an LDAP provider needs a host")
		}
		if provider.LDAPBaseDN == "" {
			return errors.New("an LDAP provider needs a base DN")
		}
		if provider.LDAPBindDN != "" && provider.LDAPBindPassword == "" {
			return errors.New("a bind DN needs a bind password")
		}
	}
	return nil
}

// listSSOProviders returns every provider, configuration and all.
func (s *server) listSSOProviders(c *gin.Context) {
	providers, err := s.store.ListSSOProviders(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load the identity providers"})
		return
	}

	publicURL := s.settings(c.Request.Context()).PublicURL
	out := make([]ssoProviderResponse, 0, len(providers))
	for _, provider := range providers {
		provider.Normalize()
		out = append(out, s.toSSOProviderResponse(provider, publicURL))
	}
	c.JSON(http.StatusOK, gin.H{
		"providers": out,
		// The console renders the login redirect against its own origin, so it
		// needs to know which origins the server will accept one for.
		"console_origins": s.consoleOrigins(publicURL),
	})
}

// createSSOProvider registers a provider.
func (s *server) createSSOProvider(c *gin.Context) {
	var req ssoProviderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid provider configuration"})
		return
	}

	provider := &db.SSOProviderConfig{Enabled: true, AllowJIT: true, LDAPUseTLS: true}
	req.apply(provider)
	if err := validateProvider(provider); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := s.store.CreateSSOProvider(c.Request.Context(), provider); err != nil {
		if errors.Is(err, db.ErrConflict) {
			c.JSON(http.StatusConflict, gin.H{"error": "a provider with this name already exists"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not save the identity provider"})
		return
	}

	publicURL := s.settings(c.Request.Context()).PublicURL
	c.JSON(http.StatusCreated, s.toSSOProviderResponse(*provider, publicURL))
}

// updateSSOProvider replaces a provider's configuration.
func (s *server) updateSSOProvider(c *gin.Context) {
	stored, ok := s.provider(c)
	if !ok {
		return
	}

	var req ssoProviderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid provider configuration"})
		return
	}

	req.apply(stored)
	if err := validateProvider(stored); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := s.store.UpdateSSOProvider(c.Request.Context(), stored); err != nil {
		switch {
		case errors.Is(err, db.ErrConflict):
			c.JSON(http.StatusConflict, gin.H{"error": "a provider with this name already exists"})
		case errors.Is(err, db.ErrNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": "identity provider not found"})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "could not save the identity provider"})
		}
		return
	}

	publicURL := s.settings(c.Request.Context()).PublicURL
	c.JSON(http.StatusOK, s.toSSOProviderResponse(*stored, publicURL))
}

// deleteSSOProvider removes a provider and its mapping rules. The accounts it
// provisioned stay: they hold audit history and hand-written grants, and their
// sign-in stops working the moment the provider is gone either way.
func (s *server) deleteSSOProvider(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid provider id"})
		return
	}

	if err := s.store.DeleteSSOProvider(c.Request.Context(), uint(id)); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "identity provider not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not delete the identity provider"})
		return
	}
	c.Status(http.StatusNoContent)
}

// checkSSOProvider proves the configuration reaches the directory, and records
// the verdict. It is the same idea as the cluster and datasource probes: an
// operator finds out here rather than from the first person who cannot sign in.
func (s *server) checkSSOProvider(c *gin.Context) {
	provider, ok := s.provider(c)
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()

	var (
		message string
		err     error
	)
	switch provider.Protocol {
	case db.ProtocolOIDC:
		message, err = auth.CheckOIDC(ctx, provider)
	case db.ProtocolSAML:
		message, err = auth.CheckSAML(ctx, provider)
	case db.ProtocolLDAP:
		message, err = auth.CheckLDAP(ctx, provider)
	default:
		err = errors.New("unsupported provider protocol")
	}

	status := db.StatusHealthy
	if err != nil {
		status, message = db.StatusUnhealthy, err.Error()
	}
	_ = s.store.UpdateSSOProviderHealth(c.Request.Context(), provider.ID, status, message)

	c.JSON(http.StatusOK, gin.H{
		"status":  status,
		"message": message,
	})
}

// ssoMappingRequest is one federation rule.
type ssoMappingRequest struct {
	ProviderID           uint   `json:"provider_id"`
	ExternalGroupPattern string `json:"external_group_pattern" binding:"required"`

	TargetGroupID     uint     `json:"target_group_id"`
	TargetK8sRole     string   `json:"target_k8s_role" binding:"omitempty,oneof=view edit cluster-admin"`
	EnvironmentFilter string   `json:"environment_filter" binding:"omitempty,oneof=prod staging dev"`
	Namespaces        []string `json:"namespaces"`
	TargetSystemRole  string   `json:"target_system_role" binding:"omitempty,oneof=user admin"`
}

// ssoMappingResponse renders the stored namespace scope as the list the UI edits.
type ssoMappingResponse struct {
	db.SSOGroupMapping
	Namespaces []string `json:"namespaces"`
}

func toSSOMappingResponse(mapping db.SSOGroupMapping) ssoMappingResponse {
	return ssoMappingResponse{SSOGroupMapping: mapping, Namespaces: mapping.NamespaceList()}
}

// listSSOMappings returns the rules, optionally for one provider.
func (s *server) listSSOMappings(c *gin.Context) {
	var providerID uint
	if raw := strings.TrimSpace(c.Query("provider_id")); raw != "" {
		parsed, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid provider id"})
			return
		}
		providerID = uint(parsed)
	}

	mappings, err := s.store.ListSSOMappings(c.Request.Context(), providerID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load the mapping rules"})
		return
	}

	out := make([]ssoMappingResponse, 0, len(mappings))
	for _, mapping := range mappings {
		out = append(out, toSSOMappingResponse(mapping))
	}
	c.JSON(http.StatusOK, gin.H{"mappings": out})
}

// createSSOMapping adds a rule.
func (s *server) createSSOMapping(c *gin.Context) {
	var req ssoMappingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid mapping rule"})
		return
	}

	mapping := &db.SSOGroupMapping{ProviderID: req.ProviderID}
	if err := s.applyMappingRequest(c.Request.Context(), req, mapping, true); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := s.store.CreateSSOMapping(c.Request.Context(), mapping); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not save the mapping rule"})
		return
	}
	c.JSON(http.StatusCreated, toSSOMappingResponse(*mapping))
}

// updateSSOMapping replaces a rule's terms.
func (s *server) updateSSOMapping(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid mapping id"})
		return
	}

	var req ssoMappingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid mapping rule"})
		return
	}

	mapping := &db.SSOGroupMapping{ID: uint(id), ProviderID: req.ProviderID}
	if err := s.applyMappingRequest(c.Request.Context(), req, mapping, false); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := s.store.UpdateSSOMapping(c.Request.Context(), mapping); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "mapping rule not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not save the mapping rule"})
		return
	}
	c.JSON(http.StatusOK, toSSOMappingResponse(*mapping))
}

// deleteSSOMapping removes a rule. Whatever it granted is withdrawn on the
// affected accounts' next sign-in, which is when the rules are evaluated —
// revoking someone's session as well means disabling the account.
func (s *server) deleteSSOMapping(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid mapping id"})
		return
	}

	if err := s.store.DeleteSSOMapping(c.Request.Context(), uint(id)); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "mapping rule not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not delete the mapping rule"})
		return
	}
	c.Status(http.StatusNoContent)
}

// applyMappingRequest validates a rule and folds it onto the record. Every
// reference it names is checked to exist, because a rule pointing at a deleted
// group is a rule that silently grants nothing.
func (s *server) applyMappingRequest(
	ctx context.Context, req ssoMappingRequest, mapping *db.SSOGroupMapping, requireProvider bool,
) error {
	pattern := strings.TrimSpace(req.ExternalGroupPattern)
	if pattern == "" {
		return errors.New("a rule needs an external group pattern")
	}

	// A rule that names nothing to confer would match happily and do nothing,
	// which is indistinguishable from a rule whose pattern is wrong.
	if req.TargetGroupID == 0 && req.TargetK8sRole == "" && req.TargetSystemRole == "" {
		return errors.New("a rule must grant a local group, a Kubernetes role, or a system role")
	}
	if req.TargetK8sRole == "" && (req.EnvironmentFilter != "" || len(req.Namespaces) > 0) {
		return errors.New("an environment filter and a namespace scope only apply to a Kubernetes role")
	}

	if requireProvider || req.ProviderID != 0 {
		if _, err := s.store.SSOProviderByID(ctx, req.ProviderID); err != nil {
			if errors.Is(err, db.ErrNotFound) {
				return errors.New("that identity provider does not exist")
			}
			return errors.New("could not verify the identity provider")
		}
	}
	if req.TargetGroupID != 0 {
		if _, err := s.store.GroupByID(ctx, req.TargetGroupID); err != nil {
			if errors.Is(err, db.ErrNotFound) {
				return errors.New("that local group does not exist")
			}
			return errors.New("could not verify the local group")
		}
	}
	for _, namespace := range req.Namespaces {
		if strings.ContainsAny(namespace, " ,") {
			return fmt.Errorf("%q is not a valid namespace name", namespace)
		}
	}
	if req.TargetK8sRole != "" &&
		!slices.Contains([]string{db.K8sRoleView, db.K8sRoleEdit, db.K8sRoleClusterAdmin}, req.TargetK8sRole) {
		return errors.New("unknown Kubernetes role")
	}

	mapping.ProviderID = req.ProviderID
	mapping.ExternalGroupPattern = pattern
	mapping.TargetGroupID = req.TargetGroupID
	mapping.TargetK8sRole = req.TargetK8sRole
	mapping.EnvironmentFilter = req.EnvironmentFilter
	mapping.Namespaces = db.JoinNamespaces(req.Namespaces)
	mapping.TargetSystemRole = req.TargetSystemRole
	return nil
}
