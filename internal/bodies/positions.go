package bodies

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/horizons"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
	"github.com/USA-RedDragon/astro-stacker/internal/store/tables"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	ModeOff    = "off"
	ModeDryRun = "dry-run"
	ModeOn     = "on"

	CoarseStep = 10 * time.Minute
	FineStep   = time.Minute
	// NearEarth is the geocentric range, in au, under which a night's table
	// is fetched at FineStep.
	NearEarth = 0.1
	// SpanPad is added before sunset and after sunrise.
	SpanPad = 30 * time.Minute
	// Nights is how many nights, from tonight, each target has a table for.
	Nights = 2
	// CacheKeep is how long fetched tables stay cached after their night.
	CacheKeep = 30 * 24 * time.Hour

	SkipOff         = "off"
	SkipNoScheduler = "no scheduler database"
	SkipNoTargets   = "no " + TargetTable + " table"
	SkipNoTable     = "no " + PositionTable + " table"
	SkipUnchanged   = "unchanged"
)

var ErrNoSite = errors.New("the observatory site is not known")

type SiteFunc func(ctx context.Context) (sky.Site, error)

// PositionRow is one row of ts_body_position. RA and Dec are Horizons
// quantity 1 (astrometric ICRF) and RAApp, DecApp quantity 2, in degrees;
// RARate is dRA*cos(Dec) and DecRate d(Dec)/dt, in arcsec per hour; Mag is
// quantity 9, Delta the range in au and UncRA, UncDec the 3-sigma
// uncertainty in arcsec.
type PositionRow struct {
	TargetGUID  string    `gorm:"column:target_guid" json:"target_guid"`
	At          time.Time `gorm:"column:at" json:"at"`
	Designation string    `gorm:"column:designation" json:"designation"`
	Night       string    `gorm:"column:night" json:"night"`
	RA          float64   `gorm:"column:ra" json:"ra"`
	Dec         float64   `gorm:"column:dec" json:"dec"`
	RAApp       *float64  `gorm:"column:ra_app" json:"ra_app"`
	DecApp      *float64  `gorm:"column:dec_app" json:"dec_app"`
	RARate      float64   `gorm:"column:ra_rate" json:"ra_rate"`
	DecRate     float64   `gorm:"column:dec_rate" json:"dec_rate"`
	Mag         *float64  `gorm:"column:mag" json:"mag"`
	Delta       *float64  `gorm:"column:delta" json:"delta"`
	UncRA       *float64  `gorm:"column:unc_ra" json:"unc_ra"`
	UncDec      *float64  `gorm:"column:unc_dec" json:"unc_dec"`
	ComputedAt  time.Time `gorm:"column:computed_at" json:"computed_at"`
}

type rowKey struct {
	guid string
	at   int64
}

func (r PositionRow) key() rowKey { return rowKey{r.TargetGUID, r.At.UTC().Unix()} }

func sameP(a, b *float64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return math.Abs(*a-*b) <= 1e-9*math.Max(1, math.Abs(*a))
}

func (r PositionRow) same(o PositionRow) bool {
	return r.Designation == o.Designation && r.Night == o.Night && r.RA == o.RA && r.Dec == o.Dec &&
		r.RARate == o.RARate && r.DecRate == o.DecRate && sameP(r.RAApp, o.RAApp) && sameP(r.DecApp, o.DecApp) &&
		sameP(r.Mag, o.Mag) && sameP(r.Delta, o.Delta) && sameP(r.UncRA, o.UncRA) && sameP(r.UncDec, o.UncDec)
}

// NightSpan is one night's table: from SpanPad before sunset to SpanPad
// after sunrise, on CoarseStep boundaries. Night is the local date the night
// starts on.
type NightSpan struct {
	Night string    `json:"night"`
	Start time.Time `json:"start"`
	Stop  time.Time `json:"stop"`
}

// Span is the night that starts at the local noon before t.
func Span(site sky.Site, t time.Time) NightSpan {
	noon := site.LocalNoon(t)
	local := noon.Add(time.Duration(site.Longitude / 15 * float64(time.Hour)))
	ns := NightSpan{Night: local.Format(time.DateOnly), Start: noon, Stop: noon.Add(24 * time.Hour)}
	var set, rise *time.Time
	for at := noon; at.Before(noon.Add(24 * time.Hour)); at = at.Add(CoarseStep) {
		if site.SunAltitude(at) < 0 {
			a := at
			if set == nil {
				set = &a
			}
			rise = &a
		}
	}
	if set != nil {
		ns.Start = set.Add(-CoarseStep - SpanPad).Truncate(CoarseStep)
		ns.Stop = rise.Add(CoarseStep + SpanPad).Truncate(CoarseStep)
	}
	return ns
}

