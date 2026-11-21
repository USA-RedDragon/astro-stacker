package goals

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/sky"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
)

const (
	filterBlue = "Blue"
	objLow     = "Low"
	objNew     = "New"
)

type passFixture struct {
	t   *testing.T
	db  *gorm.DB
	n   int
	now time.Time
}

func (f *passFixture) sub(object, filter, status string, score float64, at time.Time) {
	f.t.Helper()
	f.subAt(object, filter, status, score, at, nil)
}

// subAt adds a 300 s sub pointed at pos (RA, Dec in degrees), or with no pointing when pos is nil.
func (f *passFixture) subAt(object, filter, status string, score float64, at time.Time, pos *[2]float64) {
	f.t.Helper()
	f.n++
	exposure := 300.0
	fr := app.Frame{Key: fmt.Sprintf("pass-%d", f.n), ETag: "e", Type: typeLight, Object: object, Filter: filter, DateObs: &at, LastModified: at, IndexedAt: at, Exposure: &exposure}
	if pos != nil {
		fr.MountRA, fr.MountDec = &pos[0], &pos[1]
	}
	if err := f.db.Create(&fr).Error; err != nil {
		f.t.Fatal(err)
	}
	if err := f.db.Create(&app.StackFrame{FrameID: fr.ID, Status: status, Score: score, ProcessedAt: at}).Error; err != nil {
		f.t.Fatal(err)
	}
}

func (f *passFixture) status(score, best float64) string {
	if score < 0.3*best {
		return app.StackStatusLowScore
	}
	return app.StackStatusAdded
}

// history builds 2026-10-17/18 as the stacker saw it: three settled targets with Blue bests of 0.8, 0.9
// and 1.0 from a good night (night quality about 0.7) ten days back, then a poor night two days back on
// which H-a scored 0.10 to 0.15 and Blue 0.01 to 0.22.
func (f *passFixture) history() {
	f.t.Helper()
	good := f.now.Add(-10 * 24 * time.Hour)
	bests := map[string]float64{"A": 0.8, "B": 0.9, "C": 1.0}
	for i := range 30 {
		obj := []string{"A", "B", "C"}[i%3]
		s := 0.6 + 0.2*float64(i%5)/4
		if i < 3 {
			s = bests[obj]
		}
		f.sub(obj, filterBlue, app.StackStatusAdded, s, good.Add(time.Duration(i*4)*time.Minute))
	}
	poor := f.now.Add(-2 * 24 * time.Hour)
	for i := range 60 {
		at := poor.Add(time.Duration(i*3) * time.Minute)
		if i%2 == 0 {
			f.sub(objGarlic, filterHa, app.StackStatusAdded, 0.10+0.05*float64(i%4)/2, at)
			continue
		}
		obj := []string{"A", "B", "C"}[i%3]
		s := 0.01 + 0.21*float64(i%7)/6
		f.sub(obj, filterBlue, f.status(s, bests[obj]), s, at)
	}
}

