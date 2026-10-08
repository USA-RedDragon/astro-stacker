package stacking

import (
	"context"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/measure"
	"github.com/USA-RedDragon/astro-stacker/internal/quality"
	"github.com/USA-RedDragon/astro-stacker/internal/siril"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

const (
	subTSReject = "tsreject.xisf"
	subR1       = "r1.xisf"
	subB1       = "b1.xisf"
	subB2       = "b2.xisf"
)

type rescoreSub struct {
	stack *app.Stack
	score float64
}

// Masters stacked under an older scoreMethod have their subs scored again:
// a hazy sub transparency puts under the cut leaves and its master is
// restacked; one under the cut without transparency too (the references
// moved since it was stacked) stays, re-weighted; a master whose weights
// hardly move is only re-weighted; one already scored this way, or with a
// sub not yet photometered, is left alone.
func TestRescoreAdded(t *testing.T) {
	t.Parallel()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&app.Frame{}, &app.StackFrame{}, &app.Stack{}); err != nil {
		t.Fatal(err)
	}
	orion := app.Stack{Object: objectOrion, Filter: filterLuminance}
	m31 := app.Stack{Object: objectM31, Filter: filterGreen}
	done := app.Stack{Object: "M42", Filter: filterRed, ScoreMethod: scoreMethod}
	waiting := app.Stack{Object: "M45", Filter: filterBlue}
	for _, s := range []*app.Stack{&orion, &m31, &done, &waiting} {
		if err := db.Create(s).Error; err != nil {
			t.Fatal(err)
		}
	}
	// The masters' subs as stacked: 300 s, weight = score x exposure.
	subs := map[string]rescoreSub{
		"clear.xisf": {&orion, 1},
		"hazy.xisf":  {&orion, 0.77},
		"lost.xisf":  {&orion, 0.8}, // no scheduler record now
		subTSReject:  {&orion, 0.9}, // rejected in Target Scheduler since
		"drift.xisf": {&orion, 0.4}, // under the cut without transparency too
		"g1.xisf":    {&m31, 1},
		"g2.xisf":    {&m31, 0.9},
		subR1:        {&done, 0.9},
		subB1:        {&waiting, 0.9},
		subB2:        {&waiting, 0.9}, // not photometered yet
	}
	ids := addRescoreSubs(t, db, subs)
	scores := map[string]quality.SubScore{
		// Orion's hazy sub: 0.77 x 0.57^2 = 0.25, under 0.3 of the best.
		"clear.xisf": {Score: 0.95, TargetBest: 0.95, Transparency: 0.97, PlainScore: 1, PlainTargetBest: 1},
		"hazy.xisf":  {Score: 0.25, TargetBest: 0.95, Transparency: 0.57, PlainScore: 0.77, PlainTargetBest: 1},
		"drift.xisf": {Score: 0.2, TargetBest: 0.95, Transparency: 0.95, PlainScore: 0.22, PlainTargetBest: 1},
		subTSReject:  {Score: 0, TargetBest: 0.95, GradingStatus: quality.GradingRejected},
		// M31: clear, weights within rescoreWeightTolerance.
		"g1.xisf": {Score: 0.92, TargetBest: 0.92, Transparency: 0.96, PlainScore: 1, PlainTargetBest: 1},
		"g2.xisf": {Score: 0.85, TargetBest: 0.92, Transparency: 0.97, PlainScore: 0.9, PlainTargetBest: 1},
		// Would leave, but its master was scored this way already.
		subR1: {Score: 0.1, TargetBest: 1, PlainScore: 1, PlainTargetBest: 1},
		// Would leave, but a sub of its master has no photometry yet.
		subB1: {Score: 0.1, TargetBest: 1, PlainScore: 1, PlainTargetBest: 1},
		subB2: {Score: 0.9, TargetBest: 1, PlainScore: 1, PlainTargetBest: 1},
	}
	opts := DefaultPipelineOptions()
	opts.Photometry = true
	p := NewPipeline(nil, "", "", db, nil, siril.Runner{}, "", opts)
	now := time.Now()
	if err := p.rescore(context.Background(), scores, now); err != nil {
		t.Fatal(err)
	}

	get := func(key string) app.StackFrame {
		t.Helper()
		var sf app.StackFrame
		if err := db.First(&sf, ids[key]).Error; err != nil {
			t.Fatal(err)
		}
		return sf
	}
	checkRescoredSubs(t, get, subs, now.Add(p.opts.RetryAfter))

	stack := func(s *app.Stack) app.Stack {
		t.Helper()
		var got app.Stack
		if err := db.First(&got, s.ID).Error; err != nil {
			t.Fatal(err)
		}
		return got
	}
	if s := stack(&orion); !s.NeedsRebuild || s.ScoreMethod != scoreMethod {
		t.Errorf("Orion: needs rebuild %v, method %d; want restacked under %d", s.NeedsRebuild, s.ScoreMethod, scoreMethod)
	}
	if s := stack(&m31); s.NeedsRebuild || s.ScoreMethod != scoreMethod {
		t.Errorf("M31: needs rebuild %v, method %d; want re-weighted only, under %d", s.NeedsRebuild, s.ScoreMethod, scoreMethod)
	}
	if s := stack(&done); s.NeedsRebuild {
		t.Error("a master already scored this way was marked")
	}
	if s := stack(&waiting); s.NeedsRebuild || s.ScoreMethod != 0 {
		t.Errorf("a master waiting for photometry: needs rebuild %v, method %d", s.NeedsRebuild, s.ScoreMethod)
	}

	// A second pass finds nothing to do.
	if err := db.Model(&app.Stack{}).Where("id = ?", orion.ID).UpdateColumn("needs_rebuild", false).Error; err != nil {
		t.Fatal(err)
	}
	scores["clear.xisf"] = quality.SubScore{Score: 0.1, TargetBest: 1, PlainScore: 1, PlainTargetBest: 1}
	if err := p.rescore(context.Background(), scores, now); err != nil {
		t.Fatal(err)
	}
	if sf := get("clear.xisf"); sf.Status != app.StackStatusAdded {
		t.Error("a master was scored again under the same method")
	}
}

