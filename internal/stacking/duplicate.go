package stacking

import (
	"context"
	"log/slog"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
)

// Telescope.live delivered some exposures twice, the same bytes under two
// names. Stacked twice, a sub is two votes in rejection and counts double.
// A light whose object store ETag and size match an earlier light's (a
// lower id) of the same target is left out as a duplicate; the earlier
// one stands for both.

// earlierTwin is the condition, on frames aliased f, that an earlier
// light of the same target is the same file.
const earlierTwin = "EXISTS (SELECT 1 FROM frames t WHERE t.object = f.object AND t.e_tag = f.e_tag " +
	"AND t.size = f.size AND t.id < f.id AND t.type = 'LIGHT' AND t.index_error IS NULL)"

// duplicates returns which of frames are the same file as an earlier light
// of their target.
func (p *Pipeline) duplicates(ctx context.Context, frames []app.Frame) (map[int]bool, error) {
	ids := make([]int, 0, len(frames))
	for _, f := range frames {
		ids = append(ids, f.ID)
	}
	var dup []int
	if err := p.db.WithContext(ctx).Table("frames f").Where("f.id IN ?", ids).Where(earlierTwin).
		Pluck("f.id", &dup).Error; err != nil {
		return nil, err
	}
	out := make(map[int]bool, len(dup))
	for _, id := range dup {
		out[id] = true
	}
	return out, nil
}

// dropDuplicates takes duplicates stacked before they were recognised out of
// their masters and marks the masters to be stacked again, which the moon
// sweep does. A duplicate whose twin has gone from the bucket is let back
// in to be stacked on its own.
func (p *Pipeline) dropDuplicates(ctx context.Context) error {
	var stacks []int
	var n int64
	err := p.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		added := tx.Table("stack_frames sf").Joins("JOIN frames f ON f.id = sf.frame_id").
			Where("sf.status = ?", app.StackStatusAdded).Where(earlierTwin)
		var ids []int
		if err := added.Session(&gorm.Session{}).Pluck("sf.id", &ids).Error; err != nil || len(ids) == 0 {
			return err
		}
		if err := added.Session(&gorm.Session{}).Where("sf.stack_id IS NOT NULL").Distinct().
			Pluck("sf.stack_id", &stacks).Error; err != nil {
			return err
		}
		// Marked together, so a restart before the sweep still stacks
		// the masters again.
		res := tx.Model(&app.StackFrame{}).Where("id IN ?", ids).
			UpdateColumns(map[string]any{"status": app.StackStatusDuplicate, "next_attempt_at": nil})
		if res.Error != nil {
			return res.Error
		}
		n = res.RowsAffected
		return tx.Model(&app.Stack{}).Where("id IN ?", stacks).UpdateColumn("needs_rebuild", true).Error
	})
	if err != nil {
		return err
	}
	if n > 0 {
		slog.Info("Taking duplicate lights out of their masters", "lights", n, "masters", len(stacks))
	}
	orphans := p.db.WithContext(ctx).Where("status = ?", app.StackStatusDuplicate).
		Where("frame_id NOT IN (?)", p.db.Table("frames f").Select("f.id").Where(earlierTwin)).
		Delete(&app.StackFrame{})
	if orphans.Error != nil {
		return orphans.Error
	}
	if orphans.RowsAffected > 0 {
		slog.Info("Duplicates' twins are gone; stacking them", "lights", orphans.RowsAffected)
	}
	return nil
}
