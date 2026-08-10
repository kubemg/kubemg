package db

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

/*
 * Where a cluster's metrics and logs come from.
 *
 * KubeMG's own Metrics API read answers "right now" and nothing more, so a
 * cluster that has a series backend — VictoriaMetrics, Prometheus, Thanos — has
 * to say so somewhere. That somewhere is the cluster record, because the
 * datasource belongs to the cluster and not to the server: two clusters have two
 * Prometheuses, and an operator registering the second one should not have to
 * edit a global config file.
 *
 * A source is stored per cluster *and per kind*, so metrics and logs are two
 * independent rows with the same shape. Nothing in the query path is written
 * yet — this is the registration, the credential and the reachability check.
 */

// Observability source kinds.
const (
	SourceMetrics = "metrics"
	SourceLogs    = "logs"
)

// SourceKinds enumerates the datasource kinds a cluster can carry.
var SourceKinds = []string{SourceMetrics, SourceLogs}

// Providers KubeMG knows how to talk to. The metrics four all speak the
// Prometheus query API, which is why they share one probe; the logs two do not
// speak anything in common, so each carries its own.
const (
	ProviderVictoriaMetrics = "victoriametrics"
	ProviderPrometheus      = "prometheus"
	ProviderThanos          = "thanos"
	ProviderMimir           = "mimir"

	ProviderVictoriaLogs = "victorialogs"
	ProviderLoki         = "loki"
)

// ProvidersFor lists the providers assignable to a datasource kind.
func ProvidersFor(kind string) []string {
	switch kind {
	case SourceMetrics:
		return []string{ProviderVictoriaMetrics, ProviderPrometheus, ProviderThanos, ProviderMimir}
	case SourceLogs:
		return []string{ProviderVictoriaLogs, ProviderLoki}
	default:
		return nil
	}
}

// How KubeMG reaches a datasource.
const (
	// AccessInCluster reaches it through the cluster's own agent tunnel, by
	// asking the API server to proxy to a Service. Nothing has to be exposed
	// outside the cluster and the call is impersonated and audited like every
	// other read KubeMG makes.
	AccessInCluster = "in-cluster"
	// AccessDirect dials the URL from KubeMG itself. This is the shape a central
	// Thanos or a hosted Mimir takes, where the series live outside the cluster
	// they describe.
	AccessDirect = "direct"
)

// AccessModes enumerates the assignable access modes.
var AccessModes = []string{AccessInCluster, AccessDirect}

// Datasource authentication modes.
const (
	AuthNone   = "none"
	AuthBearer = "bearer"
	AuthBasic  = "basic"
)

// AuthModes enumerates the assignable authentication modes.
var AuthModes = []string{AuthNone, AuthBearer, AuthBasic}

// Source probe outcomes, reusing the cluster status vocabulary so the UI renders
// them with the same tones.
const (
	SourceStatusPending   = StatusPending
	SourceStatusHealthy   = StatusHealthy
	SourceStatusUnhealthy = StatusUnhealthy
)

// ValidSourceKind reports whether a datasource kind is one KubeMG stores.
func ValidSourceKind(kind string) bool { return slices.Contains(SourceKinds, kind) }

// ValidProvider reports whether a provider can serve the given kind.
func ValidProvider(kind, provider string) bool {
	return slices.Contains(ProvidersFor(kind), provider)
}

// ValidAccessMode reports whether an access mode is assignable.
func ValidAccessMode(mode string) bool { return slices.Contains(AccessModes, mode) }

// ValidAuthMode reports whether an authentication mode is assignable.
func ValidAuthMode(mode string) bool { return slices.Contains(AuthModes, mode) }

