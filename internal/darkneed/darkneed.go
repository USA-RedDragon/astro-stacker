package darkneed

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/coverage"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const Table = "ts_dark_need"

const (
	PublishOff    = "off"
	PublishDryRun = "dry-run"
	PublishOn     = "on"
)

const (
	SkipOff         = "off"
	SkipNoScheduler = "no scheduler database"
	SkipNoTable     = "no " + Table + " table"
	SkipUnchanged   = "unchanged"
)

type Row struct {
	ComboKey        string     `gorm:"column:combo_key"`
	Priority        int        `gorm:"column:priority"`
	Exposure        float64    `gorm:"column:exposure"`
	Gain            float64    `gorm:"column:gain"`
	CameraOffset    float64    `gorm:"column:camera_offset"`
	Binning         float64    `gorm:"column:binning"`
	ReadoutMode     *string    `gorm:"column:readout_mode"`
	SetTemp         float64    `gorm:"column:set_temp"`
	FramesNeeded    int        `gorm:"column:frames_needed"`
	FramesHave      int        `gorm:"column:frames_have"`
	FramesRejected  int        `gorm:"column:frames_rejected"`
	LightsBlocked   int        `gorm:"column:lights_blocked"`
	LightsScaled    int        `gorm:"column:lights_scaled"`
	NewestDarkAt    *time.Time `gorm:"column:newest_dark_at"`
	SessionGapHours float64    `gorm:"column:session_gap_hours"`
	Basis           string     `gorm:"column:basis"`
	ComputedAt      time.Time  `gorm:"column:computed_at"`
}

