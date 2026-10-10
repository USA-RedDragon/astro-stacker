package cmd_test

import (
	"context"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/cmd"
	"github.com/USA-RedDragon/astro-stacker/internal/config"
	"github.com/USA-RedDragon/astro-stacker/internal/goalmeasure"
	"github.com/USA-RedDragon/astro-stacker/internal/goals"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	configulator "github.com/USA-RedDragon/configulator/v2"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func memDB(t *testing.T) *gorm.DB {
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

func TestGoalPublishModesMatch(t *testing.T) {
	t.Parallel()
	pairs := [][2]string{
		{config.GoalsPublishOff, goals.PublishOff},
		{config.GoalsPublishDryRun, goals.PublishDryRun},
		{config.GoalsPublishOn, goals.PublishOn},
	}
	for _, p := range pairs {
		if p[0] != p[1] {
			t.Errorf("config mode %q is goals mode %q", p[0], p[1])
		}
	}
}

func TestGoalOptionsPublishFromEnv(t *testing.T) {
	t.Parallel()
	env := map[string]string{"GOALS_ENABLED": "true", "GOALS_PUBLISH": "on", "GOALS_INTERVAL_MINUTES": "30"}
	cfg, err := configulator.New(config.ConfigSchema()).
		WithEnvironmentVariables(&configulator.EnvironmentVariableOptions{Separator: "_"}).
		WithEnviron(func(k string) (string, bool) { v, ok := env[k]; return v, ok }).
		LoadWithoutValidation()
	if err != nil {
		t.Fatal(err)
	}
	opts := cmd.GoalOptions(cfg, nil, nil)
	if opts.Publish != goals.PublishOn || opts.Interval != 30*time.Minute {
		t.Fatalf("options %+v", opts)
	}

	db := memDB(t)
	if err := db.AutoMigrate(&app.GoalMeasurement{}, &app.FrameTarget{}, &app.Stack{}, &app.StackFrame{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&app.GoalMeasurement{Object: "Garlic Nebula", Filter: "H-a", Subs: 64, SNR: 5, EffectiveHours: 8,
		GainPerHourPct: 5, MeasuredAt: time.Now().UTC()}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&app.FrameTarget{FrameID: 1, Object: "Garlic Nebula", TargetGUID: "g-garlic", Method: "header"}).Error; err != nil {
		t.Fatal(err)
	}
	sched := memDB(t)
	for _, s := range []string{
		`CREATE TABLE project ("Id" INTEGER PRIMARY KEY, guid TEXT, name TEXT, "isMosaic" INTEGER, minimumaltitude REAL)`,
		`CREATE TABLE target ("Id" INTEGER PRIMARY KEY, name TEXT, guid TEXT, projectid INTEGER, active INTEGER, ra REAL, dec REAL, rotation REAL)`,
		`INSERT INTO project VALUES (1, 'p1', 'Project', 0, 30)`,
		`INSERT INTO target ("Id", name, guid, projectid) VALUES (1, 'Garlic', 'g-garlic', 1)`,
		`CREATE TABLE ts_goal (target_guid TEXT NOT NULL, filter TEXT NOT NULL, kind INTEGER NOT NULL DEFAULT 0, snr_goal REAL,
			depth_goal REAL, plateau_stop INTEGER NOT NULL DEFAULT 1, region TEXT, updated_at TEXT, PRIMARY KEY (target_guid, filter))`,
		`CREATE TABLE ts_goal_progress (target_guid TEXT NOT NULL, filter TEXT NOT NULL, kind INTEGER, goal_value REAL, achieved_value REAL,
			progress REAL, snr REAL, depth REAL, effective_hours REAL, hours_needed REAL, gain_per_hour_pct REAL, plateau INTEGER,
			low_confidence INTEGER, done INTEGER, measured_at TIMESTAMP, PRIMARY KEY (target_guid, filter))`,
		`INSERT INTO ts_goal VALUES ('g-garlic', 'H-a', 0, 15, NULL, 1, NULL, '2026-10-10T06:06:24Z')`,
	} {
		if err := sched.Exec(s).Error; err != nil {
			t.Fatal(err)
		}
	}

	r := goalmeasure.New(db, sched, nil, nil, opts)
	sum, err := r.Publish(context.Background())
	if err != nil || sum.Rows != 1 || sum.Changed != 1 {
		t.Fatalf("publish %+v %v", sum, err)
	}
	var n int64
	if err := sched.Table(goals.ProgressTable).Where("target_guid = ? AND filter = ?", "g-garlic", "H-a").Count(&n).Error; err != nil || n != 1 {
		t.Fatalf("progress rows %d %v", n, err)
	}
}
