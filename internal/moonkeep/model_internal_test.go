package moonkeep

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/moon"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestBetaQuantile(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ p, a, b, want float64 }{
		{0.5, 1, 1, 0.5},
		{0.95, 1, 1, 0.95},
		{0.5, 2, 5, 0.26445},
		{0.95, 1, 20, 0.13911},
	} {
		if got := BetaQuantile(c.p, c.a, c.b); math.Abs(got-c.want) > 1e-4 {
			t.Errorf("BetaQuantile(%g, %g, %g) = %g, want %g", c.p, c.a, c.b, got, c.want)
		}
	}
	if !math.IsNaN(BetaQuantile(0.5, 0, 1)) {
		t.Error("a = 0 must be NaN")
	}
}

func TestVerdict(t *testing.T) {
	t.Parallel()
	for status, want := range map[string][2]bool{
		app.StackStatusAdded: {true, true}, app.StackStatusNight: {true, true}, app.StackStatusRecalibrate: {true, true},
		app.StackStatusMoon: {true, true}, app.StackStatusLowScore: {false, true}, app.StackStatusRejected: {false, true},
		app.StackStatusOffTarget: {false, false}, app.StackStatusDead: {false, false}, app.StackStatusUnmeasured: {false, false},
		app.StackStatusCalibration: {false, false}, "": {false, false},
	} {
		if k, ok := Verdict(status); k != want[0] || ok != want[1] {
			t.Errorf("Verdict(%q) = %v %v, want %v %v", status, k, ok, want[0], want[1])
		}
	}
}

func TestCellOf(t *testing.T) {
	t.Parallel()
	if c := CellOf(State{Illumination: 0.95, Altitude: -3, Separation: 10}); !c.Down {
		t.Error("a Moon below the horizon is the down cell whatever its distance")
	}
	if c := CellOf(State{Illumination: 0.95, Altitude: 40, Separation: 19.9}); c.Valid() {
		t.Error("inside the moon floor must not be a valid cell")
	}
	c := CellOf(State{Illumination: 1, Altitude: 90, Separation: 180})
	if !c.Valid() || c.Illum != 4 || c.Alt != 2 || c.Sep != 3 {
		t.Errorf("edges belong to the last bin: %+v", c)
	}
	if c := CellOf(State{Illumination: 0.9, Altitude: 15, Separation: 45}); c.Illum != 4 || c.Alt != 1 || c.Sep != 1 {
		t.Errorf("a lower edge opens its bin: %+v", c)
	}
}

func night(i int) string {
	return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i).Format(time.DateOnly)
}

func sample(n int, filter string, nb bool, s State, kept bool) Sample {
	return Sample{Night: night(n), Object: objM31, Filter: filter, Narrowband: nb, Kept: kept, Exposure: 300,
		Start: time.Date(2026, 1, 1, 6, 0, 0, 0, time.UTC).AddDate(0, 0, n), Moon: s}
}

const (
	objM31 = "M31"
	red    = "Red"
	hAlpha = "H-a"
)

func dark() State   { return State{Illumination: 0.95, Altitude: -10, Separation: 90} }
func bright() State { return State{Illumination: 0.97, Altitude: 50, Separation: 60} }
func dim() State    { return State{Illumination: 0.3, Altitude: 20, Separation: 90} }

func history() []Sample {
	out := make([]Sample, 0, 1000)
	for n := range 20 {
		for i := range 10 {
			out = append(out, sample(n, red, false, dark(), i < 8))
			out = append(out, sample(n, hAlpha, true, dark(), i < 8))
			out = append(out, sample(n, red, false, bright(), false))
			out = append(out, sample(n, hAlpha, true, bright(), i < 4))
			out = append(out, sample(n, red, false, dim(), i < 7))
		}
	}
	return out
}

