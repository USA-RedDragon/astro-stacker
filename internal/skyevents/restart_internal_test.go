package skyevents

import (
	"context"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	shownEvent = "moon-m45-2030-01-02"
	leftEvent  = "moon-m44-2030-01-03"
)

func TestRestartKeepsTheRaritiesOfLeftOutEvents(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&app.SkyEventRun{}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	shown := &Rarity{Status: StatusReady, Key: shownEvent}
	left := &Rarity{Status: StatusReady, Key: leftEvent}
	s := &Service{App: db, rarities: map[string]*Rarity{shownEvent: shown, leftEvent: left}}
	if err := s.save(ctx, &Result{ComputedAt: &now, Events: []Event{{ID: shownEvent, Rarity: shown}}}); err != nil {
		t.Fatal(err)
	}
	restarted := &Service{App: db}
	if err := restarted.Load(ctx); err != nil {
		t.Fatal(err)
	}
	got := restarted.cachedRarities()
	if len(got) != 2 || got[shownEvent] == nil || got[shownEvent].Key != shownEvent || got[leftEvent] == nil || got[leftEvent].Key != leftEvent {
		t.Fatalf("rarities after a restart: %v", got)
	}

	if err := db.Model(&app.SkyEventRun{}).Where("id = 1").Update("rarities", nil).Error; err != nil {
		t.Fatal(err)
	}
	older := &Service{App: db}
	if err := older.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if got := older.cachedRarities(); len(got) != 1 || got[shownEvent] == nil || got[shownEvent].Key != shownEvent {
		t.Fatalf("rarities from a row saved before they were kept: %v", got)
	}
}
