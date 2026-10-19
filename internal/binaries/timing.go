package binaries

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/binaries/minima"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/ownimage"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/period"
)

// TimingVersion is the version of the rules that time eclipses; timings
// from an older one are measured again.
const TimingVersion = 4

// Photometry measures a star in the user's own frames.
type Photometry interface {
	// Subs lists a target's registered subs, oldest first.
	Subs(ctx context.Context, object string) ([]ownimage.Sub, error)
	// Curve measures spot in subs only.
	Curve(ctx context.Context, object string, spot ownimage.Spot, subs []ownimage.Sub) (*ownimage.Curve, error)
}

// ErrNoCurve says the frames gave the star no light curve: no Gaia star
// at the spot, or too few steady stars to compare with.
var ErrNoCurve = errors.New("the frames gave the star no light curve")

// OwnPhotometry measures with the vet's own-image photometry: forced PSF
// fits against steady Gaia stars of like brightness and colour.
type OwnPhotometry struct {
	Data    ownimage.Data
	Threads int
	WorkDir string
}

// Subs lists the target's subs.
func (p OwnPhotometry) Subs(ctx context.Context, object string) ([]ownimage.Sub, error) {
	return p.Data.Subs(ctx, object)
}

// Curve measures spot in subs.
func (p OwnPhotometry) Curve(ctx context.Context, object string, spot ownimage.Spot, subs []ownimage.Sub) (*ownimage.Curve, error) {
	res, err := ownimage.Measure(ctx, onlySubs{Data: p.Data, subs: subs}, object, []ownimage.Spot{spot},
		ownimage.Options{Threads: max(1, p.Threads), WorkDir: p.WorkDir})
	if err != nil {
		return nil, err
	}
	if len(res) != 1 || res[0].Curve == nil || len(res[0].Curve.Points) == 0 {
		return nil, ErrNoCurve
	}
	return res[0].Curve, nil
}

type onlySubs struct {
	ownimage.Data
	subs []ownimage.Sub
}

func (o onlySubs) Subs(context.Context, string) ([]ownimage.Sub, error) { return o.subs, nil }

const (
	// timingsPerTick is the most eclipses measured in one look; the rest
	// wait for the next.
	timingsPerTick = 4
	// settle is how long after an eclipse's frames end before it is
	// measured, so the night's frames are all in.
	settle = 6 * time.Hour
	// reachShare is how far frames are taken either side of a predicted
	// middle, as a share of the dip's half width.
	reachShare = 2.0
	// minFramesSide and minFramesDip are the fewest frames in the dip on
	// each side of the predicted middle, and in all, to try timing it.
	minFramesSide = 3
	minFramesDip  = 8
)

// event is one predicted eclipse the user's frames cover.
type event struct {
	star  int
	kind  string
	dip   int
	cycle float64
	mid   float64
	half  float64
	utc   time.Time
	subs  []ownimage.Sub
	row   *app.EclipseTiming
	req   *app.EclipseRequest
}

func dipHalf(e *period.Ephemeris, k int) float64 {
	return e.Eclipses[k].Width * e.Period
}

func dipShape(e *period.Ephemeris, k int) func(float64) float64 {
	d := e.Eclipses[k]
	p := e.Period
	return func(dt float64) float64 { return period.Shape(dt/p, d.Width, d.Shape) }
}

// kindOf is the page's kind of dip k.
func kindOf(k, deep int) string {
	if k == deep {
		return KindPrimary
	}
	return KindSecondary
}

