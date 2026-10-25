package stacking

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/dustcheck"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

const rimLeftOut = "Left out"

func TestRimFactor(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		rim    dustcheck.Rim
		factor float64
		note   string
	}{
		{"dark night Sculptor Luminance 26404", dustcheck.Rim{Depth: -0.20, Z: 3.9}, 1, ""},
		{"noisy dark night M 15 H-a 26400", dustcheck.Rim{Depth: -0.74, Z: 2.3}, 1, ""},
		{"moonlit M 15 H-a 27862", dustcheck.Rim{X: 2626, Y: 2016, Depth: -0.54, Z: 7.4}, 0.61, "Weight lowered to 61%"},
		{"moonlit Sculptor Green 27882", dustcheck.Rim{X: 2626, Y: 2016, Depth: -0.78, Z: 5.4}, 0.29, "Weight lowered to 29%"},
		{"moonlit Triangulum Luminance 27891", dustcheck.Rim{X: 2626, Y: 2016, Depth: -1.02, Z: 20.4}, 0, rimLeftOut},
	}
	for _, c := range cases {
		f, note := rimFactor(c.rim)
		if f != c.factor || !strings.HasPrefix(note, c.note) {
			t.Errorf("%s: factor %v %q, want %v %q", c.name, f, note, c.factor, c.note)
		}
	}
}

func TestRimWeight(t *testing.T) {
	t.Parallel()
	if w, out, why := rimWeight(0.8, 300, app.RimStreak{}, false); w != 240 || out || why != "" {
		t.Errorf("no streak: %v %v %q", w, out, why)
	}
	if w, out, _ := rimWeight(0.8, 300, app.RimStreak{Factor: 0.5, Note: "n"}, true); w != 120 || out {
		t.Errorf("half: %v %v", w, out)
	}
	if _, out, why := rimWeight(0.8, 300, app.RimStreak{Factor: 0, Note: rimLeftOut}, true); !out || why != rimLeftOut {
		t.Errorf("left out: %v %q", out, why)
	}
}

func TestApplyRim(t *testing.T) {
	t.Parallel()
	db := fallbackDB(t)
	p := &Pipeline{db: db, opts: DefaultPipelineOptions(), busy: map[string]bool{}, drain: make(chan struct{})}
	stack := app.Stack{Object: "Sculptor Galaxy", Filter: "Green"}
	if err := db.Create(&stack).Error; err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	add := func(frame int) rimCandidate {
		sf := app.StackFrame{FrameID: frame, StackID: &stack.ID, Status: app.StackStatusAdded, Score: 0.8, Exposure: 300, Weight: 240}
		if err := db.Create(&sf).Error; err != nil {
			t.Fatal(err)
		}
		return rimCandidate{StackFrameID: sf.ID, Status: sf.Status, Score: sf.Score, Exposure: sf.Exposure, Weight: sf.Weight, StackID: stack.ID}
	}
	clean := add(1)
	f, note := rimFactor(dustcheck.Rim{Depth: -0.20, Z: 3.9})
	if restack, out, err := p.applyRim(ctx, clean, app.RimStreak{Factor: f, Note: note}); err != nil || restack || out {
		t.Fatalf("clean sub: restack %v out %v err %v", restack, out, err)
	}
	streaked := add(2)
	f, note = rimFactor(dustcheck.Rim{X: 2626, Y: 2016, Depth: -0.78, Z: 5.4})
	if restack, out, err := p.applyRim(ctx, streaked, app.RimStreak{Factor: f, Note: note}); err != nil || !restack || out {
		t.Fatalf("streaked sub: restack %v out %v err %v", restack, out, err)
	}
	gone := add(3)
	f, note = rimFactor(dustcheck.Rim{X: 2626, Y: 2016, Depth: -1.02, Z: 20.4})
	if restack, out, err := p.applyRim(ctx, gone, app.RimStreak{Factor: f, Note: note}); err != nil || !restack || !out {
		t.Fatalf("left out sub: restack %v out %v err %v", restack, out, err)
	}
	var rows []app.StackFrame
	if err := db.Order("frame_id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if rows[0].Weight != 240 || rows[0].Error != nil {
		t.Errorf("clean sub: weight %v error %v", rows[0].Weight, rows[0].Error)
	}
	if math.Abs(rows[1].Weight-240*0.29) > 1e-9 || rows[1].Error == nil || !strings.HasPrefix(*rows[1].Error, "Weight lowered to 29%") {
		t.Errorf("streaked sub: weight %v error %v", rows[1].Weight, rows[1].Error)
	}
	if rows[2].Status != app.StackStatusLowScore || rows[2].Error == nil || !strings.HasPrefix(*rows[2].Error, rimLeftOut) {
		t.Errorf("left out sub: status %s error %v", rows[2].Status, rows[2].Error)
	}
	var got app.Stack
	if err := db.First(&got, stack.ID).Error; err != nil || !got.NeedsRebuild {
		t.Errorf("master not marked for restacking: %v %v", got.NeedsRebuild, err)
	}
}

