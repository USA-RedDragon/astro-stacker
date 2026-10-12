package autofocus_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/autofocus"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const sqliteSchema = `CREATE TABLE ts_autofocus_run (
    id TEXT NOT NULL PRIMARY KEY,
    profile_id TEXT,
    started_at TIMESTAMP,
    finished_at TIMESTAMP NOT NULL,
    duration_seconds REAL,
    success INTEGER NOT NULL,
    error TEXT,
    filter TEXT,
    temperature REAL,
    autofocuser TEXT,
    method TEXT,
    fitting TEXT,
    initial_position REAL,
    previous_position REAL,
    previous_value REAL,
    chosen_position REAL,
    chosen_value REAL,
    points TEXT NOT NULL,
    fit TEXT,
    target_name TEXT,
    target_guid TEXT,
    source TEXT NOT NULL,
    recorded_at TIMESTAMP NOT NULL
)`

func memDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s_%d?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"), time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func exec(t *testing.T, db *gorm.DB, stmts ...string) {
	t.Helper()
	for _, s := range stmts {
		if err := db.Exec(s).Error; err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
}

func TestParsePoints(t *testing.T) {
	t.Parallel()
	got, err := autofocus.ParsePoints(`[{"position":1200,"value":3.1,"error":0.2},{"position":1300,"value":2.4},{"position":1400},{"value":2}]`)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Position != 1200 || got[0].Value != 3.1 || got[0].Error == nil || *got[0].Error != 0.2 ||
		got[1].Error != nil {
		t.Fatalf("%+v", got)
	}
	for _, s := range []string{"", "  ", "[]"} {
		got, err := autofocus.ParsePoints(s)
		if err != nil || got == nil || len(got) != 0 {
			t.Fatalf("%q: %v %v", s, got, err)
		}
	}
	got, err = autofocus.ParsePoints(`{"position":1}`)
	if err == nil || got == nil || len(got) != 0 {
		t.Fatalf("object: %v %v", got, err)
	}
}

func TestLoadWithoutTable(t *testing.T) {
	t.Parallel()
	out, err := autofocus.Load(context.Background(), memDB(t), time.Time{}, 0)
	if err != nil || out.Available || out.Runs == nil || len(out.Runs) != 0 {
		t.Fatalf("%+v %v", out, err)
	}
	out, err = autofocus.Load(context.Background(), nil, time.Time{}, 0)
	if err != nil || out.Available || out.Runs == nil {
		t.Fatalf("nil db: %+v %v", out, err)
	}
}

func seed(t *testing.T, db *gorm.DB) {
	t.Helper()
	exec(t, db, sqliteSchema,
		`INSERT INTO ts_autofocus_run VALUES ('a', 'prof', '2026-10-11 02:00:00.000', '2026-10-11 02:03:00.000', 180, 1, NULL, 'L', 11.5, 'Hocus Focus', 'STARHFR', 'HYPERBOLIC',
			5000, 5010, 2.2, 5040, 1.9, '[{"position":4900,"value":3.0,"error":0.1},{"position":5000,"value":2.1,"error":0.05},{"position":5100,"value":2.9,"error":0.1}]',
			'{"fittings":{"hyperbolic":"y = 1.9 * cosh(asinh(...))"},"r_squared":{"hyperbolic":0.98}}', 'M31', 'g1', 'event', '2026-10-11 02:03:01.000')`,
		`INSERT INTO ts_autofocus_run (id, profile_id, started_at, finished_at, duration_seconds, success, error, filter, autofocuser, method, fitting, initial_position, points, fit, source, recorded_at)
			VALUES ('b', 'prof', '2026-10-12 03:00:00.000', '2026-10-12 03:02:00.500', 120.5, 0, 'not enough stars', 'Ha', 'Hocus Focus', 'STARHFR', 'HYPERBOLIC',
			5040, '[{"position":4940,"value":4.0}]', 'not json', 'report', '2026-10-12 03:02:01.000')`,
		`INSERT INTO ts_autofocus_run (id, profile_id, finished_at, success, filter, chosen_position, points, source, recorded_at)
			VALUES ('c', 'prof', '2026-10-01 01:02:00.000', 1, 'R', 5100, 'garbage', 'event', '2026-10-01 01:02:00.000')`)
}

func checkFailedRun(t *testing.T, b autofocus.Run) {
	t.Helper()
	if b.Success || b.Error != "not enough stars" || b.Filter != "Ha" || b.Temperature != nil || b.ChosenPosition != nil || b.Fit != nil ||
		len(b.Points) != 1 || b.Source != "report" || !b.FinishedAt.Equal(time.Date(2026, 10, 12, 3, 2, 0, 500_000_000, time.UTC)) {
		t.Fatalf("b %+v", b)
	}
}

func checkGoodRun(t *testing.T, a autofocus.Run) {
	t.Helper()
	if !a.Success || a.ChosenPosition == nil || *a.ChosenPosition != 5040 || a.ChosenValue == nil || *a.ChosenValue != 1.9 ||
		a.PreviousPosition == nil || *a.PreviousPosition != 5010 || len(a.Points) != 3 || a.Points[1].Position != 5000 {
		t.Fatalf("a %+v", a)
	}
	if !strings.Contains(string(a.Fit), `"hyperbolic":0.98`) || a.TargetName != "M31" || a.TargetGUID != "g1" || a.Autofocuser != "Hocus Focus" ||
		a.DurationSeconds == nil || *a.DurationSeconds != 180 || a.StartedAt == nil || a.StartedAt.Location() != time.UTC ||
		!a.StartedAt.Equal(time.Date(2026, 10, 11, 2, 0, 0, 0, time.UTC)) {
		t.Fatalf("a %+v", a)
	}
}

func TestLoadNewestFirstSince(t *testing.T) {
	t.Parallel()
	db := memDB(t)
	seed(t, db)
	since := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	out, err := autofocus.Load(context.Background(), db, since, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Available || len(out.Runs) != 2 || out.Runs[0].ID != "b" || out.Runs[1].ID != "a" || !out.Since.Equal(since) {
		t.Fatalf("%+v", out)
	}
	checkFailedRun(t, out.Runs[0])
	checkGoodRun(t, out.Runs[1])
	out, err = autofocus.Load(context.Background(), db, time.Time{}, 0)
	if err != nil || len(out.Runs) != 3 {
		t.Fatalf("all %+v %v", out, err)
	}
	c := out.Runs[2]
	if c.ID != "c" || c.PointsError == "" || c.Points == nil || len(c.Points) != 0 || c.StartedAt != nil || c.DurationSeconds != nil {
		t.Fatalf("c %+v", c)
	}
	out, err = autofocus.Load(context.Background(), db, time.Time{}, 1)
	if err != nil || len(out.Runs) != 1 || out.Runs[0].ID != "b" {
		t.Fatalf("limit %+v %v", out, err)
	}
}