func TestPassInputsReproduceAPoorNight(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, db := signalDBs(t)
	now := time.Date(2026, 10, 18, 6, 0, 0, 0, time.UTC)
	f := &passFixture{t: t, db: db, now: now}
	f.history()
	for i := range 5 {
		f.sub(objGarlic, filterHa, app.StackStatusAdded, 0.10, now.Add(-time.Duration(10+i*10)*time.Minute))
	}

	in, err := LoadPassInputs(ctx, db, now, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ref := in.Reference[filterBlue]; math.Abs(ref-0.9) > 1e-9 {
		t.Fatalf("Blue reference %v", ref)
	}
	if _, ok := in.Reference[filterHa]; ok {
		t.Fatal("H-a has a reference from one target")
	}

	garlic := []string{objGarlic}
	fresh := in.ScoreCut(garlic, filterBlue, 0.3)
	if fresh == nil || math.Abs(*fresh-0.27) > 1e-9 {
		t.Fatalf("fresh target Blue cut %v", orNil(fresh))
	}
	settled := in.ScoreCut([]string{"C"}, filterBlue, 0.3)
	if settled == nil || math.Abs(*settled-0.3) > 1e-9 {
		t.Fatalf("settled cut %v", orNil(settled))
	}
	if e := in.ExpectedScore(garlic, filterHa, nil); e == nil || math.Abs(*e-0.10) > 1e-9 {
		t.Fatalf("H-a from tonight's five subs at 0.10: expected %v", orNil(e))
	}
	if e := in.ExpectedScore(garlic, filterBlue, nil); e != nil {
		t.Fatalf("Blue with no subs tonight and no sky got %v", *e)
	}
	if len(in.Sky.ratios) != 0 {
		t.Fatalf("a sky fit without the site: %d cells", len(in.Sky.ratios))
	}
	if c := in.ScoreCut([]string{objGarlic}, "L", 0.3); c != nil {
		t.Fatalf("L without any best got cut %v", *c)
	}
	if c := in.ScoreCut([]string{"C"}, filterBlue, 0); c != nil {
		t.Fatalf("MinScore 0 gave cut %v", *c)
	}
}

func TestScoreCutUsesTheReferenceUntilTheTargetSettles(t *testing.T) {
	t.Parallel()
	in := PassInputs{
		Best:      map[Key]BestScore{{Object: objNew, Filter: filterBlue}: {Score: 0.2, Subs: MinSubs - 1}, {Object: objLow, Filter: filterBlue}: {Score: 0.5, Subs: MinSubs}},
		Reference: map[string]float64{filterBlue: 0.9},
	}
	if c := in.ScoreCut([]string{objNew}, filterBlue, 0.3); c == nil || math.Abs(*c-0.27) > 1e-9 {
		t.Fatalf("unsettled cut %v", orNil(c))
	}
	if c := in.ScoreCut([]string{objLow}, filterBlue, 0.3); c == nil || math.Abs(*c-0.15) > 1e-9 {
		t.Fatalf("settled low target cut %v", orNil(c))
	}
	if c := in.ScoreCut([]string{objNew, objLow}, filterBlue, 0.3); c == nil || math.Abs(*c-0.15) > 1e-9 {
		t.Fatalf("objects of one target together cut %v", orNil(c))
	}
}

type passOut struct {
	Filter        string
	NightQuality  *float64
	ScoreCut      *float64
	ExpectedScore *float64
}

func testSite() sky.Site {
	elevation := 1000.0
	return sky.Site{Latitude: 35, Longitude: -110, Elevation: &elevation}
}

// garlicAt is where the test's Garlic target is: RA 3h, Dec +40.
func garlicAt() [2]float64 { return [2]float64{45, 40} }

// moonHistory adds Garlic Blue subs every 20 minutes through the nights around the new Moon of
// 2026-10-10 and the full Moon of 2026-10-26 at the test site: 0.05 with a Moon over 75% lit above the
// horizon, 0.9 otherwise.
func (f *passFixture) moonHistory(site sky.Site) {
	f.t.Helper()
	garlicPos := garlicAt()
	for _, day := range []int{9, 10, 11, 25, 26, 27} {
		for m := 0; m < 9*60; m += 20 {
			at := time.Date(2026, 10, day, 3, m, 0, 0, time.UTC)
			s := SkyAt(site, at, garlicPos[0], garlicPos[1])
			score := 0.9
			if s.MoonAltitude > 0 && s.Illumination > 0.75 {
				score = 0.05
			}
			f.subAt(objGarlic, filterBlue, f.status(score, 0.9), score, at, &garlicPos)
		}
	}
}

func TestSkyAtTheTestSite(t *testing.T) {
	t.Parallel()
	garlicPos := garlicAt()
	site := testSite()
	full := SkyAt(site, time.Date(2026, 11, 25, 7, 0, 0, 0, time.UTC), garlicPos[0], garlicPos[1])
	if full.Illumination < 0.95 || full.MoonAltitude < 45 {
		t.Fatalf("local midnight after the full Moon of 2026-11-24: %+v", full)
	}
	if c := full.Cell(); c.Moon != 42 || c.Alt != 3 {
		t.Fatalf("full Moon cell %+v of %+v", c, full)
	}
	dark := SkyAt(site, time.Date(2026, 11, 9, 7, 0, 0, 0, time.UTC), garlicPos[0], garlicPos[1])
	if dark.Illumination > 0.05 || dark.MoonAltitude > 0 || dark.Cell().Moon != -1 {
		t.Fatalf("local midnight at the new Moon of 2026-11-09: %+v", dark)
	}
	for _, c := range []struct {
		s    SkyState
		want SkyCell
	}{
		{SkyState{MoonAltitude: -1, Illumination: 1, Altitude: 29.9}, SkyCell{Moon: -1, Alt: 0}},
		{SkyState{MoonAltitude: 14, Illumination: 0.2, Altitude: 30}, SkyCell{Moon: 0, Alt: 1}},
		{SkyState{MoonAltitude: 15, Illumination: 0.5, Altitude: 45}, SkyCell{Moon: 21, Alt: 2}},
		{SkyState{MoonAltitude: 60, Illumination: 0.89, Altitude: 80}, SkyCell{Moon: 32, Alt: 3}},
	} {
		if got := c.s.Cell(); got != c.want {
			t.Errorf("%+v: cell %+v, want %+v", c.s, got, c.want)
		}
	}
}

func ratiosOf(n int, v float64) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = v + 0.01*float64(i)
	}
	return out
}