func addRescoreSubs(t *testing.T, db *gorm.DB, subs map[string]rescoreSub) map[string]int {
	t.Helper()
	ids := map[string]int{}
	for key, s := range subs {
		rev := measure.PhotometryRevision
		f := app.Frame{Key: s.stack.Object + "/LIGHT/" + key, Type: frameTypeLight, LastModified: time.Now(), PhotometryRev: &rev}
		if key == subB2 {
			f.PhotometryRev = nil
		}
		if err := db.Create(&f).Error; err != nil {
			t.Fatal(err)
		}
		sf := app.StackFrame{FrameID: f.ID, StackID: &s.stack.ID, Status: app.StackStatusAdded, Score: s.score, Weight: s.score * 300, Exposure: 300}
		if err := db.Create(&sf).Error; err != nil {
			t.Fatal(err)
		}
		ids[key] = sf.ID
	}
	return ids
}

func checkRescoredSubs(t *testing.T, get func(string) app.StackFrame, subs map[string]rescoreSub, retryAt time.Time) {
	t.Helper()
	if sf := get("hazy.xisf"); sf.Status != app.StackStatusLowScore || sf.Score != 0.25 ||
		sf.NextAttemptAt == nil || !sf.NextAttemptAt.Equal(retryAt) {
		t.Errorf("hazy sub = %s score %v next %v, want low_score 0.25 retried after RetryAfter", sf.Status, sf.Score, sf.NextAttemptAt)
	}
	if sf := get("clear.xisf"); sf.Status != app.StackStatusAdded || sf.Score != 0.95 || math.Abs(sf.Weight-0.95*300) > 1e-9 {
		t.Errorf("clear sub = %s score %v weight %v, want added 0.95, weight 285", sf.Status, sf.Score, sf.Weight)
	}
	if sf := get("drift.xisf"); sf.Status != app.StackStatusAdded || sf.Score != 0.2 || math.Abs(sf.Weight-0.2*300) > 1e-9 {
		t.Errorf("drifted sub = %s score %v weight %v, want kept added at 0.2, weight 60", sf.Status, sf.Score, sf.Weight)
	}
	for _, key := range []string{"lost.xisf", subTSReject, subR1, subB1} {
		if sf := get(key); sf.Status != app.StackStatusAdded || sf.Score != subs[key].score {
			t.Errorf("%s = %s score %v, want left added at %v", key, sf.Status, sf.Score, subs[key].score)
		}
	}
	if sf := get("g2.xisf"); sf.Score != 0.85 || math.Abs(sf.Weight-0.85*300) > 1e-9 {
		t.Errorf("g2 = score %v weight %v, want 0.85 and 255", sf.Score, sf.Weight)
	}
}

