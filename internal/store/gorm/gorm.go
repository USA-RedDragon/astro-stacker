package gorm

import (
	"context"
	"fmt"
	"github.com/USA-RedDragon/astro-stacker/internal/frameheader"
	"log"
	"os"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/config"
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
		app.CalibrationMaster{}, app.TargetReference{}, app.Stack{}, app.StackFrame{}, app.Mosaic{}, app.Cover{}, app.ReferenceReset{})
	if err != nil {
		return nil, err
	}
	if err := normalizeFilters(store.db); err != nil {
		return nil, err
	}
	return store, nil
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
