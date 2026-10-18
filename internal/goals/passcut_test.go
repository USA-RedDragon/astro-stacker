package goals

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

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
	f.n++
	fr := app.Frame{Key: fmt.Sprintf("pass-%d", f.n), ETag: "e", Type: typeLight, Object: object, Filter: filter, DateObs: &at, LastModified: at, IndexedAt: at}
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

	in, err := LoadPassInputs(ctx, db, now)
	if err != nil {
		t.Fatal(err)
	}
	if ref := in.Reference[filterBlue]; math.Abs(ref-0.9) > 1e-9 {
		t.Fatalf("Blue reference %v", ref)
	}
	if _, ok := in.Reference[filterHa]; ok {
		t.Fatal("H-a has a reference from one target")
	}
	q, _, err := NightQuality(ctx, db, now)
	if err != nil || q == nil || math.Abs(*q-0.1) > 1e-9 {
		t.Fatalf("night quality %v %v", q, err)
	}

	garlic := []string{objGarlic}
	fresh := in.ScoreCut(garlic, filterBlue, 0.3)
	poor := in.ExpectedScore(garlic, filterBlue, q)
	if fresh == nil || math.Abs(*fresh-0.27) > 1e-9 || poor == nil || *poor >= *fresh {
		t.Fatalf("fresh target Blue at quality 0.10: expected %v, cut %v", orNil(poor), orNil(fresh))
	}
	settled := in.ScoreCut([]string{"C"}, filterBlue, 0.3)
	if settled == nil || math.Abs(*settled-0.3) > 1e-9 {
		t.Fatalf("settled cut %v", orNil(settled))
	}

	good := 0.7
	if e := in.ExpectedScore(garlic, filterBlue, &good); e == nil || *e < *fresh {
		t.Fatalf("Blue at quality 0.7: expected %v under cut %v", orNil(e), *fresh)
	}
	far := 0.4
	if e := in.ExpectedScore(garlic, filterBlue, &far); e != nil {
		t.Fatalf("Blue at quality 0.4 has no subs near it, got %v", *e)
	}
	if e := in.ExpectedScore(garlic, "L", &good); e != nil {
		t.Fatalf("L without history got %v", *e)
	}
	if c := in.ScoreCut([]string{objGarlic}, "L", 0.3); c != nil {
		t.Fatalf("L without any best got cut %v", *c)
	}
	if c := in.ScoreCut([]string{"C"}, filterBlue, 0); c != nil {
		t.Fatalf("MinScore 0 gave cut %v", *c)
	}
	if e := in.ExpectedScore(garlic, filterBlue, nil); e != nil {
		t.Fatalf("no night quality gave %v", *e)
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

	for range PassMinSamples {
		in.History = map[string][]QualityRatio{filterBlue: append(in.History[filterBlue], QualityRatio{Quality: 0.6, Ratio: 0.8}, QualityRatio{Quality: 0.1, Ratio: 0.2})}
	}
	good, poor := 0.6, 0.1
	for _, c := range []struct {
		obj      string
		q        *float64
		expected float64
	}{{objNew, &good, 0.72}, {objNew, &poor, 0.18}, {objLow, &good, 0.4}, {objLow, &poor, 0.1}} {
		e := in.ExpectedScore([]string{c.obj}, filterBlue, c.q)
		if e == nil || math.Abs(*e-c.expected) > 1e-9 {
			t.Errorf("%s at quality %v: expected %v, want %v", c.obj, *c.q, orNil(e), c.expected)
		}
	}
}

type passOut struct {
	Filter        string
	NightQuality  *float64
	ScoreCut      *float64
	ExpectedScore *float64
}

func TestPublishScoreCutAndExpectedScore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sched, db := signalDBs(t)
	for _, s := range []string{
		"ALTER TABLE ts_goal_progress ADD COLUMN state TEXT", "ALTER TABLE ts_goal_progress ADD COLUMN reason TEXT",
		"ALTER TABLE ts_goal_progress ADD COLUMN stack_subs INTEGER", "ALTER TABLE ts_goal_progress ADD COLUMN min_subs INTEGER",
		"ALTER TABLE ts_goal_progress ADD COLUMN sub_limit INTEGER",
		`INSERT INTO exposuretemplate VALUES (4, 'Blue', 'Blue')`, `INSERT INTO exposureplan VALUES (4, 2, 4)`,
		`INSERT INTO ts_goal VALUES ('g-garlic', 'Blue', 0, 10, NULL, 1, NULL, '2026-10-01 00:00:00')`,
	} {
		if err := sched.Exec(s).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.AutoMigrate(&app.Stack{}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 18, 6, 0, 0, 0, time.UTC)
	f := &passFixture{t: t, db: db, now: now}
	f.history()
	for i := range 5 {
		f.sub(objGarlic, filterHa, app.StackStatusAdded, 0.10, now.Add(-time.Duration(10+i*10)*time.Minute))
	}
	p := &Publisher{App: db, Sched: sched, Mode: PublishOn, Now: func() time.Time { return now }, MinScore: 0.3}
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
	if sum, err := p.Publish(ctx); err != nil || sum.Changed == 0 {
		t.Fatalf("publish %+v %v", sum, err)
	}
	if err := sched.Table(ProgressTable).Where("filter = ?", filterBlue).Scan(&blue).Error; err != nil {
		t.Fatal(err)
	}
	if blue.NightQuality == nil || math.Abs(*blue.NightQuality-0.1) > 1e-9 || blue.ScoreCut == nil || math.Abs(*blue.ScoreCut-0.27) > 1e-9 ||
		blue.ExpectedScore == nil || *blue.ExpectedScore >= *blue.ScoreCut {
		t.Fatalf("Blue row %+v cut %v expected %v", blue, orNil(blue.ScoreCut), orNil(blue.ExpectedScore))
	}
	t.Logf("quality %v: Blue cut %v expected %v", *blue.NightQuality, *blue.ScoreCut, *blue.ExpectedScore)
	if sum, err := p.Publish(ctx); err != nil || sum.Changed != 0 {
		t.Fatalf("unchanged publish %+v %v", sum, err)
	}

	now = now.Add(30 * time.Minute)
	for i := range 12 {
		f.sub(objGarlic, filterHa, app.StackStatusAdded, 0.7, now.Add(-time.Duration(i+1)*time.Minute))
	}
	if sum, err := p.Publish(ctx); err != nil || sum.Changed == 0 {
		t.Fatalf("publish after the sky cleared %+v %v", sum, err)
	}
	if err := sched.Table(ProgressTable).Where("filter = ?", filterBlue).Scan(&blue).Error; err != nil {
		t.Fatal(err)
	}
	if blue.NightQuality == nil || math.Abs(*blue.NightQuality-0.7) > 1e-9 || blue.ExpectedScore == nil || *blue.ExpectedScore < *blue.ScoreCut {
		t.Fatalf("Blue row after the sky cleared %+v expected %v", blue, orNil(blue.ExpectedScore))
	}
	t.Logf("quality %v: Blue cut %v expected %v", *blue.NightQuality, *blue.ScoreCut, *blue.ExpectedScore)
}

func orNil(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}
