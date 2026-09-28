package config

import (
	"errors"

	"github.com/USA-RedDragon/astro-stacker/internal/store/utils"
	"github.com/USA-RedDragon/astro-stacker/internal/types"
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
	S3       S3       `name:"s3" description:"Object store holding the raw frames"`
	Indexer  Indexer  `name:"indexer" description:"Frame indexer configuration"`
	Previews Previews `name:"previews" description:"Sub preview rendering"`
	Stacking Stacking `name:"stacking" description:"Calibrating, registering and stacking lights into one master per target and filter"`
}

type Stacking struct {
	Enabled         bool    `name:"enabled" description:"Stack good lights into masters as they arrive"`
	IntervalSeconds int     `name:"interval-seconds" description:"Seconds between checks for new lights when idle" default:"120"`
	MinScore        float64 `name:"min-score" description:"Lowest sub score (0-1) that goes into a master" default:"0.3"`
	BatchSize       int     `name:"batch-size" description:"Subs calibrated and registered per Siril run" default:"12"`
	WorkDir         string  `name:"work-dir" description:"Scratch space for downloads, masters and Siril output" default:"/tmp/stacking"`
	SirilCommand    string  `name:"siril-command" description:"siril-cli, or an extracted Siril AppImage's AppRun" default:"siril-cli"`
	SirilThreads    int     `name:"siril-threads" description:"Threads Siril may use" default:"4"`
	SirilMemory     float64 `name:"siril-memory" description:"Share of memory all Siril runs together may use (Siril reads the container limit); registration needs about 320 MiB per thread" default:"0.5"`
	Workers         int     `name:"workers" description:"Targets stacked at once; each holds up to about 1.5 GB besides Siril" default:"1"`
	MosaicMinutes   int     `name:"mosaic-minutes" description:"Minutes between checks for mosaics to build from panel masters; 0 turns mosaics off" default:"10"`
	MosaicQuiet     int     `name:"mosaic-quiet-minutes" description:"Minutes a mosaic's panel masters must be unchanged before it is rebuilt" default:"30"`
	Pedestal        float64 `name:"pedestal" description:"Camera pedestal in ADU, for scoring subs" default:"506"`
}

type Previews struct {
	Enabled         bool `name:"enabled" description:"Render auto-stretched JPEG previews of lights into the processed bucket"`
	IntervalSeconds int  `name:"interval-seconds" description:"Seconds between checks for frames without previews" default:"120"`
	Concurrency     int  `name:"concurrency" description:"Frames rendered in parallel; each needs about 200 MB" default:"2"`
	MaxWidth        int  `name:"max-width" description:"Preview width in pixels" default:"1280"`
	Quality         int  `name:"quality" description:"JPEG quality" default:"80"`
	URLTTLSeconds   int  `name:"url-ttl-seconds" description:"Lifetime of presigned preview URLs" default:"3600"`
}

type S3 struct {
	Endpoint  string `name:"endpoint" description:"S3 endpoint host, without scheme" default:"s3.mcswain.dev"`
	UseSSL    bool   `name:"use-ssl" description:"Use HTTPS for the S3 endpoint" default:"true"`
	Region    string `name:"region" description:"S3 region" default:"us-east-1"`
	Bucket    string `name:"bucket" description:"Bucket holding the raw frames" default:"astro"`
	AccessKey string `name:"access-key" description:"S3 access key"`
	SecretKey string `name:"secret-key" description:"S3 secret key"`
	// Processed outputs (previews, calibrated subs, stacks) go here.
	ProcessedBucket string `name:"processed-bucket" description:"Bucket for previews and processed frames" default:"astro-processed"`
	// Browsers fetch presigned URLs, so they must be signed for the public
	// host, not the in-cluster endpoint the worker itself uses.
	PublicEndpoint string `name:"public-endpoint" description:"S3 host browsers use for presigned URLs" default:"s3.mcswain.dev"`
	PublicUseSSL   bool   `name:"public-use-ssl" description:"Presigned URLs use HTTPS" default:"true"`
}

type Indexer struct {
	Enabled         bool `name:"enabled" description:"Index frame headers from the bucket"`
	IntervalSeconds int  `name:"interval-seconds" description:"Seconds between bucket scans" default:"600"`
	Concurrency     int  `name:"concurrency" description:"Headers to fetch in parallel" default:"8"`
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
	Type types.StorageType `name:"type" description:"Storage type. One of mysql, postgres, sqlite" default:"sqlite"`
	// SchedulerDBType lets the scheduler database use a different engine
	// from the app database, e.g. a local SQLite app database against the
	// cluster's Postgres. Empty means the same as Type.
	SchedulerDBType types.StorageType `name:"schedulerdb-type" description:"Storage type of the scheduler database, if different from type"`
	DSN             DSN               `name:"dsn" description:"Data source names for the storage"`
}

type DSN struct {
	App         string `name:"app" description:"Data source name for the application storage" default:":memory:?_pragma=foreign_keys(1)"`
	SchedulerDB string `name:"schedulerdb" description:"Data source name for the scheduler database" default:":memory:?_pragma=foreign_keys(1)"`
}

var (
	ErrMissingS3Credentials         = errors.New("s3 access and secret keys are required when the indexer is enabled")
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

	if c.Storage.DSN.App == "" {
		return ErrEmptyStorageDSNApp
	}

	if (c.Indexer.Enabled || c.Previews.Enabled || c.Stacking.Enabled) && (c.S3.AccessKey == "" || c.S3.SecretKey == "") {
		return ErrMissingS3Credentials
	}

	if err := utils.TestDSN(c.Storage.Type, c.Storage.DSN.App); err != nil {
		return ErrInvalidStorageDSNApp
	}

	if c.Storage.DSN.SchedulerDB == "" {
		return ErrEmptyStorageDSNSchedulerDB
	}

	schedType := c.Storage.Type
	if c.Storage.SchedulerDBType != "" {
		schedType = c.Storage.SchedulerDBType
	}
	if err := utils.TestDSN(schedType, c.Storage.DSN.SchedulerDB); err != nil {
		return ErrInvalidStorageDSNSchedulerDB
	}

	return nil
}
