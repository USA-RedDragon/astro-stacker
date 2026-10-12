package moonkeep

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/frameheader"
	"github.com/USA-RedDragon/astro-stacker/internal/goals"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
	"github.com/USA-RedDragon/astro-stacker/internal/store/tables"
	"github.com/USA-RedDragon/astro-stacker/internal/tslink"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	ModeOff    = "off"
	ModeDryRun = "dry-run"
	ModeOn     = "on"

	Table = "ts_moon_keep"

	SkipOff         = "off"
	SkipNoScheduler = "no scheduler database"
	SkipNoTable     = "no " + Table + " table"
	SkipUnchanged   = "unchanged"
)

var ErrNoSite = errors.New("the observatory site is not known, so the Moon can't be placed")

type SiteFunc func(ctx context.Context) (sky.Site, error)

// Status is the last publish to ts_moon_keep.
type Status struct {
	Mode        string     `json:"mode"`
	PublishedAt *time.Time `json:"published_at"`
	Rows        int        `json:"rows"`
	Changed     int        `json:"changed"`
	Deleted     int        `json:"deleted"`
	Skipped     string     `json:"skipped,omitempty"`
	Error       string     `json:"error,omitempty"`
}

// Rules are the constants the rows were judged by, for the UI.
type Rules struct {
	SkipBelow        float64   `json:"skip_below"`
	Confidence       float64   `json:"confidence"`
	MinSubs          int       `json:"min_subs"`
	MinNights        int       `json:"min_nights"`
	GoodNightQuality float64   `json:"good_night_quality"`
	FloorDegrees     float64   `json:"floor_degrees"`
	PriorStrength    float64   `json:"prior_strength"`
	IllumEdges       []float64 `json:"illum_edges"`
	AltEdges         []float64 `json:"alt_edges"`
	SepEdges         []float64 `json:"sep_edges"`
}

func rules() Rules {
	return Rules{SkipBelow: SkipBelow, Confidence: Confidence, MinSubs: MinSubs, MinNights: MinNights, GoodNightQuality: GoodNightQuality,
		FloorDegrees: FloorDegrees, PriorStrength: PriorStrength, IllumEdges: IllumEdges(), AltEdges: AltEdges(), SepEdges: SepEdges()}
}

// Result is one fit.
type Result struct {
	ComputedAt *time.Time `json:"computed_at"`
	Mode       string     `json:"mode"`
	Rules      Rules      `json:"rules"`
	Subs       int        `json:"subs"`
	Kept       int        `json:"kept"`
	Nights     int        `json:"nights"`
	From       *time.Time `json:"from"`
	To         *time.Time `json:"to"`
	Rows       []Row      `json:"rows"`
	Replay     *Replay    `json:"replay"`
	Publish    Status     `json:"publish"`
	Reason     string     `json:"reason,omitempty"`
	model      *Model
	candidates []Candidate
}

// Candidates is the active targets and their planned filters at the fit.
func (r *Result) Candidates() []Candidate {
	if r == nil {
		return nil
	}
	return r.candidates
}

// Model is the fitted table, nil before the first fit.
func (r *Result) Model() *Model {
	if r == nil {
		return nil
	}
	return r.model
}

type Service struct {
	App   *gorm.DB
	Sched *gorm.DB
	Site  SiteFunc
	Mode  string
	Now   func() time.Time

	mu      sync.Mutex
	refresh sync.Mutex
	last    *Result
	status  Status
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) mode() string {
	switch s.Mode {
	case ModeOn, ModeDryRun:
		return s.Mode
	}
	return ModeOff
}

func (s *Service) Latest() *Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

type lightRow struct {
	Object   string
	Filter   string
	Key      string
	Exposure *float64
	DateObs  *time.Time
	MountRA  *float64
	MountDec *float64
	Status   *string
	Score    *float64
}

