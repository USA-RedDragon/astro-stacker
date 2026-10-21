package binaries

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveryfollowup"
	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"github.com/USA-RedDragon/astro-stacker/internal/schedsettle"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/ownimage"
	"gorm.io/gorm/clause"
)

// KindDip is the kind of an imaging request for one dip window of a
// dipper.
const KindDip = "dip"

const (
	// dipMinDark is the least usable dark a window needs to be asked
	// for.
	dipMinDark = time.Hour
	// dipAhead is how many windows are planned and asked for ahead.
	dipAhead = 5
	// dipPast is how far back the dipper's card lists past windows and
	// their results are worked out.
	dipPast = 30 * 24 * time.Hour
	// dipStep is the step the dark part of a window is found at.
	dipStep = time.Minute
	// dipLead is the least time from now a request's dark part starts.
	dipLead = 15 * time.Minute
	// dipJudgeAfter is how long after a window its subs are judged.
	dipJudgeAfter = time.Hour
	mjdUnix       = 40587.0
	// minDipPeriod is the shortest dip cycle taken, in days.
	minDipPeriod = 0.1
)

// Dip window errors the caller made, as opposed to the server's.
var (
	ErrNotDipper    = errors.New("no dip cycle is stored for that star")
	ErrNoDipWindow  = errors.New("no dip window of that star near that time")
	ErrNoDark       = errors.New("that window has less than 1 hour of usable dark")
	ErrBadDipCycle  = errors.New("bad dip cycle")
	ErrSiteNotKnown = errors.New("the site location is not known yet")
)

// DipCycle is a dipper's dip cycle and past record as the pages and the
// API show and take it.
type DipCycle struct {
	Period       float64 `json:"period"`
	EpochMJD     float64 `json:"epoch_mjd"`
	WindowHours  float64 `json:"window_hours"`
	DepthLow     float64 `json:"depth_low"`
	DepthHigh    float64 `json:"depth_high"`
	DurLowHours  float64 `json:"dur_low_hours"`
	DurHighHours float64 `json:"dur_high_hours"`
	ActiveShare  float64 `json:"active_share"`
	PastSurveys  string  `json:"past_surveys"`
	PastDips     int     `json:"past_dips"`
	PastYears    int     `json:"past_years"`
	PastActive   int     `json:"past_active"`
	PastCovered  int     `json:"past_covered"`
}

func cycleOf(e app.DipperEphemeris) DipCycle {
	return DipCycle{Period: e.Period, EpochMJD: e.EpochMJD, WindowHours: e.WindowHours, DepthLow: e.DepthLow, DepthHigh: e.DepthHigh,
		DurLowHours: e.DurLowHours, DurHighHours: e.DurHighHours, ActiveShare: e.ActiveShare, PastSurveys: e.PastSurveys,
		PastDips: e.PastDips, PastYears: e.PastYears, PastActive: e.PastActive, PastCovered: e.PastCovered}
}

func (c DipCycle) check() error {
	bad := func(why string) error { return fmt.Errorf("%w: %s", ErrBadDipCycle, why) }
	switch {
	case !finiteAll(c.Period, c.EpochMJD, c.WindowHours, c.DepthLow, c.DepthHigh, c.DurLowHours, c.DurHighHours, c.ActiveShare):
		return bad("every number must be finite")
	case c.Period < minDipPeriod:
		return bad("the period must be at least 0.1 days")
	case c.EpochMJD <= 0:
		return bad("the epoch must be a modified Julian date")
	case c.WindowHours <= 0 || c.WindowHours >= c.Period*24:
		return bad("the window must be above 0 hours and shorter than the period")
	case c.DepthLow < 0 || c.DepthHigh < c.DepthLow:
		return bad("the depths must run from a low to a high of 0 or more")
	case c.DurLowHours < 0 || c.DurHighHours < c.DurLowHours:
		return bad("the dip lengths must run from a low to a high of 0 or more")
	case c.ActiveShare < 0 || c.ActiveShare > 1:
		return bad("the share of windows with a dip must be from 0 to 1")
	case c.PastDips < 0 || c.PastYears < 0 || c.PastActive < 0 || c.PastCovered < 0:
		return bad("the past counts must be 0 or more")
	case c.PastCovered > 0 && c.PastActive > c.PastCovered:
		return bad("more windows with a dip than covered windows")
	}
	return nil
}

// dipSpan is one window of a dip cycle: its cycle number, middle and
// ends.
type dipSpan struct {
	n               int
	mid, start, end time.Time
}

func mjdTime(mjd float64) time.Time {
	return time.Unix(0, 0).UTC().Add(time.Duration(math.Round((mjd - mjdUnix) * 86400e9)))
}

func timeMJD(t time.Time) float64 {
	return mjdUnix + float64(t.UTC().UnixNano())/86400e9
}

func windowOf(e app.DipperEphemeris, n int) dipSpan {
	mid := mjdTime(e.EpochMJD + float64(n)*e.Period)
	half := time.Duration(e.WindowHours / 2 * float64(time.Hour))
	return dipSpan{n: n, mid: mid, start: mid.Add(-half), end: mid.Add(half)}
}

// cycleNear is the cycle whose middle is nearest t.
func cycleNear(e app.DipperEphemeris, t time.Time) int {
	return int(math.Round((timeMJD(t) - e.EpochMJD) / e.Period))
}

// cycleFrom is the first cycle whose window ends after t.
func cycleFrom(e app.DipperEphemeris, t time.Time) int {
	n := int(math.Ceil((timeMJD(t) - e.EpochMJD - e.WindowHours/48) / e.Period))
	for !windowOf(e, n).end.After(t) {
		n++
	}
	for n > math.MinInt32 && windowOf(e, n-1).end.After(t) {
		n--
	}
	return n
}

func (w dipSpan) holds(t time.Time) bool { return !t.Before(w.start) && !t.After(w.end) }

// covers says r is the request of window w: its middle is inside the
// window, or it is open and its imaging overlaps the window, as when a new
// cycle moved the window of a request under way.
func (w dipSpan) covers(r app.EclipseRequest) bool {
	return w.holds(r.Mid) || open(r) && r.Start.Before(w.end) && w.start.Before(r.End)
}