// Spans are the nights from tonight: tonight's until its sunrise, then the
// following ones.
func Spans(site sky.Site, now time.Time, n int) []NightSpan {
	out := make([]NightSpan, 0, n)
	at := now
	first := Span(site, at)
	if !now.Before(first.Stop) {
		at = at.Add(24 * time.Hour)
	}
	for range n {
		s := Span(site, at)
		out = append(out, s)
		at = s.Stop.Add(12 * time.Hour)
	}
	return out
}

type TargetError struct {
	TargetGUID  string `json:"target_guid"`
	Name        string `json:"name"`
	Designation string `json:"designation"`
	Night       string `json:"night,omitempty"`
	Error       string `json:"error"`
}

type TableSummary struct {
	TargetGUID  string     `json:"target_guid"`
	Name        string     `json:"name"`
	Designation string     `json:"designation"`
	Command     string     `json:"horizons_command"`
	Night       string     `json:"night"`
	Start       time.Time  `json:"start"`
	Stop        time.Time  `json:"stop"`
	StepSeconds int        `json:"step_seconds"`
	Rows        int        `json:"rows"`
	MinDelta    *float64   `json:"min_delta_au"`
	MaxRate     float64    `json:"max_rate_arcsec_per_min"`
	Mag         *float64   `json:"mag"`
	FetchedAt   *time.Time `json:"fetched_at"`
	Cached      bool       `json:"cached"`
}

type Status struct {
	Mode        string        `json:"mode"`
	ComputedAt  *time.Time    `json:"computed_at"`
	PublishedAt *time.Time    `json:"published_at"`
	Targets     int           `json:"targets"`
	Rows        int           `json:"rows"`
	Changed     int           `json:"changed"`
	Deleted     int           `json:"deleted"`
	Skipped     string        `json:"skipped,omitempty"`
	Error       string        `json:"error,omitempty"`
	Errors      []TargetError `json:"errors"`
}

type Result struct {
	Status
	Tables []TableSummary `json:"tables"`
	rows   []PositionRow
}

// Positions are the computed rows of one target, or all of them for "".
func (r *Result) Positions(guid, night string) []PositionRow {
	out := []PositionRow{}
	if r == nil {
		return out
	}
	for _, row := range r.rows {
		if (guid == "" || strings.EqualFold(row.TargetGUID, guid)) && (night == "" || row.Night == night) {
			out = append(out, row)
		}
	}
	return out
}

type Service struct {
	Sched    *gorm.DB
	Horizons *horizons.Client
	Site     SiteFunc
	Mode     string
	Now      func() time.Time

	refresh sync.Mutex
	mu      sync.Mutex
	last    *Result
}

func (s *Service) mode() string {
	switch s.Mode {
	case ModeOff, ModeDryRun:
		return s.Mode
	}
	return ModeOn
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) Latest() *Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

