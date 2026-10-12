package goals

import (
	"context"
	"errors"
	"math"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/bodies"
	"github.com/USA-RedDragon/astro-stacker/internal/frameheader"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
)

const (
	methodHeader = "header"
	regKey       = "reg"
	typeLight    = "LIGHT"
	guidComet    = "g-161p"
	guidAsteroid = "g-vesta"
	objComet     = "161P/Hartley-IRAS"
	objAsteroid  = "4 Vesta"
	filterL      = "Luminance"
)

func TestComaRegion(t *testing.T) {
	t.Parallel()
	bw, bh := 200, 200
	hx, hy := 400.0, 400.0
	at := func(region []bool, x, y float64) bool {
		return region[int(y)/NoiseBin*bw+int(x)/NoiseBin]
	}
	tail := [2]float64{1, 0}
	c := ComaRegions(bw, bh, hx, hy, &tail, 2)
	region, n := c.Union(), c.HeadBins+c.TailBins
	if c.HeadBins == 0 || c.TailBins == 0 || !at(c.Head, hx, hy) || at(c.Tail, hx, hy) || !at(c.Tail, hx+60, hy) {
		t.Fatalf("head %d tail %d", c.HeadBins, c.TailBins)
	}
	for _, c := range []struct {
		x, y float64
		in   bool
		what string
	}{
		{hx, hy, true, "head"},
		{hx - 12, hy, true, "head, sunward side within the coma radius"},
		{hx - 30, hy, false, "sunward beyond the coma radius"},
		{hx + 60, hy, true, "inner tail"},
		{hx + 60, hy + 40, false, "beside the tail"},
		{hx + 100, hy, false, "beyond the inner tail"},
	} {
		if got := at(region, c.x, c.y); got != c.in {
			t.Errorf("%s: in=%v", c.what, got)
		}
	}
	h := ComaRegions(bw, bh, hx, hy, nil, 2)
	if h.TailBins != 0 || h.HeadBins >= n || !at(h.Union(), hx, hy) || at(h.Union(), hx+60, hy) {
		t.Errorf("head only: %d bins of %d", h.HeadBins, n)
	}
	if z := ComaRegions(bw, bh, hx, hy, &tail, 0); z.HeadBins+z.TailBins != 0 {
		t.Error("a region without a pixel scale")
	}
}

func star(w, h int, x, y, flux, sky float64, peakClip float32) []float32 {
	sub := make([]float32, w*h)
	const s = 1.5
	for j := range h {
		for i := range w {
			r2 := (float64(i)-x)*(float64(i)-x) + (float64(j)-y)*(float64(j)-y)
			v := float32(sky + flux/(2*math.Pi*s*s)*math.Exp(-r2/(2*s*s)))
			sub[j*w+i] = min(v, peakClip)
		}
	}
	return sub
}

func TestApertureFlux(t *testing.T) {
	t.Parallel()
	w, h := 64, 64
	f := ApertureFlux(star(w, h, 32, 32, 0.5, 0.1, 0.95), w, h, 32, 32, 0.9)
	if !f.OK || f.Saturated || math.Abs(f.Flux-0.5) > 0.01 {
		t.Errorf("flux %+v", f)
	}
	if s := ApertureFlux(star(w, h, 32, 32, 50, 0.1, 1), w, h, 32, 32, 0.9); !s.Saturated || s.OK {
		t.Errorf("saturated %+v", s)
	}
	if e := ApertureFlux(star(w, h, 4, 4, 0.5, 0.1, 1), w, h, 4, 4, 0.9); e.OK || e.Saturated {
		t.Errorf("at the edge %+v", e)
	}
}

func TestPointSNR(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(1, 2))
	fluxes := make([]float64, 400)
	for i := range fluxes {
		fluxes[i] = 100 + 10*rng.NormFloat64()
	}
	r, err := PointSNR(fluxes, 0)
	if err != nil || math.Abs(r.SNR-200)/200 > 0.1 || PointSaturated(r) {
		t.Errorf("SNR %+v %v", r, err)
	}
	if _, err := PointSNR(fluxes[:MinSubs-1], 0); !errors.Is(err, ErrInsufficientData) {
		t.Errorf("too few: %v", err)
	}
	s, err := PointSNR(fluxes[:3], 6)
	if err != nil || !PointSaturated(s) || s.SNR != 0 {
		t.Errorf("saturated %+v %v", s, err)
	}
	if _, err := PointSNR(fluxes[:5], 4); !errors.Is(err, ErrInsufficientData) {
		t.Errorf("few unsaturated: %v", err)
	}
}

