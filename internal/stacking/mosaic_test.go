package stacking

import (
	"context"
	"math"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestMosaicGroupsFromSchedulerProjects(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE project ("Id" INTEGER PRIMARY KEY, name TEXT)`,
		`CREATE TABLE target ("Id" INTEGER PRIMARY KEY, name TEXT, ra REAL, dec REAL, rotation REAL, projectid INTEGER)`,
		`INSERT INTO project VALUES (1, 'Cygnis Loop'), (2, 'Rosette'), (3, 'M31'), (4, 'Lonely')`,
		// Panels out of order, a mosaic, two targets that aren't panels, and
		// a project with one panel.
		`INSERT INTO target VALUES
			(1, 'Cygnis Loop Panel 2', 20.868, 29.76, 0, 1),
			(2, 'Cygnis Loop Panel 1', 20.868, 31.537, 0, 1),
			(3, 'Rosette Nebula', 6.53, 4.95, 0, 2),
			(4, 'Rosette Nebula SHO', 6.53, 4.95, 0, 2),
			(5, 'Andromeda', 0.71, 41.27, 0, 3),
			(6, 'Lonely Panel 1', 1, 1, 0, 4)`,
	} {
		if err := db.Exec(q).Error; err != nil {
			t.Fatal(err)
		}
	}
	groups, err := mosaicGroups(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0].Project != "Cygnis Loop" || len(groups[0].Panels) != 2 {
		t.Fatalf("groups %+v", groups)
	}
	first := groups[0].Panels[0]
	if first.Object != "Cygnis Loop Panel 1" || first.Number != 1 {
		t.Errorf("panels not in order: %+v", groups[0].Panels)
	}
	// Right ascension comes from Target Scheduler in hours.
	if math.Abs(first.RA-313.02) > 1e-9 || first.Dec != 31.537 {
		t.Errorf("panel 1 at %v, %v", first.RA, first.Dec)
	}
}
