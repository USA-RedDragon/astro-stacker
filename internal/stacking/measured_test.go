package stacking

import (
	"context"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// A measured sub carries its camera offset, which sets the pedestal its sky
// is scored against.
func TestMeasuredSubsCarryOffset(t *testing.T) {
	t.Parallel()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&app.Frame{}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	offset, sky, hfr := 240.0, 2487.0, 2.4
	if err := db.Create(&app.Frame{Key: "M 92/LIGHT/x.xisf", Type: frameTypeLight, Object: "M 92", Filter: filterBlue,
		LastModified: now, Offset: &offset, SkyADU: &sky, StarHFR: &hfr, MeasuredAt: &now}).Error; err != nil {
		t.Fatal(err)
	}
	p := &Pipeline{db: db}
	got, err := p.measuredSubs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Offset != 240 {
		t.Fatalf("measured %+v, want offset 240", got)
	}
}
