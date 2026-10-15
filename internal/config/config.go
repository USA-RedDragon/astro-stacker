package config

//go:generate go tool configulator -type Config

import (
	"cmp"
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
	Scheduler    Scheduler    `name:"scheduler" description:"The observatory-scheduler plugin's API and the durable command queue"`
	Goals        Goals        `name:"goals" description:"Faint-signal SNR and depth of every master, measured from random half stacks of its registered subs, and progress toward Target Scheduler goals"`
	Darks        Darks        `name:"darks" description:"The dark backlog the observatory's Target Scheduler Darks instruction works through"`
	SubLength    SubLength    `name:"sub-length" description:"Sub exposure lengths the system picks per target, filter and moon glare from the measured sky, read noise, overhead, star saturation, rejections and the dark library"`
	Calibration  Calibration  `name:"calibration" description:"The bias backlog and requested flats the observatory's Target Scheduler Darks and Flats instructions work through"`
	MoonKeep     MoonKeep     `name:"moon-keep" description:"How many subs the stacker keeps under each Moon, by filter, Moon phase, Moon altitude and distance from the target, and by the target's altitude with the Moon down, fitted from its own verdicts for the plugin's weights and skips"`
	Bodies       Bodies       `name:"bodies" description:"Comets and other moving targets: nightly JPL Horizons position tables for the plugin and the list of comets observable tonight"`
	Discoveries  Discoveries  `name:"discoveries" description:"Each night of a target with 3 or more accepted nights compared with its other nights for variable stars, supernovae, asteroids and anything new"`
	Scheduling   Scheduling   `name:"scheduling" description:"One global set of scheduling settings for every Target Scheduler project, with each project's minimum time at the measured switching floor and its minimum altitude at the hard limit"`
	// MorningReport is the daily Telegram report on the night that ended.
	MorningReport MorningReport `name:"morning-report" description:"A daily Telegram report on the night that just ended: safe and imaged hours, subs kept and rejected, unsafe events and how fast the flap closed, dew, autofocus, guiding and camera, with each imaged target's current stack"`
	// DiscoveryAlerts is the fast path for a new find.
	DiscoveryAlerts DiscoveryAlerts `name:"discovery-alerts" description:"A Telegram alert, through a bot of its own, as soon as a night comparison stores a high-confidence new find whose catalogue checks all succeeded, and a second look at its field"`
}

// DiscoveryAlerts are the alerts on new discovery candidates and their
// follow-up imaging.
type DiscoveryAlerts struct {
	Enabled         bool   `name:"enabled" description:"Send one Telegram alert per new find (a run's finds together, a run with more than 5 as one summary), once per source across restarts and reruns; candidates already stored when this is first on are never alerted; off while the token or chat is empty" default:"true"`
	TelegramToken   string `name:"telegram-token" description:"Telegram bot token the alerts are sent with; a bot of their own, empty turns the alerts off" secret:"true"`
	TelegramChatID  string `name:"telegram-chat-id" description:"Telegram chat the alerts go to; empty turns the alerts off"`
	Followup        bool   `name:"followup" description:"For each alerted find, add a Target Scheduler project that images its field again in the same filter group (at least 6 subs or 30 minutes) at high priority from now to the third night, record whether the find was seen again, and close the project once settled; off by default"`
	IntervalMinutes int    `name:"interval-minutes" description:"Minutes between backstop looks for new finds and follow-up results; a stored comparison starts a look about 20 seconds later" default:"5"`
}

