package stacking

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/calmatch"
	"github.com/USA-RedDragon/astro-stacker/internal/coverage"
	"github.com/USA-RedDragon/astro-stacker/internal/quality"
	"github.com/USA-RedDragon/astro-stacker/internal/siril"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func fp(v float64) *float64 { return &v }

// A light matched to a set still uploading waits for it, and says so.
func TestLightsWaitForArrivingSets(t *testing.T) {
	t.Parallel()
	night := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	f := app.Frame{Key: "M31/LIGHT/a.xisf", Object: "M31", Filter: "Red", Exposure: fp(600), Gain: fp(0), Offset: fp(50),
		SetTemp: fp(-5), BinX: fp(1), Night: &night}
	scores := map[string]quality.SubScore{"a.xisf": {Score: 1, TargetBest: 1}}
	now := time.Now()
	set := func(typ string, uploaded time.Time) calmatch.Set {
		return calmatch.Set{Type: typ, Night: night, Filter: "Red", Exposure: 600, Gain: 0, Offset: 50, SetTemp: -5,
			BinX: 1, Rotator: math.NaN(), Count: 25, Uploaded: uploaded}
	}
	opts := DefaultPipelineOptions
	p := NewPipeline(nil, "", "", nil, nil, siril.Runner{}, "", opts)
	old := now.Add(-24 * time.Hour)
	sets := []calmatch.Set{set("BIAS", old), set("FLAT", old), set("DARK", now.Add(-time.Hour))}
	c, status := p.classify(f, scores, sets, nil)
	if status != app.StackStatusCalibration || c.waiting == nil || c.waiting.Type != "DARK" {
		t.Fatalf("with darks arriving: status %q, waiting %+v", status, c.waiting)
	}
	sets[2].Uploaded = now.Add(-4 * time.Hour)
	if c, status := p.classify(f, scores, sets, nil); status != "" || c.waiting != nil {
		t.Fatalf("with darks settled: status %q, waiting %+v", status, c.waiting)
	}
	// So does one whose flats are still coming.
	sets[1].Uploaded = now.Add(-time.Minute)
	if c, status := p.classify(f, scores, sets, nil); status != app.StackStatusCalibration || c.waiting.Type != "FLAT" {
		t.Fatalf("with flats arriving: status %q, waiting %+v", status, c.waiting)
	}
}