// resultIn is the stored result of window w, when it was judged for the
// same window of the current cycle.
func resultIn(rows map[int]app.DipResult, w dipSpan) app.DipResult {
	if r, ok := rows[w.n]; ok && w.holds(r.Mid) {
		return r
	}
	return app.DipResult{}
}

// DarkSpans are the parts of the span between from and to when the site
// sees the star dark and high, by the eclipse planner's rule: the Sun below DarkSun and the
// star above MinAltitude.
func DarkSpans(site sky.Site, ra, dec float64, from, to time.Time) []TimeSpan {
	var out []TimeSpan
	var cur *TimeSpan
	for t := from.Truncate(dipStep); !t.After(to); t = t.Add(dipStep) {
		if t.Before(from) {
			continue
		}
		ok, _ := Seen(site, ra, dec, t)
		switch {
		case ok && cur == nil:
			cur = &TimeSpan{Start: t, End: t}
		case ok:
			cur.End = t
		case cur != nil:
			cur.End = t
			out = append(out, *cur)
			cur = nil
		}
	}
	if cur != nil {
		out = append(out, *cur)
	}
	return out
}

func spanTotal(spans []TimeSpan) time.Duration {
	var d time.Duration
	for _, s := range spans {
		d += s.End.Sub(s.Start)
	}
	return d
}

// DipImaging is how much of a window the user's own frames cover.
type DipImaging struct {
	Subs    int     `json:"subs"`
	Seconds float64 `json:"seconds"`
	Text    string  `json:"text"`
}

// Dip window result states the pages show beyond the stored ones.
const (
	DipNotImaged = "not-imaged"
	DipWaiting   = "waiting"
	DipMeasuring = "measuring"
)

// DipResultView is what a window showed, or why nothing is known yet.
type DipResultView struct {
	State string `json:"state"`
	Label string `json:"label"`
	Note  string `json:"note"`
}

// DipWindow is one dip window as the planner and the dipper's card show
// it.
type DipWindow struct {
	SourceID       int           `json:"source_id"`
	Name           string        `json:"name"`
	Field          string        `json:"field"`
	Magnitude      string        `json:"magnitude"`
	PeriodText     string        `json:"period_text"`
	N              int           `json:"n"`
	Mid            time.Time     `json:"mid"`
	Start          time.Time     `json:"start"`
	End            time.Time     `json:"end"`
	GroupAt        time.Time     `json:"group_at"`
	DayLabel       string        `json:"day_label"`
	WindowText     string        `json:"window_text"`
	Zone           string        `json:"zone"`
	MidText        string        `json:"mid_text"`
	MidLabel       string        `json:"mid_label"`
	Dark           []TimeSpan    `json:"dark"`
	DarkMinutes    float64       `json:"dark_minutes"`
	DarkText       string        `json:"dark_text"`
	DarkNote       string        `json:"dark_note"`
	Observable     bool          `json:"observable"`
	ObservableNote string        `json:"observable_note"`
	Why            string        `json:"why"`
	EveryWindow    bool          `json:"every_window"`
	Over           bool          `json:"over"`
	Request        *RequestView  `json:"request"`
	Imaging        *DipImaging   `json:"imaging"`
	Result         DipResultView `json:"result"`
}

// DipperSummary is a dipper's cycle in words, for its card.
type DipperSummary struct {
	WindowsText string `json:"windows_text"`
	DipsText    string `json:"dips_text"`
	EveryWindow bool   `json:"every_window"`
}

// DipperView is a dipper's dip windows box.
type DipperView struct {
	SourceID    int         `json:"source_id"`
	Ephemeris   *DipCycle   `json:"ephemeris"`
	EveryWindow bool        `json:"every_window"`
	WindowsText string      `json:"windows_text"`
	DipsText    string      `json:"dips_text"`
	PastText    string      `json:"past_text"`
	ZoneNote    string      `json:"zone_note"`
	Windows     []DipWindow `json:"windows"`
}

func dipNum(v float64) string {
	return strconv.FormatFloat(math.Round(v*1000)/1000, 'f', -1, 64)
}

func rangeOf(lo, hi float64, unit string) string {
	if lo == hi || lo <= 0 {
		return dipNum(hi) + " " + unit
	}
	return dipNum(lo) + " to " + dipNum(hi) + " " + unit
}

func shareText(share float64) string {
	if share <= 0 {
		return ""
	}
	k := math.Round(1 / share)
	if k <= 1 {
		return "in nearly every window"
	}
	return fmt.Sprintf("in about 1 window in %.0f", k)
}

func cyclePeriodText(e app.DipperEphemeris) string { return strconv.FormatFloat(e.Period, 'f', 3, 64) }

// summaryOf is the cycle in words: "every 9.356 days, 20 hours wide" and
// "0.15 to 0.6 mag, 2 to 18 hours, in about 1 window in 4".
func summaryOf(e app.DipperEphemeris) DipperSummary {
	out := DipperSummary{WindowsText: fmt.Sprintf("every %s days, %s hours wide", cyclePeriodText(e), dipNum(e.WindowHours)), EveryWindow: e.EveryWindow}
	var parts []string
	if e.DepthHigh > 0 {
		parts = append(parts, rangeOf(e.DepthLow, e.DepthHigh, "mag"))
	}
	if e.DurHighHours > 0 {
		parts = append(parts, rangeOf(e.DurLowHours, e.DurHighHours, "hours"))
	}
	if s := shareText(e.ActiveShare); s != "" {
		parts = append(parts, s)
	}
	out.DipsText = strings.Join(parts, ", ")
	return out
}

