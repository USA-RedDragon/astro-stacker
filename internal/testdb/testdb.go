// Package testdb opens throwaway databases for tests.
package testdb

import (
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var opened atomic.Int64 //nolint:gochecknoglobals

// SQLite opens a new, empty shared-cache in-memory database for t and closes
// it when t ends. Every call gets its own database, and closing the last
// connection lets SQLite free it, so reruns under -count start clean.
func SQLite(t testing.TB, name string) *gorm.DB {
	t.Helper()
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "_" + name + "_" + strconv.FormatInt(opened.Add(1), 10) + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}