// A hazy sub's lower score keeps it out when it is classified. A reject in
// Target Scheduler is final, unless it is the stacker's own verdict: that
// sub is judged again like any other, so it can come back.
func TestClassifyHazySubAndRejects(t *testing.T) {
	t.Parallel()
	p := NewPipeline(nil, "", "", nil, nil, siril.Runner{}, "", DefaultPipelineOptions())
	f := app.Frame{Key: "Orion/LIGHT/sub.xisf", Object: objectOrion}
	for _, c := range []struct {
		name string
		s    quality.SubScore
		want string
	}{
		{"hazy", quality.SubScore{Score: 0.25, TargetBest: 1, Transparency: 0.57}, app.StackStatusLowScore},
		{"rejected by TS", quality.SubScore{Score: 0, TargetBest: 1, GradingStatus: quality.GradingRejected}, app.StackStatusRejected},
		{"stacker's reject, still low", quality.SubScore{Score: 0.2, TargetBest: 1, GradingStatus: quality.GradingRejected, StackerRejected: true}, app.StackStatusLowScore},
		// Past the scores, classify goes on to calibration, which this
		// frame lacks: anything but rejected or low_score means it is in.
		{"stacker's reject, good now", quality.SubScore{Score: 0.9, TargetBest: 1, GradingStatus: quality.GradingRejected, StackerRejected: true}, app.StackStatusNoMetadata},
	} {
		if _, status := p.classify(f, map[string]quality.SubScore{"sub.xisf": c.s}, nil, nil); status != c.want {
			t.Errorf("%s: status = %q, want %q", c.name, status, c.want)
		}
	}
}

// Lights recorded as rejected because Target Scheduler showed the stacker's
// own verdict are classified again; those TS or a person rejected stay.
func TestRequeueStackerRejects(t *testing.T) {
	t.Parallel()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&app.Frame{}, &app.StackFrame{}); err != nil {
		t.Fatal(err)
	}
	const verdict, graded, low = "verdict.xisf", "graded.xisf", "low.xisf"
	ids := map[string]int{}
	for _, key := range []string{verdict, graded, low} {
		f := app.Frame{Key: "Orion/LIGHT/" + key, Type: frameTypeLight, LastModified: time.Now()}
		if err := db.Create(&f).Error; err != nil {
			t.Fatal(err)
		}
		status := app.StackStatusRejected
		if key == low {
			status = app.StackStatusLowScore
		}
		sf := app.StackFrame{FrameID: f.ID, Status: status}
		if err := db.Create(&sf).Error; err != nil {
			t.Fatal(err)
		}
		ids[key] = sf.ID
	}
	scores := map[string]quality.SubScore{
		verdict: {GradingStatus: quality.GradingRejected, StackerRejected: true, Score: 0.2, TargetBest: 1},
		graded:  {GradingStatus: quality.GradingRejected},
		low:     {GradingStatus: quality.GradingRejected, StackerRejected: true, Score: 0.2, TargetBest: 1},
	}
	p := NewPipeline(nil, "", "", db, nil, siril.Runner{}, "", DefaultPipelineOptions())
	now := time.Now()
	if err := p.requeueRejects(context.Background(), scores, now); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		verdict: app.StackStatusLowScore, graded: app.StackStatusRejected, low: app.StackStatusLowScore,
	} {
		var sf app.StackFrame
		if err := db.First(&sf, ids[key]).Error; err != nil {
			t.Fatal(err)
		}
		if sf.Status != want {
			t.Errorf("%s = %s, want %s", key, sf.Status, want)
		}
		if key == verdict && (sf.NextAttemptAt == nil || sf.NextAttemptAt.After(now)) {
			t.Errorf("%s next attempt %v, want due now", key, sf.NextAttemptAt)
		}
	}
}

