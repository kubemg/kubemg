package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ListUsers returns every local account, ordered by username.
func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	users := []User{}
	if err := s.gdb.WithContext(ctx).Order("username asc").Find(&users).Error; err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	return users, nil
}

// UserUpdate carries the mutable fields of an account. Nil fields are left
// untouched, which is what distinguishes "clear the email" from "do not change
// the email".
type UserUpdate struct {
	Username     *string
	Email        *string
	SystemRole   *string
	PasswordHash *string
}

// UpdateUser applies a partial update and returns the stored record.
func (s *Store) UpdateUser(ctx context.Context, id uint, update UserUpdate) (*User, error) {
	fields := map[string]any{}
	if update.Username != nil {
		fields["username"] = *update.Username
	}
	if update.Email != nil {
		fields["email"] = *update.Email
	}
	if update.SystemRole != nil {
		fields["system_role"] = *update.SystemRole
		// Keep the coarse role the JWT carries in step with the system role.
		fields["role"] = LegacyRoleFor(*update.SystemRole)
	}
	if update.PasswordHash != nil {
		fields["password_hash"] = *update.PasswordHash
	}
	if len(fields) == 0 {
		return s.UserByID(ctx, id)
	}
	fields["updated_at"] = time.Now().UTC()

	res := s.gdb.WithContext(ctx).Model(&User{}).Where("id = ?", id).Updates(fields)
	if errors.Is(res.Error, gorm.ErrDuplicatedKey) {
		return nil, ErrConflict
	}
	if res.Error != nil {
		return nil, fmt.Errorf("update user: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return nil, ErrNotFound
	}
	return s.UserByID(ctx, id)
}

// SetUserActive enables or disables sign-in for an account without touching its
// grants, so a suspension can be reversed.
func (s *Store) SetUserActive(ctx context.Context, id uint, active bool) (*User, error) {
	res := s.gdb.WithContext(ctx).Model(&User{}).Where("id = ?", id).Updates(map[string]any{
		"is_active":  active,
		"updated_at": time.Now().UTC(),
	})
	if res.Error != nil {
		return nil, fmt.Errorf("set user active: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return nil, ErrNotFound
	}
	return s.UserByID(ctx, id)
}

// DeleteUser removes an account along with its grants and group memberships.
func (s *Store) DeleteUser(ctx context.Context, id uint) error {
	return s.gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Delete(&User{}, id)
		if res.Error != nil {
			return fmt.Errorf("delete user: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			return ErrNotFound
		}
		if err := tx.Where("user_id = ?", id).Delete(&UserClusterAccess{}).Error; err != nil {
			return fmt.Errorf("delete user cluster access: %w", err)
		}
		if err := tx.Where("user_id = ?", id).Delete(&UserGroup{}).Error; err != nil {
			return fmt.Errorf("delete user group memberships: %w", err)
		}
		return nil
	})
}

// TouchLastLogin records a successful sign-in.
func (s *Store) TouchLastLogin(ctx context.Context, id uint, at time.Time) error {
	err := s.gdb.WithContext(ctx).Model(&User{}).Where("id = ?", id).
		Update("last_login_at", at).Error
	if err != nil {
		return fmt.Errorf("touch last login: %w", err)
	}
	return nil
}

// GroupSummary is a group plus the members it currently holds.
type GroupSummary struct {
	Group
	MemberIDs []uint `json:"member_ids"`
}

// ListGroups returns every local group with its membership.
func (s *Store) ListGroups(ctx context.Context) ([]GroupSummary, error) {
	groups := []Group{}
	if err := s.gdb.WithContext(ctx).Order("name asc").Find(&groups).Error; err != nil {
		return nil, fmt.Errorf("list groups: %w", err)
	}

	memberships := []UserGroup{}
	if err := s.gdb.WithContext(ctx).Find(&memberships).Error; err != nil {
		return nil, fmt.Errorf("list group memberships: %w", err)
	}
	byGroup := map[uint][]uint{}
	for _, m := range memberships {
		byGroup[m.GroupID] = append(byGroup[m.GroupID], m.UserID)
	}

	out := make([]GroupSummary, 0, len(groups))
	for _, g := range groups {
		members := byGroup[g.ID]
		if members == nil {
			members = []uint{}
		}
		out = append(out, GroupSummary{Group: g, MemberIDs: members})
	}
	return out, nil
}

// GroupByID loads a single group.
func (s *Store) GroupByID(ctx context.Context, id uint) (*Group, error) {
	var group Group
	err := s.gdb.WithContext(ctx).First(&group, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("group by id: %w", err)
	}
	return &group, nil
}

// CreateGroup inserts a new local group.
func (s *Store) CreateGroup(ctx context.Context, group *Group) error {
	if err := s.gdb.WithContext(ctx).Create(group).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return ErrConflict
		}
		return fmt.Errorf("create group: %w", err)
	}
	return nil
}

