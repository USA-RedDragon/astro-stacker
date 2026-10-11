package stacking

import (
	"context"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/quality"
	"github.com/USA-RedDragon/astro-stacker/internal/siril"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestScorePerSecondScoresEveryMasterAgain(t *testing.T) {
	t.Parallel()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&app.Frame{}, &app.StackFrame{}, &app.Stack{}); err != nil {
		t.Fatal(err)
	}
	old := app.Stack{Object: objectOrion, Filter: filterRed, ScoreMethod: scoreMethod}
	if err := db.Create(&old).Error; err != nil {
		t.Fatal(err)
	}
	ids := addRescoreSubs(t, db, map[string]rescoreSub{"moon120.xisf": {&old, 1}, "dark300.xisf": {&old, 1}})
	scores := map[string]quality.SubScore{
		"moon120.xisf": {Score: 0.4, TargetBest: 1, Transparency: 1, PlainScore: 0.4, PlainTargetBest: 1},
		"dark300.xisf": {Score: 1, TargetBest: 1, Transparency: 1, PlainScore: 1, PlainTargetBest: 1},
	}
	opts := DefaultPipelineOptions()
	opts.ScorePerSecond = true
	p := NewPipeline(nil, "", "", db, nil, siril.Runner{}, "", opts)
	if !p.scorer.PerSecond {
		t.Fatal("the pipeline's scorer does not score per second")
	}
	if err := p.rescore(context.Background(), scores, time.Now()); err != nil {
		t.Fatal(err)
	}
	var got app.Stack
	if err := db.First(&got, old.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.ScoreMethod != scoreMethodPerSecond || !got.NeedsRebuild {
		t.Errorf("method %d, needs rebuild %v; want %d and restacked for the moved weight", got.ScoreMethod, got.NeedsRebuild, scoreMethodPerSecond)
	}
	var sf app.StackFrame
	if err := db.First(&sf, ids["moon120.xisf"]).Error; err != nil {
		t.Fatal(err)
	}
	if sf.Status != app.StackStatusAdded || sf.Score != 0.4 {
		t.Errorf("moonlit sub %s score %v; want kept in with the per-second score 0.4", sf.Status, sf.Score)
	}
	off := NewPipeline(nil, "", "", db, nil, siril.Runner{}, "", DefaultPipelineOptions())
	if err := off.rescore(context.Background(), scores, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&got, old.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.ScoreMethod != scoreMethod {
		t.Errorf("after turning it off: method %d, want %d", got.ScoreMethod, scoreMethod)
	}
}