// events lists the eclipses of star si whose frames are all in and cover
// both sides of the predicted middle, with any timing already stored.
func events(si int, st Star, subs []ownimage.Sub, rows []app.EclipseTiming, now time.Time) []event {
	e, _ := st.ephemeris()
	if e == nil || st.Facts == nil || st.Facts.Gaia == nil || e.Period <= 0 {
		return nil
	}
	deep := st.deeper()
	ra, dec := st.Facts.Gaia.Now(now)
	type key struct {
		dip   int
		cycle float64
	}
	found := map[key]*event{}
	var order []key
	for _, s := range subs {
		b := lc.BJD(midOf(s), ra, dec)
		for k, d := range e.Eclipses {
			half := dipHalf(e, k)
			if half <= 0 {
				continue
			}
			n := math.Round((b-e.T0)/e.Period - d.Phase)
			tm := e.T0 + (n+d.Phase)*e.Period
			if math.Abs(b-tm) > reachShare*half {
				continue
			}
			kk := key{k, n}
			ev, ok := found[kk]
			if !ok {
				ev = &event{star: si, kind: kindOf(k, deep), dip: k, cycle: n, mid: tm, half: half, utc: lc.UTCOfBJD(tm, ra, dec)}
				found[kk] = ev
				order = append(order, kk)
			}
			ev.subs = append(ev.subs, s)
		}
	}
	var out []event
	for _, kk := range order {
		ev := found[kk]
		if now.Before(ev.utc.Add(time.Duration(reachShare*ev.half*86400e9) + settle)) {
			continue
		}
		before, after := 0, 0
		for _, s := range ev.subs {
			dt := lc.BJD(midOf(s), ra, dec) - ev.mid
			switch {
			case dt > -ev.half && dt < 0:
				before++
			case dt >= 0 && dt < ev.half:
				after++
			}
		}
		if before < minFramesSide || after < minFramesSide || before+after < minFramesDip {
			continue
		}
		ev.row = matchTiming(rows, st.Source.ID, ev.kind, ev.utc, e.Period)
		out = append(out, *ev)
	}
	return out
}

func midOf(s ownimage.Sub) time.Time {
	return s.Start.Add(time.Duration(s.Exposure / 2 * float64(time.Second)))
}

// matchTiming is the stored timing of the eclipse predicted at utc: same
// star and kind, its prediction within a quarter period and half a day.
func matchTiming(rows []app.EclipseTiming, id int, kind string, utc time.Time, p float64) *app.EclipseTiming {
	tol := min(time.Duration(p/4*86400e9), 12*time.Hour)
	for i := range rows {
		r := &rows[i]
		if r.SourceID == id && r.Kind == kind && r.State != app.EclipseMoved && absDur(r.Predicted.Sub(utc)) < tol {
			return r
		}
	}
	return nil
}

func (ev event) due() bool {
	return ev.row == nil || ev.row.Version != TimingVersion || ev.row.Frames != len(ev.subs)
}

// timingWork measures the eclipses the user's frames cover that are not
// measured yet, those from imaging requests first, then the newest, a
// few in each look, showing its progress in the processing indicator.
func (s *Service) timingWork(ctx context.Context, stars []Star) error {
	if s.Photometry == nil {
		return nil
	}
	var rows []app.EclipseTiming
	if err := s.DB.WithContext(ctx).Find(&rows).Error; err != nil {
		return err
	}
	var reqs []app.EclipseRequest
	if err := s.DB.WithContext(ctx).Where("state IN ?", []string{app.EclipseSent, app.EclipseDone}).Find(&reqs).Error; err != nil {
		return err
	}
	now := s.now()
	subsOf := map[string][]ownimage.Sub{}
	var todo []event
	var errs []error
	for i, st := range stars {
		obj := st.Source.Object
		subs, ok := subsOf[obj]
		if !ok {
			var err error
			if subs, err = s.Photometry.Subs(ctx, obj); err != nil {
				errs = append(errs, fmt.Errorf("subs of %s: %w", obj, err))
				continue
			}
			subsOf[obj] = subs
		}
		for _, ev := range events(i, st, subs, rows, now) {
			if !ev.due() {
				continue
			}
			ev.req = matchReq(reqs, st.Source.ID, ev.kind, ev.utc)
			todo = append(todo, ev)
		}
	}
	slices.SortStableFunc(todo, func(a, b event) int {
		if (a.req != nil) != (b.req != nil) {
			if a.req != nil {
				return -1
			}
			return 1
		}
		return b.utc.Compare(a.utc)
	})
	if len(todo) == 0 {
		s.TimingTask.SetQueued(0, "")
		return errors.Join(errs...)
	}
	run := todo[:min(len(todo), timingsPerTick)]
	s.TimingTask.Start("Timing eclipses in your own frames", int64(len(run)), "eclipses")
	s.TimingTask.SetQueued(len(todo)-len(run), "eclipses")
	for _, ev := range run {
		if ctx.Err() != nil {
			break
		}
		st := stars[ev.star]
		name, _ := st.Names()
		s.TimingTask.SetCurrent(fmt.Sprintf("%s, %s of %s", name, eclipseWords(ev.kind), nightOf(ev.utc, s.loc())))
		if err := s.measure(ctx, st, ev); err != nil {
			errs = append(errs, fmt.Errorf("source %d eclipse of %s: %w", st.Source.ID, ev.utc.Format(time.RFC3339), err))
		}
		s.TimingTask.Step(1)
	}
	err := errors.Join(errs...)
	s.TimingTask.Finish(err)
	return err
}

