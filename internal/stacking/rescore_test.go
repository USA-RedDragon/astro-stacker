package stacking

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/quality"
	"github.com/USA-RedDragon/astro-stacker/internal/siril"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// Masters stacked under an older scoreMethod have their subs scored again:
// a hazy sub now under the cut leaves and its master is restacked; a master
// whose weights hardly move is only re-weighted; a master already scored
// this way is left alone.
func TestRescoreAdded(t *testing.T) {
	t.Parallel()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&app.Frame{}, &app.StackFrame{}, &app.Stack{}); err != nil {
		t.Fatal(err)
	}
	orion := app.Stack{Object: "Orion", Filter: "Luminance"}
	m31 := app.Stack{Object: "M31", Filter: "Green"}
	done := app.Stack{Object: "M42", Filter: "Red", ScoreMethod: scoreMethod}
	for _, s := range []*app.Stack{&orion, &m31, &done} {
		if err := db.Create(s).Error; err != nil {
			t.Fatal(err)
		}
	}
	// The masters' subs as stacked: 300 s, weight = score x exposure.
	subs := map[string]struct {
		stack *app.Stack
		score float64
	}{
		"clear.xisf":    {&orion, 1},
		"hazy.xisf":     {&orion, 0.77},
		"lost.xisf":     {&orion, 0.8}, // no scheduler record now
		"tsreject.xisf": {&orion, 0.9}, // rejected in Target Scheduler since
		"g1.xisf":       {&m31, 1},
		"g2.xisf":       {&m31, 0.9},
		"r1.xisf":       {&done, 0.9},
	}
	ids := map[string]int{}
	for key, s := range subs {
		f := app.Frame{Key: s.stack.Object + "/LIGHT/" + key, Type: "LIGHT", LastModified: time.Now()}
		if err := db.Create(&f).Error; err != nil {
			t.Fatal(err)
		}
		sf := app.StackFrame{FrameID: f.ID, StackID: &s.stack.ID, Status: app.StackStatusAdded, Score: s.score, Weight: s.score * 300, Exposure: 300}
		if err := db.Create(&sf).Error; err != nil {
			t.Fatal(err)
		}
		ids[key] = sf.ID
	}
	scores := map[string]quality.SubScore{
		// Orion's hazy sub: 0.77 x 0.57^2 = 0.25, under 0.3 of the best.
		"clear.xisf":    {Score: 0.95, TargetBest: 0.95, Transparency: 0.97},
		"hazy.xisf":     {Score: 0.25, TargetBest: 0.95, Transparency: 0.57},
		"tsreject.xisf": {Score: 0, TargetBest: 0.95, GradingStatus: quality.GradingRejected},
		// M31: clear, weights within rescoreWeightTolerance.
		"g1.xisf": {Score: 0.92, TargetBest: 0.92, Transparency: 0.96},
		"g2.xisf": {Score: 0.85, TargetBest: 0.92, Transparency: 0.97},
		// Would leave, but its master was scored this way already.
		"r1.xisf": {Score: 0.1, TargetBest: 1},
	}
	p := NewPipeline(nil, "", "", db, nil, siril.Runner{}, "", DefaultPipelineOptions)
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
	if sf := get("hazy.xisf"); sf.Status != app.StackStatusLowScore || sf.Score != 0.25 ||
		sf.NextAttemptAt == nil || !sf.NextAttemptAt.Equal(now.Add(p.opts.RetryAfter)) {
		t.Errorf("hazy sub = %s score %v next %v, want low_score 0.25 retried after RetryAfter", sf.Status, sf.Score, sf.NextAttemptAt)
	}
	if sf := get("clear.xisf"); sf.Status != app.StackStatusAdded || sf.Score != 0.95 || math.Abs(sf.Weight-0.95*300) > 1e-9 {
		t.Errorf("clear sub = %s score %v weight %v, want added 0.95, weight 285", sf.Status, sf.Score, sf.Weight)
	}
	for _, key := range []string{"lost.xisf", "tsreject.xisf", "r1.xisf"} {
		if sf := get(key); sf.Status != app.StackStatusAdded || sf.Score != subs[key].score {
			t.Errorf("%s = %s score %v, want left added at %v", key, sf.Status, sf.Score, subs[key].score)
		}
	}
	if sf := get("g2.xisf"); sf.Score != 0.85 || math.Abs(sf.Weight-0.85*300) > 1e-9 {
		t.Errorf("g2 = score %v weight %v, want 0.85 and 255", sf.Score, sf.Weight)
	}

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

	// A second pass finds nothing to do.
	if err := db.Model(&app.Stack{}).Where("id = ?", orion.ID).UpdateColumn("needs_rebuild", false).Error; err != nil {
		t.Fatal(err)
	}
	scores["clear.xisf"] = quality.SubScore{Score: 0.1, TargetBest: 1}
	if err := p.rescore(context.Background(), scores, now); err != nil {
		t.Fatal(err)
	}
	if sf := get("clear.xisf"); sf.Status != app.StackStatusAdded {
		t.Error("a master was scored again under the same method")
	}
}

// A hazy sub's lower score keeps it out when it is classified: the low_score
// path judges it by the same Score.
func TestClassifyHazySub(t *testing.T) {
	t.Parallel()
	p := NewPipeline(nil, "", "", nil, nil, siril.Runner{}, "", DefaultPipelineOptions)
	f := app.Frame{Key: "Orion/LIGHT/hazy.xisf", Object: "Orion"}
	scores := map[string]quality.SubScore{"hazy.xisf": {Score: 0.25, TargetBest: 1, Transparency: 0.57}}
	if _, status := p.classify(f, scores, nil, nil); status != app.StackStatusLowScore {
		t.Errorf("status = %q, want %q", status, app.StackStatusLowScore)
	}
}
