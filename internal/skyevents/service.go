package skyevents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/bodies"
	"github.com/USA-RedDragon/astro-stacker/internal/catalog"
	"github.com/USA-RedDragon/astro-stacker/internal/ephem"
	"github.com/USA-RedDragon/astro-stacker/internal/goals"
	"github.com/USA-RedDragon/astro-stacker/internal/horizons"
	"github.com/USA-RedDragon/astro-stacker/internal/planning"
	"github.com/USA-RedDragon/astro-stacker/internal/rigsource"
	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/store/tables"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	StatusReady       = "ready"
	StatusPending     = "pending"
	StatusUnavailable = "unavailable"

	// MonthsBack is how far before today the search starts, so an event
	// already under way keeps its first night and its id.
	MonthsBack = 3
	// MonthsAhead is how far ahead the search looks; the API shows up to it.
	MonthsAhead   = 24
	DefaultMonths = 24
	Interval      = 24 * time.Hour
	// WindowTable is the plugin's table of event windows; planning waits
	// for it.
	WindowTable = "ts_target_window"
	planFilter  = "L"
)

var (
	ErrNotFound       = errors.New("no such event")
	ErrNotReady       = errors.New("the event search has not run yet")
	ErrCannotPlan     = errors.New("events cannot be planned yet")
	ErrAlreadyPlanned = errors.New("the event is already planned")
	ErrNotPlanned     = errors.New("the event is not planned")
	ErrOver           = errors.New("the event is over")
)

type BodyInfo struct {
	Name   string `json:"name"`
	Kind   Kind   `json:"kind"`
	Source string `json:"source"`
}

type BodyError struct {
	Name  string `json:"name"`
	Error string `json:"error"`
}

type FrameInfo struct {
	Width  float64 `json:"width_deg"`
	Height float64 `json:"height_deg"`
	Basis  string  `json:"basis"`
}

type Planning struct {
	Available bool    `json:"available"`
	Reason    *string `json:"reason"`
}

// Result is one search, as cached.
type Result struct {
	Status     string      `json:"status"`
	Reason     *string     `json:"reason"`
	ComputedAt *time.Time  `json:"computed_at"`
	From       *time.Time  `json:"from"`
	To         *time.Time  `json:"to"`
	Criteria   *Criteria   `json:"criteria"`
	Frame      *FrameInfo  `json:"frame"`
	Bodies     []BodyInfo  `json:"bodies"`
	BodyErrors []BodyError `json:"body_errors"`
	Events     []Event     `json:"events"`
}

// View is what the API serves.
type View struct {
	Result
	Months   int      `json:"months"`
	Planning Planning `json:"planning"`
}

type SiteFunc func(ctx context.Context) (sky.Site, error)

type RigFunc func(ctx context.Context) (rigsource.Rig, error)

type Service struct {
	App      *gorm.DB
	Sched    *gorm.DB
	Horizons *horizons.Client
	Site     SiteFunc
	Rig      RigFunc
	Catalog  func() (*catalog.Index, error)
	Commands *schedcmd.Service
	Now      func() time.Time
	// Snapshot loads what drafting a project needs, for the plans the
	// stacker makes by itself.
	Snapshot func(ctx context.Context) (*planning.Snapshot, error)
	// Orbits gives a comet's or asteroid's orbit, for the rarity of its
	// events.
	Orbits func(ctx context.Context, designation, kind string) (bodies.Orbit, error)

	refresh    sync.Mutex
	planMu     sync.Mutex
	backfilled bool
	mu         sync.Mutex
	last       *Result
	tables     map[string]Table
	site       sky.Site
	eph        *ephem.Ephemeris
	ephErr     error
	ephOnce    sync.Once
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) ephemeris() (*ephem.Ephemeris, error) {
	s.ephOnce.Do(func() { s.eph, s.ephErr = ephem.Load() })
	return s.eph, s.ephErr
}

func monthStart(t time.Time, months int) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, months, 0)
}

func unavailable(reason string) *Result {
	return &Result{Status: StatusUnavailable, Reason: &reason}
}

