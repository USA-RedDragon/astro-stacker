// Package schedwatch notices changes in the scheduler database, which
// SymmetricDS fills from the observatory, so work that reads it runs as
// soon as a change arrives instead of on a timer.
package schedwatch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/tables"
	"gorm.io/gorm"
)

// Every is how often the database is checked.
const Every = 30 * time.Second

// SyncStampQuery reads the newest batches SymmetricDS exchanged with the
// observatory, heartbeats aside.
const SyncStampQuery = `SELECT
	(SELECT COALESCE(MAX(batch_id), 0) FROM sym_incoming_batch WHERE channel_id <> 'heartbeat' AND status = 'OK') AS incoming,
	(SELECT COALESCE(MAX(batch_id), 0) FROM sym_outgoing_batch WHERE channel_id <> 'heartbeat' AND status = 'OK') AS outgoing`

// planTable is a table the plans key covers, less columns that change with
// every sub.
type planTable struct {
	name   string
	ignore []string
}

// planTables are what the projects, targets, exposure plans, templates,
// rule weights and goals are made of.
func planTables() []planTable {
	return []planTable{
		{name: "project"},
		{name: "target"},
		{name: "exposureplan", ignore: []string{"acquired", "accepted"}},
		{name: "exposuretemplate"},
		{name: "ruleweight"},
		{name: "ts_goal"},
	}
}

// PlansKey identifies the current projects, targets, exposure plans,
// templates, rule weights and goals, so any change to them shows as a new
// key. Acquired and accepted counts are left out.
func PlansKey(ctx context.Context, db *gorm.DB) (string, error) {
	h := sha256.New()
	for _, t := range planTables() {
		has, err := tables.Has(ctx, db, t.name)
		if err != nil {
			return "", err
		}
		if !has {
			continue
		}
		var rows []map[string]any
		if err := db.WithContext(ctx).Table(t.name).Find(&rows).Error; err != nil {
			return "", fmt.Errorf("read %s: %w", t.name, err)
		}
		lines := make([]string, 0, len(rows))
		for _, r := range rows {
			var b strings.Builder
			for _, k := range slices.Sorted(maps.Keys(r)) {
				if slices.Contains(t.ignore, k) {
					continue
				}
				fmt.Fprintf(&b, "%s=%v\x1f", k, r[k])
			}
			lines = append(lines, b.String())
		}
		slices.Sort(lines)
		fmt.Fprintf(h, "%s\x1e%s\x1d", t.name, strings.Join(lines, "\x1e"))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// IncomingBatch is the newest batch SymmetricDS took in from the
// observatory, false when it can't be read, as without SymmetricDS.
func IncomingBatch(ctx context.Context, db *gorm.DB) (int64, bool) {
	var row struct {
		Incoming int64
		Outgoing int64
	}
	if err := db.WithContext(ctx).Raw(SyncStampQuery).Scan(&row).Error; err != nil {
		return 0, false
	}
	return row.Incoming, true
}

// Watcher checks the scheduler database every Every and calls OnPlans when
// the plans key changes and OnSync when SymmetricDS takes in a new batch.
// The first check only notes where things stand.
type Watcher struct {
	DB      *gorm.DB
	Every   time.Duration
	OnPlans []func()
	OnSync  []func()

	plans string
	batch int64
	ready bool
}

// Check compares the database with the last check and calls the listeners
// of what changed.
func (w *Watcher) Check(ctx context.Context) {
	plans, err := PlansKey(ctx, w.DB)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("Checking the scheduler database for plan changes failed", "error", err)
		}
		return
	}
	batch, ok := IncomingBatch(ctx, w.DB)
	if !ok {
		batch = w.batch
	}
	ready := w.ready
	plansChanged, synced := plans != w.plans, batch > w.batch
	w.plans, w.batch, w.ready = plans, batch, true
	if !ready {
		return
	}
	if plansChanged {
		slog.Debug("Scheduler database plans changed")
		for _, fn := range w.OnPlans {
			fn()
		}
	}
	if synced {
		for _, fn := range w.OnSync {
			fn()
		}
	}
}

// Run checks every Every until ctx ends. A nil DB watches nothing.
func (w *Watcher) Run(ctx context.Context) {
	if w.DB == nil {
		return
	}
	every := w.Every
	if every <= 0 {
		every = Every
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		w.Check(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
