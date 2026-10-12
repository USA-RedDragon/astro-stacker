package skyevents

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/bodies"
	"github.com/USA-RedDragon/astro-stacker/internal/ephem"
	"github.com/USA-RedDragon/astro-stacker/internal/mosaics"
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
	window := schedcmd.EventWindow{Start: e.Start, End: e.End, Priority: priority, EventID: e.ID, IgnoreMoon: hasMoon(e)}
	type frame struct {
		guid, name    string
		ra, dec, turn float64
	}
	mosaic := e.Frame.Mosaic
	var projectGUID string
	frames := []frame{{name: e.Title, ra: e.Frame.CenterRA, dec: e.Frame.CenterDec, turn: e.Frame.Rotation}}
	if mosaic != nil {
		projectGUID = newID()
		frames = frames[:0]
		for _, p := range mosaic.Panels {
			frames = append(frames, frame{guid: panelGUID(projectGUID, p.N), name: panelName(e.Title, p.N), ra: p.CenterRA, dec: p.CenterDec, turn: p.Rotation})
		}
	}
	targets := make([]schedcmd.NewTarget, 0, len(frames))
	for _, f := range frames {
		if f.guid == "" {
			f.guid = newID()
		}
		w := window
		nt := schedcmd.NewTarget{GUID: f.guid, Name: f.name, RAHours: f.ra / 15, Dec: f.dec, Rotation: f.turn, Window: &w}
		if e.MovingCenter {
			nt.Designation, nt.BodyKind = bodies.EventPrefix+e.ID, bodies.KindEvent
		}
		for _, l := range lengths {
			nt.Plans = append(nt.Plans, schedcmd.NewPlan{GUID: newID(), TemplateID: int64(t.ID), TemplateName: t.Name, Exposure: l,
				Desired: desired(budget, len(lengths)*len(frames), l)})
		}
		targets = append(targets, nt)
	}
	prio := 1
	if priority == PriorityHigh {
		prio = 2
	}
	desc := fmt.Sprintf("Sky event %s: %s. Closest %s UTC at %.2f°. %s", e.ID, e.Title, e.Best.At.UTC().Format("2006-01-02 15:04"), e.Best.Separation, exp.Basis)
	if projectGUID == "" {
		projectGUID = newID()
	}
	out := &schedcmd.ProjectCreatePayload{
		Project: schedcmd.NewProject{GUID: projectGUID, Name: s.projectName(snap, e), Description: desc, Priority: prio, State: 1,
			MinimumTime: MinimumTime, MinimumAltitude: DefaultMinAlt, ProjectSettings: settings},
		Targets:     targets,
		RuleWeights: planning.WeightsFor(snap.GlobalWeights.Weights, mosaic != nil),
	}
	if mosaic != nil {
		out.Project.IsMosaic = true
		out.Project.Description = fmt.Sprintf("Sky event %s: %s, %d-panel mosaic. Closest %s UTC at %.2f°. %s", e.ID, e.Title, len(frames),
			e.Best.At.UTC().Format("2006-01-02 15:04"), e.Best.Separation, exp.Basis)
		out.Mosaic = &schedcmd.MosaicPlan{Layout: fmt.Sprintf("grid-%dx%d", mosaic.Rows, mosaic.Cols), Rotation: e.Frame.Rotation, Overlap: mosaic.Overlap,
			Cols: mosaic.Cols, Rows: mosaic.Rows}
	}
	return out, nil
}

func panelName(title string, n int) string { return fmt.Sprintf("%s Panel %d", title, n) }