func (s *Service) inputs(ctx context.Context, pos map[string]position) ([]Input, error) {
	var rows []lightRow
	if err := s.App.WithContext(ctx).Table("frames f").
		Select(`f.object, f.filter, f.key, f.exposure, f.date_obs, f.mount_ra, f.mount_dec, sf.status, sf.score`).
		Joins("JOIN stack_frames sf ON sf.frame_id = f.id").
		Where("f.type = ? AND f.index_error IS NULL AND f.date_obs IS NOT NULL", "LIGHT").
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("load lights: %w", err)
	}
	out := make([]Input, 0, len(rows))
	for _, r := range rows {
		if r.Status == nil || r.Exposure == nil || strings.Contains(path.Base(r.Key), "_cal.") {
			continue
		}
		in := Input{Object: r.Object, Filter: r.Filter, Exposure: *r.Exposure, Start: r.DateObs.UTC(), Status: *r.Status, Score: r.Score}
		if p, ok := pos[r.Object]; ok {
			in.RA, in.Dec, in.HasPos = p.ra, p.dec, true
		} else if r.MountRA != nil && r.MountDec != nil {
			in.RA, in.Dec, in.HasPos = *r.MountRA, *r.MountDec, true
		}
		out = append(out, in)
	}
	return out, nil
}

type position struct {
	ra, dec, minAlt float64
}

func (s *Service) positions(ctx context.Context) (map[string]position, error) {
	out := map[string]position{}
	if s.Sched == nil {
		return out, nil
	}
	if has, err := tables.Has(ctx, s.Sched, "target"); err != nil || !has {
		return out, err
	}
	targets, err := tslink.Targets(ctx, s.Sched)
	if err != nil {
		return nil, err
	}
	objTargets, err := tslink.ObjectTargets(ctx, s.App, targets)
	if err != nil {
		return nil, err
	}
	byGUID := map[string]tslink.Target{}
	for _, t := range targets {
		byGUID[t.GUID] = t
	}
	for obj, guid := range objTargets {
		if t, ok := byGUID[guid]; ok {
			out[obj] = position{t.RA, t.Dec, t.MinAltitude}
		}
	}
	return out, nil
}

// planFilters is each object's planned filters, for the replay's idle hours.
func (s *Service) planFilters(ctx context.Context) (map[string][]Filter, error) {
	out := map[string][]Filter{}
	if s.Sched == nil {
		return out, nil
	}
	for _, t := range []string{"exposureplan", "exposuretemplate", "target", "project"} {
		if has, err := tables.Has(ctx, s.Sched, t); err != nil || !has {
			return out, err
		}
	}
	var rows []struct {
		GUID       string
		Filtername string
	}
	if err := s.Sched.WithContext(ctx).Table("exposureplan e").
		Select(`coalesce(t.guid, '') AS guid, et.filtername AS filtername`).
		Joins(`JOIN exposuretemplate et ON et."Id" = e."exposureTemplateId"`).
		Joins(`JOIN target t ON t."Id" = e.targetid`).
		Joins(`JOIN project p ON p."Id" = t.projectid`).
		Where(`p.state IN (0, 1) AND coalesce(t.active, 1) = 1 AND coalesce(e.enabled, 1) = 1`).
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("load exposure plans: %w", err)
	}
	targets, err := tslink.Targets(ctx, s.Sched)
	if err != nil {
		return nil, err
	}
	objTargets, err := tslink.ObjectTargets(ctx, s.App, targets)
	if err != nil {
		return nil, err
	}
	byGUID := map[string][]Filter{}
	for _, r := range rows {
		f := frameheader.NormalizeFilter(strings.TrimSpace(r.Filtername))
		if r.GUID == "" || f == "" {
			continue
		}
		dup := false
		for _, have := range byGUID[r.GUID] {
			dup = dup || strings.EqualFold(have.Name, f)
		}
		if !dup {
			byGUID[r.GUID] = append(byGUID[r.GUID], Filter{Name: f, Narrowband: goals.IsNarrowband(f)})
		}
	}
	for obj, guid := range objTargets {
		if fs, ok := byGUID[guid]; ok {
			out[obj] = fs
		}
	}
	return out, nil
}

// SiteOf is the observatory site the Moon is placed from.
func (s *Service) SiteOf(ctx context.Context) (sky.Site, error) { return s.site(ctx) }

func (s *Service) site(ctx context.Context) (sky.Site, error) {
	if s.Site == nil {
		return sky.Site{}, ErrNoSite
	}
	site, err := s.Site(ctx)
	if err != nil {
		return sky.Site{}, fmt.Errorf("%w: %w", ErrNoSite, err)
	}
	return site, nil
}

