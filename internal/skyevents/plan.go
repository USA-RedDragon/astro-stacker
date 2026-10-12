package skyevents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/bodies"
	"github.com/USA-RedDragon/astro-stacker/internal/ephem"
	"github.com/USA-RedDragon/astro-stacker/internal/planning"
	"github.com/USA-RedDragon/astro-stacker/internal/rigsource"
	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/store/tables"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	// PlanBudget caps the imaging time the plans ask for; the window ends
	// the target anyway.
	PlanBudget = 10 * time.Hour
	// SubOverhead is the per-sub time the counts allow for download.
	SubOverhead   = 3 * time.Second
	MaxDesired    = 1000
	MinimumTime   = 10
	CentreStep    = 10 * time.Minute
	CentrePad     = 30 * time.Minute
	projectPrefix = "Event: "
)

func validPriority(p string) bool { return p == PriorityNormal || p == PriorityHigh }

func hasMoon(e Event) bool {
	return slices.ContainsFunc(e.Members, func(m Member) bool { return m.Kind == KindMoon })
}

func desired(budget time.Duration, plans int, exposure float64) int {
	per := budget.Seconds() / float64(plans)
	n := int(math.Ceil(per / (exposure + SubOverhead.Seconds())))
	return max(1, min(n, MaxDesired))
}

// pickTemplate is the luminance template the most plans use, or failing
// that the most used broadband one.
func pickTemplate(snap *planning.Snapshot) (planning.Template, bool) {
	var best planning.Template
	found := false
	rank := func(t planning.Template) int {
		switch rigsource.CanonicalFilter(t.Filter) {
		case "L":
			return 2
		case "R", "G", "B":
			return 1
		}
		return 0
	}
	for _, t := range snap.Templates {
		r := rank(t)
		if r == 0 {
			continue
		}
		if !found || r > rank(best) || (r == rank(best) && t.UsedByPlans > best.UsedByPlans) {
			best, found = t, true
		}
	}
	return best, found
}

func (s *Service) projectName(snap *planning.Snapshot, e Event) string {
	name := projectPrefix + e.Title + " (" + e.FirstNight + ")"
	for _, p := range snap.Projects {
		if strings.EqualFold(p.Name, name) {
			return projectPrefix + e.Title + " (" + e.ID + ")"
		}
	}
	return name
}

// Draft is the project.create "Plan it" sends: one target at the frame
// centre of the best moment (or following the published centre for two
// bodies), limited to the event window, with a short set for the brightest
// body and a normal set for the deep-sky objects.
func (s *Service) Draft(ctx context.Context, snap *planning.Snapshot, e Event, priority string, newID func() string) (*schedcmd.ProjectCreatePayload, error) {
	t, ok := pickTemplate(snap)
	if !ok {
		return nil, errors.New("the observatory has no luminance or broadband exposure template")
	}
	phot := s.photometry(ctx, t.Filter)
	if phot.Normal == nil && t.DefaultExposure > 0 {
		n := t.DefaultExposure
		phot.Normal, phot.NormalSrc = &n, "the "+t.Name+" template's default"
	}
	exp := EstimateExposure(e, phot)
	var lengths []float64
	if exp.ShortSeconds != nil {
		lengths = append(lengths, *exp.ShortSeconds)
	}
	if exp.NormalSeconds != nil {
		lengths = append(lengths, *exp.NormalSeconds)
	}
	if len(lengths) == 0 {
		return nil, errors.New("no exposure length is known for this event: " + exp.Basis)
	}
	settings, err := snap.SettingsFor(t.ID, DefaultMinAlt)
	if err != nil {
		return nil, err
	}
	budget := min(time.Duration(e.Hours*float64(time.Hour)), PlanBudget)
	nt := schedcmd.NewTarget{GUID: newID(), Name: e.Title, RAHours: e.Frame.CenterRA / 15, Dec: e.Frame.CenterDec, Rotation: e.Frame.Rotation,
		Window: &schedcmd.EventWindow{Start: e.Start, End: e.End, Priority: priority, EventID: e.ID, IgnoreMoon: hasMoon(e)}}
	if e.MovingCenter {
		nt.Designation, nt.BodyKind = bodies.EventPrefix+e.ID, bodies.KindEvent
	}
	for _, l := range lengths {
		nt.Plans = append(nt.Plans, schedcmd.NewPlan{GUID: newID(), TemplateID: int64(t.ID), TemplateName: t.Name, Exposure: l, Desired: desired(budget, len(lengths), l)})
	}
	prio := 1
	if priority == PriorityHigh {
		prio = 2
	}
	desc := fmt.Sprintf("Sky event %s: %s. Closest %s UTC at %.2f°. %s", e.ID, e.Title, e.Best.At.UTC().Format("2006-01-02 15:04"), e.Best.Separation, exp.Basis)
	return &schedcmd.ProjectCreatePayload{
		Project: schedcmd.NewProject{GUID: newID(), Name: s.projectName(snap, e), Description: desc, Priority: prio, State: 1,
			MinimumTime: MinimumTime, MinimumAltitude: DefaultMinAlt, ProjectSettings: settings},
		Targets:     []schedcmd.NewTarget{nt},
		RuleWeights: planning.WeightsFor(snap.GlobalWeights.Weights, false),
	}, nil
}

