// Package tsmigration reads the plugin's template migration: which plans
// moved from their own exposure template to their filter's managed one.
package tsmigration

import (
	"context"
	"fmt"

	"github.com/USA-RedDragon/astro-stacker/internal/store/tables"
	"gorm.io/gorm"
)

// Table records each plan's moves to a managed template, one row per
// period; reverted_at is set when the plan moves back.
const Table = "ts_template_migration"

// OldDefaults is, by plan id, the default exposure of the template a plan
// had before its open migration. A managed template's default is the
// filter type's, which can be shorter than what the plan imaged at, so
// estimates keep the old one. Empty without the table.
func OldDefaults(ctx context.Context, sched *gorm.DB) (map[int]float64, error) {
	out := map[int]float64{}
	if sched == nil {
		return out, nil
	}
	has, err := tables.Has(ctx, sched, Table)
	if err != nil || !has {
		return out, err
	}
	var rows []struct {
		PlanID  int      `gorm:"column:plan_id"`
		Default *float64 `gorm:"column:defaultexposure"`
	}
	if err := sched.WithContext(ctx).Table(Table + " m").Select("m.plan_id AS plan_id, old.defaultexposure AS defaultexposure").
		Joins(`JOIN exposuretemplate old ON old."Id" = m.old_template_id`).
		Where("m.reverted_at IS NULL").Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("load template migrations: %w", err)
	}
	for _, r := range rows {
		if r.Default != nil && *r.Default > 0 {
			out[r.PlanID] = *r.Default
		}
	}
	return out, nil
}
