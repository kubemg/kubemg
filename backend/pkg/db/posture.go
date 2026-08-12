package db

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm/clause"
)

/*
 * Acknowledging a workload security posture finding.
 *
 * A posture finding is a fact read out of a manifest, not a stored object — the
 * scan itself keeps no state and recomputes every finding on every read (see
 * pkg/api/resources_posture.go). What has to persist is the one thing a scan can
 * never know on its own: that a human already looked at this specific finding on
 * this specific object and decided it is there on purpose. Without that,
 * "namespaces with no NetworkPolicy" or "no resource limits" fires forever on the
 * same debug pod and the list becomes noise nobody reads a second time — which is
 * the exact failure the roadmap calls out.
 *
 * This is the first table in KubeMG keyed by the identity of a *live cluster
 * object* rather than by a rule of KubeMG's own (contrast GuardrailPolicy, which
 * matches by regex, or AlarmRule, which narrows by a namespace list). The key is
 * every coordinate needed to say "this exact finding, on this exact object, in
 * this cluster": cluster, kind, namespace, name and the rule id. A workload
 * rescanned tomorrow under the same coordinates finds the same acknowledgement
 * again; a same-named object recreated with a fixed manifest finds nothing,
 * because there is nothing to look up under those coordinates that still fires.
 *
 * Acknowledging is deliberately not deleting the finding: the scan still reports
 * it, still ranked by what it permits, and still visible — it is marked
 * `acknowledged` with who set it, when, and *why*. The reason is mandatory (see
 * the API layer) precisely so this is an audit-able decision rather than a mute
 * button: "seen and accepted, by whom, because of what" is the whole point of
 * keeping the row at all instead of just suppressing the finding client-side.
 */

// PostureAcknowledgement marks one posture finding, on one object, in one
// cluster, as reviewed and accepted rather than fixed.
type PostureAcknowledgement struct {
	ID uint `gorm:"primaryKey" json:"id"`

	ClusterID uint   `gorm:"uniqueIndex:idx_posture_ack_key;not null" json:"cluster_id"`
	Kind      string `gorm:"size:40;uniqueIndex:idx_posture_ack_key;not null" json:"kind"`
	// Namespace is empty for a cluster-scoped finding (today, only the
	// "namespace with no NetworkPolicy" rule, whose Name is itself the
	// namespace) and set for everything else.
	Namespace string `gorm:"size:190;uniqueIndex:idx_posture_ack_key" json:"namespace,omitempty"`
	Name      string `gorm:"size:190;uniqueIndex:idx_posture_ack_key;not null" json:"name"`
	Rule      string `gorm:"size:60;uniqueIndex:idx_posture_ack_key;not null" json:"rule"`

	// Reason is why this finding is here on purpose. The API layer requires it
	// on every write; nothing here defaults it, because a table that can be
	// written with an empty reason is one that eventually will be.
	Reason string `gorm:"type:text;not null" json:"reason"`

	AckedByID uint   `gorm:"index" json:"acked_by_id"`
	AckedBy   string `gorm:"size:120" json:"acked_by"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName pins the table name.
func (PostureAcknowledgement) TableName() string { return "posture_acknowledgements" }

// ListPostureAcknowledgements returns every acknowledgement recorded for a
// cluster, which the scan indexes by (kind, namespace, name, rule) to annotate
// its own findings.
func (s *Store) ListPostureAcknowledgements(ctx context.Context, clusterID uint) ([]PostureAcknowledgement, error) {
	out := []PostureAcknowledgement{}
	if err := s.gdb.WithContext(ctx).
		Where("cluster_id = ?", clusterID).
		Find(&out).Error; err != nil {
		return nil, fmt.Errorf("list posture acknowledgements: %w", err)
	}
	return out, nil
}

// AcknowledgePostureFinding creates or replaces the acknowledgement for one
// finding, keyed by its natural identity rather than a surrogate id the caller
// would have to have fetched first — the same reason ClusterConsole and
// ObservabilitySource upsert on (cluster, kind) rather than requiring a prior
// read. Re-acknowledging updates the reason and who most recently stood behind
// it; it does not stack a second row.
func (s *Store) AcknowledgePostureFinding(ctx context.Context, ack *PostureAcknowledgement) error {
	now := time.Now().UTC()
	ack.UpdatedAt = now
	if ack.CreatedAt.IsZero() {
		ack.CreatedAt = now
	}

	err := s.gdb.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "cluster_id"}, {Name: "kind"}, {Name: "namespace"}, {Name: "name"}, {Name: "rule"},
		},
		DoUpdates: clause.AssignmentColumns([]string{"reason", "acked_by_id", "acked_by", "updated_at"}),
	}).Create(ack).Error
	if err != nil {
		return fmt.Errorf("acknowledge posture finding: %w", err)
	}
	return nil
}

// UnacknowledgePostureFinding removes an acknowledgement, which puts the
// finding it covered back into the plain, unacknowledged list on the next
// scan. It does not touch anything on the cluster — there is nothing on the
// cluster to touch.
func (s *Store) UnacknowledgePostureFinding(
	ctx context.Context, clusterID uint, kind, namespace, name, rule string,
) error {
	res := s.gdb.WithContext(ctx).
		Where("cluster_id = ? AND kind = ? AND namespace = ? AND name = ? AND rule = ?",
			clusterID, kind, namespace, name, rule).
		Delete(&PostureAcknowledgement{})
	if res.Error != nil {
		return fmt.Errorf("unacknowledge posture finding: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}
