package binaries

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/sky"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

const (
	// autoLead and autoAhead bound how soon and how far ahead an eclipse
	// may be asked for by the app.
	autoLead  = 2 * time.Hour
	autoAhead = 14 * 24 * time.Hour
	// maxOpenAuto is the most requests the app keeps open at once for all
	// stars, one per star at a time.
	maxOpenAuto = 6
	// autoFailStop is how many failed requests stop a star's asking for
	// the season.
	autoFailStop = 2
	// awaitTiming is how long after a window a request still counts while
	// its frames wait to be measured.
	awaitTiming = 3 * 24 * time.Hour
	yearDays    = 365.25
)

// Season is the year of a star's sky around its opposition: from half a
// year before the date the Sun stands opposite it to half a year after,
// so each season's ends fall when the star is behind the Sun.
type Season struct {
	Start      time.Time `json:"start"`
	End        time.Time `json:"end"`
	Opposition time.Time `json:"opposition"`
}

// SeasonOf is the season of a star at ra degrees that holds t.
func SeasonOf(ra float64, t time.Time) Season {
	opp := t
	for range 4 {
		sun, _ := sky.SunPosition(opp)
		d := math.Mod(ra+180-sun+540, 360) - 180
		opp = opp.Add(time.Duration(d / 360 * yearDays * 24 * float64(time.Hour)))
	}
	half := time.Duration(yearDays / 2 * 24 * float64(time.Hour))
	return Season{Start: opp.Add(-half), End: opp.Add(half), Opposition: opp}
}

func (s Season) holds(t time.Time) bool { return !t.Before(s.Start) && t.Before(s.End) }

// KindQuota is how many eclipses of one kind the app asks for in a
// season, how many are timed and how many are asked for and not yet
// timed.
type KindQuota struct {
	Kind   string `json:"kind"`
	Label  string `json:"label"`
	Want   int    `json:"want"`
	Timed  int    `json:"timed"`
	Asked  int    `json:"asked"`
	Needed int    `json:"needed"`
}

// SeasonView is one star's automatic requests this season.
type SeasonView struct {
	Season
	Label    string        `json:"label"`
	Kinds    []KindQuota   `json:"kinds"`
	Stopped  string        `json:"stopped"`
	Requests []RequestView `json:"requests"`
}

// quota is how many eclipses of each kind a season asks for: two of the
// deeper and one of the second, or three where only one is seen; two more
// of the deeper where the period could be twice this, one more of each
// where it could be half, and one more of the deeper where the record has
// few eclipses.
func quota(st Star) map[string]int {
	e, dbl := st.ephemeris()
	if e == nil {
		return nil
	}
	q := map[string]int{KindPrimary: 2, KindSecondary: 1}
	if len(e.Eclipses) == 1 {
		q = map[string]int{KindPrimary: 3}
	}
	if st.Report == nil {
		return q
	}
	f := Facts{Ephemeris: e, Doubled: dbl, Deeper: st.deeper()}
	if st.Facts != nil {
		f = *st.Facts
	}
	for _, fl := range Flags(st.Report, f) {
		switch {
		case fl.ID == FlagTwice:
			q[KindPrimary] += 2
		case fl.ID == FlagHalf && len(e.Eclipses) == 2:
			q[KindPrimary]++
			q[KindSecondary]++
		case fl.ID == FlagFew:
			q[KindPrimary]++
		}
	}
	return q
}

func open(r app.EclipseRequest) bool {
	return r.State == app.EclipseWaiting || r.State == app.EclipseSent
}

// seasonView counts this season's timings and requests of st against its
// quota and says whether asking has stopped.
func seasonView(st Star, reqs []app.EclipseRequest, now time.Time) *SeasonView {
	q := quota(st)
	if q == nil || st.Facts == nil || st.Facts.Gaia == nil {
		return nil
	}
	ra, _ := st.Facts.Gaia.Now(now)
	sv := &SeasonView{Season: SeasonOf(ra, now), Requests: []RequestView{}}
	sv.Label = "This season, best in " + sv.Opposition.Format("January 2006")
	timed := map[string]int{}
	for _, t := range st.Timings {
		if t.State == app.TimingGood && sv.holds(t.Predicted) {
			timed[t.Kind]++
		}
	}
	asked := map[string]int{}
	fails := 0
	var lastFail string
	for _, r := range reqs {
		if r.SourceID != st.Source.ID || !sv.holds(r.Mid) {
			continue
		}
		if r.Auto {
			sv.Requests = append(sv.Requests, RequestView{ID: r.ID, State: r.State, Note: r.Note, Auto: true})
		}
		switch {
		case r.Auto && r.State == app.EclipseCancel:
			sv.Stopped = "You cancelled one of this season's requests, so the app asks for no more of this star until next season."
		case r.Auto && r.State == app.EclipseFailed:
			fails++
			lastFail = r.Note
		case open(r):
			asked[r.Kind]++
		case r.State == app.EclipseDone && now.Before(r.End.Add(awaitTiming)) && matchTiming(st.Timings, r.SourceID, r.Kind, r.Mid, periodOf(st)) == nil:
			asked[r.Kind]++
		}
	}
	if sv.Stopped == "" && fails >= autoFailStop {
		sv.Stopped = fmt.Sprintf("%d of this season's requests failed, so the app asks for no more of this star until next season. The last: %s", fails, lastFail)
	}
	for _, k := range []string{KindPrimary, KindSecondary} {
		want, ok := q[k]
		if !ok {
			continue
		}
		kq := KindQuota{Kind: k, Label: kindLabelOf(k, st.Facts.Doubled && st.Facts.Alike), Want: want, Timed: timed[k], Asked: asked[k]}
		kq.Needed = max(0, want-kq.Timed-kq.Asked)
		sv.Kinds = append(sv.Kinds, kq)
	}
	return sv
}