func TestFitSkipsOnlyNearWorthlessRows(t *testing.T) {
	t.Parallel()
	m := Fit(history())
	if m.Subs != 1000 || m.Kept != 540 || m.Nights != 20 {
		t.Fatalf("totals %d %d %d", m.Subs, m.Kept, m.Nights)
	}
	r := m.Lookup(red, false, bright())
	if r == nil || r.Filter != red || !r.Skip || r.Subs != 200 || r.Kept != 0 || !(r.High < SkipBelow) {
		t.Fatalf("Red under a full Moon: %+v", r)
	}
	if !m.Skips("red ", false, bright(), nil) {
		t.Error("filter names match case-insensitively")
	}
	if q := 0.4; m.Skips(red, false, bright(), &q) {
		t.Error("a good measured night quality must not skip")
	}
	if q := 0.1; !m.Skips(red, false, bright(), &q) {
		t.Error("a poor measured night quality still skips")
	}
	if h := m.Lookup(hAlpha, true, bright()); h == nil || h.Skip || math.Abs(h.Worth-0.4/0.8) > 0.05 {
		t.Errorf("H-a under a full Moon keeps 40%% of the 80%% it keeps in the dark: %+v", h)
	}
	if d := m.Lookup(red, false, dark()); d == nil || d.Skip || d.Worth != 1 || !d.Down {
		t.Errorf("Moon down: %+v", d)
	}
	if r := m.Lookup("Luminance", false, bright()); r == nil || r.Filter != "" || !r.Skip {
		t.Errorf("a filter without its own rows uses its class row: %+v", r)
	}
	if m.Lookup(red, false, State{Illumination: 0.97, Altitude: 50, Separation: 10}) != nil {
		t.Error("inside the moon floor there is no row")
	}
	if r := m.Lookup(red, false, State{Illumination: 0.6, Altitude: 50, Separation: 60}); r != nil {
		t.Errorf("a bin with no subs is not usable: %+v", r)
	}
}

func TestFewNightsAreNotUsable(t *testing.T) {
	t.Parallel()
	s := make([]Sample, 0, 200)
	for i := range 100 {
		s = append(s, sample(i%2, red, false, bright(), false), sample(i%2, red, false, dark(), true))
	}
	m := Fit(s)
	if r := m.Lookup(red, false, bright()); r != nil {
		t.Fatalf("two nights are not enough: %+v", r)
	}
	if m.Skips(red, false, bright(), nil) {
		t.Fatal("no usable row must not skip")
	}
}

func TestClusteredNightsWidenTheBound(t *testing.T) {
	t.Parallel()
	spread, lumped := make([]Sample, 0, 200), make([]Sample, 0, 200)
	for n := range 20 {
		for i := range 10 {
			spread = append(spread, sample(n, red, false, bright(), (n*10+i)%10 == 0))
			lumped = append(lumped, sample(n, red, false, bright(), n < 2))
		}
	}
	a, b := Fit(spread).Lookup(red, false, bright()), Fit(lumped).Lookup(red, false, bright())
	if a == nil || b == nil || a.Kept != b.Kept || !(b.High > a.High) {
		t.Fatalf("kept subs bunched on two nights must give a wider bound: %+v %+v", a, b)
	}
}

func TestRunLeavesEachNightOut(t *testing.T) {
	t.Parallel()
	h := history()
	rp := Run(h, map[string][]Filter{objM31: {{red, false}, {hAlpha, true}}})
	if rp.Nights != 20 || rp.MoonUpNights != 20 || rp.Subs != 1000 || rp.MoonUpSubs != 600 {
		t.Fatalf("%+v", rp)
	}
	if rp.SkippedSubs != 200 || rp.KeptLost != 0 || rp.SkippedNights != 20 || math.Abs(rp.SkippedHours-200*300/3600.0) > 0.1 {
		t.Fatalf("only the full-Moon Red subs are skipped: %+v", rp)
	}
	if rp.IdleSubs != 0 {
		t.Errorf("H-a is still worth taking, so M31 is never idle: %+v", rp)
	}
	alone := Run(h, map[string][]Filter{objM31: {{red, false}}})
	if alone.IdleSubs != 200 {
		t.Errorf("a broadband-only target is idle when Red skips: %+v", alone)
	}
	h[len(h)-3].Kept = true
	if lost := Run(h, nil); lost.KeptLost != 1 {
		t.Errorf("a kept sub on a skipped night counts as lost: %+v", lost)
	}
}