func TestSkyRatiosFallBackFromTheTargetToTheFilterAndTheClass(t *testing.T) {
	t.Parallel()
	c := SkyCell{Moon: 32, Alt: 2}
	r := SkyRatios{ratios: map[skyKey][]float64{
		{"object:A", filterBlue, 32, 2}:      ratiosOf(ExpectedOwnSubs, 0.10),
		{"object:B", filterBlue, 32, anyAlt}: ratiosOf(ExpectedOwnSubs, 0.40),
		{"object:C", filterBlue, 32, 2}:      ratiosOf(ExpectedOwnSubs-1, 0.90),
		{"", filterBlue, 32, 2}:              ratiosOf(ExpectedPoolSubs, 0.20),
		{"", filterHa, 32, anyAlt}:           ratiosOf(ExpectedPoolSubs, 0.50),
		{"narrowband", "", 32, anyAlt}:       ratiosOf(ExpectedPoolSubs, 0.60),
	}}
	for _, k := range []struct {
		objects []string
		filter  string
		want    float64
		source  string
	}{
		{[]string{"A"}, filterBlue, 0.10 + 0.01*(ExpectedOwnSubs-1)*ExpectedOwnQuantile, SourceTarget},
		{[]string{"B"}, filterBlue, 0.40 + 0.01*(ExpectedOwnSubs-1)*ExpectedOwnQuantile, SourceTargetMoon},
		{[]string{"C"}, filterBlue, 0.20 + 0.01*(ExpectedPoolSubs-1)*ExpectedPoolQuantile, SourceFilter},
		{[]string{"A", "C"}, filterBlue, percentileSorted(append(ratiosOf(ExpectedOwnSubs, 0.10), ratiosOf(ExpectedOwnSubs-1, 0.90)...), 100*ExpectedOwnQuantile), SourceTarget},
		{[]string{"A"}, filterHa, 0.50 + 0.01*(ExpectedPoolSubs-1)*ExpectedPoolQuantile, SourceFilterMoon},
		{[]string{"A"}, filterO3, 0.60 + 0.01*(ExpectedPoolSubs-1)*ExpectedPoolQuantile, SourceClassMoon},
	} {
		got, source, ok := r.Ratio(k.objects, k.filter, c)
		if !ok || math.Abs(got-k.want) > 1e-9 || source != k.source {
			t.Errorf("%v %s: %v from %q (%v), want %v from %q", k.objects, k.filter, got, source, ok, k.want, k.source)
		}
	}
	if _, _, ok := r.Ratio([]string{"A"}, "Red", c); ok {
		t.Error("Red with no subs anywhere has a ratio")
	}
	if _, _, ok := r.Ratio([]string{"A"}, filterBlue, SkyCell{Moon: -1, Alt: 2}); ok {
		t.Error("a moonless sky has the bright Moon's ratio")
	}
}

