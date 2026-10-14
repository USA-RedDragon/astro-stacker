package stacking

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/calmatch"
	"github.com/USA-RedDragon/astro-stacker/internal/siril"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func day(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

func flatSet(n string, rot float64, uploaded string) calmatch.Set {
	up, err := time.Parse(time.RFC3339, uploaded)
	if err != nil {
		panic(err)
	}
	return calmatch.Set{Type: frameTypeFlat, Night: day(n), Filter: filterRed, Gain: 0, Offset: 50, BinX: 1, Rotator: rot,
		Count: 30, FirstUploaded: up, Uploaded: up.Add(time.Hour)}
}

func dustEpochs() calmatch.Epochs {
	return calmatch.Epochs{{Start: day("2026-04-16"), Fallback: calmatch.FallbackPrevious}, {Start: day("2026-05-28")}}
}

func lightGroup(n string, rot float64) calmatch.Group {
	return calmatch.Group{Night: day(n), Filter: filterRed, Exposure: 300, Gain: 0, Offset: 50, SetTemp: -10, BinX: 1,
		Rotator: rot, Epochs: dustEpochs()}
}

// The flats on 2026-09-28: 2025's, then the first 2026 flats, uploaded
// from 04:06 UTC on 2026-09-29.
func historyFlats() []calmatch.Set {
	return []calmatch.Set{
		flatSet("2025-02-05", 133, "2025-02-05T21:28:36Z"),
		flatSet("2025-05-13", 132, "2025-05-13T22:31:37Z"),
		flatSet("2026-09-28", 141, "2026-09-29T04:13:00Z"),
		flatSet("2026-09-28", 142, "2026-09-29T04:32:32Z"),
	}
}

// Lights of June 2026 stacked on 2026-09-28, before any 2026 flat was
// uploaded, had a 2025 flat: clean of the dust they carry. They were never
// calibrated again once the 2026 flats came, which left the dust in their
// masters. They are now due, as are lights from before the dust that got a
// 2026 flat, and lights already on the right flat are not.
func TestFlatRecalibrationAcrossDustEpochs(t *testing.T) {
	t.Parallel()
	sets := historyFlats()
	june := lightGroup("2026-06-10", 141)
	early := time.Date(2026, 9, 28, 15, 29, 0, 0, time.UTC)
	late := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)

	h := flatHistory{likely: inferFlat(june, sets, early)}
	if h.likely == nil || h.likely.Night.Year() != 2025 {
		t.Fatalf("inferred %+v, want a 2025 flat", h.likely)
	}
	now := calmatch.Choose(june, sets).Flat
	if why := flatRecalReason(june, h, now); why != recalFlatEpoch {
		t.Fatalf("june lights: got %q", why)
	}

	march := lightGroup("2026-03-20", 141.6)
	h = flatHistory{likely: inferFlat(march, sets, late)}
	if h.likely == nil || h.likely.Night != day("2026-09-28") {
		t.Fatalf("inferred %+v, want the nearest night, 2026-09-28", h.likely)
	}
	if why := flatRecalReason(march, h, calmatch.Choose(march, sets).Flat); why != recalFlatEpoch {
		t.Fatalf("march lights on a 2026 flat: got %q", why)
	}

	right := calmatch.Choose(june, sets).Flat
	if why := flatRecalReason(june, flatHistory{used: right.Set}, right); why != "" {
		t.Fatalf("lights on the flat that matches now: got %q", why)
	}
	if why := flatRecalReason(june, flatHistory{gone: true}, right); why != recalFlatChanged {
		t.Fatalf("master of a changed set: got %q", why)
	}
	if why := flatRecalReason(june, flatHistory{}, right); why != recalFlatUnknown {
		t.Fatalf("nothing known: got %q", why)
	}
}

func TestFlatRecalibrationForAngle(t *testing.T) {
	t.Parallel()
	g := lightGroup("2026-10-12", 142)
	at43 := flatSet("2026-10-12", 43, "2026-10-13T12:00:00Z")
	at142 := flatSet("2026-10-11", 142, "2026-10-12T12:00:00Z")
	now := calmatch.Choose(g, []calmatch.Set{at43, at142}).Flat
	if now.Set.Rotator != 142 {
		t.Fatalf("got %+v", now.Set)
	}
	if why := flatRecalReason(g, flatHistory{used: &at43}, now); why != recalFlatAngle {
		t.Fatalf("got %q", why)
	}
	at136 := flatSet("2026-10-12", 136, "2026-10-13T12:00:00Z")
	only := calmatch.Choose(g, []calmatch.Set{at43, at136}).Flat
	if why := flatRecalReason(g, flatHistory{used: &at43}, only); why != "" {
		t.Fatalf("no flat within tolerance now, got %q", why)
	}
}

