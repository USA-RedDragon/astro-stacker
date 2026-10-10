package mosaicplan_test

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/mosaicplan"
	"github.com/USA-RedDragon/astro-stacker/internal/mosaics"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	light     = "LIGHT"
	panel1    = "Markarian Chain Panel 1"
	panel2    = "Markarian Chain Panel 2"
	markarian = "Markarian Chain"
	filterHa  = "H-a"
	filterRed = "Red"
)

func open(t *testing.T, name string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s-%s?mode=memory&cache=shared", t.Name(), name)), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func schedFixture(t *testing.T) *gorm.DB {
	t.Helper()
	db := open(t, "sched")
	stmts := make([]string, 0, 16)
	stmts = append(stmts,
		`CREATE TABLE project ("Id" INTEGER PRIMARY KEY, name TEXT, guid TEXT, "isMosaic" INTEGER, minimumaltitude REAL, priority INTEGER, state INTEGER, minimumtime INTEGER)`,
		`CREATE TABLE target ("Id" INTEGER PRIMARY KEY, name TEXT, active INTEGER, ra REAL, dec REAL, rotation REAL, projectid INTEGER, guid TEXT)`,
		`CREATE TABLE exposuretemplate ("Id" INTEGER PRIMARY KEY, filtername TEXT, defaultexposure REAL)`,
		`CREATE TABLE exposureplan ("Id" INTEGER PRIMARY KEY, targetid INTEGER, "exposureTemplateId" INTEGER, exposure REAL, desired INTEGER, accepted INTEGER, enabled INTEGER)`,
		`CREATE TABLE ruleweight ("Id" INTEGER PRIMARY KEY, name TEXT, weight REAL, projectid INTEGER)`,
		`INSERT INTO project VALUES (1, 'Markarian Chain', 'pm', 1, 20, 1, 1, 60), (2, 'Rho', 'pr', 1, 10, 1, 1, 90), (3, 'Rosette', 'pros', 0, 0, 1, 1, 60)`,
		`INSERT INTO exposuretemplate VALUES (1, 'Red', 120), (2, 'H-a', 300)`,
		`INSERT INTO ruleweight VALUES (1, 'Panel Deficit', 75, 1), (2, 'Mosaic Completion', 0, 1), (3, 'Mosaic Completion', 0, 2)`,
	)
	ra, dec := 12.45, 13.0
	step := 3.32 * 0.85 / 15 / math.Cos(dec*math.Pi/180)
	for i := range 2 {
		stmts = append(stmts, fmt.Sprintf(`INSERT INTO target VALUES (%d, 'Markarian Chain Panel %d', 1, %f, %f, 0, 1, 'm%d')`, i+1, i+1, ra-float64(i)*step, dec, i+1))
	}
	stmts = append(stmts,
		`INSERT INTO target VALUES (10, 'IC 4604 Panel 1', 1, 16.40, -24.0, 0, 2, 'r1')`,
		fmt.Sprintf(`INSERT INTO target VALUES (11, 'IC 4604 Panel 2', 1, %f, -24.0, 0, 2, 'r2')`, 16.40-3.32*0.85/15/math.Cos(24*math.Pi/180)),
		`INSERT INTO target VALUES (20, 'Rosette Nebula', 1, 6.53, 4.95, 0, 3, 'ros1')`,
		`INSERT INTO target VALUES (21, 'Rosette Nebula SHO', 1, 6.53, 4.95, 0, 3, 'ros2')`,
		`INSERT INTO exposureplan VALUES (1, 1, 1, -1, 100, 50, 1), (2, 1, 2, 300, 40, 40, 1), (3, 2, 1, 120, 100, 10, 1), (4, 2, 2, -1, 40, 20, 1), (5, 2, 2, 300, 99, 0, 0)`,
	)
	for _, q := range stmts {
		if err := db.Exec(q).Error; err != nil {
			t.Fatal(q, err)
		}
	}
	return db
}

