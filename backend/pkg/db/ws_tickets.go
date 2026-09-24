package db

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm/clause"
)

// WSTicket is a minted, not yet redeemed WebSocket ticket — see
// auth.Manager.IssueWSTicket.
//
// It is a row rather than process memory because a ticket is minted by one
// request and redeemed by the next, and with more than one replica behind a
// load balancer those two requests need not reach the same process. Only the
// ticket's SHA-256 is kept: the ticket is 256 bits of CSPRNG output, so there
// is nothing to guess, and a row read out of a backup is not a credential.
type WSTicket struct {
	TokenHash string `gorm:"primaryKey;size:64"`
	// Claims is the verified session the ticket stands in for, encoded by
	// pkg/auth. The store never interprets it.
	Claims    []byte    `gorm:"not null"`
	ExpiresAt time.Time `gorm:"index;not null"`
}

// TableName pins the table name.
func (WSTicket) TableName() string { return "ws_tickets" }

// PutWSTicket files a ticket until expiresAt.
//
// Expired rows are cleared on the way in. A ticket that was minted and never
// used — a tab closed between the two requests — is otherwise never deleted,
// and a terminal being opened is rare enough that one indexed delete in front
// of it costs nothing a sweeper would save.
func (s *Store) PutWSTicket(ctx context.Context, hash string, payload []byte, expiresAt time.Time) error {
	gdb := s.gdb.WithContext(ctx)
	if err := gdb.Where("expires_at < ?", time.Now().UTC()).Delete(&WSTicket{}).Error; err != nil {
		return fmt.Errorf("prune websocket tickets: %w", err)
	}
	row := WSTicket{TokenHash: hash, Claims: payload, ExpiresAt: expiresAt.UTC()}
	if err := gdb.Create(&row).Error; err != nil {
		return fmt.Errorf("store websocket ticket: %w", err)
	}
	return nil
}

// TakeWSTicket deletes a ticket and returns what it held.
//
// Delete-and-return is one statement, not a read followed by a delete: two
// replicas presented the same ticket at the same instant both pass a read, and
// only the database can decide that exactly one of them deleted the row. The
// expiry is part of the same condition, so an expired ticket is refused (and
// left for the next PutWSTicket to clear).
func (s *Store) TakeWSTicket(ctx context.Context, hash string) ([]byte, bool, error) {
	var rows []WSTicket
	result := s.gdb.WithContext(ctx).
		Clauses(clause.Returning{}).
		Where("token_hash = ? AND expires_at > ?", hash, time.Now().UTC()).
		Delete(&rows)
	if result.Error != nil {
		return nil, false, fmt.Errorf("redeem websocket ticket: %w", result.Error)
	}
	if result.RowsAffected == 0 || len(rows) == 0 {
		return nil, false, nil
	}
	return rows[0].Claims, true, nil
}