func periodOf(st Star) float64 {
	if e, _ := st.ephemeris(); e != nil {
		return e.Period
	}
	return 1
}

// autoRequests asks the scheduler for each star's eclipses until the
// season's quota is timed or asked for: the soonest eclipse of a needed
// kind in the next two weeks that is dark and high from the start to the
// end of the dip, whose window overlaps no open request, and that was
// never asked for before; one open request per star and maxOpenAuto in
// all.
func (s *Service) autoRequests(ctx context.Context, stars []Star) error {
	if s.Commands == nil || s.Snapshot == nil || s.Site == nil {
		return nil
	}
	var site *sky.Site
	if v, err := s.Site(ctx); err == nil {
		site = &v
	}
	if site == nil {
		return nil
	}
	var reqs []app.EclipseRequest
	if err := s.DB.WithContext(ctx).Find(&reqs).Error; err != nil {
		return err
	}
	now := s.now()
	openAuto := 0
	for _, r := range reqs {
		if r.Auto && open(r) {
			openAuto++
		}
	}
	type cand struct {
		st   Star
		sv   *SeasonView
		fill float64
	}
	var cands []cand
	for _, st := range stars {
		if !EclipsingBinary(st.Report) {
			continue
		}
		sv := seasonView(st, reqs, now)
		if sv == nil || sv.Stopped != "" || slices.ContainsFunc(reqs, func(r app.EclipseRequest) bool { return r.SourceID == st.Source.ID && r.Auto && open(r) }) {
			continue
		}
		want, have := 0, 0
		for _, k := range sv.Kinds {
			want += k.Want
			have += k.Want - k.Needed
		}
		if have >= want {
			continue
		}
		cands = append(cands, cand{st: st, sv: sv, fill: float64(have) / float64(want)})
	}
	slices.SortStableFunc(cands, func(a, b cand) int { return cmp.Compare(a.fill, b.fill) })
	var errs []error
	for _, c := range cands {
		if openAuto >= maxOpenAuto || ctx.Err() != nil {
			break
		}
		p, ok := nextAuto(c.st, c.sv, *site, reqs, now)
		if !ok {
			continue
		}
		r, err := s.requestImaging(ctx, c.st.Source.ID, p.Kind, p.Mid, true)
		if err != nil {
			errs = append(errs, fmt.Errorf("source %d: %w", c.st.Source.ID, err))
		}
		if r.ID != 0 {
			reqs = append(reqs, r)
			if open(r) {
				openAuto++
			}
		}
	}
	return errors.Join(errs...)
}

// nextAuto is the soonest eclipse of st the season still needs.
func nextAuto(st Star, sv *SeasonView, site sky.Site, reqs []app.EclipseRequest, now time.Time) (Predicted, bool) {
	e, _ := st.ephemeris()
	need := map[string]bool{}
	for _, k := range sv.Kinds {
		need[k.Kind] = k.Needed > 0
	}
	ra, dec := st.Facts.Gaia.Now(now)
	for _, p := range Predict(e, st.deeper(), ra, dec, now.Add(autoLead), now.Add(autoAhead)) {
		p = st.withPhaseError(p, e)
		if !need[p.Kind] || !sv.holds(p.Mid) {
			continue
		}
		start, end := p.Window()
		if !seenThrough(site, ra, dec, p) || clash(reqs, st.Source.ID, p, start, end, now) {
			continue
		}
		return p, true
	}
	return Predicted{}, false
}

// seenThrough says the site sees the dip's middle and both its edges.
func seenThrough(site sky.Site, ra, dec float64, p Predicted) bool {
	for _, t := range []time.Time{p.Mid.Add(-p.Duration / 2), p.Mid, p.Mid.Add(p.Duration / 2)} {
		if ok, _ := Seen(site, ra, dec, t); !ok {
			return false
		}
	}
	return true
}

// clash says the eclipse was asked for before, in any state but moved, or its
// window overlaps an open request of any star.
func clash(reqs []app.EclipseRequest, id int, p Predicted, start, end, now time.Time) bool {
	for _, r := range reqs {
		if r.SourceID == id && r.State != app.EclipseMoved && sameEclipse(r.Kind, p.Kind, r.Mid.Sub(p.Mid)) {
			return true
		}
		if open(r) && r.End.After(now) && r.Start.Before(end) && start.Before(r.End) {
			return true
		}
	}
	return false
}
