package store

import (
	"context"

	"github.com/USA-RedDragon/astro-stacker/internal/config"
	"github.com/USA-RedDragon/astro-stacker/internal/store/gorm"
	"github.com/USA-RedDragon/astro-stacker/internal/types"
	gormlib "gorm.io/gorm"
)

type Store interface {
	WithContext(ctx context.Context) Store
	DB() *gormlib.DB
}

type gormStore struct {
	*gorm.Gorm
}

func (g *gormStore) WithContext(ctx context.Context) Store {
	return &gormStore{
		Gorm: g.Gorm.WithContext(ctx),
	}
}

func NewAppStore(cfg *config.Config) (Store, error) {
	switch cfg.Storage.Type {
	case types.StorageTypeSQLite, types.StorageTypeMySQL, types.StorageTypePostgres:
		g, err := gorm.NewAppGormStore(cfg)
		if err != nil {
			return nil, err
		}
		return &gormStore{Gorm: g}, nil
	default:
		return nil, config.ErrInvalidStorageType
	}
}

func NewSchedulerDBStore(cfg *config.Config) (Store, error) {
	switch cfg.Storage.Type {
	case types.StorageTypeSQLite, types.StorageTypeMySQL, types.StorageTypePostgres:
		g, err := gorm.NewSchedulerDBGormStore(cfg)
		if err != nil {
			return nil, err
		}
		return &gormStore{Gorm: g}, nil
	default:
		return nil, config.ErrInvalidStorageType
	}
}

// OpenReadOnly opens the database at dsn without migrating it. On
// PostgreSQL it holds one connection whose transactions are read only.
func OpenReadOnly(typ types.StorageType, dsn string) (Store, error) {
	g, err := gorm.NewGormStore(typ, dsn)
	if err != nil {
		return nil, err
	}
	if typ == types.StorageTypePostgres {
		sqlDB, err := g.DB().DB()
		if err != nil {
			return nil, err
		}
		sqlDB.SetMaxOpenConns(1)
		sqlDB.SetMaxIdleConns(1)
		if err := g.DB().Exec("SET SESSION CHARACTERISTICS AS TRANSACTION READ ONLY").Error; err != nil {
			return nil, err
		}
	}
	return &gormStore{Gorm: g}, nil
}