func (s *Service) frame(ctx context.Context) (*FrameInfo, error) {
	if s.Rig == nil {
		return nil, rigsource.ErrUnknown
	}
	rig, err := s.Rig(ctx)
	if err != nil {
		return nil, err
	}
	if rig.WidthDeg == nil || rig.HeightDeg == nil || *rig.WidthDeg <= 0 || *rig.HeightDeg <= 0 {
		return nil, rigsource.ErrUnknown
	}
	w, h := math.Max(*rig.WidthDeg, *rig.HeightDeg), math.Min(*rig.WidthDeg, *rig.HeightDeg)
	basis := fmt.Sprintf("Measured camera field %.2f° × %.2f°", w, h)
	if rig.Scale != nil {
		basis += fmt.Sprintf(" at %.2f″/px", *rig.Scale)
	}
	if rig.Basis.Source != "" {
		basis += " (" + rig.Basis.Source + ")"
	}
	return &FrameInfo{Width: round(w, 3), Height: round(h, 3), Basis: basis + "."}, nil
}

// MeasuredFrame is the measured camera field, long side first.
func (s *Service) MeasuredFrame(ctx context.Context) (*FrameInfo, error) {
	return s.frame(ctx)
}

func (s *Service) siteOrReason(ctx context.Context) (sky.Site, string) {
	if s.Site == nil {
		return sky.Site{}, "The observatory site is not known."
	}
	site, err := s.Site(ctx)
	if err != nil {
		return site, "The observatory site is not known: " + err.Error()
	}
	return site, ""
}

func (s *Service) frameOrReason(ctx context.Context) (*FrameInfo, string) {
	fi, err := s.frame(ctx)
	if err != nil {
		return nil, "The camera field is not measured yet, so no frame can be fitted: " + err.Error()
	}
	return fi, ""
}

// Compute runs the search from MonthsBack before now to MonthsAhead after.
func (s *Service) Compute(ctx context.Context) (*Result, error) {
	at := s.now().UTC()
	e, err := s.ephemeris()
	if err != nil {
		return nil, fmt.Errorf("load the ephemeris: %w", err)
	}
	site, reason := s.siteOrReason(ctx)
	if reason != "" {
		return unavailable(reason), nil
	}
	fi, reason := s.frameOrReason(ctx)
	if reason != "" {
		return unavailable(reason), nil
	}
	_, last := e.Range()
	if !e.Covers(at) {
		return unavailable("The embedded ephemeris ends " + last.Format(time.DateOnly) + "; the stacker needs a newer slice."), nil
	}
	from, to := monthStart(at, -MonthsBack), monthStart(at, MonthsAhead+1)
	if to.After(last.Add(-48 * time.Hour)) {
		to = last.Add(-48 * time.Hour)
	}
	c := NewCriteria(fi.Width, fi.Height)
	g, err := NewGrid(e, site, from, to, c)
	if err != nil {
		return nil, err
	}
	res := &Result{Status: StatusReady, ComputedAt: &at, From: &from, To: &to, Criteria: &c, Frame: fi, Bodies: []BodyInfo{}, BodyErrors: []BodyError{}}
	var tracks []Track
	for _, b := range append([]ephem.Body{ephem.Moon}, ephem.Planets()...) {
		tr, err := EphemerisTrack(e, b, g)
		if err != nil {
			return nil, err
		}
		tracks = append(tracks, tr)
		res.Bodies = append(res.Bodies, BodyInfo{Name: tr.Name, Kind: tr.Kind, Source: tr.Source})
	}
	tabs := map[string]Table{}
	designated, err := bodies.Targets(ctx, s.Sched)
	if err != nil {
		res.BodyErrors = append(res.BodyErrors, BodyError{Name: "designated bodies", Error: err.Error()})
	}
	seen := map[string]bool{}
	var orbits []bodies.Orbit
	for _, t := range designated {
		key := strings.ToLower(t.Designation)
		if !t.Active || seen[key] || s.Horizons == nil {
			continue
		}
		seen[key] = true
		tab, err := HorizonsTable(ctx, s.Horizons, site, t.Designation, t.Kind, from, to)
		if err != nil {
			res.BodyErrors = append(res.BodyErrors, BodyError{Name: t.Designation, Error: err.Error()})
			continue
		}
		tabs[key] = tab
		if s.Orbits != nil {
			o, err := s.Orbits(ctx, t.Designation, t.Kind)
			if err != nil {
				res.BodyErrors = append(res.BodyErrors, BodyError{Name: t.Designation + " orbit", Error: err.Error()})
			} else {
				orbits = append(orbits, o)
			}
		}
		tr := TableTrack(t.Designation, t.Designation, t.Kind, tab, g)
		tracks = append(tracks, tr)
		res.Bodies = append(res.Bodies, BodyInfo{Name: tr.Name, Kind: tr.Kind, Source: tr.Source})
	}
	var dsos []DSO
	if s.Catalog != nil {
		ix, err := s.Catalog()
		if err != nil {
			return nil, fmt.Errorf("load the catalogue: %w", err)
		}
		dsos = EventDSOs(ix.All())
	}
	res.Events = Search(g, tracks, dsos, c)
	Rarities(e, site, c, res.Events, dsos, s.cachedRarities(), orbits)
	res.Events = WorthAMosaic(res.Events)
	s.mu.Lock()
	s.tables, s.site = tabs, site
	s.mu.Unlock()
	return res, nil
}