func TestNight(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 1, 3, 6, 0, 0, 0, time.UTC)
	lat, lon := 31.5, -99.4
	g := moon.At(at).Topocentric(at, lat, lon)
	site := sky.Site{Latitude: lat, Longitude: lon}
	var near, far Candidate
	for ra := 0.0; ra < 360; ra += 5 {
		sep := g.Separation(ra, g.Dec)
		if site.Altitude(ra, g.Dec, at) < 10 {
			continue
		}
		if sep > 45 && sep < 75 && near.Object == "" {
			near = Candidate{Object: "near", RA: ra, Dec: g.Dec, Filters: []Filter{{red, false}, {hAlpha, true}}}
		}
		if sep >= 75 && sep < 110 && far.Object == "" {
			far = Candidate{Object: "far", RA: ra, Dec: g.Dec, Filters: []Filter{{red, false}}}
		}
	}
	if near.Object == "" || far.Object == "" {
		t.Fatal("no test positions")
	}
	st := At(at, near.RA, near.Dec, lat, lon)
	s := make([]Sample, 0, 300)
	for n := range 10 {
		for range 10 {
			s = append(s, sample(n, red, false, st, false), sample(n, hAlpha, true, st, n < 5))
		}
	}
	m := Fit(s)
	slots := Night(m, []Candidate{near}, lat, lon, at.Add(-15*time.Minute), at.Add(15*time.Minute))
	if len(slots) != 3 {
		t.Fatalf("slots %d", len(slots))
	}
	mid := slots[1]
	if mid.Altitude <= 0 || mid.SunAltitude > -12 || mid.Broadband == nil || !mid.Broadband.Skip || mid.Broadband.Targets != 1 || mid.Broadband.RateHigh <= 0 {
		t.Fatalf("Red near the full Moon skips: %+v %+v", mid, mid.Broadband)
	}
	if mid.Narrowband == nil || mid.Narrowband.Skip || mid.Narrowband.Plans != 1 || mid.Narrowband.Skipped != 0 {
		t.Fatalf("H-a keeps half: %+v", mid.Narrowband)
	}
	both := Night(m, []Candidate{near, far}, lat, lon, at, at)[0]
	if both.Broadband.Skip || both.Broadband.Plans != 2 || both.Broadband.Unknown != 1 {
		t.Fatalf("a target in a bin with no subs keeps the glare rule, so broadband is not all skipped: %+v", both.Broadband)
	}
	if Night(m, nil, 0, 0, at, at.Add(40*time.Hour)) != nil || Night(nil, nil, 0, 0, at, at.Add(time.Hour)) != nil {
		t.Error("a span over 36 hours or no model gives no slots")
	}
}

func TestSamples(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 1, 3, 5, 0, 0, 0, time.UTC)
	score := func(v float64) *float64 { return &v }
	in := make([]Input, 0, 6)
	for i := range 4 {
		in = append(in, Input{Object: objM31, Filter: "Ha", Exposure: 600, Start: start.Add(time.Duration(i) * 10 * time.Minute),
			Status: app.StackStatusLowScore, Score: score(0.1 + 0.1*float64(i)), RA: 10, Dec: 41, HasPos: true})
	}
	in = append(in, Input{Object: objM31, Filter: "Ha", Exposure: 600, Start: start, Status: app.StackStatusOffTarget, RA: 10, Dec: 41, HasPos: true},
		Input{Object: "X", Filter: red, Exposure: 300, Start: start, Status: app.StackStatusAdded})
	s := Samples(in, 31.5, -99.4)
	if len(s) != 4 {
		t.Fatalf("lights without a verdict or a position are left out: %d", len(s))
	}
	if s[0].Filter != hAlpha || !s[0].Narrowband || s[0].Night != "2026-01-02" {
		t.Errorf("%+v", s[0])
	}
	if s[2].Quality != nil {
		t.Errorf("two earlier subs are too few for a night quality: %v", *s[2].Quality)
	}
	if s[3].Quality == nil || math.Abs(*s[3].Quality-0.2) > 1e-9 {
		t.Errorf("the median of the three earlier scores: %v", s[3].Quality)
	}
	want := At(start.Add(5*time.Minute), 10, 41, 31.5, -99.4)
	if s[0].Moon != want {
		t.Errorf("the Moon is placed at mid-exposure: %+v want %+v", s[0].Moon, want)
	}
}

func openDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	return db
}

