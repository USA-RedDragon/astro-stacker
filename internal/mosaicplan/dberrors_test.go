package mosaicplan_test

import (
	"context"
	"strings"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/goals"
	"github.com/USA-RedDragon/astro-stacker/internal/mosaicplan"
	"github.com/USA-RedDragon/astro-stacker/internal/mosaics"
	"gorm.io/gorm"
)

const injected = "injected_failure"

func failQueries(t *testing.T, db *gorm.DB, match string) {
	t.Helper()
	fail := func(tx *gorm.DB) {
		if tx.Statement.Table == match {
			tx.Statement.SQL.Reset()
			tx.Statement.SQL.WriteString("SELECT 1 FROM " + injected)
			tx.Statement.Vars = nil
		}
	}
	if err := db.Callback().Query().Before("gorm:query").Register("test:fail", fail); err != nil {
		t.Fatal(err)
	}
	if err := db.Callback().Row().Before("gorm:row").Register("test:fail", fail); err != nil {
		t.Fatal(err)
	}
}

func TestDetailFailsWhenGoalsCannotBeRead(t *testing.T) {
	t.Parallel()
	sched := schedFixture(t)
	if err := sched.Exec(`CREATE TABLE ts_goal (target_guid TEXT NOT NULL, filter TEXT NOT NULL, kind INTEGER NOT NULL DEFAULT 0, snr_goal REAL, depth_goal REAL, plateau_stop INTEGER NOT NULL DEFAULT 1, region TEXT, updated_at TIMESTAMP, PRIMARY KEY (target_guid, filter))`).Error; err != nil {
		t.Fatal(err)
	}
	failQueries(t, sched, goals.GoalTable)
	svc := mosaicplan.New(appFixture(t), sched)
	svc.Rig = mosaics.Rig{WidthDeg: 3.32, HeightDeg: 2.22, ScaleArcsec: 1.915}
	d, err := svc.Detail(context.Background(), "pm")
	if err == nil || !strings.Contains(err.Error(), injected) {
		t.Fatalf("detail scored against default goals: %v, %+v", err, d.Average)
	}
}
