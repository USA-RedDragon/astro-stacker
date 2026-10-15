package tables_test

import (
	"context"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/store/tables"
	"github.com/USA-RedDragon/astro-stacker/internal/testdb"
	"gorm.io/gorm"
)

const acquired = "acquiredimage"

func check(t *testing.T, db *gorm.DB) {
	t.Helper()
	ctx := context.Background()
	if err := db.Exec("create table " + acquired + ` ("Id" integer primary key, "profileId" text, rotation real)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&app.Frame{}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		value any
		want  bool
	}{{acquired, true}, {&app.Frame{}, true}, {"project", false}, {&app.StackFrame{}, false}} {
		got, err := tables.Has(ctx, db, c.value)
		if err != nil || got != c.want || got != db.Migrator().HasTable(c.value) {
			t.Errorf("Has(%v) = %v, %v; want %v", c.value, got, err, c.want)
		}
	}
	for _, c := range []struct {
		table  any
		column string
		want   bool
	}{{acquired, "rotation", true}, {acquired, "profileId", true}, {acquired, "guid", false}, {"project", "guid", false}, {&app.Frame{}, "preview_key", true}} {
		got, err := tables.HasColumn(ctx, db, c.table, c.column)
		if err != nil || got != c.want || got != db.Migrator().HasColumn(c.table, c.column) {
			t.Errorf("HasColumn(%v, %s) = %v, %v; want %v", c.table, c.column, got, err, c.want)
		}
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := tables.Has(ctx, db, acquired); err == nil {
		t.Error("Has on a closed database: no error")
	}
	if _, err := tables.HasColumn(ctx, db, acquired, "rotation"); err == nil {
		t.Error("HasColumn on a closed database: no error")
	}
}

func TestSQLite(t *testing.T) {
	t.Parallel()
	db := testdb.SQLite(t, "")
	check(t, db)
}

func TestPostgres(t *testing.T) {
	t.Parallel()
	db := testdb.Postgres(t, "stacker", "stacker")
	check(t, db)
}
