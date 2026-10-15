package schedcmd_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"github.com/USA-RedDragon/astro-stacker/internal/testdb"
)

type altitudeSnapshot struct {
	fakeSnapshot
	minimum, maximum float64
}

func (a altitudeSnapshot) ProjectAltitudes(_ context.Context, id int64) (float64, float64, error) {
	if id != 5 {
		return 0, 0, schedcmd.ErrProjectNotFound
	}
	return a.minimum, a.maximum, nil
}

func TestProjectEditChecksMaximumAgainstMinimumAltitude(t *testing.T) {
	t.Parallel()
	s := spec(t, schedcmd.KindProjectEdit)
	both := json.RawMessage(`{"id":5,"name":"Cygnis Loop","changes":[{"field":"minimumaltitude","before":20,"after":40},{"field":"maximumaltitude","before":0,"after":35}]}`)
	if err := s.Validate(both); !errors.Is(err, schedcmd.ErrInvalid) {
		t.Fatalf("both altitudes inverted: %v", err)
	}
	ok := json.RawMessage(`{"id":5,"name":"Cygnis Loop","changes":[{"field":"minimumaltitude","before":20,"after":30},{"field":"maximumaltitude","before":0,"after":75}]}`)
	if err := s.Validate(ok); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	svc := newService(t)
	svc.Snapshot = altitudeSnapshot{minimum: 30, maximum: 70}
	cases := []struct {
		payload json.RawMessage
		bad     bool
	}{
		{projectEdit(schedcmd.FieldMaximumAltitude, "70", "25"), true},
		{projectEdit(schedcmd.FieldMaximumAltitude, "70", "0"), false},
		{projectEdit(schedcmd.FieldMaximumAltitude, "70", "60"), false},
		{projectEdit("minimumaltitude", "30", "75"), true},
		{projectEdit("minimumaltitude", "30", "40"), false},
		{json.RawMessage(`{"id":9,"name":"Elsewhere","changes":[{"field":"maximumaltitude","before":0,"after":10}]}`), false},
	}
	for _, c := range cases {
		_, err := svc.Submit(ctx, schedcmd.KindProjectEdit, c.payload, "web")
		if c.bad != errors.Is(err, schedcmd.ErrInvalid) {
			t.Errorf("%s: %v", c.payload, err)
		}
	}
	batch := json.RawMessage(`{"items":[{"id":5,"name":"Cygnis Loop","changes":[{"field":"maximumaltitude","before":70,"after":20}]}]}`)
	if _, err := svc.Submit(ctx, schedcmd.KindProjectBatchEdit, batch, "web"); !errors.Is(err, schedcmd.ErrInvalid) {
		t.Errorf("batch: %v", err)
	}
}

func TestProjectAltitudesFrom(t *testing.T) {
	t.Parallel()
	db := testdb.SQLite(t, "")
	for _, q := range []string{
		`CREATE TABLE project ("Id" INTEGER PRIMARY KEY, minimumaltitude REAL, "maximumAltitude" REAL)`,
		`INSERT INTO project VALUES (5, 30, 70), (6, 20, NULL)`,
	} {
		if err := db.Exec(q).Error; err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	if lo, hi, err := schedcmd.ProjectAltitudesFrom(ctx, db, 5); err != nil || lo != 30 || hi != 70 {
		t.Fatalf("%v %v %v", lo, hi, err)
	}
	if lo, hi, err := schedcmd.ProjectAltitudesFrom(ctx, db, 6); err != nil || lo != 20 || hi != 0 {
		t.Fatalf("null maximum %v %v %v", lo, hi, err)
	}
	if _, _, err := schedcmd.ProjectAltitudesFrom(ctx, db, 7); !errors.Is(err, schedcmd.ErrProjectNotFound) {
		t.Fatalf("missing project %v", err)
	}
}
