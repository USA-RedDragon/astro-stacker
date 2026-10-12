// Package schedauto keeps every Target Scheduler project on the global
// scheduling settings and on its automatic minimum time and minimum
// altitude, through project.edit commands the plugin applies.
package schedauto

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/moonkeep"
	"github.com/USA-RedDragon/astro-stacker/internal/mosaics"
	"github.com/USA-RedDragon/astro-stacker/internal/planning"
	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"github.com/USA-RedDragon/astro-stacker/internal/schedglobal"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/store/tables"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	Author     = "stacker: scheduling"
	ModeOff    = "off"
	ModeDryRun = "dry-run"
	ModeOn     = "on"
	// SyncGrace is how long a project is left alone after an automatic
	// edit, so the home copy of the scheduler database catches up first.
	SyncGrace = 2 * time.Hour
	cacheFor  = time.Hour
)

var (
	ErrNoSite = errors.New("the observatory site is not known, so target windows can't be worked out")
	ErrOff    = errors.New("automatic scheduling settings are off")
)

type SiteFunc func(ctx context.Context) (sky.Site, error)

// HorizonFunc reports the NINA profile's custom horizon, false when the
// observatory scheduler has not reported one.
type HorizonFunc func(ctx context.Context) (mosaics.HorizonReport, bool)

// BodyWindowsFunc is bodies.NightService.Windows.
type BodyWindowsFunc func(guid string, minAlt float64, tonight string) (*float64, *float64, int, bool)

// HorizonStatus says whether projects use the observatory horizon.
type HorizonStatus struct {
	Custom    bool   `json:"custom"`
	ProfileID string `json:"profileId,omitempty"`
	Reason    string `json:"reason"`
}

// RunStatus is the last automatic run.
type RunStatus struct {
	At         time.Time `json:"at"`
	Mode       string    `json:"mode"`
	Projects   int       `json:"projects"`
	Fields     int       `json:"fields"`
	Left       []string  `json:"left"`
	Failed     []string  `json:"failed"`
	CommandIDs []string  `json:"commandIds"`
	Error      string    `json:"error,omitempty"`
}

// Rules are the constants the automatic values follow, for the UI.
type Rules struct {
	MaxSwitchShare        float64 `json:"maxSwitchShare"`
	MaxSwitchGapMinutes   float64 `json:"maxSwitchGapMinutes"`
	MinTimeMax            int     `json:"minTimeMax"`
	MinTimeStep           int     `json:"minTimeStep"`
	TypicalNights         int     `json:"typicalNights"`
	AltitudeFallback      float64 `json:"altitudeFallback"`
	AltitudeWorthBelow    float64 `json:"altitudeWorthBelow"`
	AltitudeReferenceFrom float64 `json:"altitudeReferenceFrom"`
	MinSubs               int     `json:"minSubs"`
	MinNights             int     `json:"minNights"`
}

func rules() Rules {
	return Rules{MaxSwitchShare: MaxSwitchShare, MaxSwitchGapMinutes: MaxSwitchGap.Minutes(), MinTimeMax: MinTimeMax, MinTimeStep: MinTimeStep,
		TypicalNights: TypicalNights, AltitudeFallback: moonkeep.AltitudeFallback, AltitudeWorthBelow: moonkeep.AltitudeWorthBelow,
		AltitudeReferenceFrom: moonkeep.AltitudeReferenceFrom, MinSubs: moonkeep.MinSubs, MinNights: moonkeep.MinNights}
}

// Result is one computation of the global and automatic values.
type Result struct {
	ComputedAt  time.Time                    `json:"computedAt"`
	Mode        string                       `json:"mode"`
	Global      schedglobal.Settings         `json:"global"`
	Defaults    schedglobal.Settings         `json:"defaults"`
	Saved       bool                         `json:"saved"`
	SavedAt     *time.Time                   `json:"savedAt,omitempty"`
	SavedBy     string                       `json:"savedBy,omitempty"`
	Rules       Rules                        `json:"rules"`
	Overhead    Overhead                     `json:"overhead"`
	SwitchFloor int                          `json:"switchFloorMinutes"`
	Floors      []moonkeep.AltitudeFloor     `json:"altitudeFloors"`
	AltReplay   []moonkeep.AltitudeReplayRow `json:"altitudeReplay"`
	FloorsNote  string                       `json:"altitudeFloorsNote,omitempty"`
	Horizon     HorizonStatus                `json:"horizon"`
	Tonight     string                       `json:"tonight"`
	Projects    []ProjectResult              `json:"projects"`
	Differing   int                          `json:"differing"`
	LastRun     *RunStatus                   `json:"lastRun,omitempty"`
}

