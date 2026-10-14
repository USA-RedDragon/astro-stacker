package stacking

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/dustcheck"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm/clause"
)

// What makes a window change sure enough to start a dust epoch on its own.
const (
	// EpochMinFilters is how many filters must show the change, unless
	// every filter checked that night does and there were at least two.
	EpochMinFilters = 3
	// EpochMinDepth is how deep, in percent, its deepest shadow must be.
	EpochMinDepth = 0.3
	// EpochMaxBracket is the most nights between the flats before and
	// after the change, so its night is known to within a week.
	EpochMaxBracket = 7
)

// proposeDustEpoch records a window change as a dust epoch starting on the
// night of the flats that first show it, unless an epoch (applied or
// proposed) already starts between the flats before and those. It is
// applied at once when the change shows in EpochMinFilters filters (or in
// every filter checked, at least two), is at least EpochMinDepth deep, and
// the flats before it are at most EpochMaxBracket nights older; otherwise
// it is stored as Proposed for a person to confirm.
func (p *Pipeline) proposeDustEpoch(ctx context.Context, night time.Time, g dustcheck.Group, members []app.DustEvent) error {
	if len(members) == 0 {
		return nil
	}
	db := p.db.WithContext(ctx)
	var before time.Time
	depth := 0.0
	for _, m := range members {
		depth = math.Max(depth, math.Abs(m.Depth))
		var prev app.DustFlat
		if err := db.Where("id = ?", m.PrevID).Limit(1).Find(&prev).Error; err != nil {
			return err
		}
		if prev.Night != nil && prev.Night.After(before) {
			before = *prev.Night
		}
	}
	var n int64
	if err := db.Model(&app.DustEpoch{}).Where("start > ? AND start <= ?", before, night).Count(&n).Error; err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	var checked []string
	if err := db.Model(&app.DustFlat{}).Where("night = ? AND prev_id IS NOT NULL", night).Distinct("filter").Pluck("filter", &checked).Error; err != nil {
		return err
	}
	filters := g.Filters()
	enough := len(filters) >= EpochMinFilters || (len(filters) >= 2 && len(filters) >= len(checked))
	bracket := int(math.Round(night.Sub(before).Hours() / 24))
	sure := enough && depth >= EpochMinDepth && !before.IsZero() && bracket <= EpochMaxBracket
	first := members[0]
	note := fmt.Sprintf("Proposed by the flat dust check: a window shadow %s at x %d, y %d, %.2f%% deep, %d px, in %s; flats before it are from %s, %d nights earlier.",
		first.Class, first.X, first.Y, depth, first.Diameter, strings.Join(filters, ", "), before.Format(time.DateOnly), bracket)
	if !sure {
		note += " Not applied: it needs at least three filters (or every filter checked), 0.3% depth and flats before it at most 7 nights older; date it from the lights and clear Proposed to apply it."
	}
	res := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&app.DustEpoch{Start: night, Note: note, Proposed: !sure})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected > 0 {
		slog.Warn("Dust epoch boundary from a window dust change", "start", night.Format(time.DateOnly), "applied", sure,
			"filters", strings.Join(filters, ","), "depth", fmt.Sprintf("%.2f%%", depth), "nights_since_flats", bracket)
	}
	return nil
}