func eclipseWords(kind string) string {
	if kind == KindPrimary {
		return "the deeper eclipse"
	}
	return "the second eclipse"
}

// nightOf is the local date the night of t began on.
func nightOf(t time.Time, loc *time.Location) string {
	return t.In(loc).Add(-12 * time.Hour).Format(time.DateOnly)
}

func matchReq(reqs []app.EclipseRequest, id int, kind string, utc time.Time) *app.EclipseRequest {
	var best *app.EclipseRequest
	for i := range reqs {
		r := &reqs[i]
		if r.SourceID == id && r.Kind == kind && r.State != app.EclipseMoved && absDur(r.Mid.Sub(utc)) <= requestMatch && (best == nil || r.ID > best.ID) {
			best = r
		}
	}
	return best
}

// measure times one eclipse in each filter the frames hold and keeps the
// filter that times it best.
func (s *Service) measure(ctx context.Context, st Star, ev event) error {
	row, err := s.timeEvent(ctx, st, ev)
	if err != nil {
		return err
	}
	if err := s.DB.WithContext(ctx).Save(&row).Error; err != nil {
		return err
	}
	if ev.req != nil && ev.req.State == app.EclipseDone {
		ev.req.Note = requestNote(row, ev)
		ev.req.UpdatedAt = s.now()
		if err := s.DB.WithContext(ctx).Save(ev.req).Error; err != nil {
			slog.Warn("Could not note the timing on its request", "request", ev.req.ID, "error", err)
		}
	}
	return nil
}

// DryTimings times every eclipse the user's frames cover for the
// confirmed stars (ids, or all) and returns their timing records,
// writing nothing.
func (s *Service) DryTimings(ctx context.Context, ids []int) ([]TimingRecord, error) {
	if s.Photometry == nil {
		return nil, errors.New("no photometry: the local Gaia copy is off")
	}
	stars, err := s.Binaries(ctx)
	if err != nil {
		return nil, err
	}
	now := s.now()
	out := []TimingRecord{}
	for i, st := range stars {
		if len(ids) > 0 && !slices.Contains(ids, st.Source.ID) {
			continue
		}
		subs, err := s.Photometry.Subs(ctx, st.Source.Object)
		if err != nil {
			return out, err
		}
		st.Timings = nil
		for _, ev := range events(i, st, subs, nil, now) {
			row, err := s.timeEvent(ctx, st, ev)
			if err != nil {
				row.Note = "Not timed: " + err.Error()
			}
			row.ID = len(st.Timings) + 1
			st.Timings = append(st.Timings, row)
		}
		out = append(out, st.Record(s.loc()))
	}
	return out, nil
}

