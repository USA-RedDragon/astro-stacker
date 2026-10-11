package stacking

import (
	"context"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/quality"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

const (
	fallbackCalKey   = "Caldwell 14/LIGHT/g_cal.xisf"
	fallbackObject   = "Caldwell 14"
	fallbackExposure = 600.0
	unknownObject    = "Unknown"
)

func fallbackFrame(age time.Duration, now time.Time) app.Frame {
	exp := fallbackExposure
	night := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	return app.Frame{ID: 1, Key: fallbackCalKey, Object: fallbackObject, Filter: filterGreen, Exposure: &exp, Night: &night,
		LastModified: now.Add(-age)}
}

func TestMetadataRetry(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 11, 6, 0, 0, 0, time.UTC)
	measured := now.Add(-5 * time.Minute)
	soon := now.Add(time.Minute)
	for _, tc := range []struct {
		name      string
		age       time.Duration
		scored    bool
		placeable bool
		measured  bool
		want      *time.Time
	}{
		{name: "before grace", age: 5 * time.Minute, placeable: true, want: &soon},
		{name: "before grace, header places nothing", age: 5 * time.Minute, want: &soon},
		{name: "after grace, waiting for the indexer to measure it", age: 40 * time.Minute, placeable: true, want: &soon},
		{name: "after grace, header places nothing", age: 40 * time.Minute},
		{name: "after grace, measured but not scorable", age: 40 * time.Minute, placeable: true, measured: true},
		{name: "never measured after MeasureWait", age: MetadataGrace + MeasureWait + time.Minute, placeable: true},
		{name: "scored, header lacks exposure, fresh", age: 5 * time.Minute, scored: true, want: &soon},
		{name: "scored, header lacks exposure, old", age: 40 * time.Minute, scored: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := fallbackFrame(tc.age, now)
			if tc.measured {
				f.MeasuredAt = &measured
			}
			got, why := metadataRetry(f, tc.scored, tc.placeable, now)
			if (got == nil) != (tc.want == nil) || (got != nil && !got.Equal(*tc.want)) {
				t.Errorf("next attempt %v, want %v (%s)", got, tc.want, why)
			}
			if why == "" {
				t.Error("no reason given")
			}
		})
	}
}

func TestHeaderPlaceable(t *testing.T) {
	t.Parallel()
	now := time.Now()
	positions := map[string][2]float64{fallbackObject: {300, 40}}
	zero := 0.0
	for _, tc := range []struct {
		name string
		edit func(*app.Frame)
		want bool
	}{
		{name: "complete header", edit: func(*app.Frame) {}, want: true},
		{name: "object names no target", edit: func(f *app.Frame) { f.Object = unknownObject }},
		{name: "no object", edit: func(f *app.Frame) { f.Object = "" }},
		{name: "no filter", edit: func(f *app.Frame) { f.Filter = "" }},
		{name: "no exposure", edit: func(f *app.Frame) { f.Exposure = nil }},
		{name: "zero exposure", edit: func(f *app.Frame) { f.Exposure = &zero }},
		{name: "no night", edit: func(f *app.Frame) { f.Night = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := fallbackFrame(time.Hour, now)
			tc.edit(&f)
			if got := headerPlaceable(f, positions); got != tc.want {
				t.Errorf("headerPlaceable = %v, want %v", got, tc.want)
			}
		})
	}
}

// Before MetadataGrace a light scored only from its pixels waits for its
// Target Scheduler record; after it, it is stacked from its header at once.
func TestClassifyMetadataFallback(t *testing.T) {
	t.Parallel()
	p := &Pipeline{opts: DefaultPipelineOptions()}
	now := time.Now()
	positions := map[string][2]float64{fallbackObject: {300, 40}}
	recorded := quality.SubScore{Score: 0.9, TargetBest: 0.9}
	pixels := quality.SubScore{Score: 0.9, TargetBest: 0.9, Measured: true}
	for _, tc := range []struct {
		name       string
		age        time.Duration
		object     string
		score      *quality.SubScore
		wantStatus string
		wantSource string
	}{
		{name: "record, fresh", age: time.Minute, score: &recorded, wantSource: app.MetadataSourceTS},
		{name: "pixels only, before grace", age: 5 * time.Minute, score: &pixels, wantStatus: app.StackStatusNoMetadata},
		{name: "pixels only, after grace", age: 40 * time.Minute, score: &pixels, wantSource: app.MetadataSourceHeader},
		{name: "pixels only, after grace, unknown target", age: 40 * time.Minute, object: unknownObject, score: &pixels,
			wantSource: app.MetadataSourceHeader},
		{name: "nothing, after grace", age: 40 * time.Minute, wantStatus: app.StackStatusNoMetadata},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := fallbackFrame(tc.age, now)
			if tc.object != "" {
				f.Object = tc.object
			}
			scores := map[string]quality.SubScore{}
			if tc.score != nil {
				scores["g_cal.xisf"] = *tc.score
			}
			c, status := p.classify(f, scores, nil, positions)
			if status != tc.wantStatus {
				t.Errorf("status %q, want %q", status, tc.wantStatus)
			}
			if c.source != tc.wantSource {
				t.Errorf("source %q, want %q", c.source, tc.wantSource)
			}
		})
	}
}

func fallbackDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&app.Frame{}, &app.StackFrame{}, &app.Stack{}); err != nil {
		t.Fatal(err)
	}
	return db
}

// The timing as recorded: a light with no record is looked at again in a
// minute before MetadataGrace, and after it while its header places it and
// the indexer has yet to measure it; one whose header places nothing waits
// RetryAfter, as before.
func TestNoMetadataRecordedTiming(t *testing.T) {
	t.Parallel()
	db := fallbackDB(t)
	p := &Pipeline{db: db, opts: DefaultPipelineOptions()}
	ctx := context.Background()
	positions := map[string][2]float64{fallbackObject: {300, 40}}
	now := time.Now()
	for i, tc := range []struct {
		name   string
		age    time.Duration
		object string
		within time.Duration
	}{
		{name: "before grace", age: 5 * time.Minute, within: 2 * time.Minute},
		{name: "after grace, header places it", age: 40 * time.Minute, within: 2 * time.Minute},
		{name: "after grace, header places nothing", age: 40 * time.Minute, object: unknownObject, within: p.opts.RetryAfter + time.Minute},
	} {
		f := fallbackFrame(tc.age, now)
		f.ID = i + 1
		if tc.object != "" {
			f.Object = tc.object
		}
		c, status := p.classify(f, map[string]quality.SubScore{}, nil, positions)
		if status != app.StackStatusNoMetadata {
			t.Fatalf("%s: status %q", tc.name, status)
		}
		if err := p.record(ctx, p.leftOut(c, status)); err != nil {
			t.Fatal(err)
		}
		var sf app.StackFrame
		if err := db.Where("frame_id = ?", f.ID).First(&sf).Error; err != nil {
			t.Fatal(err)
		}
		if sf.NextAttemptAt == nil || time.Until(*sf.NextAttemptAt) > tc.within {
			t.Errorf("%s: next attempt %v, want within %v", tc.name, sf.NextAttemptAt, tc.within)
		}
		if tc.within > time.Hour && time.Until(*sf.NextAttemptAt) < p.opts.RetryAfter-time.Minute {
			t.Errorf("%s: next attempt %v, want RetryAfter", tc.name, sf.NextAttemptAt)
		}
		if sf.Error == nil || *sf.Error == "" {
			t.Errorf("%s: no reason recorded", tc.name)
		}
		if sf.MetadataSource != nil {
			t.Errorf("%s: metadata source %q without a score", tc.name, *sf.MetadataSource)
		}
	}
}

// A light scored from its header has no acquired image to carry a verdict:
// it is counted apart from other lights without one, and once its record
// arrives it is judged like any other.
func TestPlanVerdictsHeaderSource(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 11, 6, 0, 0, 0, time.UTC)
	frames := []verdictFrame{
		{FrameID: 1, Key: "C14/LIGHT/late.xisf", Object: fallbackObject, Filter: filterGreen, Status: app.StackStatusLowScore,
			Source: app.MetadataSourceHeader},
		{FrameID: 2, Key: "C14/LIGHT/old.xisf", Object: fallbackObject, Filter: filterGreen, Status: app.StackStatusLowScore},
	}
	plan := planVerdicts(frames, nil, map[int]app.TSVerdict{}, now, 10)
	if plan.Skipped["no_ts_record"] != 1 || plan.Skipped["no_image"] != 1 || len(plan.Sends) != 0 {
		t.Errorf("without records: skipped %v, sends %d", plan.Skipped, len(plan.Sends))
	}
	images := []tsImage{{ID: 9, GUID: "g", PlanID: 3, Status: quality.GradingAccepted, Target: fallbackObject, File: "late.xisf", Grader: true}}
	plan = planVerdicts(frames[:1], images, map[int]app.TSVerdict{}, now, 10)
	if len(plan.Sends) != 1 || plan.Sends[0].Image.ID != 9 || plan.Sends[0].Verdict != app.TSVerdictReject {
		t.Errorf("record arrived: sends %+v, skipped %v", plan.Sends, plan.Skipped)
	}
}

func TestVerdictFramesCarrySource(t *testing.T) {
	t.Parallel()
	db := fallbackDB(t)
	if err := db.AutoMigrate(&app.TSVerdict{}); err != nil {
		t.Fatal(err)
	}
	p := &Pipeline{db: db}
	header := app.MetadataSourceHeader
	for i, src := range []*string{&header, nil} {
		f := app.Frame{Key: fallbackObject + "/LIGHT/" + string(rune('a'+i)) + ".xisf", ETag: "e", Type: frameTypeLight, Object: fallbackObject, Filter: filterGreen}
		if err := db.Create(&f).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&app.StackFrame{FrameID: f.ID, Status: app.StackStatusLowScore, MetadataSource: src}).Error; err != nil {
			t.Fatal(err)
		}
	}
	frames, err := p.verdictFrames(context.Background(), VerdictOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 2 || frames[0].Source != app.MetadataSourceHeader || frames[1].Source != "" {
		t.Errorf("frames = %+v", frames)
	}
}
