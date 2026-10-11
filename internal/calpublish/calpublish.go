// Package calpublish writes the bias backlog into ts_bias_need and the open
// flat requests into ts_flat_request in the scheduler database, which
// SymmetricDS carries to the observatory.
package calpublish

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/calreq"
	"github.com/USA-RedDragon/astro-stacker/internal/coverage"
	"github.com/USA-RedDragon/astro-stacker/internal/darkneed"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/store/tables"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	BiasTable = "ts_bias_need"
	FlatTable = "ts_flat_request"
)

// Status is a publisher's last run, shaped like the dark publisher's.
type Status = darkneed.Status

type BiasRow struct {
	ComboKey        string     `gorm:"column:combo_key"`
	Priority        int        `gorm:"column:priority"`
	Gain            float64    `gorm:"column:gain"`
	CameraOffset    float64    `gorm:"column:camera_offset"`
	Binning         float64    `gorm:"column:binning"`
	ReadoutMode     *string    `gorm:"column:readout_mode"`
	FramesNeeded    int        `gorm:"column:frames_needed"`
	FramesHave      int        `gorm:"column:frames_have"`
	NewestBiasAt    *time.Time `gorm:"column:newest_bias_at"`
	SessionGapHours float64    `gorm:"column:session_gap_hours"`
	Basis           string     `gorm:"column:basis"`
	ComputedAt      time.Time  `gorm:"column:computed_at"`
}

func BiasRows(b coverage.BiasBacklog) []BiasRow {
	rows := make([]BiasRow, 0, len(b.Combos))
	for _, c := range b.Combos {
		r := BiasRow{
			ComboKey: c.ComboKey, Priority: c.Priority, Gain: c.Gain, CameraOffset: c.Offset, Binning: c.Binning,
			ReadoutMode: c.ReadoutMode, FramesNeeded: c.FramesNeeded, FramesHave: c.FramesHave,
			SessionGapHours: b.SessionGapHours, Basis: c.Basis, ComputedAt: b.ComputedAt,
		}
		if c.NewestBiasAt != nil {
			t := c.NewestBiasAt.UTC()
			r.NewestBiasAt = &t
		}
		rows = append(rows, r)
	}
	return rows
}

func timeEqual(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Truncate(time.Second).Equal(b.Truncate(time.Second))
}

func strEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func (r BiasRow) key() string { return r.ComboKey }

func (r BiasRow) same(o BiasRow) bool {
	return r.Priority == o.Priority && r.Gain == o.Gain && r.CameraOffset == o.CameraOffset && r.Binning == o.Binning &&
		strEqual(r.ReadoutMode, o.ReadoutMode) && r.FramesNeeded == o.FramesNeeded && r.FramesHave == o.FramesHave &&
		timeEqual(r.NewestBiasAt, o.NewestBiasAt) && r.SessionGapHours == o.SessionGapHours && r.Basis == o.Basis
}

type FlatRow struct {
	RequestID   string    `gorm:"column:request_id"`
	TargetID    int       `gorm:"column:target_id"`
	TargetName  string    `gorm:"column:target_name"`
	Night       string    `gorm:"column:night"`
	Filters     *string   `gorm:"column:filters"`
	RequestedAt time.Time `gorm:"column:requested_at"`
	ComputedAt  time.Time `gorm:"column:computed_at"`
}

func (r FlatRow) key() string { return r.RequestID }

func (r FlatRow) same(o FlatRow) bool {
	return r.TargetID == o.TargetID && r.TargetName == o.TargetName && r.Night == o.Night && strEqual(r.Filters, o.Filters) &&
		timeEqual(&r.RequestedAt, &o.RequestedAt)
}

