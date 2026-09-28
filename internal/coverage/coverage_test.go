package coverage_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/USA-RedDragon/pixinsight-worker/internal/calmatch"
	"github.com/USA-RedDragon/pixinsight-worker/internal/coverage"
	"github.com/USA-RedDragon/pixinsight-worker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
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
	light := app.Frame{Type: "LIGHT", Object: "Andromeda", Filter: "Red", Exposure: f(600), Gain: f(0), Offset: f(50),
		SetTemp: f(-10), BinX: f(1), Rotator: f(142.38), Night: day("2025-11-15")}
	add(light, 3)
	light2 := light
	light2.Rotator = f(142.41) // same session, tiny rotator jitter
	add(light2, 2)
	add(app.Frame{Type: "FLAT", Filter: "Red", Exposure: f(1.2), Gain: f(0), Offset: f(50), SetTemp: f(-10), BinX: f(1),
		Rotator: f(142.4), Night: day("2025-11-15")}, 30)
	add(app.Frame{Type: "DARK", Exposure: f(600), Gain: f(0), Offset: f(50), SetTemp: f(0), BinX: f(1), Night: day("2025-02-06")}, 20)
	add(app.Frame{Type: "BIAS", Exposure: f(0.00003), Gain: f(0), Offset: f(50), SetTemp: f(0), BinX: f(1), Night: day("2025-02-05")}, 60)
	errMsg := "unreadable"
	add(app.Frame{Type: "LIGHT", Object: "Andromeda", Filter: "Red", IndexError: &errMsg, Night: day("2025-11-15")}, 1)

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
		{Night: "2025-11-15", Gain: f(0), Offset: f(50), SetTemp: f(-13), Lights: 10},
		{Night: "2025-11-16", Gain: f(0), Offset: f(50), SetTemp: f(-17), Lights: 5},
		{Night: "2025-11-16", Gain: f(100), Offset: f(50), SetTemp: f(4), Lights: 7},
		{Night: "2025-11-17", Gain: f(0), Offset: f(50), SetTemp: f(-4), Lights: 3},
		{Night: "2025-11-18", SetTemp: f(-13), Lights: 4}, // no gain recorded
	}
	have := []calmatch.Set{{Type: "DARK", Gain: 0, Offset: 50, SetTemp: -5}}
	gaps := coverage.DarkGaps(rows, have)
	if len(gaps) != 2 {
		t.Fatalf("got %+v", gaps)
	}
	if gaps[0].SetTemp != -15 || *gaps[0].Gain != 0 || gaps[0].Lights != 15 || gaps[0].Nights != 2 || gaps[0].Latest != "2025-11-16" {
		t.Errorf("first gap %+v", gaps[0])
	}
	if gaps[1].SetTemp != 5 || *gaps[1].Gain != 100 || gaps[1].Lights != 7 {
		t.Errorf("second gap %+v", gaps[1])
	}
}