// choiceColumns are the columns a saved choice overwrites; the automatic
// planning columns change only through their own guarded updates.
func choiceColumns() []string {
	return []string{"bodies", "objects", "priority", "project_guid", "target_guid", "target_name", "command_id", "window_command",
		"designation", "moving", "ignore_moon", colWindowStart, colWindowEnd, "planned_at", "updated_at"}
}

// saveChoice stores the choice under its event id, replacing the row it was
// read from when an earlier search gave the event another id.
func (s *Service) saveChoice(ctx context.Context, c app.SkyEventChoice, readAs string) error {
	c.UpdatedAt = s.now().UTC()
	return s.App.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if readAs != "" && readAs != c.EventID {
			var old app.SkyEventChoice
			if err := tx.Limit(1).Find(&old, "event_id = ?", readAs).Error; err != nil {
				return err
			}
			if old.EventID != "" {
				c.AutoPlannedAt, c.AutoCommandID, c.AutoDeclinedAt, c.AutoError = old.AutoPlannedAt, old.AutoCommandID, old.AutoDeclinedAt, old.AutoError
			}
			if err := tx.Delete(&app.SkyEventChoice{}, "event_id = ?", readAs).Error; err != nil {
				return err
			}
		}
		return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "event_id"}}, DoUpdates: clause.AssignmentColumns(choiceColumns())}).Create(&c).Error
	})
}

// Plan sends the project.create for an event and remembers it.
func (s *Service) Plan(ctx context.Context, snap *planning.Snapshot, id, priority, author string) (Event, schedcmd.Record, error) {
	var rec schedcmd.Record
	if !validPriority(priority) {
		return Event{}, rec, fmt.Errorf("%w: priority %q", schedcmd.ErrInvalid, priority)
	}
	s.planMu.Lock()
	defer s.planMu.Unlock()
	e, err := s.Event(ctx, id)
	if err != nil {
		return e, rec, err
	}
	c, err := s.choiceFor(ctx, e)
	if err != nil {
		return e, rec, err
	}
	if activePlan(e.Plan) {
		return e, rec, ErrAlreadyPlanned
	}
	if err := s.holdForHand(ctx, e, c); err != nil {
		return e, rec, err
	}
	e, rec, err = s.plan(ctx, snap, e, c, priority, author, "")
	if rec.ID == "" {
		if rerr := s.unholdForHand(ctx, c); rerr != nil {
			err = errors.Join(err, rerr)
		}
	}
	return e, rec, err
}

// holdForHand marks a choice as being planned by hand, unless the stacker
// holds it for an automatic plan that has not sent its command yet; an
// automatic claim needs planned_at empty, so the two cannot both send.
func (s *Service) holdForHand(ctx context.Context, e Event, c app.SkyEventChoice) error {
	db := s.App.WithContext(ctx)
	if c.WindowStart == nil || c.WindowEnd == nil {
		c.WindowStart, c.WindowEnd = &e.Start, &e.End
	}
	if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&c).Error; err != nil {
		return err
	}
	r := db.Model(&app.SkyEventChoice{}).Where("event_id = ? AND (auto_planned_at IS NULL OR auto_command_id IS NOT NULL)", c.EventID).
		Updates(map[string]any{"planned_at": s.now().UTC(), colWindowStart: gorm.Expr("COALESCE(window_start, ?)", e.Start), colWindowEnd: gorm.Expr("COALESCE(window_end, ?)", e.End)})
	if r.Error != nil {
		return r.Error
	}
	if r.RowsAffected != 1 {
		return fmt.Errorf("%w: the stacker is planning this rare event by itself right now", ErrAlreadyPlanned)
	}
	return nil
}