// whyOf says what a window holds and what a dip in it would show.
func whyOf(e app.DipperEphemeris) string {
	var b strings.Builder
	b.WriteString("Dims")
	if s := shareText(e.ActiveShare); s != "" {
		b.WriteString(" " + s)
		if e.DepthHigh > 0 || e.DurHighHours > 0 {
			b.WriteString(",")
		}
	}
	if e.DepthHigh > 0 {
		b.WriteString(" by " + rangeOf(e.DepthLow, e.DepthHigh, "mag"))
	}
	if e.DurHighHours > 0 {
		b.WriteString(" for " + rangeOf(e.DurLowHours, e.DurHighHours, "hours"))
	}
	s := b.String() + "."
	if s == "Dims." {
		s = ""
	} else {
		s += " "
	}
	return s + "A dip here would back the " + cyclePeriodText(e) + "-day cycle."
}

// pastText is the past record in words.
func pastText(e app.DipperEphemeris) string {
	if e.PastDips == 0 && e.PastCovered == 0 {
		return "Past windows: not measured yet."
	}
	head := "Past windows"
	if e.PastSurveys != "" {
		head += " from " + e.PastSurveys
	}
	var b strings.Builder
	b.WriteString(head + ": ")
	b.WriteString(plural(e.PastDips, "dip"))
	if e.PastYears > 0 {
		b.WriteString(" over " + plural(e.PastYears, "year"))
	}
	if e.PastCovered > 0 {
		fmt.Fprintf(&b, ", in %d of about %d covered windows", e.PastActive, e.PastCovered)
	}
	return b.String() + "."
}

// durText is a length in hours and minutes: "6 h 30 min".
func durText(d time.Duration) string {
	m := int(math.Round(d.Minutes()))
	switch {
	case m < 60:
		return fmt.Sprintf("%d min", m)
	case m%60 == 0:
		return fmt.Sprintf("%d h", m/60)
	}
	return fmt.Sprintf("%d h %d min", m/60, m%60)
}

func clock(t time.Time, loc *time.Location) string {
	return t.Round(time.Minute).In(loc).Format("3:04 PM")
}

func spansText(spans []TimeSpan, loc *time.Location) string {
	parts := make([]string, len(spans))
	for i, s := range spans {
		parts[i] = clock(s.Start, loc) + " to " + clock(s.End, loc)
	}
	return strings.Join(parts, " and ")
}

// dipper is a confirmed dipper with its stored dip cycle.
type dipper struct {
	st Star
	e  app.DipperEphemeris
}

func (d dipper) position(t time.Time) (float64, float64) {
	if d.st.Facts != nil && d.st.Facts.Gaia != nil {
		return d.st.Facts.Gaia.Now(t)
	}
	return d.st.Source.RA, d.st.Source.Dec
}

func (d dipper) g() float64 {
	switch {
	case d.st.Facts != nil && d.st.Facts.Gaia != nil:
		return d.st.Facts.Gaia.G
	case d.st.Source.GaiaG != nil:
		return *d.st.Source.GaiaG
	}
	return 0
}

func isOther(st Star) bool {
	return st.Source.Vetting == app.VetReal && st.Source.Kind != app.DiscoveryKindMover && !EclipsingBinary(st.Report)
}

// dippers are the confirmed stars that are not eclipsing binaries and
// have a stored dip cycle, all of them when ids is nil.
func (s *Service) dippers(ctx context.Context, ids []int) ([]dipper, error) {
	var rows []app.DipperEphemeris
	q := s.DB.WithContext(ctx)
	if ids != nil {
		q = q.Where("source_id IN ?", ids)
	}
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	want := make([]int, len(rows))
	for i, r := range rows {
		want[i] = r.SourceID
	}
	stars, err := s.load(ctx, want)
	if err != nil {
		return nil, err
	}
	var out []dipper
	for _, st := range stars {
		if !isOther(st) || st.Dipper == nil {
			continue
		}
		out = append(out, dipper{st: st, e: *st.Dipper})
	}
	return out, nil
}

// dipInputs is what the window rows are worked out from.
type dipInputs struct {
	site    *sky.Site
	loc     *time.Location
	now     time.Time
	reqs    []app.EclipseRequest
	results map[int]app.DipResult
	// subs are the field's registered subs, nil when they could not be
	// read.
	subs []ownimage.Sub
}

func subMid(sub ownimage.Sub) time.Time {
	return sub.Start.Add(time.Duration(sub.Exposure / 2 * float64(time.Second)))
}

func subsIn(subs []ownimage.Sub, w dipSpan) []ownimage.Sub {
	var out []ownimage.Sub
	for _, sub := range subs {
		if w.holds(subMid(sub)) {
			out = append(out, sub)
		}
	}
	return out
}

// dipRequest is the request of the window, the newest not moved; nil
// when there is none or it was cancelled.
func dipRequest(reqs []app.EclipseRequest, id int, w dipSpan) *RequestView {
	var best *app.EclipseRequest
	for i, r := range reqs {
		if r.SourceID != id || r.Kind != KindDip || r.State == app.EclipseMoved || !w.holds(r.Mid) {
			continue
		}
		if best == nil || r.ID > best.ID {
			best = &reqs[i]
		}
	}
	if best == nil || best.State == app.EclipseCancel {
		return nil
	}
	return &RequestView{ID: best.ID, State: best.State, Note: best.Note, Auto: best.Auto}
}

