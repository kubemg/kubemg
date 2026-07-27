package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/db"
	"github.com/kubemg/kubemg/backend/pkg/observability"
)

/*
 * Where a cluster's metrics and logs come from, registered per cluster.
 *
 * This is the configuration surface, not the query path: it stores which
 * backend a cluster has, checks that KubeMG can actually reach it, and finds the
 * one that is probably already running so an operator does not have to go and
 * read a Service list to fill in a form.
 *
 * Reading the configuration is open to anyone the cluster is granted to — a
 * developer needs to know a series backend exists before they can be shown a
 * chart from it — but writing it is administrative, and the credential never
 * leaves the server in either direction.
 */

// sourceResponse is one datasource as the UI sees it. The credential is
// represented by whether there is one, never by its value.
type sourceResponse struct {
	Kind          string `json:"kind"`
	Provider      string `json:"provider"`
	ProviderLabel string `json:"provider_label"`
	AccessMode    string `json:"access_mode"`

	URL              string `json:"url,omitempty"`
	ServiceNamespace string `json:"service_namespace,omitempty"`
	ServiceName      string `json:"service_name,omitempty"`
	ServicePort      string `json:"service_port,omitempty"`
	ServiceScheme    string `json:"service_scheme,omitempty"`
	PathPrefix       string `json:"path_prefix,omitempty"`

	AuthMode           string `json:"auth_mode"`
	Username           string `json:"username,omitempty"`
	HasCredential      bool   `json:"has_credential"`
	InsecureSkipVerify bool   `json:"insecure_skip_verify"`

	Enabled bool `json:"enabled"`

	// Endpoint is the address this resolves to, rendered for display.
	Endpoint        string     `json:"endpoint"`
	LastStatus      string     `json:"last_status"`
	LastMessage     string     `json:"last_message,omitempty"`
	DetectedVersion string     `json:"detected_version,omitempty"`
	LastCheckedAt   *time.Time `json:"last_checked_at,omitempty"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

func toSourceResponse(source db.ObservabilitySource) sourceResponse {
	return sourceResponse{
		Kind:               source.Kind,
		Provider:           source.Provider,
		ProviderLabel:      observability.Label(source.Provider),
		AccessMode:         source.AccessMode,
		URL:                source.URL,
		ServiceNamespace:   source.ServiceNamespace,
		ServiceName:        source.ServiceName,
		ServicePort:        source.ServicePort,
		ServiceScheme:      source.ServiceScheme,
		PathPrefix:         source.PathPrefix,
		AuthMode:           source.AuthMode,
		Username:           source.Username,
		HasCredential:      source.HasCredential(),
		InsecureSkipVerify: source.InsecureSkipVerify,
		Enabled:            source.Enabled,
		Endpoint:           observability.TargetOf(source).Endpoint(),
		LastStatus:         source.LastStatus,
		LastMessage:        source.LastMessage,
		DetectedVersion:    source.DetectedVersion,
		LastCheckedAt:      source.LastCheckedAt,
		UpdatedAt:          source.UpdatedAt,
	}
}

// sourceRequest is the datasource form, used both to save and to check a draft
// that has not been saved yet — which is what makes "test before you commit"
// possible in the registration wizard.
type sourceRequest struct {
	Provider   string `json:"provider" binding:"required"`
	AccessMode string `json:"access_mode" binding:"required,oneof=in-cluster direct"`

	URL string `json:"url"`

	ServiceNamespace string `json:"service_namespace"`
	ServiceName      string `json:"service_name"`
	ServicePort      string `json:"service_port"`
	ServiceScheme    string `json:"service_scheme" binding:"omitempty,oneof=http https"`

	PathPrefix string `json:"path_prefix"`

	AuthMode string `json:"auth_mode" binding:"omitempty,oneof=none bearer basic"`
	Username string `json:"username"`
	// Credential is write-only. Omitted keeps whatever is stored, so an operator
	// can edit the port without re-typing a token; sent empty, it is cleared.
	Credential *string `json:"credential"`

	InsecureSkipVerify bool `json:"insecure_skip_verify"`
	// Enabled defaults to true: a source someone just filled in is one they want
	// used.
	Enabled *bool `json:"enabled"`
}

// target renders the request as something callable, folding in the stored
// credential when the caller did not send a new one.
func (r sourceRequest) target(kind string, stored *db.ObservabilitySource) observability.Target {
	authMode := r.AuthMode
	if authMode == "" {
		authMode = db.AuthNone
	}

	credential := ""
	switch {
	case r.Credential != nil:
		credential = *r.Credential
	case stored != nil:
		credential = stored.Credential
	}
	if authMode == db.AuthNone {
		credential = ""
	}

	return observability.Target{
		Kind:               kind,
		Provider:           strings.TrimSpace(r.Provider),
		AccessMode:         r.AccessMode,
		URL:                strings.TrimSpace(r.URL),
		ServiceNamespace:   strings.TrimSpace(r.ServiceNamespace),
		ServiceName:        strings.TrimSpace(r.ServiceName),
		ServicePort:        strings.TrimSpace(r.ServicePort),
		ServiceScheme:      r.ServiceScheme,
		PathPrefix:         strings.TrimSpace(r.PathPrefix),
		AuthMode:           authMode,
		Username:           strings.TrimSpace(r.Username),
		Credential:         credential,
		InsecureSkipVerify: r.InsecureSkipVerify,
	}
}

// sourceKind resolves and validates the :kind path parameter.
func sourceKind(c *gin.Context) (string, bool) {
	kind := c.Param("kind")
	if !db.ValidSourceKind(kind) {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "a datasource is either metrics or logs",
		})
		return "", false
	}
	return kind, true
}

// listObservabilitySources returns the datasources registered for a cluster,
// alongside what the caller may do with them.
func (s *server) listObservabilitySources(c *gin.Context) {
	user, cluster, _, _, ok := s.loadAuthorizedCluster(c)
	if !ok {
		return
	}

	sources, err := s.store.ObservabilitySources(c.Request.Context(), cluster.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load the datasources"})
		return
	}

	out := make([]sourceResponse, 0, len(sources))
	for _, source := range sources {
		out = append(out, toSourceResponse(source))
	}

	c.JSON(http.StatusOK, gin.H{
		"sources": out,
		// Discovery and in-cluster access both ride the agent tunnel, so a
		// direct-mode cluster can only carry an external datasource. Saying so
		// here keeps the UI from offering a button that cannot work.
		"agent_attached": s.tunnels != nil && connectionMode(*cluster) == db.ModeAgent &&
			s.tunnels.Connected(cluster.ID),
		"connection_mode": connectionMode(*cluster),
		"editable":        user.IsAdmin(),
	})
}

// putObservabilitySource registers or replaces a cluster's datasource of one
// kind (admin only). Saving does not require the backend to be reachable — an
// operator may well be configuring it before it exists — but the check runs
// anyway and its verdict is stored, so nothing is quietly assumed to work.
func (s *server) putObservabilitySource(c *gin.Context) {
	user, cluster, _, _, ok := s.loadAuthorizedCluster(c)
	if !ok {
		return
	}
	kind, ok := sourceKind(c)
	if !ok {
		return
	}

	var req sourceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := c.Request.Context()
	stored, err := s.store.ObservabilitySource(ctx, cluster.ID, kind)
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load the existing datasource"})
		return
	}

	target := req.target(kind, stored)
	if err := target.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if target.AccessMode == db.AccessInCluster && connectionMode(*cluster) != db.ModeAgent {
		c.JSON(http.StatusConflict, gin.H{
			"error": "an in-cluster datasource is reached through the agent tunnel, " +
				"which a direct-mode cluster does not have — give its external address instead",
		})
		return
	}

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	source := db.ObservabilitySource{
		ClusterID:          cluster.ID,
		Kind:               kind,
		Provider:           target.Provider,
		AccessMode:         target.AccessMode,
		URL:                target.URL,
		ServiceNamespace:   target.ServiceNamespace,
		ServiceName:        target.ServiceName,
		ServicePort:        target.ServicePort,
		ServiceScheme:      target.ServiceScheme,
		PathPrefix:         target.PathPrefix,
		AuthMode:           target.AuthMode,
		Username:           target.Username,
		Credential:         target.Credential,
		InsecureSkipVerify: target.InsecureSkipVerify,
		Enabled:            enabled,
	}
	if stored != nil {
		source.ID = stored.ID
		source.CreatedAt = stored.CreatedAt
	}

	// Check on the way in. A datasource that is saved and never checked is one
	// whose first failure surfaces as an empty chart weeks later.
	result := observability.Probe(ctx, target, s.tunnelCall(user, cluster))
	source.LastStatus = db.SourceStatusUnhealthy
	if result.Reachable {
		source.LastStatus = db.SourceStatusHealthy
	}
	source.LastMessage = result.Message
	source.DetectedVersion = result.Version
	checkedAt := time.Now().UTC()
	source.LastCheckedAt = &checkedAt

	if err := s.store.PutObservabilitySource(ctx, &source); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not save the datasource"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"source": toSourceResponse(source), "check": result})
}

// deleteObservabilitySource removes a cluster's datasource (admin only).
func (s *server) deleteObservabilitySource(c *gin.Context) {
	_, cluster, _, _, ok := s.loadAuthorizedCluster(c)
	if !ok {
		return
	}
	kind, ok := sourceKind(c)
	if !ok {
		return
	}

	err := s.store.DeleteObservabilitySource(c.Request.Context(), cluster.ID, kind)
	if errors.Is(err, db.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "this cluster has no " + kind + " datasource"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not remove the datasource"})
		return
	}

	c.Status(http.StatusNoContent)
}

// testObservabilitySource checks a datasource that has not been saved, which is
// what the registration wizard needs: the operator finds out the address is
// wrong while they are still looking at the field holding it.
func (s *server) testObservabilitySource(c *gin.Context) {
	user, cluster, _, _, ok := s.loadAuthorizedCluster(c)
	if !ok {
		return
	}
	kind, ok := sourceKind(c)
	if !ok {
		return
	}

	var req sourceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// A draft that leaves the credential out is testing the stored one, which is
	// how "check this again" works without making anyone re-type a token.
	stored, err := s.store.ObservabilitySource(c.Request.Context(), cluster.ID, kind)
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load the existing datasource"})
		return
	}

	target := req.target(kind, stored)
	if err := target.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, observability.Probe(c.Request.Context(), target, s.tunnelCall(user, cluster)))
}

// checkObservabilitySource re-checks the stored datasource and records the
// verdict, so the cluster page can say when it was last known good.
func (s *server) checkObservabilitySource(c *gin.Context) {
	user, cluster, _, _, ok := s.loadAuthorizedCluster(c)
	if !ok {
		return
	}
	kind, ok := sourceKind(c)
	if !ok {
		return
	}

	ctx := c.Request.Context()
	stored, err := s.store.ObservabilitySource(ctx, cluster.ID, kind)
	if errors.Is(err, db.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "this cluster has no " + kind + " datasource"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load the datasource"})
		return
	}

	result := observability.Probe(ctx, observability.TargetOf(*stored), s.tunnelCall(user, cluster))

	health := db.SourceHealth{
		Status:          db.SourceStatusUnhealthy,
		Message:         result.Message,
		DetectedVersion: result.Version,
		CheckedAt:       time.Now().UTC(),
	}
	if result.Reachable {
		health.Status = db.SourceStatusHealthy
	}
	if err := s.store.UpdateSourceHealth(ctx, stored.ID, health); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not record the check result"})
		return
	}

	stored.LastStatus = health.Status
	stored.LastMessage = health.Message
	stored.DetectedVersion = health.DetectedVersion
	stored.LastCheckedAt = &health.CheckedAt

	c.JSON(http.StatusOK, gin.H{"source": toSourceResponse(*stored), "check": result})
}

// discoverObservabilitySources looks for a datasource already running in the
// cluster. It reads Services through the same tunnel, impersonation and audit
// trail as every other read — discovery is not a privileged back door.
func (s *server) discoverObservabilitySources(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}

	var list struct {
		Items []struct {
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
			Spec struct {
				Ports []struct {
					Name string `json:"name"`
					Port int32  `json:"port"`
				} `json:"ports"`
			} `json:"spec"`
		} `json:"items"`
	}
	if !s.fetch(c, user, cluster, grant, "/api/v1/services", &list) {
		return
	}

	services := make([]observability.ServiceRef, 0, len(list.Items))
	for _, item := range list.Items {
		ports := make([]observability.ServicePort, 0, len(item.Spec.Ports))
		for _, port := range item.Spec.Ports {
			ports = append(ports, observability.ServicePort{Name: port.Name, Port: port.Port})
		}
		services = append(services, observability.ServiceRef{
			Namespace: item.Metadata.Namespace,
			Name:      item.Metadata.Name,
			Ports:     ports,
		})
	}

	c.JSON(http.StatusOK, gin.H{"candidates": observability.Discover(services)})
}

// tunnelCall gives the prober a way into the cluster, or nothing when there is
// no way in. An admin is asserted as cluster-admin here for the same reason the
// resource reads do it: they hold no stored grant, and the API server still has
// to be told who is asking.
func (s *server) tunnelCall(user *db.User, cluster *db.Cluster) observability.TunnelCall {
	if s.proxy == nil || connectionMode(*cluster) != db.ModeAgent {
		return nil
	}
	grant := db.UserClusterAccess{
		UserID:    user.ID,
		ClusterID: cluster.ID,
		K8sRole:   db.K8sRoleClusterAdmin,
	}
	return func(ctx context.Context, method, path string, body []byte) (int, []byte, error) {
		resp, err := s.proxy.Call(ctx, user, cluster, grant, method, path, body)
		if err != nil {
			return 0, nil, err
		}
		return resp.Status, resp.Body, nil
	}
}
