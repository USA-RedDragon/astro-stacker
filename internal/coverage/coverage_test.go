package coverage_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/calmatch"
	"github.com/USA-RedDragon/astro-stacker/internal/coverage"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

const (
	filterRed  = "Red"
	typeDark   = "DARK"
	gapNight   = "2025-11-16"
	laterNight = "2025-11-17"
	typeLight  = "LIGHT"
	objM31     = "M31"
)

func f(v float64) *float64 { return &v }

func day(s string) *time.Time {
	t, _ := time.Parse("2006-01-02", s)
	return &t
}

func TestReport(t *testing.T) {
	t.Parallel()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&app.Frame{}); err != nil {
		t.Fatal(err)
	}

	n := 0
	add := func(fr app.Frame, count int) {
		for range count {
			n++
			fr.ID = 0
			fr.Key = fmt.Sprintf("frame-%d", n)
			fr.ETag = "e"
			fr.LastModified = time.Now()
			if err := db.Create(&fr).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	light := app.Frame{Type: typeLight, Object: "Andromeda", Filter: filterRed, Exposure: f(600), Gain: f(0), Offset: f(50),
		SetTemp: f(-10), BinX: f(1), Rotator: f(142.38), Night: day("2025-11-15")}
	add(light, 3)
	light2 := light
	light2.Rotator = f(142.41) // same session, tiny rotator jitter
	add(light2, 2)
	add(app.Frame{Type: "FLAT", Filter: filterRed, Exposure: f(1.2), Gain: f(0), Offset: f(50), SetTemp: f(-10), BinX: f(1),
		Rotator: f(142.4), Night: day("2025-11-15")}, 30)
	add(app.Frame{Type: typeDark, Exposure: f(600), Gain: f(0), Offset: f(50), SetTemp: f(0), BinX: f(1), Night: day("2025-02-06")}, 20)
	add(app.Frame{Type: "BIAS", Exposure: f(0.00003), Gain: f(0), Offset: f(50), SetTemp: f(0), BinX: f(1), Night: day("2025-02-05")}, 60)
	errMsg := "unreadable"
	add(app.Frame{Type: typeLight, Object: "Andromeda", Filter: filterRed, IndexError: &errMsg, Night: day("2025-11-15")}, 1)

	rows, err := coverage.Report(context.Background(), db, "Andromeda")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("want one grouped row, got %d: %+v", len(rows), rows)
	}
	r := rows[0]
	if r.Lights != 5 || r.Night != "2025-11-15" {
		t.Errorf("row %+v", r)
	}
	if r.Flat.Quality != "exact" || r.Flat.Frames != 30 {
		t.Errorf("flat %+v", r.Flat)
	}
	// The only darks are at 0 °C, 10 °C away from the -10 °C lights: usable
	// once scaled.
	if r.Dark.Quality != "fallback" || !r.Dark.Scaled {
		t.Errorf("dark %+v", r.Dark)
	}
	if r.Bias.Quality != "fallback" || r.Bias.AgeDays != 283 {
		t.Errorf("bias %+v", r.Bias)
	}
}

func TestDarkGaps(t *testing.T) {
	t.Parallel()
	rows := []coverage.Row{
		{Night: "2025-11-15", Exposure: 300, Gain: f(0), Offset: f(50), SetTemp: f(-13), Lights: 10},
		{Night: gapNight, Exposure: 300, Gain: f(0), Offset: f(50), SetTemp: f(-17), Lights: 5},
		{Night: gapNight, Exposure: 600, Gain: f(0), Offset: f(50), SetTemp: f(-15), Lights: 4},
		{Night: gapNight, Exposure: 600, Gain: f(100), Offset: f(50), SetTemp: f(4), Lights: 7},
		{Night: laterNight, Exposure: 300, Gain: f(0), Offset: f(50), SetTemp: f(-4), Lights: 3},
		{Night: laterNight, Exposure: 600, Gain: f(0), Offset: f(50), SetTemp: f(-15), Lights: 2},
		{Night: "2025-11-18", Exposure: 300, SetTemp: f(-13), Lights: 4},
	}
	have := []calmatch.Set{
		{Type: typeDark, Exposure: 300, Gain: 0, Offset: 50, SetTemp: -5, Count: 20},
		{Type: typeDark, Exposure: 300, Gain: 0, Offset: 50, SetTemp: -15, Count: 20},
		{Type: typeDark, Exposure: 600, Gain: 0, Offset: 50, SetTemp: -15, Count: 2},
	}
	gaps := coverage.DarkGaps(rows, have)
	if len(gaps) != 2 {
		t.Fatalf("got %+v", gaps)
	}
	if gaps[0].SetTemp != 5 || *gaps[0].Gain != 100 || *gaps[0].Exposure != 600 || gaps[0].Lights != 7 ||
		len(gaps[0].OtherExposures) != 0 {
		t.Errorf("first gap %+v", gaps[0])
	}
	g := gaps[1]
	if g.SetTemp != -15 || *g.Gain != 0 || *g.Exposure != 600 || g.Lights != 6 || g.Nights != 2 ||
		g.Latest != laterNight || len(g.OtherExposures) != 1 || g.OtherExposures[0] != 300 {
		t.Errorf("second gap %+v", g)
	}
}

