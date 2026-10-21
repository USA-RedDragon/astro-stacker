package stacking

import (
	"context"
	"path"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/quality"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

// A light below the stacking cut in a dip watch's window goes to the batch
// to be registered for photometry only; one outside every window, one
// already registered, and one above the cut are handled as before. A
// registered one judged below the cut again keeps its registered copy, and
// one that did not register is left out with no copy.
func TestPhotometryBelowCut(t *testing.T) {
	t.Parallel()
	db := offTempDB(t)
	if err := db.AutoMigrate(&app.DiscoverySource{}, &app.DipWatch{}, &app.EclipseRequest{}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	frames := offTempFrames(t, db, []string{"R/moonlit_cal.fits", "R/outside_cal.fits", "R/done_cal.fits", "R/good_cal.fits", "R/far_cal.fits"},
		[]*float64{nil, nil, nil, nil, nil})
	win := time.Date(2026, 10, 15, 2, 0, 0, 0, time.UTC)
	at := []time.Time{win.Add(30 * time.Minute), win.Add(-5 * time.Hour), win.Add(time.Hour), win.Add(time.Hour), win.Add(30 * time.Minute)}
	for i := range frames {
		frames[i].DateObs = &at[i]
		if err := db.Model(&frames[i]).Update("date_obs", at[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	far := &frames[4]
	far.Object = "Elsewhere"
	if err := db.Model(far).Update("object", far.Object).Error; err != nil {
		t.Fatal(err)
	}
	src := app.DiscoverySource{Object: objectHartley, Kind: "dipper"}
	if err := db.Create(&src).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&app.DipWatch{SourceID: src.ID, ProjectGUID: "p", Start: win, End: win.Add(3 * time.Hour)}).Error; err != nil {
		t.Fatal(err)
	}
	stack := app.Stack{Object: objectHartley, Filter: filterRed}
	if err := db.Create(&stack).Error; err != nil {
		t.Fatal(err)
	}
	doneKey := "registered/done.xisf"
	if err := db.Create(&app.StackFrame{FrameID: frames[2].ID, StackID: &stack.ID, Status: app.StackStatusLowScore,
		RegisteredKey: &doneKey, Weight: 18, Exposure: 60}).Error; err != nil {
		t.Fatal(err)
	}
	scores := map[string]quality.SubScore{}
	for _, f := range frames {
		scores[path.Base(f.Key)] = quality.SubScore{GradingStatus: quality.GradingAccepted, Score: 0.28, TargetBest: 1}
	}
	scores[path.Base(frames[3].Key)] = quality.SubScore{GradingStatus: quality.GradingAccepted, Score: 0.9, TargetBest: 1}
	p := &Pipeline{db: db, opts: DefaultPipelineOptions()}
	batch, err := p.triage(ctx, frames, scores, nil, nil, map[int]string{}, false)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, c := range batch {
		got[c.frame.Key] = c.photometryOnly
	}
	if len(got) != 2 || !got["R/moonlit_cal.fits"] {
		t.Fatalf("batch %v, want the moonlit light for photometry and the good one stacked", got)
	}
	if only, ok := got["R/good_cal.fits"]; !ok || only {
		t.Errorf("good light: in batch %v, photometry only %v", ok, only)
	}
	for _, f := range []app.Frame{frames[1], frames[4]} {
		var sf app.StackFrame
		if err := db.Where("frame_id = ?", f.ID).First(&sf).Error; err != nil {
			t.Fatal(err)
		}
		if sf.Status != app.StackStatusLowScore || sf.RegisteredKey != nil {
			t.Errorf("%s: status %q key %v", f.Key, sf.Status, sf.RegisteredKey)
		}
	}
	var done app.StackFrame
	if err := db.Where("frame_id = ?", frames[2].ID).First(&done).Error; err != nil {
		t.Fatal(err)
	}
	if done.Status != app.StackStatusLowScore || done.RegisteredKey == nil || *done.RegisteredKey != doneKey || done.Weight != 18 {
		t.Errorf("registered light judged again: status %q key %v weight %v", done.Status, done.RegisteredKey, done.Weight)
	}
	checkPhotometryRecords(ctx, t, p, &stack, batch, frames[1])
}

// checkPhotometryRecords records the batch's photometry light as
// registered, and frame as not registered.
func checkPhotometryRecords(ctx context.Context, t *testing.T, p *Pipeline, stack *app.Stack, batch []candidate, frame app.Frame) {
	t.Helper()
	db := p.db
	var moonlit candidate
	for _, c := range batch {
		if c.photometryOnly {
			moonlit = c
		}
	}
	moonlit.photometryOnly = true
	rest, err := p.recordPhotometry(ctx, stack, []addedSub{{c: calibrated{c: moonlit}, key: "registered/moonlit.xisf"}})
	if err != nil || len(rest) != 0 {
		t.Fatal(rest, err)
	}
	var sf app.StackFrame
	if err := db.Where("frame_id = ?", moonlit.frame.ID).First(&sf).Error; err != nil {
		t.Fatal(err)
	}
	if sf.Status != app.StackStatusLowScore || sf.RegisteredKey == nil || *sf.RegisteredKey != "registered/moonlit.xisf" ||
		sf.StackID == nil || *sf.StackID != stack.ID || !(sf.Weight > 0) || sf.NextAttemptAt == nil {
		t.Errorf("photometry light recorded %+v", sf)
	}

	unsolved := moonlit
	unsolved.frame = frame
	added, unregistered, err := p.storeRegistered(ctx, objectHartley, filterRed, []calibrated{{c: unsolved}}, []string{""})
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 0 || unregistered != 0 {
		t.Errorf("unregistered photometry light: added %d, counted %d", len(added), unregistered)
	}
	sf = app.StackFrame{}
	if err := db.Where("frame_id = ?", unsolved.frame.ID).First(&sf).Error; err != nil {
		t.Fatal(err)
	}
	if sf.Status != app.StackStatusLowScore || sf.RegisteredKey != nil {
		t.Errorf("unregistered photometry light: status %q key %v", sf.Status, sf.RegisteredKey)
	}
}
