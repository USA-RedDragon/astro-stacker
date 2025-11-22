package utils

import (
	"errors"
	"fmt"

	"github.com/USA-RedDragon/pixinsight-worker/internal/types"
	"github.com/glebarez/sqlite"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestDSN(storageType types.StorageType, dsn string) error {
	var dialect gorm.Dialector
	switch storageType {
	case types.StorageTypeSQLite:
		dialect = sqlite.Open(dsn)
	case types.StorageTypePostgres:
		dialect = postgres.Open(dsn)
	case types.StorageTypeMySQL:
		dialect = mysql.Open(dsn)
	default:
		return errors.New("invalid storage type provided")
	}

	_, err := gorm.Open(dialect, &gorm.Config{})
	if err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}

	return nil
}