func TestSetFramesMatchesGrouping(t *testing.T) {
	t.Parallel()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&app.Frame{}); err != nil {
		t.Fatal(err)
	}
	n := 0
	add := func(fr app.Frame, count int) {
		for range count {
			n++
			fr.ID = 0
			fr.Key = fmt.Sprintf("frame-%d", n)
			fr.ETag = "e"
			fr.LastModified = time.Now()
			if err := db.Create(&fr).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	flat := app.Frame{Type: "FLAT", Object: objM31, Filter: filterRed, Exposure: f(1.2), Gain: f(0), Offset: f(50),
		SetTemp: f(-10), BinX: f(1), Rotator: f(142.38), Night: day("2025-11-15")}
	add(flat, 20)
	jitter := flat
	jitter.Rotator = f(142.44)
	add(jitter, 10)
	// Same everything but no gain recorded: a different set.
	noGain := flat
	noGain.Gain = nil
	add(noGain, 5)
	other := flat
	other.Filter = "Green"
	add(other, 7)

	// Darks, one every 10 minutes from dark(start): a library shot across
	// local noon, so over two nights, with some frames named for a filter,
	// is one set; the same setup a week later is another.
	dark := func(start time.Time, count int, night, filter string) {
		for i := range count {
			d := start.Add(time.Duration(i) * 10 * time.Minute)
			add(app.Frame{Type: typeDark, Filter: filter, Exposure: f(600), Gain: f(100), Offset: f(50), SetTemp: f(5),
				BinX: f(1), Night: day(night), DateObs: &d}, 1)
		}
	}
	noon := time.Date(2026, 9, 29, 18, 30, 0, 0, time.UTC)
	dark(noon.Add(-80*time.Minute), 8, "2026-09-28", "")
	dark(noon.Add(time.Minute), 12, "2026-09-29", "")
	dark(noon.Add(121*time.Minute), 5, "2026-09-29", "L")
	dark(noon.AddDate(0, 0, 7), 20, "2026-10-06", "")
	// The same session's bias: its own set, by type.
	b := noon.Add(5 * time.Hour)
	add(app.Frame{Type: "BIAS", Exposure: f(0.00003), Gain: f(100), Offset: f(50), SetTemp: f(5), BinX: f(1),
		Night: day("2026-09-29"), DateObs: &b}, 40)

	sets, err := coverage.Sets(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[int]int{}
	built := sets[:0]
	for _, s := range sets {
		if s.Master != "" {
			continue // made elsewhere, no frames (coverage.Imported)
		}
		built = append(built, s)
	}
	sets = built
	for _, s := range sets {
		frames, err := coverage.SetFrames(context.Background(), db, s)
		if err != nil {
			t.Fatal(err)
		}
		if len(frames) != s.Count {
			t.Errorf("%s %s gain %v: %d frames, set says %d", s.Type, s.Filter, s.Gain, len(frames), s.Count)
		}
		counts[s.Count]++
	}
	if len(sets) != 6 || counts[30] != 1 || counts[5] != 1 || counts[7] != 1 || counts[25] != 1 ||
		counts[20] != 1 || counts[40] != 1 {
		t.Errorf("sets %+v", sets)
	}
	for _, s := range sets {
		if s.Type == typeDark && s.Count == 25 {
			if got := s.Night.Format("2006-01-02"); got != "2026-09-29" {
				t.Errorf("a library's night is its newest frame's, got %s", got)
			}
			if s.From != noon.Add(-80*time.Minute) || s.To != noon.Add(161*time.Minute) {
				t.Errorf("library spans %v to %v", s.From, s.To)
			}
		}
	}
}