type Service struct {
	App         *gorm.DB
	Sched       *gorm.DB
	Commands    *schedcmd.Service
	MoonKeep    *moonkeep.Service
	BodyWindows BodyWindowsFunc
	Horizon     HorizonFunc
	Site        SiteFunc
	Mode        string
	Now         func() time.Time

	mu      sync.Mutex
	refresh sync.Mutex
	last    *Result
	run     *RunStatus
	proj    *ProjectionView
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

type lightRow struct {
	Object   string
	Key      string
	Exposure *float64
	DateObs  *time.Time
}

func (s *Service) lights(ctx context.Context, since time.Time) ([]Light, error) {
	var rows []lightRow
	if err := s.App.WithContext(ctx).Table("frames").Select("object, key, exposure, date_obs").
		Where("type = ? AND date_obs IS NOT NULL AND date_obs >= ?", "LIGHT", since).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("load lights: %w", err)
	}
	out := make([]Light, 0, len(rows))
	for _, r := range rows {
		if r.Exposure == nil || r.DateObs == nil || strings.Contains(path.Base(r.Key), "_cal.") {
			continue
		}
		out = append(out, Light{Object: r.Object, Start: r.DateObs.UTC(), Exposure: *r.Exposure})
	}
	return out, nil
}

func (s *Service) floors() ([]moonkeep.AltitudeFloor, []moonkeep.AltitudeReplayRow, string) {
	if s.MoonKeep == nil {
		return nil, nil, fmt.Sprintf("the kept-rate fit is not running, so every project uses %g°", moonkeep.AltitudeFallback)
	}
	res := s.MoonKeep.Latest()
	if res == nil {
		return nil, nil, fmt.Sprintf("no kept-rate fit yet, so every project uses %g°", moonkeep.AltitudeFallback)
	}
	return res.Altitude, res.AltReplay, ""
}

func (s *Service) horizon(ctx context.Context) (*Horizon, HorizonStatus) {
	if s.Horizon == nil {
		return nil, HorizonStatus{Reason: "the stacker does not ask the observatory scheduler for its horizon"}
	}
	r, ok := s.Horizon(ctx)
	switch {
	case !ok:
		return nil, HorizonStatus{Reason: "the observatory scheduler has not reported its horizon yet, so each project keeps its own custom horizon setting"}
	case !r.Custom:
		return &Horizon{ProfileID: r.ProfileID}, HorizonStatus{ProfileID: r.ProfileID, Reason: "the NINA profile has no custom horizon yet, so the minimum altitude is the only limit"}
	case r.Horizon == nil:
		return nil, HorizonStatus{ProfileID: r.ProfileID, Reason: "the observatory scheduler reported a custom horizon the stacker can't read, so each project keeps its own custom horizon setting"}
	}
	return &Horizon{ProfileID: r.ProfileID, Custom: true, Horizon: r.Horizon}, HorizonStatus{Custom: true, ProfileID: r.ProfileID,
		Reason: "the NINA profile has a custom horizon, so every project of that profile uses it as a hard limit with no offset"}
}

func (s *Service) events(ctx context.Context) (map[string]bool, error) {
	out := map[string]bool{}
	if ok, err := tables.Has(ctx, s.App, &app.SkyEventChoice{}); err != nil || !ok {
		return out, err
	}
	var guids []string
	if err := s.App.WithContext(ctx).Model(&app.SkyEventChoice{}).Where("project_guid <> ''").Pluck("project_guid", &guids).Error; err != nil {
		return nil, fmt.Errorf("load sky event projects: %w", err)
	}
	for _, g := range guids {
		out[strings.ToLower(g)] = true
	}
	return out, nil
}

