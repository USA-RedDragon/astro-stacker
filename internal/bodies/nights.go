package bodies

import (
	"context"
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
	NightTable = "ts_body_night"
	// SeasonNights is how many nights after tonight each target's night
	// table covers: the 365 nights the plugin's season scan looks at, and
	// one more.
	SeasonNights = 366
	// SeasonScan is how many nights from tonight a season is counted
	// over, as in the plugin's SeasonCounter.
	SeasonScan = 365
	NightStep  = 24 * time.Hour
	// SeasonNearEarth is the geocentric range, in au, under which a night
	// gets no row: one position a day is too coarse that near.
	SeasonNearEarth = 0.05
	NightBasis      = "One position per night, at local mean midnight, from one JPL Horizons table per body (1 day steps, tonight to a year ahead, refreshed daily). " +
		"mag is the brightness estimate: for a comet the brighter of JPL's T-mag (M1 + 5 log10(delta) + K1 log10(r)) and the MPC's H + 5 log10(delta) + 2.5 G log10(r) from its daily comet elements, " +
		"for an asteroid Horizons' APmag (H, G). Nights when the body is nearer than 0.05 au get no row, so they are not in its season.  Both are model estimates, not measurements. mag_limit is the Finder's limit (V 15 for comets, V 12 for asteroids); " +
		"a night counts toward the season only when mag is no fainter than mag_limit. mag_limit is empty when no estimate exists, and then brightness does not limit the night. " +
		"The plugin counts the season from these positions with the same rules as fixed targets: horizon and minimum altitude, twilight, moon avoidance and minimum time."
)

// MagLimit is the Finder's brightness limit for a body of kind.
func MagLimit(kind string) float64 {
	if kind == KindAsteroid {
		return DefaultAsteroidMaxMag
	}
	return DefaultMaxMag
}

// NightRow is one row of ts_body_night: where a body is at local mean
// midnight after the evening of Night, as quantity 1 (astrometric ICRF)
// RA and Dec in degrees and rates in arcsec per hour, with the brightness
// estimate and the limit the night is judged against.
type NightRow struct {
	TargetGUID  string    `gorm:"column:target_guid" json:"target_guid"`
	Night       string    `gorm:"column:night" json:"night"`
	At          time.Time `gorm:"column:at" json:"at"`
	Designation string    `gorm:"column:designation" json:"designation"`
	RA          float64   `gorm:"column:ra" json:"ra"`
	Dec         float64   `gorm:"column:dec" json:"dec"`
	RARate      float64   `gorm:"column:ra_rate" json:"ra_rate"`
	DecRate     float64   `gorm:"column:dec_rate" json:"dec_rate"`
	Mag         *float64  `gorm:"column:mag" json:"mag"`
	MagLimit    *float64  `gorm:"column:mag_limit" json:"mag_limit"`
	ComputedAt  time.Time `gorm:"column:computed_at" json:"computed_at"`
}

type nightKey struct {
	guid  string
	night string
}

func (r NightRow) key() nightKey { return nightKey{strings.ToLower(r.TargetGUID), r.Night} }

func (r NightRow) same(o NightRow) bool {
	return r.Designation == o.Designation && r.At.Equal(o.At) && r.RA == o.RA && r.Dec == o.Dec &&
		r.RARate == o.RARate && r.DecRate == o.DecRate && sameP(r.Mag, o.Mag) && sameP(r.MagLimit, o.MagLimit)
}

// Bright reports whether the row's night passes its brightness limit.
func (r NightRow) Bright() bool {
	return r.MagLimit == nil || (r.Mag != nil && *r.Mag <= *r.MagLimit)
}

func localMean(site sky.Site) time.Duration {
	return time.Duration(site.Longitude / 15 * float64(time.Hour))
}

// NightAt is the time of a night's row: local mean midnight after the
// evening of the date night.
func NightAt(site sky.Site, night time.Time) time.Time {
	d := time.Date(night.Year(), night.Month(), night.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1)
	return d.Add(-localMean(site)).Truncate(time.Minute)
}