type MorningReport struct {
	Enabled        bool   `name:"enabled" description:"Send the report once a day; a report missed while the stacker was down is sent when it comes back the same day"`
	Time           string `name:"time" description:"Local time of day, HH:MM, the report is sent; each report covers the day before up to this time" default:"11:00"`
	Timezone       string `name:"timezone" description:"IANA time zone of the send time and of the times in the report" default:"America/Chicago"`
	ZoneLabel      string `name:"zone-label" description:"Label after every time in the report" default:"CT"`
	LokiURL        string `name:"loki-url" description:"Loki holding the observatory's NINA logs, for the flap, unsafe events and guider errors, e.g. http://loki.monitoring:3100; empty leaves those not measured"`
	TelegramToken  string `name:"telegram-token" description:"Telegram bot token" secret:"true"`
	TelegramChatID string `name:"telegram-chat-id" description:"Telegram chat the report goes to"`
	FirstNight     string `name:"first-night" description:"First night, as its evening date YYYY-MM-DD, whose report is sent; earlier nights never are, so turning the report on after the send time does not send that day's report; empty sends every night"`
}

// FirstNightDate is the first night whose report is sent, zero for every
// night.
func (m MorningReport) FirstNightDate() time.Time {
	t, err := time.Parse(time.DateOnly, m.FirstNight)
	if err != nil {
		return time.Time{}
	}
	return t
}

// Clock is the send time as hour and minute.
func (m MorningReport) Clock() (hour, minute int, err error) {
	t, err := time.Parse("15:04", m.Time)
	if err != nil {
		return 0, 0, ErrInvalidMorningReportTime
	}
	return t.Hour(), t.Minute(), nil
}

func (m MorningReport) validate() error {
	if !m.Enabled {
		return nil
	}
	if _, _, err := m.Clock(); err != nil {
		return err
	}
	if _, err := time.LoadLocation(m.Timezone); err != nil {
		return ErrInvalidMorningReportZone
	}
	if m.TelegramToken == "" || m.TelegramChatID == "" {
		return ErrMissingMorningReportTelegram
	}
	if m.FirstNight != "" && m.FirstNightDate().IsZero() {
		return ErrInvalidMorningReportFirst
	}
	return nil
}

type Bodies struct {
	Publish          string `name:"publish" description:"Write each moving target's nightly position table into ts_body_position, which the plugin uses for visibility, centering and sub length, and its season table (one position and magnitude per night for a year) into ts_body_night, which the plugin counts its season from, in the scheduler database: off (no Horizons requests), dry-run (log what would be written) or on; nothing is fetched until the scheduler has the ts_target_body table" default:"on"`
	IntervalMinutes  int    `name:"interval-minutes" description:"Minutes between rebuilding the position tables; tables already fetched for a night come from the cache" default:"60"`
	HorizonsURL      string `name:"horizons-url" description:"JPL Horizons API" default:"https://ssd.jpl.nasa.gov/api/horizons.api"`
	CometElementsURL string `name:"comet-elements-url" description:"Minor Planet Center comet elements, for the list of comets observable tonight" default:"https://www.minorplanetcenter.net/iau/MPCORB/CometEls.txt"`
	CometSBDBURL     string `name:"comet-sbdb-url" description:"JPL SBDB query of every comet's M1 and K1, for the JPL magnitude estimate in the comet list" default:"https://ssd-api.jpl.nasa.gov/sbdb_query.api?fields=pdes,full_name,M1,K1&sb-kind=c"`
}

func (b Bodies) validate() error {
	switch b.Publish {
	case GoalsPublishOff, GoalsPublishDryRun, GoalsPublishOn:
	default:
		return ErrInvalidBodiesPublish
	}
	if b.IntervalMinutes < 1 {
		return ErrInvalidBodiesInterval
	}
	return nil
}

// RejectOffTempSinceTime is RejectOffTempSince as a time, zero when empty.
func (s Stacking) RejectOffTempSinceTime() time.Time {
	t, err := time.Parse(time.DateOnly, s.RejectOffTempSince)
	if err != nil {
		return time.Time{}
	}
	return t
}

func (d Darks) RejectSinceTime() time.Time {
	t, err := time.Parse(time.DateOnly, d.RejectSince)
	if err != nil {
		return time.Time{}
	}
	return t
}

