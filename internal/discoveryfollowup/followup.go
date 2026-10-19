// Package discoveryfollowup asks Target Scheduler to image an alerted
// discovery's field again at the next chance and records whether the find
// was seen again.
//
// A follow-up is a project of its own: one target at the original target's
// position and framing, named like it so its subs join the same night
// comparisons, with one count-based plan in the find's filter group for at
// least MinSubs subs or MinMinutes of exposure, and a high-priority event
// window from the request to the morning after Until. The plugin keeps a
// high window above every target without one but still applies safety,
// flats and darks, collaboration hand-over, the horizon, the meridian and
// moon avoidance first, and the plan completes on its own once the subs are
// in. The project is closed once every find it serves is settled.
package discoveryfollowup

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/planning"
	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/trigger"
	"gorm.io/gorm"
)

const (
	colNote      = "note"
	colCreateCmd = "create_cmd"
)

const (
	// Nights is how many nights, the request's own included, a follow-up
	// may be imaged on before it expires.
	Nights = 3
	// MinSubs and MinMinutes are the least a follow-up takes: MinSubs subs
	// and MinMinutes of exposure, whichever is more.
	MinSubs    = 6
	MinMinutes = 30
	// ConfirmArcsec is how close a change on the later look must be to the
	// find to be it.
	ConfirmArcsec = 2.5
	// Author signs the scheduler commands.
	Author = "discovery follow-up"
	// maxDesired caps the subs one follow-up asks for.
	maxDesired = 120
	// capFactor times the subs asked for closes a follow-up project still
	// imaging, as on a night whose subs are mostly rejected.
	capFactor = 2
)

// Commands sends scheduler commands once each and reads their outcome.
type Commands interface {
	SubmitOnce(ctx context.Context, id string, kind schedcmd.Kind, payload json.RawMessage, author string) (schedcmd.Record, error)
	Status(ctx context.Context, id string) (schedcmd.Record, error)
}

// Service turns alerts into follow-up projects and settles them.
type Service struct {
	DB       *gorm.DB
	Commands Commands
	Snapshot func(ctx context.Context) (*planning.Snapshot, error)
	// Loc is the observatory's time zone, which says what tonight is.
	Loc      *time.Location
	Now      func() time.Time
	Interval time.Duration

	kick *trigger.Debouncer
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Tonight is the night under way or just ended, by its evening date: the
// civil date in loc twelve hours before t.
func Tonight(t time.Time, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	l := t.In(loc).Add(-12 * time.Hour)
	return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, time.UTC)
}

// FirstNight is the first night still to come or under way at t: tonight,
// or in the morning after a night, the next one.
func FirstNight(t time.Time, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	n := Tonight(t, loc)
	if h := t.In(loc).Hour(); h >= morningHour && h < 12 {
		n = n.AddDate(0, 0, 1)
	}
	return n
}

// morningHour is when a night counts as over.
const morningHour = 6

// WindowEnd is local noon after the night until, when that night's imaging
// is over.
func WindowEnd(until time.Time, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	d := until.AddDate(0, 0, 1)
	return time.Date(d.Year(), d.Month(), d.Day(), 12, 0, 0, 0, loc)
}

// Desired is how many subs of exposure seconds a follow-up asks for.
func Desired(exposure float64) int {
	n := MinSubs
	if exposure > 0 {
		n = max(n, int(math.Ceil(MinMinutes*60/exposure)))
	}
	return min(n, maxDesired)
}

