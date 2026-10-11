package server_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/server"
	"gorm.io/gorm"
)

func closeDB(t *testing.T, db *gorm.DB) {
	t.Helper()
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSchedulerDataFailsWhenTheSchedulerDatabaseIsDown(t *testing.T) {
	t.Parallel()
	sched, appDB, at := tonightFixture(t)
	h := newHandler(t, sched, appDB, server.Extras{Now: func() time.Time { return at(9) }})
	closeDB(t, sched)
	for _, p := range []string{pathProjects, pathSubs} {
		if code, body := do(t, h, http.MethodGet, p, ""); code != http.StatusInternalServerError {
			t.Errorf("%s with the scheduler database down: %d %s", p, code, body)
		}
	}
}

func TestTonightSubsFailWhenTheAppDatabaseIsDown(t *testing.T) {
	t.Parallel()
	sched, appDB, at := tonightFixture(t)
	h := newHandler(t, sched, appDB, server.Extras{Now: func() time.Time { return at(9) }})
	closeDB(t, appDB)
	if code, body := do(t, h, http.MethodGet, pathSubs, ""); code != http.StatusInternalServerError {
		t.Errorf("tonight subs with the app database down: %d %s", code, body)
	}
}