// Compute fits the table and replays it over past nights.
func (s *Service) Compute(ctx context.Context) (*Result, error) {
	res := &Result{Mode: s.mode(), Rules: rules(), Rows: []Row{}}
	site, err := s.site(ctx)
	if err != nil {
		return nil, err
	}
	pos, err := s.positions(ctx)
	if err != nil {
		return nil, err
	}
	in, err := s.inputs(ctx, pos)
	if err != nil {
		return nil, err
	}
	filters, err := s.planFilters(ctx)
	if err != nil {
		return nil, err
	}
	for obj, fs := range filters {
		if p, ok := pos[obj]; ok {
			res.candidates = append(res.candidates, Candidate{Object: obj, RA: p.ra, Dec: p.dec, MinAlt: p.minAlt, Filters: fs})
		}
	}
	samples := Samples(in, site.Latitude, site.Longitude)
	m := Fit(samples)
	at := s.now().UTC()
	res.ComputedAt = &at
	res.model = m
	res.Subs, res.Kept, res.Nights, res.From, res.To = m.Subs, m.Kept, m.Nights, m.From, m.To
	res.Rows = m.Rows
	if m.Subs == 0 {
		res.Reason = "no lights with a stacker verdict and a known position yet"
	}
	rp := Run(samples, filters)
	res.Replay = &rp
	return res, nil
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
		cp := *s.last
		cp.Publish = st
		s.last = &cp
	}
	return st
}

// TableRow is one row of ts_moon_keep.
type TableRow struct {
	Filter     string    `gorm:"column:filter"`
	Narrowband int       `gorm:"column:narrowband"`
	MoonDown   int       `gorm:"column:moon_down"`
	IllumBin   int       `gorm:"column:illum_bin"`
	AltBin     int       `gorm:"column:alt_bin"`
	SepBin     int       `gorm:"column:sep_bin"`
	IllumFrom  float64   `gorm:"column:illum_from"`
	IllumTo    float64   `gorm:"column:illum_to"`
	AltFrom    float64   `gorm:"column:alt_from"`
	AltTo      float64   `gorm:"column:alt_to"`
	SepFrom    float64   `gorm:"column:sep_from"`
	SepTo      float64   `gorm:"column:sep_to"`
	Subs       int       `gorm:"column:subs"`
	Kept       int       `gorm:"column:kept"`
	Nights     int       `gorm:"column:nights"`
	Rate       float64   `gorm:"column:rate"`
	RateLow    float64   `gorm:"column:rate_low"`
	RateHigh   float64   `gorm:"column:rate_high"`
	Worth      float64   `gorm:"column:worth"`
	Usable     int       `gorm:"column:usable"`
	Skips      int       `gorm:"column:skips"`
	ComputedAt time.Time `gorm:"column:computed_at"`
}

type rowKey struct {
	filter          string
	narrowband      int
	illum, alt, sep int
}

func (r TableRow) key() rowKey {
	return rowKey{r.Filter, r.Narrowband, r.IllumBin, r.AltBin, r.SepBin}
}

func near(a, b float64) bool { return math.Abs(a-b) <= 1e-6 }