// row is window w of d as the pages show it.
func (d dipper) row(w dipSpan, in dipInputs) DipWindow {
	loc := in.loc
	name, _ := d.st.Names()
	r := DipWindow{SourceID: d.st.Source.ID, Name: name, Field: d.st.Source.Object, Magnitude: d.st.Magnitude(), PeriodText: cyclePeriodText(d.e),
		N: w.n, Mid: w.mid, Start: w.start, End: w.end, GroupAt: w.mid, Why: whyOf(d.e), EveryWindow: d.e.EveryWindow,
		Over: !w.end.After(in.now), Dark: []TimeSpan{}}
	r.WindowText = clock(w.start, loc) + " to " + clock(w.end, loc)
	_, _, r.Zone = Labels(w.mid, loc)
	r.MidText = w.mid.Round(time.Minute).In(loc).Format("3:04 PM Jan 2")
	r.MidLabel = w.mid.Round(time.Minute).In(loc).Format("Jan 2, 3:04 PM")
	if in.site != nil {
		ra, dec := d.position(w.mid)
		r.Dark = DarkSpans(*in.site, ra, dec, w.start, w.end)
		total := spanTotal(r.Dark)
		r.DarkMinutes = total.Minutes()
		r.Observable = total >= dipMinDark
		if len(r.Dark) > 0 {
			r.GroupAt = r.Dark[0].Start
			r.DarkText = spansText(r.Dark, loc)
			r.DarkNote = "dark from " + r.DarkText + ", " + durText(total)
		} else {
			r.DarkNote = "no dark time inside it"
		}
		if !r.Observable {
			r.ObservableNote = "Not requested, under the 1 hour of usable dark a request needs"
		}
	} else {
		r.DarkNote, r.ObservableNote = "Site location not known yet", "Not requested, the site location is not known yet"
	}
	r.DayLabel, _, _ = Labels(r.GroupAt, loc)
	r.Request = dipRequest(in.reqs, d.st.Source.ID, w)
	var got []ownimage.Sub
	if in.subs != nil {
		got = subsIn(in.subs, w)
		if len(got) > 0 {
			var secs float64
			for _, sub := range got {
				secs += sub.Exposure
			}
			r.Imaging = &DipImaging{Subs: len(got), Seconds: secs, Text: fmt.Sprintf("%s, %s", plural(len(got), "sub"), durText(time.Duration(secs*float64(time.Second))))}
		}
	}
	r.Result = resultOf(resultIn(in.results, w), r.Over, in.subs != nil, len(got))
	return r
}

func resultOf(res app.DipResult, over, subsKnown bool, subs int) DipResultView {
	switch {
	case res.State == app.DipSeen:
		return DipResultView{State: app.DipSeen, Label: "Dip seen", Note: res.Note}
	case res.State == app.DipNone:
		return DipResultView{State: app.DipNone, Label: "No dip", Note: res.Note}
	case res.State == app.DipUnsure:
		return DipResultView{State: app.DipUnsure, Label: "Could not judge", Note: res.Note}
	case !over && subs > 0:
		return DipResultView{State: DipWaiting, Label: "Being imaged, follow it on the Now page"}
	case !over:
		return DipResultView{State: DipWaiting, Label: "Not imaged yet"}
	case !subsKnown:
		return DipResultView{State: DipMeasuring, Label: "Not measured yet"}
	case subs > 0:
		return DipResultView{State: DipMeasuring, Label: "Being measured"}
	}
	return DipResultView{State: DipNotImaged, Label: "Not imaged"}
}

func (s *Service) site(ctx context.Context) *sky.Site {
	if s.Site == nil {
		return nil
	}
	v, err := s.Site(ctx)
	if err != nil {
		return nil
	}
	return &v
}

func (s *Service) dipInputs(ctx context.Context, ds []dipper) (dipInputs, error) {
	in := dipInputs{site: s.site(ctx), loc: s.loc(), now: s.now()}
	if len(ds) == 0 {
		return in, nil
	}
	ids := make([]int, len(ds))
	for i, d := range ds {
		ids[i] = d.st.Source.ID
	}
	if err := s.DB.WithContext(ctx).Where("kind = ? AND source_id IN ?", KindDip, ids).Find(&in.reqs).Error; err != nil {
		return in, err
	}
	return in, nil
}

func (s *Service) withResults(ctx context.Context, in dipInputs, d dipper) (dipInputs, error) {
	var rows []app.DipResult
	if err := s.DB.WithContext(ctx).Where("source_id = ?", d.st.Source.ID).Find(&rows).Error; err != nil {
		return in, err
	}
	in.results = map[int]app.DipResult{}
	for _, r := range rows {
		in.results[r.Cycle] = r
	}
	in.subs = nil
	if s.LiveData != nil {
		subs, err := s.LiveData.Subs(ctx, d.st.Source.Object)
		if err != nil {
			slog.Warn("Could not list the subs of a dipper's field", "source", d.st.Source.ID, "error", err)
		} else {
			in.subs = subs
			if in.subs == nil {
				in.subs = []ownimage.Sub{}
			}
		}
	}
	return in, nil
}

// dipPlan is every dipper's windows between from and to, the ones with an
// hour of usable dark unless all, and how many were left out.
func (s *Service) dipPlan(ctx context.Context, from, to time.Time, all bool) ([]DipWindow, int, error) {
	ds, err := s.dippers(ctx, nil)
	if err != nil {
		return nil, 0, err
	}
	in, err := s.dipInputs(ctx, ds)
	if err != nil {
		return nil, 0, err
	}
	out := []DipWindow{}
	hidden := 0
	for _, d := range ds {
		if in, err = s.withResults(ctx, in, d); err != nil {
			return nil, 0, err
		}
		for n := cycleFrom(d.e, from); ; n++ {
			w := windowOf(d.e, n)
			if !w.start.Before(to) {
				break
			}
			r := d.row(w, in)
			if !r.Observable && !all {
				hidden++
				continue
			}
			out = append(out, r)
		}
	}
	slices.SortStableFunc(out, func(a, b DipWindow) int { return a.GroupAt.Compare(b.GroupAt) })
	return out, hidden, nil
}

// Dipper is one dipper's dip windows box: its windows of the last
// dipPast and the next dipAhead.
func (s *Service) Dipper(ctx context.Context, id int) (DipperView, error) {
	st, err := s.Other(ctx, id)
	if err != nil {
		return DipperView{}, err
	}
	v := DipperView{SourceID: id, Windows: []DipWindow{}, ZoneNote: ZoneNote(s.loc(), s.now(), s.now().Add(dipPast))}
	if st.Dipper == nil {
		return v, nil
	}
	d := dipper{st: st, e: *st.Dipper}
	c := cycleOf(d.e)
	sum := summaryOf(d.e)
	v.Ephemeris, v.EveryWindow, v.WindowsText, v.DipsText, v.PastText = &c, d.e.EveryWindow, sum.WindowsText, sum.DipsText, pastText(d.e)
	in, err := s.dipInputs(ctx, []dipper{d})
	if err != nil {
		return v, err
	}
	if in, err = s.withResults(ctx, in, d); err != nil {
		return v, err
	}
	first, next := cycleFrom(d.e, in.now.Add(-dipPast)), cycleFrom(d.e, in.now)
	for n := first; n < next+dipAhead; n++ {
		v.Windows = append(v.Windows, d.row(windowOf(d.e, n), in))
	}
	return v, nil
}

