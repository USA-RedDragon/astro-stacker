// Package schedglobal stores the global scheduling settings that every
// Target Scheduler project is kept equal to. Target Scheduler keeps them per
// project, so the set is saved here and copied to each project.
package schedglobal

import (
	"context"
	"errors"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/store/tables"
	"gorm.io/gorm"
)

const rowID = 1

var ErrStale = errors.New("the global scheduling settings changed since this was prepared")

// Settings are the scheduling settings every project takes.
type Settings struct {
	DitherEvery           int     `json:"ditherEvery"`
	FilterSwitchFrequency int     `json:"filterSwitchFrequency"`
	FlatsHandling         int     `json:"flatsHandling"`
	SmartExposureOrder    bool    `json:"smartExposureOrder"`
	MeridianWindow        int     `json:"meridianWindow"`
	MaximumAltitude       float64 `json:"maximumAltitude"`
	EnableGrader          bool    `json:"enableGrader"`
}

// Defaults are the settings until the global set is first saved: the
// values chosen on 2026-10-12, which are also what most projects had.
func Defaults() Settings {
	return Settings{DitherEvery: 2, FilterSwitchFrequency: 1, FlatsHandling: 1, EnableGrader: true}
}

// Saved is the saved set, nil when nothing is saved yet.
type Saved struct {
	Settings *Settings
	At       *time.Time
	By       string
}

func rowOf(s Settings) app.GlobalSchedulingSetting {
	return app.GlobalSchedulingSetting{ID: rowID, DitherEvery: s.DitherEvery, FilterSwitchFrequency: s.FilterSwitchFrequency,
		FlatsHandling: s.FlatsHandling, SmartExposureOrder: s.SmartExposureOrder, MeridianWindow: s.MeridianWindow,
		MaximumAltitude: s.MaximumAltitude, EnableGrader: s.EnableGrader}
}

func settingsOf(r app.GlobalSchedulingSetting) Settings {
	return Settings{DitherEvery: r.DitherEvery, FilterSwitchFrequency: r.FilterSwitchFrequency, FlatsHandling: r.FlatsHandling,
		SmartExposureOrder: r.SmartExposureOrder, MeridianWindow: r.MeridianWindow, MaximumAltitude: r.MaximumAltitude, EnableGrader: r.EnableGrader}
}

func load(ctx context.Context, db *gorm.DB) (Saved, error) {
	var rows []app.GlobalSchedulingSetting
	if err := db.WithContext(ctx).Where("id = ?", rowID).Limit(1).Find(&rows).Error; err != nil {
		return Saved{}, err
	}
	if len(rows) == 0 {
		return Saved{}, nil
	}
	s := settingsOf(rows[0])
	at := rows[0].SavedAt
	return Saved{Settings: &s, At: &at, By: rows[0].SavedBy}, nil
}

// Load reads the saved set; nothing saved when the table is missing.
func Load(ctx context.Context, db *gorm.DB) (Saved, error) {
	if db == nil {
		return Saved{}, nil
	}
	ok, err := tables.Has(ctx, db, &app.GlobalSchedulingSetting{})
	if err != nil || !ok {
		return Saved{}, err
	}
	return load(ctx, db)
}

// Current is the saved set, or Defaults when nothing is saved.
func (s Saved) Current() Settings {
	if s.Settings != nil {
		return *s.Settings
	}
	return Defaults()
}

// Apply saves after in one transaction when the saved set is still before
// (nil: nothing saved). A nil after removes the saved set, so the defaults
// apply again. It fails with ErrStale when the saved set changed.
func Apply(ctx context.Context, db *gorm.DB, before, after *Settings, by string, now time.Time) error {
	if db == nil {
		return errors.New("no app database")
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		cur, err := load(ctx, tx)
		if err != nil {
			return err
		}
		if !same(cur.Settings, before) {
			return ErrStale
		}
		if after == nil {
			return tx.Where("id = ?", rowID).Delete(&app.GlobalSchedulingSetting{}).Error
		}
		row := rowOf(*after)
		row.SavedBy, row.SavedAt = by, now
		return tx.Save(&row).Error
	})
}

func same(a, b *Settings) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
