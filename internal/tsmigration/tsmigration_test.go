package tsmigration_test

import (
	"context"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/tsmigration"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

const migrationSchema = `CREATE TABLE ts_template_migration (plan_id integer not null, plan_guid text, target_id integer, profile_id text not null,
	filter text not null, old_template_id integer not null, old_template_guid text, old_template_name text, new_template_id integer not null,
	migrated_at text not null, reverted_at text, PRIMARY KEY (plan_id, migrated_at))`

func TestOldDefaults(t *testing.T) {
	t.Parallel()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE exposuretemplate ("Id" integer primary key, filtername text, defaultexposure real)`,
		`INSERT INTO exposuretemplate VALUES (1, 'Red', 600), (2, 'Red', 300), (3, 'H-a', 0)`,
	} {
		if err := db.Exec(q).Error; err != nil {
			t.Fatal(err)
		}
	}
	got, err := tsmigration.OldDefaults(context.Background(), db)
	if err != nil || len(got) != 0 {
		t.Fatalf("without the table %v %v", got, err)
	}
	for _, q := range []string{
		migrationSchema,
		`INSERT INTO ts_template_migration (plan_id, profile_id, filter, old_template_id, new_template_id, migrated_at, reverted_at) VALUES
			(40, 'p', 'Red', 1, 2, '2026-10-12T03:00:00Z', null),
			(41, 'p', 'Red', 1, 2, '2026-10-12T03:00:00Z', '2026-10-13T03:00:00Z'),
			(42, 'p', 'H-a', 3, 2, '2026-10-12T03:00:00Z', null)`,
	} {
		if err := db.Exec(q).Error; err != nil {
			t.Fatal(err)
		}
	}
	got, err = tsmigration.OldDefaults(context.Background(), db)
	if err != nil || len(got) != 1 || got[40] != 600 {
		t.Fatalf("old defaults %v %v, want plan 40 at 600 s only: 41 was reverted, 42's old template has no default", got, err)
	}
}