type Darks struct {
	RejectSince string `name:"reject-since" description:"Leave out darks taken on or after this date (YYYY-MM-DD, UTC) that the ingest check finds lit or off their setpoint; older darks only get the verdict recorded; empty records every verdict only" default:"2026-10-10"`
	Publish     string `name:"publish" description:"Write the dark backlog into ts_dark_need in the scheduler database after each index scan: off, dry-run (log what would be written) or on" default:"off"`
}

type SubLength struct {
	Publish         string  `name:"publish" description:"Write the picked lengths into ts_sub_length in the scheduler database, which the plugin uses at exposure selection: off, dry-run (log what would be written) or on; on needs stacking.score-per-second" default:"off"`
	NewLengths      bool    `name:"new-lengths" description:"Put a dark set for each length the data proposes (one that would gain 5% SNR per hour or more but has no full dark set) on the dark backlog in ts_dark_need; off only shows the proposals"`
	IntervalMinutes int     `name:"interval-minutes" description:"Minutes between backstop recomputes; the lengths are worked out again about two minutes after subs are measured or triaged or the exposure plans change, and at most every ten minutes while subs keep arriving" default:"60"`
	TolerancePct    float64 `name:"tolerance-percent" description:"The shortest length allowed is picked if its SNR per hour is within this percent of the best allowed length; shorter subs lose less to a bad moment and saturate fewer stars" default:"2"`
}

func (s SubLength) validate(perSecond bool) error {
	switch s.Publish {
	case GoalsPublishOff, GoalsPublishDryRun:
	case GoalsPublishOn:
		if !perSecond {
			return ErrSubLengthNeedsPerSecond
		}
	default:
		return ErrInvalidSubLengthPublish
	}
	if s.IntervalMinutes < 1 {
		return ErrInvalidSubLengthInterval
	}
	if !(s.TolerancePct > 0 && s.TolerancePct < 50) {
		return ErrInvalidSubLengthTolerance
	}
	return nil
}

type Scheduling struct {
	Auto          string `name:"auto" description:"Keep every project on the global scheduling settings and its automatic minimum time and minimum altitude through project.batchedit commands: off, dry-run (log the edits) or on" default:"off"`
	IntervalHours int    `name:"interval-hours" description:"Hours between backstop runs; the values are worked out again about a minute after projects, targets, exposure plans or the global settings change (at most every five minutes), and when a manual edit's hold ends" default:"1"`
}

func (s Scheduling) validate() error {
	switch s.Auto {
	case GoalsPublishOff, GoalsPublishDryRun, GoalsPublishOn:
	default:
		return ErrInvalidSchedulingAuto
	}
	if s.IntervalHours < 1 {
		return ErrInvalidSchedulingInterval
	}
	return nil
}

type MoonKeep struct {
	Publish       string `name:"publish" description:"Write the fitted kept rates into ts_moon_keep and ts_alt_keep in the scheduler database, which the plugin uses to weigh and skip exposures by the Moon and by the target's altitude: off, dry-run (log what would be written) or on; nothing is written to a table until the scheduler has it" default:"on"`
	IntervalHours int    `name:"interval-hours" description:"Hours between backstop refits; the kept rates are refitted about two minutes after the stacker's verdicts or the exposure plans change, and at most every ten minutes while subs keep arriving" default:"24"`
}

func (m MoonKeep) validate() error {
	switch m.Publish {
	case GoalsPublishOff, GoalsPublishDryRun, GoalsPublishOn:
	default:
		return ErrInvalidMoonKeepPublish
	}
	if m.IntervalHours < 1 {
		return ErrInvalidMoonKeepInterval
	}
	return nil
}

type Calibration struct {
	Publish string `name:"publish" description:"Write the bias backlog into ts_bias_need and open flat requests into ts_flat_request in the scheduler database after each index scan: off, dry-run (log what would be written) or on" default:"off"`
}