func (r TableRow) same(o TableRow) bool {
	return r.MoonDown == o.MoonDown && near(r.IllumFrom, o.IllumFrom) && near(r.AltFrom, o.AltFrom) && near(r.SepFrom, o.SepFrom) && near(r.IllumTo, o.IllumTo) && near(r.AltTo, o.AltTo) && near(r.SepTo, o.SepTo) &&
		r.Subs == o.Subs && r.Kept == o.Kept && r.Nights == o.Nights && near(r.Rate, o.Rate) && near(r.RateLow, o.RateLow) &&
		near(r.RateHigh, o.RateHigh) && near(r.Worth, o.Worth) && r.Usable == o.Usable && r.Skips == o.Skips
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// TableRows is what ts_moon_keep gets from a fit: every class row and the
// filter rows with subs.
func TableRows(res *Result) []TableRow {
	var out []TableRow
	if res == nil || res.ComputedAt == nil || res.Subs == 0 {
		return out
	}
	for _, r := range res.Rows {
		if r.Filter != "" && r.Subs == 0 {
			continue
		}
		out = append(out, TableRow{Filter: r.Filter, Narrowband: b2i(r.Narrowband), MoonDown: b2i(r.Down), IllumBin: r.IllumBin, AltBin: r.AltBin, SepBin: r.SepBin,
			IllumFrom: r.IllumFrom, IllumTo: r.IllumTo, AltFrom: r.AltFrom, AltTo: r.AltTo, SepFrom: r.SepFrom, SepTo: r.SepTo,
			Subs: r.Subs, Kept: r.Kept, Nights: r.Nights, Rate: r.Rate, RateLow: r.Low, RateHigh: r.High, Worth: r.Worth,
			Usable: b2i(r.Usable), Skips: b2i(r.Skip), ComputedAt: res.ComputedAt.UTC()})
	}
	return out
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
	var upserts []TableRow
	for _, r := range want {
		keep[r.key()] = true
		if o, ok := old[r.key()]; ok && o.same(r) {
			continue
		}
		st.Changed++
		upserts = append(upserts, r)
	}
	var stale []rowKey
	for k := range old {
		if !keep[k] {
			st.Deleted++
			stale = append(stale, k)
		}
	}
	if st.Changed == 0 && st.Deleted == 0 {
		st.Skipped = SkipUnchanged
		return s.finish(st), nil
	}
	if s.mode() == ModeDryRun {
		slog.Info("Would publish the moon kept rates (dry run)", "rows", st.Rows, "changed", st.Changed, "deleted", st.Deleted)
		return s.finish(st), nil
	}
	if err := s.Sched.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, r := range upserts {
			if err := tx.Table(Table).Clauses(clause.OnConflict{
				Columns: []clause.Column{{Name: "filter"}, {Name: "narrowband"}, {Name: "illum_bin"}, {Name: "alt_bin"}, {Name: "sep_bin"}},
				DoUpdates: clause.AssignmentColumns([]string{"moon_down", "illum_from", "alt_from", "sep_from", "illum_to", "alt_to", "sep_to", "subs", "kept", "nights", "rate",
					"rate_low", "rate_high", "worth", "usable", "skips", "computed_at"}),
			}).Create(&r).Error; err != nil {
				return fmt.Errorf("publish moon kept rate %q: %w", r.Filter, err)
			}
		}
		for _, k := range stale {
			if err := tx.Table(Table).Where("filter = ? AND narrowband = ? AND illum_bin = ? AND alt_bin = ? AND sep_bin = ?",
				k.filter, k.narrowband, k.illum, k.alt, k.sep).Delete(nil).Error; err != nil {
				return fmt.Errorf("remove moon kept rate: %w", err)
			}
		}
		return nil
	}); err != nil {
		return s.finish(Status{Error: err.Error()}), err
	}
	at := s.now().UTC()
	st.PublishedAt = &at
	slog.Info("Published the moon kept rates", "rows", st.Rows, "changed", st.Changed, "deleted", st.Deleted)
	return s.finish(st), nil
}

// Current is the last fit, fitting once if there is none yet.
func (s *Service) Current(ctx context.Context) (*Result, error) {
	if res := s.Latest(); res != nil {
		return res, nil
	}
	s.refresh.Lock()
	defer s.refresh.Unlock()
	if res := s.Latest(); res != nil {
		return res, nil
	}
	return s.refreshLocked(ctx)
}

func (s *Service) Refresh(ctx context.Context) (*Result, error) {
	s.refresh.Lock()
	defer s.refresh.Unlock()
	return s.refreshLocked(ctx)
}

func (s *Service) refreshLocked(ctx context.Context) (*Result, error) {
	res, err := s.Compute(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	res.Publish = s.status
	s.last = res
	s.mu.Unlock()
	if res.ComputedAt == nil {
		return res, nil
	}
	if _, err := s.Publish(ctx, res); err != nil {
		return res, err
	}
	return s.Latest(), nil
}

func (s *Service) Run(ctx context.Context, every time.Duration) {
	for {
		if _, err := s.Refresh(ctx); err != nil && ctx.Err() == nil {
			slog.Error("Fitting the moon kept rates failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}

const Schema = `create table if not exists ts_moon_keep (
    filter text NOT NULL,
    narrowband integer NOT NULL,
    moon_down integer NOT NULL,
    illum_bin integer NOT NULL,
    alt_bin integer NOT NULL,
    sep_bin integer NOT NULL,
    illum_from double precision NOT NULL,
    illum_to double precision NOT NULL,
    alt_from double precision NOT NULL,
    alt_to double precision NOT NULL,
    sep_from double precision NOT NULL,
    sep_to double precision NOT NULL,
    subs integer NOT NULL,
    kept integer NOT NULL,
    nights integer NOT NULL,
    rate double precision NOT NULL,
    rate_low double precision NOT NULL,
    rate_high double precision NOT NULL,
    worth double precision NOT NULL,
    usable integer NOT NULL,
    skips integer NOT NULL,
    computed_at timestamp NOT NULL,
    PRIMARY KEY (filter, narrowband, illum_bin, alt_bin, sep_bin)
)`