func uuidFrom(seed string) string {
	h := sha256.Sum256([]byte(seed))
	h[6] = (h[6] & 0x0f) | 0x50
	h[8] = (h[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", h[0:4], h[4:6], h[6:8], h[8:10], h[10:16])
}

// panelGUID is the target guid of a mosaic event's panel, made from the
// project guid, so later commands find every panel from the stored project.
func panelGUID(project string, n int) string {
	return uuidFrom(fmt.Sprintf("sky-event-panel:%s:%d", project, n))
}

type eventTarget struct {
	guid, name string
	x, y       float64
}

// sentFrame is what the project.create of a planned event sent: its
// targets with each panel's offset from the centre in frame axes, the
// rotation, and the whole field the members must stay inside.
type sentFrame struct {
	project  string
	rotation float64
	w, h     float64
	reach    float64
	targets  []eventTarget
}

// sentFrom reads a planned event's frame back from its project.create, for
// a fw by fh camera field.
func sentFrom(raw json.RawMessage, fw, fh float64) (sentFrame, bool) {
	var p schedcmd.ProjectCreatePayload
	if err := json.Unmarshal(raw, &p); err != nil || len(p.Targets) == 0 || fw <= 0 || fh <= 0 {
		return sentFrame{}, false
	}
	f := sentFrame{project: p.Project.GUID, rotation: p.Targets[0].Rotation, w: fw, h: fh, reach: math.Max(fw, fh)}
	if p.Mosaic != nil && len(p.Targets) > 1 && p.Mosaic.Cols > 0 && p.Mosaic.Rows > 0 {
		f.rotation = p.Mosaic.Rotation
		o := p.Mosaic.Overlap / 100
		f.w = float64(p.Mosaic.Cols)*fw - float64(p.Mosaic.Cols-1)*fw*o
		f.h = float64(p.Mosaic.Rows)*fh - float64(p.Mosaic.Rows-1)*fh*o
		f.reach = math.Hypot(f.w, f.h)
	}
	ds := make([]disc, 0, len(p.Targets))
	for _, t := range p.Targets {
		ds = append(ds, disc{ra: t.RAHours * 15, dec: t.Dec})
	}
	centre := meanPoint(ds)
	for _, t := range p.Targets {
		tg := eventTarget{guid: t.GUID, name: t.Name}
		if len(p.Targets) > 1 {
			e, n := mosaics.Project(centre, mosaics.Point{RA: t.RAHours * 15, Dec: t.Dec})
			tg.x, tg.y = frameXY(e, n, f.rotation)
		}
		f.targets = append(f.targets, tg)
	}
	return f, true
}

// sent is the frame a planned event's command sent, or failing that its
// stored target alone.
func (s *Service) sent(ctx context.Context, c app.SkyEventChoice) sentFrame {
	fw, fh := 0.0, 0.0
	if res := s.Latest(); res != nil && res.Criteria != nil {
		fw, fh = res.Criteria.frame()
	}
	if s.Commands != nil && s.Commands.Log != nil && c.CommandID != "" {
		if r, err := s.Commands.Log.Get(ctx, c.CommandID); err == nil {
			if f, ok := sentFrom(r.Payload, fw, fh); ok {
				return f
			}
		}
	}
	return sentFrame{project: c.ProjectGUID, w: fw, h: fh, reach: math.Max(fw, fh), targets: []eventTarget{{guid: c.TargetGUID, name: c.TargetName}}}
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
	if len(rec.Payload) > 0 {
		var sent schedcmd.ProjectCreatePayload
		if json.Unmarshal(rec.Payload, &sent) == nil && len(sent.Targets) > 0 && sent.Targets[0].Window != nil {
			payload = &sent
		}
	}
	at := s.now().UTC()
	tg := payload.Targets[0]
	start, end := tg.Window.Start, tg.Window.End
	readAs := c.EventID
	c.EventID, c.Priority, c.ProjectGUID, c.TargetGUID, c.TargetName, c.CommandID = e.ID, priority, payload.Project.GUID, tg.GUID, tg.Name, rec.ID
	c.Designation, c.Moving, c.IgnoreMoon, c.WindowStart, c.WindowEnd, c.PlannedAt, c.WindowCommand = tg.Designation, tg.Designation != "", tg.Window.IgnoreMoon, &start, &end, &at, ""
	if err := s.saveChoice(ctx, c, readAs); err != nil {
		return e, rec, fmt.Errorf("the project was sent (command %s), but saving the plan failed: %w", rec.ID, err)
	}
	if c.Moving {
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
		var ids string
		switch out, ids, err = s.sendWindow(ctx, c, priority, before, author); {
		case errors.Is(err, errNothingToSend):
		case err != nil:
			return e, nil, err
		default:
			c.WindowCommand = ids
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

// sendWindow sends a target.window for every target of the planned event.
// When one fails it takes back the ones already sent, so the panels of a
// mosaic never end up at different priorities, and it remembers every
// command id.
func (s *Service) sendWindow(ctx context.Context, c app.SkyEventChoice, priority, before, author string) (*schedcmd.Record, string, error) {
	if c.WindowStart == nil || c.WindowEnd == nil || s.Commands == nil {
		return nil, "", errNothingToSend
	}
	w := func(p string) *schedcmd.EventWindow {
		return &schedcmd.EventWindow{Start: *c.WindowStart, End: *c.WindowEnd, Priority: p, EventID: c.EventID, IgnoreMoon: c.IgnoreMoon}
	}
	var sent []schedcmd.Record
	fail := func(err error) (*schedcmd.Record, string, error) {
		for _, r := range sent {
			if _, uerr := s.Commands.Undo(ctx, r.ID, author); uerr != nil {
				err = errors.Join(err, uerr)
			}
		}
		return nil, "", err
	}
	for _, tg := range s.sent(ctx, c).targets {
		raw, err := json.Marshal(schedcmd.TargetWindowPayload{TargetGUID: tg.guid, TargetName: tg.name, Window: w(priority), Before: w(before)})
		if err != nil {
			return fail(err)
		}
		rec, err := s.Commands.Submit(ctx, schedcmd.KindTargetWindow, raw, author)
		if err != nil {
			return fail(err)
		}
		sent = append(sent, rec)
	}
	if len(sent) == 0 {
		return nil, "", errNothingToSend
	}
	ids := make([]string, 0, len(sent))
	for _, r := range sent {
		ids = append(ids, r.ID)
	}
	last, joined := sent[len(sent)-1], strings.Join(ids, ",")
	return &last, joined, s.App.WithContext(ctx).Model(&app.SkyEventChoice{}).Where("event_id = ?", c.EventID).Update("window_command", joined).Error
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
	rec, _, err := s.sendWindow(ctx, now, PriorityNormal, now.Priority, author)
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

// placers places a planned event's bodies from the member ids its choice
// stored.
func (s *Service) placers(c app.SkyEventChoice) ([]placer, sky.Site, bool) {
	eph, err := s.ephemeris()
	if err != nil {
		return nil, sky.Site{}, false
	}
	s.mu.Lock()
	site, tabs := s.site, s.tables
	s.mu.Unlock()
	var place []placer
	for _, id := range strings.Split(c.Bodies, ",") {
		name, ok := strings.CutPrefix(id, "body:")
		if !ok {
			return nil, site, false
		}
		if b, ok := ephemerisBodyID(name); ok {
			place = append(place, ephemerisPlacer(eph, b, site))
			continue
		}
		tab, ok := tabs[name]
		if !ok {
			return nil, site, false
		}
		place = append(place, tablePlacer(tab))
	}
	return place, site, len(place) >= 2
}

func ephemerisBodyID(name string) (ephem.Body, bool) {
	for _, b := range append([]ephem.Body{ephem.Moon}, ephem.Planets()...) {
		if strings.EqualFold(b.String(), name) {
			return b, true
		}
	}
	return 0, false
}

// centreRows are the moving frame centre of a planned event of bodies, or
// of each panel of its mosaic, every CentreStep from before its window
// opens to after it closes while the Sun is down, as ts_body_position rows.
// The bodies, the targets, the rotation and the panel offsets are the ones
// its project.create sent, and a centre is published only while the bodies
// fit the field at that rotation.
func (s *Service) centreRows(ctx context.Context, c app.SkyEventChoice, at time.Time) []bodies.PositionRow {
	if c.WindowStart == nil || c.WindowEnd == nil {
		return nil
	}
	place, site, ok := s.placers(c)
	if !ok {
		return nil
	}
	eph, err := s.ephemeris()
	if err != nil {
		return nil
	}
	f := s.sent(ctx, c)
	if f.w <= 0 || f.h <= 0 {
		return nil
	}
	sunBelow := DefaultSunBelow
	if res := s.Latest(); res != nil && res.Criteria != nil {
		sunBelow = res.Criteria.SunBelow
	}
	centre := func(t time.Time) (mosaics.Point, bool) {
		ds := make([]disc, 0, len(place))
		for _, p := range place {
			d, ok := p(t)
			if !ok {
				return mosaics.Point{}, false
			}
			ds = append(ds, d)
		}
		p := project(ds)
		slack, bx, by := p.box(f.rotation, f.w, f.h)
		if slack < 0 || p.pairwise > f.reach {
			return mosaics.Point{}, false
		}
		return p.centre(f.rotation, bx, by), true
	}
	var out []bodies.PositionRow
	for t := c.WindowStart.Add(-CentrePad).Truncate(CentreStep); !t.After(c.WindowEnd.Add(CentrePad)); t = t.Add(CentreStep) {
		if sun, err := eph.SunAltitude(ephem.NewObserver(site, t)); err != nil || sun >= sunBelow {
			continue
		}
		c0, ok := centre(t)
		c1, ok2 := centre(t.Add(time.Minute))
		if !ok || !ok2 {
			continue
		}
		for _, tg := range f.targets {
			p0, p1 := mosaics.Offset(c0, f.rotation, tg.x, tg.y), mosaics.Offset(c1, f.rotation, tg.x, tg.y)
			raRate := math.Remainder(p1.RA-p0.RA, 360) * math.Cos(p0.Dec*deg) * 3600 * 60
			out = append(out, bodies.PositionRow{TargetGUID: tg.guid, At: t.UTC(), Designation: c.Designation, Night: nightOf(site, t),
				RA: p0.RA, Dec: p0.Dec, RARate: raRate, DecRate: (p1.Dec - p0.Dec) * 3600 * 60, ComputedAt: at})
		}
	}
	return out
}

// PublishCentres writes the moving centres of planned events of bodies into
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
		if !c.Moving || c.Designation == "" || !activePlan(s.planState(ctx, c)) {
			continue
		}
		want = append(want, s.centreRows(ctx, c, at)...)
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