func TestRecalReason(t *testing.T) {
	t.Parallel()
	dark := func(temp float64, count int) *calmatch.Set {
		return &calmatch.Set{Type: "DARK", Exposure: 600, Gain: 0, Offset: 50, SetTemp: temp, BinX: 1, Count: count}
	}
	g := calmatch.Group{Exposure: 600, Gain: 0, Offset: 50, SetTemp: -8, BinX: 1, Rotator: math.NaN()}
	now := calmatch.Match{Set: dark(-5, 25)}
	for _, c := range []struct {
		name string
		h    darkHistory
		want string
	}{
		{"no dark then", darkHistory{noDark: true}, recalNoDark},
		{"part of the set", darkHistory{used: dark(-5, 8)}, recalGrown},
		{"the whole set", darkHistory{used: dark(-5, 25)}, ""},
		{"a bigger set elsewhere", darkHistory{used: dark(-5, 30)}, ""},
		{"not recorded, nothing before", darkHistory{}, ""},
		{"a dark 8 °C off, now 3", darkHistory{used: dark(0, 30)}, recalCloser},
		{"a dark 4 °C off, now 3", darkHistory{used: dark(-12, 30)}, ""},
		{"likely 8 °C off", darkHistory{likely: dark(0, 30)}, recalCloser},
		// Unrecorded lights are never taken back for a set that grew.
		{"likely the same setup", darkHistory{likely: dark(-5, 8)}, ""},
	} {
		if got := recalReason(g, c.h, now); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	if got := recalReason(g, darkHistory{noDark: true}, calmatch.Match{}); got != "" {
		t.Errorf("no dark now: %q", got)
	}
	// Without a setpoint on the lights no dark is closer than another.
	g.SetTemp = math.NaN()
	if got := recalReason(g, darkHistory{used: dark(0, 30)}, now); got != "" {
		t.Errorf("unknown setpoint: %q", got)
	}
}

// A light stacked before masters were recorded is taken to have the dark
// that best matched it among the sets complete when it was stacked.
func TestInferDark(t *testing.T) {
	t.Parallel()
	g := calmatch.Group{Exposure: 600, Gain: 0, Offset: 50, SetTemp: -5, BinX: 1, Rotator: math.NaN()}
	day := func(d int) time.Time { return time.Date(2026, 9, d, 0, 0, 0, 0, time.UTC) }
	sets := []calmatch.Set{
		{Type: "DARK", Exposure: 600, Gain: 0, Offset: 50, SetTemp: 0, BinX: 1, Uploaded: day(1)},
		{Type: "DARK", Exposure: 600, Gain: 0, Offset: 50, SetTemp: -5, BinX: 1, Uploaded: day(20)},
	}
	if s := inferDark(g, sets, day(19)); s == nil || s.SetTemp != 0 {
		t.Errorf("stacked while the -5 °C darks came: %+v", s)
	}
	if s := inferDark(g, sets, day(21)); s == nil || s.SetTemp != -5 {
		t.Errorf("stacked after: %+v", s)
	}
	if s := inferDark(g, sets, day(1)); s != nil {
		t.Errorf("stacked before any: %+v", s)
	}
}

// recalDB holds a -5 °C dark library of 25 frames uploaded settled long ago,
// with the master of its first 8, and lights stacked in master 7.
func recalDB(t *testing.T) (*gorm.DB, func(key string, sf app.StackFrame) int) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&app.Frame{}, &app.StackFrame{}, &app.Stack{}, &app.CalibrationMaster{}); err != nil {
		t.Fatal(err)
	}
	night := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	start := time.Date(2026, 9, 29, 4, 0, 0, 0, time.UTC)
	for i := range 25 {
		d := start.Add(time.Duration(i) * 10 * time.Minute)
		if err := db.Create(&app.Frame{Key: fmt.Sprintf("dark-%d", i), Type: "DARK", Exposure: fp(600), Gain: fp(0),
			Offset: fp(50), SetTemp: fp(-5), BinX: fp(1), Night: &night, DateObs: &d, LastModified: d}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&app.CalibrationMaster{SetKey: "partial", Type: "DARK", ObjectKey: "x", Frames: 8,
		Exposure: fp(600), Gain: fp(0), Offset: fp(50), SetTemp: fp(-5), BinX: fp(1)}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&app.CalibrationMaster{SetKey: "whole", Type: "DARK", ObjectKey: "y", Frames: 25,
		Exposure: fp(600), Gain: fp(0), Offset: fp(50), SetTemp: fp(-5), BinX: fp(1)}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&app.Stack{ID: 7, Object: "M31", Filter: "Red"}).Error; err != nil {
		t.Fatal(err)
	}
	light := func(key string, sf app.StackFrame) int {
		f := app.Frame{Key: key, Type: "LIGHT", Object: "M31", Filter: "Red", Exposure: fp(600), Gain: fp(0),
			Offset: fp(50), SetTemp: fp(-5), BinX: fp(1), Night: &night, LastModified: start}
		if err := db.Create(&f).Error; err != nil {
			t.Fatal(err)
		}
		sf.FrameID = f.ID
		if sf.Status == "" {
			sf.Status = app.StackStatusAdded
		}
		if sf.StackID == nil {
			stack := 7
			sf.StackID = &stack
		}
		if err := db.Create(&sf).Error; err != nil {
			t.Fatal(err)
		}
		return sf.ID
	}
	return db, light
}

func TestRecalibrateDarks(t *testing.T) {
	t.Parallel()
	db, light := recalDB(t)
	s := func(v string) *string { return &v }
	noDark := light("a.xisf", app.StackFrame{NoDark: true})
	partial := light("b.xisf", app.StackFrame{DarkMaster: s("partial")})
	whole := light("c.xisf", app.StackFrame{DarkMaster: s("whole")})
	unknown := light("d.xisf", app.StackFrame{})
	calibrated := light("e_cal.fits", app.StackFrame{})
	// A 0 °C library from 2025, and lights stacked before masters were
	// recorded: one while the -5 °C darks came, one after.
	old := time.Date(2025, 2, 6, 20, 0, 0, 0, time.UTC)
	oldNight := time.Date(2025, 2, 6, 0, 0, 0, 0, time.UTC)
	for i := range 20 {
		d := old.Add(time.Duration(i) * 10 * time.Minute)
		if err := db.Create(&app.Frame{Key: fmt.Sprintf("old-dark-%d", i), Type: "DARK", Exposure: fp(600), Gain: fp(0),
			Offset: fp(50), SetTemp: fp(0), BinX: fp(1), Night: &oldNight, DateObs: &d, LastModified: d}).Error; err != nil {
			t.Fatal(err)
		}
	}
	during := light("f.xisf", app.StackFrame{ProcessedAt: time.Date(2026, 9, 29, 6, 0, 0, 0, time.UTC)})
	after := light("g.xisf", app.StackFrame{ProcessedAt: time.Date(2026, 9, 30, 6, 0, 0, 0, time.UTC)})
	recorded := light("h.xisf", app.StackFrame{DarkMaster: s("old"), ProcessedAt: time.Date(2026, 9, 30, 6, 0, 0, 0, time.UTC)})
	if err := db.Create(&app.CalibrationMaster{SetKey: "old", Type: "DARK", ObjectKey: "z", Frames: 20,
		Exposure: fp(600), Gain: fp(0), Offset: fp(50), SetTemp: fp(0), BinX: fp(1)}).Error; err != nil {
		t.Fatal(err)
	}

	// Lights stacked before no_dark existed have it NULL.
	if err := db.Model(&app.StackFrame{}).Where("id = ?", unknown).Update("no_dark", gorm.Expr("NULL")).Error; err != nil {
		t.Fatal(err)
	}
	p := NewPipeline(nil, "", "", db, nil, siril.Runner{}, "", DefaultPipelineOptions)
	if err := p.recalibrateDarks(context.Background()); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[int]string{noDark: app.StackStatusRecalibrate, partial: app.StackStatusRecalibrate,
		whole: app.StackStatusAdded, unknown: app.StackStatusAdded, calibrated: app.StackStatusAdded,
		during: app.StackStatusRecalibrate, after: app.StackStatusAdded, recorded: app.StackStatusRecalibrate} {
		var sf app.StackFrame
		db.First(&sf, id)
		if sf.Status != want {
			t.Errorf("stack_frame %d: %s, want %s", id, sf.Status, want)
		}
	}
	var st app.Stack
	db.First(&st, 7)
	if !st.NeedsRebuild {
		t.Error("master not marked for a rebuild")
	}
}

