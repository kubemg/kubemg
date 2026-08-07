package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// Store is the GORM-backed persistence layer for KubeMG.
type Store struct {
	gdb *gorm.DB
}

// NewStore wraps a GORM handle.
func NewStore(gdb *gorm.DB) *Store { return &Store{gdb: gdb} }

// DB exposes the underlying GORM handle.
func (s *Store) DB() *gorm.DB { return s.gdb }

// UserByUsername looks up a user by its unique username.
func (s *Store) UserByUsername(ctx context.Context, username string) (*User, error) {
	var user User
	err := s.gdb.WithContext(ctx).Where("username = ?", username).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("user by username: %w", err)
	}
	return &user, nil
}

// UserByID looks up a user by primary key.
func (s *Store) UserByID(ctx context.Context, id uint) (*User, error) {
	var user User
	err := s.gdb.WithContext(ctx).First(&user, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("user by id: %w", err)
	}
	return &user, nil
}

// CreateUser inserts a new user record.
func (s *Store) CreateUser(ctx context.Context, user *User) error {
	user.Normalize()
	if err := s.gdb.WithContext(ctx).Create(user).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return ErrConflict
		}
		return fmt.Errorf("create user: %w", err)
	}
	return nil
}

// CountUsers returns the number of stored users.
func (s *Store) CountUsers(ctx context.Context) (int64, error) {
	var n int64
	if err := s.gdb.WithContext(ctx).Model(&User{}).Count(&n).Error; err != nil {
		return 0, fmt.Errorf("count users: %w", err)
	}
	return n, nil
}

// ClustersForUser returns every cluster the user may act on. Admins see all
// registered clusters; regular users see clusters granted to them directly via
// user_cluster_access or inherited from a group they belong to.
func (s *Store) ClustersForUser(ctx context.Context, user *User) ([]Cluster, error) {
	q := s.gdb.WithContext(ctx).Model(&Cluster{}).Order("name asc")
	if !user.IsAdmin() {
		// An expired grant is not access, so it must not keep a cluster in the
		// fleet list either: a cluster still listed after its elevation ran out
		// would open onto refusals from the resource reads.
		direct := s.gdb.Model(&UserClusterAccess{}).
			Select("cluster_id").
			Where("user_id = ?", user.ID).
			Where(liveGrantCond, time.Now().UTC())
		inherited := s.gdb.Model(&GroupClusterAccess{}).
			Select("cluster_id").
			Where("group_id IN (?)", s.gdb.Model(&UserGroup{}).
				Select("group_id").
				Where("user_id = ?", user.ID))
		q = q.Where("id IN (?) OR id IN (?)", direct, inherited)
	}

	clusters := []Cluster{}
	if err := q.Find(&clusters).Error; err != nil {
		return nil, fmt.Errorf("clusters for user: %w", err)
	}
	return clusters, nil
}

// liveGrantCond separates a grant from the memory of one: no expiry, or an expiry
// still ahead. It takes the instant as a parameter rather than reading the
// database's own NOW() on purpose — the expiry was computed by this process when
// the approval was recorded, so one clock decides both ends of the window. A
// second clock would mean a few seconds of access nobody approved, or a few
// seconds refused before the window closed.
const liveGrantCond = "expires_at IS NULL OR expires_at > ?"

// AccessForUser returns the user's effective cluster grants keyed by cluster
// ID: direct grants merged with everything inherited from their groups. The
// more permissive grant wins, so adding someone to a group can never take
// access away.
//
// Two things are resolved here rather than by any caller, and both are the whole
// authorization decision for a proxied call:
//
//   - An **expired** grant is dropped. A JIT elevation stops counting the second
//     its window ends, whether or not the sweeper has got round to deleting the
//     row, because this is the read every proxied request goes through.
//   - A user may hold several direct rows for one cluster — a standing grant, a
//     federated one, a live elevation — so they are *merged* rather than one
//     overwriting another. Overwriting would make the answer depend on row order,
//     which is how an elevation would silently take away the access it was
//     supposed to add.
func (s *Store) AccessForUser(ctx context.Context, userID uint) (map[uint]UserClusterAccess, error) {
	direct := []UserClusterAccess{}
	err := s.gdb.WithContext(ctx).
		Where("user_id = ?", userID).
		Where(liveGrantCond, time.Now().UTC()).
		Find(&direct).Error
	if err != nil {
		return nil, fmt.Errorf("access for user: %w", err)
	}

	inherited := []GroupClusterAccess{}
	err = s.gdb.WithContext(ctx).
		Where("group_id IN (?)", s.gdb.Model(&UserGroup{}).
			Select("group_id").
			Where("user_id = ?", userID)).
		Find(&inherited).Error
	if err != nil {
		return nil, fmt.Errorf("inherited access for user: %w", err)
	}

	out := make(map[uint]UserClusterAccess, len(direct)+len(inherited))
	for _, g := range direct {
		if existing, ok := out[g.ClusterID]; ok {
			out[g.ClusterID] = MergeAccess(existing, g)
			continue
		}
		out[g.ClusterID] = g
	}
	for _, g := range inherited {
		candidate := UserClusterAccess{
			UserID:     userID,
			ClusterID:  g.ClusterID,
			K8sRole:    g.K8sRole,
			Namespaces: g.Namespaces,
		}
		if existing, ok := out[g.ClusterID]; ok {
			out[g.ClusterID] = MergeAccess(existing, candidate)
			continue
		}
		out[g.ClusterID] = candidate
	}
	return out, nil
}

