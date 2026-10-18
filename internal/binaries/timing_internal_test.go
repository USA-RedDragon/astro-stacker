package binaries

import (
	"context"
	"math"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/testdb"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/ownimage"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/period"
)

type fakePhot struct {
	subs  []ownimage.Sub
	truth map[float64]float64
	e     *period.Ephemeris
	calls atomic.Int32
	noise float64
}

func (f *fakePhot) Subs(context.Context, string) ([]ownimage.Sub, error) { return f.subs, nil }

func (f *fakePhot) Curve(_ context.Context, _ string, spot ownimage.Spot, subs []ownimage.Sub) (*ownimage.Curve, error) {
	f.calls.Add(1)
	c := &ownimage.Curve{Comps: 8}
	for i, s := range subs {
		b := lc.BJD(midOf(s), spot.RA, spot.Dec)
		var y float64
		for _, d := range f.e.Eclipses {
			n := math.Round((b-f.e.T0)/f.e.Period - d.Phase)
			tm := f.e.T0 + (n+d.Phase)*f.e.Period
			if t, ok := f.truth[tm]; ok {
				tm = t
			}
			y += d.Depth[keyZTFr] * period.Shape((b-tm)/f.e.Period, d.Width, d.Shape)
		}
		y += f.noise * math.Sin(float64(i)*12.9898+float64(s.FrameID)*78.233) * 1.7
		c.Points = append(c.Points, ownimage.Point{FrameID: s.FrameID, BJD: b, Filter: s.Filter, DMag: y, Err: f.noise})
	}
	return c, nil
}

func timingEphemeris() *period.Ephemeris {
	return &period.Ephemeris{Period: 3.3, PeriodErr: 2e-6, T0: 2460700.4, T0Err: 0.0005, Kind: period.KindEclipsing,
		Eclipses: []period.Eclipse{
			{Phase: 0, Width: 0.03, Shape: 1.5, Depth: map[string]float64{keyZTFr: 0.4}},
			{Phase: 0.5, Width: 0.03, Shape: 1.5, Depth: map[string]float64{keyZTFr: 0.2}},
		}}
}

// subsOver covers bjd ± reach days with subs every step seconds.
func subsOver(bjd, reach, step float64, firstID int) []ownimage.Sub {
	var out []ownimage.Sub
	id := firstID
	for b := bjd - reach; b <= bjd+reach; b += step / 86400 {
		mid := lc.UTCOfBJD(b, testRA, testDec)
		out = append(out, ownimage.Sub{FrameID: id, Filter: "Red", Exposure: 120, Start: mid.Add(-time.Minute), Night: mid.Truncate(24 * time.Hour)})
		id++
	}
	return out
}

func timingStar(e *period.Ephemeris) Star {
	return Star{Source: app.DiscoverySource{ID: 7, Object: "Timing field", Kind: app.DiscoveryKindChange},
		Facts: &Facts{Ephemeris: e, Gaia: &GaiaFacts{RA: testRA, Dec: testDec, G: 14}}}
}