func fixture(t *testing.T, table bool) *Service {
	t.Helper()
	sched, appDB := openDB(t), openDB(t)
	for _, q := range []string{
		`CREATE TABLE project ("Id" integer, name text, state integer, guid text, "isMosaic" integer, minimumaltitude real)`,
		`CREATE TABLE target ("Id" integer, projectid integer, active integer, ra real, dec real, name text, guid text, rotation real)`,
		`CREATE TABLE exposuretemplate ("Id" integer, filtername text, gain integer, defaultexposure real)`,
		`CREATE TABLE exposureplan ("Id" integer, "exposureTemplateId" integer, targetid integer, enabled integer, exposure real)`,
		`INSERT INTO project VALUES (1, 'Andromeda', 1, 'p1', 0, 0)`,
		`INSERT INTO target VALUES (1, 1, 1, 0.712, 41.27, 'M31', 'g-m31', 0)`,
		`INSERT INTO exposuretemplate VALUES (1, 'Red', 0, 300)`,
		`INSERT INTO exposureplan VALUES (1, 1, 1, 1, -1)`,
	} {
		if err := sched.Exec(q).Error; err != nil {
			t.Fatal(err)
		}
	}
	if table {
		if err := sched.Exec(Schema).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := appDB.AutoMigrate(&app.Frame{}, &app.StackFrame{}, &app.FrameTarget{}); err != nil {
		t.Fatal(err)
	}
	f := func(v float64) *float64 { return &v }
	id := 0
	for n := range 5 {
		for i := range 8 {
			at := time.Date(2026, 1, 1, 4, 0, 0, 0, time.UTC).AddDate(0, 0, n).Add(time.Duration(i) * 6 * time.Minute)
			id++
			if err := appDB.Create(&app.Frame{ID: id, Key: fmt.Sprintf("M31/LIGHT/r%d.xisf", id), Type: "LIGHT", Object: objM31, Filter: red,
				Exposure: f(300), DateObs: &at, IndexedAt: at}).Error; err != nil {
				t.Fatal(err)
			}
			status := app.StackStatusAdded
			if i%2 == 1 {
				status = app.StackStatusLowScore
			}
			if err := appDB.Create(&app.StackFrame{FrameID: id, Status: status, Score: 0.5}).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	now := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	return &Service{App: appDB, Sched: sched, Mode: ModeOn, Now: func() time.Time { return now },
		Site: func(context.Context) (sky.Site, error) { return sky.Site{Latitude: 31.5, Longitude: -99.4}, nil }}
}

func TestServicePublishes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := fixture(t, true)
	res, err := s.Refresh(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Subs != 40 || res.Kept != 20 || res.Replay == nil || res.Publish.Changed == 0 || res.Publish.Error != "" {
		t.Fatalf("%+v %+v", res, res.Publish)
	}
	var n int64
	s.Sched.Table(Table).Count(&n)
	if int(n) != res.Publish.Rows || n == 0 {
		t.Fatalf("rows %d, status %+v", n, res.Publish)
	}
	var reds []TableRow
	if err := s.Sched.Table(Table).Where("filter = ?", red).Find(&reds).Error; err != nil {
		t.Fatal(err)
	}
	subs, kept := 0, 0
	for _, r := range reds {
		subs, kept = subs+r.Subs, kept+r.Kept
	}
	if subs != 40 || kept != 20 {
		t.Errorf("Red rows hold every sub once: %d %d", subs, kept)
	}
	again, err := s.Refresh(ctx)
	if err != nil || again.Publish.Skipped != SkipUnchanged {
		t.Fatalf("a second fit of the same lights changes nothing: %+v %v", again.Publish, err)
	}
	if err := s.Sched.Exec(`INSERT INTO ts_moon_keep VALUES ('Old', 0, 1, -1, -1, -1, 0, 1, -90, 0, 0, 180, 1, 1, 1, 1, 1, 1, 1, 0, 0, '2026-01-01')`).Error; err != nil {
		t.Fatal(err)
	}
	third, _ := s.Refresh(ctx)
	if third.Publish.Deleted != 1 {
		t.Errorf("a row no fit gives any more is removed: %+v", third.Publish)
	}
}

func TestServiceDryRunAndNoTable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := fixture(t, true)
	s.Mode = ModeDryRun
	res, err := s.Refresh(ctx)
	if err != nil || res.Publish.Changed == 0 || res.Publish.PublishedAt != nil {
		t.Fatalf("%+v %v", res.Publish, err)
	}
	var n int64
	s.Sched.Table(Table).Count(&n)
	if n != 0 {
		t.Fatalf("dry run wrote %d rows", n)
	}
	s = fixture(t, false)
	res, err = s.Refresh(ctx)
	if err != nil || res.Publish.Skipped != SkipNoTable || res.Subs != 40 {
		t.Fatalf("%+v %v", res.Publish, err)
	}
	if a, _ := s.Current(ctx); a != s.Latest() {
		t.Fatal("Current reuses the last fit")
	}
	s.Mode = ModeOff
	if res, _ := s.Refresh(ctx); res.Publish.Skipped != SkipOff {
		t.Fatalf("%+v", res.Publish)
	}
	s.Site = func(context.Context) (sky.Site, error) { return sky.Site{}, ErrNoSite }
	if _, err := s.Refresh(ctx); !errors.Is(err, ErrNoSite) {
		t.Fatalf("no site: %v", err)
	}
}
