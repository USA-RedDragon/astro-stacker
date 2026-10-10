package stacking

import (
	"context"
	"math"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

const (
	rhoProject = "Rho"
	p1Typo     = "P1 typo"
)

func schedulerFixture(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE project ("Id" INTEGER PRIMARY KEY, name TEXT, guid TEXT, "isMosaic" INTEGER, minimumaltitude REAL)`,
		`CREATE TABLE target ("Id" INTEGER PRIMARY KEY, name TEXT, active INTEGER, ra REAL, dec REAL, rotation REAL, projectid INTEGER, guid TEXT)`,
		`INSERT INTO project VALUES (1, 'Cygnis Loop', 'p1', 1, 0), (2, 'Rosette', 'p2', 0, 0), (3, 'M31', 'p3', 0, 0), (4, 'Lonely', 'p4', 0, 0), (5, 'Rho', 'p5', 1, 0)`,
		`INSERT INTO target VALUES
			(1, 'Cygnis Loop Panel 2', 1, 20.868, 29.76, 0, 1, 't1'),
			(2, 'Cygnis Loop Panel 1', 1, 20.868, 31.537, 0, 1, 't2'),
			(3, 'Rosette Nebula', 1, 6.53, 4.95, 0, 2, 't3'),
			(4, 'Rosette Nebula SHO', 1, 6.53, 4.95, 0, 2, 't4'),
			(5, 'Andromeda', 1, 0.71, 41.27, 0, 3, 't5'),
			(6, 'Lonely Panel 1', 1, 1, 1, 0, 4, 't6'),
			(7, 'IC 4604 East', 1, 16.4, -24, 0, 5, 't7'),
			(8, 'IC 4604 West', 1, 16.6, -24, 0, 5, 't8')`,
	} {
		if err := db.Exec(q).Error; err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func appFixture(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&app.MosaicPanel{}, &app.FrameTarget{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestMosaicGroupsFromSchedulerProjects(t *testing.T) {
	t.Parallel()
	groups, err := mosaicGroups(context.Background(), appFixture(t), schedulerFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0].Project != "Cygnis Loop" || groups[0].ProjectGUID != "p1" || len(groups[0].Panels) != 2 {
		t.Fatalf("groups %+v", groups)
	}
	first := groups[0].Panels[0]
	if first.Object != "Cygnis Loop Panel 1" || first.Number != 1 || first.TargetGUID != "t2" {
		t.Errorf("panels not in order: %+v", groups[0].Panels)
	}
	if math.Abs(first.RA-313.02) > 1e-9 || first.Dec != 31.537 {
		t.Errorf("panel 1 at %v, %v", first.RA, first.Dec)
	}
}

func TestMosaicGroupsUseAdoptedPanelsAndLinkedObjects(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := appFixture(t)
	if err := db.Create(&[]app.MosaicPanel{
		{ProjectGUID: "p5", TargetGUID: "t8", Target: "old name", Panel: 2, RA: 249, Dec: -24, Footprint: `[{"ra":1,"dec":2},{"ra":3,"dec":4},{"ra":5,"dec":6},{"ra":7,"dec":8}]`},
		{ProjectGUID: "p5", TargetGUID: "t7", Target: "IC 4604 East", Panel: 1, RA: 246, Dec: -24},
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&[]app.FrameTarget{
		{FrameID: 1, Object: "IC4604 East", TargetGUID: "t7", Method: app.LinkHeader},
		{FrameID: 2, Object: "Cygnus Panel 1", TargetGUID: "t2", Method: app.LinkAcquiredImage},
	}).Error; err != nil {
		t.Fatal(err)
	}
	groups, err := mosaicGroups(ctx, db, schedulerFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 {
		t.Fatalf("groups %+v", groups)
	}
	rho := groups[1]
	if rho.Project != rhoProject || len(rho.Panels) != 2 || rho.Panels[0].Object != "IC 4604 East" || rho.Panels[1].Object != "IC 4604 West" {
		t.Fatalf("rho %+v", rho)
	}
	if got := rho.Panels[0].Objects; len(got) != 2 || got[1] != "IC4604 East" {
		t.Errorf("panel 1 objects %v", got)
	}
	if rho.Panels[1].Planned == nil || rho.Panels[0].Planned != nil {
		t.Errorf("planned footprints %+v %+v", rho.Panels[0].Planned, rho.Panels[1].Planned)
	}
	if got := groups[0].Panels[0].Objects; len(got) != 2 || got[1] != "Cygnus Panel 1" {
		t.Errorf("cygnus panel 1 objects %v", got)
	}
	names, err := MosaicPanels(ctx, db, schedulerFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if !names["IC4604 East"] || !names["Cygnis Loop Panel 2"] || names["Andromeda"] {
		t.Errorf("mosaic panels %v", names)
	}
}

func TestPanelMastersPickOneStackPerPanelAndFilter(t *testing.T) {
	t.Parallel()
	g := mosaicGroup{Project: rhoProject, Panels: []panel{
		{Object: "P1", Objects: []string{"P1", p1Typo}, Number: 1},
		{Object: "P2", Objects: []string{"P2"}, Number: 2},
	}}
	stacks := []app.Stack{
		{Object: "P1", Filter: filterHa, EffectiveSeconds: 100},
		{Object: p1Typo, Filter: filterHa, EffectiveSeconds: 300},
		{Object: "P2", Filter: filterHa, EffectiveSeconds: 50},
		{Object: "P1", Filter: "R", EffectiveSeconds: 10},
		{Object: "Other", Filter: "R", EffectiveSeconds: 999},
	}
	got := panelMasters(g, stacks)
	if len(got) != 2 {
		t.Fatalf("filters %v", got)
	}
	ha := got[filterHa]
	if ha.Panels[0].Object != p1Typo || ha.Panels[1].Object != "P2" || g.Panels[0].Object != "P1" {
		t.Errorf("H-a panels %+v", ha.Panels)
	}
	if m := filterMasters(ha, filterHa, stacks); len(m) != 2 {
		t.Errorf("H-a masters %+v", m)
	}
	r := got["R"]
	if r.Panels[0].Object != "P1" || r.Panels[1].Object != "P2" {
		t.Errorf("R panels %+v", r.Panels)
	}
	if m := filterMasters(r, "R", stacks); len(m) != 1 || m[0].Object != "P1" {
		t.Errorf("R masters %+v", m)
	}
}

// A panel's sky plane is found under stars and taken out, keeping the level.
func TestFitSkyPlane(t *testing.T) {
	t.Parallel()
	const w, h = 400, 300
	data := make([]float32, w*h)
	for y := range h {
		for x := range w {
			data[y*w+x] = float32(0.01 + 0.004*(float64(x)/w-0.5) - 0.002*(float64(y)/h-0.5))
		}
	}
	for i := 0; i < len(data); i += 97 {
		data[i] += 0.3 // stars
	}
	a, bx, by, ok := fitSkyPlane(data, w, h, 0.9)
	if !ok || math.Abs(a-0.01) > 1e-5 || math.Abs(bx-0.004) > 1e-5 || math.Abs(by+0.002) > 1e-5 {
		t.Errorf("plane %v %v %v %v, want 0.01 0.004 -0.002", a, bx, by, ok)
	}
}