// NightOf is the local date of the evening before at.
func NightOf(site sky.Site, at time.Time) string {
	return at.UTC().Add(localMean(site)).Add(-12 * time.Hour).Format(time.DateOnly)
}

// Tonight is the night Spans starts with at now.
func Tonight(site sky.Site, now time.Time) time.Time {
	d, _ := time.Parse(time.DateOnly, Spans(site, now, 1)[0].Night)
	return d
}

// SeasonRequest is the Horizons request of a target's night table from
// the night before first to SeasonNights nights after it.
func SeasonRequest(t Target, site sky.Site, first time.Time) horizons.Request {
	return horizons.Request{Command: horizons.CommandFor(t.Designation, t.Kind), Site: site,
		Start: NightAt(site, first.AddDate(0, 0, -1)), Stop: NightAt(site, first.AddDate(0, 0, SeasonNights)), Step: NightStep}
}

func roundMag(v float64) *float64 {
	r := math.Round(v*100) / 100
	return &r
}

// ElementsFunc finds a comet's MPC elements by designation.
type ElementsFunc func(ctx context.Context, designation string) (Elements, bool)

// SeasonRows turns a target's Horizons table into its night rows. el, when
// given, adds the MPC magnitude estimate for a comet.
func SeasonRows(t Target, site sky.Site, tab horizons.Table, el *Elements, at time.Time) []NightRow {
	out := make([]NightRow, 0, len(tab.Rows))
	seen := map[string]bool{}
	for _, r := range tab.Rows {
		night := NightOf(site, r.At)
		if seen[night] || (r.Delta != nil && *r.Delta < SeasonNearEarth) {
			continue
		}
		seen[night] = true
		row := NightRow{TargetGUID: t.GUID, Night: night, At: r.At.UTC(), Designation: t.Designation,
			RA: r.RA, Dec: r.Dec, RARate: r.RARate, DecRate: r.DecRate, ComputedAt: at}
		var mag *float64
		if r.Mag != nil && finite(*r.Mag) {
			mag = roundMag(*r.Mag)
		}
		if t.Kind != KindAsteroid && el != nil {
			if m, ok := el.Magnitude(el.Seen(r.At)); ok && finite(m) && (mag == nil || m < *mag) {
				mag = roundMag(m)
			}
		}
		if mag != nil {
			row.Mag = mag
			limit := MagLimit(t.Kind)
			row.MagLimit = &limit
		}
		out = append(out, row)
	}
	return out
}

type NightResult struct {
	Mode        string        `json:"mode"`
	ComputedAt  *time.Time    `json:"computed_at"`
	PublishedAt *time.Time    `json:"published_at"`
	From        string        `json:"from_night,omitempty"`
	To          string        `json:"to_night,omitempty"`
	Targets     int           `json:"targets"`
	Rows        int           `json:"rows"`
	Changed     int           `json:"changed"`
	Deleted     int           `json:"deleted"`
	Skipped     string        `json:"skipped,omitempty"`
	Error       string        `json:"error,omitempty"`
	Errors      []TargetError `json:"errors"`
	Basis       string        `json:"basis"`
	rows        []NightRow
	site        *sky.Site
	guids       map[string]bool
	hasRows     map[string]bool
	index       map[nightKey]NightRow
}

// keepFrom is the first night a failed target keeps: the night before
// tonight, whose row the plugin needs for tonight's evening.
func (r *NightResult) keepFrom() string {
	d, err := time.Parse(time.DateOnly, r.From)
	if err != nil {
		return r.From
	}
	return d.AddDate(0, 0, -1).Format(time.DateOnly)
}

// Nights are the computed rows of one target, or all of them for "".
func (r *NightResult) Nights(guid string) []NightRow {
	out := []NightRow{}
	if r == nil {
		return out
	}
	for _, row := range r.rows {
		if guid == "" || strings.EqualFold(row.TargetGUID, guid) {
			out = append(out, row)
		}
	}
	return out
}

// NightService builds each active body target's night table for its
// season and publishes it to the plugin through ts_body_night.
type NightService struct {
	Sched    *gorm.DB
	Horizons *horizons.Client
	Elements ElementsFunc
	Site     SiteFunc
	Mode     string
	Now      func() time.Time

	refresh sync.Mutex
	mu      sync.Mutex
	last    *NightResult
}

