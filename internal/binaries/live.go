package binaries

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strings"
	"time"

	eventbus "github.com/USA-RedDragon/astro-stacker/internal/events"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/trigger"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/ownimage"
	"gorm.io/gorm"
)

// The live card's kinds.
const (
	LiveEclipse  = "eclipse"
	LiveDipWatch = "dip-watch"
)

const (
	// livePerLook is the most subs measured in one look; the rest wait
	// for the next, a few seconds later.
	livePerLook = 6
	// liveEvery is how often the scheduler's current target is checked
	// when no sub arrives.
	liveEvery = time.Minute
	// liveMissFor is how long a project found to be neither an eclipse
	// request nor a dip watch is not looked up again.
	liveMissFor = 10 * time.Minute
	// curveSamples is how many times the expected curve is drawn at.
	curveSamples = 160
)

// LiveTarget is the scheduler's current target.
type LiveTarget struct {
	ProjectGUID string
	ProjectName string
	TargetName  string
}

// LiveCurve is the Now page's live card: the expected curve of the
// current eclipse request or dip watch and the subs measured so far.
type LiveCurve struct {
	Kind        string    `json:"kind"`
	ProjectGUID string    `json:"project_guid"`
	ProjectName string    `json:"project_name"`
	Object      string    `json:"object"`
	SourceID    int       `json:"source_id"`
	Name        string    `json:"name"`
	State       string    `json:"state"`
	Why         string    `json:"why"`
	Note        string    `json:"note,omitempty"`
	Filter      string    `json:"filter,omitempty"`
	OtherSubs   int       `json:"other_subs"`
	From        time.Time `json:"from"`
	To          time.Time `json:"to"`
	// Points are the subs measured, in magnitudes fainter than the
	// star's normal level, with the nearby stars together beside them.
	Points      []LivePoint `json:"points"`
	Comps       int         `json:"comps"`
	CompScatter *float64    `json:"comp_scatter,omitempty"`
	// CompShift is how much fainter than usual the nearby stars read
	// together over the latest subs, a dimming taken out of the star's
	// points, and CompSpread how far they scatter about each other.
	CompShift  *float64         `json:"comp_shift,omitempty"`
	CompSpread *float64         `json:"comp_spread,omitempty"`
	Scatter    *float64         `json:"scatter,omitempty"`
	LastSub    *time.Time       `json:"last_sub,omitempty"`
	Eclipse    *LiveEclipseView `json:"eclipse,omitempty"`
	Dip        *LiveDipView     `json:"dip,omitempty"`
	UpdatedAt  time.Time        `json:"updated_at"`
	// Stored says the card is the stored verdict of the night Night, the
	// evening's date, shown until the next night's imaging starts.
	Stored bool   `json:"stored,omitempty"`
	Night  string `json:"night,omitempty"`
	// LowFrom is when the star dropped below the planner's height limit
	// while subs were still taken.
	LowFrom *time.Time `json:"low_from,omitempty"`
}

// LiveEclipseView is the expected eclipse and how the subs time it.
type LiveEclipseView struct {
	Kind     string        `json:"kind"`
	Mid      time.Time     `json:"mid"`
	ErrMin   float64       `json:"err_min"`
	Start    time.Time     `json:"start"`
	End      time.Time     `json:"end"`
	Depth    float64       `json:"depth"`
	Band     string        `json:"band"`
	Basis    string        `json:"basis"`
	Curve    []CurveSample `json:"curve"`
	ShiftMin *float64      `json:"shift_min,omitempty"`
	ShiftErr *float64      `json:"shift_err_min,omitempty"`
	FitDepth *float64      `json:"fit_depth,omitempty"`
	DepthErr *float64      `json:"fit_depth_err,omitempty"`
	Planned  time.Time     `json:"planned_end"`
}

// CurveSample is the expected curve at one time, with the range the
// timing error allows.
type CurveSample struct {
	T  time.Time `json:"t"`
	V  float64   `json:"v"`
	Lo float64   `json:"lo"`
	Hi float64   `json:"hi"`
}

