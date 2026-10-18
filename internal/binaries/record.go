package binaries

import (
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/binaries/minima"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/period"
)

// TimingView is one timed eclipse as the pages show it. BJD is the
// middle in barycentric Julian date on the TDB scale, the ephemeris's
// own; HJD the same middle as a heliocentric Julian date on the UTC
// scale, as VSX and most observers quote it.
type TimingView struct {
	ID           int       `json:"id"`
	Kind         string    `json:"kind"`
	KindLabel    string    `json:"kind_label"`
	Night        string    `json:"night"`
	Mid          time.Time `json:"mid"`
	Filter       string    `json:"filter"`
	BJD          float64   `json:"bjd_tdb"`
	HJD          float64   `json:"hjd_utc"`
	ErrMinutes   float64   `json:"err_minutes"`
	LateMinutes  float64   `json:"late_minutes"`
	LateText     string    `json:"late_text"`
	Cycle        int       `json:"cycle"`
	Points       int       `json:"points"`
	Method       string    `json:"method"`
	CheckMinutes *float64  `json:"check_minutes"`
	Used         bool      `json:"used"`
	State        string    `json:"state"`
	Note         string    `json:"note"`
	RequestID    *int      `json:"request_id"`
}

// LatePoint is one timing on the plot: minutes late against the
// prediction, by year.
type LatePoint struct {
	Year       float64 `json:"year"`
	Kind       string  `json:"kind"`
	Minutes    float64 `json:"minutes"`
	ErrMinutes float64 `json:"err_minutes"`
	Used       bool    `json:"used"`
}

// LateLine is the adopted fit on the plot for one kind of eclipse.
type LateLine struct {
	Kind   string       `json:"kind"`
	Points [][2]float64 `json:"points"`
}

// TrendView is one trend the timings are tested for: State flag, clear
// or need, the finding in plain words and what it rests on.
type TrendView struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	State string `json:"state"`
	Why   string `json:"why"`
	Basis string `json:"basis"`
}

// Updated is the ephemeris the timings call for when it needs updating.
type Updated struct {
	PeriodText string  `json:"period_text"`
	Period     float64 `json:"period"`
	PeriodErr  float64 `json:"period_err"`
	EpochBJD   float64 `json:"epoch_bjd_tdb"`
}

// TimingRecord is one confirmed binary's eclipse timings from the user's
// own frames, against the ephemeris the pages use, and the trends they
// show.
type TimingRecord struct {
	SourceID   int          `json:"source_id"`
	Name       string       `json:"name"`
	PeriodText string       `json:"period_text"`
	EpochBJD   float64      `json:"epoch_bjd_tdb"`
	EpochHJD   float64      `json:"epoch_hjd_utc"`
	Summary    string       `json:"summary"`
	Timings    []TimingView `json:"timings"`
	Untimed    []TimingView `json:"untimed"`
	Points     []LatePoint  `json:"points"`
	Lines      []LateLine   `json:"lines"`
	Trends     []TrendView  `json:"trends"`
	Updated    *Updated     `json:"updated"`
	Season     *SeasonView  `json:"season"`
}

// TimingSummary is the timing line of a My discoveries card.
type TimingSummary struct {
	Count int      `json:"count"`
	Text  string   `json:"text"`
	Flags []string `json:"flags"`
}

// timed is one stored timing placed on the ephemeris.
type timed struct {
	row    app.EclipseTiming
	dip    int
	cycle  float64
	oc     float64
	utc    time.Time
	hjd    float64
	second bool
}

// place puts each timed row on e: the dip whose phase is nearest, the
// cycle and the observed minus calculated time, in days.
func place(rows []app.EclipseTiming, e *period.Ephemeris, g *GaiaFacts) []timed {
	var out []timed
	for _, r := range rows {
		if r.State != app.TimingGood && r.State != app.TimingDoubtful {
			continue
		}
		x := (r.BJD - e.T0) / e.Period
		best, bestD := 0, math.Inf(1)
		for k, d := range e.Eclipses {
			if dd := math.Abs(period.WrapPhase(x - d.Phase)); dd < bestD {
				best, bestD = k, dd
			}
		}
		ph := e.Eclipses[best].Phase
		n := math.Round(x - ph)
		t := timed{row: r, dip: best, cycle: n + ph, oc: r.BJD - (e.T0 + (n+ph)*e.Period), second: best != 0}
		ra, dec := g.Now(r.Predicted)
		t.utc = lc.UTCOfBJD(r.BJD, ra, dec)
		t.hjd = lc.HJD(t.utc, ra, dec)
		out = append(out, t)
	}
	slices.SortFunc(out, func(a, b timed) int { return a.utc.Compare(b.utc) })
	return out
}

