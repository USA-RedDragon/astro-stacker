package store

import (
	"context"

	"github.com/USA-RedDragon/pixinsight-worker/internal/config"
	"github.com/USA-RedDragon/pixinsight-worker/internal/store/gorm"
	"github.com/USA-RedDragon/pixinsight-worker/internal/types"
)

type Store interface {
	WithContext(ctx context.Context) Store
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
