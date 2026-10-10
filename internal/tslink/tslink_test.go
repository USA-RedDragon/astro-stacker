package tslink_test

import (
	"context"
	"math"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/tslink"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

const (
	t2    = "t-2"
	t1    = "t-1"
	pRho  = "p-rho"
	light = "LIGHT"
)

func open(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func scheduler(t *testing.T) *gorm.DB {
	t.Helper()
	db := open(t)
	for _, q := range []string{
		`CREATE TABLE project ("Id" INTEGER PRIMARY KEY, name TEXT, guid TEXT, "isMosaic" INTEGER, minimumaltitude REAL)`,
		`CREATE TABLE target ("Id" INTEGER PRIMARY KEY, name TEXT, active INTEGER, ra REAL, dec REAL, rotation REAL, projectid INTEGER, guid TEXT)`,
		`CREATE TABLE acquiredimage ("Id" INTEGER PRIMARY KEY, "targetId" INTEGER, metadata TEXT)`,
		`INSERT INTO project VALUES (1, 'Rho', 'p-rho', 1, 20), (2, 'Rosette', 'p-ros', 0, 0)`,
		`INSERT INTO target VALUES
			(1, 'IC 4604 Panel 1', 1, 16.4, -24.0, 0, 1, 't-1'),
			(2, 'IC 4604 Panel 2', 1, 16.6, -24.0, 0, 1, 't-2'),
			(3, 'Rosette Nebula', 1, 6.53, 4.95, 0, 2, 't-ros'),
			(4, 'Rosette Nebula', 0, 6.53, 4.95, 0, 2, 't-ros2')`,
		`INSERT INTO acquiredimage VALUES
			(10, 2, '{"FileName":"C:\\Images\\2026-10-01\\IC 4604 Panel 2_0001.fits"}'),
			(11, 1, 'not json')`,
	} {
		if err := db.Exec(q).Error; err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func appDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := open(t)
	if err := db.AutoMigrate(&app.Frame{}, &app.FrameTarget{}); err != nil {
		t.Fatal(err)
	}
	hdr := t2
	proj := pRho
	frames := []app.Frame{
		{ID: 1, Key: "a/IC 4604 Panel 2_0001.fits", ETag: "e", Type: light, Object: "IC 4604 Panel 2"},
		{ID: 2, Key: "a/typo.fits", ETag: "e", Type: light, Object: "IC4604 Panel 2", TSTarget: &hdr, TSProject: &proj},
		{ID: 3, Key: "a/p1.fits", ETag: "e", Type: light, Object: "IC 4604 Panel 1"},
		{ID: 4, Key: "a/ros.fits", ETag: "e", Type: light, Object: "Rosette Nebula"},
		{ID: 5, Key: "a/flat.fits", ETag: "e", Type: "FLAT", Object: "IC 4604 Panel 1"},
		{ID: 6, Key: "a/dolphin.fits", ETag: "e", Type: light, Object: "Dolphin Head"},
	}
	if err := db.Create(&frames).Error; err != nil {
		t.Fatal(err)
	}
	return db
}

func TestTargetsReadDegreesAndGuids(t *testing.T) {
	t.Parallel()
	targets, err := tslink.Targets(context.Background(), scheduler(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 4 {
		t.Fatalf("%d targets", len(targets))
	}
	got := targets[0]
	if got.GUID != t1 || got.ProjectGUID != pRho || !got.IsMosaic || math.Abs(got.RA-246) > 1e-9 || got.MinAltitude != 20 {
		t.Errorf("target %+v", got)
	}
	if targets[3].Active {
		t.Error("inactive target read as active")
	}
	projects := tslink.GroupProjects(targets)
	if len(projects) != 2 || projects[0].Name != "Rho" || len(projects[0].Targets) != 2 || len(projects[1].Targets) != 2 {
		t.Errorf("projects %+v", projects)
	}
}

func TestLinkPrefersHeaderThenAcquiredImageThenUniqueName(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, sched := appDB(t), scheduler(t)
	var l tslink.Linker
	st, err := l.Link(ctx, db, sched)
	if err != nil {
		t.Fatal(err)
	}
	if st != (tslink.Stats{Header: 1, AcquiredImage: 1, Name: 1, Unlinked: 2}) {
		t.Errorf("stats %+v", st)
	}
	var links []app.FrameTarget
	if err := db.Order("frame_id").Find(&links).Error; err != nil {
		t.Fatal(err)
	}
	want := map[int][2]string{1: {t2, app.LinkAcquiredImage}, 2: {t2, app.LinkHeader}, 3: {t1, app.LinkName}}
	if len(links) != len(want) {
		t.Fatalf("links %+v", links)
	}
	for _, k := range links {
		if w := want[k.FrameID]; w[0] != k.TargetGUID || w[1] != k.Method || k.ProjectGUID != pRho {
			t.Errorf("frame %d linked %+v, want %v", k.FrameID, k, w)
		}
	}
	st, err = l.Link(ctx, db, sched)
	if err != nil {
		t.Fatal(err)
	}
	if st != (tslink.Stats{Unlinked: 2}) {
		t.Errorf("second run %+v", st)
	}

	targets, _ := tslink.Targets(ctx, sched)
	objs, err := tslink.ObjectTargets(ctx, db, targets)
	if err != nil {
		t.Fatal(err)
	}
	if objs["IC4604 Panel 2"] != t2 || objs["IC 4604 Panel 2"] != t2 || objs["IC 4604 Panel 1"] != t1 {
		t.Errorf("object targets %v", objs)
	}
	if _, ok := objs["Rosette Nebula"]; ok {
		t.Error("an ambiguous name was linked")
	}
	if got := tslink.TargetObjects(objs)[t2]; len(got) != 2 || got[0] != "IC 4604 Panel 2" {
		t.Errorf("t-2 objects %v", got)
	}
}

func TestFileBase(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		`{"FileName":"C:\\a\\b.fits"}`: "b.fits",
		`{"FileName":"/x/y.xisf"}`:     "y.xisf",
		`{}`:                           "",
		`nope`:                         "",
	} {
		if got := tslink.FileBase(in); got != want {
			t.Errorf("tslink.FileBase(%s) = %q, want %q", in, got, want)
		}
	}
}