// FlatRows are the rows of the open flat requests.
func FlatRows(ctx context.Context, db *gorm.DB, now time.Time) ([]FlatRow, error) {
	open, err := calreq.OpenRequests(ctx, db)
	if err != nil {
		return nil, err
	}
	var flats []app.CalibrationRequest
	for _, r := range open {
		if r.Type == app.CalibrationFlats && r.TargetID != nil {
			flats = append(flats, r)
		}
	}
	prog, err := coverage.MeasureRequests(ctx, db, flats)
	if err != nil {
		return nil, err
	}
	rows := make([]FlatRow, 0, len(flats))
	for i, r := range flats {
		row := FlatRow{RequestID: r.ID, TargetID: *r.TargetID, TargetName: r.Object, Night: r.Night.Format(time.DateOnly),
			RequestedAt: r.CreatedAt.UTC(), ComputedAt: now.UTC()}
		if f := prog[i].Filters(); len(f) > 0 {
			s := strings.Join(f, ",")
			row.Filters = &s
		}
		rows = append(rows, row)
	}
	return rows, nil
}

type row[R any] interface {
	key() string
	same(R) bool
}

// table is what a publisher needs to know of its table.
type table[R row[R]] struct {
	name     string
	keyCol   string
	columns  []string
	noun     string
	want     func(ctx context.Context, app *gorm.DB, now time.Time) ([]R, error)
	describe func(R) []any
}

type publisher[R row[R]] struct {
	App   *gorm.DB
	Sched *gorm.DB
	Mode  string
	Now   func() time.Time

	t      table[R]
	mu     sync.Mutex
	status Status
	noted  string
}

func (p *publisher[R]) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *publisher[R]) Status() Status {
	if p == nil {
		return Status{Mode: darkneed.PublishOff, Skipped: darkneed.SkipOff}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.status
	if s.Mode == "" {
		s.Mode = p.Mode
	}
	if s.Mode != darkneed.PublishOn && s.Mode != darkneed.PublishDryRun && s.Skipped == "" {
		s.Skipped = darkneed.SkipOff
	}
	return s
}

func (p *publisher[R]) finish(s Status) Status {
	s.Mode = p.Mode
	p.mu.Lock()
	defer p.mu.Unlock()
	if s.PublishedAt == nil {
		s.PublishedAt = p.status.PublishedAt
	}
	p.status = s
	if s.Skipped != "" && s.Skipped != darkneed.SkipUnchanged && s.Skipped != p.noted {
		slog.Warn("Calibration need is not published to the observatory", "table", p.t.name, "reason", s.Skipped, "mode", p.Mode)
	}
	p.noted = s.Skipped
	return s
}

func (p *publisher[R]) fail(err error) (Status, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.status.Mode = p.Mode
	p.status.Error = err.Error()
	return p.status, err
}

// Publish upserts the rows that changed and deletes those no longer
// wanted. It skips when off, without a scheduler database, or when the
// table is missing.
func (p *publisher[R]) Publish(ctx context.Context) (Status, error) {
	if p.Mode != darkneed.PublishOn && p.Mode != darkneed.PublishDryRun {
		return p.finish(Status{Skipped: darkneed.SkipOff}), nil
	}
	if p.Sched == nil {
		return p.finish(Status{Skipped: darkneed.SkipNoScheduler}), nil
	}
	has, err := tables.Has(ctx, p.Sched, p.t.name)
	if err != nil {
		return p.fail(fmt.Errorf("check %s: %w", p.t.name, err))
	}
	if !has {
		return p.finish(Status{Skipped: "no " + p.t.name + " table"}), nil
	}
	want, err := p.t.want(ctx, p.App, p.now())
	if err != nil {
		return p.fail(err)
	}
	var have []R
	if err := p.Sched.WithContext(ctx).Table(p.t.name).Find(&have).Error; err != nil {
		return p.fail(fmt.Errorf("load %s: %w", p.t.name, err))
	}
	old := make(map[string]R, len(have))
	for _, r := range have {
		old[r.key()] = r
	}
	s := Status{Rows: len(want)}
	keep := make(map[string]bool, len(want))
	for _, r := range want {
		keep[r.key()] = true
		if o, ok := old[r.key()]; ok && o.same(r) {
			continue
		}
		s.Changed++
		if p.Mode == darkneed.PublishDryRun {
			slog.Info("Would publish a "+p.t.noun+" (dry run)", p.t.describe(r)...)
			continue
		}
		if err := p.Sched.WithContext(ctx).Table(p.t.name).Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: p.t.keyCol}},
			DoUpdates: clause.AssignmentColumns(p.t.columns),
		}).Create(&r).Error; err != nil {
			return p.fail(fmt.Errorf("publish %s %s: %w", p.t.noun, r.key(), err))
		}
	}
	for k := range old {
		if keep[k] {
			continue
		}
		s.Deleted++
		if p.Mode == darkneed.PublishDryRun {
			slog.Info("Would remove a "+p.t.noun+" (dry run)", p.t.keyCol, k)
			continue
		}
		if err := p.Sched.WithContext(ctx).Table(p.t.name).Where(p.t.keyCol+" = ?", k).Delete(nil).Error; err != nil {
			return p.fail(fmt.Errorf("remove %s %s: %w", p.t.noun, k, err))
		}
	}
	if s.Changed == 0 && s.Deleted == 0 {
		s.Skipped = darkneed.SkipUnchanged
		return p.finish(s), nil
	}
	if p.Mode == darkneed.PublishOn {
		at := p.now().UTC()
		s.PublishedAt = &at
		slog.Info("Published "+p.t.name, "rows", s.Rows, "changed", s.Changed, "deleted", s.Deleted)
	}
	return p.finish(s), nil
}