// ObservabilitySource is one cluster's metrics or logs backend.
//
// The credential is treated exactly like a cluster's service account token:
// stored, never serialized. A caller that needs to know whether one is set reads
// the rendered HasCredential flag instead.
type ObservabilitySource struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// One source per cluster per kind: a cluster has *the* metrics backend, not
	// a list of candidates.
	ClusterID uint   `gorm:"uniqueIndex:idx_source_cluster_kind;not null" json:"cluster_id"`
	Kind      string `gorm:"size:20;uniqueIndex:idx_source_cluster_kind;not null" json:"kind"`

	Provider   string `gorm:"size:40;not null" json:"provider"`
	AccessMode string `gorm:"size:20;not null;default:in-cluster" json:"access_mode"`

	// URL is the base address in direct mode. It is ignored in in-cluster mode,
	// where the Service reference below names the target instead.
	URL string `gorm:"size:512" json:"url,omitempty"`

	// The Service the API server is asked to proxy to, in in-cluster mode.
	ServiceNamespace string `gorm:"size:190" json:"service_namespace,omitempty"`
	ServiceName      string `gorm:"size:190" json:"service_name,omitempty"`
	// ServicePort is a port name or number, kept as text because Kubernetes
	// accepts either in the proxy path.
	ServicePort   string `gorm:"size:40" json:"service_port,omitempty"`
	ServiceScheme string `gorm:"size:8" json:"service_scheme,omitempty"`

	// PathPrefix is what sits in front of the provider's own API paths. A
	// Prometheus behind a reverse proxy at /prometheus needs it; most do not.
	PathPrefix string `gorm:"size:190" json:"path_prefix,omitempty"`

	AuthMode string `gorm:"size:20;not null;default:none" json:"auth_mode"`
	Username string `gorm:"size:190" json:"username,omitempty"`
	// Credential is the bearer token or the basic-auth password.
	Credential string `gorm:"type:text" json:"-"`

	// InsecureSkipVerify applies to direct mode only; an in-cluster call rides
	// the tunnel and never terminates TLS here.
	InsecureSkipVerify bool `gorm:"not null;default:false" json:"insecure_skip_verify"`

	// Enabled lets an operator park a source without deleting its credential.
	Enabled bool `gorm:"not null;default:true" json:"enabled"`

	// GrafanaDatasource is this backend's uid in the cluster's registered
	// Grafana, which is the one thing an Explore deep link cannot be built
	// without. It is stored here rather than on the Grafana console row because
	// it identifies *this* source: one Grafana holds the metrics datasource and
	// the logs one, and they are two different uids.
	GrafanaDatasource string `gorm:"size:190" json:"grafana_datasource,omitempty"`

	LastCheckedAt *time.Time `json:"last_checked_at,omitempty"`
	LastStatus    string     `gorm:"size:20;not null;default:pending" json:"last_status"`
	LastMessage   string     `gorm:"type:text" json:"last_message,omitempty"`
	// DetectedVersion is what the backend reported about itself at the last
	// check, which is the fastest way to tell a real VictoriaMetrics from
	// something answering on the same port.
	DetectedVersion string `gorm:"size:80" json:"detected_version,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName pins the table name.
func (ObservabilitySource) TableName() string { return "observability_sources" }

// HasCredential reports whether a secret is stored for this source.
func (s ObservabilitySource) HasCredential() bool { return strings.TrimSpace(s.Credential) != "" }

// SourceHealth is the outcome of a datasource probe, ready to persist.
type SourceHealth struct {
	Status          string
	Message         string
	DetectedVersion string
	CheckedAt       time.Time
}

// ObservabilitySources returns every datasource registered for a cluster.
func (s *Store) ObservabilitySources(ctx context.Context, clusterID uint) ([]ObservabilitySource, error) {
	sources := []ObservabilitySource{}
	err := s.gdb.WithContext(ctx).
		Where("cluster_id = ?", clusterID).
		Order("kind asc").
		Find(&sources).Error
	if err != nil {
		return nil, fmt.Errorf("observability sources: %w", err)
	}
	return sources, nil
}

// ObservabilitySource loads one cluster's datasource of a given kind.
func (s *Store) ObservabilitySource(ctx context.Context, clusterID uint, kind string) (*ObservabilitySource, error) {
	var source ObservabilitySource
	err := s.gdb.WithContext(ctx).
		Where("cluster_id = ? AND kind = ?", clusterID, kind).
		First(&source).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("observability source: %w", err)
	}
	return &source, nil
}

// PutObservabilitySource upserts a cluster's datasource, keyed by cluster and
// kind. The caller has already resolved whether the credential is a new one or
// the stored one being kept, so what arrives here is written as given.
func (s *Store) PutObservabilitySource(ctx context.Context, source *ObservabilitySource) error {
	now := time.Now().UTC()
	source.UpdatedAt = now
	if source.CreatedAt.IsZero() {
		source.CreatedAt = now
	}
	if source.LastStatus == "" {
		source.LastStatus = SourceStatusPending
	}

	err := s.gdb.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "cluster_id"}, {Name: "kind"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"provider", "access_mode", "url",
			"service_namespace", "service_name", "service_port", "service_scheme",
			"path_prefix", "auth_mode", "username", "credential",
			"insecure_skip_verify", "enabled", "grafana_datasource",
			"last_checked_at", "last_status", "last_message", "detected_version",
			"updated_at",
		}),
	}).Create(source).Error
	if err != nil {
		return fmt.Errorf("put observability source: %w", err)
	}
	return nil
}

// UpdateSourceHealth records the result of a datasource probe.
func (s *Store) UpdateSourceHealth(ctx context.Context, id uint, health SourceHealth) error {
	res := s.gdb.WithContext(ctx).Model(&ObservabilitySource{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"last_status":      health.Status,
			"last_message":     health.Message,
			"detected_version": health.DetectedVersion,
			"last_checked_at":  health.CheckedAt,
			"updated_at":       health.CheckedAt,
		})
	if res.Error != nil {
		return fmt.Errorf("update source health: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteObservabilitySource removes one cluster's datasource of a given kind.
func (s *Store) DeleteObservabilitySource(ctx context.Context, clusterID uint, kind string) error {
	res := s.gdb.WithContext(ctx).
		Where("cluster_id = ? AND kind = ?", clusterID, kind).
		Delete(&ObservabilitySource{})
	if res.Error != nil {
		return fmt.Errorf("delete observability source: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}
