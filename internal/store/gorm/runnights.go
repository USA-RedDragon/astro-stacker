package gorm

import (
	"log/slog"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
)

// mergeRunNights readies the stored run nights for the run key: it drops
// the old one row per project and night index and keeps one row of each
// run, the one under the request that is still live, removing the rows a
// cancelled or moved project of the same run stored.
func mergeRunNights(db *gorm.DB) error {
	m := db.Migrator()
	if !m.HasTable(&app.RunNight{}) {
		return nil
	}
	if m.HasIndex(&app.RunNight{}, "idx_run_night") {
		if err := m.DropIndex(&app.RunNight{}, "idx_run_night"); err != nil {
			return err
		}
	}
	var rows []app.RunNight
	if err := db.Select("id", "project_guid", "source_id", "night", "kind", "first", "last", "subs", "judged_at").Order("id").Find(&rows).Error; err != nil {
		return err
	}
	stale, err := app.StaleRunGUIDs(db)
	if err != nil {
		return err
	}
	_, drop := app.MergeRunNights(rows, stale)
	if len(drop) == 0 {
		return nil
	}
	slog.Info("Merged stored run nights stored twice for one run", "removed", drop)
	return db.Delete(&app.RunNight{}, drop).Error
}
