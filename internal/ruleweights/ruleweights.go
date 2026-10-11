// Package ruleweights stores the global rule weight set that every Target
// Scheduler project is kept equal to. Target Scheduler keeps rule weights per
// project, so the set is saved here and copied to each project's rows.
package ruleweights

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/store/tables"
	"gorm.io/gorm"
)

const (
	Novelty        = "Novelty"
	NoveltyMosaic  = "Novelty (mosaic)"
	FilterSteering = "Filter Steering"
	PanelDeficit   = "Panel Deficit"
	EvenPanels     = "Even Panels"
	Tolerance      = 1e-6
)

var ErrStale = errors.New("the global rule weights changed since this was prepared")

// Keys lists every weight in the global set in display order. Novelty is the
// value for single-target projects and NoveltyMosaic the Novelty value for
// mosaic projects; every other key is a Target Scheduler rule name.
func Keys() []string {
	return []string{
		"Project Priority", "Setting Soonest", "Percent Complete", "Target Switch Penalty",
		"Mosaic Completion", PanelDeficit, EvenPanels, "Meridian Window Priority", "Meridian Flip Penalty",
		"Smart Exposure Order", Novelty, NoveltyMosaic, "Rarity", "Condition Match",
		"Seasonal Runway", "Season Priority", FilterSteering,
	}
}

func Known(key string) bool { return slices.Contains(Keys(), key) }

// Rule is the Target Scheduler rule name a key is stored under.
func Rule(key string) string {
	if key == NoveltyMosaic {
		return Novelty
	}
	return key
}

// Key is the global key that sets rule on a project of the given kind.
func Key(rule string, mosaic bool) string {
	if rule == Novelty && mosaic {
		return NoveltyMosaic
	}
	return rule
}

func Same(a, b float64) bool { return math.Abs(a-b) <= Tolerance }

func Check(key string, w float64) error {
	if !Known(key) {
		return fmt.Errorf("%s is not a rule in the global set", key)
	}
	if math.IsNaN(w) || w < 0 || w > 100 {
		return fmt.Errorf("%s: weights run from 0 to 100", key)
	}
	return nil
}

type Saved struct {
	Weights map[string]float64
	At      *time.Time
	By      string
}

func Load(ctx context.Context, db *gorm.DB) (Saved, error) {
	out := Saved{Weights: map[string]float64{}}
	if db == nil {
		return out, nil
	}
	ok, err := tables.Has(ctx, db, &app.GlobalRuleWeight{})
	if err != nil || !ok {
		return out, err
	}
	var rows []app.GlobalRuleWeight
	if err := db.WithContext(ctx).Find(&rows).Error; err != nil {
		return out, err
	}
	for _, r := range rows {
		if !Known(r.Name) {
			continue
		}
		out.Weights[r.Name] = r.Weight
		if out.At == nil || r.SavedAt.After(*out.At) {
			at := r.SavedAt
			out.At, out.By = &at, r.SavedBy
		}
	}
	return out, nil
}

// Change sets one key. Before is the saved value the change expects, nil when
// the key is not saved yet; After nil removes the saved value.
type Change struct {
	Rule   string   `json:"rule"`
	Before *float64 `json:"before"`
	After  *float64 `json:"after"`
}

func same(a, b *float64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return Same(*a, *b)
}

func Validate(changes []Change) error {
	if len(changes) == 0 {
		return errors.New("no changes")
	}
	seen := map[string]bool{}
	for _, c := range changes {
		if c.Rule == "" || seen[c.Rule] {
			return errors.New("rule names must be set and unique")
		}
		seen[c.Rule] = true
		if !Known(c.Rule) {
			return fmt.Errorf("%s is not a rule in the global set", c.Rule)
		}
		for _, w := range []*float64{c.Before, c.After} {
			if w == nil {
				continue
			}
			if err := Check(c.Rule, *w); err != nil {
				return err
			}
		}
		if same(c.Before, c.After) {
			return fmt.Errorf("%s is unchanged", c.Rule)
		}
	}
	return nil
}

// Apply saves changes in one transaction. It fails with ErrStale when a saved
// value is no longer the one a change expects.
func Apply(ctx context.Context, db *gorm.DB, changes []Change, by string, now time.Time) error {
	if db == nil {
		return errors.New("no app database")
	}
	if err := Validate(changes); err != nil {
		return err
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, c := range changes {
			var rows []app.GlobalRuleWeight
			if err := tx.Where("name = ?", c.Rule).Find(&rows).Error; err != nil {
				return err
			}
			var current *float64
			if len(rows) > 0 {
				current = &rows[0].Weight
			}
			if !same(current, c.Before) {
				return fmt.Errorf("%w: %s", ErrStale, c.Rule)
			}
			if c.After == nil {
				if err := tx.Where("name = ?", c.Rule).Delete(&app.GlobalRuleWeight{}).Error; err != nil {
					return err
				}
				continue
			}
			row := app.GlobalRuleWeight{Name: c.Rule, Weight: *c.After, SavedBy: by, SavedAt: now}
			if err := tx.Save(&row).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
