package goals

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
)

const injected = "injected_failure"

func failQueries(t *testing.T, db *gorm.DB, match string) {
	t.Helper()
	fail := func(tx *gorm.DB) {
		if tx.Statement.Table == match || strings.Contains(tx.Statement.SQL.String(), match) {
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

func wantInjected(t *testing.T, what string, err error) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), injected) {
		t.Errorf("%s: want the injected error, got %v", what, err)
	}
}

func TestLoadGoalsFailsWhenATableCheckFails(t *testing.T) {
	t.Parallel()
	sched := schedDB(t, true)
	failQueries(t, sched, "sqlite_master")
	gs, filters, err := LoadGoals(context.Background(), appDB(t), sched)
	if err == nil {
		t.Errorf("goals %v filters %v", gs, filters)
	}
	wantInjected(t, "LoadGoals", err)
}

func TestPublishFailsWhenATableCheckFails(t *testing.T) {
	t.Parallel()
	sched := schedDB(t, true)
	failQueries(t, sched, "sqlite_master")
	p := &Publisher{App: appDB(t), Sched: sched, Mode: PublishOn}
	sum, err := p.Publish(context.Background())
	if err == nil {
		t.Errorf("summary %+v", sum)
	}
	wantInjected(t, "Publish", err)
}

func TestPublishFailsWhenAColumnCheckFails(t *testing.T) {
	t.Parallel()
	sched, db := readinessDBs(t)
	at := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	if err := db.Create(&app.GoalMeasurement{Object: objGarlic, Filter: filterHa, Subs: 40, SNR: 5, EffectiveHours: 3, GainPerHourPct: 9, MeasuredAt: at}).Error; err != nil {
		t.Fatal(err)
	}
	addStack(t, db, objGarlic, filterHa, 40, 0)
	failQueries(t, sched, "tbl_name")
	p := &Publisher{App: db, Sched: sched, Mode: PublishOn}
	sum, err := p.Publish(context.Background())
	if err == nil {
		t.Errorf("published without readiness: %+v %+v", sum, readRows(t, sched))
	}
	wantInjected(t, "Publish", err)
}

func TestAppReadsFailWhenATableCheckFails(t *testing.T) {
	t.Parallel()
	_, db := readinessDBs(t)
	addStack(t, db, objGarlic, filterHa, 5, 0)
	failQueries(t, db, "sqlite_master")
	subs, err := MeasurableSubs(context.Background(), db)
	if err == nil {
		t.Errorf("subs %v", subs)
	}
	wantInjected(t, "MeasurableSubs", err)
	b, err := CountBackfill(context.Background(), db, BackfillLive{})
	if err == nil {
		t.Errorf("backfill %+v", b)
	}
	wantInjected(t, "CountBackfill", err)
}