// Before epochs, a flat set counted as soon as its first frame was
// uploaded: lights stacked while it uploaded got a master of part of it.
func TestInferFlatUsesSetsStartedBeforeTheLightWasStacked(t *testing.T) {
	t.Parallel()
	g := lightGroup("2026-08-01", 141)
	sets := historyFlats()
	if got := inferFlat(g, sets, time.Date(2026, 9, 29, 4, 20, 0, 0, time.UTC)); got == nil || got.Rotator != 141 {
		t.Fatalf("got %+v, want the 141 set then uploading", got)
	}
	if got := inferFlat(g, sets, time.Date(2026, 9, 29, 4, 0, 0, 0, time.UTC)); got == nil || got.Night.Year() != 2025 {
		t.Fatalf("got %+v, want a 2025 flat", got)
	}
	if got := inferFlat(g, nil, time.Now()); got != nil {
		t.Fatalf("got %+v from no sets", got)
	}
}

// Stacked lights are queued again by their flat from the database: June
// lights on an inferred 2025 flat and lights on a master of a set that has
// since grown, but not lights already on the flat that matches now or lights
// delivered calibrated.
func TestRecalibrateFlats(t *testing.T) {
	t.Parallel()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&app.Frame{}, &app.StackFrame{}, &app.Stack{}, &app.CalibrationMaster{}, &app.DustEpoch{}); err != nil {
		t.Fatal(err)
	}
	for _, e := range dustEpochs() {
		if err := db.Create(&app.DustEpoch{Start: e.Start, Fallback: e.Fallback}).Error; err != nil {
			t.Fatal(err)
		}
	}
	flats := func(n string, rot float64, at time.Time, count int) []app.Frame {
		nt := day(n)
		out := make([]app.Frame, 0, count)
		for i := range count {
			d := at.Add(time.Duration(i) * time.Minute)
			f := app.Frame{Key: fmt.Sprintf("flat-%s-%v-%d", n, rot, i), Type: frameTypeFlat, Object: "FlatWizard", Filter: filterRed,
				Exposure: fp(1), Gain: fp(0), Offset: fp(50), BinX: fp(1), Rotator: fp(rot), Night: &nt, DateObs: &d, LastModified: d}
			if err := db.Create(&f).Error; err != nil {
				t.Fatal(err)
			}
			out = append(out, f)
		}
		return out
	}
	flats("2025-05-13", 132, time.Date(2025, 5, 13, 22, 0, 0, 0, time.UTC), 5)
	sep := flats("2026-09-28", 141, time.Date(2026, 9, 29, 4, 13, 0, 0, time.UTC), 5)
	if err := db.Create(&app.Stack{ID: 3, Object: objectM31, Filter: filterRed}).Error; err != nil {
		t.Fatal(err)
	}
	june := day("2026-06-10")
	light := func(key string, sf app.StackFrame) int {
		f := app.Frame{Key: key, Type: frameTypeLight, Object: objectM31, Filter: filterRed, Exposure: fp(300), Gain: fp(0),
			Offset: fp(50), SetTemp: fp(-10), BinX: fp(1), Rotator: fp(141.2), Night: &june, LastModified: june}
		if err := db.Create(&f).Error; err != nil {
			t.Fatal(err)
		}
		stack := 3
		sf.FrameID, sf.StackID, sf.Status = f.ID, &stack, app.StackStatusAdded
		if err := db.Create(&sf).Error; err != nil {
			t.Fatal(err)
		}
		return sf.ID
	}
	s := func(v string) *string { return &v }
	before := light("a.xisf", app.StackFrame{ProcessedAt: time.Date(2026, 9, 28, 16, 0, 0, 0, time.UTC)})
	current := light("b.xisf", app.StackFrame{FlatMaster: s(setKey(frameTypeFlat, sep)), ProcessedAt: time.Now()})
	partial := light("c.xisf", app.StackFrame{FlatMaster: s(setKey(frameTypeFlat, sep[:3])), ProcessedAt: time.Now()})
	delivered := light("d_cal.fits", app.StackFrame{ProcessedAt: time.Date(2026, 9, 28, 16, 0, 0, 0, time.UTC)})

	p := NewPipeline(nil, "", "", db, nil, siril.Runner{}, "", DefaultPipelineOptions())
	if err := p.recalibrateFlats(context.Background()); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[int]string{before: app.StackStatusRecalibrate, current: app.StackStatusAdded,
		partial: app.StackStatusRecalibrate, delivered: app.StackStatusAdded} {
		var sf app.StackFrame
		db.First(&sf, id)
		if sf.Status != want {
			t.Errorf("stack_frame %d: %s, want %s", id, sf.Status, want)
		}
	}
	var st app.Stack
	db.First(&st, 3)
	if !st.NeedsRebuild {
		t.Error("master not marked for a rebuild")
	}
}
