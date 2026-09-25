package db

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// AgentInstallTicket is a minted, not yet fetched agent install download — the
// credential in an install URL.
//
// The install URL used to carry the cluster's tunnel credential itself, so a
// URL that landed in shell history, a CI log or a chat message was a working
// agent credential for the life of the installation. A ticket is not: it is
// redeemed by the first download, dies unused after a few minutes, and the
// package it returns is what carries the tunnel credential. It is a row, like a
// WebSocket ticket, because the console that mints it and the kubectl that
// fetches it need not reach the same replica; only its SHA-256 is kept.
type AgentInstallTicket struct {
	TokenHash string    `gorm:"primaryKey;size:64"`
	ClusterID uint      `gorm:"index;not null"`
	ExpiresAt time.Time `gorm:"index;not null"`
}

// TableName pins the table name.
func (AgentInstallTicket) TableName() string { return "agent_install_tickets" }

// PutAgentInstallTicket files a download ticket for a cluster until expiresAt.
// Expired rows are cleared on the way in, the ws_tickets rule: an URL that was
// shown and never used is otherwise never deleted.
func (s *Store) PutAgentInstallTicket(ctx context.Context, hash string, clusterID uint, expiresAt time.Time) error {
	gdb := s.gdb.WithContext(ctx)
	if err := gdb.Where("expires_at < ?", time.Now().UTC()).Delete(&AgentInstallTicket{}).Error; err != nil {
		return fmt.Errorf("prune agent install tickets: %w", err)
	}
	row := AgentInstallTicket{TokenHash: hash, ClusterID: clusterID, ExpiresAt: expiresAt.UTC()}
	if err := gdb.Create(&row).Error; err != nil {
		return fmt.Errorf("store agent install ticket: %w", err)
	}
	return nil
}

// TakeAgentInstallTicket deletes a ticket and returns the cluster it was minted
// for. One `DELETE … RETURNING` with the expiry in its condition, so of any
// number of concurrent fetches of the same URL, on any replica, exactly one
// gets the package.
func (s *Store) TakeAgentInstallTicket(ctx context.Context, hash string) (uint, bool, error) {
	var rows []AgentInstallTicket
	result := s.gdb.WithContext(ctx).
		Clauses(clause.Returning{}).
		Where("token_hash = ? AND expires_at > ?", hash, time.Now().UTC()).
		Delete(&rows)
	if result.Error != nil {
		return 0, false, fmt.Errorf("redeem agent install ticket: %w", result.Error)
	}
	if result.RowsAffected == 0 || len(rows) == 0 {
		return 0, false, nil
	}
	return rows[0].ClusterID, true, nil
}

// RotateClusterAgentToken replaces a cluster's tunnel credential and, in the
// same transaction, withdraws every download ticket still outstanding for it.
//
// The second half matters because a ticket renders the cluster's *current*
// token when it is fetched: a URL minted before the rotation and leaked would
// otherwise hand out the new credential.
func (s *Store) RotateClusterAgentToken(ctx context.Context, clusterID uint, token string) error {
	return s.gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&Cluster{}).
			Where("id = ? AND connection_mode = ?", clusterID, ModeAgent).
			Updates(map[string]any{"agent_token": token, "agent_token_hash": HashAgentToken(token)})
		if res.Error != nil {
			return fmt.Errorf("rotate agent token: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			return ErrNotFound
		}
		if err := tx.Where("cluster_id = ?", clusterID).Delete(&AgentInstallTicket{}).Error; err != nil {
			return fmt.Errorf("withdraw agent install tickets: %w", err)
		}
		return nil
	})
}
