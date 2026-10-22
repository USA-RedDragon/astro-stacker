package binaries

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/planning"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/eclfacts"
	"gorm.io/gorm"
)

func hours(h float64) time.Duration { return time.Duration(h * float64(time.Hour)) }

// star618 is source 618, Gaia DR3 171075625506327552 at 04 06 48.14
// +34 49 36.4, every 2.2563970 days, seen from the observatory near
// Rockwood, Texas: its second eclipse of Nov 2, 2026 07:11 UTC lasts
// 8.1 h, and its deeper one 7.2 h, timed to 1.2 min.
type star618 struct {
	site           sky.Site
	ra, dec        float64
	second, deeper Predicted
}

func newStar618() star618 {
	return star618{site: sky.Site{Latitude: 31.53, Longitude: -99.37}, ra: 61.70058, dec: 34.82678,
		second: Predicted{Dip: 1, Kind: KindSecondary, Mid: time.Date(2026, 11, 2, 7, 10, 47, 0, time.UTC), Duration: hours(8.095952819930565)},
		deeper: Predicted{Dip: 0, Kind: KindPrimary, Mid: time.Date(2026, 11, 1, 4, 3, 8, 0, time.UTC), Err: 72 * time.Second, Duration: hours(7.238867312791258)}}
}

func (s star618) why(p Predicted) string { return vagueWhy(s.site, s.ra, s.dec, p) }

func (s star618) secondWith(err time.Duration) Predicted {
	p := s.second
	p.Err = err
	return p
}

func TestSecondEclipseOf618IsTooVague(t *testing.T) {
	t.Parallel()
	s := newStar618()
	if night := nightDark(s.site, s.ra, s.dec, s.second); night < 9*time.Hour || night > 12*time.Hour {
		t.Fatalf("618 is up in the dark %s on the night of Nov 2, want 9 to 12 h", night)
	}
	if why := s.why(s.secondWith(340 * time.Minute)); !strings.Contains(why, "±5.7 h") || !strings.Contains(why, "42.1 h") {
		t.Errorf("the second eclipse uncertain by 340 min: %q", why)
	}
	if s.why(s.secondWith(hours(90.9/60))) == "" {
		t.Error("the 91 min error spreads the 8.1 h dip over 17.2 h, more than the night")
	}
	if why := s.why(s.secondWith(5 * time.Minute)); why != "" {
		t.Errorf("the second eclipse timed to 5 min fits the night: %q", why)
	}
	if why := s.why(s.deeper); why != "" {
		t.Errorf("the deeper eclipse is still asked for: %q", why)
	}
	wide := s.deeper
	wide.Err = 340 * time.Minute
	if why := s.why(wide); why != "" {
		t.Errorf("deeper eclipses keep their rule whatever their error: %q", why)
	}
}

func TestWellMeasuredSecondEclipseIsAsked(t *testing.T) {
	t.Parallel()
	s := newStar618()
	mid := s.second.Mid
	cases := []struct {
		name string
		p    Predicted
	}{
		{"a 2 h dip timed to 15 min", Predicted{Dip: 1, Kind: KindSecondary, Mid: mid, Duration: 2 * time.Hour, Err: 15 * time.Minute}},
		{"an 8.8 h dip timed to 4 min, over 9.2 h of a 9.0 h night, is long, not vague", Predicted{Dip: 1, Kind: KindSecondary, Mid: mid, Duration: hours(8.8), Err: 4 * time.Minute}},
		{"a dip longer than the night", Predicted{Dip: 1, Kind: KindSecondary, Mid: mid, Duration: 14 * time.Hour, Err: 2 * time.Hour}},
	}
	for _, c := range cases {
		if why := s.why(c.p); why != "" {
			t.Errorf("%s: %q", c.name, why)
		}
	}
}