func TestTimingFromOwnFrames(t *testing.T) {
	t.Parallel()
	db := testdb.SQLite(t, "timing")
	if err := db.AutoMigrate(&app.EclipseTiming{}, &app.EclipseRequest{}); err != nil {
		t.Fatal(err)
	}
	e := timingEphemeris()
	half := 0.03 * e.Period
	prim := e.T0 + 200*e.Period
	sec := e.T0 + 230.5*e.Period
	late, early := 4.0/1440, -2.5/1440
	ph := &fakePhot{e: e, noise: 0.008, truth: map[float64]float64{prim: prim + late, sec: sec + early}}
	ph.subs = append(subsOver(prim, 1.8*half, 150, 1), subsOver(sec, 1.8*half, 150, 1000)...)
	ph.subs = append(ph.subs, subsOver(e.T0+215*e.Period-0.85*half, 0.75*half, 150, 2000)...)
	reqMid := lc.UTCOfBJD(prim, testRA, testDec)
	req := app.EclipseRequest{SourceID: 7, Kind: KindPrimary, Mid: reqMid, State: app.EclipseDone, Note: "The window has passed."}
	if err := db.Create(&req).Error; err != nil {
		t.Fatal(err)
	}
	now := lc.UTCOfBJD(sec+5, testRA, testDec)
	loc := chicago(t)
	svc := &Service{DB: db, Photometry: ph, Loc: loc, Now: func() time.Time { return now }}
	st := timingStar(e)
	ctx := context.Background()
	if err := svc.timingWork(ctx, []Star{st}); err != nil {
		t.Fatal(err)
	}
	var rows []app.EclipseTiming
	if err := db.Order("predicted").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d timings, want 2 (the eclipse with one side only is skipped): %+v", len(rows), rows)
	}
	for i, want := range []float64{prim + late, sec + early} {
		r := rows[i]
		if r.State != app.TimingGood {
			t.Fatalf("timing %d: %s %q", i, r.State, r.Note)
		}
		if d := math.Abs(r.BJD - want); d > 3*r.Err || r.Err > 1.5/1440 {
			t.Errorf("timing %d: %.6f ± %.2f min, truth %.6f (%.2f min off)", i, r.BJD, r.Err*1440, want, d*1440)
		}
	}
	if rows[0].RequestID == nil || *rows[0].RequestID != req.ID {
		t.Errorf("the requested eclipse's timing is not tied to its request: %v", rows[0].RequestID)
	}
	if err := db.First(&req, req.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(req.Note, "minutes late") {
		t.Errorf("request note %q", req.Note)
	}
	calls := ph.calls.Load()
	if err := svc.timingWork(ctx, []Star{st}); err != nil {
		t.Fatal(err)
	}
	if ph.calls.Load() != calls {
		t.Error("measured again with no new frames")
	}

	st.Timings = rows
	rec := st.Record(loc)
	if len(rec.Timings) != 2 {
		t.Fatalf("record %+v", rec)
	}
	if v := rec.Timings[0]; v.Kind != KindPrimary || math.Abs(v.LateMinutes-4) > 3*v.ErrMinutes+0.1 {
		t.Errorf("deeper eclipse %+v, want 4 minutes late", v)
	}
	if v := rec.Timings[1]; v.Kind != KindSecondary || math.Abs(v.LateMinutes+2.5) > 3*v.ErrMinutes+0.1 {
		t.Errorf("second eclipse %+v, want 2.5 minutes early", v)
	}
	for _, v := range rec.Timings {
		if d := (v.BJD - v.HJD) * 86400; d < 60 || d > 78 {
			t.Errorf("BJD TDB minus HJD UTC is %.1f s, want TT-UTC (69.2 s) give or take the Sun's offset from the barycentre", d)
		}
	}
	if len(rec.Trends) != 4 || rec.Trends[0].State != "need" || !strings.HasPrefix(rec.Trends[0].Why, "Not enough timings yet") {
		t.Errorf("trends from two timings: %+v", rec.Trends)
	}
}

func TestNightLabelAcrossDaylightTimeEnd(t *testing.T) {
	t.Parallel()
	loc := chicago(t)
	cases := []struct {
		utc  time.Time
		want string
	}{
		{time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC), "2026-10-31"},
		{time.Date(2026, 11, 1, 7, 30, 0, 0, time.UTC), "2026-10-31"},
		{time.Date(2026, 11, 2, 4, 0, 0, 0, time.UTC), "2026-11-01"},
		{time.Date(2026, 3, 8, 8, 30, 0, 0, time.UTC), "2026-03-07"},
	}
	for _, c := range cases {
		if got := nightOf(c.utc, loc); got != c.want {
			t.Errorf("%s: night %s, want %s", c.utc, got, c.want)
		}
	}
}

func TestTimingOnTheEphemerisScale(t *testing.T) {
	t.Parallel()
	e := timingEphemeris()
	tm := e.T0 + 400*e.Period
	utc := lc.UTCOfBJD(tm, testRA, testDec)
	if back := lc.BJD(utc, testRA, testDec); math.Abs(back-tm)*86400 > 0.01 {
		t.Fatalf("BJD round trip off by %.3f s", (back-tm)*86400)
	}
	st := timingStar(e)
	st.Timings = []app.EclipseTiming{{ID: 1, SourceID: 7, Kind: KindPrimary, Predicted: utc, BJD: tm + 1.0/1440, Err: 0.5 / 1440, State: app.TimingGood, Method: "model"}}
	rec := st.Record(time.UTC)
	if v := rec.Timings[0]; math.Abs(v.LateMinutes-1) > 0.05 || v.Cycle != 400 {
		t.Errorf("one minute late at cycle 400: %+v", v)
	}
	jd := 2440587.5 + float64(utc.UnixNano())/86400e9
	if d := (rec.Timings[0].HJD - jd) * 86400; math.Abs(d) > 500.1 {
		t.Errorf("HJD is %.0f s from the JD, more than light takes across Earth's orbit", d)
	}
}
