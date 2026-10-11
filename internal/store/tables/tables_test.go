package tables_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/store/tables"
	"github.com/glebarez/sqlite"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	pgdriver "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
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
	dsn := fmt.Sprintf("file:%s_%d?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"), time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	check(t, db)
}

func TestPostgres(t *testing.T) {
	t.Parallel()
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx := context.Background()
	c, err := postgres.Run(ctx, "postgres:17-alpine", postgres.WithDatabase("stacker"), postgres.WithUsername("stacker"), postgres.WithPassword("stacker"),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(2*time.Minute)))
	testcontainers.CleanupContainer(t, c)
	if err != nil {
		t.Fatal(err)
	}
	dsn, err := c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(pgdriver.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	check(t, db)
}