func FromBacklog(b coverage.DarkBacklog) []Row {
	rows := make([]Row, 0, len(b.Combos))
	for _, c := range b.Combos {
		r := Row{
			ComboKey: c.ComboKey, Priority: c.Priority, Exposure: c.Exposure, Gain: c.Gain, CameraOffset: c.Offset,
			Binning: c.Binning, ReadoutMode: c.ReadoutMode, SetTemp: c.SetTemp, FramesNeeded: c.FramesNeeded,
			FramesHave: c.FramesHave, FramesRejected: c.FramesRejected, LightsBlocked: c.LightsBlocked,
			LightsScaled: c.LightsScaled, SessionGapHours: b.SessionGapHours, Basis: c.Basis, ComputedAt: b.ComputedAt,
		}
		if c.NewestDarkAt != nil {
			t := c.NewestDarkAt.UTC()
			r.NewestDarkAt = &t
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

func (r Row) same(o Row) bool {
	return r.Priority == o.Priority && r.Exposure == o.Exposure && r.Gain == o.Gain && r.CameraOffset == o.CameraOffset &&
		r.Binning == o.Binning && strEqual(r.ReadoutMode, o.ReadoutMode) && r.SetTemp == o.SetTemp &&
		r.FramesNeeded == o.FramesNeeded && r.FramesHave == o.FramesHave && r.FramesRejected == o.FramesRejected &&
		r.LightsBlocked == o.LightsBlocked && r.LightsScaled == o.LightsScaled && timeEqual(r.NewestDarkAt, o.NewestDarkAt) &&
		r.SessionGapHours == o.SessionGapHours && r.Basis == o.Basis
}

type Status struct {
	Mode        string     `json:"mode"`
	PublishedAt *time.Time `json:"published_at"`
	Rows        int        `json:"rows"`
	Changed     int        `json:"changed"`
	Deleted     int        `json:"deleted"`
	Skipped     string     `json:"skipped"`
}

type Publisher struct {
	App   *gorm.DB
	Sched *gorm.DB
	Mode  string
	Now   func() time.Time

	mu     sync.Mutex
	status Status
	noted  string
}

func (p *Publisher) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *Publisher) Status() Status {
	if p == nil {
		return Status{Mode: PublishOff, Skipped: SkipOff}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.status
	if s.Mode == "" {
		s.Mode = p.Mode
	}
	return s
}

func (p *Publisher) finish(s Status) Status {
	s.Mode = p.Mode
	p.mu.Lock()
	defer p.mu.Unlock()
	if s.PublishedAt == nil {
		s.PublishedAt = p.status.PublishedAt
	}
	p.status = s
	if s.Skipped != "" && s.Skipped != SkipUnchanged && s.Skipped != p.noted {
		slog.Warn("Dark backlog is not published to the observatory", "reason", s.Skipped, "mode", p.Mode)
	}
	p.noted = s.Skipped
	return s
}

func (p *Publisher) Publish(ctx context.Context) (Status, error) {
	if p.Mode != PublishOn && p.Mode != PublishDryRun {
		return p.finish(Status{Skipped: SkipOff}), nil
	}
	if p.Sched == nil {
		return p.finish(Status{Skipped: SkipNoScheduler}), nil
	}
	if !p.Sched.Migrator().HasTable(Table) {
		return p.finish(Status{Skipped: SkipNoTable}), nil
	}
	b, err := coverage.BuildDarkBacklog(ctx, p.App, p.now())
	if err != nil {
		return p.Status(), err
	}
	want := FromBacklog(b)
	var have []Row
	if err := p.Sched.WithContext(ctx).Table(Table).Find(&have).Error; err != nil {
		return p.Status(), fmt.Errorf("load %s: %w", Table, err)
	}
	old := make(map[string]Row, len(have))
	for _, r := range have {
		old[r.ComboKey] = r
	}
	s := Status{Rows: len(want)}
	keep := make(map[string]bool, len(want))
	for _, r := range want {
		keep[r.ComboKey] = true
		if o, ok := old[r.ComboKey]; ok && o.same(r) {
			continue
		}
		s.Changed++
		if p.Mode == PublishDryRun {
			slog.Info("Would publish a dark need (dry run)", "combo", r.ComboKey, "priority", r.Priority,
				"frames_needed", r.FramesNeeded, "frames_have", r.FramesHave, "lights_blocked", r.LightsBlocked)
			continue
		}
		if err := p.Sched.WithContext(ctx).Table(Table).Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "combo_key"}},
			DoUpdates: clause.AssignmentColumns([]string{"priority", "exposure", "gain", "camera_offset", "binning", "readout_mode",
				"set_temp", "frames_needed", "frames_have", "frames_rejected", "lights_blocked", "lights_scaled", "newest_dark_at",
				"session_gap_hours", "basis", "computed_at"}),
		}).Create(&r).Error; err != nil {
			return p.Status(), fmt.Errorf("publish dark need %s: %w", r.ComboKey, err)
		}
	}
	for k := range old {
		if keep[k] {
			continue
		}
		s.Deleted++
		if p.Mode == PublishDryRun {
			slog.Info("Would remove a met dark need (dry run)", "combo", k)
			continue
		}
		if err := p.Sched.WithContext(ctx).Table(Table).Where("combo_key = ?", k).Delete(nil).Error; err != nil {
			return p.Status(), fmt.Errorf("remove dark need %s: %w", k, err)
		}
	}
	if s.Changed == 0 && s.Deleted == 0 {
		s.Skipped = SkipUnchanged
		return p.finish(s), nil
	}
	if p.Mode == PublishOn {
		at := p.now().UTC()
		s.PublishedAt = &at
		slog.Info("Published the dark backlog", "rows", s.Rows, "changed", s.Changed, "deleted", s.Deleted)
	}
	return p.finish(s), nil
}

func (p *Publisher) Run(ctx context.Context) {
	if _, err := p.Publish(ctx); err != nil && ctx.Err() == nil {
		slog.Error("Publishing the dark backlog failed", "error", err)
	}
}

const Schema = `create table if not exists ts_dark_need (
    combo_key text NOT NULL PRIMARY KEY,
    priority integer NOT NULL,
    exposure double precision NOT NULL,
    gain double precision NOT NULL,
    camera_offset double precision NOT NULL,
    binning double precision NOT NULL,
    readout_mode text,
    set_temp double precision NOT NULL,
    frames_needed integer NOT NULL,
    frames_have integer NOT NULL,
    frames_rejected integer NOT NULL,
    lights_blocked integer NOT NULL,
    lights_scaled integer NOT NULL,
    newest_dark_at timestamp,
    session_gap_hours double precision NOT NULL,
    basis text,
    computed_at timestamp NOT NULL
)`
