package types

type StorageType string

const (
	StorageTypeMySQL    StorageType = "mysql"
	StorageTypePostgres StorageType = "postgres"
	StorageTypeSQLite   StorageType = "sqlite"
)