// LiveDipView is a dip watch's window and what the subs show.
type LiveDipView struct {
	Start       time.Time  `json:"start"`
	End         time.Time  `json:"end"`
	DepthLow    float64    `json:"depth_low"`
	DepthHigh   float64    `json:"depth_high"`
	ActiveShare float64    `json:"active_share"`
	Latest      *float64   `json:"latest,omitempty"`
	Smallest    *float64   `json:"smallest,omitempty"`
	OverMin     *float64   `json:"over_min,omitempty"`
	DipStart    *time.Time `json:"dip_start,omitempty"`
	DipDepth    *float64   `json:"dip_depth,omitempty"`
	// Run is how many subs in a row one reading averages, and Bins the
	// subs averaged that many at a time from the first sub, the last
	// holding what is left.
	Run  int       `json:"run,omitempty"`
	Bins []LiveBin `json:"bins,omitempty"`
}

// LiveBin is a run of subs averaged: its middle and ends, its weighted
// mean in magnitudes fainter than the normal level, the error of that
// mean, and how many subs it holds.
type LiveBin struct {
	Time  time.Time `json:"time"`
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
	Mag   float64   `json:"mag"`
	Err   float64   `json:"err"`
	Subs  int       `json:"subs"`
}

// liveRun is the run being followed: what it expects and the star's
// measurements so far.
type liveRun struct {
	guid    string
	object  string
	from    time.Time
	to      time.Time
	spot    ownimage.Spot
	model   *eclipseModel
	dip     *app.DipWatch
	goal    dipGoal
	tracker *ownimage.Live
	view    LiveCurve
	dirty   bool
}

// KickLive asks for a look at the live card soon: object's subs changed.
func (s *Service) KickLive(object string) {
	s.liveMu.Lock()
	run := s.live
	s.liveMu.Unlock()
	if run != nil && run.object == object {
		s.liveDebouncer().Kick("subs")
	}
}

func (s *Service) liveDebouncer() *trigger.Debouncer {
	s.liveOnce.Do(func() { s.liveKick = &trigger.Debouncer{Quiet: 10 * time.Second, MaxWait: time.Minute} })
	return s.liveKick
}

// RunLive follows the current target while it is an eclipse request or a
// dip watch, measuring each sub as it is registered, until ctx ends.
func (s *Service) RunLive(ctx context.Context) {
	if s.Current == nil || s.LiveData == nil {
		return
	}
	s.liveDebouncer().Run(ctx, liveEvery, func(ctx context.Context, keys []string) {
		more, err := s.liveLook(ctx, len(keys) > 0)
		if err != nil {
			slog.Warn("Live curve", "error", err)
		}
		if more {
			s.liveDebouncer().Kick("more")
		}
	})
}

// Live is the live card: the run being followed, else the latest stored
// night until the next night's imaging starts, else the last run. ok is
// false when there is none.
func (s *Service) Live(ctx context.Context) (LiveCurve, bool) {
	s.liveMu.Lock()
	run := s.live
	var v LiveCurve
	if run != nil {
		v = run.view
		v.Points = slices.Clone(v.Points)
	}
	s.liveMu.Unlock()
	if run != nil && s.following(run.guid) {
		return v, true
	}
	if st, ok := s.storedCard(ctx); ok {
		return st, true
	}
	return v, run != nil
}