func TestExpectedScorePrefersTheTargetsSubsTonight(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 25, 7, 0, 0, 0, time.UTC)
	in := PassInputs{
		Best: map[Key]BestScore{{Object: objGarlic, Filter: filterBlue}: {Score: 0.8, Subs: MinSubs}},
		Sky:  SkyRatios{ratios: map[skyKey][]float64{{"object:" + objGarlic, filterBlue, 42, 3}: ratiosOf(ExpectedOwnSubs, 0.05)}},
	}
	bright := SkyState{MoonAltitude: 60, Illumination: 0.98, Altitude: 70}
	garlic := []string{objGarlic}
	underMoon := quantize((0.05+0.01*(ExpectedOwnSubs-1)*ExpectedOwnQuantile)*0.8, passScoreStep)
	e := in.ExpectedScore(garlic, filterBlue, &bright)
	if e == nil || math.Abs(*e-underMoon) > 1e-9 {
		t.Fatalf("under the full Moon: expected %v, want %v", orNil(e), underMoon)
	}
	if e := in.ExpectedScore(garlic, filterBlue, &SkyState{MoonAltitude: -5, Altitude: 70}); e != nil {
		t.Fatalf("a sky without history got %v", *e)
	}
	in.Recent = map[Key][]RecentScore{{Object: objGarlic, Filter: filterBlue}: {{now.Add(-50 * time.Minute), 0.7}, {now.Add(-40 * time.Minute), 0.6}}}
	if e := in.ExpectedScore(garlic, filterBlue, &bright); e == nil || math.Abs(*e-underMoon) > 1e-9 {
		t.Fatalf("two subs tonight are too few: expected %v", orNil(e))
	}
	for i, s := range []float64{0.01, 0.02, 0.5, 0.9} {
		in.Recent[Key{Object: objGarlic, Filter: filterBlue}] = append(in.Recent[Key{Object: objGarlic, Filter: filterBlue}], RecentScore{now.Add(-time.Duration(30-5*i) * time.Minute), s})
	}
	if e := in.ExpectedScore(garlic, filterBlue, &bright); e == nil || math.Abs(*e-0.5) > 1e-9 {
		t.Fatalf("the median of the newest five of tonight's subs: expected %v", orNil(e))
	}
	if e := in.ExpectedScore(garlic, filterBlue, nil); e == nil || math.Abs(*e-0.5) > 1e-9 {
		t.Fatalf("tonight's subs without the sky: expected %v", orNil(e))
	}
	in.Recent[Key{Object: objGarlic, Filter: filterBlue}] = []RecentScore{{now, 1}, {now, 1}, {now, 1}}
	if e := in.ExpectedScore(garlic, filterBlue, nil); e == nil || math.Abs(*e-0.8) > 1e-9 {
		t.Fatalf("subs over the best: expected %v, want the best", orNil(e))
	}
}

