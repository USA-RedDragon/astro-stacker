package goals

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

const (
	CreateGoalSQLite = `CREATE TABLE ts_goal (
    target_guid TEXT NOT NULL,
    filter TEXT NOT NULL,
    kind INTEGER NOT NULL DEFAULT 0,
    snr_goal REAL,
    depth_goal REAL,
    plateau_stop INTEGER NOT NULL DEFAULT 1,
    region TEXT,
    updated_at TIMESTAMP,
    PRIMARY KEY (target_guid, filter)
)`
	CreateProgressSQLite = `CREATE TABLE ts_goal_progress (
    target_guid TEXT NOT NULL,
    filter TEXT NOT NULL,
    kind INTEGER NOT NULL,
    goal_value REAL,
    achieved_value REAL,
    progress REAL,
    snr REAL,
    depth REAL,
    effective_hours REAL,
    hours_needed REAL,
    gain_per_hour_pct REAL,
    plateau INTEGER NOT NULL DEFAULT 0,
    low_confidence INTEGER NOT NULL DEFAULT 0,
    done INTEGER NOT NULL DEFAULT 0,
    measured_at TIMESTAMP,
    PRIMARY KEY (target_guid, filter)
)`
)

const (
	objGarlic = "Garlic"
	guidGar   = "g-garlic"
	tsHa      = "Ha"
	filterHa  = "H-a"
	filterO3  = "O-III"
)

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

func schedDB(t *testing.T, goalTables bool) *gorm.DB {
	t.Helper()
	db := openDB(t)
	stmts := []string{
		`CREATE TABLE project ("Id" INTEGER PRIMARY KEY, guid TEXT, name TEXT, "isMosaic" INTEGER, minimumaltitude REAL)`,
		`CREATE TABLE target ("Id" INTEGER PRIMARY KEY, name TEXT, guid TEXT, projectid INTEGER, active INTEGER, ra REAL, dec REAL, rotation REAL)`,
		`INSERT INTO project VALUES (1, 'p1', 'Project', 0, 30)`,
		`CREATE TABLE exposuretemplate ("Id" INTEGER PRIMARY KEY, name TEXT, filtername TEXT)`,
		`CREATE TABLE exposureplan ("Id" INTEGER PRIMARY KEY, targetid INTEGER, "exposureTemplateId" INTEGER)`,
		`INSERT INTO target ("Id", name, guid, projectid) VALUES (1, 'M31', 'g-m31', 1), (2, 'Garlic', 'g-garlic', 1), (3, 'Panel 1', 'g-p1a', 1), (4, 'Panel 1', 'g-p1b', 1), (5, 'Old', NULL, 1)`,
		`INSERT INTO exposuretemplate VALUES (1, 'L', 'L'), (2, 'Ha 5m', 'Ha'), (3, 'OIII 5m', 'OIII')`,
		`INSERT INTO exposureplan VALUES (1, 1, 1), (2, 2, 2), (3, 2, 3)`,
	}
	if goalTables {
		stmts = append(stmts, CreateGoalSQLite, CreateProgressSQLite,
			`INSERT INTO ts_goal VALUES ('g-garlic', 'Ha', 0, 15, NULL, 1, '[{"x":0.1,"y":0.1},{"x":0.5,"y":0.1},{"x":0.5,"y":0.5},{"x":0.1,"y":0.5}]', '2026-10-01 00:00:00')`,
			`INSERT INTO ts_goal VALUES ('g-garlic', 'OIII', 1, NULL, 25.9, 0, NULL, '2026-10-01 00:00:00')`,
			`INSERT INTO ts_goal VALUES ('g-gone', 'Ha', 0, 20, NULL, 1, NULL, '2026-10-01 00:00:00')`)
	}
	for _, s := range stmts {
		if err := db.Exec(s).Error; err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	return db
}

func TestLoadGoals(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sched := schedDB(t, true)
	db := appDB(t)
	links := []app.FrameTarget{
		{FrameID: 1, Object: "Panel 1", TargetGUID: "g-p1b", Method: "header"},
		{FrameID: 2, Object: "Garlic Nebula", TargetGUID: guidGar, Method: "header"},
	}
	if err := db.Create(&links).Error; err != nil {
		t.Fatal(err)
	}
	goals, filters, err := LoadGoals(ctx, db, sched)
	if err != nil {
		t.Fatal(err)
	}
	ha := goals[Key{Object: objGarlic, Filter: filterHa}]
	if ha.Kind != KindSNR || ha.SNR != 15 || ha.TargetGUID != guidGar || len(ha.Region) != 4 || !ha.PlateauStop || ha.Depth != DefaultDepth(filterHa) {
		t.Errorf("H-a goal %+v", ha)
	}
	o3 := goals[Key{Object: objGarlic, Filter: filterO3}]
	if o3.Kind != KindDepth || o3.Depth != 25.9 || o3.PlateauStop || o3.SNR != DefaultSNRGoal {
		t.Errorf("O-III goal %+v", o3)
	}
	if len(goals) != 4 || goals[Key{Object: "Garlic Nebula", Filter: filterHa}].SNR != 15 {
		t.Errorf("goals %v", goals)
	}
	if filters[Key{Object: objGarlic, Filter: filterHa}] != tsHa || filters[Key{Object: objGarlic, Filter: filterO3}] != "OIII" ||
		filters[Key{Object: "M31", Filter: "L"}] != "L" {
		t.Errorf("filters %v", filters)
	}
	guids, err := ObjectGUIDs(ctx, db, sched)
	if err != nil {
		t.Fatal(err)
	}
	if guids["Panel 1"] != "g-p1b" || guids[objGarlic] != guidGar || guids["Garlic Nebula"] != guidGar || guids["Old"] != "" {
		t.Errorf("guids %v", guids)
	}

	bare := schedDB(t, false)
	goals, filters, err = LoadGoals(ctx, appDB(t), bare)
	if err != nil || len(goals) != 0 || len(filters) != 3 {
		t.Fatalf("without ts_goal: %v %v %v", goals, filters, err)
	}
}

func appDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := openDB(t)
	if err := db.AutoMigrate(&app.GoalMeasurement{}, &app.FrameTarget{}); err != nil {
		t.Fatal(err)
	}
	return db
}