// liveLook starts following the current target when it is a run, and
// measures up to livePerLook of its new subs. more says subs wait.
func (s *Service) liveLook(ctx context.Context, subsChanged bool) (bool, error) {
	cur := s.Current()
	s.liveMu.Lock()
	run := s.live
	s.liveMu.Unlock()
	if cur == nil || cur.ProjectGUID == "" {
		s.endRun(ctx, run)
		return false, nil
	}
	fresh := false
	if run == nil || run.guid != cur.ProjectGUID {
		s.endRun(ctx, run)
		s.liveMu.Lock()
		until, ok := s.liveMiss[cur.ProjectGUID]
		s.liveMu.Unlock()
		if ok && s.now().Before(until) {
			if run != nil {
				s.setLive(nil)
			}
			return false, nil
		}
		next, err := s.newRun(ctx, cur)
		if err != nil {
			return false, err
		}
		if next == nil {
			s.liveMu.Lock()
			if s.liveMiss == nil {
				s.liveMiss = map[string]time.Time{}
			}
			s.liveMiss[cur.ProjectGUID] = s.now().Add(liveMissFor)
			s.liveMu.Unlock()
			s.setLive(nil)
			return false, nil
		}
		run, fresh = next, true
		s.setLive(run)
	}
	if !fresh && !subsChanged && s.now().Sub(run.view.UpdatedAt) < liveEvery {
		return false, nil
	}
	return s.measureRun(ctx, run)
}

func (s *Service) setLive(run *liveRun) {
	s.liveMu.Lock()
	s.live = run
	s.liveMu.Unlock()
	if run != nil {
		s.Events.Publish(eventbus.Event{Type: eventbus.TypeLiveCurve, Object: run.object})
	} else {
		s.Events.Publish(eventbus.Event{Type: eventbus.TypeLiveCurve})
	}
}