// Compute works out the global and automatic values of every project.
func (s *Service) Compute(ctx context.Context) (*Result, error) {
	now := s.now().UTC()
	saved, err := schedglobal.Load(ctx, s.App)
	if err != nil {
		return nil, fmt.Errorf("load the global scheduling settings: %w", err)
	}
	res := &Result{ComputedAt: now, Mode: s.mode(), Global: saved.Current(), Defaults: schedglobal.Defaults(), Saved: saved.Settings != nil,
		SavedAt: saved.At, SavedBy: saved.By, Rules: rules(), Projects: []ProjectResult{}}
	if s.Site == nil {
		return nil, ErrNoSite
	}
	site, err := s.Site(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNoSite, err)
	}
	lights, err := s.lights(ctx, now.Add(-OverheadNights*24*time.Hour))
	if err != nil {
		return nil, err
	}
	res.Overhead = MeasureOverhead(lights, site.Longitude, now)
	res.SwitchFloor = SwitchFloor(res.Overhead.MeanMinutes)
	res.Floors, res.AltReplay, res.FloorsNote = s.floors()
	hz, status := s.horizon(ctx)
	res.Horizon = status
	nights := NightsFrom(site, now, TypicalNights)
	res.Tonight = nights.First
	if s.Sched == nil {
		return res, nil
	}
	if ok, err := tables.Has(ctx, s.Sched, "project"); err != nil || !ok {
		return res, err
	}
	pin, err := planning.AppInputs(ctx, s.App, s.Sched)
	if err != nil {
		return nil, err
	}
	snap, err := planning.Load(ctx, s.Sched, s.App, pin)
	if err != nil {
		return nil, err
	}
	events, err := s.events(ctx)
	if err != nil {
		return nil, err
	}
	in := Inputs{Global: res.Global, Floors: res.Floors, SwitchFloor: res.SwitchFloor, Nights: nights, Horizon: hz, Events: events}
	if s.BodyWindows != nil {
		in.BodyWindows = func(guid string, minAlt float64) (Window, bool) {
			t, typ, n, ok := s.BodyWindows(guid, minAlt, nights.First)
			return Window{Tonight: t, Typical: typ, Nights: n}, ok
		}
	}
	for _, p := range snap.Projects {
		r := Plan(p, in)
		if len(r.Changes) > 0 {
			res.Differing++
		}
		res.Projects = append(res.Projects, r)
	}
	return res, nil
}

// Current is the latest computation, made again when it is over an hour
// old.
func (s *Service) Current(ctx context.Context) (*Result, error) {
	s.mu.Lock()
	last := s.last
	s.mu.Unlock()
	if last != nil && s.now().Sub(last.ComputedAt) < cacheFor {
		return s.withRun(last), nil
	}
	return s.Refresh(ctx)
}

// Refresh computes again.
func (s *Service) Refresh(ctx context.Context) (*Result, error) {
	s.refresh.Lock()
	defer s.refresh.Unlock()
	res, err := s.Compute(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.last = res
	s.mu.Unlock()
	return s.withRun(res), nil
}

func (s *Service) withRun(r *Result) *Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *r
	cp.LastRun = s.run
	return &cp
}

func editedProjects(r schedcmd.Record) []int64 {
	if r.Kind == schedcmd.KindProjectEdit {
		var p schedcmd.EditPayload
		if json.Unmarshal(r.Payload, &p) == nil {
			return []int64{p.ID}
		}
		return nil
	}
	if r.Kind != schedcmd.KindProjectBatchEdit {
		return nil
	}
	var p schedcmd.TemplateBatchPayload
	if json.Unmarshal(r.Payload, &p) != nil {
		return nil
	}
	out := make([]int64, 0, len(p.Items))
	for _, it := range p.Items {
		out = append(out, it.ID)
	}
	return out
}

// busy is the projects with a project edit still waiting for the plugin,
// or with a project edit by anyone in the last SyncGrace, so the home copy
// of the scheduler database has caught up before the next edit.
func (s *Service) busy(ctx context.Context) (map[int64]bool, error) {
	out := map[int64]bool{}
	waiting, err := s.Commands.Log.Waiting(ctx)
	if err != nil {
		return nil, fmt.Errorf("list waiting commands: %w", err)
	}
	recent, err := s.Commands.Log.List(ctx, schedcmd.Filter{Since: s.now().Add(-SyncGrace), Limit: schedcmd.MaxListLimit})
	if err != nil {
		return nil, fmt.Errorf("list recent project edits: %w", err)
	}
	for _, r := range append(waiting, recent...) {
		for _, id := range editedProjects(r) {
			out[id] = true
		}
	}
	return out, nil
}

