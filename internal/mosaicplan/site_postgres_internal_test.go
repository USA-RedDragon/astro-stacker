package mosaicplan

import (
	"fmt"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/testdb"
)

func TestNewestLightsSkipUndatedOnPostgres(t *testing.T) {
	t.Parallel()
	db := testdb.Postgres(t, "stacker", "stacker")
	ctx := t.Context()
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
