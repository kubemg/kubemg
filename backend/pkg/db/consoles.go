package db

import (
	"context"
	"fmt"
	"slices"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

/*
 * Where a cluster's *other* consoles live.
 *
 * A cluster already declares where its metrics and logs are (see
 * observability.go), and KubeMG draws charts from them — but the moment a
 * question outgrows the fixed chart catalogue, the answer is in a Grafana
 * somebody has to go and find, in another tab, at a URL nobody wrote down. The
 * same holds for the GitOps tool that owns half the workloads in Explore.
 *
 * This is deliberately **a link and not an embed, and never a proxy.** An
 * iframe would inherit this console's origin and its session; proxying a whole
 * Grafana through the agent tunnel would mean carrying a second application's
 * routing, assets and websockets inside a transport built for the Kubernetes
 * API. So what is stored is an address and nothing else: KubeMG holds no
 * session for either tool, and the operator authenticates to them as
 * themselves. That is also why a URL carrying userinfo is refused — a link with
 * a credential in it is a credential, and this table stores none.
 */

// Console kinds a cluster can carry. Each is a whole application with its own
// identity and its own login; KubeMG only knows where it is.
const (
	ConsoleGrafana = "grafana"
	ConsoleArgoCD  = "argocd"
)

// ConsoleKinds enumerates the console kinds a cluster can carry.
var ConsoleKinds = []string{ConsoleGrafana, ConsoleArgoCD}

// ValidConsoleKind reports whether a console kind is one KubeMG stores.
func ValidConsoleKind(kind string) bool { return slices.Contains(ConsoleKinds, kind) }

// ClusterConsole is one external console registered against one cluster.
//
// One row per cluster per kind: a cluster has *the* Grafana that answers for it,
// not a list of candidates — the same rule the datasource rows follow, for the
// same reason.
type ClusterConsole struct {
	ID        uint   `gorm:"primaryKey" json:"id"`
	ClusterID uint   `gorm:"uniqueIndex:idx_console_cluster_kind;not null" json:"cluster_id"`
	Kind      string `gorm:"size:20;uniqueIndex:idx_console_cluster_kind;not null" json:"kind"`

	// URL is the console's own base address, as a browser would open it.
	URL string `gorm:"size:512;not null" json:"url"`

	// Ref is the one identifier the target needs to open on the right thing
	// rather than on its front page: an Argo CD project. It is optional, and a
	// console with none still gets a bare link.
	//
	// Grafana's equivalent — which datasource a query belongs to — is stored on
	// the datasource row instead (ObservabilitySource.GrafanaDatasource),
	// because that is the thing it identifies.
	Ref string `gorm:"size:190" json:"ref,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName pins the table name.
func (ClusterConsole) TableName() string { return "cluster_consoles" }

// ClusterConsoles returns every console registered for a cluster.
func (s *Store) ClusterConsoles(ctx context.Context, clusterID uint) ([]ClusterConsole, error) {
	consoles := []ClusterConsole{}
	err := s.gdb.WithContext(ctx).
		Where("cluster_id = ?", clusterID).
		Order("kind asc").
		Find(&consoles).Error
	if err != nil {
		return nil, fmt.Errorf("cluster consoles: %w", err)
	}
	return consoles, nil
}

// PutClusterConsole registers or replaces one console, keyed by cluster and kind.
func (s *Store) PutClusterConsole(ctx context.Context, console *ClusterConsole) error {
	now := time.Now().UTC()
	console.UpdatedAt = now
	if console.CreatedAt.IsZero() {
		console.CreatedAt = now
	}

	err := s.gdb.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "cluster_id"}, {Name: "kind"}},
		DoUpdates: clause.AssignmentColumns([]string{"url", "ref", "updated_at"}),
	}).Create(console).Error
	if err != nil {
		return fmt.Errorf("put cluster console: %w", err)
	}
	return nil
}

// DeleteClusterConsole removes one cluster's console of a given kind.
func (s *Store) DeleteClusterConsole(ctx context.Context, clusterID uint, kind string) error {
	res := s.gdb.WithContext(ctx).
		Where("cluster_id = ? AND kind = ?", clusterID, kind).
		Delete(&ClusterConsole{})
	if res.Error != nil {
		return fmt.Errorf("delete cluster console: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// deleteClusterConsoles removes a cluster's consoles with it. A link to another
// tool is a property of the cluster, so it does not outlive the row.
func deleteClusterConsoles(tx *gorm.DB, clusterID uint) error {
	if err := tx.Where("cluster_id = ?", clusterID).Delete(&ClusterConsole{}).Error; err != nil {
		return fmt.Errorf("delete cluster consoles: %w", err)
	}
	return nil
}
