package skybright_test

import (
	"context"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/moon"
	"github.com/USA-RedDragon/astro-stacker/internal/skybright"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func openDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&app.SkySample{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func add(t *testing.T, db *gorm.DB, id int, filter string, night, at time.Time, mag float64) {
	t.Helper()
	n, a := night, at
	if err := db.Create(&app.SkySample{FrameID: id, Filter: filter, Night: &n, DateObs: &a, SkyMag: mag, SkyRate: 1}).Error; err != nil {
		t.Fatal(err)
	}
}

func TestMeasureTakesTheMedianOfRecentDarkNights(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	now := time.Date(2026, 10, 9, 18, 0, 0, 0, time.UTC)
	id := 0
	for d := 1; d <= 12; d++ {
		night := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC).AddDate(0, 0, d)
		for k := range 6 {
			id++
			add(t, db, id, "Luminance", night, night.Add(30*time.Hour), 21.0+0.05*float64(d)+0.01*float64(k))
		}
	}
	thin := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	for k := range 3 {
		id++
		add(t, db, id, "L", thin, thin.Add(30*time.Hour), 18+float64(k))
	}
	id++
	add(t, db, id, "H-a", thin, thin.Add(30*time.Hour), 15)
	v, err := skybright.Measure(context.Background(), db, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if v.Mag == nil || v.Basis.Source != skybright.SourceMeasured || v.Basis.Nights != skybright.MaxNights || len(v.Basis.PerNight) != skybright.MaxNights {
		t.Fatalf("value %+v", v)
	}
	if v.Basis.Frames != 6*skybright.MaxNights || *v.Basis.To <= *v.Basis.From || v.Basis.Reason != nil {
		t.Errorf("basis %+v", v.Basis)
	}
	if *v.Mag < 21.1 || *v.Mag > 21.7 {
		t.Errorf("sky %v", *v.Mag)
	}
}

func TestMeasureSaysWhyItHasNoValue(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	now := time.Now()
	v, err := skybright.Measure(context.Background(), db, nil, now)
	if err != nil || v.Mag != nil || v.Basis.Source != skybright.SourceNone || v.Basis.Reason == nil || v.Basis.PerNight == nil {
		t.Fatalf("empty %+v %v", v, err)
	}
	night := now.Add(-48 * time.Hour)
	add(t, db, 1, "L", night, night.Add(6*time.Hour), 21)
	if v, _ = skybright.Measure(context.Background(), db, nil, now); v.Mag != nil || v.Basis.Reason == nil {
		t.Errorf("one sub made a night: %+v", v)
	}
	src := &skybright.Source{DB: db, Override: 20.5}
	if got := src.Get(context.Background()); got.Mag == nil || *got.Mag != 20.5 || got.Basis.Source != skybright.SourceConfig {
		t.Errorf("override %+v", got)
	}
}

func TestMeasureLeavesOutMoonlitSubs(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	const lat, lon = 31.5, -99.4
	up := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	for moon.At(up).Altitude(up, lat, lon) < 20 {
		up = up.Add(30 * time.Minute)
	}
	night := time.Date(up.Year(), up.Month(), up.Day(), 0, 0, 0, 0, time.UTC)
	for k := range 6 {
		add(t, db, k+1, "L", night, up.Add(time.Duration(k)*time.Minute), 19)
	}
	site := func(context.Context) (float64, float64, bool) { return lat, lon, true }
	v, err := skybright.Measure(context.Background(), db, site, up.Add(24*time.Hour))
	if err != nil || v.Mag != nil || v.Basis.Reason == nil {
		t.Fatalf("moonlit subs measured the dark sky: %+v %v", v, err)
	}
	unlit, _ := skybright.Measure(context.Background(), db, nil, up.Add(24*time.Hour))
	if unlit.Mag == nil || *unlit.Mag != 19 {
		t.Fatalf("no value without a site: %+v", unlit)
	}
}

func TestMeasureUsesRGBWithoutLuminance(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	now := time.Date(2026, 10, 9, 18, 0, 0, 0, time.UTC)
	id := 0
	for d := 1; d <= 3; d++ {
		night := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, d)
		for _, f := range []struct {
			name string
			mag  float64
		}{{"Red", 20.8}, {"Green", 21.1}, {"H-a", 15}} {
			for range 6 {
				id++
				add(t, db, id, f.name, night, night.Add(30*time.Hour), f.mag)
			}
		}
	}
	v, err := skybright.Measure(context.Background(), db, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if v.Mag == nil || *v.Mag != 21.1 || v.Basis.Filter != "G" || v.Basis.Band != skybright.Band || v.Basis.Nights != 3 {
		t.Fatalf("value %+v %+v", v.Mag, v.Basis)
	}
	if !skybright.Broadband("Blue") || skybright.Broadband("O-III") {
		t.Error("broadband filters")
	}
}
