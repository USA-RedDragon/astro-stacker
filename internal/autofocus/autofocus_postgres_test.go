package autofocus_test

import (
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/autofocus"
	"github.com/USA-RedDragon/astro-stacker/internal/testdb"
)

func TestLoadOnPostgres(t *testing.T) {
	t.Parallel()
	db := testdb.Postgres(t, "schedulerdb", "stacker")
	ctx := t.Context()
	out, err := autofocus.Load(ctx, db, time.Time{}, 0)
	if err != nil || out.Available {
		t.Fatalf("before the table: %+v %v", out, err)
	}
	exec(t, db, autofocus.SchemaPostgres, autofocus.SchemaPostgres,
		`INSERT INTO ts_autofocus_run (id, started_at, finished_at, success, filter, chosen_position, chosen_value, points, fit, source, recorded_at)
		 VALUES ('x', '2026-10-12 03:00:00', '2026-10-12 03:02:30.250', 1, 'L', 5040, 1.9, '[{"position":5000,"value":2.1}]', '{"r_squared":{"hyperbolic":0.97}}', 'event', '2026-10-12 03:02:31')`)
	out, err = autofocus.Load(ctx, db, time.Date(2026, 10, 12, 3, 2, 30, 0, time.UTC), 0)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Available || len(out.Runs) != 1 {
		t.Fatalf("%+v", out)
	}
	r := out.Runs[0]
	if !r.FinishedAt.Equal(time.Date(2026, 10, 12, 3, 2, 30, 250_000_000, time.UTC)) || !r.Success || r.ChosenPosition == nil || *r.ChosenPosition != 5040 ||
		len(r.Points) != 1 || string(r.Fit) != `{"r_squared":{"hyperbolic":0.97}}` {
		t.Fatalf("%+v", r)
	}
}