func (s *Service) cachedRarities() map[string]*Rarity {
	out := map[string]*Rarity{}
	if res := s.Latest(); res != nil {
		for _, e := range res.Events {
			if e.Rarity != nil && e.Rarity.Status == StatusReady {
				out[e.ID] = e.Rarity
			}
		}
	}
	return out
}

func (s *Service) Latest() *Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

func (s *Service) keep(res *Result) {
	s.mu.Lock()
	s.last = res
	s.mu.Unlock()
}

// Load restores the last search from the app database.
func (s *Service) Load(ctx context.Context) error {
	if s.App == nil {
		return nil
	}
	var row app.SkyEventRun
	if err := s.App.WithContext(ctx).Limit(1).Find(&row, 1).Error; err != nil {
		return err
	}
	if row.ID == 0 {
		return nil
	}
	var res Result
	if err := json.Unmarshal([]byte(row.Result), &res); err != nil {
		return err
	}
	s.keep(&res)
	return nil
}

func (s *Service) save(ctx context.Context, res *Result) error {
	if s.App == nil || res.ComputedAt == nil {
		return nil
	}
	b, err := json.Marshal(res)
	if err != nil {
		return err
	}
	row := app.SkyEventRun{ID: 1, ComputedAt: *res.ComputedAt, Result: string(b)}
	return s.App.WithContext(ctx).Clauses(clause.OnConflict{UpdateAll: true}).Create(&row).Error
}

func (s *Service) Refresh(ctx context.Context) (*Result, error) {
	s.refresh.Lock()
	defer s.refresh.Unlock()
	res, err := s.Compute(ctx)
	if err != nil {
		return nil, err
	}
	s.keep(res)
	if err := s.save(ctx, res); err != nil {
		slog.Warn("Saving the sky events failed", "error", err)
	}
	if res.Status == StatusReady {
		if err := s.PublishCentres(ctx); err != nil {
			slog.Warn("Publishing event frame centres failed", "error", err)
		}
		slog.Info("Sky events computed", "events", len(res.Events), "bodies", len(res.Bodies), "body_errors", len(res.BodyErrors))
	}
	return res, nil
}

// Run searches at start and then once a day, and plans rare events after
// each search and every AutoPlanEvery, so they are planned soon after the
// scheduler can take event windows.
func (s *Service) Run(ctx context.Context, every time.Duration) {
	if err := s.Load(ctx); err != nil {
		slog.Warn("Loading the saved sky events failed", "error", err)
	}
	search := time.NewTimer(0)
	defer search.Stop()
	auto := time.NewTicker(AutoPlanEvery)
	defer auto.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-search.C:
			if _, err := s.Refresh(ctx); err != nil && ctx.Err() == nil {
				slog.Error("Sky event search failed", "error", err)
			}
			search.Reset(every)
		case <-auto.C:
		}
		if err := s.AutoPlanRare(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("Planning rare sky events failed", "error", err)
		}
	}
}

func (s *Service) choices(ctx context.Context) ([]app.SkyEventChoice, error) {
	if s.App == nil {
		return nil, nil
	}
	has, err := tables.Has(ctx, s.App, &app.SkyEventChoice{})
	if err != nil || !has {
		return nil, err
	}
	var out []app.SkyEventChoice
	return out, s.App.WithContext(ctx).Order("event_id").Find(&out).Error
}

