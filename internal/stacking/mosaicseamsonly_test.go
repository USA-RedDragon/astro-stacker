package stacking

import (
	"context"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestMeasureSeamsOnlyKeepsUpdatedAt(t *testing.T) {
	t.Parallel()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&app.Mosaic{}); err != nil {
		t.Fatal(err)
	}
	built := time.Date(2026, 10, 9, 21, 42, 27, 0, time.UTC)
	m := app.Mosaic{Project: "Galaxy Cluster", Filter: filterRed, Panels: 1, PanelsTotal: 4, UpdatedAt: built}
	if err := db.Create(&m).Error; err != nil {
		t.Fatal(err)
	}
	p := &Pipeline{db: db}
	if err := p.measureSeamsOnly(context.Background(), mosaicGroup{Project: m.Project}, filterRed, nil, &m, "sig"); err != nil {
		t.Fatal(err)
	}
	var got app.Mosaic
	if err := db.First(&got, m.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.SeamSignature != seamSignature("sig") {
		t.Fatalf("seam signature %q", got.SeamSignature)
	}
	if !got.UpdatedAt.Equal(built) {
		t.Fatalf("updated_at moved to %v without a new mosaic", got.UpdatedAt)
	}
}