// timeEvent measures one eclipse and times it, writing nothing.
func (s *Service) timeEvent(ctx context.Context, st Star, ev event) (app.EclipseTiming, error) {
	row := app.EclipseTiming{SourceID: st.Source.ID, Kind: ev.kind, Predicted: ev.utc, Night: nightOf(ev.utc, s.loc()),
		Frames: len(ev.subs), Version: TimingVersion, MeasuredAt: s.now(), State: app.TimingNone}
	if ev.row != nil {
		row.ID = ev.row.ID
	}
	if ev.req != nil {
		id := ev.req.ID
		row.RequestID = &id
	}
	curve, err := s.Photometry.Curve(ctx, st.Source.Object, spotOf(st, ev), ev.subs)
	switch {
	case errors.Is(err, ErrNoCurve):
		row.Note = "Not timed: the frames gave the star no light curve to time."
	case err != nil:
		return row, err
	default:
		m, filter, why, ok := bestMinimum(curve, dipOf(st, ev))
		if !ok {
			row.Note = "Not timed: " + why + "." + unmeasured(curve, ev, st)
			break
		}
		row.BJD, row.Err, row.Method, row.Filter, row.Points, row.Depth = m.T, m.Err, m.Method, filter, m.Points, m.Depth
		row.State = app.TimingGood
		if m.Check != nil {
			row.CheckBJD, row.CheckErr = m.Check.T, m.Check.Err
		}
		if !m.Agree {
			row.State = app.TimingDoubtful
			row.Note = doubtNote(m)
		}
	}
	return row, nil
}

// doubtNote says why a timing whose two methods disagree is left out of
// the trends.
func doubtNote(m minima.Minimum) string {
	gap := (m.T - m.Check.T) * 1440
	if s := m.Spread; s != nil {
		return fmt.Sprintf("The two ways of finding the middle differ by %.1f minutes; on nights made up to match this one they differ by %.1f ± %.1f minutes, so this timing is left out of the trends.",
			gap, s.Mean*1440, s.Sigma()*1440)
	}
	return fmt.Sprintf("The two ways of finding the middle disagree by %.1f minutes, more than their errors allow, so this timing is left out of the trends.", math.Abs(gap))
}

func spotOf(st Star, ev event) ownimage.Spot {
	ra, dec := st.Facts.Gaia.Now(ev.utc)
	g := st.Facts.Gaia.G
	return ownimage.Spot{SourceID: st.Source.ID, Object: st.Source.Object, Kind: st.Source.Kind, RA: ra, Dec: dec, GaiaG: &g}
}

func dipOf(st Star, ev event) minima.Dip {
	e, _ := st.ephemeris()
	return minima.Dip{Mid: ev.mid, Half: ev.half, Shape: dipShape(e, ev.dip)}
}

// unmeasured is a sentence on the event's frames that gave no
// measurement of the star, those in the dip counted apart, or "".
func unmeasured(c *ownimage.Curve, ev event, st Star) string {
	got := map[int]bool{}
	for _, p := range c.Points {
		got[p.FrameID] = true
	}
	ra, dec := st.Facts.Gaia.Now(ev.utc)
	lost, inDip := 0, 0
	for _, sub := range ev.subs {
		if got[sub.FrameID] {
			continue
		}
		lost++
		if math.Abs(lc.BJD(midOf(sub), ra, dec)-ev.mid) < ev.half {
			inDip++
		}
	}
	if lost == 0 {
		return ""
	}
	return fmt.Sprintf(" %d of the %d frames gave no measurement of the star, %d of them inside the dip.", lost, len(ev.subs), inDip)
}

func requestNote(row app.EclipseTiming, ev event) string {
	if row.State == app.TimingNone {
		return "The window has passed. " + row.Note
	}
	oc := (row.BJD - ev.mid) * 1440
	return fmt.Sprintf("The window has passed. Timed in %s: the middle came %s, give or take %.1f minutes.", row.Filter, earlyLate(oc), row.Err*1440)
}