// With Photometry on, a light waits until the indexer has measured its
// starlight at the current revision.
func TestLightsWaitForPhotometry(t *testing.T) {
	t.Parallel()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&app.Frame{}, &app.StackFrame{}); err != nil {
		t.Fatal(err)
	}
	old, cur := measure.PhotometryRevision-1, measure.PhotometryRevision
	for key, rev := range map[string]*int{"none": nil, "old": &old, "measured": &cur} {
		if err := db.Create(&app.Frame{Key: key, Type: frameTypeLight, Object: objectOrion, Filter: filterRed, LastModified: time.Now(), PhotometryRev: rev}).Error; err != nil {
			t.Fatal(err)
		}
	}
	pending := func(photometry bool) []string {
		t.Helper()
		opts := DefaultPipelineOptions()
		opts.Photometry = photometry
		p := NewPipeline(nil, "", "", db, nil, siril.Runner{}, "", opts)
		var keys []string
		if err := p.pendingLights(db, time.Now()).Order("frames.key").Pluck("frames.key", &keys).Error; err != nil {
			t.Fatal(err)
		}
		return keys
	}
	if got := pending(true); !slices.Equal(got, []string{"measured"}) {
		t.Errorf("pending with photometry: %v, want only the measured light", got)
	}
	if got := pending(false); len(got) != 3 {
		t.Errorf("pending without photometry: %v, want all three", got)
	}
}

// A moonlit light the stacker rejected in Target Scheduler for the moon is
// classified again on its score first, then left out for the moon again, so
// its verdict isn't undone.
func TestStackerMoonRejectStaysOut(t *testing.T) {
	t.Parallel()
	p := NewPipeline(nil, "", "", nil, nil, siril.Runner{}, "", DefaultPipelineOptions())
	exp, night := 600.0, time.Now()
	// A light classify would stack: delivered calibrated, so it needs no
	// calibration frames.
	f := app.Frame{Key: "Orion/LIGHT/moonlit_cal.fits", Object: objectOrion, Filter: filterHa, Exposure: &exp, Night: &night}
	scores := map[string]quality.SubScore{"moonlit_cal.fits": {Score: 0.9, TargetBest: 1, GradingStatus: quality.GradingRejected, StackerRejected: true}}
	_, status := p.classify(f, scores, nil, nil)
	if status != "" {
		t.Fatalf("classify = %q, want it stacked on its score", status)
	}
	if got := withMoon(status, true, true); got != app.StackStatusMoon {
		t.Errorf("moonlit, master with moon-free lights: %q, want %q", got, app.StackStatusMoon)
	}
	if got := withMoon(status, true, false); got != "" {
		t.Errorf("moonlit, master of moonlit lights only: %q, want stacked", got)
	}
	if got := withMoon(app.StackStatusLowScore, true, true); got != app.StackStatusLowScore {
		t.Errorf("low score and moonlit: %q, want low_score", got)
	}
}