func setPhaseErr(t *testing.T, db *gorm.DB, id int, phaseErr float64) {
	t.Helper()
	var row app.BinaryFacts
	if err := db.Where("source_id = ?", id).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	var f Facts
	if err := json.Unmarshal([]byte(row.Facts), &f); err != nil {
		t.Fatal(err)
	}
	f.Survey = &eclfacts.Summary{Period: f.Ephemeris.Period, Twin: &eclfacts.Twin{Phase2: 0.5, PhaseErr: phaseErr}}
	raw, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&row).Update("facts", string(raw)).Error; err != nil {
		t.Fatal(err)
	}
}

// vagueFixture is the seasonal star with its second dip's phase measured
// to phaseErr, its requests and what the scheduler was asked to undo.
type vagueFixture struct {
	t      *testing.T
	db     *gorm.DB
	svc    *Service
	site   sky.Site
	now    time.Time
	id     int
	undone []string
}

func newVagueFixture(t *testing.T, phaseErr float64) *vagueFixture {
	t.Helper()
	f := &vagueFixture{t: t, site: sky.Site{Latitude: 40, Longitude: 0}, now: time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)}
	f.db = seasonalDB(t, f.site, f.now)
	f.svc = &Service{DB: f.db, Commands: undoCount{undone: &f.undone}, Now: func() time.Time { return f.now },
		Site:     func(context.Context) (sky.Site, error) { return f.site, nil },
		Snapshot: func(context.Context) (*planning.Snapshot, error) { return &planning.Snapshot{}, nil }}
	f.id = f.stars()[0].Source.ID
	setPhaseErr(t, f.db, f.id, phaseErr)
	return f
}

func (f *vagueFixture) stars() []Star {
	f.t.Helper()
	stars, err := f.svc.Binaries(context.Background())
	if err != nil || len(stars) != 1 {
		f.t.Fatalf("stars %d %v", len(stars), err)
	}
	return stars
}

// seen is the star's eclipses of kind from a day and a half to ten days
// ahead that the site sees from start to end.
func (f *vagueFixture) seen(kind string) []Predicted {
	st := f.stars()[0]
	e, _ := st.ephemeris()
	ra, dec := st.Facts.Gaia.Now(f.now)
	var out []Predicted
	for _, p := range Predict(e, st.deeper(), ra, dec, f.now.Add(36*time.Hour), f.now.Add(10*24*time.Hour)) {
		if p = st.withPhaseError(p, e); p.Kind == kind && seenThrough(f.site, ra, dec, p) {
			out = append(out, p)
		}
	}
	return out
}

func (f *vagueFixture) get(id int) app.EclipseRequest {
	f.t.Helper()
	var r app.EclipseRequest
	if err := f.db.First(&r, id).Error; err != nil {
		f.t.Fatal(err)
	}
	return r
}

func (f *vagueFixture) all() []app.EclipseRequest {
	f.t.Helper()
	var reqs []app.EclipseRequest
	if err := f.db.Order("id").Find(&reqs).Error; err != nil {
		f.t.Fatal(err)
	}
	return reqs
}

func (f *vagueFixture) tick() {
	f.t.Helper()
	stars := f.stars()
	if err := f.svc.holdVague(context.Background(), stars); err != nil {
		f.t.Fatal(err)
	}
	if err := f.svc.redraftMoved(context.Background(), stars); err != nil {
		f.t.Fatal(err)
	}
}

// secondOnly is a season that needs one second eclipse.
func (f *vagueFixture) secondOnly() *SeasonView {
	ra, _ := f.stars()[0].Facts.Gaia.Now(f.now)
	return &SeasonView{Season: SeasonOf(ra, f.now), Kinds: []KindQuota{{Kind: KindSecondary, Want: 1, Needed: 1}}}
}

func (f *vagueFixture) heldRows() (second, deeper int) {
	site := f.site
	for _, row := range Plan(PlanInput{Stars: f.stars(), Site: &site, From: f.now, To: f.now.Add(10 * 24 * time.Hour), All: true, Requests: f.all()}).Eclipses {
		switch {
		case row.Held == "":
		case row.Kind == KindPrimary:
			deeper++
		case strings.HasPrefix(row.Held, "Not requested: the second eclipse's timing is uncertain by ±") && row.Request == nil:
			second++
		}
	}
	return second, deeper
}