func TestLoadRecentTakesTheLastHour(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, db := signalDBs(t)
	now := time.Date(2026, 11, 25, 7, 0, 0, 0, time.UTC)
	f := &passFixture{t: t, db: db, now: now}
	f.sub(objGarlic, filterBlue, app.StackStatusAdded, 0.5, now.Add(-30*time.Minute))
	f.sub(objGarlic, filterBlue, app.StackStatusMoon, 0.4, now.Add(-20*time.Minute))
	f.sub(objGarlic, "Ha", app.StackStatusAdded, 0.3, now.Add(-20*time.Minute))
	f.sub(objGarlic, filterBlue, app.StackStatusLowScore, 0.1, now.Add(-10*time.Minute))
	f.sub(objGarlic, filterBlue, app.StackStatusAdded, 0.9, now.Add(-2*time.Hour))
	f.sub(objGarlic, filterBlue, app.StackStatusRejected, 0.9, now.Add(-5*time.Minute))
	f.sub(objGarlic, filterBlue, app.StackStatusAdded, 0.9, now.Add(5*time.Minute))
	recent, err := LoadRecent(ctx, db, now)
	if err != nil {
		t.Fatal(err)
	}
	got := recent[Key{Object: objGarlic, Filter: filterBlue}]
	if len(got) != 3 || got[0].Score != 0.5 || got[1].Score != 0.4 || got[2].Score != 0.1 || len(recent) != 2 || len(recent[Key{Object: objGarlic, Filter: filterHa}]) != 1 {
		t.Fatalf("recent %+v", recent)
	}
}

func TestFitSkyBinsSubsByTheSkyTheyWereTakenUnder(t *testing.T) {
	t.Parallel()
	garlicPos := garlicAt()
	site := testSite()
	in := PassInputs{Best: map[Key]BestScore{{Object: objGarlic, Filter: filterBlue}: {Score: 0.5, Subs: MinSubs}}}
	at := time.Date(2026, 11, 25, 7, 0, 0, 0, time.UTC)
	exposure := 600.0
	bad := math.NaN()
	subs := []PassSub{
		{Object: objGarlic, Filter: " Blue ", Key: "a.xisf", Score: 0.4, DateObs: at, Exposure: &exposure, MountRA: &garlicPos[0], MountDec: &garlicPos[1]},
		{Object: objGarlic, Filter: filterBlue, Key: "b.xisf", Score: 0.9, DateObs: at, MountRA: &garlicPos[0], MountDec: &garlicPos[1]},
		{Object: objGarlic, Filter: filterBlue, Key: "c_cal.xisf", Score: 0.1, DateObs: at, MountRA: &garlicPos[0], MountDec: &garlicPos[1]},
		{Object: objGarlic, Filter: filterBlue, Key: "d.xisf", Score: 0.1, DateObs: at},
		{Object: objGarlic, Filter: filterBlue, Key: "e.xisf", Score: bad, DateObs: at, MountRA: &garlicPos[0], MountDec: &garlicPos[1]},
		{Object: "Unknown", Filter: filterBlue, Key: "f.xisf", Score: 0.3, DateObs: at, MountRA: &garlicPos[0], MountDec: &garlicPos[1]},
	}
	r := in.FitSky(subs, site)
	c := SkyAt(site, at.Add(5*time.Minute), garlicPos[0], garlicPos[1]).Cell()
	for _, k := range []skyKey{
		{"object:" + objGarlic, filterBlue, c.Moon, c.Alt}, {"object:" + objGarlic, filterBlue, c.Moon, anyAlt},
		{"", filterBlue, c.Moon, c.Alt}, {"", filterBlue, c.Moon, anyAlt}, {"broadband", "", c.Moon, anyAlt},
	} {
		if got := r.ratios[k]; len(got) != 2 || math.Abs(got[0]-0.8) > 1e-9 || got[1] != 1 {
			t.Errorf("%+v: %v, want the two usable subs as 0.8 and 1 of the best", k, got)
		}
	}
	if len(r.ratios) != 5 {
		t.Errorf("%d cells, want 5", len(r.ratios))
	}
}

