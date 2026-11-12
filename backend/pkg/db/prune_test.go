package db

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// agedRows stands in for a table: the ages of its rows, of which a batch
// deletes up to its size from those older than the cutoff.
type agedRows struct {
	ages    []time.Duration
	cutoff  time.Duration
	batches int
}

func (r *agedRows) deleteBatch(size int) func(context.Context) (int64, error) {
	return func(context.Context) (int64, error) {
		r.batches++
		var removed int64
		kept := r.ages[:0]
		for _, age := range r.ages {
			if age > r.cutoff && removed < int64(size) {
				removed++
				continue
			}
			kept = append(kept, age)
		}
		r.ages = kept
		return removed, nil
	}
}

func seedAged(old, recent int) *agedRows {
	rows := &agedRows{cutoff: 30 * 24 * time.Hour}
	for range old {
		rows.ages = append(rows.ages, 400*24*time.Hour)
	}
	for range recent {
		rows.ages = append(rows.ages, time.Hour)
	}
	return rows
}

func TestPruneInBatchesDeletesEverythingPastTheCutoff(t *testing.T) {
	for _, tc := range []struct {
		old, batches int
	}{
		{old: 0, batches: 1},
		{old: 4, batches: 1},
		{old: 12, batches: 3},
		// An exact multiple takes one more batch, the one that finds nothing.
		{old: 10, batches: 3},
	} {
		rows := seedAged(tc.old, 3)
		removed, err := pruneInBatches(context.Background(), 5, 0, rows.deleteBatch(5))
		if err != nil {
			t.Fatal(err)
		}
		if removed != int64(tc.old) || rows.batches != tc.batches {
			t.Fatalf("%d old rows: removed %d in %d batches, want %d in %d",
				tc.old, removed, rows.batches, tc.old, tc.batches)
		}
		if len(rows.ages) != 3 {
			t.Fatalf("%d old rows: %d rows survived, want the 3 inside the window", tc.old, len(rows.ages))
		}
	}
}

func TestPruneInBatchesStopsWhenCancelled(t *testing.T) {
	rows := seedAged(12, 3)
	ctx, cancel := context.WithCancel(context.Background())
	deleteBatch := rows.deleteBatch(5)

	removed, err := pruneInBatches(ctx, 5, time.Hour, func(ctx context.Context) (int64, error) {
		defer cancel()
		return deleteBatch(ctx)
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if removed != 5 || rows.batches != 1 {
		t.Fatalf("removed %d in %d batches, want the first batch's 5 and no more", removed, rows.batches)
	}
}

func TestPruneInBatchesCountsWhatWentBeforeAFailure(t *testing.T) {
	rows := seedAged(12, 3)
	deleteBatch := rows.deleteBatch(5)
	failure := errors.New("connection reset")

	removed, err := pruneInBatches(context.Background(), 5, 0, func(ctx context.Context) (int64, error) {
		if rows.batches == 1 {
			return 0, failure
		}
		return deleteBatch(ctx)
	})
	if !errors.Is(err, failure) || removed != 5 {
		t.Fatalf("removed %d, err %v; want 5 and the failure", removed, err)
	}
}

// dryRunPool lets a dry-run handle open a transaction, which dryRunDB's cannot
// without a server. Nothing is ever sent through it.
type dryRunPool struct{}

var errDryRun = errors.New("dry run: nothing is sent")

func (*dryRunPool) PrepareContext(context.Context, string) (*sql.Stmt, error) { return nil, errDryRun }
func (*dryRunPool) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	return nil, errDryRun
}
func (*dryRunPool) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, errDryRun
}
func (*dryRunPool) QueryRowContext(context.Context, string, ...any) *sql.Row { return nil }
func (*dryRunPool) BeginTx(context.Context, *sql.TxOptions) (gorm.ConnPool, error) {
	return &dryRunPool{}, nil
}
func (*dryRunPool) Commit() error   { return nil }
func (*dryRunPool) Rollback() error { return nil }

// TestPrunesDeleteOldestFirstInBatches reads the statements every retention
// prune sends, so none goes back to one unbounded DELETE.
func TestPrunesDeleteOldestFirstInBatches(t *testing.T) {
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: &dryRunPool{}}),
		&gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	var statements []string
	record := func(tx *gorm.DB) {
		statements = append(statements, tx.Dialector.Explain(tx.Statement.SQL.String(), tx.Statement.Vars...))
	}
	if err := gdb.Callback().Delete().After("gorm:delete").Register("test:record", record); err != nil {
		t.Fatal(err)
	}
	if err := gdb.Callback().Query().After("gorm:query").Register("test:record", record); err != nil {
		t.Fatal(err)
	}
	store := NewStore(gdb)
	ctx := context.Background()
	before := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	for _, tc := range []struct {
		prune func() error
		want  string
	}{
		{
			prune: func() error { _, err := store.PruneAuditEvents(ctx, before); return err },
			want: `DELETE FROM "audit_events" WHERE id IN ` +
				`(SELECT "id" FROM "audit_events" WHERE at < '2026-01-02 03:04:05' ORDER BY at LIMIT 5000)`,
		},
		{
			prune: func() error { _, err := store.PruneJitRequests(ctx, before); return err },
			want: `DELETE FROM "jit_requests" WHERE id IN ` +
				`(SELECT "id" FROM "jit_requests" WHERE status NOT IN ('approved','active') AND updated_at < '2026-01-02 03:04:05' ` +
				`ORDER BY updated_at LIMIT 5000)`,
		},
		{
			prune: func() error { _, err := store.PruneCLISessions(ctx, before); return err },
			want: `DELETE FROM "cli_sessions" WHERE id IN ` +
				`(SELECT "id" FROM "cli_sessions" WHERE expires_at < '2026-01-02 03:04:05' ORDER BY expires_at LIMIT 5000)`,
		},
		{
			prune: func() error { _, err := store.PruneKubeconfigIssuances(ctx, before); return err },
			want: `DELETE FROM "kubeconfig_issuances" WHERE id IN ` +
				`(SELECT "id" FROM "kubeconfig_issuances" WHERE created_at < '2026-01-02 03:04:05' ORDER BY created_at LIMIT 5000)`,
		},
		{
			prune: func() error { _, err := store.PruneTerminalSessions(ctx, before); return err },
			want:  `SELECT * FROM "terminal_sessions" WHERE started_at < '2026-01-02 03:04:05' ORDER BY started_at LIMIT 5000`,
		},
	} {
		statements = nil
		if err := tc.prune(); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(strings.Join(statements, "\n"), tc.want) {
			t.Fatalf("statements sent:\n%s\nwant one to read:\n%s", strings.Join(statements, "\n"), tc.want)
		}
	}
}