func TestNightOfMatchesFrameNights(t *testing.T) {
	t.Parallel()
	lon := -97.4
	for _, at := range []string{"2026-10-13T00:30:00Z", "2026-10-13T11:00:00Z", "2026-10-12T19:00:00Z", "2026-10-13T17:00:00Z"} {
		ts, _ := time.Parse(time.RFC3339, at)
		want, _ := frameheader.Frame{DateObs: ts, HasDate: true, Longitude: lon}.Night()
		if got := NightOf(ts, lon); !got.Equal(want) {
			t.Errorf("%s: %s, frames say %s", at, got, want)
		}
	}
}

func night(s string) time.Time {
	t, _ := time.Parse(NightLayout, s)
	return t
}

func cometNight(n string, snr float64, subs int) app.BodyNight {
	return app.BodyNight{Object: objComet, Filter: filterL, Night: night(n), Kind: BodyComet, Subs: subs, Levels: 3,
		EffectiveHours: 1, SNR: snr, Signal: snr, NoiseNow: 1, NoiseA: 1, GainPerHourPct: 20, MeasuredAt: night(n).Add(30 * time.Hour)}
}

func TestEvaluateBodyComet(t *testing.T) {
	t.Parallel()
	g := DefaultBodyGoal(BodyComet, filterL)
	past := cometNight("2026-10-11", 14, 40)
	prog, rd, st := EvaluateBody(BodyComet, []app.BodyNight{past}, g, night("2026-10-12"), 0)
	if prog != nil || rd.State != StateCollecting || rd.Reason != ReasonNoSubsTonight || !rd.Open || st.Current != nil || len(st.Nights) != 1 || !st.Nights[0].Done {
		t.Fatalf("new night %+v %+v %+v", prog, rd, st)
	}
	_, rd, _ = EvaluateBody(BodyComet, []app.BodyNight{past}, g, night("2026-10-12"), 5)
	if rd.Reason != ReasonTonightNotBuilt || !rd.Open {
		t.Errorf("tonight not built %+v", rd)
	}
	insufficient := ErrInsufficientData.Error()
	few := cometNight("2026-10-12", 0, 6)
	few.Error = &insufficient
	_, rd, _ = EvaluateBody(BodyComet, []app.BodyNight{past, few}, g, night("2026-10-12"), 9)
	if rd.State != StateCollecting || !rd.Open || rd.Reason != "6 subs that night; 8 are needed to measure" {
		t.Errorf("too few subs measured, enough taken %+v", rd)
	}
	tonight := cometNight("2026-10-12", 5, 20)
	prog, rd, st = EvaluateBody(BodyComet, []app.BodyNight{tonight, past}, g, night("2026-10-12"), 20)
	if prog == nil || prog.Done || math.Abs(prog.Progress-0.25) > 1e-9 || !rd.Open || rd.State != StateMeasured || st.Current == nil || *st.Current.SNR != 5 || st.Nights[0].Night != "2026-10-11" {
		t.Errorf("tonight halfway %+v %+v %+v", prog, rd, st)
	}
	tonight.SNR = 12
	prog, rd, _ = EvaluateBody(BodyComet, []app.BodyNight{tonight, past}, g, night("2026-10-12"), 20)
	if prog == nil || !prog.Done || rd.Open {
		t.Errorf("tonight met %+v %+v", prog, rd)
	}
	tonight.SNR, tonight.GainPerHourPct = 4, 1
	prog, rd, _ = EvaluateBody(BodyComet, []app.BodyNight{tonight}, g, night("2026-10-12"), 20)
	if prog == nil || !prog.Done || !prog.Plateau || rd.Open {
		t.Errorf("tonight plateaued %+v %+v", prog, rd)
	}
	g.PlateauStop = false
	if prog, _, _ := EvaluateBody(BodyComet, []app.BodyNight{tonight}, g, night("2026-10-12"), 20); prog.Done {
		t.Error("plateau stop off")
	}
}

func asteroidNight(n string, snr, trail float64) app.BodyNight {
	return app.BodyNight{Object: objAsteroid, Filter: filterL, Night: night(n), Kind: BodyAsteroid, Subs: 30, EffectiveHours: 0.5,
		SNR: snr, TrailPx: &trail, MeasuredAt: night(n).Add(30 * time.Hour)}
}