// SetDipCycle stores a dipper's dip cycle and past record, keeping its
// every window setting; open requests the new cycle moves are drafted
// again on the next look.
func (s *Service) SetDipCycle(ctx context.Context, id int, c DipCycle) (DipperView, error) {
	if err := c.check(); err != nil {
		return DipperView{}, err
	}
	st, err := s.Other(ctx, id)
	if err != nil {
		return DipperView{}, err
	}
	row := app.DipperEphemeris{SourceID: id, Period: c.Period, EpochMJD: c.EpochMJD, WindowHours: c.WindowHours, DepthLow: c.DepthLow,
		DepthHigh: c.DepthHigh, DurLowHours: c.DurLowHours, DurHighHours: c.DurHighHours, ActiveShare: c.ActiveShare,
		PastSurveys: strings.TrimSpace(c.PastSurveys), PastDips: c.PastDips, PastYears: c.PastYears, PastActive: c.PastActive,
		PastCovered: c.PastCovered, UpdatedAt: s.now()}
	if st.Dipper != nil {
		row.EveryWindow = st.Dipper.EveryWindow
	}
	if err := s.DB.WithContext(ctx).Save(&row).Error; err != nil {
		return DipperView{}, err
	}
	s.Kick()
	return s.Dipper(ctx, id)
}

// SetEveryWindow turns on or off the app's asking for imaging on every
// window of a dipper the site can see. Turning it off leaves the requests
// already made.
func (s *Service) SetEveryWindow(ctx context.Context, id int, on bool) (DipperView, error) {
	st, err := s.Other(ctx, id)
	if err != nil {
		return DipperView{}, err
	}
	if st.Dipper == nil {
		return DipperView{}, ErrNotDipper
	}
	if err := s.DB.WithContext(ctx).Model(&app.DipperEphemeris{}).Where("source_id = ?", id).
		Updates(map[string]any{"every_window": on, "updated_at": s.now()}).Error; err != nil {
		return DipperView{}, err
	}
	if on {
		s.Kick()
	}
	return s.Dipper(ctx, id)
}

// RequestDipWindow asks the scheduler to image the dark part of the dip
// window of a dipper whose middle is within requestMatch of mid.
func (s *Service) RequestDipWindow(ctx context.Context, id int, mid time.Time) (app.EclipseRequest, error) {
	ds, err := s.dippers(ctx, []int{id})
	if err != nil {
		return app.EclipseRequest{}, err
	}
	if len(ds) == 0 {
		return app.EclipseRequest{}, ErrNotDipper
	}
	d := ds[0]
	w := windowOf(d.e, cycleNear(d.e, mid))
	if absDur(w.mid.Sub(mid)) > requestMatch {
		return app.EclipseRequest{}, ErrNoDipWindow
	}
	return s.askDip(ctx, d, w, false)
}

// askDip asks for window w of d: the window's request when it has one
// open or done, else a dip watch made by hand for it, else a new project
// over the window's dark part from dipLead on.
func (s *Service) askDip(ctx context.Context, d dipper, w dipSpan, auto bool) (app.EclipseRequest, error) {
	now := s.now()
	id := d.st.Source.ID
	r := app.EclipseRequest{SourceID: id, Kind: KindDip, Mid: w.mid, State: app.EclipseWaiting, Auto: auto, CreatedAt: now, UpdatedAt: now}
	if !w.end.After(now) {
		return r, ErrOver
	}
	var before []app.EclipseRequest
	if err := s.DB.WithContext(ctx).Where("source_id = ? AND kind = ?", id, KindDip).Find(&before).Error; err != nil {
		return r, err
	}
	for _, b := range before {
		if !w.covers(b) {
			continue
		}
		if b.State == app.EclipseWaiting || b.State == app.EclipseSent || b.State == app.EclipseDone {
			return b, nil
		}
		r.Attempt++
	}
	if a, ok, err := s.adoptWatch(ctx, d, w, before); err != nil || ok {
		return a, err
	}
	site := s.site(ctx)
	if site == nil {
		return r, ErrSiteNotKnown
	}
	ra, dec := d.position(w.mid)
	dark := DarkSpans(*site, ra, dec, maxTime(w.start, now.Add(dipLead)), w.end)
	if spanTotal(dark) < dipMinDark {
		return r, ErrNoDark
	}
	r.Start, r.End = dark[0].Start, dark[len(dark)-1].End
	if s.Commands == nil || s.Snapshot == nil {
		return r, errors.New("the scheduler is not set up")
	}
	snap, err := s.Snapshot(ctx)
	if err != nil {
		return r, fmt.Errorf("planning snapshot: %w", err)
	}
	orig, err := discoveryfollowup.OriginalTarget(ctx, s.DB, &app.DiscoveryFollowup{Object: d.st.Source.Object, Night: d.st.Source.Night})
	if err != nil {
		return r, err
	}
	name, _ := d.st.Names()
	payload, why := DraftDipRequest(snap, orig, &r, name, d.st.Source.Object, d.g(), s.loc())
	if payload == nil {
		r.State, r.Note = app.EclipseFailed, why
		return r, s.DB.WithContext(ctx).Create(&r).Error
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return r, err
	}
	rec, err := s.Commands.SubmitOnce(ctx, "dip-request-"+r.ProjectGUID, schedcmd.KindProjectCreate, raw, RequestAuthor)
	if err != nil {
		r.State, r.Note = app.EclipseFailed, "the scheduler did not take it: "+err.Error()
	} else {
		r.CreateCmd, r.State = rec.ID, app.EclipseSent
		r.Note = fmt.Sprintf("Sent to the scheduler: %d × %gs %s from %s to %s.", r.Subs, r.Exposure, r.Filter,
			r.Start.In(s.loc()).Format("Jan 2 3:04 PM"), r.End.In(s.loc()).Format("3:04 PM MST"))
	}
	if err := s.DB.WithContext(ctx).Create(&r).Error; err != nil {
		return r, err
	}
	if r.State == app.EclipseSent {
		return r, s.linkWatch(ctx, d, w, r.ProjectGUID)
	}
	return r, nil
}