func (s *Service) Planning(ctx context.Context) Planning {
	no := func(r string) Planning { return Planning{Reason: &r} }
	if s.Commands == nil {
		return no("Scheduler commands are not configured.")
	}
	if s.Sched == nil {
		return no("The stacker has no scheduler database.")
	}
	has, err := tables.Has(ctx, s.Sched, WindowTable)
	if err != nil {
		return no("Checking the scheduler for event windows failed: " + err.Error())
	}
	if !has {
		return no("The scheduler plugin with event windows is not installed yet (no " + WindowTable + " table in the scheduler database).")
	}
	return Planning{Available: true}
}

func memberKeys(e Event) (string, []string) {
	var b, o []string
	for _, m := range e.Members {
		if m.Kind == KindDSO {
			o = append(o, m.ID)
		} else {
			b = append(b, m.ID)
		}
	}
	sort.Strings(b)
	sort.Strings(o)
	return strings.Join(b, ","), o
}

// matches ties a stored choice to an event of a later search: the same id,
// or the same bodies, a shared object (or none on either side), the same
// framing (one frame or a mosaic) and an overlapping window.
func matches(c app.SkyEventChoice, e Event) bool {
	if c.EventID == e.ID {
		return true
	}
	if strings.HasPrefix(c.EventID, mosaicIDPrefix+"-") != (e.Kind == EventMosaic2) {
		return false
	}
	b, o := memberKeys(e)
	if c.Bodies != b || c.WindowStart == nil || c.WindowEnd == nil {
		return false
	}
	if !c.WindowStart.Before(e.End) || !e.Start.Before(*c.WindowEnd) {
		return false
	}
	have := strings.Split(c.Objects, ",")
	if c.Objects == "" || len(o) == 0 {
		return c.Objects == "" && len(o) == 0
	}
	return slices.ContainsFunc(o, func(x string) bool { return slices.Contains(have, x) })
}

func (s *Service) planState(ctx context.Context, c app.SkyEventChoice) *PlanState {
	if c.CommandID == "" || c.PlannedAt == nil {
		return nil
	}
	p := &PlanState{State: string(schedcmd.StatusQueued), Priority: c.Priority, ProjectGUID: c.ProjectGUID, TargetGUID: c.TargetGUID, CommandID: c.CommandID, PlannedAt: *c.PlannedAt,
		Automatic: c.AutoCommandID != nil && *c.AutoCommandID == c.CommandID}
	if s.Commands == nil || s.Commands.Log == nil {
		return p
	}
	r, err := s.Commands.Log.Get(ctx, c.CommandID)
	if err != nil {
		msg := "the command record is gone: " + err.Error()
		p.Message = &msg
		return p
	}
	p.State = string(r.Status)
	if r.UndoneBy != "" {
		p.State = StateUndone
	}
	if r.Message != "" {
		m := r.Message
		p.Message = &m
	}
	return p
}

func activePlan(p *PlanState) bool {
	if p == nil {
		return false
	}
	switch schedcmd.Status(p.State) {
	case schedcmd.StatusQueued, schedcmd.StatusPending, schedcmd.StatusApplied, schedcmd.StatusSaved:
		return true
	case schedcmd.StatusConflict, schedcmd.StatusRejected, schedcmd.StatusFailed, schedcmd.StatusCancelled:
		return false
	}
	return false
}

// View is the latest search for the next months, with the user's choices.
func (s *Service) View(ctx context.Context, months int) (View, error) {
	if months <= 0 {
		months = DefaultMonths
	}
	months = min(months, MonthsAhead)
	v := View{Months: months, Planning: s.Planning(ctx)}
	res := s.Latest()
	if res == nil {
		r := "The first search has not finished yet."
		v.Result = Result{Status: StatusPending, Reason: &r, Bodies: []BodyInfo{}, BodyErrors: []BodyError{}, Events: []Event{}}
		return v, nil
	}
	v.Result = *res
	v.Bodies, v.BodyErrors = nonNil(v.Bodies), nonNil(v.BodyErrors)
	now := s.now()
	horizon := now.AddDate(0, months, 0)
	cs, err := s.choices(ctx)
	if err != nil {
		return v, err
	}
	phot := s.photometry(ctx, planFilter)
	v.Events = []Event{}
	for _, e := range res.Events {
		if !e.End.After(now) || !e.Start.Before(horizon) {
			continue
		}
		s.decorate(ctx, &e, cs, phot, v.Planning)
		v.Events = append(v.Events, e)
	}
	return v, nil
}

