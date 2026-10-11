package mosaicplan_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/mosaicplan"
	"github.com/USA-RedDragon/astro-stacker/internal/mosaics"
	"github.com/USA-RedDragon/astro-stacker/internal/mosaicstore"
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
	icPanel1  = "IC 4604 Panel 1"
	filterHa  = "H-a"
	filterRed = "Red"
	filterLum = "Luminance"
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
	return appFixtureOn(t, open(t, "app"))
}

func appFixtureOn(t *testing.T, db *gorm.DB) *gorm.DB {
	t.Helper()
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

func addUnmatchedSubs(t *testing.T, db *gorm.DB) {
	t.Helper()
	pt := func(v float64) *float64 { return &v }
	frames := []app.Frame{
		{ID: 10, Key: "mk1", ETag: "e", Type: light, Object: markarian, MountRA: pt(186.75 + 0.3/math.Cos(13*math.Pi/180)), MountDec: pt(13)},
		{ID: 11, Key: "mk2", ETag: "e", Type: light, Object: markarian, MountRA: pt(186.75 + 0.3/math.Cos(13*math.Pi/180)), MountDec: pt(13)},
		{ID: 12, Key: "sh1", ETag: "e", Type: light, Object: "Sh2-1", MountRA: pt(246), MountDec: pt(-24.3)},
		{ID: 13, Key: "ros", ETag: "e", Type: light, Object: "NGC 2244", MountRA: pt(97.95), MountDec: pt(5.2)},
	}
	if err := db.Create(&frames).Error; err != nil {
		t.Fatal(err)
	}
}

func TestRunAdoptionDryRunWritesNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	appDB := appFixture(t)
	addUnmatchedSubs(t, appDB)
	svc := mosaicplan.New(appDB, schedFixture(t))
	svc.Rig = mosaics.Rig{WidthDeg: 3.32, HeightDeg: 2.22, ScaleArcsec: 1.915}
	rep, err := svc.RunAdoption(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.DryRun || rep.Clean != 1 || rep.Review != 4 || rep.Auto != 3 || rep.New != 7 {
		t.Errorf("report %+v", rep)
	}
	for _, m := range []any{&app.MosaicAdoption{}, &app.MosaicPanel{}, &app.FrameTarget{}} {
		var n int64
		appDB.Model(m).Count(&n)
		if n != 0 {
			t.Errorf("dry run wrote %d %T", n, m)
		}
	}
}

func adoptionsBySubject(t *testing.T, items []mosaicplan.Adoption) map[string]mosaicplan.Adoption {
	t.Helper()
	out := map[string]mosaicplan.Adoption{}
	for _, it := range items {
		key := it.Project
		if it.Kind == mosaicplan.KindFrames {
			key = "subs " + key
		}
		out[key] = it
	}
	return out
}

func TestRunAdoptionDecidesWhatTheEvidenceSettles(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	appDB := appFixture(t)
	addUnmatchedSubs(t, appDB)
	svc := mosaicplan.New(appDB, schedFixture(t))
	svc.Rig = mosaics.Rig{WidthDeg: 3.32, HeightDeg: 2.22, ScaleArcsec: 1.915}
	rep, err := svc.RunAdoption(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	by := adoptionsBySubject(t, rep.Items)
	want := map[string]string{
		markarian: app.AdoptionAuto, "subs " + markarian: app.AdoptionAuto, "subs Dolphin Head": app.AdoptionAuto, "Rho": app.AdoptionProposed,
		"Rosette": app.AdoptionProposed, "subs Sh2-1": app.AdoptionProposed, "subs NGC 2244": app.AdoptionProposed,
	}
	for project, status := range want {
		if a := by[project]; a.Status != status {
			t.Errorf("%s is %s, want %s", project, a.Status, status)
		}
	}
	if by[markarian].Kind != mosaics.KindMosaic || by[markarian].DecidedBy != mosaicplan.AutoDecider || by[markarian].DecidedAt == nil {
		t.Errorf("markarian mosaic %+v", by[markarian])
	}
	if by["subs NGC 2244"].Frames.Within != 2 || !strings.HasPrefix(by["subs NGC 2244"].Rule, "Medium: 2 targets") {
		t.Errorf("two candidates %+v %+v", by["subs NGC 2244"], by["subs NGC 2244"].Frames)
	}
	if f := by["subs Sh2-1"].Frames; f.Within != 1 || f.NameMatch || f.Target != icPanel1 {
		t.Errorf("name mismatch %+v", f)
	}
	var panels []app.MosaicPanel
	appDB.Order("panel").Find(&panels)
	if len(panels) != 2 || panels[0].ProjectGUID != "pm" || panels[0].Source != app.MosaicSourceAdopted || panels[1].Neighbours != `["m1"]` {
		t.Fatalf("panels %+v", panels)
	}
	var links []app.FrameTarget
	appDB.Order("frame_id").Find(&links)
	if len(links) != 2 || links[0].FrameID != 10 || links[0].TargetGUID != "m1" || links[0].Method != mosaicplan.LinkReview {
		t.Fatalf("links %+v", links)
	}
	checkAdoptionRules(t, by)

	again, err := svc.RunAdoption(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if again.Auto != 0 || again.Review != 4 || again.New != 0 {
		t.Errorf("second run %+v", again)
	}
	checkUndoSticks(ctx, t, svc, appDB, by)
}

func checkUndoSticks(ctx context.Context, t *testing.T, svc *mosaicplan.Service, appDB *gorm.DB, by map[string]mosaicplan.Adoption) {
	t.Helper()
	undo := func(id int) {
		if _, err := mosaicstore.SetStatus(ctx, appDB, id, app.AdoptionAuto, app.AdoptionProposed, "web", svc.Rig); err != nil {
			t.Fatal(err)
		}
	}
	undo(by[markarian].ID)
	undo(by["subs Dolphin Head"].ID)
	undo(by["subs "+markarian].ID)
	for _, m := range []any{&app.MosaicPanel{}, &app.FrameTarget{}} {
		var n int64
		appDB.Model(m).Count(&n)
		if n != 0 {
			t.Errorf("%d %T left after undoing", n, m)
		}
	}
	rep, err := svc.RunAdoption(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Auto != 0 || rep.Review != 7 {
		t.Errorf("run after undo %+v", rep)
	}
	var row app.MosaicAdoption
	appDB.First(&row, by[markarian].ID)
	if row.Status != app.AdoptionProposed || row.DecidedBy != "web" {
		t.Errorf("undone row %+v", row)
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
	if rep.Clean != 1 || rep.Review != 2 || rep.Auto != 2 || rep.Changed != 0 {
		t.Fatalf("report %+v", rep)
	}
	byKind := adoptionsBySubject(t, rep.Items)
	if a := byKind["Rho"]; a.Status != app.AdoptionProposed || a.Confidence != mosaics.ConfidenceHigh {
		t.Errorf("rho %+v", a)
	}
	if a := byKind["Rosette"]; a.Kind != mosaics.KindNotMosaic {
		t.Errorf("rosette %+v", a)
	}
	dol := byKind["subs Dolphin Head"]
	if err := appDB.Model(&app.MosaicAdoption{}).Where("id = ?", dol.ID).Updates(map[string]any{"issue": "stale", "rule": ""}).Error; err != nil {
		t.Fatal(err)
	}
	again, err := svc.RunAdoption(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if again.Unchanged != 4 || again.New != 0 || again.Review != 2 || again.Auto != 0 {
		t.Errorf("second run %+v", again)
	}

	var refreshed app.MosaicAdoption
	appDB.First(&refreshed, dol.ID)
	if refreshed.Issue != dol.Issue || refreshed.Rule != dol.Rule || refreshed.Status != dol.Status {
		t.Errorf("stale wording kept: %+v", refreshed)
	}

	checkDecisions(ctx, t, svc, appDB, byKind["Rho"].ID)
}

func checkAdoptionRules(t *testing.T, byKind map[string]mosaicplan.Adoption) {
	t.Helper()
	dol := byKind["subs Dolphin Head"]
	if dol.Kind != mosaicplan.KindFrames || dol.Frames == nil || dol.Frames.Count != 1 || dol.Confidence != mosaics.ConfidenceLow {
		t.Fatalf("dolphin %+v %+v", dol, dol.Frames)
	}
	if !strings.HasPrefix(dol.Rule, "Low: no target within") || !dol.Frames.Coords || dol.Frames.Separation == nil || dol.Frames.Nearest == "" || dol.Frames.Target != "" {
		t.Errorf("dolphin rule %q frames %+v", dol.Rule, dol.Frames)
	}
	if !strings.HasPrefix(byKind["Rho"].Rule, "High: ") || byKind["Rho"].FoundAt == nil {
		t.Errorf("rho %+v", byKind["Rho"])
	}
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
	rep, err := svc.RunAdoption(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if by := adoptionsBySubject(t, rep.Items); by["Rho"].Status != app.AdoptionAccepted || by["Rho"].DecidedBy != "test" {
		t.Errorf("run changed a person's decision %+v", by["Rho"])
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
	if !d.Adopted || len(d.Panels) != 2 || d.Rows != 1 || d.Cols != 2 || !d.Balancing.On || d.Balancing.PanelDeficit != 75 || !d.Balancing.PanelDeficitSet || d.Balancing.OnWeight != mosaics.PanelDeficitOnWeight {
		t.Fatalf("detail %+v", d)
	}
	if !d.RigKnown || d.Layout == nil || *d.Layout != "2 × 1 at 0°" || d.Panels[1].Col == nil {
		t.Errorf("layout %v %+v", d.Layout, d.Panels[1])
	}
	if strings.Join(d.Filters, ",") != "Red,H-a" {
		t.Errorf("filters %v", d.Filters)
	}
	checkPanels(t, d)
	checkLeast(t, d)
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
	if p1.Progress == nil || *p1.Progress != 0.25 || p1.Weakest != filterRed || p1.PastGoal {
		t.Errorf("panel 1 %+v", p1)
	}
	p2ha := p2.Filters[1]
	if p2ha.Desired != 40 || p2ha.Accepted != 20 || math.Abs(p2ha.PlannedHours-40*300.0/3600) > 1e-9 || math.Abs(p2ha.HoursNeeded-p2ha.PlannedHours/2) > 1e-9 {
		t.Errorf("panel 2 H-a %+v", p2ha)
	}
	if h := d.HoursLeft; h.EffectiveFilters != 1 || math.Abs(h.Effective-12) > 1e-9 || h.RawFilters != 2 || math.Abs(h.Raw-(3+40*300.0/3600/2)) > 1e-9 || h.UnknownFilters != 0 {
		t.Errorf("hours left %+v", h)
	}
	if red.HoursBasis != mosaicplan.HoursEffective || p2ha.HoursBasis != mosaicplan.HoursRaw {
		t.Errorf("hours basis %q %q", red.HoursBasis, p2ha.HoursBasis)
	}
	if len(d.Needs) != 2 || !strings.Contains(d.Needs[0], "Panel 2 needs about +12.0 h effective H-a") || !strings.Contains(d.Needs[1], "NE corner") {
		t.Errorf("needs %v", d.Needs)
	}
}

func checkLeast(t *testing.T, d mosaicplan.Detail) {
	t.Helper()
	p2 := d.Panels[1]
	if p2ha := p2.Filters[1]; p2.Progress != nil || p2.Weakest != filterRed || p2ha.Measured || p2ha.NotMeasured != mosaicplan.NoGoalMeasurement {
		t.Errorf("panel 2 on counts only %+v", p2)
	}
	if d.Complete != nil || d.Average != nil || d.WeakestPanel != 2 || d.WeakestFilter != filterRed || d.NotMeasured != mosaicplan.NoGoalMeasurement {
		t.Errorf("complete %v panel %d filter %s %q", d.Complete, d.WeakestPanel, d.WeakestFilter, d.NotMeasured)
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
	if plan.HoursPerSeason != nil || !plan.Basis.InsufficientHistory || plan.Basis.Reason == nil || len(plan.Rows) != 0 || plan.Pace != nil || plan.Projection != nil {
		t.Fatalf("a rate was invented from two nights: %+v", plan)
	}
	if !plan.SiteKnown || len(plan.Months) != 12 || len(plan.PanelPriority) != 2 || len(plan.Basis.ImagingNightsPerMonth) != 12 {
		t.Fatalf("plan %+v", plan)
	}
	if plan.Months[3].Hours < 3 || plan.Months[8].Hours > 1 {
		t.Errorf("Markarian's dark hours by month %+v", plan.Months)
	}
	if r := plan.EffectivePerRaw; r == nil || r.Value != 0.75 || r.Subs != 2 || r.Scope != mosaicplan.RatioProject {
		t.Errorf("effective per raw %+v", plan.EffectivePerRaw)
	}
	if plan.Last == nil || plan.Last.Nights != 2 || math.Abs(plan.Last.Hours-3) > 1e-9 {
		t.Errorf("last season %+v", plan.Last)
	}
	checkBestSeason(ctx, t, svc, now)
	checkMeasuredSeasons(t, svc, appDB, now)
}

func checkBestSeason(ctx context.Context, t *testing.T, svc *mosaicplan.Service, now time.Time) {
	t.Helper()
	if _, err := svc.Seasons(ctx, markarian, "bogus", "", now); !errors.Is(err, mosaicplan.ErrBadRequest) {
		t.Errorf("unknown strategy: %v", err)
	}
	if _, err := svc.Seasons(ctx, markarian, "", "bogus", now); !errors.Is(err, mosaicplan.ErrBadRequest) {
		t.Errorf("unknown pace: %v", err)
	}
	best, err := svc.Seasons(ctx, markarian, "", "good", now)
	if err != nil {
		t.Fatal(err)
	}
	if best.Strategy != "weakest" || best.StrategyInForce != "weakest" || best.Pace == nil || *best.Pace != mosaicplan.PaceBest || best.HoursPerSeason == nil || *best.HoursPerSeason != 3 || len(best.Rows) == 0 {
		t.Errorf("best season %+v", best)
	}
	if pr := best.Projection; pr == nil || pr.HoursPerSeason != 3 || pr.Pace != mosaicplan.PaceBest || pr.Items == 0 || pr.StepHours != mosaics.StepHours || pr.EffectivePerRaw == nil {
		t.Errorf("projection inputs %+v", best.Projection)
	}
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
	if b.InsufficientHistory || b.HoursPerImagingNight == nil || b.ImagingNightsPerSeason == nil || measured.HoursPerSeason == nil {
		t.Fatalf("measured basis %+v", b)
	}
	if b.ProjectNights != 10 || b.HistoryNights != 36 || len(b.UsableMonths) == 0 {
		t.Errorf("basis counts %+v", b)
	}
	if want := math.Round(*b.HoursPerImagingNight**b.ImagingNightsPerSeason*10) / 10; *measured.HoursPerSeason != want {
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
	rot, minor, pa := 0.0, 120.0, 90.0
	req := mosaicplan.FramingRequest{RA: 313, Dec: 44, MajorArcmin: 240, MinorArcmin: &minor, PA: &pa, Rotation: &rot, Rows: 2, Cols: 3, HoursPerPanel: 10}
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

func TestEstimatorMatchesTheFramingCost(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	appDB := appFixture(t)
	addNights(t, appDB, "Elsewhere", time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC), 10, 4*3600)
	svc := mosaicplan.New(appDB, schedFixture(t))
	svc.Rig = mosaics.Rig{WidthDeg: 3.32, HeightDeg: 2.22, ScaleArcsec: 1.915}
	rot := 0.0
	f, err := svc.Frame(ctx, mosaicplan.FramingRequest{RA: 313, Dec: 44, MajorArcmin: 240, Rotation: &rot, Rows: 2, Cols: 3}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	est, err := svc.Estimator(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c := est(6, [12]float64{})
	if f.Chosen == nil || c.Hours == nil || f.Chosen.Hours == nil || *c.Hours != *f.Chosen.Hours || c.Nights == nil || *c.Nights != *f.Chosen.Nights || c.Text != f.Chosen.Cost {
		t.Errorf("estimator %+v, framing %+v", c, f.Chosen)
	}
	march := [12]float64{2: 6}
	if c := est(6, march); c.Seasons == nil || !strings.Contains(c.Text, "season") {
		t.Errorf("with a usable March %+v", c)
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

func TestNeedsSmallHoursAndUnknownGapLocation(t *testing.T) {
	t.Parallel()
	d := mosaicplan.Detail{
		Panels: []mosaicplan.Panel{{Number: 1, Filters: []mosaicplan.PanelFilter{{Filter: filterLum, EffectiveHours: 0.0006}}}, {Number: 2}},
		Seams:  []app.MosaicSeam{{Filter: filterLum, PanelA: 1, PanelB: 2, NoiseA: 8.3, NoiseB: 1, NoiseRatio: 8.3}},
		Health: []app.MosaicPanelHealth{{Filter: filterLum, Panel: 2, GapFraction: 0.05, GapDeg2: 0.2}},
	}
	got := mosaicplan.Needs(d)
	if len(got) != 2 || !strings.Contains(got[0], "+2 min effective Luminance") || !strings.Contains(got[1], "location not measured") {
		t.Errorf("needs %q", got)
	}
}

func TestNoiseAndSeamStatus(t *testing.T) {
	t.Parallel()
	p90, at := 2e-4, time.Unix(100, 0)
	ms := []app.Mosaic{{Filter: filterHa, NoiseP90: &p90, NoiseMaxPanel: 3, NoiseTiles: 40, NoiseAt: &at, SeamSignature: "x/2"}, {Filter: filterOIII}}
	seams := []app.MosaicSeam{{Filter: filterHa, PanelA: 1, PanelB: 2, MeasuredAt: at}, {Filter: filterHa, PanelA: 2, PanelB: 3, MeasuredAt: at.Add(time.Hour)}}
	noise, status := mosaicplan.NoiseAndStatus(ms, seams)
	if len(noise) != 2 || noise[0].P90 == nil || *noise[0].P90 != p90 || noise[0].MaxPanel == nil || *noise[0].MaxPanel != 3 || noise[1].MaxPanel != nil || noise[1].P90 != nil {
		t.Errorf("noise %+v", noise)
	}
	if !status[0].Measured || status[0].Pairs != 2 || !status[0].MeasuredAt.Equal(at.Add(time.Hour)) || status[1].Measured || status[1].MeasuredAt != nil {
		t.Errorf("status %+v", status)
	}
}

func TestSeasonBoostsCoverEveryActiveTarget(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sched := schedFixture(t)
	for _, q := range []string{
		`CREATE TABLE ts_target_season (target_guid TEXT PRIMARY KEY, nights_left INTEGER, out_of_season INTEGER, season_end TEXT, computed_for TEXT, computed_at TEXT)`,
		`INSERT INTO ts_target_season VALUES ('m1', 60, 0, '2026-05-30', '2026-04-01', NULL), ('m2', 0, 1, NULL, '2026-04-01', NULL), ('ros1', 20, 0, '2026-04-20', '2026-04-01', NULL)`,
		`INSERT INTO exposureplan VALUES (6, 20, 1, 300, 30, 0, 1)`,
	} {
		if err := sched.Exec(q).Error; err != nil {
			t.Fatal(q, err)
		}
	}
	svc := mosaicplan.New(appFixture(t), sched)
	now := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	if boosts, err := svc.SeasonBoosts(ctx, now); err != nil || len(boosts) != 0 {
		t.Fatalf("no site should publish no boosts: %v %v", boosts, err)
	}
	svc.Site = func(context.Context) (mosaics.Site, bool) { return mosaics.Site{Lat: 32, Lon: -97}, true }
	boosts, err := svc.SeasonBoosts(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, guid := range []string{"m1", "m2", "r1", "r2", "ros1", "ros2"} {
		b, ok := boosts[guid]
		if !ok || b < 0 || b > 1 {
			t.Errorf("%s boost %v %v", guid, b, ok)
		}
	}
	if boosts["m1"] <= 0 || boosts["ros1"] <= 0 {
		t.Errorf("in-season mosaic panels and single targets with work left get a boost: %v", boosts)
	}
	if boosts["m2"] != 0 || boosts["r1"] != 0 || boosts["ros2"] != 0 {
		t.Errorf("out of season, no season row, or no work left gets 0: %v", boosts)
	}
	if boosts["m1"] == 1 && boosts["ros1"] == 1 {
		t.Errorf("boosts are not scaled to a top target: %v", boosts)
	}
}

func setKit(t *testing.T, db *gorm.DB, object, camera, telescope string, pixel, focal float64) {
	t.Helper()
	if err := db.Model(&app.Frame{}).Where("object = ?", object).
		Updates(map[string]any{"camera": camera, "telescope": telescope, "pixel_size": pixel, "focal_length": focal}).Error; err != nil {
		t.Fatal(err)
	}
}

func TestRunAdoptionKeepsOtherInstrumentsApartAndWithdrawsItsOwnAttachments(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	appDB := appFixture(t)
	addUnmatchedSubs(t, appDB)
	pt := func(v float64) *float64 { return &v }
	extra := []app.Frame{
		{ID: 20, Key: "own", ETag: "e", Type: light, Object: panel1},
		{ID: 21, Key: "bat", ETag: "e", Type: light, Object: "Flying Bat", MountRA: pt(183.85), MountDec: pt(13.1)},
	}
	if err := appDB.Create(&extra).Error; err != nil {
		t.Fatal(err)
	}
	setKit(t, appDB, panel1, "ZWO ASI2600MM Pro", "Apertura 75Q", 3.76, 405)
	setKit(t, appDB, "Flying Bat", "FLI PL16083", "SPA-3", 9, 530)
	if err := appDB.Create(&app.FrameTarget{FrameID: 20, Object: panel1, TargetGUID: "m1", ProjectGUID: "pm", Method: app.LinkHeader}).Error; err != nil {
		t.Fatal(err)
	}
	svc := mosaicplan.New(appDB, schedFixture(t))
	svc.Rig = mosaics.Rig{WidthDeg: 3.32, HeightDeg: 2.22, ScaleArcsec: 1.915}
	first, err := svc.RunAdoption(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	by := adoptionsBySubject(t, first.Items)
	if a := by["subs "+markarian]; a.Status != app.AdoptionAuto || a.Frames.Target != panel1 {
		t.Fatalf("subs without instrument headers not attached %+v %+v", a, a.Frames)
	}
	if a := by["subs Flying Bat"]; a.Status != app.AdoptionAuto || a.Frames.Target != "" || a.Confidence != mosaics.ConfidenceLow ||
		!strings.Contains(a.Suggestion, "FLI PL16083 on SPA-3") || !strings.Contains(a.Suggestion, panel2) {
		t.Fatalf("other instrument near a target %+v %+v", a, a.Frames)
	}
	if _, err := mosaicstore.SetStatus(ctx, appDB, by["subs Sh2-1"].ID, app.AdoptionProposed, app.AdoptionAccepted, "web", svc.Rig); err != nil {
		t.Fatal(err)
	}

	setKit(t, appDB, markarian, "FLI ProLine PL9000", "CHI-1", 12, 2280)
	setKit(t, appDB, "Sh2-1", "FLI", "", 9, 0)
	again, err := svc.RunAdoption(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	by = adoptionsBySubject(t, again.Items)
	mk := by["subs "+markarian]
	if mk.Status != app.AdoptionAuto || mk.DecidedBy != mosaicplan.AutoDecider || mk.Frames.Target != "" || mk.Frames.Count != 2 ||
		mk.Frames.Instrument != "FLI ProLine PL9000 on CHI-1" || mk.Frames.TargetRig != "ZWO ASI2600MM Pro on Apertura 75Q" ||
		!strings.Contains(mk.Suggestion, panel1) {
		t.Fatalf("automatic attachment not withdrawn %+v %+v", mk, mk.Frames)
	}
	var n int64
	appDB.Model(&app.FrameTarget{}).Where("object = ?", markarian).Count(&n)
	if n != 0 {
		t.Errorf("%d links left after withdrawing", n)
	}
	if a := by["subs Sh2-1"]; a.Status != app.AdoptionAccepted || a.DecidedBy != "web" || a.Confidence != mosaics.ConfidenceMedium || a.Frames.Target != icPanel1 {
		t.Errorf("a person's decision changed %+v %+v", a, a.Frames)
	}
	appDB.Model(&app.FrameTarget{}).Where("object = ? AND target_guid = ?", "Sh2-1", "r1").Count(&n)
	if n != 1 {
		t.Errorf("a person's attachment lost: %d links", n)
	}
	third, err := svc.RunAdoption(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if third.Auto != 0 || third.Changed != 0 {
		t.Errorf("third run not settled %+v", third)
	}
}

func TestRunAdoptionSettlesSubsTargetSchedulerLinkedSince(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	appDB := appFixture(t)
	addUnmatchedSubs(t, appDB)
	svc := mosaicplan.New(appDB, schedFixture(t))
	svc.Rig = mosaics.Rig{WidthDeg: 3.32, HeightDeg: 2.22, ScaleArcsec: 1.915}
	first, err := svc.RunAdoption(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	sh := adoptionsBySubject(t, first.Items)["subs Sh2-1"]
	if sh.Status != app.AdoptionProposed {
		t.Fatalf("sh2-1 %+v", sh)
	}
	if err := appDB.Create(&app.FrameTarget{FrameID: 12, Object: "Sh2-1", TargetGUID: "r1", ProjectGUID: "pr", Method: app.LinkAcquiredImage}).Error; err != nil {
		t.Fatal(err)
	}
	again, err := svc.RunAdoption(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	a := adoptionsBySubject(t, again.Items)["subs Sh2-1"]
	if a.Status != app.AdoptionAuto || a.DecidedBy != mosaicplan.AutoDecider || a.Frames == nil || !a.Frames.LinkedByTS || a.Frames.Target != "" ||
		!strings.HasPrefix(a.Suggestion, "Nothing to do") || again.Review != 3 {
		t.Fatalf("moot row %+v %+v report %+v", a, a.Frames, again)
	}
	third, err := svc.RunAdoption(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if third.Auto != 0 {
		t.Errorf("third run %+v", third)
	}
}

func TestRunAdoptionClosesRowsForRemovedOrPlannedProjects(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	appDB := appFixture(t)
	sched := schedFixture(t)
	svc := mosaicplan.New(appDB, sched)
	svc.Rig = mosaics.Rig{WidthDeg: 3.32, HeightDeg: 2.22, ScaleArcsec: 1.915}
	first, err := svc.RunAdoption(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	by := adoptionsBySubject(t, first.Items)
	if by["Rosette"].Status != app.AdoptionProposed || by["Rho"].Status != app.AdoptionProposed {
		t.Fatalf("open rows %+v %+v", by["Rosette"], by["Rho"])
	}
	for _, q := range []string{`DELETE FROM target WHERE projectid = 3`, `DELETE FROM project WHERE "Id" = 3`} {
		if err := sched.Exec(q).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := appDB.Create(&app.MosaicPanel{ProjectGUID: "pr", TargetGUID: "r1", Project: "Rho", Target: icPanel1, Panel: 1, Source: app.MosaicSourceWizard}).Error; err != nil {
		t.Fatal(err)
	}
	again, err := svc.RunAdoption(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	by = adoptionsBySubject(t, again.Items)
	if a := by["Rosette"]; a.Status != app.AdoptionAuto || a.DecidedBy != mosaicplan.AutoDecider || !strings.Contains(a.Suggestion, "removed from Target Scheduler") {
		t.Errorf("removed project %+v", a)
	}
	if a := by["Rho"]; a.Status != app.AdoptionAuto || !strings.Contains(a.Suggestion, "planned in Add target") {
		t.Errorf("planned project %+v", a)
	}
	if again.Review != 0 {
		t.Errorf("report %+v", again)
	}
}

func TestListLoadsGoalsOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sched := schedFixture(t)
	var targetLoads atomic.Int64
	if err := sched.Callback().Row().After("gorm:row").Register("count:targets", func(db *gorm.DB) {
		if strings.Contains(db.Statement.SQL.String(), "JOIN project ON target.projectid") {
			targetLoads.Add(1)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := sched.Callback().Query().After("gorm:query").Register("count:targets", func(db *gorm.DB) {
		if strings.Contains(db.Statement.SQL.String(), "JOIN project ON target.projectid") {
			targetLoads.Add(1)
		}
	}); err != nil {
		t.Fatal(err)
	}
	svc := mosaicplan.New(appFixture(t), sched)
	svc.Rig = mosaics.Rig{WidthDeg: 3.32, HeightDeg: 2.22, ScaleArcsec: 1.915}
	list, err := svc.List(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("list %+v %v", list, err)
	}
	if n := targetLoads.Load(); n > 2 {
		t.Errorf("List of %d mosaics loaded Target Scheduler's targets %d times", len(list), n)
	}
}