func appFixture(t *testing.T) *gorm.DB {
	t.Helper()
	db := open(t, "app")
	if err := db.AutoMigrate(&app.Frame{}, &app.Stack{}, &app.StackFrame{}, &app.GoalMeasurement{}, &app.MosaicPanel{}, &app.MosaicAdoption{},
		&app.FrameTarget{}, &app.MosaicSeam{}, &app.MosaicPanelHealth{}, &app.Mosaic{}); err != nil {
		t.Fatal(err)
	}
	ra, dec := 120.0, 40.0
	nights := []time.Time{time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)}
	frames := []app.Frame{
		{ID: 1, Key: "m1a", ETag: "e", Type: light, Object: panel1, Night: &nights[0]},
		{ID: 2, Key: "m2a", ETag: "e", Type: light, Object: panel2, Night: &nights[1]},
		{ID: 3, Key: "dol", ETag: "e", Type: light, Object: "Dolphin Head", MountRA: &ra, MountDec: &dec},
	}
	stacks := []app.Stack{
		{ID: 1, Object: panel1, Filter: filterRed, Subs: 50, EffectiveSeconds: 4 * 3600},
		{ID: 2, Object: panel1, Filter: filterHa, Subs: 40, EffectiveSeconds: 9 * 3600},
		{ID: 3, Object: panel2, Filter: filterRed, Subs: 10, EffectiveSeconds: 1 * 3600},
		{ID: 4, Object: panel2, Filter: filterHa, Subs: 20, EffectiveSeconds: 4 * 3600},
	}
	sid1, sid4 := 1, 4
	sf := []app.StackFrame{
		{FrameID: 1, StackID: &sid1, Status: app.StackStatusAdded, Score: 1, Exposure: 7200},
		{FrameID: 2, StackID: &sid4, Status: app.StackStatusAdded, Score: 0.5, Exposure: 7200},
	}
	gm := []app.GoalMeasurement{{Object: panel1, Filter: filterRed, Subs: 50, EffectiveHours: 4, SNR: 5, GainPerHourPct: 10}}
	for _, v := range []any{&frames, &stacks, &sf, &gm} {
		if err := db.Create(v).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&app.MosaicSeam{Project: markarian, Filter: filterHa, PanelA: 1, PanelB: 2, NoiseA: 1, NoiseB: 2, NoiseRatio: 2, Samples: 900}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&app.MosaicPanelHealth{Project: markarian, Filter: filterHa, Panel: 2, GapFraction: 0.08, GapDeg2: 0.6, GapWhere: "NE corner"}).Error; err != nil {
		t.Fatal(err)
	}
	return db
}

func TestRunAdoptionDryRunWritesNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	appDB := appFixture(t)
	svc := mosaicplan.New(appDB, schedFixture(t))
	svc.Rig = mosaics.Rig{WidthDeg: 3.32, HeightDeg: 2.22, ScaleArcsec: 1.915}
	rep, err := svc.RunAdoption(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.DryRun || rep.Clean != 1 || rep.Review != 4 || rep.New != 4 {
		t.Errorf("report %+v", rep)
	}
	var n int64
	appDB.Model(&app.MosaicAdoption{}).Count(&n)
	if n != 0 {
		t.Errorf("dry run wrote %d adoptions", n)
	}
	appDB.Model(&app.MosaicPanel{}).Count(&n)
	if n != 0 {
		t.Errorf("dry run wrote %d panels", n)
	}
}