// earlyLate is a time offset in minutes in words: "2.3 minutes late".
func earlyLate(minutes float64) string {
	switch {
	case math.Abs(minutes) < 0.05:
		return "on time"
	case minutes > 0:
		return fmt.Sprintf("%.1f minutes late", minutes)
	}
	return fmt.Sprintf("%.1f minutes early", -minutes)
}

// attempt is one timing bestMinimum tried: one filter's points, or all
// filters' together.
type attempt struct {
	Filter string
	Joint  bool
	Points int
	OK     bool
	Why    string
	Min    minima.Minimum
}

// attempts times the dip in all filters together, each with its own
// normal level and depth, and in each filter's points alone, the
// filters with the most points first.
func attempts(c *ownimage.Curve, d minima.Dip) []attempt {
	byFilter := map[string][]minima.Point{}
	for _, p := range c.Points {
		byFilter[p.Filter] = append(byFilter[p.Filter], minima.Point{T: p.BJD, Y: p.DMag, E: p.Err})
	}
	filters := sortedKeys(byFilter)
	slices.SortStableFunc(filters, func(a, b string) int { return cmp.Compare(len(byFilter[b]), len(byFilter[a])) })
	var out []attempt
	try := func(name string, joint bool, pts []minima.Point) {
		m, why, ok := minima.Time(pts, d)
		out = append(out, attempt{Filter: name, Joint: joint, Points: len(pts), OK: ok, Why: why, Min: m})
	}
	if len(filters) > 1 {
		var all []minima.Point
		for g, f := range filters {
			for _, p := range byFilter[f] {
				p.G = g
				all = append(all, p)
			}
		}
		try(strings.Join(filters, " + "), true, all)
	}
	for _, f := range filters {
		try(f, false, byFilter[f])
	}
	return out
}

// bestMinimum keeps the attempt that times the dip best: one whose two
// methods agree, then the smallest error. When none times it, why is
// each filter's own reason, or the one reason they share.
func bestMinimum(c *ownimage.Curve, d minima.Dip) (minima.Minimum, string, string, bool) {
	tries := attempts(c, d)
	var best minima.Minimum
	bestF, ok := "", false
	for _, a := range tries {
		if !a.OK {
			continue
		}
		m := a.Min
		if !ok || (m.Agree && !best.Agree) || (m.Agree == best.Agree && m.Err < best.Err) {
			best, bestF, ok = m, a.Filter, true
		}
	}
	if ok {
		return best, bestF, "", true
	}
	return best, "", failWhy(tries), false
}

// failWhy is why no attempt timed the dip: the one reason every filter
// alone shares, or each filter's own. When every filter alone has too
// few frames, it gives how many each has and why all of them together
// gave no timing.
func failWhy(tries []attempt) string {
	var alone []attempt
	var joint *attempt
	for i, a := range tries {
		if a.Joint {
			joint = &tries[i]
		} else {
			alone = append(alone, a)
		}
	}
	switch {
	case len(alone) == 0:
		return minima.ReasonFew
	case len(alone) == 1:
		return alone[0].Why
	}
	same := true
	for _, a := range alone[1:] {
		same = same && a.Why == alone[0].Why
	}
	if same && alone[0].Why == minima.ReasonFew && joint != nil {
		counts := make([]string, len(alone))
		for i, a := range alone {
			counts[i] = fmt.Sprintf("%s %d", a.Filter, a.Points)
		}
		return fmt.Sprintf("%s in any one filter (%s measured), and with all filters together %s", minima.ReasonFew, strings.Join(counts, ", "), joint.Why)
	}
	if same {
		return alone[0].Why
	}
	parts := make([]string, len(alone))
	for i, a := range alone {
		parts[i] = "in " + a.Filter + ", " + a.Why
	}
	return strings.Join(parts, "; ")
}