func TestVagueSecondEclipsesAreWithdrawn(t *testing.T) {
	t.Parallel()
	f := newVagueFixture(t, 0.152)
	sec, prim := f.seen(KindSecondary), f.seen(KindPrimary)
	if len(prim) == 0 || len(sec) < 2 || sec[0].Err < 4*time.Hour {
		t.Fatalf("primaries %d, seen secondaries %d %+v", len(prim), len(sec), sec)
	}
	pending, deeper := requestFor(sec[0], 0), requestFor(prim[0], 0)
	pending.Start, pending.End = sec[0].Mid.Add(-time.Hour), sec[0].Mid.Add(time.Hour)
	begun := requestFor(sec[0], 0)
	begun.Mid, begun.Start, begun.End = f.now.Add(-time.Hour), f.now.Add(-2*time.Hour), f.now.Add(time.Hour)
	for i, r := range []*app.EclipseRequest{&pending, &deeper, &begun} {
		r.SourceID, r.CreateCmd = f.id, []string{"pending", "deeper", "begun"}[i]
		if err := f.db.Create(r).Error; err != nil {
			t.Fatal(err)
		}
	}
	f.tick()
	if r := f.get(pending.ID); r.State != app.EclipseHeld || !strings.HasPrefix(r.Note, "Withdrawn: the second eclipse's timing is uncertain by ±") {
		t.Errorf("the pending second eclipse is withdrawn: %+v", r)
	}
	if len(f.undone) != 1 || f.undone[0] != "pending" {
		t.Errorf("withdrawn through the scheduler: %v", f.undone)
	}
	if r := f.get(deeper.ID); r.State != app.EclipseSent {
		t.Errorf("the deeper eclipse's request is left alone: %+v", r)
	}
	if r := f.get(begun.ID); r.State != app.EclipseSent {
		t.Errorf("a request already under way is left alone: %+v", r)
	}
	if n := len(f.all()); n != 3 {
		t.Errorf("nothing asked for again while vague: %d requests", n)
	}
	if _, err := f.svc.RequestImaging(context.Background(), f.id, KindSecondary, sec[1].Mid); !errors.Is(err, ErrVague) {
		t.Errorf("asking for a vague second eclipse: %v", err)
	}
	if second, deeperHeld := f.heldRows(); second == 0 || deeperHeld != 0 {
		t.Errorf("planner rows: %d second eclipses not requested, %d deeper ones", second, deeperHeld)
	}
	if sv := seasonView(f.stars()[0], f.all(), f.now); sv == nil || sv.Stopped != "" {
		t.Errorf("a withdrawn request does not stop the season: %+v", sv)
	}
	if p, ok := nextAuto(f.stars()[0], f.secondOnly(), f.site, nil, f.now); ok {
		t.Errorf("the app asks for a vague second eclipse at %s", p.Mid)
	}

	setPhaseErr(t, f.db, f.id, 0.005)
	f.tick()
	if r := f.get(pending.ID); r.State != app.EclipseMoved {
		t.Errorf("the held request is settled once asked for again: %+v", r)
	}
	reqs := f.all()
	if len(reqs) != 4 || reqs[3].Kind != KindSecondary || reqs[3].Auto || absDur(reqs[3].Mid.Sub(sec[0].Mid)) > 2*time.Hour {
		t.Fatalf("the user's second eclipse is asked for again once measured better: %+v", reqs)
	}
	f.tick()
	if n := len(f.all()); n != 4 {
		t.Errorf("asked for again once only: %d requests", n)
	}
	if p, ok := nextAuto(f.stars()[0], f.secondOnly(), f.site, nil, f.now); !ok || p.Kind != KindSecondary {
		t.Errorf("the app asks for second eclipses again once measured better: %v %+v", ok, p)
	}
	if second, deeperHeld := f.heldRows(); second+deeperHeld != 0 {
		t.Errorf("eclipses still held once measured better: %d second, %d deeper", second, deeperHeld)
	}
}
