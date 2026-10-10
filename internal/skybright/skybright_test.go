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
	if err != nil || v.Mag != nil || v.Basis.Source != skybright.SourceNone || v.Basis.Reason == nil || v.Basis.PerNight == nil || v.Basis.Method != "" {
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
	if v.Mag == nil || *v.Mag != 21.1 || v.Basis.Filter != "G" || v.Basis.PerNight[0].Filter != "G" || v.Basis.Band != skybright.Band || v.Basis.Nights != 3 {
		t.Fatalf("value %+v %+v", v.Mag, v.Basis)
	}
	if !skybright.Broadband("Blue") || skybright.Broadband("O-III") {
		t.Error("broadband filters")
	}
}

func TestMeasurePrefersNewerNightsOverStaleLuminance(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	now := time.Date(2026, 10, 9, 18, 0, 0, 0, time.UTC)
	id := 0
	night := func(m time.Month, d int) time.Time { return time.Date(2026, m, d, 0, 0, 0, 0, time.UTC) }
	for d := 1; d <= skybright.MaxNights; d++ {
		n := night(1, d)
		for range 6 {
			id++
			add(t, db, id, "L", n, n.Add(30*time.Hour), 20)
		}
	}
	for d := 1; d <= 3; d++ {
		n := night(9, d)
		for _, f := range []string{"Red", "Blue"} {
			for range 6 {
				id++
				add(t, db, id, f, n, n.Add(30*time.Hour), 21)
			}
		}
	}
	mixed := night(9, 10)
	for _, f := range []string{"L", "G"} {
		for k := range 6 {
			if f == "L" && k >= skybright.MinNightFrames-1 {
				break
			}
			id++
			add(t, db, id, f, mixed, mixed.Add(30*time.Hour), 22)
		}
	}
	v, err := skybright.Measure(context.Background(), db, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if v.Basis.Nights != skybright.MaxNights || *v.Basis.To != "2026-09-10" || *v.Basis.From != "2026-01-05" {
		t.Fatalf("nights %+v", v.Basis)
	}
	if v.Basis.Filter != "L, G, R" {
		t.Errorf("filters %q", v.Basis.Filter)
	}
	want := map[string]string{"2026-09-10": "G", "2026-09-03": "R", "2026-01-10": "L"}
	for _, n := range v.Basis.PerNight {
		if f, ok := want[n.Night]; ok && n.Filter != f {
			t.Errorf("night %s used %s, want %s", n.Night, n.Filter, f)
		}
		if n.Frames != 6 {
			t.Errorf("night %s frames %d", n.Night, n.Frames)
		}
	}
}

func TestSourceMarksAStaleValue(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	at := time.Date(2026, 10, 9, 18, 0, 0, 0, time.UTC)
	n := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	for k := range 6 {
		add(t, db, k+1, "L", n, n.Add(30*time.Hour), 21)
	}
	src := &skybright.Source{DB: db, TTL: time.Minute, Now: func() time.Time { return at }}
	fresh := src.Get(context.Background())
	if fresh.Mag == nil || fresh.Basis.Stale || fresh.Basis.Error != nil {
		t.Fatalf("fresh %+v", fresh)
	}
	if err := db.Migrator().DropColumn(&app.SkySample{}, "Night"); err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Hour)
	stale := src.Get(context.Background())
	if stale.Mag == nil || *stale.Mag != 21 || !stale.Basis.Stale || stale.Basis.Error == nil || *stale.Basis.Error == "" {
		t.Fatalf("stale %+v", stale.Basis)
	}
}