func (s *Service) keep(res *Result) *Result {
	res.Mode = s.mode()
	if res.Errors == nil {
		res.Errors = []TargetError{}
	}
	if res.Tables == nil {
		res.Tables = []TableSummary{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if res.PublishedAt == nil && s.last != nil {
		res.PublishedAt = s.last.PublishedAt
	}
	s.last = res
	return res
}

func rowsOf(t Target, ns NightSpan, tab horizons.Table, at time.Time) []PositionRow {
	out := make([]PositionRow, 0, len(tab.Rows))
	for _, r := range tab.Rows {
		out = append(out, PositionRow{TargetGUID: t.GUID, At: r.At.UTC(), Designation: t.Designation, Night: ns.Night,
			RA: r.RA, Dec: r.Dec, RAApp: r.RAApp, DecApp: r.DecApp, RARate: r.RARate, DecRate: r.DecRate,
			Mag: r.Mag, Delta: r.Delta, UncRA: r.UncRA, UncDec: r.UncDec, ComputedAt: at})
	}
	return out
}

func summary(t Target, ns NightSpan, step time.Duration, tab horizons.Table) TableSummary {
	sm := TableSummary{TargetGUID: t.GUID, Name: t.Name, Designation: t.Designation, Command: horizons.Command(t.Designation),
		Night: ns.Night, Start: ns.Start, Stop: ns.Stop, StepSeconds: int(step / time.Second), Rows: len(tab.Rows), Cached: tab.Cached}
	if d, ok := tab.MinDelta(); ok {
		sm.MinDelta = &d
	}
	for _, r := range tab.Rows {
		sm.MaxRate = max(sm.MaxRate, math.Hypot(r.RARate, r.DecRate)/60)
		if sm.Mag == nil && r.Mag != nil {
			m := *r.Mag
			sm.Mag = &m
		}
	}
	sm.MaxRate = math.Round(sm.MaxRate*1e4) / 1e4
	if !tab.FetchedAt.IsZero() {
		f := tab.FetchedAt
		sm.FetchedAt = &f
	}
	return sm
}

// Table fetches one target's night: at CoarseStep, then again at FineStep
// when the body comes nearer than NearEarth.
func (s *Service) Table(ctx context.Context, t Target, site sky.Site, ns NightSpan) (horizons.Table, time.Duration, error) {
	req := horizons.Request{Command: horizons.Command(t.Designation), Site: site, Start: ns.Start, Stop: ns.Stop, Step: CoarseStep}
	tab, err := s.Horizons.Ephemeris(ctx, req)
	if err != nil {
		return tab, CoarseStep, err
	}
	if d, ok := tab.MinDelta(); ok && d < NearEarth {
		req.Step = FineStep
		fine, err := s.Horizons.Ephemeris(ctx, req)
		return fine, FineStep, err
	}
	return tab, CoarseStep, nil
}

// Compute builds the tables of every active target with a designation.
func (s *Service) Compute(ctx context.Context) (*Result, error) {
	res := &Result{}
	at := s.now().UTC()
	res.ComputedAt = &at
	if s.mode() == ModeOff {
		res.Skipped = SkipOff
		return res, nil
	}
	if s.Sched == nil {
		res.Skipped = SkipNoScheduler
		return res, nil
	}
	has, err := tables.Has(ctx, s.Sched, TargetTable)
	if err != nil {
		return res, err
	}
	if !has {
		res.Skipped = SkipNoTargets
		return res, nil
	}
	targets, err := Targets(ctx, s.Sched)
	if err != nil {
		return res, err
	}
	targets = slices.DeleteFunc(targets, func(t Target) bool { return !t.Active })
	res.Targets = len(targets)
	if len(targets) == 0 {
		return res, nil
	}
	has, err = tables.Has(ctx, s.Sched, PositionTable)
	if err != nil {
		return res, err
	}
	if !has {
		res.Skipped = SkipNoTable
		return res, nil
	}
	if s.Site == nil || s.Horizons == nil {
		return res, ErrNoSite
	}
	site, err := s.Site(ctx)
	if err != nil {
		return res, fmt.Errorf("%w: %w", ErrNoSite, err)
	}
	spans := Spans(site, at, Nights)
	seen := map[rowKey]bool{}
	for _, t := range targets {
		for _, ns := range spans {
			if err := ctx.Err(); err != nil {
				return res, err
			}
			tab, step, err := s.Table(ctx, t, site, ns)
			if err != nil {
				res.Errors = append(res.Errors, TargetError{TargetGUID: t.GUID, Name: t.Name, Designation: t.Designation, Night: ns.Night, Error: err.Error()})
				slog.Warn("Horizons table failed", "target", t.Name, "designation", t.Designation, "night", ns.Night, "error", err)
				continue
			}
			res.Tables = append(res.Tables, summary(t, ns, step, tab))
			for _, r := range rowsOf(t, ns, tab, at) {
				if !seen[r.key()] {
					seen[r.key()] = true
					res.rows = append(res.rows, r)
				}
			}
		}
	}
	res.Rows = len(res.rows)
	if _, err := s.Horizons.Prune(ctx, at.Add(-CacheKeep)); err != nil {
		slog.Warn("Pruning the Horizons cache failed", "error", err)
	}
	return res, nil
}

// Publish writes the rows into ts_body_position, removing rows of nights
// and targets no longer computed. A target whose fetch failed keeps the rows
// it has of its current designation. A dry run counts what it would change.
func (s *Service) Publish(ctx context.Context, res *Result) error {
	if res.Skipped != "" {
		return nil
	}
	has, err := tables.Has(ctx, s.Sched, PositionTable)
	if err != nil {
		return err
	}
	if !has {
		res.Skipped = SkipNoTable
		return nil
	}
	var have []PositionRow
	if err := s.Sched.WithContext(ctx).Table(PositionTable).Find(&have).Error; err != nil {
		return fmt.Errorf("load %s: %w", PositionTable, err)
	}
	old := make(map[rowKey]PositionRow, len(have))
	for _, r := range have {
		old[r.key()] = r
	}
	failed := map[string]string{}
	for _, e := range res.Errors {
		failed[e.TargetGUID+"\x00"+e.Night] = e.Designation
	}
	want := map[rowKey]bool{}
	var write []PositionRow
	for _, r := range res.rows {
		want[r.key()] = true
		if o, ok := old[r.key()]; ok && o.same(r) {
			continue
		}
		res.Changed++
		write = append(write, r)
	}
	if len(write) > 0 && s.mode() == ModeOn {
		if err := s.Sched.WithContext(ctx).Table(PositionTable).Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "target_guid"}, {Name: "at"}},
			DoUpdates: clause.AssignmentColumns([]string{"designation", "night", "ra", "dec", "ra_app", "dec_app", "ra_rate", "dec_rate",
				"mag", "delta", "unc_ra", "unc_dec", "computed_at"}),
		}).CreateInBatches(&write, 200).Error; err != nil {
			return fmt.Errorf("publish %s: %w", PositionTable, err)
		}
	}
	var drop []PositionRow
	for k, r := range old {
		if want[k] {
			continue
		}
		// A failed fetch keeps the night's rows, unless they are of another
		// designation than the one now stored.
		if des, ok := failed[k.guid+"\x00"+r.Night]; ok && des == r.Designation {
			continue
		}
		res.Deleted++
		drop = append(drop, r)
	}
	if s.mode() == ModeOn {
		for _, r := range drop {
			if err := s.Sched.WithContext(ctx).Table(PositionTable).Where("target_guid = ? AND at = ?", r.TargetGUID, r.At).Delete(nil).Error; err != nil {
				return fmt.Errorf("remove %s rows: %w", PositionTable, err)
			}
		}
	}
	switch {
	case res.Changed == 0 && res.Deleted == 0:
		res.Skipped = SkipUnchanged
	case s.mode() == ModeOn:
		at := s.now().UTC()
		res.PublishedAt = &at
		slog.Info("Published body positions", "rows", res.Rows, "changed", res.Changed, "deleted", res.Deleted)
	default:
		slog.Info("Would publish body positions (dry run)", "rows", res.Rows, "changed", res.Changed, "deleted", res.Deleted)
	}
	return nil
}

