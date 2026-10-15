package gorm

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/config"
	"github.com/USA-RedDragon/astro-stacker/internal/frameheader"
	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/types"
	"github.com/glebarez/sqlite"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type Gorm struct {
	db *gorm.DB
}

func NewAppGormStore(cfg *config.Config) (*Gorm, error) {
	store, err := NewGormStore(cfg.Storage.Type, cfg.Storage.DSN.App)
	if err != nil {
		return nil, err
	}
	err = store.db.AutoMigrate(app.ImageProcess{}, app.PreStackedImage{}, app.Frame{},
		app.CalibrationMaster{}, app.TargetReference{}, app.Stack{}, app.StackFrame{}, app.Mosaic{}, app.Cover{}, app.ReferenceReset{},
		app.TSVerdict{}, app.PublicFrame{}, app.RegisteredOrphan{}, app.GoalMeasurement{}, app.GoalMask{}, schedcmd.Record{},
		app.MosaicPanel{}, app.MosaicAdoption{}, app.FrameTarget{}, app.MosaicSeam{}, app.MosaicPanelHealth{}, app.GaiaField{}, app.XPField{}, app.SkySample{}, app.HAlphaMap{}, app.SkyCutout{},
		app.GlobalRuleWeight{}, app.CalibrationRequest{}, app.HorizonsCache{}, app.CometElements{}, app.SkyEventRun{}, app.SkyEventChoice{}, app.BodyNight{}, app.NightReference{}, app.GlobalSchedulingSetting{}, app.SchedulingOriginal{})
	if err != nil {
		return nil, err
	}
	if err := store.db.AutoMigrate(app.ObjectXref{}, app.StarfrontCache{}, app.CollabSwitch{}); err != nil {
		return nil, err
	}
	if err := store.db.AutoMigrate(app.ObservatoryContact{}, app.ObservatoryHorizon{}, app.MorningReport{}); err != nil {
		return nil, err
	}
	if err := store.db.AutoMigrate(app.DiscoveryRun{}, app.DiscoveryCandidate{}); err != nil {
		return nil, err
	}
	if err := store.db.AutoMigrate(app.DiscoveryAlert{}, app.DiscoveryAlertMessage{}, app.DiscoveryAlertState{}, app.DiscoveryFollowup{}, app.DiscoveryReport{}); err != nil {
		return nil, err
	}
	if err := store.db.AutoMigrate(app.DustEpoch{}); err != nil {
		return nil, err
	}
	if err := seedDustEpochs(store.db); err != nil {
		return nil, err
	}
	if err := store.db.AutoMigrate(app.DustFlat{}, app.DustEvent{}, app.DustLightSub{}, app.DustLightCheck{}); err != nil {
		return nil, err
	}
	if err := normalizeFilters(store.db); err != nil {
		return nil, err
	}
	return store, nil
}

// seedDustEpochs fills an empty dust epoch table with the boundaries dated
// from the lights by the 2026-10 dust study. It leaves a table with rows as
// it is, so rows edited or removed by hand stay that way.
func seedDustEpochs(db *gorm.DB) error {
	var n int64
	if err := db.Model(&app.DustEpoch{}).Count(&n).Error; err != nil || n > 0 {
		return err
	}
	return db.Create(&[]app.DustEpoch{
		{Start: time.Date(2026, 4, 16, 0, 0, 0, 0, time.UTC), Fallback: dustSeedAprilFallback, Note: dustSeedAprilNote},
		{Start: time.Date(2026, 5, 28, 0, 0, 0, 0, time.UTC), Note: dustSeedMayNote},
	}).Error
}

// normalizeFilters renames filters indexed before their names were
// normalized (frameheader.NormalizeFilter).
func normalizeFilters(db *gorm.DB) error {
	var names []string
	if err := db.Model(&app.Frame{}).Distinct("filter").Pluck("filter", &names).Error; err != nil {
		return err
	}
	for _, n := range names {
		if to := frameheader.NormalizeFilter(n); to != n {
			if err := db.Model(&app.Frame{}).Where("filter = ?", n).UpdateColumn("filter", to).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

func NewSchedulerDBGormStore(cfg *config.Config) (*Gorm, error) {
	typ := cfg.Storage.Type
	if cfg.Storage.SchedulerDBType != "" {
		typ = cfg.Storage.SchedulerDBType
	}
	return NewGormStore(typ, cfg.Storage.DSN.SchedulerDB)
}

func NewGormStore(storageType types.StorageType, dsn string) (*Gorm, error) {
	var dialect gorm.Dialector
	switch storageType {
	case types.StorageTypeSQLite:
		dialect = sqlite.Open(dsn)
	case types.StorageTypePostgres:
		dialect = postgres.Open(dsn)
	case types.StorageTypeMySQL:
		dialect = mysql.Open(dsn)
	default:
		return nil, config.ErrInvalidStorageType
	}

	db, err := gorm.Open(dialect, &gorm.Config{
		// Lookups that find nothing are normal here, not errors.
		Logger: logger.New(log.New(os.Stderr, "", log.LstdFlags), logger.Config{
			SlowThreshold:             time.Second,
			LogLevel:                  logger.Warn,
			IgnoreRecordNotFoundError: true,
		}),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	return &Gorm{
		db: db,
	}, nil
}

func (g *Gorm) WithContext(ctx context.Context) *Gorm {
	return &Gorm{
		db: g.db.WithContext(ctx),
	}
}

// DB exposes the underlying connection for packages that run their own queries.
func (g *Gorm) DB() *gorm.DB {
	return g.db
}

const (
	dustSeedAprilFallback = "previous"
	dustSeedAprilNote     = "Disc B, about 520 px across and 1.1 % deep near x 2610, y 1900, came between the nights of 2026-04-08 (clean) " +
		"and 2026-04-16 (shadow present); no lights were taken between, so any start in that span splits the lights the same way. " +
		"No flats were taken in this epoch. On its lights the 2025-05-13 flats left a mean of -0.16 % (rms 0.46 %) in the dust, " +
		"the 2026-09-28 flats +0.45 % (rms 0.59 %) as they add bright copies of the later shadows, so the earlier epoch's flats " +
		"are the fallback; Red, Green, Blue and H-a get synthesized flats instead (coverage dustBDir), which left +0.11 % (rms 0.35 %)."
	dustSeedMayNote = "Disc A, overlapping disc B below it, and disc C near x 4830, y 2500 (0.35 % deep) came during the night of " +
		"2026-05-27: the overlap darkened from -0.1 % to -0.9 % through that night and kept deepening to its full depth by 2026-06-01. " +
		"The boundary is the first night with most of it; nights 2026-05-27 to 2026-06-01 hold partial shadows that no flat matches."
)
