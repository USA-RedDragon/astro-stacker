package vetting

import (
	"context"
	"errors"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
)

// lockKey names the PostgreSQL advisory lock a vetting run holds.
const lockKey int64 = 0x76657474696e67

// ErrBusy says another vetting run holds the lock.
var ErrBusy = errors.New("another vetting run is going")

// stoppedNote is the error a source keeps when the run that held it
// stopped part way, and stoppedAgainNote when the retry stopped too.
const (
	stoppedNote      = "The last run stopped part way, out of memory or killed; it is tried again."
	stoppedAgainNote = "The run stopped part way twice, out of memory or killed; it waits before it is tried again."
)

// Lock takes the lock that lets one vetting run go at a time, on its own
// database connection, and gives back its release. A run that dies drops
// its connection and with it the lock. Databases other than PostgreSQL
// have no lock to take.
func Lock(ctx context.Context, db *gorm.DB) (func(), error) {
	if db.Name() != "postgres" {
		return func() {}, nil
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return nil, err
	}
	var ok bool
	if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", lockKey).Scan(&ok); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if !ok {
		_ = conn.Close()
		return nil, ErrBusy
	}
	return func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_, _ = conn.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", lockKey)
		_ = conn.Close()
	}, nil
}

// requeueStale queues again every source left running. Only the lock
// holder runs sources, so a running one belongs to a run that stopped
// part way. A source whose retry stopped too is marked failed, to wait
// out the failed retry delay instead of stopping every run. It gives
// back the ids queued again.
func requeueStale(ctx context.Context, db *gorm.DB, now time.Time) (map[int]bool, error) {
	var rows []app.DiscoveryVet
	if err := db.WithContext(ctx).Select("source_id", "error").Where(colState+" = ?", StateRunning).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[int]bool, len(rows))
	var again, twice []int
	for _, r := range rows {
		if r.Error == stoppedNote {
			twice = append(twice, r.SourceID)
			continue
		}
		again = append(again, r.SourceID)
		out[r.SourceID] = true
	}
	q := db.WithContext(ctx).Model(&app.DiscoveryVet{})
	if len(again) > 0 {
		err := q.Where("source_id IN ? AND "+colState+" = ?", again, StateRunning).
			Updates(map[string]any{colState: StateQueued, "requested_at": now, "error": stoppedNote, colUpdatedAt: now}).Error
		if err != nil {
			return nil, err
		}
	}
	if len(twice) > 0 {
		err := db.WithContext(ctx).Model(&app.DiscoveryVet{}).Where("source_id IN ? AND "+colState+" = ?", twice, StateRunning).
			Updates(map[string]any{colState: StateFailed, "error": stoppedAgainNote, "finished_at": now, colUpdatedAt: now}).Error
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// release puts the sources a run marked running but did not start back
// in the queue, not started, so those asked for by hand keep their place.
func release(ctx context.Context, db *gorm.DB, ids []int, now time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	return db.WithContext(ctx).Model(&app.DiscoveryVet{}).Where("source_id IN ? AND "+colState+" = ?", ids, StateRunning).
		Updates(map[string]any{colState: StateQueued, "started_at": nil, colUpdatedAt: now}).Error
}