// unholdForHand gives back a hold when no command was sent.
func (s *Service) unholdForHand(ctx context.Context, c app.SkyEventChoice) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseTimeout)
	defer cancel()
	return s.App.WithContext(ctx).Model(&app.SkyEventChoice{}).Where("event_id = ?", c.EventID).Update("planned_at", c.PlannedAt).Error
}

func (s *Service) plan(ctx context.Context, snap *planning.Snapshot, e Event, c app.SkyEventChoice, priority, author, cmdID string) (Event, schedcmd.Record, error) {
	var rec schedcmd.Record
	if p := s.Planning(ctx); !p.Available {
		return e, rec, fmt.Errorf("%w: %s", ErrCannotPlan, *p.Reason)
	}
	if !e.End.After(s.now()) {
		return e, rec, ErrOver
	}
	if activePlan(e.Plan) {
		return e, rec, ErrAlreadyPlanned
	}
	payload, err := s.Draft(ctx, snap, e, priority, schedcmd.NewID)
	if err != nil {
		return e, rec, err
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return e, rec, err
	}
	if cmdID != "" {
		rec, err = s.Commands.SubmitOnce(ctx, cmdID, schedcmd.KindProjectCreate, raw, author)
	} else {
		rec, err = s.Commands.Submit(ctx, schedcmd.KindProjectCreate, raw, author)
	}
	if err != nil {
		return e, rec, err
	}
	at := s.now().UTC()
	tg := payload.Targets[0]
	readAs := c.EventID
	c.EventID, c.Priority, c.ProjectGUID, c.TargetGUID, c.TargetName, c.CommandID = e.ID, priority, payload.Project.GUID, tg.GUID, tg.Name, rec.ID
	c.Designation, c.Moving, c.IgnoreMoon, c.WindowStart, c.WindowEnd, c.PlannedAt, c.WindowCommand = tg.Designation, e.MovingCenter, tg.Window.IgnoreMoon, &e.Start, &e.End, &at, ""
	if err := s.saveChoice(ctx, c, readAs); err != nil {
		return e, rec, fmt.Errorf("the project was sent (command %s), but saving the plan failed: %w", rec.ID, err)
	}
	if e.MovingCenter {
		if err := s.PublishCentres(ctx); err != nil {
			return e, rec, fmt.Errorf("the project was sent, but publishing its frame centres failed: %w", err)
		}
	}
	e, err = s.Event(ctx, e.ID)
	return e, rec, err
}

// decline records that the user turned an event down, so the stacker never
// plans it by itself.
func (s *Service) decline(ctx context.Context, c app.SkyEventChoice) error {
	at := s.now().UTC()
	if c.AutoDeclinedAt != nil {
		return nil
	}
	return s.App.WithContext(ctx).Model(&app.SkyEventChoice{}).Where("event_id = ? AND auto_declined_at IS NULL", c.EventID).
		Update("auto_declined_at", at).Error
}

// SetPriority stores the priority and, for a planned event, sends a
// target.window command with it. Lowering an event to normal turns down
// planning it automatically.
func (s *Service) SetPriority(ctx context.Context, id, priority, author string) (Event, *schedcmd.Record, error) {
	if !validPriority(priority) {
		return Event{}, nil, fmt.Errorf("%w: priority %q", schedcmd.ErrInvalid, priority)
	}
	s.planMu.Lock()
	defer s.planMu.Unlock()
	e, err := s.Event(ctx, id)
	if err != nil {
		return e, nil, err
	}
	c, err := s.choiceFor(ctx, e)
	if err != nil {
		return e, nil, err
	}
	before := c.Priority
	if before == "" {
		before = PriorityNormal
	}
	readAs := c.EventID
	c.Priority = priority
	if c.WindowStart == nil || c.WindowEnd == nil {
		c.WindowStart, c.WindowEnd = &e.Start, &e.End
	}
	var out *schedcmd.Record
	if activePlan(e.Plan) && before != priority {
		switch out, err = s.sendWindow(ctx, c, priority, before, author); {
		case errors.Is(err, errNothingToSend):
		case err != nil:
			return e, nil, err
		default:
			c.WindowCommand = out.ID
		}
	}
	if err := s.saveChoice(ctx, c, readAs); err != nil {
		return e, out, err
	}
	if priority == PriorityNormal {
		if err := s.decline(ctx, c); err != nil {
			return e, out, err
		}
		switch late, err := s.lowerLateAutoPlan(ctx, e, c, author); {
		case errors.Is(err, errNothingToSend):
		case err != nil:
			return e, out, err
		default:
			out = late
		}
	}
	e, err = s.Event(ctx, id)
	return e, out, err
}

