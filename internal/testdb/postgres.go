package testdb

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	pgdriver "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// ReaperIdle is how long the shared testcontainers reaper waits for a new
// client before it exits. All packages of one go test run share the reaper.
// With the library default of 10s, a package that finds the reaper during
// its shutdown waits 60s for it and fails, so the wait outlasts a whole run.
const ReaperIdle = "30m"

var keepReaper sync.Once //nolint:gochecknoglobals

// Postgres starts a throwaway PostgreSQL 17 container with the named
// database, skips t when no container runtime is available, and removes the
// container when t ends.
func Postgres(t *testing.T, database, user string) *gorm.DB {
	t.Helper()
	keepReaper.Do(func() {
		if os.Getenv("RYUK_RECONNECTION_TIMEOUT") == "" && os.Getenv("TESTCONTAINERS_RYUK_RECONNECTION_TIMEOUT") == "" {
			_ = os.Setenv("TESTCONTAINERS_RYUK_RECONNECTION_TIMEOUT", ReaperIdle)
		}
	})
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx := context.Background()
	c, err := postgres.Run(ctx, "postgres:17-alpine", postgres.WithDatabase(database), postgres.WithUsername(user), postgres.WithPassword(user),
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
	return db
}