func TestRimCandidates(t *testing.T) {
	t.Parallel()
	db := fallbackDB(t)
	if err := db.AutoMigrate(&app.DustLightSub{}); err != nil {
		t.Fatal(err)
	}
	p := &Pipeline{db: db, opts: DefaultPipelineOptions(), busy: map[string]bool{}, drain: make(chan struct{})}
	mk, reg := "stacks/M 15/Red/master.fit", "registered/M 15/Red/a.xisf"
	stack := app.Stack{Object: "M 15", Filter: filterRed, MasterKey: &mk}
	if err := db.Create(&stack).Error; err != nil {
		t.Fatal(err)
	}
	night := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	for _, f := range []int{1, 2, 3} {
		status := app.StackStatusAdded
		if f == 3 {
			status = app.StackStatusLowScore
		}
		if err := db.Create(&app.StackFrame{FrameID: f, StackID: &stack.ID, Status: status, RegisteredKey: &reg, Score: 1, Exposure: 300, Weight: 300}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&app.DustLightSub{FrameID: f, Night: night, Key: "k", FlatMaster: "f"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&app.RimStreak{FrameID: 2, Night: night, Factor: 1, Version: RimVersion}).Error; err != nil {
		t.Fatal(err)
	}
	rows, err := p.rimCandidates(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].FrameID != 1 || rows[0].StackID != stack.ID || rows[0].MasterKey != mk || rows[0].RegisteredKey != reg || rows[0].Weight != 300 {
		t.Errorf("candidates %+v, want only frame 1 with its master and registered copy", rows)
	}
}

func TestConfirmRims(t *testing.T) {
	t.Parallel()
	db := fallbackDB(t)
	p := &Pipeline{db: db, opts: DefaultPipelineOptions(), busy: map[string]bool{}, drain: make(chan struct{})}
	stack := app.Stack{Object: "M 15", Filter: "H-a"}
	if err := db.Create(&stack).Error; err != nil {
		t.Fatal(err)
	}
	night := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	measured := []app.RimStreak{
		{FrameID: 27860, X: 2626, Y: 2016, Depth: -0.79, Z: 3.8},
		{FrameID: 27862, X: 2629, Y: 2013, Depth: -0.57, Z: 11.9},
		{FrameID: 27864, X: 2626, Y: 2016, Depth: -0.99, Z: 5.4},
		{FrameID: 27866, X: 4819, Y: 2488, Depth: -0.61, Z: 3.8},
		{FrameID: 27868, X: 2626, Y: 2016, Depth: -0.12, Z: 1.0},
	}
	for _, r := range measured {
		r.Night, r.Object, r.Filter, r.Factor, r.Version = night, stack.Object, stack.Filter, 1, RimVersion
		if err := db.Create(&r).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&app.StackFrame{FrameID: r.FrameID, StackID: &stack.ID, Status: app.StackStatusAdded, Score: 1, Exposure: 600, Weight: 600}).Error; err != nil {
			t.Fatal(err)
		}
	}
	lowered, left, marked, err := p.confirmRims(t.Context(), night, stack.Object)
	if err != nil {
		t.Fatal(err)
	}
	if lowered != 3 || left != 0 || !marked[stack.ID] {
		t.Errorf("lowered %d left %d marked %v, want the three streaks on the double shadow weighted down", lowered, left, marked)
	}
	var frames []app.StackFrame
	if err := db.Order("frame_id").Find(&frames).Error; err != nil {
		t.Fatal(err)
	}
	want := map[int]float64{27860: 600 * 0.28, 27862: 600 * 0.57, 27864: 600 * 0.01, 27866: 600, 27868: 600}
	for _, f := range frames {
		if math.Abs(f.Weight-want[f.FrameID]) > 1e-9 {
			t.Errorf("frame %d: weight %v, want %v", f.FrameID, f.Weight, want[f.FrameID])
		}
	}
	if lowered, left, _, err := p.confirmRims(t.Context(), night, stack.Object); err != nil || lowered != 0 || left != 0 {
		t.Errorf("weighing the night again changed %d and %d lights (%v), want none", lowered, left, err)
	}
}
