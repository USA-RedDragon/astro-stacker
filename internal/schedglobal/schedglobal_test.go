package schedglobal_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/schedglobal"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestSaveChangeAndRemove(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if s, err := schedglobal.Load(ctx, db); err != nil || s.Settings != nil || s.Current() != schedglobal.Defaults() {
		t.Fatalf("no table: %+v %v", s, err)
	}
	if err := db.AutoMigrate(&app.GlobalSchedulingSetting{}); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	a := schedglobal.Defaults()
	a.DitherEvery = 3
	if err := schedglobal.Apply(ctx, db, nil, &a, "web", at); err != nil {
		t.Fatal(err)
	}
	s, err := schedglobal.Load(ctx, db)
	if err != nil || s.Settings == nil || *s.Settings != a || s.By != "web" || !s.At.Equal(at) {
		t.Fatalf("saved %+v %v", s, err)
	}
	if err := schedglobal.Apply(ctx, db, nil, &a, "web", at); !errors.Is(err, schedglobal.ErrStale) {
		t.Errorf("save over a saved set that was expected missing: %v", err)
	}
	b := a
	b.FlatsHandling = 0
	if err := schedglobal.Apply(ctx, db, &a, &b, "web", at); err != nil {
		t.Fatal(err)
	}
	if err := schedglobal.Apply(ctx, db, &b, nil, "web", at); err != nil {
		t.Fatal(err)
	}
	if s, _ := schedglobal.Load(ctx, db); s.Settings != nil || s.Current() != schedglobal.Defaults() {
		t.Errorf("removed set: %+v", s)
	}
	if schedglobal.Apply(ctx, nil, nil, &a, "web", at) == nil {
		t.Error("no database")
	}
}