// analyse reads the trends in the good timings.
func analyse(ts []timed) (minima.Analysis, []int) {
	var in []minima.Timing
	var idx []int
	for i, t := range ts {
		if t.row.State != app.TimingGood || !(t.row.Err > 0) {
			continue
		}
		in = append(in, minima.Timing{Cycle: t.cycle, Second: t.second, OC: t.oc, Err: t.row.Err, Day: t.row.BJD})
		idx = append(idx, i)
	}
	return minima.Analyze(in), idx
}

// Record is s's timing record.
func (s Star) Record(loc *time.Location) TimingRecord {
	name, _ := s.Names()
	out := TimingRecord{SourceID: s.Source.ID, Name: name, Timings: []TimingView{}, Untimed: []TimingView{}, Points: []LatePoint{}, Lines: []LateLine{}, Trends: []TrendView{}}
	e, _ := s.ephemeris()
	if e == nil || s.Facts == nil || s.Facts.Gaia == nil {
		out.Summary = "Not ready yet: the star's survey facts are not worked out."
		return out
	}
	out.PeriodText = periodText(e.Period, e.PeriodErr)
	out.EpochBJD = e.T0
	ra, dec := s.Facts.Gaia.Now(time.Now())
	out.EpochHJD = lc.HJD(lc.UTCOfBJD(e.T0, ra, dec), ra, dec)
	deep := s.deeper()
	ts := place(s.Timings, e, s.Facts.Gaia)
	a, idx := analyse(ts)
	used := map[int]bool{}
	for j, i := range idx {
		used[i] = a.Used[j]
	}
	for i, t := range ts {
		v := timingView(t, deep, s.Facts.Doubled && s.Facts.Alike, loc)
		v.Used = used[i]
		out.Timings = append(out.Timings, v)
		out.Points = append(out.Points, LatePoint{Year: decimalYear(t.utc), Kind: v.Kind, Minutes: v.LateMinutes, ErrMinutes: v.ErrMinutes, Used: v.Used})
	}
	for _, r := range s.Timings {
		if r.State == app.TimingNone {
			out.Untimed = append(out.Untimed, TimingView{ID: r.ID, Kind: r.Kind, KindLabel: kindLabelOf(r.Kind, false), Night: r.Night, Mid: r.Predicted, State: r.State, Note: r.Note, RequestID: r.RequestID})
		}
	}
	out.Lines = lines(ts, a, e, deep)
	out.Trends = trends(a, e, deep)
	out.Summary = summary(a, len(ts))
	if a.Ephemeris.State == minima.StateFlag {
		out.Updated = updated(a, e)
	}
	return out
}

// Summary is the timing line of s's card.
func (s Star) Summary() TimingSummary {
	e, _ := s.ephemeris()
	if e == nil || s.Facts == nil || s.Facts.Gaia == nil {
		return TimingSummary{Text: "No timings yet.", Flags: []string{}}
	}
	ts := place(s.Timings, e, s.Facts.Gaia)
	a, _ := analyse(ts)
	out := TimingSummary{Count: a.N, Text: summary(a, len(ts)), Flags: []string{}}
	for _, t := range trends(a, e, s.deeper()) {
		if t.State == minima.StateFlag {
			out.Flags = append(out.Flags, t.Label)
		}
	}
	return out
}

