package ruleweights_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/ruleweights"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const rarity = "Rarity"

func f(v float64) *float64 { return &v }

func memDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestKeysMapToRules(t *testing.T) {
	t.Parallel()
	if ruleweights.Rule(ruleweights.NoveltyMosaic) != ruleweights.Novelty || ruleweights.Rule(rarity) != rarity {
		t.Fatal("Novelty (mosaic) is stored as Novelty")
	}
	if ruleweights.Key(ruleweights.Novelty, true) != ruleweights.NoveltyMosaic || ruleweights.Key(ruleweights.Novelty, false) != ruleweights.Novelty || ruleweights.Key(rarity, true) != rarity {
		t.Fatal("only Novelty splits by project kind")
	}
	if !ruleweights.Known(ruleweights.FilterSteering) || ruleweights.Known("Made Up") {
		t.Fatal("known keys")
	}
}

func TestLoadWithoutTableOrDB(t *testing.T) {
	t.Parallel()
	for _, db := range []*gorm.DB{nil, memDB(t)} {
		s, err := ruleweights.Load(context.Background(), db)
		if err != nil || len(s.Weights) != 0 || s.At != nil {
			t.Fatalf("%+v %v", s, err)
		}
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()
	bad := [][]ruleweights.Change{
		nil,
		{{Rule: "Made Up", After: f(1)}},
		{{Rule: rarity, After: f(101)}},
		{{Rule: rarity, Before: f(-1), After: f(1)}},
		{{Rule: rarity, Before: f(5), After: f(5)}},
		{{Rule: rarity}},
		{{Rule: rarity, After: f(1)}, {Rule: rarity, After: f(2)}},
		{{Rule: "", After: f(1)}},
	}
	for i, c := range bad {
		if ruleweights.Validate(c) == nil {
			t.Errorf("case %d should fail", i)
		}
	}
	if err := ruleweights.Validate([]ruleweights.Change{{Rule: ruleweights.NoveltyMosaic, After: f(5)}, {Rule: rarity, Before: f(20), After: nil}}); err != nil {
		t.Fatal(err)
	}
}

func TestApplySavesChecksAndRemoves(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := memDB(t)
	if err := db.AutoMigrate(&app.GlobalRuleWeight{}); err != nil {
		t.Fatal(err)
	}
	t1 := time.Date(2026, 10, 11, 8, 0, 0, 0, time.UTC)
	if err := ruleweights.Apply(ctx, db, []ruleweights.Change{{Rule: ruleweights.Novelty, After: f(35)}, {Rule: ruleweights.NoveltyMosaic, After: f(5)}}, "jacob", t1); err != nil {
		t.Fatal(err)
	}
	s, err := ruleweights.Load(ctx, db)
	if err != nil || s.Weights[ruleweights.Novelty] != 35 || s.Weights[ruleweights.NoveltyMosaic] != 5 || s.By != "jacob" || !s.At.Equal(t1) {
		t.Fatalf("%+v %v", s, err)
	}
	err = ruleweights.Apply(ctx, db, []ruleweights.Change{{Rule: ruleweights.Novelty, Before: f(10), After: f(40)}}, "web", t1)
	if !errors.Is(err, ruleweights.ErrStale) {
		t.Fatalf("a stale before should fail: %v", err)
	}
	err = ruleweights.Apply(ctx, db, []ruleweights.Change{{Rule: ruleweights.Novelty, Before: f(35), After: f(40)}, {Rule: rarity, Before: f(1), After: f(2)}}, "web", t1)
	if !errors.Is(err, ruleweights.ErrStale) {
		t.Fatalf("a missing row expects no before: %v", err)
	}
	if s, _ = ruleweights.Load(ctx, db); s.Weights[ruleweights.Novelty] != 35 {
		t.Fatalf("a failed apply must change nothing: %+v", s)
	}
	t2 := t1.Add(time.Hour)
	if err := ruleweights.Apply(ctx, db, []ruleweights.Change{{Rule: ruleweights.Novelty, Before: f(35), After: f(40)}, {Rule: ruleweights.NoveltyMosaic, Before: f(5)}}, "web", t2); err != nil {
		t.Fatal(err)
	}
	s, _ = ruleweights.Load(ctx, db)
	if _, ok := s.Weights[ruleweights.NoveltyMosaic]; ok || s.Weights[ruleweights.Novelty] != 40 || s.By != "web" || !s.At.Equal(t2) {
		t.Fatalf("%+v", s)
	}
	if err := ruleweights.Apply(ctx, nil, []ruleweights.Change{{Rule: rarity, After: f(1)}}, "", t2); err == nil {
		t.Fatal("no database should fail")
	}
}