func nonNil[T any](v []T) []T {
	if v == nil {
		return []T{}
	}
	return v
}

func (s *Service) decorate(ctx context.Context, e *Event, cs []app.SkyEventChoice, phot Photometry, pl Planning) {
	e.Exposure = EstimateExposure(*e, phot)
	c, found := matchChoice(cs, *e)
	if found {
		if c.Priority == PriorityHigh {
			e.Priority = PriorityHigh
		}
		e.Plan = s.planState(ctx, c)
	}
	var cp *app.SkyEventChoice
	if found {
		cp = &c
	}
	e.AutoPlan = autoState(*e, cp, pl, s.now())
}

func matchChoice(cs []app.SkyEventChoice, e Event) (app.SkyEventChoice, bool) {
	for _, c := range cs {
		if matches(c, e) {
			return c, true
		}
	}
	return app.SkyEventChoice{}, false
}

func (s *Service) find(id string) (Event, error) {
	res := s.Latest()
	if res == nil || res.Status != StatusReady {
		return Event{}, ErrNotReady
	}
	for _, e := range res.Events {
		if e.ID == id {
			return e, nil
		}
	}
	return Event{}, ErrNotFound
}

func (s *Service) choiceFor(ctx context.Context, e Event) (app.SkyEventChoice, error) {
	cs, err := s.choices(ctx)
	if err != nil {
		return app.SkyEventChoice{}, err
	}
	if c, ok := matchChoice(cs, e); ok {
		return c, nil
	}
	return newChoice(e), nil
}

func newChoice(e Event) app.SkyEventChoice {
	b, o := memberKeys(e)
	return app.SkyEventChoice{EventID: e.ID, Bodies: b, Objects: strings.Join(o, ","), Priority: PriorityNormal, WindowStart: &e.Start, WindowEnd: &e.End}
}

// Event is one event of the latest search with the user's choices.
func (s *Service) Event(ctx context.Context, id string) (Event, error) {
	e, err := s.find(id)
	if err != nil {
		return e, err
	}
	cs, err := s.choices(ctx)
	if err != nil {
		return e, err
	}
	s.decorate(ctx, &e, cs, s.photometry(ctx, planFilter), s.Planning(ctx))
	return e, nil
}

// photometry gathers what the exposure estimate needs for a filter.
func (s *Service) photometry(ctx context.Context, filter string) Photometry {
	p := Photometry{Filter: filter}
	if s.Rig != nil {
		if rig, err := s.Rig(ctx); err == nil {
			p.Scale, p.HFR = rig.Scale, rig.TypicalHFR
			if v, ok := rig.Exposures[rigsource.CanonicalFilter(filter)]; ok && v > 0 {
				n := v
				p.Normal, p.NormalSrc = &n, "the typical "+filter+" sub length of recent lights"
			}
		}
	}
	if s.App == nil {
		return p
	}
	var zps []struct {
		Filter    string
		ZeroPoint float64
	}
	if err := s.App.WithContext(ctx).Model(&app.GoalMeasurement{}).Select("filter, zero_point").
		Where("zero_point IS NOT NULL AND depth_system = ? AND zero_point_stars >= ?", goals.SystemGaiaG, goals.MinZeroPointStars).
		Scan(&zps).Error; err != nil {
		return p
	}
	var vals []float64
	for _, z := range zps {
		if rigsource.CanonicalFilter(z.Filter) == rigsource.CanonicalFilter(filter) && !math.IsNaN(z.ZeroPoint) {
			vals = append(vals, z.ZeroPoint)
		}
	}
	if len(vals) > 0 {
		sort.Float64s(vals)
		m := vals[len(vals)/2]
		if len(vals)%2 == 0 {
			m = (vals[len(vals)/2-1] + vals[len(vals)/2]) / 2
		}
		p.ZeroPoint, p.ZPSamples = &m, len(vals)
	}
	return p
}

// WorthAMosaic drops 2-panel mosaic events of fewer than three bodies that
// are not rare; deep-sky objects do not count, and one-frame events all
// stay.
func WorthAMosaic(events []Event) []Event {
	return slices.DeleteFunc(events, func(e Event) bool {
		bodies := 0
		for _, m := range e.Members {
			if m.Kind != KindDSO {
				bodies++
			}
		}
		return e.Kind == EventMosaic2 && bodies < 3 && !e.IsRare()
	})
}