// DeleteGroup removes a group along with its memberships and cluster grants.
func (s *Store) DeleteGroup(ctx context.Context, id uint) error {
	return s.gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Delete(&Group{}, id)
		if res.Error != nil {
			return fmt.Errorf("delete group: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			return ErrNotFound
		}
		if err := tx.Where("group_id = ?", id).Delete(&UserGroup{}).Error; err != nil {
			return fmt.Errorf("delete group memberships: %w", err)
		}
		if err := tx.Where("group_id = ?", id).Delete(&GroupClusterAccess{}).Error; err != nil {
			return fmt.Errorf("delete group cluster access: %w", err)
		}
		return nil
	})
}

// AddGroupMember puts a user into a group. Re-adding an existing member is a
// no-op rather than an error.
func (s *Store) AddGroupMember(ctx context.Context, groupID, userID uint) error {
	member := UserGroup{GroupID: groupID, UserID: userID, Source: GrantSourceLocal}
	err := s.gdb.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "user_id"}, {Name: "group_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"source"}),
		}).
		Create(&member).Error
	if err != nil {
		return fmt.Errorf("add group member: %w", err)
	}
	return nil
}

// RemoveGroupMember takes a user out of a group.
func (s *Store) RemoveGroupMember(ctx context.Context, groupID, userID uint) error {
	res := s.gdb.WithContext(ctx).
		Where("group_id = ? AND user_id = ?", groupID, userID).
		Delete(&UserGroup{})
	if res.Error != nil {
		return fmt.Errorf("remove group member: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// ListUserAccess returns every direct user grant.
func (s *Store) ListUserAccess(ctx context.Context) ([]UserClusterAccess, error) {
	grants := []UserClusterAccess{}
	err := s.gdb.WithContext(ctx).Order("user_id asc, cluster_id asc").Find(&grants).Error
	if err != nil {
		return nil, fmt.Errorf("list user access: %w", err)
	}
	return grants, nil
}

// ListGroupAccess returns every group grant.
func (s *Store) ListGroupAccess(ctx context.Context) ([]GroupClusterAccess, error) {
	grants := []GroupClusterAccess{}
	err := s.gdb.WithContext(ctx).Order("group_id asc, cluster_id asc").Find(&grants).Error
	if err != nil {
		return nil, fmt.Errorf("list group access: %w", err)
	}
	return grants, nil
}

// AssignUserAccess grants a user access to a cluster, replacing any existing
// grant for that pair.
// A grant written through this path is an administrator's decision, so it is
// stamped local — which is what takes it out of the federation sync's reach,
// including a row that sync had previously derived.
func (s *Store) AssignUserAccess(ctx context.Context, grant *UserClusterAccess) error {
	if grant.Source == "" {
		grant.Source = GrantSourceLocal
	}
	err := s.gdb.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: "cluster_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"k8s_role", "namespaces", "source"}),
	}).Create(grant).Error
	if err != nil {
		return fmt.Errorf("assign user access: %w", err)
	}
	return nil
}

// AssignGroupAccess grants a group access to a cluster, replacing any existing
// grant for that pair.
func (s *Store) AssignGroupAccess(ctx context.Context, grant *GroupClusterAccess) error {
	err := s.gdb.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "group_id"}, {Name: "cluster_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"k8s_role", "namespaces"}),
	}).Create(grant).Error
	if err != nil {
		return fmt.Errorf("assign group access: %w", err)
	}
	return nil
}

// RevokeUserAccess drops a user's direct grant on a cluster.
func (s *Store) RevokeUserAccess(ctx context.Context, userID, clusterID uint) error {
	res := s.gdb.WithContext(ctx).
		Where("user_id = ? AND cluster_id = ?", userID, clusterID).
		Delete(&UserClusterAccess{})
	if res.Error != nil {
		return fmt.Errorf("revoke user access: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// RevokeGroupAccess drops a group's grant on a cluster.
func (s *Store) RevokeGroupAccess(ctx context.Context, groupID, clusterID uint) error {
	res := s.gdb.WithContext(ctx).
		Where("group_id = ? AND cluster_id = ?", groupID, clusterID).
		Delete(&GroupClusterAccess{})
	if res.Error != nil {
		return fmt.Errorf("revoke group access: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// Clusters returns every registered cluster, for admin views that must show the
// full inventory regardless of grants.
func (s *Store) Clusters(ctx context.Context) ([]Cluster, error) {
	clusters := []Cluster{}
	if err := s.gdb.WithContext(ctx).Order("name asc").Find(&clusters).Error; err != nil {
		return nil, fmt.Errorf("list clusters: %w", err)
	}
	return clusters, nil
}