func TestPublishTheExpectedScoreUnderTheMoon(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sched, db := signalDBs(t)
	for _, s := range []string{
		"ALTER TABLE ts_goal_progress ADD COLUMN state TEXT", "ALTER TABLE ts_goal_progress ADD COLUMN reason TEXT",
		"ALTER TABLE ts_goal_progress ADD COLUMN stack_subs INTEGER", "ALTER TABLE ts_goal_progress ADD COLUMN min_subs INTEGER",
		"ALTER TABLE ts_goal_progress ADD COLUMN sub_limit INTEGER",
		`INSERT INTO exposuretemplate VALUES (4, 'Blue', 'Blue')`, `INSERT INTO exposureplan VALUES (4, 2, 4)`,
		`INSERT INTO ts_goal VALUES ('g-garlic', 'Blue', 0, 10, NULL, 1, NULL, '2026-10-01 00:00:00')`,
		`UPDATE target SET ra = 3, dec = 40 WHERE guid = 'g-garlic'`,
	} {
		if err := sched.Exec(s).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.AutoMigrate(&app.Stack{}); err != nil {
		t.Fatal(err)
	}
	site := testSite()
	now := time.Date(2026, 11, 25, 7, 0, 0, 0, time.UTC)
	f := &passFixture{t: t, db: db, now: now}
	f.moonHistory(site)
	garlicPos := garlicAt()
	p := &Publisher{App: db, Sched: sched, Mode: PublishOn, Now: func() time.Time { return now }, MinScore: 0.3,
		Site: func(context.Context) (sky.Site, error) { return site, nil }}
	if _, err := p.Publish(ctx); err != nil {
		t.Fatal(err)
	}
	var blue passOut
	if err := sched.Table(ProgressTable).Where("filter = ?", filterBlue).Scan(&blue).Error; err != nil {
		t.Fatal(err)
	}
	if blue.ScoreCut != nil || blue.ExpectedScore != nil {
		t.Fatalf("published pass columns the table lacks: %+v", blue)
	}

	for _, c := range []string{"score_cut REAL", "expected_score REAL"} {
		if err := sched.Exec("ALTER TABLE ts_goal_progress ADD COLUMN " + c).Error; err != nil {
			t.Fatal(err)
		}
	}
	read := func(what string) passOut {
		t.Helper()
		if sum, err := p.Publish(ctx); err != nil || sum.Changed == 0 {
			t.Fatalf("publish %s: %+v %v", what, sum, err)
		}
		var out passOut
		if err := sched.Table(ProgressTable).Where("filter = ?", filterBlue).Scan(&out).Error; err != nil {
			t.Fatal(err)
		}
		if out.ScoreCut == nil || math.Abs(*out.ScoreCut-0.27) > 1e-9 || out.ExpectedScore == nil {
			t.Fatalf("Blue row %s: cut %v expected %v", what, orNil(out.ScoreCut), orNil(out.ExpectedScore))
		}
		return out
	}
	if blue = read("under the full Moon"); *blue.ExpectedScore >= *blue.ScoreCut {
		t.Fatalf("under the full Moon: expected %v, not under the cut %v", *blue.ExpectedScore, *blue.ScoreCut)
	}
	if blue.NightQuality != nil {
		t.Fatalf("a night quality without subs tonight: %v", *blue.NightQuality)
	}
	if sum, err := p.Publish(ctx); err != nil || sum.Changed != 0 {
		t.Fatalf("unchanged publish %+v %v", sum, err)
	}

	for i := range 3 {
		f.subAt(objGarlic, filterBlue, app.StackStatusAdded, 0.6, now.Add(-time.Duration(10+10*i)*time.Minute), &garlicPos)
	}
	if blue = read("after three good subs tonight"); math.Abs(*blue.ExpectedScore-0.6) > 1e-9 {
		t.Fatalf("after three subs at 0.6 tonight: expected %v", *blue.ExpectedScore)
	}

	now = time.Date(2026, 12, 9, 7, 0, 0, 0, time.UTC)
	if blue = read("at the new Moon"); *blue.ExpectedScore < 0.8 {
		t.Fatalf("at the new Moon: expected %v", *blue.ExpectedScore)
	}
}

func orNil(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}