func (s *NightService) mode() string {
	switch s.Mode {
	case ModeOff, ModeDryRun:
		return s.Mode
	}
	return ModeOn
}

func (s *NightService) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *NightService) Latest() *NightResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

func (s *NightService) keep(res *NightResult) *NightResult {
	res.Mode = s.mode()
	res.Basis = NightBasis
	if res.Errors == nil {
		res.Errors = []TargetError{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if res.PublishedAt == nil && s.last != nil {
		res.PublishedAt = s.last.PublishedAt
	}
	res.index = make(map[nightKey]NightRow, len(res.rows))
	res.hasRows = map[string]bool{}
	for _, r := range res.rows {
		res.index[r.key()] = r
		res.hasRows[r.key().guid] = true
	}
	s.last = res
	return res
}

func (s *NightService) isBodyTarget(guid string) bool {
	if s.Sched == nil {
		return false
	}
	ts, err := Targets(context.Background(), s.Sched)
	if err != nil {
		return false
	}
	for _, t := range ts {
		if strings.EqualFold(t.GUID, guid) {
			return true
		}
	}
	return false
}

// NightPoint is where an active body target is on night, from the latest
// table, and whether it is bright enough then. isBody is false for a
// target that is not a moving body; usable is false for a night without a
// row. For night "", usable reports whether the body has any rows; a body
// is reported without rows until its table is built.
func (s *NightService) NightPoint(guid, night string) (raDeg, dec float64, usable, isBody bool) {
	g := strings.ToLower(guid)
	res := s.Latest()
	if res == nil || res.guids == nil {
		return 0, 0, false, s.isBodyTarget(g)
	}
	if !res.guids[g] {
		return 0, 0, false, false
	}
	if night == "" {
		return 0, 0, res.hasRows[g], true
	}
	r, ok := res.index[nightKey{g, night}]
	if !ok {
		return 0, 0, false, true
	}
	return r.RA, r.Dec, r.Bright(), true
}

// Compute builds the night tables of every active target with a
// designation.
func (s *NightService) Compute(ctx context.Context) (*NightResult, error) {
	res := &NightResult{}
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
	res.guids = map[string]bool{}
	for _, t := range targets {
		res.guids[strings.ToLower(t.GUID)] = true
	}
	if s.Site == nil || s.Horizons == nil {
		return res, ErrNoSite
	}
	site, err := s.Site(ctx)
	if err != nil {
		return res, fmt.Errorf("%w: %w", ErrNoSite, err)
	}
	res.site = &site
	first := Tonight(site, at)
	res.From, res.To = first.Format(time.DateOnly), first.AddDate(0, 0, SeasonNights).Format(time.DateOnly)
	for _, t := range targets {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		req := SeasonRequest(t, site, first)
		tab, err := s.Horizons.Ephemeris(ctx, req)
		if err == nil {
			if _, perr := s.Horizons.PruneEarlier(ctx, req); perr != nil {
				slog.Warn("Pruning earlier Horizons season tables failed", "designation", t.Designation, "error", perr)
			}
		}
		if err != nil {
			res.Errors = append(res.Errors, TargetError{TargetGUID: t.GUID, Name: t.Name, Designation: t.Designation, Error: err.Error()})
			slog.Warn("Horizons season table failed", "target", t.Name, "designation", t.Designation, "error", err)
			continue
		}
		var el *Elements
		if t.Kind != KindAsteroid && s.Elements != nil {
			if e, ok := s.Elements(ctx, t.Designation); ok {
				el = &e
			}
		}
		res.rows = append(res.rows, SeasonRows(t, site, tab, el, at)...)
	}
	res.Rows = len(res.rows)
	return res, nil
}

// Publish writes the rows into ts_body_night and removes rows of nights
// and targets no longer computed. A target whose fetch failed keeps the
// rows it has of its current designation. A dry run counts what it would
// change.
func (s *NightService) Publish(ctx context.Context, res *NightResult) error {
	if res.Skipped != "" {
		return nil
	}
	has, err := tables.Has(ctx, s.Sched, NightTable)
	if err != nil {
		return err
	}
	if !has {
		res.Skipped = "no " + NightTable + " table"
		return nil
	}
	var have []NightRow
	if err := s.Sched.WithContext(ctx).Table(NightTable).Find(&have).Error; err != nil {
		return fmt.Errorf("load %s: %w", NightTable, err)
	}
	old := make(map[nightKey]NightRow, len(have))
	for _, r := range have {
		old[r.key()] = r
	}
	failed := map[string]string{}
	for _, e := range res.Errors {
		failed[strings.ToLower(e.TargetGUID)] = e.Designation
	}
	want := map[nightKey]bool{}
	var write []NightRow
	for _, r := range res.rows {
		want[r.key()] = true
		if o, ok := old[r.key()]; ok && o.same(r) {
			continue
		}
		res.Changed++
		write = append(write, r)
	}
	var drop []NightRow
	for k, r := range old {
		if want[k] {
			continue
		}
		if des, ok := failed[k.guid]; ok && des == r.Designation && r.Night >= res.keepFrom() {
			continue
		}
		res.Deleted++
		drop = append(drop, r)
	}
	if s.mode() == ModeOn {
		if len(write) > 0 {
			if err := s.Sched.WithContext(ctx).Table(NightTable).Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "target_guid"}, {Name: "night"}},
				DoUpdates: clause.AssignmentColumns([]string{"at", "designation", "ra", "dec", "ra_rate", "dec_rate", "mag", "mag_limit", "computed_at"}),
			}).CreateInBatches(&write, 200).Error; err != nil {
				return fmt.Errorf("publish %s: %w", NightTable, err)
			}
		}
		for _, r := range drop {
			if err := s.Sched.WithContext(ctx).Table(NightTable).Where("target_guid = ? AND night = ?", r.TargetGUID, r.Night).Delete(nil).Error; err != nil {
				return fmt.Errorf("remove %s rows: %w", NightTable, err)
			}
		}
	}
	switch {
	case res.Changed == 0 && res.Deleted == 0:
		res.Skipped = SkipUnchanged
	case s.mode() == ModeOn:
		at := s.now().UTC()
		res.PublishedAt = &at
		slog.Info("Published body season nights", "rows", res.Rows, "changed", res.Changed, "deleted", res.Deleted)
	default:
		slog.Info("Would publish body season nights (dry run)", "rows", res.Rows, "changed", res.Changed, "deleted", res.Deleted)
	}
	return nil
}

