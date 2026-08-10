package db

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm/clause"
)

/*
 * Leases: which replica does a piece of unattended work.
 *
 * Almost everything KubeMG does happens because somebody asked for it, so N
 * replicas behind a load balancer share the work by definition. Background work
 * is the exception: a poller started in every process polls N times, and the
 * cost lands on the *target cluster* rather than here — which is the wrong place
 * for it to land, since a fleet operator sizing KubeMG for availability did not
 * ask to triple the read load on their production API servers.
 *
 * So a background job that reads a cluster takes a lease first. The mechanism is
 * one row and one statement:
 *
 *   - it is an **expiring** lease rather than a lock, because a replica that is
 *     killed cannot release anything, and a held lock nobody can release is an
 *     outage that needs a DBA. An expiry means the worst case is one idle window
 *     rather than a job that never runs again.
 *   - it is a **conditional upsert**, not read-then-write. Two replicas ticking
 *     at the same instant is the normal case, not the rare one, and a check
 *     followed by a write would let both pass the check. The condition lives in
 *     the `ON CONFLICT ... DO UPDATE ... WHERE`, so the database decides and
 *     exactly one of them sees a row affected.
 *   - **renewal is the same statement.** The holder matches its own row and
 *     pushes the expiry out; that is why the condition is "expired *or* mine"
 *     rather than only "expired".
 *
 * This is deliberately not a Postgres advisory lock. A session-level advisory
 * lock is held by whichever pooled connection ran it, and GORM hands that
 * connection back to the pool — so holding one for the lifetime of a goroutine
 * means pinning a connection out of the pool and reasoning about which one. A
 * row with an expiry is testable, visible to an operator who wants to know which
 * replica is doing the work, and needs nothing of the connection pool.
 */

// Lease names. One per background job that reads a cluster.
const (
	// LeaseAlarmWatcher covers cluster-event polling. It is the heaviest
	// unattended read in the product — a bounded but cluster-wide event list per
	// covered cluster per interval — which is what makes it the first job that
	// had to stop being multiplied by the replica count.
	LeaseAlarmWatcher = "alarm_watcher"
)

// Lease is one held claim on a background job.
type Lease struct {
	Name string `gorm:"primaryKey;size:64" json:"name"`
	// Holder identifies the process, not the machine: it is minted per server
	// start, so a restarted replica is a different holder and does not renew a
	// lease its previous incarnation took.
	Holder    string    `gorm:"size:64;not null" json:"holder"`
	ExpiresAt time.Time `gorm:"not null" json:"expires_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName pins the table; GORM would otherwise pluralise to `lease`'s own
// English plural, which is the same word it already is.
func (Lease) TableName() string { return "leases" }

// AcquireLease takes or renews the named lease for `holder`, and reports whether
// this caller now holds it.
//
// A false return is not an error: it means somebody else holds a live lease,
// which is the answer the caller asked for. An error is a database that could not
// be reached, and a caller must treat that as "do not run" rather than as "run
// anyway" — two replicas that both fail this call and both proceed is exactly the
// duplication the lease exists to prevent.
func (s *Store) AcquireLease(
	ctx context.Context, name, holder string, ttl time.Duration,
) (bool, error) {
	now := time.Now().UTC()
	row := Lease{Name: name, Holder: holder, ExpiresAt: now.Add(ttl), UpdatedAt: now}

	result := s.gdb.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "name"}},
		DoUpdates: clause.AssignmentColumns([]string{"holder", "expires_at", "updated_at"}),
		// Written as a raw expression rather than assembled from clause helpers
		// because this condition is the whole correctness argument and it should
		// be readable as the SQL that actually runs. `leases.` is the row already
		// stored; the proposed row would be `excluded.`.
		Where: clause.Where{Exprs: []clause.Expression{
			clause.Expr{
				SQL:  "leases.expires_at < ? OR leases.holder = ?",
				Vars: []any{now, holder},
			},
		}},
	}).Create(&row)

	if result.Error != nil {
		return false, fmt.Errorf("acquire lease %s: %w", name, result.Error)
	}
	// An insert that conflicted and whose DO UPDATE condition did not match
	// reports no rows affected, which is precisely "somebody else holds it".
	return result.RowsAffected > 0, nil
}
