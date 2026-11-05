package db

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// pruneBatchSize is how many rows one retention statement deletes. After
// retention is shortened, after a long outage, or on the first pass over a big
// trail, a prune can have millions of rows to drop. In one statement that is
// one transaction holding every row lock, one WAL spike and one vacuum debt
// across the table's indexes, on a table the proxy writes to on every call.
const pruneBatchSize = 5000

// prunePause is the gap between batches, so the proxy's own writes and
// autovacuum get a turn while a long prune runs.
const prunePause = 100 * time.Millisecond

// pruneByAge deletes the rows of model that match where, oldest by column
// first, a batch at a time, and returns how many went.
func (s *Store) pruneByAge(ctx context.Context, model any, column, where string, args ...any) (int64, error) {
	return pruneInBatches(ctx, pruneBatchSize, prunePause, func(ctx context.Context) (int64, error) {
		res := deleteOldest(s.gdb.WithContext(ctx), model, column, pruneBatchSize, where, args...)
		return res.RowsAffected, res.Error
	})
}

// deleteOldest deletes up to limit rows of model that match where, oldest by
// column first.
func deleteOldest(gdb *gorm.DB, model any, column string, limit int, where string, args ...any) *gorm.DB {
	oldest := gdb.Model(model).Select("id").Where(where, args...).Order(column).Limit(limit)
	return gdb.Where("id IN (?)", oldest).Delete(model)
}

// pruneInBatches runs deleteBatch until a batch removes fewer than size rows,
// pausing between batches, and returns how many rows went in all. Cancelling
// ctx stops it between batches: what was deleted by then stays deleted and is
// counted, alongside ctx's error.
func pruneInBatches(
	ctx context.Context, size int, pause time.Duration, deleteBatch func(context.Context) (int64, error),
) (int64, error) {
	var total int64
	for {
		removed, err := deleteBatch(ctx)
		total += removed
		if err != nil {
			return total, err
		}
		if removed < int64(size) {
			return total, nil
		}
		select {
		case <-ctx.Done():
			return total, ctx.Err()
		case <-time.After(pause):
		}
	}
}
