package sublength

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/tables"
	"gorm.io/gorm/clause"
)

const Table = "ts_sub_length"

const (
	SkipOff         = "off"
	SkipNoScheduler = "no scheduler database"
	SkipNoTable     = "no " + Table + " table"
	SkipUnchanged   = "unchanged"
)

type Status struct {
	Mode        string     `json:"mode"`
	PublishedAt *time.Time `json:"published_at"`
	Rows        int        `json:"rows"`
	Changed     int        `json:"changed"`
	Deleted     int        `json:"deleted"`
	Skipped     string     `json:"skipped,omitempty"`
	Error       string     `json:"error,omitempty"`
}

type TableRow struct {
	TargetGUID string    `gorm:"column:target_guid"`
	Filter     string    `gorm:"column:filter"`
	Gain       int       `gorm:"column:gain"`
	Bucket     string    `gorm:"column:bucket"`
	Exposure   float64   `gorm:"column:exposure"`
	Ideal      *float64  `gorm:"column:ideal"`
	Current    *float64  `gorm:"column:current"`
	SkyRate    *float64  `gorm:"column:sky_rate"`
	SNRChange  *float64  `gorm:"column:snr_change"`
	Basis      *string   `gorm:"column:basis"`
	ComputedAt time.Time `gorm:"column:computed_at"`
}

type rowKey struct {
	guid, filter string
	gain         int
	bucket       string
}

func (r TableRow) key() rowKey { return rowKey{r.TargetGUID, r.Filter, r.Gain, r.Bucket} }

func fptr(v float64) *float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	v = math.Round(v*1e6) / 1e6
	return &v
}

func sameF(a, b *float64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return math.Abs(*a-*b) <= 1e-6*math.Max(1, math.Abs(*a))
}

func (r TableRow) same(o TableRow) bool {
	return r.Exposure == o.Exposure && sameF(r.Ideal, o.Ideal) && sameF(r.Current, o.Current)
}

func TableRows(res *Result) []TableRow {
	var out []TableRow
	if res == nil || res.ComputedAt == nil {
		return out
	}
	for _, r := range res.Rows {
		if !(r.Exposure > 0) {
			continue
		}
		basis := r.Basis
		out = append(out, TableRow{TargetGUID: r.TargetGUID, Filter: r.Filter, Gain: r.Gain, Bucket: string(r.Bucket), Exposure: r.Exposure,
			Ideal: fptr(r.Ideal), Current: fptr(r.Current), SkyRate: fptr(r.SkyRate), SNRChange: fptr(r.SNRChange), Basis: &basis,
			ComputedAt: res.ComputedAt.UTC()})
	}
	return out
}

func (s *Service) finish(st Status) Status {
	st.Mode = s.mode()
	s.mu.Lock()
	defer s.mu.Unlock()
	if st.PublishedAt == nil {
		st.PublishedAt = s.status.PublishedAt
	}
	s.status = st
	if s.last != nil {
		s.last.Publish = st
	}
	return st
}

func (s *Service) Publish(ctx context.Context, res *Result) (Status, error) {
	if s.mode() == ModeOff {
		return s.finish(Status{Skipped: SkipOff}), nil
	}
	if s.Sched == nil {
		return s.finish(Status{Skipped: SkipNoScheduler}), nil
	}
	has, err := tables.Has(ctx, s.Sched, Table)
	if err != nil {
		return s.finish(Status{Error: err.Error()}), err
	}
	if !has {
		return s.finish(Status{Skipped: SkipNoTable}), nil
	}
	want := TableRows(res)
	if s.dryRun == nil {
		s.dryRun = map[rowKey]TableRow{}
	}
	var have []TableRow
	if err := s.Sched.WithContext(ctx).Table(Table).Find(&have).Error; err != nil {
		return s.finish(Status{Error: err.Error()}), fmt.Errorf("load %s: %w", Table, err)
	}
	old := make(map[rowKey]TableRow, len(have))
	for _, r := range have {
		old[r.key()] = r
	}
	st := Status{Rows: len(want)}
	keep := map[rowKey]bool{}
	for _, r := range want {
		keep[r.key()] = true
		if o, ok := old[r.key()]; ok && o.same(r) {
			continue
		}
		st.Changed++
		if s.mode() == ModeDryRun {
			if seen, ok := s.dryRun[r.key()]; ok && seen.same(r) {
				continue
			}
			s.dryRun[r.key()] = r
			slog.Info("Would publish a sub length (dry run)", "target_guid", r.TargetGUID, "filter", r.Filter, "gain", r.Gain, "bucket", r.Bucket,
				"exposure", r.Exposure)
			continue
		}
		if err := s.Sched.WithContext(ctx).Table(Table).Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "target_guid"}, {Name: "filter"}, {Name: "gain"}, {Name: "bucket"}},
			DoUpdates: clause.AssignmentColumns([]string{"exposure", "ideal", "current", "sky_rate", "snr_change", "basis", "computed_at"}),
		}).Create(&r).Error; err != nil {
			return s.finish(Status{Error: err.Error()}), fmt.Errorf("publish sub length %s %s: %w", r.TargetGUID, r.Filter, err)
		}
	}
	for k := range old {
		if keep[k] {
			continue
		}
		st.Deleted++
		if s.mode() == ModeDryRun {
			slog.Info("Would remove a sub length (dry run)", "target_guid", k.guid, "filter", k.filter, "gain", k.gain, "bucket", k.bucket)
			continue
		}
		if err := s.Sched.WithContext(ctx).Table(Table).
			Where("target_guid = ? AND filter = ? AND gain = ? AND bucket = ?", k.guid, k.filter, k.gain, k.bucket).Delete(nil).Error; err != nil {
			return s.finish(Status{Error: err.Error()}), fmt.Errorf("remove sub length: %w", err)
		}
	}
	if st.Changed == 0 && st.Deleted == 0 {
		st.Skipped = SkipUnchanged
		return s.finish(st), nil
	}
	if s.mode() == ModeOn {
		at := s.now().UTC()
		st.PublishedAt = &at
		slog.Info("Published sub lengths", "rows", st.Rows, "changed", st.Changed, "deleted", st.Deleted)
	}
	return s.finish(st), nil
}

func (s *Service) Refresh(ctx context.Context) (*Result, error) {
	s.refresh.Lock()
	defer s.refresh.Unlock()
	res, err := s.Compute(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	res.Publish = s.status
	s.last = res
	s.mu.Unlock()
	if _, err := s.Publish(ctx, res); err != nil {
		return res, err
	}
	return s.Latest(), nil
}

// Run computes at start, soon after each Kick, and every backstop, until
// ctx ends.
func (s *Service) Run(ctx context.Context, backstop time.Duration) {
	s.debouncer().Run(ctx, backstop, func(ctx context.Context, _ []string) {
		if _, err := s.Refresh(ctx); err != nil && ctx.Err() == nil {
			slog.Error("Computing sub lengths failed", "error", err)
		}
	})
}

const Schema = `create table if not exists ts_sub_length (
    target_guid text NOT NULL,
    filter text NOT NULL,
    gain integer NOT NULL,
    bucket text NOT NULL,
    exposure double precision NOT NULL,
    ideal double precision,
    current double precision,
    sky_rate double precision,
    snr_change double precision,
    basis text,
    computed_at timestamp NOT NULL,
    PRIMARY KEY (target_guid, filter, gain, bucket)
)`