// newRun is the run of the current target's project: an eclipse request
// or a dip watch, or nil when it is neither.
func (s *Service) newRun(ctx context.Context, cur *LiveTarget) (*liveRun, error) {
	db := s.DB.WithContext(ctx)
	var req app.EclipseRequest
	err := db.Where("project_guid = ?", cur.ProjectGUID).Order("id DESC").First(&req).Error
	if err == nil && req.Kind != KindDip {
		return s.eclipseRun(ctx, cur, req)
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	var w app.DipWatch
	err = db.Where("project_guid = ?", cur.ProjectGUID).First(&w).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.dipRun(ctx, cur, w)
}

func (s *Service) eclipseRun(ctx context.Context, cur *LiveTarget, req app.EclipseRequest) (*liveRun, error) {
	stars, err := s.load(ctx, []int{req.SourceID})
	if err != nil {
		return nil, err
	}
	run := &liveRun{guid: cur.ProjectGUID, from: req.Start, to: req.End,
		view: LiveCurve{Kind: LiveEclipse, ProjectGUID: cur.ProjectGUID, ProjectName: cur.ProjectName, SourceID: req.SourceID,
			Name: fmt.Sprint(req.SourceID), From: req.Start, To: req.End}}
	if len(stars) == 0 {
		run.view.State, run.view.Note = LiveEarly, "The star is no longer a confirmed variable star, so there is no expected curve."
		return run, nil
	}
	st := stars[0]
	run.object, run.view.Object = st.Source.Object, st.Source.Object
	e, _ := st.ephemeris()
	if e == nil || e.Period <= 0 || st.Facts == nil || st.Facts.Gaia == nil || len(e.Eclipses) == 0 {
		run.view.State, run.view.Note = LiveEarly, "The star has no worked out ephemeris yet, so there is no expected curve."
		return run, nil
	}
	ra, dec := st.Facts.Gaia.Now(req.Mid)
	var pred *Predicted
	for _, p := range Predict(e, st.deeper(), ra, dec, req.Mid.Add(-24*time.Hour), req.Mid.Add(24*time.Hour)) {
		if p.Kind == req.Kind && (pred == nil || absDur(p.Mid.Sub(req.Mid)) < absDur(pred.Mid.Sub(req.Mid))) {
			q := st.withPhaseError(p, e)
			pred = &q
		}
	}
	if pred == nil {
		run.view.State, run.view.Note = LiveEarly, "The ephemeris no longer predicts this eclipse, so there is no expected curve."
		return run, nil
	}
	k := pred.Dip
	half := time.Duration(dipHalf(e, k) * 86400e9)
	shape := dipShape(e, k)
	m := eclipseModel{mid: pred.Mid, err: pred.Err, half: half, shape: func(dt time.Duration) float64 { return shape(dt.Hours() / 24) }}
	run.model = &m
	run.spot = spotOf(st, event{utc: pred.Mid})
	depth, band := depthIn(e.Eclipses[k].Depth, req.Filter)
	run.model.depth = depth
	run.view.Eclipse = &LiveEclipseView{Kind: req.Kind, Mid: pred.Mid, ErrMin: pred.Err.Minutes(), Start: pred.Mid.Add(-half), End: pred.Mid.Add(half),
		Depth: depth, Band: band, Planned: req.End, Basis: surveysOf(e.Eclipses[k].Depth)}
	return run, s.startTracker(ctx, run)
}

func (s *Service) dipRun(ctx context.Context, cur *LiveTarget, w app.DipWatch) (*liveRun, error) {
	var src app.DiscoverySource
	if err := s.DB.WithContext(ctx).First(&src, w.SourceID).Error; err != nil {
		return nil, fmt.Errorf("dip watch source %d: %w", w.SourceID, err)
	}
	goal := goalOf(w.DepthLow, 0)
	var e app.DipperEphemeris
	err := s.DB.WithContext(ctx).Where("source_id = ?", w.SourceID).First(&e).Error
	switch {
	case err == nil:
		goal = goalOf(w.DepthLow, e.DurLowHours)
	case !errors.Is(err, gorm.ErrRecordNotFound):
		return nil, fmt.Errorf("dipper of source %d: %w", w.SourceID, err)
	}
	run := &liveRun{guid: cur.ProjectGUID, object: src.Object, from: w.Start, to: w.End, dip: &w, goal: goal,
		spot: ownimage.Spot{SourceID: src.ID, Object: src.Object, Kind: src.Kind, RA: src.RA, Dec: src.Dec, GaiaG: src.GaiaG},
		view: LiveCurve{Kind: LiveDipWatch, ProjectGUID: cur.ProjectGUID, ProjectName: cur.ProjectName, Object: src.Object, SourceID: src.ID,
			Name: fmt.Sprint(src.ID), From: w.Start, To: w.End,
			Dip: &LiveDipView{Start: w.Start, End: w.End, DepthLow: w.DepthLow, DepthHigh: w.DepthHigh, ActiveShare: w.ActiveShare}}}
	return run, s.startTracker(ctx, run)
}

// startTracker reads the run's frame and Gaia stars once; a star with no
// catalogued match or outside the frame is noted on the card.
func (s *Service) startTracker(ctx context.Context, run *liveRun) error {
	t, err := ownimage.NewLive(ctx, s.LiveData, run.object, run.spot)
	switch {
	case errors.Is(err, ownimage.ErrNoStar):
		run.view.State, run.view.Note = LiveEarly, "Not measured: the target's frame has no catalogued star at this position."
		return nil
	case err != nil:
		run.view.State, run.view.Note = LiveEarly, "Not measured yet: "+err.Error()+"."
		return nil
	}
	run.tracker = t
	run.view.State, run.view.Why = LiveEarly, "no sub is measured yet"
	return nil
}

// measureRun measures up to livePerLook of the run's registered subs not
// yet measured, showing the work in the processing indicator, and
// refreshes the card after each.
func (s *Service) measureRun(ctx context.Context, run *liveRun) (bool, error) {
	if run.tracker == nil {
		return false, nil
	}
	subs, err := s.LiveData.Subs(ctx, run.object)
	if err != nil {
		return false, fmt.Errorf("subs of %s: %w", run.object, err)
	}
	var todo []ownimage.Sub
	for _, sub := range subs {
		if mid := sub.Start.Add(time.Duration(sub.Exposure / 2 * float64(time.Second))); !mid.Before(run.from) && !mid.After(run.to) && !run.tracker.Has(sub.FrameID) {
			todo = append(todo, sub)
		}
	}
	if len(todo) == 0 {
		s.refreshLive(run)
		return false, nil
	}
	n := min(len(todo), livePerLook)
	s.LiveTask.Start("Measuring "+run.view.ProjectName, int64(n), "subs")
	s.LiveTask.SetQueued(len(todo)-n, "subs")
	var errs []error
	for _, sub := range todo[:n] {
		if ctx.Err() != nil {
			break
		}
		if err := run.tracker.Add(ctx, s.LiveData, sub); err != nil {
			errs = append(errs, fmt.Errorf("sub %d: %w", sub.FrameID, err))
		}
		run.dirty = true
		s.LiveTask.Step(1)
		s.refreshLive(run)
	}
	err = errors.Join(errs...)
	s.LiveTask.Finish(err)
	s.LiveTask.SetQueued(0, "")
	return len(todo) > n && ctx.Err() == nil, err
}

// refreshLive works the card out again from the run's measurements and
// tells the pages.
func (s *Service) refreshLive(run *liveRun) {
	pts, kept, _ := run.tracker.Points()
	s.liveMu.Lock()
	run.view = liveView(run, pts, kept, s.now())
	s.liveMu.Unlock()
	s.Events.Publish(eventbus.Event{Type: eventbus.TypeLiveCurve, Object: run.object})
}

// liveView is the card for the run's points, raw magnitudes on an
// arbitrary zero, with kept nearby stars.
func liveView(run *liveRun, raw []ownimage.LivePoint, kept int, now time.Time) LiveCurve {
	v := run.view
	v.UpdatedAt, v.Comps, v.Points, v.Filter, v.OtherSubs = now, kept, nil, "", 0
	v.LastSub, v.Scatter, v.CompScatter, v.CompShift, v.CompSpread = nil, nil, nil, nil, nil
	var pts []LivePoint
	pts, v.Filter, v.OtherSubs = mainFilter(raw)
	if len(pts) > 0 {
		t := pts[len(pts)-1].Time
		v.LastSub = &t
	}
	switch {
	case run.model != nil:
		v.Eclipse = eclipseView(*v.Eclipse, *run.model, run.from, run.to)
		j := judgeEclipse(pts, *run.model)
		v.State, v.Why = j.state, j.why
		zero := j.zero
		if !j.ok {
			zero = levelOutside(pts, *run.model)
		} else {
			sh, se, d, de := j.shift.Minutes(), j.shiftErr.Minutes(), j.depth, j.depthErr
			v.Eclipse.ShiftMin, v.Eclipse.ShiftErr, v.Eclipse.FitDepth, v.Eclipse.DepthErr = &sh, &se, &d, &de
		}
		if j.ok && j.scatter > 0 {
			sc := j.scatter
			v.Scatter = &sc
		}
		v.Points = shifted(pts, zero)
		if len(pts) >= 2 {
			v.CompScatter, v.CompShift, v.CompSpread = compsOf(j.comps)
		}
	case run.dip != nil:
		j := judgeDip(pts, run.goal)
		v.State, v.Why = j.state, j.why
		v.Points = shifted(pts, j.zero)
		d := *v.Dip
		d.Latest, d.Smallest, d.OverMin, d.DipStart, d.DipDepth, d.Run, d.Bins = nil, nil, nil, nil, nil, 0, nil
		if len(pts) >= max(liveMinSubs, j.run) {
			l, sm, sc := j.latest, j.smallest, j.scatter
			d.Latest, d.Smallest, v.Scatter = &l, &sm, &sc
			if j.over > 0 {
				o := j.over.Minutes()
				d.OverMin = &o
			}
			d.Run, d.Bins = j.run, j.bins(pts)
		}
		if len(pts) >= liveMinSubs {
			v.CompScatter, v.CompShift, v.CompSpread = compsOf(j.comps)
		}
		if j.found {
			st, dd := j.start, j.depth
			d.DipStart, d.DipDepth = &st, &dd
		}
		v.Dip = &d
	}
	if v.Note != "" && len(pts) == 0 {
		v.State, v.Why = LiveEarly, ""
	}
	return v
}

// compsOf is the nearby stars' scatter, shift and spread for the card.
func compsOf(c compsCheck) (*float64, *float64, *float64) {
	sc, sh, sp := c.scatter, c.shift, c.spread
	return &sc, &sh, &sp
}

// bins is the subs averaged a run at a time, counted from the first so
// every sub is in an average; the last may hold fewer. Each average's
// error is that of its subs' weighted mean, scaled up by how far the subs
// scatter beyond their own errors.
func (j *dipFit) bins(pts []LivePoint) []LiveBin {
	var out []LiveBin
	for start := 0; start < len(pts); start += j.run {
		run := pts[start:min(len(pts), start+j.run)]
		a, b := run[0].Time, run[len(run)-1].Time
		mean, err := j.weighted(run)
		out = append(out, LiveBin{Time: a.Add(b.Sub(a) / 2), Start: a, End: b, Mag: mean, Err: err, Subs: len(run)})
	}
	return out
}

// weighted is the subs' weighted mean against the normal level and its
// error, scaled by the square root of the reduced chi-square when the
// subs scatter more than their errors say.
func (j *dipFit) weighted(pts []LivePoint) (float64, float64) {
	var sw float64
	for _, p := range pts {
		sw += 1 / sq(math.Max(p.Err, errFloor))
	}
	mean := j.mean(pts)
	err := 1 / math.Sqrt(sw)
	if len(pts) > 1 {
		var chi float64
		for _, p := range pts {
			chi += sq((p.Mag - j.zero - mean) / math.Max(p.Err, errFloor))
		}
		err *= math.Max(1, math.Sqrt(chi/float64(len(pts)-1)))
	}
	return mean, err
}

func shifted(pts []LivePoint, zero float64) []LivePoint {
	out := make([]LivePoint, len(pts))
	for i, p := range pts {
		p.Mag -= zero
		out[i] = p
	}
	return out
}

// levelOutside is the star's level from the subs outside the expected
// dip, or from all of them when none is.
func levelOutside(pts []LivePoint, m eclipseModel) float64 {
	var out, all []float64
	for _, p := range pts {
		all = append(all, p.Mag)
		if m.at(p.Time, 0) == 0 && absDur(p.Time.Sub(m.mid)) > m.half+m.err {
			out = append(out, p.Mag)
		}
	}
	if len(out) > 0 {
		return medianOf(out)
	}
	if len(all) == 0 {
		return 0
	}
	return medianOf(all)
}

// eclipseView samples the expected curve over the run, with the range
// the curve covers when its middle moves within the timing error.
func eclipseView(v LiveEclipseView, m eclipseModel, from, to time.Time) *LiveEclipseView {
	from, to = minTime(from, m.mid.Add(-m.half)), maxTime(to, m.mid.Add(m.half))
	v.Curve = make([]CurveSample, 0, curveSamples+1)
	step := to.Sub(from) / curveSamples
	for k := range curveSamples + 1 {
		t := from.Add(time.Duration(k) * step)
		a, b := m.depth*m.at(t, -m.err), m.depth*m.at(t, m.err)
		hi := math.Max(a, b)
		if absDur(t.Sub(m.mid)) <= m.err {
			hi = m.depth * m.at(m.mid, 0)
		}
		v.Curve = append(v.Curve, CurveSample{T: t, V: m.depth * m.at(t, 0), Lo: math.Min(a, b), Hi: hi})
	}
	return &v
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// depthIn is the dip's depth in the survey band nearest filter, and the
// band's name, or the mean over the bands it was measured in and "".
func depthIn(d map[string]float64, filter string) (float64, string) {
	band := ""
	switch strings.ToLower(filter) {
	case "red", "r", "luminance", "l":
		band = "r"
	case "green", "g", "blue", "b":
		band = "g"
	}
	if v, ok := d["ZTF "+band]; ok && band != "" {
		return v, band
	}
	var sum float64
	for _, v := range d {
		sum += v
	}
	if len(d) == 0 {
		return 0, ""
	}
	return sum / float64(len(d)), ""
}

// surveysOf names the surveys whose bands measured the dip, "ZTF, ATLAS
// and TESS".
func surveysOf(d map[string]float64) string {
	var out []string
	for k := range d {
		name, _, _ := strings.Cut(k, " ")
		if !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return joinAnd(out)
}
