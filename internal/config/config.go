package config

import (
	"errors"

	"github.com/USA-RedDragon/pixinsight-worker/internal/store/utils"
	"github.com/USA-RedDragon/pixinsight-worker/internal/types"
)

type LogLevel string

const (
	LogLevelDebug LogLevel = "debug"
	LogLevelInfo  LogLevel = "info"
	LogLevelWarn  LogLevel = "warn"
	LogLevelError LogLevel = "error"
)

type Config struct {
	LogLevel LogLevel `name:"log-level" description:"Logging level for the application. One of debug, info, warn, or error" default:"info"`
	HTTP     HTTP     `name:"http" description:"HTTP server configuration"`
	Metrics  Metrics  `name:"metrics" description:"Metrics server configuration"`
	PProf    PProf    `name:"pprof" description:"PProf server configuration"`
	Storage  Storage  `name:"storage" description:"Storage configuration"`
}

type HTTP struct {
	Bind           string   `name:"bind" description:"Address to listen on" default:"[::]"`
	Port           int      `name:"port" description:"Port to listen on" default:"8080"`
	TrustedProxies []string `name:"trusted-proxies" description:"Trusted proxies for the HTTP server"`
}

type Metrics struct {
	Enabled bool   `name:"enabled" description:"Enable metrics server"`
	Bind    string `name:"bind" description:"Address to listen on" default:"127.0.0.1"`
	Port    int    `name:"port" description:"Port to listen on" default:"9000"`
}

type PProf struct {
	Enabled bool   `name:"enabled" description:"Enable pprof server"`
	Bind    string `name:"bind" description:"Address to listen on" default:"127.0.0.1"`
	Port    int    `name:"port" description:"Port to listen on" default:"9999"`
}

type Storage struct {
	Type           types.StorageType `name:"type" description:"Storage type. One of mysql, postgres, sqlite" default:"sqlite"`
	DSNApp         string            `name:"dsn_app" description:"Data source name for the application storage" default:":memory:?_pragma=foreign_keys(1)"`
	DSNSchedulerDB string            `name:"dsn_schedulerdb" description:"Data source name for the scheduler database" default:":memory:?_pragma=foreign_keys(1)"`
}

var (
	ErrInvalidLogLevel              = errors.New("invalid log level provided")
	ErrInvalidStorageType           = errors.New("invalid storage type provided")
	ErrEmptyStorageDSNApp           = errors.New("application storage DSN cannot be empty")
	ErrEmptyStorageDSNSchedulerDB   = errors.New("scheduler database DSN cannot be empty")
	ErrInvalidStorageDSNApp         = errors.New("invalid application storage DSN provided")
	ErrInvalidStorageDSNSchedulerDB = errors.New("invalid scheduler database DSN provided")
)

func (c Config) Validate() error {
	if c.LogLevel != LogLevelDebug &&
		c.LogLevel != LogLevelInfo &&
		c.LogLevel != LogLevelWarn &&
		c.LogLevel != LogLevelError {
		return ErrInvalidLogLevel
	}

	if c.Storage.Type != types.StorageTypeMySQL &&
		c.Storage.Type != types.StorageTypePostgres &&
		c.Storage.Type != types.StorageTypeSQLite {
		return ErrInvalidStorageType
	}

	if c.Storage.DSNApp == "" {
		return ErrEmptyStorageDSNApp
	}

	if err := utils.TestDSN(c.Storage.Type, c.Storage.DSNApp); err != nil {
		return ErrInvalidStorageDSNApp
	}

	if c.Storage.DSNSchedulerDB == "" {
		return ErrEmptyStorageDSNSchedulerDB
	}

	if err := utils.TestDSN(c.Storage.Type, c.Storage.DSNSchedulerDB); err != nil {
		return ErrInvalidStorageDSNSchedulerDB
	}

	return nil
}