// Edits are the project.edit payloads for every differing project not in
// skip, and the names of those left for later. A project without a GUID is
// left, since its original values could not be kept.
func Edits(projects []ProjectResult, skip map[int64]bool) ([]schedcmd.EditPayload, []string) {
	var out []schedcmd.EditPayload
	var left []string
	for _, p := range projects {
		e := p.Edit()
		if e == nil {
			continue
		}
		if skip[e.ID] || p.GUID == "" {
			left = append(left, p.Name)
			continue
		}
		out = append(out, *e)
	}
	return out, left
}

func (s *Service) keepOriginals(ctx context.Context, projects []ProjectResult, ids []int64) error {
	if ok, err := tables.Has(ctx, s.App, &app.SchedulingOriginal{}); err != nil || !ok {
		return err
	}
	now := s.now().UTC()
	for _, p := range projects {
		if !slices.Contains(ids, int64(p.ID)) || p.GUID == "" {
			continue
		}
		v, err := json.Marshal(p.Current)
		if err != nil {
			return err
		}
		row := app.SchedulingOriginal{ProjectGUID: p.GUID, ProjectID: p.ID, Name: p.Name, Values: string(v), SavedAt: now}
		if err := s.App.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) setRun(r RunStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.run = &r
}

// Once computes and, when the mode is on, sends one project.edit for every
// differing project, so one project that fails does not hold up the
// others. Dry-run only logs what it would send.
func (s *Service) Once(ctx context.Context) (*Result, error) {
	mode := s.mode()
	if mode == ModeOff {
		return nil, ErrOff
	}
	res, err := s.Refresh(ctx)
	if err != nil {
		s.setRun(RunStatus{At: s.now().UTC(), Mode: mode, Error: err.Error()})
		return nil, err
	}
	skip := map[int64]bool{}
	if s.Commands != nil {
		if skip, err = s.busy(ctx); err != nil {
			s.setRun(RunStatus{At: s.now().UTC(), Mode: mode, Error: err.Error()})
			return res, err
		}
	}
	edits, left := Edits(res.Projects, skip)
	run := RunStatus{At: s.now().UTC(), Mode: mode, Left: left, Failed: []string{}, CommandIDs: []string{}}
	for _, p := range res.Projects {
		if p.Edit() != nil {
			slog.Info("Project differs from the global scheduling settings", "mode", mode, "project", p.Name, "fields", len(p.Changes), "summary", p.Summary())
		}
	}
	if mode != ModeOn || s.Commands == nil {
		for _, e := range edits {
			run.Projects++
			run.Fields += len(e.Changes)
		}
		s.setRun(run)
		return res, nil
	}
	for _, e := range edits {
		if err := s.send(ctx, res.Projects, e, &run); err != nil {
			run.Failed = append(run.Failed, e.Name+": "+err.Error())
			slog.Warn("Could not send the automatic scheduling edit", "project", e.Name, "error", err)
		}
	}
	s.setRun(run)
	return res, nil
}

func (s *Service) send(ctx context.Context, projects []ProjectResult, e schedcmd.EditPayload, run *RunStatus) error {
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if err := s.keepOriginals(ctx, projects, []int64{e.ID}); err != nil {
		return fmt.Errorf("keeping the original values failed, so nothing was sent: %w", err)
	}
	rec, err := s.Commands.Submit(ctx, schedcmd.KindProjectEdit, raw, Author)
	if err != nil {
		return err
	}
	run.Projects++
	run.Fields += len(e.Changes)
	run.CommandIDs = append(run.CommandIDs, rec.ID)
	return nil
}

// Run computes every interval and, when on, keeps the projects equal.
func (s *Service) Run(ctx context.Context, every time.Duration) {
	t := time.NewTimer(10 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if _, err := s.Once(ctx); err != nil && !errors.Is(err, ErrOff) && ctx.Err() == nil {
			slog.Warn("Keeping projects on the global scheduling settings failed", "error", err)
		}
		t.Reset(every)
	}
}

// Originals lists the projects' own values from before the stacker first
// changed them.
func (s *Service) Originals(ctx context.Context) ([]app.SchedulingOriginal, error) {
	out := []app.SchedulingOriginal{}
	if ok, err := tables.Has(ctx, s.App, &app.SchedulingOriginal{}); err != nil || !ok {
		return out, err
	}
	err := s.App.WithContext(ctx).Order("name").Find(&out).Error
	return out, err
}