func (s *NightService) Refresh(ctx context.Context) (*NightResult, error) {
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

func (s *NightService) targetsKey(ctx context.Context) (string, error) {
	return (&Service{Sched: s.Sched}).targetsKey(ctx)
}

// Run refreshes every interval, and within WatchEvery of a body target
// being added, removed or changed.
func (s *NightService) Run(ctx context.Context, every time.Duration) {
	watch := min(WatchEvery, every)
	var next time.Time
	last, _ := s.targetsKey(ctx)
	key := last
	for {
		if RefreshDue(time.Now(), next, key, last) {
			if key != last {
				slog.Info("Body targets changed; building season nights now")
			}
			if _, err := s.Refresh(ctx); err != nil && ctx.Err() == nil {
				slog.Error("Building body season nights failed", "error", err)
			}
			next = time.Now().Add(every)
			last = key
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(watch):
		}
		k, err := s.targetsKey(ctx)
		if err != nil {
			if ctx.Err() == nil {
				slog.Warn("Checking body targets failed", "error", err)
			}
			continue
		}
		key = k
	}
}

const NightSchema = `create table if not exists ts_body_night (
    target_guid text NOT NULL,
    night text NOT NULL,
    at timestamp NOT NULL,
    designation text NOT NULL,
    ra double precision NOT NULL,
    dec double precision NOT NULL,
    ra_rate double precision NOT NULL,
    dec_rate double precision NOT NULL,
    mag double precision,
    mag_limit double precision,
    computed_at timestamp NOT NULL,
    PRIMARY KEY (target_guid, night)
)`
