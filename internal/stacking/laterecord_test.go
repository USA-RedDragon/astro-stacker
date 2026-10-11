package stacking

import (
	"context"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/quality"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
)

type lateSub struct {
	stack  *app.Stack
	status string
	source string
	weight float64
}

// A Target Scheduler record arriving after its light was scored from its
// header is applied once: the light in a master takes the record's score,
// or leaves it, and its master is restacked when it changed enough; a
// light left out is classified again at once; lights scored from their
// record, or still without one, are left alone.
func TestReconcileLateRecords(t *testing.T) {
	t.Parallel()
	db := fallbackDB(t)
	p := &Pipeline{db: db, opts: DefaultPipelineOptions(), busy: map[string]bool{}, drain: make(chan struct{})}
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	moved := app.Stack{Object: fallbackObject, Filter: filterGreen}
	steady := app.Stack{Object: objectM31, Filter: filterRed}
	for _, s := range []*app.Stack{&moved, &steady} {
		if err := db.Create(s).Error; err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		file  string
		sub   lateSub
		score quality.SubScore
		ok    func(app.StackFrame) bool
	}{
		{"reweighted.xisf", lateSub{&moved, app.StackStatusAdded, app.MetadataSourceHeader, 0.5 * 600}, quality.SubScore{Score: 0.9, TargetBest: 0.9},
			func(sf app.StackFrame) bool {
				return sf.Status == app.StackStatusAdded && sf.Score == 0.9 && sf.Weight == 0.9*600 && source(sf) == app.MetadataSourceTS
			}},
		{"under.xisf", lateSub{&moved, app.StackStatusAdded, app.MetadataSourceHeader, 0.9 * 600}, quality.SubScore{Score: 0.1, TargetBest: 0.9},
			func(sf app.StackFrame) bool {
				return sf.Status == app.StackStatusLowScore && sf.NextAttemptAt != nil && sf.Error != nil
			}},
		{"tsreject.xisf", lateSub{&moved, app.StackStatusAdded, app.MetadataSourceHeader, 0.9 * 600},
			quality.SubScore{Score: 0, TargetBest: 0.9, GradingStatus: quality.GradingRejected},
			func(sf app.StackFrame) bool { return sf.Status == app.StackStatusRejected && sf.NextAttemptAt == nil }},
		{"left.xisf", lateSub{nil, app.StackStatusLowScore, app.MetadataSourceHeader, 0}, quality.SubScore{Score: 0.8, TargetBest: 0.9},
			func(sf app.StackFrame) bool {
				return sf.Status == app.StackStatusLowScore && sf.NextAttemptAt != nil && !sf.NextAttemptAt.After(now.Add(time.Second)) &&
					source(sf) == app.MetadataSourceTS
			}},
		{"norecord.xisf", lateSub{&steady, app.StackStatusAdded, app.MetadataSourceHeader, 0.9 * 600},
			quality.SubScore{Score: 0.2, TargetBest: 0.9, Measured: true},
			func(sf app.StackFrame) bool { return sf.Score == 0.9 && source(sf) == app.MetadataSourceHeader }},
		{"fromts.xisf", lateSub{&steady, app.StackStatusAdded, app.MetadataSourceTS, 0.9 * 600}, quality.SubScore{Score: 0.2, TargetBest: 0.9},
			func(sf app.StackFrame) bool { return sf.Score == 0.9 }},
		{"close.xisf", lateSub{&steady, app.StackStatusAdded, app.MetadataSourceHeader, 0.9 * 600}, quality.SubScore{Score: 0.85, TargetBest: 0.9},
			func(sf app.StackFrame) bool { return sf.Score == 0.85 && source(sf) == app.MetadataSourceTS }},
	}
	subs := map[string]lateSub{}
	scores := map[string]quality.SubScore{}
	for _, c := range cases {
		subs[c.file], scores[c.file] = c.sub, c.score
	}
	ids := addLateSubs(t, db, subs)
	if err := p.reconcileLate(ctx, scores, now); err != nil {
		t.Fatal(err)
	}
	get := func(id int) app.StackFrame {
		var sf app.StackFrame
		if err := db.First(&sf, id).Error; err != nil {
			t.Fatal(err)
		}
		return sf
	}
	for _, c := range cases {
		if sf := get(ids[c.file]); !c.ok(sf) {
			t.Errorf("%s = %+v", c.file, sf)
		}
	}
	checkRebuild(t, db, true, false)

	if err := db.Model(&app.Stack{}).Where("1 = 1").UpdateColumn("needs_rebuild", false).Error; err != nil {
		t.Fatal(err)
	}
	first := cases[0].file
	scores[first] = quality.SubScore{Score: 0.3, TargetBest: 0.9}
	if err := p.reconcileLate(ctx, scores, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if sf := get(ids[first]); sf.Score != 0.9 {
		t.Errorf("reconciled twice: %+v", sf)
	}
	checkRebuild(t, db, false, false)
}

func source(sf app.StackFrame) string {
	if sf.MetadataSource == nil {
		return ""
	}
	return *sf.MetadataSource
}

func checkRebuild(t *testing.T, db *gorm.DB, want ...bool) {
	t.Helper()
	var stacks []app.Stack
	if err := db.Order("id").Find(&stacks).Error; err != nil {
		t.Fatal(err)
	}
	for i, w := range want {
		if stacks[i].NeedsRebuild != w {
			t.Errorf("%s needs rebuild %v, want %v", stacks[i].Object, stacks[i].NeedsRebuild, w)
		}
	}
}

func addLateSubs(t *testing.T, db *gorm.DB, subs map[string]lateSub) map[string]int {
	t.Helper()
	ids := map[string]int{}
	exp := fallbackExposure
	for file, s := range subs {
		object := fallbackObject
		if s.stack != nil {
			object = s.stack.Object
		}
		f := app.Frame{Key: object + "/LIGHT/" + file, ETag: "e", Type: frameTypeLight, Object: object, Filter: filterGreen, Exposure: &exp}
		if err := db.Create(&f).Error; err != nil {
			t.Fatal(err)
		}
		src := s.source
		sf := app.StackFrame{FrameID: f.ID, Status: s.status, Score: s.weight / exp, Weight: s.weight, Exposure: exp, MetadataSource: &src}
		if s.stack != nil {
			sf.StackID = &s.stack.ID
		}
		if s.status == app.StackStatusLowScore {
			later := time.Now().Add(3 * time.Hour)
			sf.NextAttemptAt = &later
		}
		if err := db.Create(&sf).Error; err != nil {
			t.Fatal(err)
		}
		ids[file] = sf.ID
	}
	return ids
}