func TestEvaluateBodyAsteroid(t *testing.T) {
	t.Parallel()
	g := DefaultBodyGoal(BodyAsteroid, filterL)
	if g.SNR != DefaultAsteroidSNR {
		t.Fatalf("asteroid default goal %v", g.SNR)
	}
	short := asteroidNight("2026-10-11", 80, 5)
	prog, rd, st := EvaluateBody(BodyAsteroid, []app.BodyNight{short}, g, night("2026-10-11"), 30)
	if prog == nil || prog.Done || prog.Progress != 0.999 || !rd.Open || st.Done || !st.Current.TrailShort || prog.Plateau {
		t.Errorf("trail too short %+v %+v %+v", prog, rd, st)
	}
	good := asteroidNight("2026-10-12", 60, 25)
	prog, rd, st = EvaluateBody(BodyAsteroid, []app.BodyNight{short, good}, g, night("2026-10-14"), 0)
	if prog == nil || !prog.Done || rd.Open || !st.Done || st.DoneNight != "2026-10-12" || rd.StackSubs != 30 {
		t.Errorf("one good night %+v %+v %+v", prog, rd, st)
	}
	faint := asteroidNight("2026-10-13", 20, 40)
	prog, _, _ = EvaluateBody(BodyAsteroid, []app.BodyNight{faint}, g, night("2026-10-13"), 30)
	if prog.Done || math.Abs(prog.Progress-0.16) > 1e-9 || prog.HoursNeeded <= 0 {
		t.Errorf("faint %+v", prog)
	}
	sat := asteroidNight("2026-10-13", 0, 40)
	sat.Saturated = true
	prog, _, st = EvaluateBody(BodyAsteroid, []app.BodyNight{sat}, g, night("2026-10-15"), 0)
	if !prog.Done || !st.Done || !st.Nights[0].Saturated || st.Nights[0].SNR != nil {
		t.Errorf("saturated %+v %+v", prog, st)
	}
}

func bodySchedDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := openDB(t)
	for _, s := range []string{
		`CREATE TABLE project ("Id" INTEGER PRIMARY KEY, guid TEXT, name TEXT, "isMosaic" INTEGER, minimumaltitude REAL, state INTEGER)`,
		`CREATE TABLE target ("Id" INTEGER PRIMARY KEY, name TEXT, guid TEXT, projectid INTEGER, active INTEGER, ra REAL, dec REAL, rotation REAL)`,
		`CREATE TABLE exposuretemplate ("Id" INTEGER PRIMARY KEY, name TEXT, filtername TEXT)`,
		`CREATE TABLE exposureplan ("Id" INTEGER PRIMARY KEY, targetid INTEGER, "exposureTemplateId" INTEGER)`,
		`INSERT INTO project VALUES (1, 'p1', 'Bodies', 0, 30, 1)`,
		`INSERT INTO target ("Id", name, guid, projectid, active) VALUES (1, '161P/Hartley-IRAS', 'g-161p', 1, 1), (2, '4 Vesta', 'g-vesta', 1, 1)`,
		`INSERT INTO exposuretemplate VALUES (1, 'L', 'Luminance')`,
		`INSERT INTO exposureplan VALUES (1, 1, 1), (2, 2, 1)`,
		bodies.TargetSchema,
		`INSERT INTO ts_target_body (target_guid, designation, kind) VALUES ('g-161p', '161P', 'comet'), ('g-vesta', '4', 'asteroid')`,
		CreateGoalSQLite, CreateProgressSQLite,
		`INSERT INTO ts_goal VALUES ('g-161p', 'Luminance', 0, 10, NULL, 1, NULL, '2026-10-12 00:00:00'), ('g-vesta', 'Luminance', 0, 50, NULL, 1, NULL, '2026-10-12 00:00:00')`,
	} {
		if err := db.Exec(s).Error; err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	for _, c := range []string{"state TEXT", "reason TEXT", "stack_subs INTEGER", "min_subs INTEGER", "sub_limit INTEGER"} {
		if err := db.Exec("ALTER TABLE ts_goal_progress ADD COLUMN " + c).Error; err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func bodyAppDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := appDB(t)
	if err := db.AutoMigrate(&app.Stack{}, &app.StackFrame{}, &app.Frame{}, &app.BodyNight{}); err != nil {
		t.Fatal(err)
	}
	for _, l := range []app.FrameTarget{{FrameID: 1, Object: objComet, TargetGUID: guidComet, Method: methodHeader}, {FrameID: 2, Object: objAsteroid, TargetGUID: guidAsteroid, Method: methodHeader}} {
		if err := db.Create(&l).Error; err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func addNightSubs(t *testing.T, db *gorm.DB, object, filter, n string, count int) {
	t.Helper()
	var s app.Stack
	if err := db.Where(app.Stack{Object: object, Filter: filter}).FirstOrCreate(&s).Error; err != nil {
		t.Fatal(err)
	}
	key := regKey
	d := night(n)
	for i := range count {
		at := d.Add(30 * time.Hour)
		f := app.Frame{Key: object + n + string(rune('a'+i)), ETag: "e", LastModified: at, Type: typeLight, Object: object, Filter: filter, DateObs: &at, Night: &d}
		if err := db.Create(&f).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&app.StackFrame{FrameID: f.ID, StackID: &s.ID, Status: app.StackStatusAdded, Weight: 1, Exposure: 60, RegisteredKey: &key}).Error; err != nil {
			t.Fatal(err)
		}
	}
}

func TestPublishBodyGoals(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sched, db := bodySchedDB(t), bodyAppDB(t)
	now := time.Date(2026, 10, 13, 4, 0, 0, 0, time.UTC)
	p := &Publisher{App: db, Sched: sched, Mode: PublishOn, Now: func() time.Time { return now },
		Longitude: func(context.Context) (float64, error) { return -97.4, nil }}
	addNightSubs(t, db, objComet, filterL, "2026-10-12", 20)
	comet := cometNight("2026-10-12", 11, 20)
	trail := 30.0
	vesta := app.BodyNight{Object: objAsteroid, Filter: filterL, Night: night("2026-10-12"), Kind: BodyAsteroid, Subs: 25, EffectiveHours: 0.4, SNR: 70, TrailPx: &trail, MeasuredAt: now}
	if err := db.Create(&[]app.BodyNight{comet, vesta}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := p.Publish(ctx); err != nil {
		t.Fatal(err)
	}
	rows := readRows(t, sched)
	c, v := rows[guidComet+"/"+filterL], rows[guidAsteroid+"/"+filterL]
	if c.Done != 1 || *c.StackSubs != 20 || *c.State != StateMeasured || *c.GoalValue != 10 {
		t.Errorf("comet tonight %+v", c)
	}
	if v.Done != 1 || *v.State != StateMeasured || *v.GoalValue != 50 {
		t.Errorf("asteroid %+v", v)
	}
	now = now.Add(24 * time.Hour)
	if _, err := p.Publish(ctx); err != nil {
		t.Fatal(err)
	}
	rows = readRows(t, sched)
	c, v = rows[guidComet+"/"+filterL], rows[guidAsteroid+"/"+filterL]
	if c.Done != 0 || *c.StackSubs != 0 || *c.State != StateCollecting || *c.Reason != ReasonNoSubsTonight || c.Progress != nil {
		t.Errorf("comet next night %+v", c)
	}
	if v.Done != 1 {
		t.Errorf("asteroid stays done %+v", v)
	}
	addNightSubs(t, db, objComet, filterL, "2026-10-13", 9)
	if _, err := p.Publish(ctx); err != nil {
		t.Fatal(err)
	}
	if c = readRows(t, sched)[guidComet+"/"+filterL]; c.Done != 0 || *c.StackSubs != 9 || *c.Reason != ReasonTonightNotBuilt {
		t.Errorf("comet next night, subs before its stack %+v", c)
	}
}

func TestEvaluateBodyNegativeSNRIsNotDone(t *testing.T) {
	t.Parallel()
	comet := cometNight("2026-10-12", -12, 30)
	prog, rd, _ := EvaluateBody(BodyComet, []app.BodyNight{comet}, DefaultBodyGoal(BodyComet, filterL), night("2026-10-12"), 30)
	if prog == nil || prog.Done || prog.Progress != 0 || !rd.Open {
		t.Errorf("comet %+v %+v", prog, rd)
	}
	ast := asteroidNight("2026-10-12", -60, 30)
	prog, _, st := EvaluateBody(BodyAsteroid, []app.BodyNight{ast}, DefaultBodyGoal(BodyAsteroid, filterL), night("2026-10-12"), 30)
	if prog.Done || st.Done {
		t.Errorf("asteroid %+v %+v", prog, st)
	}
}

func TestEvaluateBodyKeepsImagingUntilMeasured(t *testing.T) {
	t.Parallel()
	g := DefaultBodyGoal(BodyComet, filterL)
	_, rd, _ := EvaluateBody(BodyComet, nil, g, night("2026-10-12"), MinSubs+5)
	if rd.State != StateCollecting || !rd.Open || rd.MinSubs != UnmeasurableSubLimit {
		t.Errorf("waiting for tonight's stack %+v", rd)
	}
	_, rd, _ = EvaluateBody(BodyComet, nil, g, night("2026-10-12"), UnmeasurableSubLimit)
	if rd.Open {
		t.Errorf("bounded %+v", rd)
	}
	off := "the body was outside the target's reference frame that night"
	bad := cometNight("2026-10-12", 0, 20)
	bad.Error = &off
	_, rd, _ = EvaluateBody(BodyComet, []app.BodyNight{bad}, g, night("2026-10-12"), 20)
	if rd.State != StateUnmeasurable || !rd.Open || rd.Reason != off {
		t.Errorf("error tonight %+v", rd)
	}
}
