package gorm

import (
	"context"
	"fmt"

	"github.com/USA-RedDragon/pixinsight-worker/internal/config"
	"github.com/USA-RedDragon/pixinsight-worker/internal/store/models/app"
	"github.com/USA-RedDragon/pixinsight-worker/internal/types"
	"github.com/glebarez/sqlite"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type Gorm struct {
	db *gorm.DB
}

func NewAppGormStore(cfg *config.Config) (*Gorm, error) {
	store, err := NewGormStore(cfg.Storage.Type, cfg.Storage.DSNApp)
	if err != nil {
		return nil, err
	}
	err = store.db.AutoMigrate(app.ImageProcess{}, app.PreStackedImage{})
	if err != nil {
		return nil, err
	}
	return store, nil
}

func NewSchedulerDBGormStore(cfg *config.Config) (*Gorm, error) {
	return NewGormStore(cfg.Storage.Type, cfg.Storage.DSNSchedulerDB)
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

	db, err := gorm.Open(dialect, &gorm.Config{})
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