func (s *Service) sendWindow(ctx context.Context, c app.SkyEventChoice, priority, before, author string) (*schedcmd.Record, error) {
	if c.WindowStart == nil || c.WindowEnd == nil || s.Commands == nil {
		return nil, errNothingToSend
	}
	w := func(p string) *schedcmd.EventWindow {
		return &schedcmd.EventWindow{Start: *c.WindowStart, End: *c.WindowEnd, Priority: p, EventID: c.EventID, IgnoreMoon: c.IgnoreMoon}
	}
	raw, err := json.Marshal(schedcmd.TargetWindowPayload{TargetGUID: c.TargetGUID, TargetName: c.TargetName, Window: w(priority), Before: w(before)})
	if err != nil {
		return nil, err
	}
	rec, err := s.Commands.Submit(ctx, schedcmd.KindTargetWindow, raw, author)
	if err != nil {
		return nil, err
	}
	return &rec, s.App.WithContext(ctx).Model(&app.SkyEventChoice{}).Where("event_id = ?", c.EventID).Update("window_command", rec.ID).Error
}

var errNothingToSend = errors.New("nothing to send")

// lowerLateAutoPlan catches an automatic plan another stacker sent while
// the user lowered the event: it lowers that plan's window too.
func (s *Service) lowerLateAutoPlan(ctx context.Context, e Event, was app.SkyEventChoice, author string) (*schedcmd.Record, error) {
	cs, err := s.choices(ctx)
	if err != nil {
		return nil, err
	}
	now, ok := matchChoice(cs, e)
	if !ok || now.AutoCommandID == nil || now.CommandID == "" || now.CommandID == was.CommandID || now.Priority == PriorityNormal ||
		!activePlan(s.planState(ctx, now)) {
		return nil, errNothingToSend
	}
	rec, err := s.sendWindow(ctx, now, PriorityNormal, now.Priority, author)
	if err != nil {
		return nil, err
	}
	return rec, s.App.WithContext(ctx).Model(&app.SkyEventChoice{}).Where("event_id = ?", now.EventID).Update("priority", PriorityNormal).Error
}

// Unplan undoes the event's project.create, which deletes the project, and
// turns down planning the event automatically.
func (s *Service) Unplan(ctx context.Context, id, author string) (Event, schedcmd.Record, error) {
	var rec schedcmd.Record
	s.planMu.Lock()
	defer s.planMu.Unlock()
	e, err := s.Event(ctx, id)
	if err != nil {
		return e, rec, err
	}
	if !activePlan(e.Plan) || s.Commands == nil {
		return e, rec, ErrNotPlanned
	}
	c, err := s.choiceFor(ctx, e)
	if err != nil {
		return e, rec, err
	}
	if err := s.decline(ctx, c); err != nil {
		return e, rec, err
	}
	rec, err = s.Commands.Undo(ctx, e.Plan.CommandID, author)
	if err != nil {
		return e, rec, err
	}
	if err := s.PublishCentres(ctx); err != nil {
		return e, rec, fmt.Errorf("the project removal was sent, but clearing its frame centres failed: %w", err)
	}
	e, err = s.Event(ctx, id)
	return e, rec, err
}