type progressOut struct {
	TargetGUID    string
	Filter        string
	Kind          int
	GoalValue     float64
	AchievedValue float64
	Progress      float64
	Depth         *float64
	HoursNeeded   float64
	Done          int
}

func TestPublish(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sched := schedDB(t, true)
	db := appDB(t)
	at := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	depth := 25.0
	errMsg := ErrInsufficientData.Error()
	ms := []app.GoalMeasurement{
		{Object: objGarlic, Filter: filterHa, Subs: 64, SNR: 5, EffectiveHours: 8, GainPerHourPct: 5, MeasuredAt: at},
		{Object: objGarlic, Filter: filterO3, Subs: 47, SNR: 4.4, EffectiveHours: 7.8, GainPerHourPct: 5.8, Depth: &depth, MeasuredAt: at},
		{Object: objGarlic, Filter: "S-II", Subs: 6, Error: &errMsg, MeasuredAt: at},
		{Object: "M31", Filter: "L", Subs: 100, SNR: 50, EffectiveHours: 10, GainPerHourPct: 4, MeasuredAt: at},
	}
	if err := db.Create(&ms).Error; err != nil {
		t.Fatal(err)
	}

	dry := &Publisher{App: db, Sched: sched, Mode: PublishDryRun}
	sum, err := dry.Publish(ctx)
	if err != nil || sum.Changed != 2 {
		t.Fatalf("dry run %+v %v", sum, err)
	}
	var n int64
	sched.Table(ProgressTable).Count(&n)
	if n != 0 {
		t.Fatalf("dry run wrote %d rows", n)
	}

	p := &Publisher{App: db, Sched: sched, Mode: PublishOn}
	if sum, err := p.Publish(ctx); err != nil || sum.Rows != 2 || sum.Changed != 2 {
		t.Fatalf("publish %+v %v", sum, err)
	}
	var rows []progressOut
	if err := sched.Table(ProgressTable).Order("filter").Scan(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows %+v", rows)
	}
	ha, o3 := rows[0], rows[1]
	if ha.TargetGUID != guidGar || ha.Filter != tsHa || ha.Kind != 0 || ha.GoalValue != 15 || ha.AchievedValue != 5 ||
		math.Abs(ha.Progress-1.0/9) > 1e-9 || math.Abs(ha.HoursNeeded-8*8) > 1e-9 || ha.Depth != nil || ha.Done != 0 {
		t.Errorf("H-a row %+v", ha)
	}
	if o3.Filter != "OIII" || o3.Kind != 1 || o3.GoalValue != 25.9 || o3.AchievedValue != 25 || o3.Depth == nil || *o3.Depth != 25 {
		t.Errorf("O-III row %+v", o3)
	}

	checkRepublish(ctx, t, db, sched, p)
}

func checkRepublish(ctx context.Context, t *testing.T, db, sched *gorm.DB, p *Publisher) {
	t.Helper()
	if sum, err := p.Publish(ctx); err != nil || sum.Changed != 0 {
		t.Fatalf("unchanged publish %+v %v", sum, err)
	}
	if err := db.Model(&app.GoalMeasurement{}).Where("filter = ?", filterHa).Update("snr", 15).Error; err != nil {
		t.Fatal(err)
	}
	if sum, err := p.Publish(ctx); err != nil || sum.Changed != 1 {
		t.Fatalf("changed publish %+v %v", sum, err)
	}
	var done progressOut
	sched.Table(ProgressTable).Where("filter = ?", tsHa).Scan(&done)
	if done.Done != 1 || done.Progress != 1 {
		t.Errorf("done row %+v", done)
	}
}

func TestPublishMissingTables(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := appDB(t)
	p := &Publisher{App: db, Sched: schedDB(t, false), Mode: PublishOn}
	for range 2 {
		if sum, err := p.Publish(ctx); err != nil || sum.Rows != 0 {
			t.Fatalf("%+v %v", sum, err)
		}
	}
	sched := schedDB(t, false)
	if err := sched.Exec(CreateGoalSQLite).Error; err != nil {
		t.Fatal(err)
	}
	p = &Publisher{App: db, Sched: sched, Mode: PublishOn}
	if sum, err := p.Publish(ctx); err != nil || sum.Rows != 0 {
		t.Fatalf("%+v %v", sum, err)
	}
	off := &Publisher{App: db, Sched: schedDB(t, true), Mode: PublishOff}
	if sum, err := off.Publish(ctx); err != nil || sum.Rows != 0 {
		t.Fatalf("%+v %v", sum, err)
	}
}

func TestRegionHash(t *testing.T) {
	t.Parallel()
	r := []Point{{0, 0}, {1, 0}, {1, 1}}
	if RegionHash(nil) != "" || RegionHash(r) == "" || RegionHash(r) == RegionHash([]Point{{0, 0}, {1, 0}, {0, 1}}) {
		t.Fatal("region hash")
	}
}