func uuidFrom(seed string) string {
	h := sha256.Sum256([]byte(seed))
	h[6] = (h[6] & 0x0f) | 0x50
	h[8] = (h[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", h[0:4], h[4:6], h[6:8], h[8:10], h[10:16])
}

// ProjectGUID is the follow-up project of a field's night.
func ProjectGUID(object, group string, night time.Time) string {
	return uuidFrom("discovery-followup:" + object + "\x00" + group + "\x00" + night.Format(time.DateOnly))
}

// Kick asks for a look soon.
func (s *Service) Kick() {
	if s.kick != nil {
		s.kick.Kick()
	}
}

// Run settles follow-ups at start, soon after each Kick and every Interval
// until ctx ends.
func (s *Service) Run(ctx context.Context) {
	if s.kick == nil {
		s.kick = &trigger.Debouncer{Quiet: 30 * time.Second, MaxWait: 2 * time.Minute}
	}
	interval := s.Interval
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	s.kick.Run(ctx, interval, func(ctx context.Context, _ []string) {
		if err := s.Tick(ctx); err != nil {
			slog.Error("Discovery follow-ups", "error", err)
		}
	})
}

// Request records a waiting follow-up for each alert that has none.
func (s *Service) Request(ctx context.Context, alerts []app.DiscoveryAlert) error {
	tonight := FirstNight(s.now(), s.Loc)
	var errs []error
	for _, a := range alerts {
		f := app.DiscoveryFollowup{AlertID: a.ID, Object: a.Object, Group: a.Group, Night: a.Night, Kind: a.Kind, Class: a.Class,
			RA: a.RA, Dec: a.Dec, Sign: 1, State: app.FollowupWaiting, ProjectGUID: ProjectGUID(a.Object, a.Group, a.Night), CreatedAt: s.now()}
		f.Until = tonight.AddDate(0, 0, Nights-1)
		if c, ok := s.candidate(ctx, a); ok {
			f.Sign, f.Mag = c.Sign, c.NightMag
			if c.Kind == app.DiscoveryKindMover {
				fillTrack(&f, c)
			}
		}
		if err := s.DB.WithContext(ctx).Where("alert_id = ?", a.ID).FirstOrCreate(&f).Error; err != nil {
			errs = append(errs, err)
			continue
		}
		slog.Info("Discovery follow-up requested", "object", a.Object, "night", a.Night.Format(time.DateOnly), "group", a.Group,
			"class", a.Class, "until", f.Until.Format(time.DateOnly))
	}
	s.Kick()
	return errors.Join(errs...)
}

// candidate is the stored candidate an alert covers: by id, or after a
// rerun replaced it, the same source of the night.
func (s *Service) candidate(ctx context.Context, a app.DiscoveryAlert) (app.DiscoveryCandidate, bool) {
	var cs []app.DiscoveryCandidate
	if err := s.DB.WithContext(ctx).Where("id = ?", a.CandidateID).Limit(1).Find(&cs).Error; err == nil && len(cs) == 1 &&
		cs[0].Class == a.Class && sep(cs[0].RA, cs[0].Dec, a.RA, a.Dec) <= 2 {
		return cs[0], true
	}
	cs = nil
	if err := s.DB.WithContext(ctx).Where("object = ? AND night = ? AND class = ?", a.Object, a.Night, a.Class).Find(&cs).Error; err != nil {
		return app.DiscoveryCandidate{}, false
	}
	for _, c := range cs {
		if sep(c.RA, c.Dec, a.RA, a.Dec) <= 2 {
			return c, true
		}
	}
	return app.DiscoveryCandidate{}, false
}

func fillTrack(f *app.DiscoveryFollowup, c app.DiscoveryCandidate) {
	var t discoveries.TrackRecord
	if json.Unmarshal([]byte(c.Track), &t) != nil || len(t.Points) == 0 {
		return
	}
	mid := t.Points[len(t.Points)/2]
	at, rate, pa := mid.Time.UTC(), t.RateArcmin, t.PA
	f.RA, f.Dec, f.At, f.RateArcmin, f.PA = mid.RA, mid.Dec, &at, &rate, &pa
}

func sep(ra1, dec1, ra2, dec2 float64) float64 {
	return discoveries.Separation(ra1, dec1, ra2, dec2) * 3600
}

// Tick sends the projects still to send, settles what a later look at the
// field decided, expires what ran out of nights and closes finished
// projects.
func (s *Service) Tick(ctx context.Context) error {
	var open []app.DiscoveryFollowup
	if err := s.DB.WithContext(ctx).Where("state = ?", app.FollowupWaiting).Order("id").Find(&open).Error; err != nil {
		return err
	}
	var errs []error
	if err := s.send(ctx, open); err != nil {
		errs = append(errs, err)
	}
	tonight := Tonight(s.now(), s.Loc)
	for i := range open {
		f := &open[i]
		if f.State != app.FollowupWaiting {
			continue
		}
		done, err := s.settle(ctx, f)
		if err != nil {
			errs = append(errs, fmt.Errorf("follow-up %d: %w", f.ID, err))
			continue
		}
		if !done && tonight.After(f.Until) {
			note := "no later look at the field was compared by the night of " + f.Until.Format("Jan 2")
			if f.Look != "" {
				note = "no later look settled it by the night of " + f.Until.Format("Jan 2") + "; the last: " + f.Look
			}
			s.finish(ctx, f, app.FollowupExpired, note, nil)
		}
	}
	if err := s.close(ctx); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (s *Service) update(ctx context.Context, f *app.DiscoveryFollowup, fields map[string]any) {
	if err := s.DB.WithContext(context.WithoutCancel(ctx)).Model(&app.DiscoveryFollowup{}).Where("id = ?", f.ID).Updates(fields).Error; err != nil {
		slog.Error("Recording a discovery follow-up failed", "id", f.ID, "error", err)
	}
}

func (s *Service) finish(ctx context.Context, f *app.DiscoveryFollowup, state, note string, extra map[string]any) {
	now := s.now()
	fields := map[string]any{"state": state, colNote: note, "closed_at": now}
	for k, v := range extra {
		fields[k] = v
	}
	s.update(ctx, f, fields)
	f.State, f.Note, f.ClosedAt = state, note, &now
	slog.Info("Discovery follow-up settled", "object", f.Object, "night", f.Night.Format(time.DateOnly), "class", f.Class, "state", state, colNote, note)
}

// send creates the follow-up project of each field still without one, and
// marks follow-ups failed when the scheduler refused it.
func (s *Service) send(ctx context.Context, open []app.DiscoveryFollowup) error {
	if s.Commands == nil || s.Snapshot == nil {
		return nil
	}
	var snap *planning.Snapshot
	var errs []error
	sent := map[string]string{}
	for i := range open {
		f := &open[i]
		if f.CreateCmd != "" {
			s.checkCreate(ctx, f)
			continue
		}
		if id, ok := sent[f.ProjectGUID]; ok {
			s.update(ctx, f, map[string]any{colCreateCmd: id})
			f.CreateCmd = id
			continue
		}
		var shared []app.DiscoveryFollowup
		if err := s.DB.WithContext(ctx).Where("project_guid = ? AND create_cmd <> ''", f.ProjectGUID).Limit(1).Find(&shared).Error; err != nil {
			errs = append(errs, err)
			continue
		}
		if len(shared) == 1 {
			s.update(ctx, f, map[string]any{colCreateCmd: shared[0].CreateCmd, "target_guid": shared[0].TargetGUID,
				"filter": shared[0].Filter, "exposure": shared[0].Exposure, "subs": shared[0].Subs})
			f.CreateCmd = shared[0].CreateCmd
			continue
		}
		if snap == nil {
			var err error
			if snap, err = s.Snapshot(ctx); err != nil {
				return errors.Join(append(errs, fmt.Errorf("planning snapshot: %w", err))...)
			}
		}
		orig, err := s.originalTarget(ctx, f)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		payload, pick, note := Draft(snap, f, orig, s.now(), s.Loc)
		if payload == nil {
			if note != f.Note {
				s.update(ctx, f, map[string]any{colNote: note})
				f.Note = note
			}
			continue
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		id := "discovery-followup-" + f.ProjectGUID
		rec, err := s.Commands.SubmitOnce(ctx, id, schedcmd.KindProjectCreate, raw, Author)
		if err != nil {
			errs = append(errs, fmt.Errorf("follow-up %d: %w", f.ID, err))
			continue
		}
		note = "follow-up project sent: " + pick.text()
		s.update(ctx, f, map[string]any{colCreateCmd: rec.ID, "target_guid": pick.TargetGUID, "filter": pick.Filter,
			"exposure": pick.Exposure, "subs": pick.Subs, colNote: note})
		f.CreateCmd, f.TargetGUID, f.Filter, f.Exposure, f.Subs, f.Note = rec.ID, pick.TargetGUID, pick.Filter, pick.Exposure, pick.Subs, note
		sent[f.ProjectGUID] = rec.ID
		slog.Info("Discovery follow-up sent to the scheduler", "object", f.Object, "night", f.Night.Format(time.DateOnly), "command", rec.ID,
			"status", rec.Status, "filter", pick.Filter, "subs", pick.Subs, "until", f.Until.Format(time.DateOnly))
	}
	return errors.Join(errs...)
}

// checkCreate marks a follow-up failed when the scheduler refused its
// project.
func (s *Service) checkCreate(ctx context.Context, f *app.DiscoveryFollowup) {
	rec, err := s.Commands.Status(ctx, f.CreateCmd)
	if err != nil {
		return
	}
	if slices.Contains([]schedcmd.Status{schedcmd.StatusRejected, schedcmd.StatusFailed, schedcmd.StatusConflict, schedcmd.StatusCancelled}, rec.Status) {
		s.finish(ctx, f, app.FollowupFailed, "the scheduler did not take the follow-up project: "+cmpOr(rec.Message, string(rec.Status)), nil)
	}
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (s *Service) originalTarget(ctx context.Context, f *app.DiscoveryFollowup) (string, error) {
	return OriginalTarget(ctx, s.DB, f)
}

// OriginalTarget is the Target Scheduler target most of the find's night's
// subs came from, the follow-up's own target aside.
func OriginalTarget(ctx context.Context, db *gorm.DB, f *app.DiscoveryFollowup) (string, error) {
	var rows []struct {
		TSTarget string
		N        int
	}
	q := db.WithContext(ctx).Model(&app.Frame{}).Select("ts_target, COUNT(*) AS n").
		Where("type = ? AND object = ? AND night = ? AND ts_target IS NOT NULL AND ts_target <> ''", "LIGHT", f.Object, f.Night)
	if err := q.Group("ts_target").Order("n DESC").Find(&rows).Error; err != nil {
		return "", err
	}
	ours := FollowTargetGUID(f.ProjectGUID)
	for _, r := range rows {
		if r.TSTarget != ours {
			return r.TSTarget, nil
		}
	}
	return "", nil
}

// FollowTargetGUID is the target of a follow-up project.
func FollowTargetGUID(project string) string {
	return uuidFrom("discovery-followup-target:" + project)
}

// ProjectPrefix starts every follow-up project's name.
const ProjectPrefix = "Follow-up: "

// Pick is what a follow-up project images.
type Pick struct {
	TargetGUID string
	Filter     string
	Exposure   float64
	Subs       int
}

func (p Pick) text() string {
	return fmt.Sprintf("%d × %gs %s", p.Subs, p.Exposure, p.Filter)
}

// Draft is the follow-up project of a field: the original target's
// position and framing, a plan on the find's filter group, and a high event
// window from now to WindowEnd(Until). A nil payload comes with the reason.
func Draft(snap *planning.Snapshot, f *app.DiscoveryFollowup, origGUID string, now time.Time, loc *time.Location) (*schedcmd.ProjectCreatePayload, Pick, string) {
	var proj planning.Project
	var tgt planning.Target
	found := false
	for _, p := range snap.Projects {
		if strings.HasPrefix(p.Name, ProjectPrefix) {
			continue
		}
		for _, t := range p.Targets {
			if (origGUID != "" && t.GUID == origGUID) || (origGUID == "" && t.Name == f.Object && !found) {
				proj, tgt, found = p, t, true
			}
		}
	}
	if !found || tgt.RAHours == nil || tgt.Dec == nil {
		return nil, Pick{}, "no Target Scheduler target with a position images " + f.Object
	}
	if tgt.BodyKind != "" {
		return nil, Pick{}, f.Object + " follows a moving body; its field can't be imaged again as it was"
	}
	plan, ok := pickPlan(tgt.Plans, f.Group)
	if !ok {
		return nil, Pick{}, f.Object + " has no exposure plan in the " + f.Group + " filters"
	}
	exposure := plan.Exposure
	if exposure <= 0 {
		return nil, Pick{}, "the " + plan.Filter + " plan of " + f.Object + " has no exposure length"
	}
	settings, err := snap.SettingsFor(plan.TemplateID, proj.MinimumAltitude)
	if err != nil {
		return nil, Pick{}, err.Error()
	}
	KeepLimits(&settings, proj)
	end := WindowEnd(f.Until, loc)
	if !end.After(now) {
		return nil, Pick{}, "the follow-up's last night is over"
	}
	pick := Pick{TargetGUID: FollowTargetGUID(f.ProjectGUID), Filter: plan.Filter, Exposure: exposure, Subs: Desired(exposure)}
	rotation := 0.0
	if tgt.Rotation != nil {
		rotation = *tgt.Rotation
	}
	window := schedcmd.EventWindow{Start: now.UTC().Truncate(time.Minute), End: end.UTC(), Priority: schedcmd.EventPriorityHigh,
		EventID: "discovery-followup-" + f.Night.Format(time.DateOnly)}
	tmplID, tmplName, filter := plan.TemplateID, plan.Template, plan.Filter
	if t, ok := snap.ResolveFilter(proj.ProfileID, plan.Filter); ok {
		tmplID, tmplName, filter = t.ID, t.Name, snap.PlanFilter(t)
	} else if t, ok := TemplateByID(snap, plan.TemplateID); ok {
		tmplName, filter = t.Name, snap.PlanFilter(t)
	}
	minutes := int(math.Ceil(float64(pick.Subs) * (exposure + 30) / 60))
	payload := &schedcmd.ProjectCreatePayload{
		Project: schedcmd.NewProject{GUID: f.ProjectGUID, Name: fmt.Sprintf(ProjectPrefix+"%s (%s, %s)", f.Object, f.Night.Format(time.DateOnly), f.Group),
			Description: fmt.Sprintf("A second look at %s for a discovery candidate found on the night of %s: %s. Made by the stacker; it closes itself once the find is settled.",
				f.Object, f.Night.Format(time.DateOnly), pick.text()),
			Priority: 2, State: 1, MinimumTime: min(minutes, 60), MinimumAltitude: proj.MinimumAltitude, ProjectSettings: settings},
		Targets: []schedcmd.NewTarget{{GUID: pick.TargetGUID, Name: tgt.Name, RAHours: *tgt.RAHours, Dec: *tgt.Dec, Rotation: rotation, Window: &window,
			Plans: []schedcmd.NewPlan{{GUID: uuidFrom("discovery-followup-plan:" + f.ProjectGUID), TemplateID: int64(tmplID), TemplateName: tmplName,
				Filter: filter, Exposure: exposure, Desired: pick.Subs}}}},
		RuleWeights: planning.WeightsFor(snap.GlobalWeights.Weights, false),
	}
	return payload, pick, ""
}

// TemplateByID is the snapshot's exposure template id.
func TemplateByID(snap *planning.Snapshot, id int) (planning.Template, bool) {
	for _, t := range snap.Templates {
		if t.ID == id {
			return t, true
		}
	}
	return planning.Template{}, false
}

// KeepLimits copies the original project's horizon and meridian limits.
func KeepLimits(s *schedcmd.ProjectSettings, p planning.Project) {
	if p.MeridianWindow != nil {
		s.MeridianWindow = p.MeridianWindow
	}
	if p.UseCustomHorizon != nil {
		s.UseCustomHorizon = p.UseCustomHorizon
	}
	if p.HorizonOffset != nil {
		s.HorizonOffset = p.HorizonOffset
	}
	if p.MaximumAltitude != nil {
		s.MaximumAltitude = p.MaximumAltitude
	}
	if p.FlatsHandling != nil {
		s.FlatsHandling = p.FlatsHandling
	}
}

// PickPlan is the plan imaged for a find in group: in the group's
// filters, Luminance first in broadband, enabled before disabled.
func PickPlan(plans []planning.Plan, group string) (planning.Plan, bool) {
	return pickPlan(plans, group)
}

// pickPlan is the plan a follow-up images: in the group's filters,
// Luminance first in broadband, enabled before disabled.
func pickPlan(plans []planning.Plan, group string) (planning.Plan, bool) {
	rank := func(p planning.Plan) int {
		g := discoveries.GroupOf(p.Filter)
		if g != group {
			return -1
		}
		r := 1
		if strings.EqualFold(p.Filter, "Luminance") || strings.EqualFold(p.Filter, "L") {
			r += 2
		}
		if p.Enabled {
			r += 4
		}
		return r
	}
	best, bestRank := planning.Plan{}, -1
	for _, p := range plans {
		if r := rank(p); r > bestRank {
			best, bestRank = p, r
		}
	}
	return best, bestRank >= 0
}

// settle compares the follow-up with the first later look at its field
// that a done run covers. It says whether the follow-up is settled.
func (s *Service) settle(ctx context.Context, f *app.DiscoveryFollowup) (bool, error) {
	var runs []app.DiscoveryRun
	if err := s.DB.WithContext(ctx).Where("object = ? AND filter_group = ? AND state = ? AND night >= ? AND night <= ? AND finished_at > ?",
		f.Object, f.Group, app.DiscoveryDone, f.Night, f.Until, f.CreatedAt).Order("night").Find(&runs).Error; err != nil {
		return false, err
	}
	if len(runs) == 0 {
		return false, nil
	}
	ours, err := s.followFrames(ctx, f)
	if err != nil {
		return false, err
	}
	for _, run := range runs {
		var cands []app.DiscoveryCandidate
		if err := s.DB.WithContext(ctx).Where("run_id = ?", run.ID).Find(&cands).Error; err != nil {
			return false, err
		}
		same := run.Night.Equal(f.Night)
		if same && !runHas(cands, ours) {
			continue
		}
		v := Judge(f, run, cands, ours, same, s.fieldRadius(ctx, f.Object, run))
		if !v.Decided {
			if v.Note != "" && v.Note != f.Look {
				s.update(ctx, f, map[string]any{"look": v.Note})
				f.Look = v.Note
			}
			continue
		}
		night := run.Night
		extra := map[string]any{"follow_night": night, "follow_run_id": run.ID, "seen_id": v.SeenID, "seen_sep_arc": v.SepArc, "seen_mag": v.Mag}
		state := app.FollowupNotConfirmed
		if v.Seen {
			state = app.FollowupConfirmed
		}
		s.finish(ctx, f, state, v.Note, extra)
		return true, nil
	}
	return false, nil
}

// fieldRadius is the radius, in degrees, of the circle inside a target's
// frame, from its master's size and the run's pixel scale; 0 when unknown.
func (s *Service) fieldRadius(ctx context.Context, object string, run app.DiscoveryRun) float64 {
	var st []app.Stack
	if err := s.DB.WithContext(ctx).Where("object = ? AND width > 0", object).Limit(1).Find(&st).Error; err != nil || len(st) == 0 || !(run.PixelScale > 0) {
		return 0
	}
	return float64(min(st[0].Width, st[0].Height)) / 2 * run.PixelScale / 3600
}

// followFrames are the ids of the follow-up project's lights.
func (s *Service) followFrames(ctx context.Context, f *app.DiscoveryFollowup) (map[int]bool, error) {
	out := map[int]bool{}
	if f.TargetGUID == "" {
		return out, nil
	}
	var ids []int
	if err := s.DB.WithContext(ctx).Model(&app.Frame{}).Where("type = ? AND ts_target = ?", "LIGHT", f.TargetGUID).Pluck("id", &ids).Error; err != nil {
		return nil, err
	}
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}

// runHas says whether any of a run's candidates was measured on one of
// the follow-up's lights, so the run holds them.
func runHas(cands []app.DiscoveryCandidate, ours map[int]bool) bool {
	if len(ours) == 0 {
		return false
	}
	for _, c := range cands {
		for _, id := range frameIDs(c) {
			if ours[id] {
				return true
			}
		}
	}
	return false
}

func frameIDs(c app.DiscoveryCandidate) []int {
	var ids []int
	var light []discoveries.LightPoint
	if c.Light != "" && json.Unmarshal([]byte(c.Light), &light) == nil {
		for _, p := range light {
			ids = append(ids, p.FrameID)
		}
	}
	var t discoveries.TrackRecord
	if c.Track != "" && json.Unmarshal([]byte(c.Track), &t) == nil {
		for _, a := range t.ADES {
			ids = append(ids, a.FrameID)
		}
	}
	return ids
}

// Verdict is what a later look says about a find.
type Verdict struct {
	Decided bool
	Seen    bool
	SeenID  *int
	SepArc  *float64
	Mag     *float64
	Note    string
}

// Judge decides whether a run saw the find again. On the find's own night
// only the follow-up's lights count: a change must show at 3 sigma in at
// least half of them, and at least 3; a moving object must have a detection
// in them. On a later night a change seen at the find's place, of the same
// sign, is the find; a moving object must sit on the path its rate and
// direction predict.
func Judge(f *app.DiscoveryFollowup, run app.DiscoveryRun, cands []app.DiscoveryCandidate, ours map[int]bool, sameNight bool, radius float64) Verdict {
	night := run.Night.Format("Jan 2")
	if f.Kind == app.DiscoveryKindMover {
		return judgeMover(f, run, cands, ours, sameNight, night, radius)
	}
	var best *app.DiscoveryCandidate
	bestSep := math.Inf(1)
	for i, c := range cands {
		if c.Kind != app.DiscoveryKindChange || c.Sign != f.Sign || (sameNight && !runHas([]app.DiscoveryCandidate{c}, ours)) {
			continue
		}
		if d := sep(c.RA, c.Dec, f.RA, f.Dec); d <= ConfirmArcsec && d < bestSep {
			best, bestSep = &cands[i], d
		}
	}
	if best == nil && sameNight {
		return Verdict{Decided: true, Note: "later the same night: not in the follow-up subs"}
	}
	if best == nil && f.Mag != nil && run.Depth != nil && *run.Depth < *f.Mag {
		return Verdict{Note: fmt.Sprintf("the night of %s reached %.1f, not deep enough for the find at %.1f", night, *run.Depth, *f.Mag)}
	}
	if best == nil {
		return Verdict{Decided: true, Note: fmt.Sprintf("not seen again on the night of %s", night)}
	}
	v := Verdict{Decided: true, SeenID: &best.ID, SepArc: &bestSep, Mag: best.NightMag}
	if !sameNight {
		v.Seen = true
		v.Note = fmt.Sprintf("seen again on the night of %s, %.1f″ from the find", night, bestSep)
		return v
	}
	var light []discoveries.LightPoint
	_ = json.Unmarshal([]byte(best.Light), &light)
	n, det := 0, 0
	for _, p := range light {
		if !ours[p.FrameID] {
			continue
		}
		n++
		if p.FluxErr > 0 && float64(f.Sign)*p.Flux >= 3*p.FluxErr {
			det++
		}
	}
	if n == 0 {
		return Verdict{}
	}
	v.Seen = det >= 3 && 2*det >= n
	v.Note = fmt.Sprintf("later the same night: shows in %d of %d follow-up subs", det, n)
	return v
}

func judgeMover(f *app.DiscoveryFollowup, run app.DiscoveryRun, cands []app.DiscoveryCandidate, ours map[int]bool, sameNight bool, night string, radius float64) Verdict {
	if f.At == nil || f.RateArcmin == nil || f.PA == nil {
		return Verdict{Note: "no track to predict the moving object's place from"}
	}
	bestSep := math.Inf(1)
	var best *app.DiscoveryCandidate
	var bestMoved float64
	for i, c := range cands {
		if c.Kind != app.DiscoveryKindMover {
			continue
		}
		var t discoveries.TrackRecord
		if json.Unmarshal([]byte(c.Track), &t) != nil {
			continue
		}
		for _, a := range t.ADES {
			if sameNight && !ours[a.FrameID] {
				continue
			}
			at, err := time.Parse("2006-01-02T15:04:05.000Z", a.ObsTime)
			if err != nil {
				continue
			}
			ra, dec, moved := Predict(f.RA, f.Dec, *f.RateArcmin, *f.PA, at.Sub(*f.At))
			tol := 10 + 0.03*moved
			if d := sep(a.RA, a.Dec, ra, dec); d <= tol && d < bestSep {
				best, bestSep, bestMoved = &cands[i], d, moved
			}
		}
	}
	if best == nil && run.MidTime != nil && radius > 0 {
		ra, dec, moved := Predict(f.RA, f.Dec, *f.RateArcmin, *f.PA, run.MidTime.Sub(*f.At))
		if sep(ra, dec, run.FieldRA, run.FieldDec) > radius*3600 {
			return Verdict{Note: fmt.Sprintf("by the night of %s it had moved %.1f′, out of the imaged field", night, moved/60)}
		}
	}
	if best == nil {
		return Verdict{Decided: true, Note: fmt.Sprintf("no moving object on the predicted path on the night of %s", night)}
	}
	return Verdict{Decided: true, Seen: true, SeenID: &best.ID, SepArc: &bestSep, Mag: best.NightMag,
		Note: fmt.Sprintf("a moving object on the predicted path on the night of %s, %.0f″ from where it should be after moving %.1f′", night, bestSep, bestMoved/60)}
}

// Predict moves a position at rate arcseconds a minute toward position
// angle pa degrees (east of north) for d, and says how far it moved in
// arcseconds.
func Predict(ra, dec, rate, pa float64, d time.Duration) (float64, float64, float64) {
	moved := rate * d.Minutes()
	r := pa * math.Pi / 180
	dDec := moved * math.Cos(r) / 3600
	cosd := math.Cos(dec * math.Pi / 180)
	dRA := 0.0
	if cosd > 1e-6 {
		dRA = moved * math.Sin(r) / 3600 / cosd
	}
	return math.Mod(ra+dRA+360, 360), dec + dDec, math.Abs(moved)
}

// close closes each follow-up project whose finds are all settled.
func (s *Service) close(ctx context.Context) error {
	if s.Commands == nil || s.Snapshot == nil {
		return nil
	}
	var rows []app.DiscoveryFollowup
	if err := s.DB.WithContext(ctx).Where("create_cmd <> '' AND close_cmd = ''").Find(&rows).Error; err != nil {
		return err
	}
	byProject := map[string][]app.DiscoveryFollowup{}
	for _, r := range rows {
		byProject[r.ProjectGUID] = append(byProject[r.ProjectGUID], r)
	}
	var snap *planning.Snapshot
	var errs []error
	for guid, rs := range byProject {
		var waiting int64
		if err := s.DB.WithContext(ctx).Model(&app.DiscoveryFollowup{}).Where("project_guid = ? AND state = ?", guid, app.FollowupWaiting).Count(&waiting).Error; err != nil {
			errs = append(errs, err)
			continue
		}
		if snap == nil {
			var err error
			if snap, err = s.Snapshot(ctx); err != nil {
				return fmt.Errorf("planning snapshot: %w", err)
			}
		}
		id := 0
		state := ""
		acquired := 0
		for _, p := range snap.Projects {
			if p.GUID != guid {
				continue
			}
			id, state = p.ID, p.State
			for _, t := range p.Targets {
				for _, pl := range t.Plans {
					acquired += pl.Acquired
				}
			}
		}
		capped := rs[0].Subs > 0 && acquired >= capFactor*rs[0].Subs
		if waiting > 0 && !capped {
			continue
		}
		if capped && waiting > 0 && id != 0 && state != "Closed" {
			slog.Info("Discovery follow-up took its cap of subs; closing its project", "object", rs[0].Object, "acquired", acquired, "asked", rs[0].Subs)
		}
		var mark string
		switch {
		case id == 0 && !s.gone(ctx, rs[0]):
			continue
		case id == 0:
			mark = "none"
		case state == "Closed":
			mark = "closed"
		default:
			raw, err := json.Marshal(schedcmd.EditPayload{Entity: "project", ID: int64(id), GUID: guid, Name: "Follow-up",
				Changes: []schedcmd.FieldChange{{Field: "state", Before: json.RawMessage(stateNumber(state)), After: json.RawMessage("3")}}})
			if err != nil {
				errs = append(errs, err)
				continue
			}
			rec, err := s.Commands.SubmitOnce(ctx, "discovery-followup-close-"+guid, schedcmd.KindProjectEdit, raw, Author)
			if err != nil {
				errs = append(errs, fmt.Errorf("close follow-up project %s: %w", guid, err))
				continue
			}
			mark = rec.ID
		}
		if err := s.DB.WithContext(ctx).Model(&app.DiscoveryFollowup{}).Where("project_guid = ? AND close_cmd = ''", guid).Update("close_cmd", mark).Error; err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// gone says whether a follow-up's project will never show up: its create
// command was refused, or its window ended a day ago.
func (s *Service) gone(ctx context.Context, f app.DiscoveryFollowup) bool {
	if s.now().After(WindowEnd(f.Until, s.Loc).Add(24 * time.Hour)) {
		return true
	}
	rec, err := s.Commands.Status(ctx, f.CreateCmd)
	return err == nil && slices.Contains([]schedcmd.Status{schedcmd.StatusRejected, schedcmd.StatusFailed, schedcmd.StatusConflict, schedcmd.StatusCancelled}, rec.Status)
}

func stateNumber(name string) string {
	for k, v := range schedcmd.ProjectStates() {
		if v == name {
			return fmt.Sprint(k)
		}
	}
	return "1"
}

// ForCandidate is the follow-up of the alert that covered a candidate, if
// any: the same night and class within 2″, or for a change on a later
// night, a follow-up whose find sits at its place.
func ForCandidate(ctx context.Context, db *gorm.DB, c app.DiscoveryCandidate) (*app.DiscoveryFollowup, error) {
	var fs []app.DiscoveryFollowup
	if err := db.WithContext(ctx).Where("object = ? AND night = ? AND class = ?", c.Object, c.Night, c.Class).Find(&fs).Error; err != nil {
		return nil, err
	}
	for i, f := range fs {
		if f.Kind == app.DiscoveryKindMover {
			var a app.DiscoveryAlert
			if err := db.WithContext(ctx).First(&a, f.AlertID).Error; err == nil && sep(a.RA, a.Dec, c.RA, c.Dec) <= 2 {
				return &fs[i], nil
			}
			continue
		}
		if sep(f.RA, f.Dec, c.RA, c.Dec) <= 2 {
			return &fs[i], nil
		}
	}
	return nil, ErrNone
}

// ErrNone says a candidate has no follow-up.
var ErrNone = errors.New("no follow-up")