func (p *publisher[R]) Run(ctx context.Context) {
	if _, err := p.Publish(ctx); err != nil && ctx.Err() == nil {
		slog.Error("Publishing failed", "table", p.t.name, "error", err)
	}
}

type BiasPublisher = publisher[BiasRow]

type FlatPublisher = publisher[FlatRow]

// NewBias publishes the bias backlog into ts_bias_need.
func NewBias(appDB, sched *gorm.DB, mode string) *BiasPublisher {
	return &BiasPublisher{App: appDB, Sched: sched, Mode: mode, t: table[BiasRow]{
		name: BiasTable, keyCol: "combo_key", noun: "bias need",
		columns: []string{"priority", "gain", "camera_offset", "binning", "readout_mode", "frames_needed", "frames_have",
			"newest_bias_at", "session_gap_hours", "basis", "computed_at"},
		want: func(ctx context.Context, db *gorm.DB, now time.Time) ([]BiasRow, error) {
			b, err := coverage.BuildBiasBacklog(ctx, db, now)
			if err != nil {
				return nil, err
			}
			return BiasRows(b), nil
		},
		describe: func(r BiasRow) []any {
			return []any{"combo", r.ComboKey, "priority", r.Priority, "frames_needed", r.FramesNeeded, "frames_have", r.FramesHave}
		},
	}}
}

// NewFlat publishes the open flat requests into ts_flat_request.
func NewFlat(appDB, sched *gorm.DB, mode string) *FlatPublisher {
	return &FlatPublisher{App: appDB, Sched: sched, Mode: mode, t: table[FlatRow]{
		name: FlatTable, keyCol: "request_id", noun: "flat request",
		columns: []string{"target_id", "target_name", "night", "filters", "requested_at", "computed_at"},
		want:    FlatRows,
		describe: func(r FlatRow) []any {
			return []any{"request", r.RequestID, "target", r.TargetName, "night", r.Night}
		},
	}}
}

const BiasSchema = `create table if not exists ts_bias_need (
    combo_key text NOT NULL PRIMARY KEY,
    priority integer NOT NULL,
    gain double precision NOT NULL,
    camera_offset double precision NOT NULL,
    binning double precision NOT NULL,
    readout_mode text,
    frames_needed integer NOT NULL,
    frames_have integer NOT NULL,
    newest_bias_at timestamp,
    session_gap_hours double precision NOT NULL,
    basis text,
    computed_at timestamp NOT NULL
)`

const FlatSchema = `create table if not exists ts_flat_request (
    request_id text NOT NULL PRIMARY KEY,
    target_id integer NOT NULL,
    target_name text NOT NULL,
    night text NOT NULL,
    filters text,
    requested_at timestamp NOT NULL,
    computed_at timestamp NOT NULL
)`
