package config

//go:generate go tool configulator -type Config

import (
	"errors"
	"time"

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
	// PublicFrames are the watermarked frames the public site shows.
	PublicFrames PublicFrames `name:"public-frames" description:"Small watermarked frames of each target's newest accepted light, for the public site"`
	Discover     Discover     `name:"discover" description:"Catalogue completion, the target finder and Starfront collaborations"`
}

type Discover struct {
	SiteLatitude     float64  `name:"site-latitude" description:"Observatory latitude in degrees; with site-longitude 0 too, the site is read from the newest light's FITS header (SITELAT, SITELONG)"`
	SiteLongitude    float64  `name:"site-longitude" description:"Observatory east longitude in degrees"`
	SiteElevation    float64  `name:"site-elevation" description:"Observatory elevation in metres"`
	MinAltitude      float64  `name:"min-altitude" description:"Altitude in degrees an object must clear in astronomical darkness to count as up" default:"30"`
	SkyBrightness    float64  `name:"sky-brightness" description:"Dark-sky brightness at the site in mag/arcsec², for brightness scores" default:"21.4"`
	FocalLength      float64  `name:"focal-length" description:"Telescope focal length in mm" default:"405"`
	PixelSize        float64  `name:"pixel-size" description:"Camera pixel size in µm" default:"3.76"`
	SensorWidth      int      `name:"sensor-width" description:"Camera width in pixels" default:"6248"`
	SensorHeight     int      `name:"sensor-height" description:"Camera height in pixels" default:"4176"`
	Colour           bool     `name:"colour" description:"The camera is one-shot colour"`
	Filters          []string `name:"filters" description:"Filters on the wheel as L, R, G, B, H, O or S, each optionally =bandpass in nm (H=3)" default:"L,R,G,B,H,O,S"`
	TypicalHFR       float64  `name:"typical-hfr" description:"Typical star HFR in arcseconds, for collaboration limits; 0 if unknown"`
	TypicalGuideRMS  float64  `name:"typical-guide-rms" description:"Typical guiding RMS in arcseconds, for collaboration limits; 0 if unknown"`
	Exposures        []string `name:"exposures" description:"Sub lengths in seconds per filter (H=600), for collaboration limits"`
	Starfront        bool     `name:"starfront" description:"Read Starfront's public collaboration list for the Collabs page; read-only, no account" default:"true"`
	StarfrontURL     string   `name:"starfront-url" description:"Starfront collaboration server" default:"https://collab.starfront.space"`
	StarfrontMinutes int      `name:"starfront-minutes" description:"Minutes between fetches of the collaboration list" default:"30"`
}

type PublicFrames struct {
	Enabled         bool `name:"enabled" description:"Render each recently imaged target's newest accepted light as a small watermarked JPEG in the processed bucket, served at /api/v1/public-light.jpg"`
	IntervalSeconds int  `name:"interval-seconds" description:"Seconds between checks for newly accepted lights" default:"60"`
	MaxAgeDays      int  `name:"max-age-days" description:"Targets with an accepted light from the last this many days get a frame; older frames are kept but not re-rendered" default:"14"`
}

type Stacking struct {
	Enabled           bool    `name:"enabled" description:"Stack good lights into masters as they arrive"`
	IntervalSeconds   int     `name:"interval-seconds" description:"Seconds between checks for new lights when idle" default:"120"`
	MinScore          float64 `name:"min-score" description:"Lowest sub score (0-1) that goes into a master" default:"0.3"`
	BatchSize         int     `name:"batch-size" description:"Subs calibrated and registered per Siril run" default:"12"`
	WorkDir           string  `name:"work-dir" description:"Scratch space for downloads, masters and Siril output" default:"/tmp/stacking"`
	SirilCommand      string  `name:"siril-command" description:"siril-cli, or an extracted Siril AppImage's AppRun" default:"siril-cli"`
	SirilThreads      int     `name:"siril-threads" description:"Threads Siril may use" default:"4"`
	SirilMemory       float64 `name:"siril-memory" description:"Share of memory all Siril runs together may use (Siril reads the container limit); registration needs about 320 MiB per thread" default:"0.5"`
	Workers           int     `name:"workers" description:"Targets stacked at once; each holds up to about 1.5 GB besides Siril" default:"1"`
	MosaicMinutes     int     `name:"mosaic-minutes" description:"Minutes between checks for mosaics to build from panel masters; 0 turns mosaics off" default:"10"`
	MosaicQuiet       int     `name:"mosaic-quiet-minutes" description:"Minutes a mosaic's panel masters must be unchanged before it is rebuilt" default:"30"`
	Pedestal          float64 `name:"pedestal" description:"Camera pedestal in ADU, for scoring subs" default:"506"`
	CalibrationSettle int     `name:"calibration-settle-minutes" description:"Minutes a flat, dark or bias set must go without a new frame before a master is built from it; lights it matches wait meanwhile" default:"180"`
	RecalibrateLimit  int     `name:"recalibrate-limit" description:"Most stacked lights waiting at once to be calibrated again with a better dark; more are queued as they clear" default:"300"`
	DrainSeconds      int     `name:"drain-seconds" description:"On shutdown, seconds to let the stacker finish the batch, master, mosaic or comet it is on before cancelling it" default:"1200"`
	// TSVerdicts sends the subs left out of masters (low score, moon) back to
	// Target Scheduler as rejected, so they stop counting toward its
	// exposure plans and it images the targets further.
	TSVerdicts             string   `name:"ts-verdicts" description:"Tell Target Scheduler which subs were left out for low score or moon: off, dry-run (log what would be sent) or on" default:"off"`
	TSVerdictsSince        string   `name:"ts-verdicts-since" description:"Only subs taken on or after this date (YYYY-MM-DD, UTC); empty for all"`
	TSVerdictsTargets      []string `name:"ts-verdicts-targets" description:"Only subs of these targets; empty for all"`
	TSVerdictsMax          int      `name:"ts-verdicts-max" description:"Most new verdicts sent per hourly sweep" default:"200"`
	RegisteredGraceHours   int      `name:"registered-grace-hours" description:"Hours a registered sub no stacked sub references is kept before it is deleted; 0 keeps them all" default:"24"`
	RegisteredDeletePause  int      `name:"registered-delete-pause-ms" description:"Milliseconds between deletions of unreferenced registered subs" default:"200"`
	RegisteredBackfillRate float64  `name:"registered-backfill-rate" description:"Stacked subs per second converted from 32-bit FITS to 16-bit XISF; 0 stops the conversion" default:"0.5"`
}

// TS verdict modes.
const (
	TSVerdictsOff    = "off"
	TSVerdictsDryRun = "dry-run"
	TSVerdictsOn     = "on"
)

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
	ErrInvalidTSVerdicts            = errors.New("stacking.ts-verdicts must be off, dry-run or on")
	ErrInvalidTSVerdictsSince       = errors.New("stacking.ts-verdicts-since must be a date, YYYY-MM-DD")
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

	switch c.Stacking.TSVerdicts {
	case TSVerdictsOff, TSVerdictsDryRun, TSVerdictsOn:
	default:
		return ErrInvalidTSVerdicts
	}
	if c.Stacking.TSVerdictsSince != "" {
		if _, err := time.Parse(time.DateOnly, c.Stacking.TSVerdictsSince); err != nil {
			return ErrInvalidTSVerdictsSince
		}
	}

	if (c.Indexer.Enabled || c.Previews.Enabled || c.Stacking.Enabled || c.PublicFrames.Enabled) && (c.S3.AccessKey == "" || c.S3.SecretKey == "") {
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