func (s *Service) Refresh(ctx context.Context) (*Result, error) {
	s.refresh.Lock()
	defer s.refresh.Unlock()
	res, err := s.Compute(ctx)
	if err == nil {
		err = s.Publish(ctx, res)
	}
	if err != nil {
		res.Error = err.Error()
	}
	return s.keep(res), err
}

func (s *Service) Run(ctx context.Context, every time.Duration) {
	for {
		if _, err := s.Refresh(ctx); err != nil && ctx.Err() == nil {
			slog.Error("Building body position tables failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}

const Schema = `create table if not exists ts_body_position (
    target_guid text NOT NULL,
    at timestamp NOT NULL,
    designation text NOT NULL,
    night text NOT NULL,
    ra double precision NOT NULL,
    dec double precision NOT NULL,
    ra_app double precision,
    dec_app double precision,
    ra_rate double precision NOT NULL,
    dec_rate double precision NOT NULL,
    mag double precision,
    delta double precision,
    unc_ra double precision,
    unc_dec double precision,
    computed_at timestamp NOT NULL,
    PRIMARY KEY (target_guid, at)
)`

const TargetSchema = `create table if not exists ts_target_body (
    target_guid text NOT NULL PRIMARY KEY,
    designation text NOT NULL,
    kind text NOT NULL DEFAULT 'comet',
    updated_at text
)`