// centreRows are the moving frame centre of a planned two-body event, every
// CentreStep over each night window, as ts_body_position rows.
func (s *Service) centreRows(c app.SkyEventChoice, e Event, at time.Time) []bodies.PositionRow {
	eph, err := s.ephemeris()
	if err != nil {
		return nil
	}
	s.mu.Lock()
	site, tabs := s.site, s.tables
	s.mu.Unlock()
	var place []placer
	for _, m := range e.Members {
		switch {
		case m.Kind == KindMoon:
			place = append(place, ephemerisPlacer(eph, ephem.Moon, site))
		case m.Kind == KindPlanet:
			for _, b := range ephem.Planets() {
				if b.String() == m.Name {
					place = append(place, ephemerisPlacer(eph, b, site))
				}
			}
		case m.Designation != nil:
			if tab, ok := tabs[strings.ToLower(*m.Designation)]; ok {
				place = append(place, tablePlacer(tab))
			}
		}
	}
	if len(place) != len(e.Members) || len(place) < 2 {
		return nil
	}
	res := s.Latest()
	if res == nil || res.Criteria == nil {
		return nil
	}
	centre := func(t time.Time) (float64, float64, bool) {
		ds := make([]disc, 0, len(place))
		for _, p := range place {
			d, ok := p(t)
			if !ok {
				return 0, 0, false
			}
			ds = append(ds, d)
		}
		f, ok := fitFrame(ds, res.Criteria.UsableW+2*FrameMargin, res.Criteria.UsableH+2*FrameMargin)
		return f.center.RA, f.center.Dec, ok
	}
	var out []bodies.PositionRow
	for _, n := range e.NightWindows {
		for t := n.Start.Add(-CentrePad).Truncate(CentreStep); !t.After(n.End.Add(CentrePad)); t = t.Add(CentreStep) {
			ra, dec, ok := centre(t)
			ra2, dec2, ok2 := centre(t.Add(time.Minute))
			if !ok || !ok2 {
				continue
			}
			raRate := math.Remainder(ra2-ra, 360) * math.Cos(dec*deg) * 3600 * 60
			out = append(out, bodies.PositionRow{TargetGUID: c.TargetGUID, At: t.UTC(), Designation: c.Designation, Night: n.Night,
				RA: ra, Dec: dec, RARate: raRate, DecRate: (dec2 - dec) * 3600 * 60, ComputedAt: at})
		}
	}
	return out
}

// PublishCentres writes the moving centres of planned two-body events into
// ts_body_position and removes event rows no longer wanted. It does nothing
// until the plugin's position table exists.
func (s *Service) PublishCentres(ctx context.Context) error {
	if s.Sched == nil {
		return nil
	}
	has, err := tables.Has(ctx, s.Sched, bodies.PositionTable)
	if err != nil || !has {
		return err
	}
	cs, err := s.choices(ctx)
	if err != nil {
		return err
	}
	at := s.now().UTC()
	var want []bodies.PositionRow
	for _, c := range cs {
		if !c.Moving || c.Designation == "" {
			continue
		}
		e, err := s.Event(ctx, c.EventID)
		if errors.Is(err, ErrNotFound) {
			res := s.Latest()
			found := false
			if res != nil {
				for _, x := range res.Events {
					if matches(c, x) {
						e, found = x, true
						e.Plan = s.planState(ctx, c)
						break
					}
				}
			}
			if !found {
				continue
			}
		} else if err != nil {
			return err
		}
		if !activePlan(e.Plan) {
			continue
		}
		want = append(want, s.centreRows(c, e, at)...)
	}
	keep := map[string]bool{}
	for _, r := range want {
		keep[strings.ToLower(r.TargetGUID)+"|"+r.At.UTC().Format(time.RFC3339)] = true
	}
	return s.Sched.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var have []bodies.PositionRow
		if err := tx.Table(bodies.PositionTable).Where("designation LIKE ?", bodies.EventPrefix+"%").Find(&have).Error; err != nil {
			return err
		}
		for _, r := range have {
			if keep[strings.ToLower(r.TargetGUID)+"|"+r.At.UTC().Format(time.RFC3339)] {
				continue
			}
			if err := tx.Table(bodies.PositionTable).Where("target_guid = ? AND at = ?", r.TargetGUID, r.At).Delete(nil).Error; err != nil {
				return err
			}
		}
		if len(want) == 0 {
			return nil
		}
		return tx.Table(bodies.PositionTable).Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "target_guid"}, {Name: "at"}},
			DoUpdates: clause.AssignmentColumns([]string{"designation", "night", "ra", "dec", "ra_rate", "dec_rate", "computed_at"}),
		}).CreateInBatches(&want, 200).Error
	})
}