// ClusterByID loads a single cluster.
func (s *Store) ClusterByID(ctx context.Context, id uint) (*Cluster, error) {
	var cluster Cluster
	err := s.gdb.WithContext(ctx).First(&cluster, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("cluster by id: %w", err)
	}
	return &cluster, nil
}

// ClusterByAgentToken resolves the cluster an agent is presenting credentials
// for. An empty token never matches, so a cluster registered in direct mode
// (which carries no agent token) cannot be claimed by a tunnel.
func (s *Store) ClusterByAgentToken(ctx context.Context, token string) (*Cluster, error) {
	if token == "" {
		return nil, ErrNotFound
	}

	var cluster Cluster
	err := s.gdb.WithContext(ctx).Where("agent_token = ?", token).First(&cluster).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("cluster by agent token: %w", err)
	}
	return &cluster, nil
}

// CreateCluster registers a new target cluster.
func (s *Store) CreateCluster(ctx context.Context, cluster *Cluster) error {
	if err := s.gdb.WithContext(ctx).Create(cluster).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return ErrConflict
		}
		return fmt.Errorf("create cluster: %w", err)
	}
	return nil
}

// ClusterHealth is the outcome of a reachability check, ready to persist.
type ClusterHealth struct {
	Status            string
	StatusMessage     string
	KubernetesVersion string
	CheckedAt         time.Time
}

// UpdateClusterHealth records the result of a reachability check.
func (s *Store) UpdateClusterHealth(ctx context.Context, id uint, health ClusterHealth) error {
	res := s.gdb.WithContext(ctx).Model(&Cluster{}).Where("id = ?", id).Updates(map[string]any{
		"status":             health.Status,
		"status_message":     health.StatusMessage,
		"kubernetes_version": health.KubernetesVersion,
		"last_checked_at":    health.CheckedAt,
	})
	if res.Error != nil {
		return fmt.Errorf("update cluster health: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// AgentState is an agent tunnel's lifecycle transition, ready to persist. It
// doubles as the cluster's health for agent-mode clusters: a cluster whose
// agent is attached is by definition reachable.
type AgentState struct {
	Connected bool
	// AgentVersion and KubernetesVersion come from the agent's handshake and
	// are left untouched on disconnect.
	AgentVersion      string
	KubernetesVersion string
	// StatusMessage explains a dropped tunnel.
	StatusMessage string
	At            time.Time
}

// UpdateClusterAgent records an agent connecting or dropping off. Both edges
// move the cluster's health, which is what makes the registration wizard's
// pending -> healthy transition happen without anyone pressing "check".
func (s *Store) UpdateClusterAgent(ctx context.Context, id uint, state AgentState) error {
	fields := map[string]any{
		"last_checked_at": state.At,
		"status_message":  state.StatusMessage,
	}
	if state.Connected {
		fields["status"] = StatusHealthy
		fields["status_message"] = ""
		fields["agent_connected_at"] = state.At
		if state.AgentVersion != "" {
			fields["agent_version"] = state.AgentVersion
		}
		if state.KubernetesVersion != "" {
			fields["kubernetes_version"] = state.KubernetesVersion
		}
	} else {
		fields["status"] = StatusUnhealthy
		// A cluster with no agent attached has no tunnel, so null the timestamp
		// rather than leaving a stale "connected since".
		fields["agent_connected_at"] = nil
	}

	res := s.gdb.WithContext(ctx).Model(&Cluster{}).Where("id = ?", id).Updates(fields)
	if res.Error != nil {
		return fmt.Errorf("update cluster agent: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteCluster removes a cluster registration along with its access grants.
func (s *Store) DeleteCluster(ctx context.Context, id uint) error {
	return s.gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Delete(&Cluster{}, id)
		if res.Error != nil {
			return fmt.Errorf("delete cluster: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			return ErrNotFound
		}
		if err := tx.Where("cluster_id = ?", id).Delete(&UserClusterAccess{}).Error; err != nil {
			return fmt.Errorf("delete cluster access: %w", err)
		}
		if err := tx.Where("cluster_id = ?", id).Delete(&GroupClusterAccess{}).Error; err != nil {
			return fmt.Errorf("delete group cluster access: %w", err)
		}
		// A datasource describes one cluster and outlives nothing; leaving it
		// behind would strand a stored credential with no owner.
		if err := tx.Where("cluster_id = ?", id).Delete(&ObservabilitySource{}).Error; err != nil {
			return fmt.Errorf("delete observability sources: %w", err)
		}
		if err := deleteClusterConsoles(tx, id); err != nil {
			return err
		}
		// Access requests go with the cluster they are about. The audit trail keeps
		// the record of who approved what; what must not survive is a row still
		// counting down an elevation on a cluster nobody can reach.
		if err := tx.Where("cluster_id = ?", id).Delete(&JitRequest{}).Error; err != nil {
			return fmt.Errorf("delete jit requests: %w", err)
		}
		return nil
	})
}