// adoptWatch takes a dip watch made by hand for window w, whose project
// no request follows, as the window's request, so the app follows,
// cancels and closes that project instead of making another.
func (s *Service) adoptWatch(ctx context.Context, d dipper, w dipSpan, reqs []app.EclipseRequest) (app.EclipseRequest, bool, error) {
	db := s.DB.WithContext(ctx)
	var watches []app.DipWatch
	if err := db.Where("source_id = ?", d.st.Source.ID).Order("id").Find(&watches).Error; err != nil {
		return app.EclipseRequest{}, false, err
	}
	for _, wt := range watches {
		if !w.holds(wt.Start.Add(wt.End.Sub(wt.Start)/2)) || slices.ContainsFunc(reqs, func(r app.EclipseRequest) bool { return r.ProjectGUID == wt.ProjectGUID }) {
			continue
		}
		var n int64
		if err := db.Model(&app.EclipseRequest{}).Where("project_guid = ?", wt.ProjectGUID).Count(&n).Error; err != nil {
			return app.EclipseRequest{}, false, err
		}
		if n > 0 {
			continue
		}
		now := s.now()
		r := app.EclipseRequest{SourceID: d.st.Source.ID, Kind: KindDip, Mid: w.mid, Start: wt.Start, End: wt.End, ProjectGUID: wt.ProjectGUID,
			State: app.EclipseSent, Note: "Made by hand before the app planned dip windows; the app follows it now.", CreatedAt: now, UpdatedAt: now}
		rec, p, err := s.createOf(ctx, wt.ProjectGUID)
		if err != nil {
			return r, false, err
		}
		if rec != nil {
			r.CreateCmd = rec.ID
			if refused(rec.Status) {
				r.State, r.Note = app.EclipseFailed, "The scheduler did not take the project made by hand: "+cmpOr(rec.Message, string(rec.Status))
			}
		}
		if p != nil && len(p.Targets) > 0 {
			r.TargetGUID = p.Targets[0].GUID
			if len(p.Targets[0].Plans) > 0 {
				pl := p.Targets[0].Plans[0]
				r.Filter, r.Exposure, r.Subs = pl.Filter, pl.Exposure, pl.Desired
			}
		}
		if err := db.Create(&r).Error; err != nil {
			return r, false, err
		}
		slog.Info("Adopted a dip watch made by hand as a dip window request", "source", r.SourceID, "project", r.ProjectGUID, "request", r.ID)
		return r, true, nil
	}
	return app.EclipseRequest{}, false, nil
}

// createOf is the scheduler command that created project guid, with its
// payload; nil when none is stored.
func (s *Service) createOf(ctx context.Context, guid string) (*schedcmd.Record, *schedcmd.ProjectCreatePayload, error) {
	db := s.DB.WithContext(ctx)
	if !db.Migrator().HasTable(&schedcmd.Record{}) {
		return nil, nil, nil
	}
	var recs []schedcmd.Record
	if err := db.Where("kind = ? AND payload LIKE ?", schedcmd.KindProjectCreate, `%"`+guid+`"%`).Order("created_at").Find(&recs).Error; err != nil {
		return nil, nil, err
	}
	for i := range recs {
		var p schedcmd.ProjectCreatePayload
		if json.Unmarshal(recs[i].Payload, &p) == nil && p.Project.GUID == guid {
			return &recs[i], &p, nil
		}
	}
	return nil, nil, nil
}

// linkWatch points the Now page's live card at the project of window w.
func (s *Service) linkWatch(ctx context.Context, d dipper, w dipSpan, guid string) error {
	if guid == "" {
		return nil
	}
	s.liveMu.Lock()
	delete(s.liveMiss, guid)
	s.liveMu.Unlock()
	now := s.now()
	return upsertWatch(s.DB.WithContext(ctx), &app.DipWatch{SourceID: d.st.Source.ID, ProjectGUID: guid, Start: w.start, End: w.end, DepthLow: d.e.DepthLow,
		DepthHigh: d.e.DepthHigh, ActiveShare: d.e.ActiveShare, CreatedAt: now, UpdatedAt: now})
}