type Scheduler struct {
	URL        string `name:"url" description:"Base URL of the observatory-scheduler plugin's API, e.g. http://observatory:8189; empty leaves the scheduler unconfigured and commands queued"`
	Token      string `name:"token" description:"Bearer token for the plugin's API"`
	Queue      bool   `name:"queue" description:"Also write commands to the scheduler database's ts_command table, which SymmetricDS carries to the observatory" default:"true"`
	MetricsURL string `name:"metrics-url" description:"Prometheus or Thanos query URL holding the observatory's weather, safety monitor, mount and UPS metrics, e.g. http://thanos-querier-app.monitoring:9090; empty shows those cards with no data source"`
	UPS        string `name:"ups" description:"The ups label of the observatory UPS in the NUT exporter's metrics" default:"observatory"`
	NINAURL    string `name:"nina-url" description:"Base URL of NINA's Advanced API on the observatory PC, e.g. http://observatory:1888/v2/api, for the collab switch on the Collabs page; empty leaves the switch unconfigured"`
}

type Discover struct {
	SiteLatitude     float64  `name:"site-latitude" description:"Observatory latitude in degrees; with site-longitude 0 too, the site is read from the newest light's FITS header (SITELAT, SITELONG)"`
	SiteLongitude    float64  `name:"site-longitude" description:"Observatory east longitude in degrees"`
	SiteElevation    *float64 `name:"site-elevation" description:"Observatory elevation in metres; unset reads SITEELEV from the newest light when the site comes from its header, and leaves the elevation unknown otherwise"`
	MinAltitude      float64  `name:"min-altitude" description:"Altitude in degrees an object must clear in astronomical darkness to count as up" default:"30"`
	SkyBrightness    float64  `name:"sky-brightness" description:"Override for the dark-sky brightness in mag/arcsec²; 0 measures it from broadband (L, G, R, B) masters' zero points and their subs' sky"`
	FocalLength      float64  `name:"focal-length" description:"Ignored: the focal length is read from lights' FOCALLEN"`
	PixelSize        float64  `name:"pixel-size" description:"Ignored: the pixel size is read from lights' XPIXSZ"`
	SensorWidth      int      `name:"sensor-width" description:"Ignored: the image width is read from lights' headers"`
	SensorHeight     int      `name:"sensor-height" description:"Ignored: the image height is read from lights' headers"`
	Colour           bool     `name:"colour" description:"Ignored: a colour camera is recognised by BAYERPAT in lights' headers"`
	Filters          []string `name:"filters" description:"Filter bandpasses in nm for collaboration limits (H=3); which filters are on the wheel is read from lights"`
	TypicalHFR       float64  `name:"typical-hfr" description:"Ignored: the typical HFR is measured from recent lights"`
	TypicalGuideRMS  float64  `name:"typical-guide-rms" description:"Ignored: the typical guiding RMS is read from Target Scheduler's records of recent lights"`
	Exposures        []string `name:"exposures" description:"Ignored: sub lengths per filter are read from recent lights"`
	Starfront        bool     `name:"starfront" description:"Read Starfront's public collaboration list for the Collabs page; read-only, no account" default:"true"`
	StarfrontURL     string   `name:"starfront-url" description:"Starfront collaboration server" default:"https://collab.starfront.space"`
	StarfrontMinutes int      `name:"starfront-minutes" description:"Minutes between fetches of the collaboration list" default:"30"`
	HAlphaMap        bool     `name:"halpha-map" description:"Fetch the Finkbeiner 2003 H-α all-sky map once from CDS and cache it in the database, for the Finder's H-α column and score" default:"true"`
	SkyCutouts       bool     `name:"sky-cutouts" description:"Serve DSS2 colour survey cutouts for framing previews, rendered from the S3 survey mirror or else fetched from CDS hips2fits and cached in the database" default:"true"`
	HiPS2FITSURL     string   `name:"hips2fits-url" description:"CDS hips2fits service for the H-α map and survey cutouts" default:"https://alasky.cds.unistra.fr/hips-image-services/hips2fits"`
	CutoutsPerMinute int      `name:"cutouts-per-minute" description:"Most survey cutouts fetched from hips2fits per minute; cached ones and ones rendered from the S3 mirror are served without limit" default:"60"`
	SurveyMirror     bool     `name:"survey-mirror" description:"Render survey cutouts from the DSS2 colour HiPS tiles mirrored in S3 when a mirrored order is as fine as one output pixel; finer fields and mirror misses come from hips2fits" default:"true"`
	SurveyBucket     string   `name:"survey-bucket" description:"S3 bucket holding the survey mirror, read with the s3 credentials" default:"sky-survey"`
	SurveyPrefix     string   `name:"survey-prefix" description:"Path of the DSS2 colour HiPS inside the survey bucket" default:"hips/CDS_P_DSS2_color"`
	SurveyMaxOrder   int      `name:"survey-max-order" description:"Deepest HiPS order in the survey mirror" default:"8"`
	SurveyCacheMB    int      `name:"survey-cache-mb" description:"Memory for decoded survey tiles, in MB" default:"100"`
}

