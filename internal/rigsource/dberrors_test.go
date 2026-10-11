package rigsource_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/rigsource"
	"gorm.io/gorm"
)

const injected = "injected_failure"

func failQueries(t *testing.T, db *gorm.DB, match string) func() {
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
	return func() {
		if err := db.Callback().Query().Remove("test:fail"); err != nil {
			t.Fatal(err)
		}
		if err := db.Callback().Row().Remove("test:fail"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMeasureFailsWhenAReadFails(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		sched bool
		match string
	}{{"lights table check", false, "sqlite_master"}, {"guiding table check", true, "sqlite_master"}, {"guiding", true, "acquiredimage"}} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			appDB, sched, now := rigFixture(t)
			db := appDB
			if c.sched {
				db = sched
			}
			failQueries(t, db, c.match)
			r, err := rigsource.Measure(context.Background(), appDB, sched, now)
			if err == nil || !strings.Contains(err.Error(), injected) {
				t.Fatalf("measured with a failed read: %v, known %v, HFR source %v", err, r.Known(), r.Basis.HFRSource)
			}
		})
	}
}

func TestSourceDoesNotCacheAFailedTableCheck(t *testing.T) {
	t.Parallel()
	appDB, sched, now := rigFixture(t)
	src := &rigsource.Source{App: appDB, Sched: sched, Now: func() time.Time { return now }}
	restore := failQueries(t, appDB, "sqlite_master")
	if r := src.Get(context.Background()); r.Known() {
		t.Fatalf("rig known with the lights unreadable: %+v", r)
	}
	restore()
	if r := src.Get(context.Background()); !r.Known() {
		t.Fatal("a failed table check was cached as an unknown rig")
	}
}