func timingView(t timed, deep int, alike bool, loc *time.Location) TimingView {
	kind := kindOf(t.dip, deep)
	v := TimingView{ID: t.row.ID, Kind: kind, KindLabel: kindLabelOf(kind, alike), Night: t.row.Night, Mid: t.utc, Filter: t.row.Filter,
		BJD: t.row.BJD, HJD: t.hjd, ErrMinutes: round1(t.row.Err * 1440), LateMinutes: round1(t.oc * 1440), Cycle: int(math.Floor(t.cycle)),
		Points: t.row.Points, Method: methodWords(t.row.Method), State: t.row.State, Note: t.row.Note, RequestID: t.row.RequestID}
	v.LateText = earlyLate(v.LateMinutes)
	if t.row.CheckErr > 0 {
		c := round1((t.row.CheckBJD - t.row.BJD) * 1440)
		v.CheckMinutes = &c
	}
	if v.Night == "" {
		v.Night = nightOf(t.utc, loc)
	}
	return v
}

func kindLabelOf(kind string, alike bool) string {
	switch {
	case alike && kind == KindPrimary:
		return "Eclipse A"
	case alike:
		return "Eclipse B"
	case kind == KindPrimary:
		return "Deeper eclipse"
	}
	return "Second eclipse"
}

func methodWords(m string) string {
	switch m {
	case minima.MethodModel:
		return "Fit of the eclipse's own shape"
	case minima.MethodKvW:
		return "Mirror method (Kwee and van Woerden)"
	}
	return m
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

func decimalYear(t time.Time) float64 {
	y := t.UTC().Year()
	start := time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(y+1, 1, 1, 0, 0, 0, 0, time.UTC)
	return float64(y) + t.Sub(start).Seconds()/end.Sub(start).Seconds()
}

// lines samples the adopted fit across the timings' years, per kind.
func lines(ts []timed, a minima.Analysis, e *period.Ephemeris, deep int) []LateLine {
	out := []LateLine{}
	if a.Fit.Model == minima.ModelNone || len(ts) == 0 {
		return out
	}
	first, last := ts[0].row.BJD, ts[len(ts)-1].row.BJD
	if last-first < 1 {
		first, last = first-15, last+15
	}
	for k := range e.Eclipses {
		has := false
		for _, t := range ts {
			has = has || t.dip == k
		}
		if !has {
			continue
		}
		l := LateLine{Kind: kindOf(k, deep)}
		for i := range 41 {
			b := first + (last-first)*float64(i)/40
			cyc := math.Round((b-e.T0)/e.Period-e.Eclipses[k].Phase) + e.Eclipses[k].Phase
			utc := julianTime(b)
			l.Points = append(l.Points, [2]float64{decimalYear(utc), round1(a.Fit.At(cyc, k != 0) * 1440)})
		}
		out = append(out, l)
	}
	return out
}

func julianTime(jd float64) time.Time {
	return time.Unix(0, int64((jd-2440587.5)*86400e9)).UTC()
}

func summary(a minima.Analysis, n int) string {
	good := a.Primary + a.Secondary
	switch {
	case n == 0:
		return "No timings yet. The app asks for eclipses each season and times them from your frames."
	case a.Ephemeris.State == minima.StateNeed:
		return fmt.Sprintf("Not enough timings yet: %s so far.", pluralWords(good, "good timing"))
	}
	return fmt.Sprintf("%s over %s.", pluralWords(good, "good timing"), spanWords(a.SpanDays))
}

func pluralWords(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func spanWords(d float64) string {
	switch {
	case d < 1:
		return "one night"
	case d < 2*365.25:
		return fmt.Sprintf("%.0f days", d)
	}
	return fmt.Sprintf("%.1f years", d/365.25)
}

// Trend labels.
const (
	labelEphemeris = "Predicted times need updating"
	labelPeriod    = "Period changing"
	labelOrbit     = "Orbit turning"
	labelThird     = "Possible third star"
	notEnough      = "Not enough timings yet. "
)

func trends(a minima.Analysis, e *period.Ephemeris, deep int) []TrendView {
	n := a.Primary + a.Secondary
	basis := fmt.Sprintf("From %s over %s.", pluralWords(n, "good timing"), spanWords(a.SpanDays))
	chance := func(p float64) string { return " Chance of this from noise alone: " + minima.OneIn(p) + "." }
	yearCycles := 365.25 / e.Period
	out := make([]TrendView, 0, 4)

	t := TrendView{ID: minima.TrendEphemeris, Label: labelEphemeris, State: a.Ephemeris.State}
	switch a.Ephemeris.State {
	case minima.StateNeed:
		t.Why = notEnough + a.Ephemeris.Need
	case minima.StateFlag:
		t.Why = fmt.Sprintf("Your eclipses now come %s, give or take %.1f minutes, against the predicted times.", earlyLate(a.Now*1440), a.NowErr*1440)
		if a.Fit.Model == minima.ModelLine || a.Fit.Model == minima.ModelCurve {
			t.Why += fmt.Sprintf(" The timings fit a period of %s days.", periodText(e.Period+a.Rate, a.RateErr))
		}
		t.Basis = basis + chance(a.Ephemeris.Prob)
	default:
		t.Why = fmt.Sprintf("Your timings agree with the predicted times: now %s, give or take %.1f minutes.", earlyLate(a.Now*1440), a.NowErr*1440)
		t.Basis = basis + chance(a.Ephemeris.Prob)
	}
	out = append(out, t)

	t = TrendView{ID: minima.TrendPeriod, Label: labelPeriod, State: a.Period.State}
	secYear := func(v float64) float64 { return 2 * v / e.Period * 365.25 * 86400 }
	switch a.Period.State {
	case minima.StateNeed:
		t.Why = notEnough + a.Period.Need
	case minima.StateFlag:
		way := "longer"
		if a.Period.Value < 0 {
			way = "shorter"
		}
		t.Why = fmt.Sprintf("The period is getting %s by %.2f seconds a year, give or take %.2f.", way, math.Abs(secYear(a.Period.Value)), secYear(a.Period.Err))
		t.Basis = basis + " Measured against the survey period as well." + chance(a.Period.Prob)
	default:
		t.Why = fmt.Sprintf("No change in the period beyond %.2f seconds a year.", 3*secYear(a.Period.Err))
		t.Basis = basis + chance(a.Period.Prob)
	}
	out = append(out, t)

	t = TrendView{ID: minima.TrendOrbit, Label: labelOrbit, State: a.Orbit.State}
	minYear := func(v float64) float64 { return v * yearCycles * 1440 }
	switch a.Orbit.State {
	case minima.StateNeed:
		t.Why = notEnough + a.Orbit.Need
		if len(e.Eclipses) < 2 {
			t.Why = "Not tested: only one eclipse per cycle is seen."
		}
	case minima.StateFlag:
		v := a.Orbit.Value
		if deep != 0 {
			v = -v
		}
		way := "later"
		if v < 0 {
			way = "earlier"
		}
		t.Why = fmt.Sprintf("The deeper eclipse comes %.1f minutes a year %s against the second one, give or take %.1f, so the oval orbit is turning.",
			math.Abs(minYear(v)), way, minYear(a.Orbit.Err))
		t.Basis = basis + chance(a.Orbit.Prob)
	default:
		t.Why = fmt.Sprintf("The two eclipses keep their spacing to within %.1f minutes a year.", 3*minYear(a.Orbit.Err))
		t.Basis = basis + chance(a.Orbit.Prob)
	}
	out = append(out, t)

	t = TrendView{ID: minima.TrendThird, Label: labelThird, State: a.Third.State}
	switch a.Third.State {
	case minima.StateNeed:
		t.Why = notEnough + a.Third.Need
	case minima.StateFlag:
		t.Why = fmt.Sprintf("The timings swing %.1f minutes early and late every %.0f days, as the pull of a third star would make them.",
			a.Third.Value*1440, a.Third.Period)
		t.Basis = basis + chance(a.Third.Prob)
	default:
		t.Why = "No regular swing in the timings."
		t.Basis = basis + chance(a.Third.Prob)
	}
	return append(out, t)
}

func updated(a minima.Analysis, e *period.Ephemeris) *Updated {
	p, pe := e.Period, e.PeriodErr
	if a.Fit.Model == minima.ModelLine || a.Fit.Model == minima.ModelCurve {
		p, pe = e.Period+a.Rate, a.RateErr
	}
	return &Updated{PeriodText: periodText(p, pe), Period: p, PeriodErr: pe, EpochBJD: e.T0 + a.RefCycle*e.Period + a.Now}
}