// dipWork follows the dippers' windows: it adopts dip watches made by
// hand, cancels the requests of stars that are no longer dippers, drafts
// again the requests a changed cycle moves, asks for every window of the
// dippers set to every window, keeps the live card's links and judges
// the windows that have passed.
func (s *Service) dipWork(ctx context.Context) error {
	ds, err := s.dippers(ctx, nil)
	if err != nil {
		return err
	}
	var errs []error
	if err := s.dropDips(ctx, ds); err != nil {
		errs = append(errs, err)
	}
	for _, d := range ds {
		if err := s.adoptAll(ctx, d); err != nil {
			errs = append(errs, fmt.Errorf("source %d: %w", d.st.Source.ID, err))
		}
	}
	if err := s.redraftDips(ctx, ds); err != nil {
		errs = append(errs, err)
	}
	if err := s.autoDips(ctx, ds); err != nil {
		errs = append(errs, err)
	}
	if err := s.linkAll(ctx, ds); err != nil {
		errs = append(errs, err)
	}
	if err := s.judgeDips(ctx, ds); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// adoptAll adopts every dip watch of d made by hand whose window has no
// request yet.
func (s *Service) adoptAll(ctx context.Context, d dipper) error {
	db := s.DB.WithContext(ctx)
	var watches []app.DipWatch
	if err := db.Where("source_id = ? AND project_guid NOT IN (?)", d.st.Source.ID, db.Model(&app.EclipseRequest{}).Select("project_guid").Where("project_guid IS NOT NULL")).
		Find(&watches).Error; err != nil {
		return err
	}
	if len(watches) == 0 {
		return nil
	}
	var reqs []app.EclipseRequest
	if err := db.Where("source_id = ? AND kind = ?", d.st.Source.ID, KindDip).Find(&reqs).Error; err != nil {
		return err
	}
	for _, wt := range watches {
		mid := wt.Start.Add(wt.End.Sub(wt.Start) / 2)
		w := windowOf(d.e, cycleNear(d.e, mid))
		if !w.holds(mid) || slices.ContainsFunc(reqs, func(r app.EclipseRequest) bool { return r.State != app.EclipseMoved && w.covers(r) }) {
			continue
		}
		r, ok, err := s.adoptWatch(ctx, d, w, reqs)
		if err != nil {
			return err
		}
		if ok {
			reqs = append(reqs, r)
		}
	}
	return nil
}

func (s *Service) openDips(ctx context.Context) ([]app.EclipseRequest, error) {
	var reqs []app.EclipseRequest
	return reqs, s.DB.WithContext(ctx).Where("kind = ? AND state IN ?", KindDip, []string{app.EclipseWaiting, app.EclipseSent}).Find(&reqs).Error
}

// dropDips cancels the open requests of stars that are no longer dippers.
func (s *Service) dropDips(ctx context.Context, ds []dipper) error {
	reqs, err := s.openDips(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, r := range reqs {
		if slices.ContainsFunc(ds, func(d dipper) bool { return d.st.Source.ID == r.SourceID }) {
			continue
		}
		if _, err := s.CancelRequest(ctx, r.ID); err != nil {
			errs = append(errs, fmt.Errorf("request %d: %w", r.ID, err))
			continue
		}
		slog.Info("Cancelled a dip window request of a star that is no longer a dipper", "source", r.SourceID, "request", r.ID)
	}
	return errors.Join(errs...)
}

// redraftDips withdraws each open dip request whose window has not begun
// and whose dark part the current cycle moves out of the requested window
// by more than minMoveSlack, or leaves under dipMinDark, and asks for the
// window again: the user's own requests here, the app's own through
// autoDips.
func (s *Service) redraftDips(ctx context.Context, ds []dipper) error {
	if s.Commands == nil {
		return nil
	}
	site := s.site(ctx)
	if site == nil {
		return nil
	}
	reqs, err := s.openDips(ctx)
	if err != nil {
		return err
	}
	now := s.now()
	var errs []error
	for _, r := range reqs {
		i := slices.IndexFunc(ds, func(d dipper) bool { return d.st.Source.ID == r.SourceID })
		if i < 0 || !r.Start.After(now) {
			continue
		}
		d := ds[i]
		w := windowOf(d.e, cycleNear(d.e, r.Mid))
		ra, dec := d.position(w.mid)
		dark := DarkSpans(*site, ra, dec, maxTime(w.start, now.Add(dipLead)), w.end)
		short := spanTotal(dark) < dipMinDark
		if !short && dark[0].Start.Sub(r.Start) >= -minMoveSlack && r.End.Sub(dark[len(dark)-1].End) >= -minMoveSlack {
			if !r.Mid.Equal(w.mid) {
				if err := s.DB.WithContext(ctx).Model(&r).Update("mid", w.mid).Error; err != nil {
					errs = append(errs, err)
				}
			}
			continue
		}
		r.State, r.UpdatedAt = app.EclipseMoved, now
		if r.CreateCmd != "" {
			r.Settle = schedsettle.Withdraw
		}
		if short {
			r.Note = "The new dip cycle leaves this window under 1 hour of usable dark, so the request was withdrawn."
		} else {
			r.Note = fmt.Sprintf("The new dip cycle moved this window to a middle of %s, so this request was withdrawn and the window asked for again.",
				w.mid.In(s.loc()).Format("Jan 2 3:04 PM MST"))
		}
		if err := s.DB.WithContext(ctx).Save(&r).Error; err != nil {
			errs = append(errs, err)
			continue
		}
		if err := s.settle(ctx, r.ID); err != nil {
			errs = append(errs, err)
		}
		if r.Auto || short {
			continue
		}
		if _, err := s.askDip(ctx, d, w, false); err != nil && !errors.Is(err, ErrOver) && !errors.Is(err, ErrNoDark) {
			errs = append(errs, fmt.Errorf("source %d: %w", r.SourceID, err))
		}
	}
	return errors.Join(errs...)
}

// autoDips asks for each of the next dipAhead windows of the dippers set
// to every window that has dipMinDark of usable dark and was never asked
// for, in any state but moved.
func (s *Service) autoDips(ctx context.Context, ds []dipper) error {
	if s.Commands == nil || s.Snapshot == nil || s.site(ctx) == nil {
		return nil
	}
	now := s.now()
	var errs []error
	for _, d := range ds {
		if !d.e.EveryWindow {
			continue
		}
		var reqs []app.EclipseRequest
		if err := s.DB.WithContext(ctx).Where("source_id = ? AND kind = ?", d.st.Source.ID, KindDip).Find(&reqs).Error; err != nil {
			errs = append(errs, err)
			continue
		}
		first := cycleFrom(d.e, now)
		for n := first; n < first+dipAhead && ctx.Err() == nil; n++ {
			w := windowOf(d.e, n)
			if slices.ContainsFunc(reqs, func(r app.EclipseRequest) bool { return r.State != app.EclipseMoved && w.covers(r) }) {
				continue
			}
			r, err := s.askDip(ctx, d, w, true)
			switch {
			case errors.Is(err, ErrNoDark), errors.Is(err, ErrOver):
			case err != nil:
				errs = append(errs, fmt.Errorf("source %d window %d: %w", d.st.Source.ID, n, err))
			}
			if r.ID != 0 {
				reqs = append(reqs, r)
			}
		}
	}
	return errors.Join(errs...)
}

// linkAll keeps a dip watch for every open dip request, its window from
// the current cycle, for the Now page's live card.
func (s *Service) linkAll(ctx context.Context, ds []dipper) error {
	reqs, err := s.openDips(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, r := range reqs {
		i := slices.IndexFunc(ds, func(d dipper) bool { return d.st.Source.ID == r.SourceID })
		if i < 0 {
			continue
		}
		if err := s.linkWatch(ctx, ds[i], windowOf(ds[i].e, cycleNear(ds[i].e, r.Mid)), r.ProjectGUID); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// judgeDips measures the star in every window of the last dipPast that
// ended dipJudgeAfter ago and holds subs not judged yet, shown in the
// processing indicator.
func (s *Service) judgeDips(ctx context.Context, ds []dipper) error {
	if s.LiveData == nil {
		return nil
	}
	now := s.now()
	type job struct {
		d    dipper
		w    dipSpan
		subs []ownimage.Sub
	}
	var todo []job
	var errs []error
	for _, d := range ds {
		subs, err := s.LiveData.Subs(ctx, d.st.Source.Object)
		if err != nil {
			errs = append(errs, fmt.Errorf("subs of %s: %w", d.st.Source.Object, err))
			continue
		}
		var rows []app.DipResult
		if err := s.DB.WithContext(ctx).Where("source_id = ?", d.st.Source.ID).Find(&rows).Error; err != nil {
			errs = append(errs, err)
			continue
		}
		for n := cycleFrom(d.e, now.Add(-dipPast)); ; n++ {
			w := windowOf(d.e, n)
			if w.end.Add(dipJudgeAfter).After(now) {
				break
			}
			got := subsIn(subs, w)
			if len(got) == 0 || slices.ContainsFunc(rows, func(r app.DipResult) bool { return r.Cycle == n && w.holds(r.Mid) && r.Subs == len(got) }) {
				continue
			}
			todo = append(todo, job{d: d, w: w, subs: got})
		}
	}
	if len(todo) == 0 {
		return errors.Join(errs...)
	}
	s.DipTask.Start("Judging past dip windows", int64(len(todo)), "windows")
	var runErrs []error
	for _, j := range todo {
		if ctx.Err() != nil {
			break
		}
		s.DipTask.SetCurrent(fmt.Sprintf("Window of %s of find #%d", j.w.mid.In(s.loc()).Format("Jan 2"), j.d.st.Source.ID))
		res, err := s.judgeWindow(ctx, j.d, j.w, j.subs)
		if ctx.Err() != nil {
			break
		}
		if err != nil {
			runErrs = append(runErrs, fmt.Errorf("source %d window %d: %w", j.d.st.Source.ID, j.w.n, err))
			res.State, res.Note = app.DipUnsure, "the subs could not be measured: "+err.Error()
		}
		if err := s.DB.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "source_id"}, {Name: "cycle"}},
			UpdateAll: true}).Create(&res).Error; err != nil {
			runErrs = append(runErrs, err)
		}
		s.DipTask.Step(1)
	}
	s.DipTask.Finish(errors.Join(runErrs...))
	return errors.Join(append(errs, runErrs...)...)
}

// judgeWindow measures the star in a window's subs and judges them as
// the live card does.
func (s *Service) judgeWindow(ctx context.Context, d dipper, w dipSpan, subs []ownimage.Sub) (app.DipResult, error) {
	src := d.st.Source
	res := app.DipResult{SourceID: src.ID, Cycle: w.n, Mid: w.mid, Subs: len(subs), MeasuredAt: s.now()}
	for _, sub := range subs {
		res.Seconds += sub.Exposure
	}
	t, err := ownimage.NewLive(ctx, s.LiveData, src.Object, ownimage.Spot{SourceID: src.ID, Object: src.Object, Kind: src.Kind, RA: src.RA, Dec: src.Dec, GaiaG: src.GaiaG})
	if errors.Is(err, ownimage.ErrNoStar) {
		res.State, res.Note = app.DipUnsure, "the field's frame has no catalogued star at this position"
		return res, nil
	}
	if err != nil {
		return res, err
	}
	for _, sub := range subs {
		if err := t.Add(ctx, s.LiveData, sub); err != nil {
			return res, fmt.Errorf("sub %d: %w", sub.FrameID, err)
		}
	}
	raw, _, _ := t.Points()
	pts, _, _ := mainFilter(raw)
	return judgeResult(res, pts, goalOf(d.e.DepthLow, d.e.DurLowHours), s.loc()), nil
}

// judgeResult is the window's result from its measured subs.
func judgeResult(res app.DipResult, pts []LivePoint, goal dipGoal, loc *time.Location) app.DipResult {
	res.Measured = len(pts)
	j := judgeDip(pts, goal)
	res.Smallest = j.smallest
	switch j.state {
	case LiveDip:
		res.State, res.Depth = app.DipSeen, j.depth
		res.Note = fmt.Sprintf("faded %.2f mag from %s", j.depth, clock(j.start, loc))
	case LiveNoDip:
		res.State, res.Note = app.DipNone, fmt.Sprintf("steady to %.2f mag", j.smallest)
	case LiveComps, LiveFaint:
		res.State, res.Note = app.DipUnsure, j.comps.why+", so the subs could not be judged"
	default:
		res.State, res.Note = app.DipUnsure, fmt.Sprintf("%s gave a measurement, %d are needed", plural(len(pts), "sub"), max(liveMinSubs, j.run))
	}
	return res
}

// mainFilter is the measured subs in the filter with the most of them,
// that filter and how many subs were in other filters.
func mainFilter(raw []ownimage.LivePoint) ([]LivePoint, string, int) {
	count := map[string]int{}
	for _, p := range raw {
		count[p.Filter]++
	}
	filter := ""
	for f, n := range count {
		if n > count[filter] || n == count[filter] && f < filter {
			filter = f
		}
	}
	var pts []LivePoint
	other := 0
	for _, p := range raw {
		if p.Filter != filter {
			other++
			continue
		}
		pts = append(pts, LivePoint{FrameID: p.FrameID, Time: p.Time, Filter: p.Filter, Mag: p.Mag, Err: math.Max(p.Err, errFloor), Comp: p.Comp,
			Spread: p.Spread, Agree: p.Agree, Present: p.Present})
	}
	return pts, filter, other
}