type Goals struct {
	Enabled         bool   `name:"enabled" description:"Measure each master's faint-signal SNR, noise curve and depth into goal_measurements, again when it grows 20% in effective hours"`
	IntervalMinutes int    `name:"interval-minutes" description:"Minutes between backstop checks for masters to measure; a master is measured about a minute after it is updated or its goal changes, and again as soon as it has subs for another noise draw level while its noise floor can't be measured yet (18 subs give the 3 levels the floor needs)" default:"30"`
	MaxSubs         int    `name:"max-subs" description:"Most registered subs read per measurement; a master with more uses a fixed random subset of this many" default:"200"`
	Workers         int    `name:"workers" description:"Masters measured at once, each using about one core; measurement waits while the stacker is working" default:"3"`
	Publish         string `name:"publish" description:"Write goal progress into ts_goal_progress in the scheduler database: off, dry-run (log what would be written) or on" default:"off"`
	DepthSwitch     string `name:"depth-switch" description:"About a minute after a master is measured or goals change (and hourly as a backstop), move a broadband filter's goal from SNR to depth once its master has a Gaia zero point, on active galaxy, cluster and dust projects, through a goal.edit command sent once per filter: off, dry-run (log the edits) or on" default:"on"`
}

type Discoveries struct {
	Enabled         bool   `name:"enabled" description:"Compare each settled night of every target with 3 or more accepted nights (per filter group: L, R, G and B together, each narrowband filter alone) against its other nights, check what changed against VSX, SkyBoT, ALeRCE and Fink, and store the candidates, cutouts and moving-object tracks"`
	IntervalMinutes int    `name:"interval-minutes" description:"Minutes between backstop checks for nights to compare; a master update starts a check about two minutes later" default:"10"`
	Threads         int    `name:"threads" description:"Cores one comparison may use; comparisons run one at a time and wait while the stacker is working" default:"2"`
	RefSubs         int    `name:"reference-subs" description:"Most subs per filter stacked into a night's reference, taken from the other nights in turn" default:"48"`
	BackfillDays    int    `name:"backfill-days" description:"Nights from the last this many days are compared on their own; older nights only on request" default:"30"`
	TNSKey          string `name:"tns-key" description:"Transient Name Server bot API key; empty skips TNS"`
	TNSBotID        string `name:"tns-bot-id" description:"Transient Name Server bot ID"`
	TNSBotName      string `name:"tns-bot-name" description:"Transient Name Server bot name"`
	TNSReporter     string `name:"tns-reporter" description:"Reporter line of TNS discovery reports, e.g. 'J. Smith (Home Observatory)'; empty shows Not set and keeps Send off"`
	TNSGroupID      string `name:"tns-group-id" description:"TNS reporting group id of your reports; empty keeps Send off"`
	TNSInstrumentID string `name:"tns-instrument-id" description:"TNS instrument id of your telescope and camera; empty keeps Send off"`
	MPCCode         string `name:"mpc-code" description:"Minor Planet Center observatory code for ADES reports of moving objects; empty shows Not set"`
}

