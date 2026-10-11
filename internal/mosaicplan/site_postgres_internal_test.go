package mosaicplan

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	pgdriver "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestNewestLightsSkipUndatedOnPostgres(t *testing.T) {
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
	if err := db.AutoMigrate(&app.Frame{}); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 10, 4, 0, 0, 0, time.UTC)
	for i := range 8 {
		f := app.Frame{Key: fmt.Sprintf("undated-%d", i), ETag: "e", Type: "LIGHT", LastModified: at}
		if i == 7 {
			f.Key, f.DateObs = "tonight", &at
		}
		if err := db.Create(&f).Error; err != nil {
			t.Fatal(err)
		}
	}
	frames, err := newestLights(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 5 || frames[0].Key != "tonight" {
		t.Errorf("newest lights %d, first %+v", len(frames), frames[:min(1, len(frames))])
	}
}