// No more than RecalibrateLimit lights wait at once, the best reason first.
func TestRecalibrateDarksTrickles(t *testing.T) {
	t.Parallel()
	db, light := recalDB(t)
	s := func(v string) *string { return &v }
	light("waiting.xisf", app.StackFrame{Status: app.StackStatusRecalibrate})
	var partial []int
	for i := range 3 {
		partial = append(partial, light(fmt.Sprintf("p%d.xisf", i), app.StackFrame{DarkMaster: s("partial")}))
	}
	noDark := light("n.xisf", app.StackFrame{NoDark: true})

	opts := DefaultPipelineOptions
	opts.RecalibrateLimit = 3
	p := NewPipeline(nil, "", "", db, nil, siril.Runner{}, "", opts)
	count := func() int64 {
		var n int64
		db.Model(&app.StackFrame{}).Where("status = ?", app.StackStatusRecalibrate).Count(&n)
		return n
	}
	if err := p.recalibrateDarks(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 3 {
		t.Fatalf("%d waiting, want the limit of 3", n)
	}
	var sf app.StackFrame
	db.First(&sf, noDark)
	if sf.Status != app.StackStatusRecalibrate {
		t.Error("the light with no dark should go first")
	}
	// Nothing more until some are done.
	if err := p.recalibrateDarks(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 3 {
		t.Fatalf("%d waiting after a second sweep, want 3", n)
	}
	// Once they are stacked again, with the whole set, the rest follow.
	db.Model(&app.StackFrame{}).Where("status = ?", app.StackStatusRecalibrate).Update("status", app.StackStatusAdded)
	db.Model(&app.StackFrame{}).Where("id IN ?", []int{noDark, partial[0]}).
		Updates(map[string]any{"dark_master": "whole", "no_dark": false})
	if err := p.recalibrateDarks(context.Background()); err != nil {
		t.Fatal(err)
	}
	var ids []int
	db.Model(&app.StackFrame{}).Where("status = ?", app.StackStatusRecalibrate).Order("id").Pluck("id", &ids)
	if len(ids) != 2 || ids[0] != partial[1] || ids[1] != partial[2] {
		t.Errorf("waiting %v, want %v", ids, partial[1:])
	}
}

// A master isn't built from a set still arriving, and a master built before
// masters recorded their setup gets it when next used.
func TestMasterForSettleAndSetup(t *testing.T) {
	t.Parallel()
	db, _ := recalDB(t)
	dir := t.TempDir()
	p := NewPipeline(nil, "", "", db, nil, siril.Runner{}, dir, DefaultPipelineOptions)
	ctx := context.Background()
	sets, err := coverage.Sets(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	var dark calmatch.Set
	for _, s := range sets {
		if s.Type == "DARK" && s.Master == "" {
			dark = s
		}
	}
	frames, err := coverage.SetFrames(ctx, db, dark)
	if err != nil {
		t.Fatal(err)
	}
	key := setKey("DARK", frames)

	arriving := dark
	arriving.Uploaded = time.Now().Add(-time.Hour)
	if _, _, err := p.masterFor(ctx, arriving, sets); err == nil || !strings.Contains(err.Error(), "still arriving") {
		t.Fatalf("built from a set still arriving: %v", err)
	}

	if err := db.Create(&app.CalibrationMaster{SetKey: key, Type: "DARK", ObjectKey: "k", Frames: 25}).Error; err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(dir, "masters", key+".fit")
	if err := os.MkdirAll(filepath.Dir(local), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// Even arriving: this very set was built already.
	got, gotKey, err := p.masterFor(ctx, arriving, sets)
	if err != nil || got != local || gotKey != key {
		t.Fatalf("masterFor = %q, %q, %v", got, gotKey, err)
	}
	var cm app.CalibrationMaster
	db.Where("set_key = ?", key).First(&cm)
	if cm.SetTemp == nil || *cm.SetTemp != -5 || cm.Exposure == nil || *cm.Exposure != 600 {
		t.Errorf("setup not recorded: %+v", cm)
	}
}