func TestRunAdoptionThenDecide(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	appDB := appFixture(t)
	svc := mosaicplan.New(appDB, schedFixture(t))
	svc.Rig = mosaics.Rig{WidthDeg: 3.32, HeightDeg: 2.22, ScaleArcsec: 1.915}
	rep, err := svc.RunAdoption(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Clean != 1 || rep.Review != 4 || rep.Changed != 0 {
		t.Fatalf("report %+v", rep)
	}
	byKind := map[string]mosaicplan.Adoption{}
	for _, it := range rep.Items {
		byKind[it.Project] = it
	}
	if a := byKind[markarian]; a.Status != app.AdoptionProposed || !a.Clean || len(a.Panels) != 2 {
		t.Errorf("markarian %+v", a)
	}
	var none int64
	appDB.Model(&app.MosaicPanel{}).Count(&none)
	if none != 0 {
		t.Fatalf("adoption wrote %d panels before any decision", none)
	}
	if _, err := svc.Decide(ctx, byKind[markarian].ID, "accept", "test"); err != nil {
		t.Fatal(err)
	}
	if a := byKind["Rho"]; a.Status != app.AdoptionProposed || a.Confidence != mosaics.ConfidenceHigh {
		t.Errorf("rho %+v", a)
	}
	if a := byKind["Rosette"]; a.Kind != mosaics.KindNotMosaic {
		t.Errorf("rosette %+v", a)
	}
	dol := byKind["Dolphin Head"]
	if dol.Kind != mosaicplan.KindFrames || dol.Frames == nil || dol.Frames.Count != 1 || dol.Confidence != mosaics.ConfidenceLow {
		t.Errorf("dolphin %+v %+v", dol, dol.Frames)
	}
	var panels []app.MosaicPanel
	appDB.Order("panel").Find(&panels)
	if len(panels) != 2 || panels[0].ProjectGUID != "pm" || panels[0].Source != app.MosaicSourceAdopted || panels[1].Neighbours != `["m1"]` {
		t.Fatalf("panels %+v", panels)
	}

	again, err := svc.RunAdoption(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if again.Unchanged != 4 || again.New != 0 || again.Review != 3 {
		t.Errorf("second run %+v", again)
	}

	checkDecisions(ctx, t, svc, appDB, byKind["Rho"].ID)
}

func checkDecisions(ctx context.Context, t *testing.T, svc *mosaicplan.Service, appDB *gorm.DB, rhoID int) {
	t.Helper()
	list, err := svc.Adoptions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 4 || list[0].Status != app.AdoptionProposed || list[3].Status != app.AdoptionAccepted {
		t.Errorf("order %+v", list)
	}
	rho, err := svc.Decide(ctx, rhoID, "accept", "test")
	if err != nil {
		t.Fatal(err)
	}
	if rho.Status != app.AdoptionAccepted || rho.DecidedBy != "test" {
		t.Errorf("rho decided %+v", rho)
	}
	var n int64
	appDB.Model(&app.MosaicPanel{}).Where("project_guid = ?", "pr").Count(&n)
	if n != 2 {
		t.Errorf("%d rho panels", n)
	}
	if _, err := svc.Decide(ctx, rhoID, "reject", "test"); err != nil {
		t.Fatal(err)
	}
	appDB.Model(&app.MosaicPanel{}).Where("project_guid = ?", "pr").Count(&n)
	if n != 0 {
		t.Errorf("%d rho panels after rejecting", n)
	}
	if _, err := svc.Decide(ctx, 999, "accept", "test"); err == nil {
		t.Error("unknown adoption decided")
	}
	if _, err := svc.Decide(ctx, 1, "maybe", "test"); err == nil {
		t.Error("bad decision accepted")
	}
}

func TestDetailUsesWeakestPanelAndFallsBackToScheduler(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc := mosaicplan.New(appFixture(t), schedFixture(t))
	svc.Rig = mosaics.Rig{WidthDeg: 3.32, HeightDeg: 2.22, ScaleArcsec: 1.915}
	rep, err := svc.RunAdoption(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range rep.Items {
		if it.Clean {
			if _, err := svc.Decide(ctx, it.ID, "accept", "test"); err != nil {
				t.Fatal(err)
			}
		}
	}
	d, err := svc.Detail(ctx, "pm")
	if err != nil {
		t.Fatal(err)
	}
	if !d.Adopted || len(d.Panels) != 2 || d.Rows != 1 || d.Cols != 2 || !d.Balancing.On || d.Balancing.PanelDeficit != 75 || !d.Balancing.PanelDeficitSet {
		t.Fatalf("detail %+v", d)
	}
	if strings.Join(d.Filters, ",") != "Red,H-a" {
		t.Errorf("filters %v", d.Filters)
	}
	checkPanels(t, d)
	checkList(ctx, t, svc)
}

func checkPanels(t *testing.T, d mosaicplan.Detail) {
	t.Helper()
	p1, p2 := d.Panels[0], d.Panels[1]
	red := p1.Filters[0]
	if red.Source != mosaicplan.SourceGoal || math.Abs(red.Progress-0.25) > 1e-9 || red.SNR != 5 {
		t.Errorf("panel 1 red %+v", red)
	}
	ha := p1.Filters[1]
	if ha.Source != mosaicplan.SourceTS || ha.Progress != 1 || !ha.Done || ha.HoursNeeded != 0 {
		t.Errorf("panel 1 H-a %+v", ha)
	}
	if p1.Progress != 0.25 || p1.Weakest != filterRed {
		t.Errorf("panel 1 %+v", p1)
	}
	p2ha := p2.Filters[1]
	if p2ha.Desired != 40 || p2ha.Accepted != 20 || math.Abs(p2ha.PlannedHours-40*300.0/3600) > 1e-9 || math.Abs(p2ha.HoursNeeded-p2ha.PlannedHours/2) > 1e-9 {
		t.Errorf("panel 2 H-a %+v", p2ha)
	}
	if d.Complete != 0.1 || d.WeakestPanel != 2 || d.WeakestFilter != filterRed {
		t.Errorf("complete %v panel %d filter %s", d.Complete, d.WeakestPanel, d.WeakestFilter)
	}
	if len(d.Needs) != 2 || !strings.Contains(d.Needs[0], "Panel 2 needs about +12.0 h H-a") || !strings.Contains(d.Needs[1], "NE corner") {
		t.Errorf("needs %v", d.Needs)
	}
}

func checkList(ctx context.Context, t *testing.T, svc *mosaicplan.Service) {
	t.Helper()
	if _, err := svc.Detail(ctx, "nope"); err == nil {
		t.Error("unknown mosaic found")
	}
	list, err := svc.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Project != "Markarian Chain" || list[0].SeamWarnings != 1 || !list[0].Balancing {
		t.Errorf("list %+v", list)
	}
}

func addNights(t *testing.T, db *gorm.DB, object string, first time.Time, n int, seconds float64) {
	t.Helper()
	for i := range n {
		night := first.AddDate(0, 0, 2*i)
		f := app.Frame{Key: fmt.Sprintf("%s-%d", object, i), ETag: "e", Type: light, Object: object, Night: &night}
		if err := db.Create(&f).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&app.StackFrame{FrameID: f.ID, Status: app.StackStatusAdded, Score: 1, Exposure: seconds}).Error; err != nil {
			t.Fatal(err)
		}
	}
}

func TestSeasonsPlanWithSite(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	appDB := appFixture(t)
	svc := mosaicplan.New(appDB, schedFixture(t))
	svc.Rig = mosaics.Rig{WidthDeg: 3.32, HeightDeg: 2.22, ScaleArcsec: 1.915}
	svc.Site = func(context.Context) (mosaics.Site, bool) { return mosaics.Site{Lat: 32, Lon: -97}, true }
	now := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	plan, err := svc.Seasons(ctx, markarian, "weakest", "", now)
	if err != nil {
		t.Fatal(err)
	}
	if plan.HoursPerSeason != nil || !plan.Basis.InsufficientHistory || plan.Basis.Reason == nil || len(plan.Rows) != 0 || plan.Pace != mosaicplan.PaceMeasured {
		t.Fatalf("a rate was invented from two nights: %+v", plan)
	}
	if !plan.SiteKnown || len(plan.Months) != 12 || len(plan.PanelPriority) != 2 || len(plan.Basis.ClearNightsPerMonth) != 12 {
		t.Fatalf("plan %+v", plan)
	}
	if plan.Months[3].Hours < 3 || plan.Months[8].Hours > 1 {
		t.Errorf("Markarian's dark hours by month %+v", plan.Months)
	}
	if plan.Last == nil || plan.Last.Nights != 2 || math.Abs(plan.Last.Hours-3) > 1e-9 {
		t.Errorf("last season %+v", plan.Last)
	}
	best, err := svc.Seasons(ctx, markarian, "bogus", "good", now)
	if err != nil {
		t.Fatal(err)
	}
	if best.Strategy != "weakest" || best.Pace != mosaicplan.PaceBest || best.HoursPerSeason == nil || *best.HoursPerSeason != 3 || len(best.Rows) == 0 {
		t.Errorf("best season %+v", best)
	}

	checkMeasuredSeasons(t, svc, appDB, now)
}

func checkMeasuredSeasons(t *testing.T, svc *mosaicplan.Service, appDB *gorm.DB, now time.Time) {
	t.Helper()
	ctx := context.Background()
	addNights(t, appDB, panel1, time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC), 8, 3*3600)
	if partial, err := svc.Seasons(ctx, markarian, "weakest", "", now); err != nil || partial.HoursPerSeason != nil || !strings.Contains(*partial.Basis.Reason, "Apr") {
		t.Fatalf("usable months without history were projected: %+v %v", partial.Basis, err)
	}
	for m := range 13 {
		addNights(t, appDB, fmt.Sprintf("Elsewhere %d", m), time.Date(2025, 9, 5, 0, 0, 0, 0, time.UTC).AddDate(0, m, 0), 2, 4*3600)
	}
	measured, err := svc.Seasons(ctx, markarian, "weakest", "", now)
	if err != nil {
		t.Fatal(err)
	}
	b := measured.Basis
	if b.InsufficientHistory || b.HoursPerClearNight == nil || b.ClearNightsPerSeason == nil || measured.HoursPerSeason == nil {
		t.Fatalf("measured basis %+v", b)
	}
	if b.ProjectNights != 10 || b.HistoryNights != 36 || len(b.UsableMonths) == 0 {
		t.Errorf("basis counts %+v", b)
	}
	if want := math.Round(*b.HoursPerClearNight**b.ClearNightsPerSeason*10) / 10; *measured.HoursPerSeason != want {
		t.Errorf("hours per season %v, want %v", *measured.HoursPerSeason, want)
	}
	if measured.FinishSeason == 0 || measured.Compare["weakest"] == 0 {
		t.Errorf("simulation %+v", measured)
	}
}

func TestFrameOffersLayoutsAndAChosenGrid(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	appDB := appFixture(t)
	svc := mosaicplan.New(appDB, schedFixture(t))
	svc.Rig = mosaics.Rig{WidthDeg: 3.32, HeightDeg: 2.22, ScaleArcsec: 1.915}
	rot := 0.0
	req := mosaicplan.FramingRequest{RA: 313, Dec: 44, MajorArcmin: 240, MinorArcmin: 120, PA: 90, Rotation: &rot, Rows: 2, Cols: 3, HoursPerPanel: 10}
	f, err := svc.Frame(ctx, req, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Options) == 0 || !f.Options[0].Recommended || f.Overlap != 15 || f.OverlapSource != "default" || f.MinAltitudeSource != "default" || f.MinAltitude != 30 || f.Rig.WidthDeg != 3.32 || f.NightHours != nil {
		t.Fatalf("framing %+v", f)
	}
	if f.Chosen == nil || len(f.Chosen.Panels) != 6 || f.Chosen.Hours == nil || *f.Chosen.Hours != 60 || f.Chosen.Nights != nil || f.Basis.Reason == nil {
		t.Errorf("chosen without history %+v basis %+v", f.Chosen, f.Basis)
	}
	addNights(t, appDB, "Elsewhere", time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC), 10, 4*3600)
	req.HoursPerPanel = 0
	f, err = svc.Frame(ctx, req, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if f.Basis.HoursPerPanel == nil || *f.Basis.HoursPerPanelSource == "request" || f.Basis.HoursPerClearNight == nil || *f.Basis.HoursPerClearNight != math.Round(43.0/12*100)/100 {
		t.Fatalf("basis %+v", f.Basis)
	}
	if c := f.Chosen; c == nil || c.Nights == nil || *c.Nights != int(math.Ceil(*c.Hours / *f.Basis.HoursPerClearNight)) || c.Seasons != nil {
		t.Errorf("chosen %+v", f.Chosen)
	}
	if _, err := svc.Frame(ctx, mosaicplan.FramingRequest{Dec: 91}, time.Now()); err == nil {
		t.Error("bad dec accepted")
	}
}

func TestHistoryIsCumulativePerPanel(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc := mosaicplan.New(appFixture(t), schedFixture(t))
	svc.Rig = mosaics.Rig{WidthDeg: 3.32, HeightDeg: 2.22, ScaleArcsec: 1.915}
	h, err := svc.History(ctx, markarian)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(h.Nights, ",") != "2025-12-01,2026-03-01" || len(h.Panels) != 2 {
		t.Fatalf("history %+v", h)
	}
	if fmt.Sprint(h.Panels[0].Hours) != "[2 2]" || fmt.Sprint(h.Panels[1].Hours) != "[0 1]" {
		t.Errorf("hours %v %v", h.Panels[0].Hours, h.Panels[1].Hours)
	}
	d, err := svc.Detail(ctx, markarian)
	if err != nil {
		t.Fatal(err)
	}
	if d.Panels[0].TargetID != 1 || d.Panels[1].TargetID != 2 {
		t.Errorf("target ids %d %d", d.Panels[0].TargetID, d.Panels[1].TargetID)
	}
}

func TestNoiseAndSeamStatus(t *testing.T) {
	t.Parallel()
	p90, at := 2e-4, time.Unix(100, 0)
	ms := []app.Mosaic{{Filter: filterHa, NoiseP90: &p90, NoiseMaxPanel: 3, NoiseTiles: 40, NoiseAt: &at, SeamSignature: "x/2"}, {Filter: "O-III"}}
	seams := []app.MosaicSeam{{Filter: filterHa, PanelA: 1, PanelB: 2, MeasuredAt: at}, {Filter: filterHa, PanelA: 2, PanelB: 3, MeasuredAt: at.Add(time.Hour)}}
	noise, status := mosaicplan.NoiseAndStatus(ms, seams)
	if len(noise) != 2 || noise[0].P90 == nil || *noise[0].P90 != p90 || noise[0].MaxPanel == nil || *noise[0].MaxPanel != 3 || noise[1].MaxPanel != nil || noise[1].P90 != nil {
		t.Errorf("noise %+v", noise)
	}
	if !status[0].Measured || status[0].Pairs != 2 || !status[0].MeasuredAt.Equal(at.Add(time.Hour)) || status[1].Measured || status[1].MeasuredAt != nil {
		t.Errorf("status %+v", status)
	}
}

func TestSeasonBoostsOnlyForBalancedMosaics(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc := mosaicplan.New(appFixture(t), schedFixture(t))
	svc.Site = func(context.Context) (mosaics.Site, bool) { return mosaics.Site{Lat: 32, Lon: -97}, true }
	boosts, err := svc.SeasonBoosts(ctx, time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	d, err := svc.Detail(ctx, markarian)
	if err != nil {
		t.Fatal(err)
	}
	if len(boosts) != len(d.Panels) {
		t.Fatalf("boosts %v for panels %+v", boosts, d.Panels)
	}
	top := 0.0
	for _, p := range d.Panels {
		b, ok := boosts[p.TargetGUID]
		if !ok || b < 0 || b > 1 {
			t.Errorf("panel %d boost %v %v", p.Number, b, ok)
		}
		top = math.Max(top, b)
	}
	if top != 0 && top != 1 {
		t.Errorf("boosts are not scaled to the top panel: %v", boosts)
	}
}
