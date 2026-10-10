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
	rep, err := svc.RunAdoption(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.DryRun || rep.Auto != 1 || rep.Review != 3 {
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
	rep, err := svc.RunAdoption(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Auto != 1 || rep.Review != 3 || rep.Changed != 0 {
		t.Fatalf("report %+v", rep)
	}
	byKind := map[string]mosaicplan.Adoption{}
	for _, it := range rep.Items {
		byKind[it.Project] = it
	}
	if a := byKind[markarian]; a.Status != app.AdoptionAuto || a.Action != "adopt" || len(a.Panels) != 2 {
		t.Errorf("markarian %+v", a)
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
	if again.Unchanged != 4 || again.Auto != 0 || again.Review != 3 {
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
	if len(list) != 4 || list[0].Status != app.AdoptionProposed || list[3].Status != app.AdoptionAuto {
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
	if _, err := svc.RunAdoption(ctx, false); err != nil {
		t.Fatal(err)
	}
	d, err := svc.Detail(ctx, "pm")
	if err != nil {
		t.Fatal(err)
	}
	if !d.Adopted || len(d.Panels) != 2 || d.Rows != 1 || d.Cols != 2 || !d.Balancing.On || d.Balancing.PanelDeficit != 75 {
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

func TestSeasonsPlanWithSite(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc := mosaicplan.New(appFixture(t), schedFixture(t))
	svc.Site = func(context.Context) (mosaics.Site, bool) { return mosaics.Site{Lat: 32, Lon: -97}, true }
	now := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	plan, err := svc.Seasons(ctx, markarian, "weakest", "good", now)
	if err != nil {
		t.Fatal(err)
	}
	if plan.HoursPerSeason != 60 || plan.Strategy != "weakest" || !plan.SiteKnown || len(plan.Months) != 12 {
		t.Fatalf("plan %+v", plan)
	}
	if plan.Months[3].Hours < 3 || plan.Months[8].Hours > 1 {
		t.Errorf("Markarian's dark hours by month %+v", plan.Months)
	}
	if plan.Last == nil || plan.Last.Nights != 2 || math.Abs(plan.Last.Hours-3) > 1e-9 {
		t.Errorf("last season %+v", plan.Last)
	}
	if plan.FinishSeason != 1 || len(plan.Rows) != 1 || !plan.Rows[0].Done {
		t.Errorf("rows %+v finish %d", plan.Rows, plan.FinishSeason)
	}
	if plan.Compare["weakest"] == 0 || len(plan.PanelPriority) != 2 {
		t.Errorf("compare %+v priority %+v", plan.Compare, plan.PanelPriority)
	}
	last, err := svc.Seasons(ctx, markarian, "bogus", "", now)
	if err != nil {
		t.Fatal(err)
	}
	if last.Strategy != "weakest" || last.Pace != mosaicplan.PaceLast || last.HoursPerSeason != 20 {
		t.Errorf("defaults %+v", last)
	}
}

func TestFrameOffersLayoutsAndAChosenGrid(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc := mosaicplan.New(appFixture(t), schedFixture(t))
	rot := 0.0
	f, err := svc.Frame(ctx, mosaicplan.FramingRequest{RA: 313, Dec: 44, MajorArcmin: 240, MinorArcmin: 120, PA: 90, Rotation: &rot, Rows: 2, Cols: 3, HoursPerPanel: 10}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Options) == 0 || !f.Options[0].Recommended || f.Overlap != 15 || f.Rig.WidthDeg != 3.32 {
		t.Fatalf("framing %+v", f)
	}
	if f.Chosen == nil || len(f.Chosen.Panels) != 6 || f.Chosen.Hours != 60 || f.Chosen.Nights != 22 || f.Chosen.Seasons != 1 {
		t.Errorf("chosen %+v", f.Chosen)
	}
	if _, err := svc.Frame(ctx, mosaicplan.FramingRequest{Dec: 91}, time.Now()); err == nil {
		t.Error("bad dec accepted")
	}
}