func (d Discoveries) validate() error {
	if d.Enabled && (d.IntervalMinutes <= 0 || d.Threads <= 0 || d.RefSubs < 2 || d.BackfillDays < 1) {
		return ErrInvalidDiscoveries
	}
	return nil
}

type PublicFrames struct {
	Enabled         bool `name:"enabled" description:"Render each recently imaged target's newest accepted light as a small watermarked JPEG in the processed bucket, served at /api/v1/public-light.jpg"`
	IntervalSeconds int  `name:"interval-seconds" description:"Seconds between backstop checks for newly accepted lights; a check also runs as soon as lights are triaged" default:"60"`
	MaxAgeDays      int  `name:"max-age-days" description:"Targets with an accepted light from the last this many days get a frame; older frames are kept but not re-rendered" default:"14"`
}

type Stacking struct {
	Enabled            bool    `name:"enabled" description:"Stack good lights into masters as they arrive"`
	IntervalSeconds    int     `name:"interval-seconds" description:"Seconds between checks for new lights when idle" default:"120"`
	MinScore           float64 `name:"min-score" description:"Lowest sub score (0-1) that goes into a master" default:"0.3"`
	ScorePerSecond     bool    `name:"score-per-second" description:"Score subs per second of exposure against every sub of the same filter and gain, instead of against subs of the same filter and exposure, so a target's subs of different lengths weigh fairly in one master; turning it on or off scores every master's subs again"`
	BatchSize          int     `name:"batch-size" description:"Subs calibrated and registered per Siril run" default:"12"`
	WorkDir            string  `name:"work-dir" description:"Scratch space for downloads, masters and Siril output" default:"/tmp/stacking"`
	SirilCommand       string  `name:"siril-command" description:"siril-cli, or an extracted Siril AppImage's AppRun" default:"siril-cli"`
	SirilThreads       int     `name:"siril-threads" description:"Threads Siril may use" default:"4"`
	SirilMemory        float64 `name:"siril-memory" description:"Share of memory all Siril runs together may use (Siril reads the container limit); registration needs about 320 MiB per thread" default:"0.5"`
	AstrometryIndexDir string  `name:"astrometry-index-dir" description:"Directory astrometry.net's index files (about 1.1 GB) are downloaded to and checked in at startup; masters Siril can't plate solve are solved with solve-field from them. Empty turns it off"`
	AstrometryCommand  string  `name:"astrometry-command" description:"astrometry.net's solve-field" default:"solve-field"`
	AstrometryCPU      int     `name:"astrometry-cpu-seconds" description:"CPU seconds one solve-field run may use; runs are one at a time, on one core" default:"120"`
	AstrometryTimeout  int     `name:"astrometry-timeout-seconds" description:"Seconds one solve-field run may take" default:"300"`
	Workers            int     `name:"workers" description:"Targets stacked at once; each holds up to about 1.5 GB besides Siril" default:"1"`
	MosaicMinutes      int     `name:"mosaic-minutes" description:"Minutes between backstop checks for mosaics, linear fits and comet masters to build; a master update or new frames start a check about a minute later, at most every five minutes; 0 turns mosaics off" default:"10"`
	MosaicQuiet        int     `name:"mosaic-quiet-minutes" description:"Minimum minutes between rebuilds of a mosaic whose panel masters keep changing; a new panel rebuilds it at once" default:"30"`
	MosaicSeams        bool    `name:"mosaic-seams" description:"Measure seam health on mosaics already built, one per mosaic check and only while no target is stacking; new builds measure their seams either way" default:"true"`
	Pedestal           float64 `name:"pedestal" description:"Camera pedestal in ADU at offset 50, for scoring subs with no master bias measured at their gain and offset" default:"506"`
	CalibrationSettle  int     `name:"calibration-settle-minutes" description:"Minutes a flat, dark or bias set must go without a new frame before a master is built from it; lights it matches wait meanwhile" default:"180"`
	RecalibrateLimit   int     `name:"recalibrate-limit" description:"Most stacked lights waiting at once to be calibrated again with a better dark; more are queued as they clear" default:"300"`
	DustCheck          bool    `name:"dust-check" description:"Check each settled flat set for dust that came, went or moved since the last flat of its filter, and each night's lights for dust their flats don't hold" default:"true"`
	DrainSeconds       int     `name:"drain-seconds" description:"On shutdown, seconds to let the stacker finish the batch, master, mosaic or comet it is on before cancelling it" default:"1200"`
	RejectOffTempSince string  `name:"reject-off-temp-since" description:"Leave out lights taken on or after this date (YYYY-MM-DD, UTC) whose sensor was more than 0.5 °C off its setpoint, and take such lights out of masters; older lights are stacked as before; empty turns the rule off" default:"2026-10-13"`
	// TSVerdicts sends the subs left out of masters (low score, moon) back to
	// Target Scheduler as rejected, so they stop counting toward its
	// exposure plans and it images the targets further.
	TSVerdicts             string   `name:"ts-verdicts" description:"Tell Target Scheduler which subs were left out for low score or moon: off, dry-run (log what would be sent) or on" default:"off"`
	TSVerdictsSince        string   `name:"ts-verdicts-since" description:"Only subs taken on or after this date (YYYY-MM-DD, UTC); empty for all"`
	TSVerdictsTargets      []string `name:"ts-verdicts-targets" description:"Only subs of these targets; empty for all"`
	TSVerdictsMax          int      `name:"ts-verdicts-max" description:"Most new verdicts sent in any hour; a sweep runs about a minute after lights are triaged or Target Scheduler's images sync in, at most every five minutes, and hourly" default:"200"`
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

const (
	GoalsPublishOff    = "off"
	GoalsPublishDryRun = "dry-run"
	GoalsPublishOn     = "on"
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
	SecretKey string `name:"secret-key" description:"S3 secret key" secret:"true"`
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
	RecentSeconds   int  `name:"recent-seconds" description:"Seconds between listings of the folders of targets imaged in the last two days, so new lights are indexed, previewed and scored soon after upload; 0 waits for the bucket scan" default:"15"`
	Concurrency     int  `name:"concurrency" description:"Headers to fetch in parallel" default:"8"`
}

type HTTP struct {
	Bind           string   `name:"bind" description:"Address to listen on" default:"[::]"`
	Port           int      `name:"port" description:"Port to listen on" default:"8080"`
	TrustedProxies []string `name:"trusted-proxies" description:"Trusted proxies for the HTTP server"`
	WebURL         string   `name:"web-url" description:"The web app that the old web UI paths redirect to" default:"https://astro-processing.jackal-stargazer.ts.net"`
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
	ErrInvalidRejectOffTempSince    = errors.New("stacking.reject-off-temp-since must be a date, YYYY-MM-DD")
	ErrInvalidGoalsPublish          = errors.New("goals.publish must be off, dry-run or on")
	ErrInvalidGoalsInterval         = errors.New("goals.interval-minutes must be positive")
	ErrInvalidDiscoveries           = errors.New("discoveries needs positive interval-minutes and threads, reference-subs of 2 or more and backfill-days of 1 or more")
	ErrInvalidGoalsDepthSwitch      = errors.New("goals.depth-switch must be off, dry-run or on")
	ErrInvalidDarksPublish          = errors.New("darks.publish must be off, dry-run or on")
	ErrInvalidMoonKeepPublish       = errors.New("moon-keep.publish must be off, dry-run or on")
	ErrInvalidMoonKeepInterval      = errors.New("moon-keep.interval-hours must be at least 1")
	ErrInvalidSchedulingAuto        = errors.New("scheduling.auto must be off, dry-run or on")
	ErrInvalidSchedulingInterval    = errors.New("scheduling.interval-hours must be at least 1")
	ErrInvalidSubLengthPublish      = errors.New("sub-length.publish must be off, dry-run or on")
	ErrSubLengthNeedsPerSecond      = errors.New("sub-length.publish on needs stacking.score-per-second, so subs of different lengths weigh fairly in one master")
	ErrInvalidSubLengthInterval     = errors.New("sub-length.interval-minutes must be at least 1")
	ErrInvalidSubLengthTolerance    = errors.New("sub-length.tolerance-percent must be above 0 and below 50")
	ErrInvalidDarksRejectSince      = errors.New("darks.reject-since must be a date, YYYY-MM-DD")
	ErrInvalidCalibrationPublish    = errors.New("calibration.publish must be off, dry-run or on")
	ErrInvalidBodiesPublish         = errors.New("bodies.publish must be off, dry-run or on")
	ErrInvalidBodiesInterval        = errors.New("bodies.interval-minutes must be at least 1")
	ErrInvalidSurveyMaxOrder        = errors.New("discover.survey-max-order must be 0 to 20")
	ErrInvalidSurveyCache           = errors.New("discover.survey-cache-mb must be at least 1")
	ErrInvalidMorningReportTime     = errors.New("morning-report.time must be HH:MM")
	ErrInvalidMorningReportZone     = errors.New("morning-report.timezone must be an IANA time zone")
	ErrMissingMorningReportTelegram = errors.New("morning-report needs telegram-token and telegram-chat-id when enabled")
	ErrInvalidMorningReportFirst    = errors.New("morning-report.first-night must be a date, YYYY-MM-DD")
)

func (c Config) validateCalibration() error {
	switch c.Darks.Publish {
	case GoalsPublishOff, GoalsPublishDryRun, GoalsPublishOn:
	default:
		return ErrInvalidDarksPublish
	}
	if c.Darks.RejectSince != "" {
		if _, err := time.Parse(time.DateOnly, c.Darks.RejectSince); err != nil {
			return ErrInvalidDarksRejectSince
		}
	}
	if c.Stacking.RejectOffTempSince != "" {
		if _, err := time.Parse(time.DateOnly, c.Stacking.RejectOffTempSince); err != nil {
			return ErrInvalidRejectOffTempSince
		}
	}
	switch c.Calibration.Publish {
	case GoalsPublishOff, GoalsPublishDryRun, GoalsPublishOn:
	default:
		return ErrInvalidCalibrationPublish
	}
	return nil
}

func (c Config) validatePublishers() error {
	if err := c.validateCalibration(); err != nil {
		return err
	}
	if err := c.MoonKeep.validate(); err != nil {
		return err
	}
	if err := c.MorningReport.validate(); err != nil {
		return err
	}
	return c.Scheduling.validate()
}

func (d Discover) validate() error {
	if !d.SurveyMirror {
		return nil
	}
	if d.SurveyMaxOrder < 0 || d.SurveyMaxOrder > 20 {
		return ErrInvalidSurveyMaxOrder
	}
	if d.SurveyCacheMB < 1 {
		return ErrInvalidSurveyCache
	}
	return nil
}

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

	switch c.Goals.Publish {
	case GoalsPublishOff, GoalsPublishDryRun, GoalsPublishOn:
	default:
		return ErrInvalidGoalsPublish
	}
	if c.Goals.Enabled && c.Goals.IntervalMinutes <= 0 {
		return ErrInvalidGoalsInterval
	}
	switch c.Goals.DepthSwitch {
	case GoalsPublishOff, GoalsPublishDryRun, GoalsPublishOn:
	default:
		return ErrInvalidGoalsDepthSwitch
	}

	if err := c.validatePublishers(); err != nil {
		return err
	}
	if err := c.SubLength.validate(c.Stacking.ScorePerSecond); err != nil {
		return err
	}
	if err := cmp.Or(c.Bodies.validate(), c.Discover.validate(), c.Discoveries.validate()); err != nil {
		return err
	}

	if (c.Indexer.Enabled || c.Previews.Enabled || c.Stacking.Enabled || c.PublicFrames.Enabled || c.Goals.Enabled) && (c.S3.AccessKey == "" || c.S3.SecretKey == "") {
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
