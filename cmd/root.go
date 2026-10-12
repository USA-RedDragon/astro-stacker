package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/bodies"
	"github.com/USA-RedDragon/astro-stacker/internal/calpublish"
	"github.com/USA-RedDragon/astro-stacker/internal/calreq"
	"github.com/USA-RedDragon/astro-stacker/internal/catalog"
	"github.com/USA-RedDragon/astro-stacker/internal/conditions"
	"github.com/USA-RedDragon/astro-stacker/internal/config"
	"github.com/USA-RedDragon/astro-stacker/internal/darkneed"
	"github.com/USA-RedDragon/astro-stacker/internal/discover"
	"github.com/USA-RedDragon/astro-stacker/internal/events"
	"github.com/USA-RedDragon/astro-stacker/internal/goalmeasure"
	"github.com/USA-RedDragon/astro-stacker/internal/goals"
	"github.com/USA-RedDragon/astro-stacker/internal/goalswitch"
	"github.com/USA-RedDragon/astro-stacker/internal/horizons"
	"github.com/USA-RedDragon/astro-stacker/internal/indexer"
	"github.com/USA-RedDragon/astro-stacker/internal/moonkeep"
	"github.com/USA-RedDragon/astro-stacker/internal/mosaicplan"
	"github.com/USA-RedDragon/astro-stacker/internal/mosaics"
	"github.com/USA-RedDragon/astro-stacker/internal/observatory"
	"github.com/USA-RedDragon/astro-stacker/internal/planning"
	"github.com/USA-RedDragon/astro-stacker/internal/preview"
	"github.com/USA-RedDragon/astro-stacker/internal/previewer"
	"github.com/USA-RedDragon/astro-stacker/internal/publicframe"
	"github.com/USA-RedDragon/astro-stacker/internal/rigsource"
	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"github.com/USA-RedDragon/astro-stacker/internal/server"
	"github.com/USA-RedDragon/astro-stacker/internal/server/middleware"
	"github.com/USA-RedDragon/astro-stacker/internal/skycutout"
	"github.com/USA-RedDragon/astro-stacker/internal/skyevents"
	"github.com/USA-RedDragon/astro-stacker/internal/store"
	"github.com/USA-RedDragon/astro-stacker/internal/sublength"
	"github.com/USA-RedDragon/astro-stacker/internal/wake"
	configulator "github.com/USA-RedDragon/configulator/v2"
	"github.com/lmittmann/tint"
	"github.com/minio/minio-go/v7"
	miniocreds "github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/spf13/cobra"
	"github.com/ztrue/shutdown"
	"gorm.io/gorm"
)

func NewCommand(version, commit string) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "astro-stacker",
		Version: fmt.Sprintf("%s - %s", version, commit),
		Annotations: map[string]string{
			"version": version,
			"commit":  commit,
		},
		RunE:              runRoot,
		SilenceErrors:     true,
		DisableAutoGenTag: true,
	}
	cmd.AddCommand(newStackCommand())
	return cmd
}

func runRoot(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()

	c, err := configulator.FromContext[config.Config](ctx)
	if err != nil {
		return fmt.Errorf("failed to get config from context")
	}

	cfg, err := c.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	var logger *slog.Logger
	switch cfg.LogLevel {
	case config.LogLevelDebug:
		logger = slog.New(tint.NewTextHandler(os.Stdout, &tint.Options{Level: slog.LevelDebug}))
	case config.LogLevelInfo:
		logger = slog.New(tint.NewTextHandler(os.Stdout, &tint.Options{Level: slog.LevelInfo}))
	case config.LogLevelWarn:
		logger = slog.New(tint.NewTextHandler(os.Stderr, &tint.Options{Level: slog.LevelWarn}))
	case config.LogLevelError:
		logger = slog.New(tint.NewTextHandler(os.Stderr, &tint.Options{Level: slog.LevelError}))
	}
	slog.SetDefault(logger)

	slog.Info("astro-stacker", "version", cmd.Annotations["version"], "commit", cmd.Annotations["commit"])

	appStore, err := store.NewAppStore(cfg)
	if err != nil {
		return fmt.Errorf("failed to connect to app datastore: %w", err)
	}

	slog.Info("Connected to app datastore", "type", cfg.Storage.Type)

	schedulerDBStore, err := store.NewSchedulerDBStore(cfg)
	if err != nil {
		return fmt.Errorf("failed to connect to scheduler database datastore: %w", err)
	}

	slog.Info("Connected to scheduler database datastore", "type", cfg.Storage.Type)

	indexCtx, stopIndexer := context.WithCancel(context.Background())
	defer stopIndexer()
	var signer *previewer.Signer
	mosaicPlans, horizon := newMosaicPlans(cfg, appStore, schedulerDBStore)
	var bodyNightsRef atomic.Pointer[bodies.NightService]
	mosaicPlans.BodyNight = bodyNightFunc(&bodyNightsRef)
	go horizon.Run(indexCtx)
	var restacker middleware.Restacker
	drainStacker := func() {}
	drainGoals := func() {}
	var stackBusy func() bool
	var backfill func() goals.BackfillLive
	broker := events.NewBroker()
	newFrames := &wake.Signal{}
	hz := horizons.New(cmd.Annotations["version"], appStore.DB())
	hz.BaseURL = cfg.Bodies.HorizonsURL
	bodyTables := &bodies.Service{Sched: schedulerDBStore.DB(), Horizons: hz, Site: bodies.SiteFunc(siteFunc(cfg, appStore)), Mode: cfg.Bodies.Publish}
	go bodyTables.Run(indexCtx, time.Duration(cfg.Bodies.IntervalMinutes)*time.Minute)
	slog.Info("Body position tables started", "publish", cfg.Bodies.Publish, "interval_minutes", cfg.Bodies.IntervalMinutes)
	darkNeed := &darkneed.Publisher{App: appStore.DB(), Sched: schedulerDBStore.DB(), Mode: cfg.Darks.Publish}
	biasNeed := calpublish.NewBias(appStore.DB(), schedulerDBStore.DB(), cfg.Calibration.Publish)
	flatRequests := calpublish.NewFlat(appStore.DB(), schedulerDBStore.DB(), cfg.Calibration.Publish)
	subLengths := &sublength.Service{App: appStore.DB(), Sched: schedulerDBStore.DB(), Site: sublength.SiteFunc(siteFunc(cfg, appStore)),
		Pedestal: cfg.Stacking.Pedestal, Mode: cfg.SubLength.Publish, NewLengths: cfg.SubLength.NewLengths,
		Tolerance: cfg.SubLength.TolerancePct / 100}
	moonKeep := &moonkeep.Service{App: appStore.DB(), Sched: schedulerDBStore.DB(), Site: moonkeep.SiteFunc(siteFunc(cfg, appStore)), Mode: cfg.MoonKeep.Publish}
	go moonKeep.Run(indexCtx, time.Duration(cfg.MoonKeep.IntervalHours)*time.Hour)
	slog.Info("Moon kept rates started", "publish", cfg.MoonKeep.Publish, "interval_hours", cfg.MoonKeep.IntervalHours)
	darkNeed.Planned = plannedDarks(subLengths)
	go subLengths.Run(indexCtx, time.Duration(cfg.SubLength.IntervalMinutes)*time.Minute)
	slog.Info("Sub lengths started", "publish", cfg.SubLength.Publish, "new_lengths", cfg.SubLength.NewLengths,
		"interval_minutes", cfg.SubLength.IntervalMinutes, "tolerance_percent", cfg.SubLength.TolerancePct)
	if cfg.Indexer.Enabled || cfg.Previews.Enabled || cfg.Stacking.Enabled || cfg.PublicFrames.Enabled || cfg.Goals.Enabled {
		creds := credentials(cfg)
		s3, err := newS3(cfg)
		if err != nil {
			return fmt.Errorf("failed to create S3 client: %w", err)
		}
		mosaicPlans.SiteFallback(mosaicplan.SiteFromLights(s3, cfg.S3.Bucket, appStore.DB()))
		if cfg.Indexer.Enabled {
			ix := indexer.New(s3, cfg.S3.Bucket, appStore.DB(), cfg.Indexer.Concurrency)
			ix.Events = broker
			ix.Wake = newFrames
			ix.RecentTargets = indexer.ScheduledTargets(schedulerDBStore.DB())
			ix.AfterScan = afterScan(appStore.DB(), broker, darkNeed.Run, biasNeed.Run, flatRequests.Run)
			ix.RejectDarksSince = cfg.Darks.RejectSinceTime()
			go ix.Run(indexCtx, time.Duration(cfg.Indexer.IntervalSeconds)*time.Second, time.Duration(cfg.Indexer.RecentSeconds)*time.Second)
			slog.Info("Frame indexer started", "bucket", cfg.S3.Bucket, "interval_seconds", cfg.Indexer.IntervalSeconds,
				"recent_seconds", cfg.Indexer.RecentSeconds)
		}
		if cfg.Previews.Enabled {
			pv := previewer.New(s3, cfg.S3.Bucket, cfg.S3.ProcessedBucket, appStore.DB(), cfg.Previews.Concurrency,
				preview.Options{MaxWidth: cfg.Previews.MaxWidth, Quality: cfg.Previews.Quality})
			pv.Events = broker
			pv.Wake = newFrames
			go pv.Run(indexCtx, time.Duration(cfg.Previews.IntervalSeconds)*time.Second)
			slog.Info("Preview renderer started", "bucket", cfg.S3.ProcessedBucket, "concurrency", cfg.Previews.Concurrency)
		}
		if cfg.PublicFrames.Enabled {
			pf := publicframe.NewRenderer(s3, cfg.S3.ProcessedBucket, appStore.DB(), publicframe.DefaultOptions(),
				time.Duration(cfg.PublicFrames.MaxAgeDays)*24*time.Hour)
			go pf.Run(indexCtx, time.Duration(cfg.PublicFrames.IntervalSeconds)*time.Second)
			slog.Info("Public frame renderer started", "bucket", cfg.S3.ProcessedBucket, "max_age_days", cfg.PublicFrames.MaxAgeDays)
		}
		// Presigned URLs, for previews and masters, are signed for the public
		// host browsers use.
		public, err := minio.New(cfg.S3.PublicEndpoint, &minio.Options{Creds: creds, Secure: cfg.S3.PublicUseSSL, Region: cfg.S3.Region})
		if err != nil {
			return fmt.Errorf("failed to create public S3 client: %w", err)
		}
		signer = previewer.NewSigner(public, s3, cfg.S3.ProcessedBucket, time.Duration(cfg.Previews.URLTTLSeconds)*time.Second)
		if cfg.Stacking.Enabled {
			p := newPipeline(cfg, s3, appStore, schedulerDBStore)
			p.Events = broker
			p.Wake = newFrames
			p.Horizons = hz
			restacker = p
			// Its own context: on shutdown it drains first, and is only
			// cancelled if that takes too long.
			stackCtx, cancelStack := context.WithCancel(context.Background())
			stackDone := make(chan struct{})
			go func() {
				p.Run(stackCtx, time.Duration(cfg.Stacking.IntervalSeconds)*time.Second)
				close(stackDone)
			}()
			drainStacker = func() {
				limit := time.Duration(cfg.Stacking.DrainSeconds) * time.Second
				slog.Info("Draining the stacker", "limit", limit)
				p.Drain()
				select {
				case <-stackDone:
					slog.Info("Stacker drained")
				case <-time.After(limit):
					slog.Warn("Stacker still busy; cancelling its work", "limit", limit)
					cancelStack()
					select {
					case <-stackDone:
					case <-time.After(30 * time.Second):
					}
				}
				cancelStack()
			}
			stackBusy = p.Busy
			go mosaicPlans.RunAdoptionEvery(indexCtx, time.Hour)
			slog.Info("Stacker started", "min_score", cfg.Stacking.MinScore, "work_dir", cfg.Stacking.WorkDir, "ts_verdicts", cfg.Stacking.TSVerdicts)
		}
		if cfg.Goals.Enabled {
			drainGoals, backfill = startGoals(cfg, s3, appStore, schedulerDBStore, mosaicPlans.SeasonBoosts, stackBusy)
		}
	}

	disc, collabs := newDiscover(indexCtx, cfg, appStore, schedulerDBStore, backfill, mosaicPlans)
	attachSubLengths(disc, subLengths)
	previews := server.NewPreviewCache(nil, nil)
	commands, monitor, obs := newScheduler(indexCtx, cfg, appStore, schedulerDBStore, broker, func(r schedcmd.Record) {
		if disc != nil && discover.Affects(r) {
			disc.Invalidate()
		}
		previews.Notice(r)
	})
	comets := &bodies.CometList{DB: appStore.DB(), URL: cfg.Bodies.CometElementsURL, SBDBURL: cfg.Bodies.CometSBDBURL, UserAgent: horizons.UserAgent(cmd.Annotations["version"]),
		Site: bodies.SiteFunc(siteFunc(cfg, appStore)), MinAlt: cfg.Discover.MinAltitude}
	bodyNights := &bodies.NightService{Sched: schedulerDBStore.DB(), Horizons: hz, Elements: comets.ElementsOf, Site: bodies.SiteFunc(siteFunc(cfg, appStore)), Mode: cfg.Bodies.Publish}
	go bodyNights.Run(indexCtx, time.Duration(cfg.Bodies.IntervalMinutes)*time.Minute)
	bodyNightsRef.Store(bodyNights)
	asteroids := &bodies.AsteroidList{Comets: comets, Horizons: hz}
	go asteroids.Run(indexCtx, bodies.AsteroidWarmInterval)
	skyEvents := &skyevents.Service{App: appStore.DB(), Sched: schedulerDBStore.DB(), Horizons: hz, Site: skyevents.SiteFunc(siteFunc(cfg, appStore)),
		Rig: func(ctx context.Context) (rigsource.Rig, error) {
			return rigsource.Measure(ctx, appStore.DB(), schedulerDBStore.DB(), time.Now())
		},
		Catalog: sync.OnceValues(catalog.LoadEmbedded), Commands: commands, Orbits: comets.FindOrbit,
		Snapshot: func(ctx context.Context) (*planning.Snapshot, error) {
			return server.LoadPlanningSnapshot(ctx, appStore.DB(), schedulerDBStore.DB(), backfill)
		}}
	go skyEvents.Run(indexCtx, skyevents.Interval)
	slog.Info("Sky event finder started", "interval", skyevents.Interval)
	extras := server.Extras{SkyEvents: skyEvents, Site: server.SiteSource(siteFunc(cfg, appStore)), DarkNeed: darkNeed, SubLength: subLengths, MoonKeep: moonKeep, Bodies: bodyTables, BodyNights: bodyNights, BiasNeed: biasNeed, FlatRequests: flatRequests, Commands: commands, Scheduler: monitor, Mosaics: mosaicPlans, Discover: disc, Collabs: collabs, Backfill: backfill,
		Comets:     comets,
		Asteroids:  asteroids,
		Cutouts:    &skycutout.Service{DB: appStore.DB(), Endpoint: cfg.Discover.HiPS2FITSURL, PerMinute: cfg.Discover.CutoutsPerMinute, Off: !cfg.Discover.SkyCutouts},
		Conditions: conditions.New(conditions.Options{MetricsURL: cfg.Scheduler.MetricsURL, UPS: cfg.Scheduler.UPS}, schedulerDBStore.DB())}
	extras.Framing = &server.BodyFraming{Tracker: &bodies.Tracker{Horizons: hz, Site: bodies.SiteFunc(siteFunc(cfg, appStore))},
		Sched: schedulerDBStore.DB(), Frame: skyEvents.MeasuredFrame}
	if obs.Configured() {
		extras.Previews = obs
		previews.Source = obs
		previews.Stamp = server.SyncStamp(schedulerDBStore.DB(), monitor.PreviewInputs)
		extras.PreviewCache = previews
		go previews.Run(indexCtx)
		extras.Grader = &observatory.GraderCache{Client: obs, Monitor: monitor}
	}

	startDepthSwitch(indexCtx, cfg, appStore, schedulerDBStore, commands, disc)

	server := server.NewServer(cfg, appStore, schedulerDBStore, signer, broker, restacker, cmd.Annotations["version"], extras)
	if err := server.Start(ctx); err != nil {
		return fmt.Errorf("failed to start server: %w", err)
	}
	slog.Info("Server started successfully")

	stop := func(sig os.Signal) {
		// Remove control codes from the current line in the terminal
		fmt.Println("")

		slog.Info("Received signal", "signal", sig)
		drainGoals()
		drainStacker()
		stopIndexer()

		err := server.Stop()
		if err != nil {
			slog.Error("Failed to stop server", "error", err)
		} else {
			slog.Info("Server stopped gracefully")
		}
	}
	shutdown.AddWithParam(stop)
	shutdown.Listen(syscall.SIGINT, syscall.SIGKILL, syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGHUP)

	return nil
}

// afterScan marks calibration requests whose frames are now indexed done,
// tells listeners, and then publishes the calibration needs.
func afterScan(db *gorm.DB, broker *events.Broker, publish ...func(context.Context)) func(context.Context) {
	return func(ctx context.Context) {
		done, err := calreq.Refresh(ctx, db, time.Now())
		if err != nil && ctx.Err() == nil {
			slog.Error("Refreshing calibration requests failed", "error", err)
		}
		for _, r := range done {
			slog.Info("Calibration request done", "id", r.ID, "type", r.Type, "object", r.Object, "night", r.Night.Format(time.DateOnly))
			broker.Broadcast(calreq.EventType, calreq.EventOf(r), false)
		}
		for _, p := range publish {
			p(ctx)
		}
	}
}

func startGoals(cfg *config.Config, s3 *minio.Client, appStore, schedStore store.Store, boosts goals.SeasonBoostSource, busy func() bool) (func(), func() goals.BackfillLive) {
	r := goalmeasure.New(appStore.DB(), schedStore.DB(), goalmeasure.MinioGetter{Client: s3, Bucket: cfg.S3.ProcessedBucket},
		goalmeasure.VizierFetcher(nil, ""), withLongitude(goalOptions(cfg, boosts, busy), siteFunc(cfg, appStore)))
	r.XP = goalmeasure.VizierXPFetcher(nil, "")
	done := make(chan struct{})
	go func() {
		r.Run(context.Background())
		close(done)
	}()
	slog.Info("Goal measurement started", "interval_minutes", cfg.Goals.IntervalMinutes, "max_subs", cfg.Goals.MaxSubs, "publish", cfg.Goals.Publish,
		"workers", cfg.Goals.Workers, "waits_for_stacking", busy != nil)
	return func() {
		r.Drain()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			slog.Warn("Goal measurement still busy at shutdown")
		}
	}, r.Live
}

func startDepthSwitch(ctx context.Context, cfg *config.Config, appStore, schedStore store.Store, commands *schedcmd.Service, disc *discover.Service) {
	if !cfg.Goals.Enabled || disc == nil {
		return
	}
	sw := &goalswitch.Switcher{App: appStore.DB(), Sched: schedStore.DB(), Commands: commands, Classes: disc.ProjectClasses, Mode: cfg.Goals.DepthSwitch}
	go sw.Run(ctx, time.Hour)
	slog.Info("Depth goal switch started", "mode", cfg.Goals.DepthSwitch)
}

func goalOptions(cfg *config.Config, boosts goals.SeasonBoostSource, busy func() bool) goalmeasure.Options {
	return goalmeasure.Options{
		Interval:     time.Duration(cfg.Goals.IntervalMinutes) * time.Minute,
		MaxSubs:      cfg.Goals.MaxSubs,
		Publish:      cfg.Goals.Publish,
		SeasonBoosts: boosts,
		Workers:      cfg.Goals.Workers,
		Busy:         busy,
	}
}

func withLongitude(o goalmeasure.Options, site discover.SiteFunc) goalmeasure.Options {
	o.Longitude = func(ctx context.Context) (float64, error) {
		s, err := site(ctx)
		return s.Longitude, err
	}
	return o
}

func credentials(cfg *config.Config) *miniocreds.Credentials {
	return miniocreds.NewStaticV4(cfg.S3.AccessKey, cfg.S3.SecretKey, "")
}

var errNoSchedulerDB = errors.New("no scheduler database")

type commandSnapshot struct {
	monitor *observatory.Monitor
	sched   *gorm.DB
}

func (c commandSnapshot) SchedulerState() (schedcmd.SchedulerState, bool) {
	return c.monitor.SchedulerState()
}

func (c commandSnapshot) TargetPlans(ctx context.Context, targetID int64) ([]schedcmd.PlanState, error) {
	if c.sched == nil {
		return nil, errNoSchedulerDB
	}
	return schedcmd.TargetPlansFrom(ctx, c.sched, targetID)
}

func (c commandSnapshot) ProjectAltitudes(ctx context.Context, projectID int64) (float64, float64, error) {
	if c.sched == nil {
		return 0, 0, errNoSchedulerDB
	}
	return schedcmd.ProjectAltitudesFrom(ctx, c.sched, projectID)
}

func newScheduler(ctx context.Context, cfg *config.Config, appStore, schedulerDBStore store.Store, broker *events.Broker, onCommand func(schedcmd.Record)) (*schedcmd.Service, *observatory.Monitor, *observatory.Client) {
	obs := observatory.NewClient(cfg.Scheduler.URL, cfg.Scheduler.Token)
	transports := []schedcmd.Transport{observatory.NewAPITransport(obs)}
	if cfg.Scheduler.Queue {
		transports = append(transports, observatory.NewQueueTransport(schedulerDBStore.DB()))
	}
	commands := &schedcmd.Service{
		Log:        schedcmd.NewGormLog(appStore.DB()),
		AppDB:      appStore.DB(),
		Transports: transports,
		Notify: func(r schedcmd.Record) {
			onCommand(r)
			broker.Broadcast("command", r, false)
		},
	}
	redeliver := &observatory.Redeliverer{Service: commands, Client: obs}
	monitor := observatory.NewMonitor(obs, commands, broker)
	monitor.Contacts = observatory.GormContacts{DB: appStore.DB()}
	commands.Snapshot = commandSnapshot{monitor: monitor, sched: schedulerDBStore.DB()}
	monitor.OnOnline = func() { redeliver.Once(ctx) }
	go monitor.Run(ctx)
	go redeliver.Run(ctx, observatory.RedeliverInterval)
	if cfg.Scheduler.Queue {
		go observatory.NewQueueResults(schedulerDBStore.DB(), commands).Run(ctx, observatory.QueueResultInterval)
	}
	if obs.Configured() {
		slog.Info("Scheduler API configured", "url", cfg.Scheduler.URL, "queue", cfg.Scheduler.Queue)
	} else {
		slog.Info("Scheduler API not configured; commands stay queued", "queue", cfg.Scheduler.Queue)
	}
	return commands, monitor, obs
}

func attachSubLengths(disc *discover.Service, s *sublength.Service) {
	if disc != nil {
		disc.SubLengths = s.Latest
	}
}

func plannedDarks(s *sublength.Service) func(context.Context) []darkneed.Planned {
	return func(ctx context.Context) []darkneed.Planned {
		proposed := s.Proposed(ctx)
		out := make([]darkneed.Planned, 0, len(proposed))
		for _, p := range proposed {
			out = append(out, darkneed.Planned{Exposure: p.Exposure, Gain: p.Gain, Offset: p.Offset, SetTemp: p.SetTemp, Basis: p.Basis})
		}
		return out
	}
}

func bodyNightFunc(ref *atomic.Pointer[bodies.NightService]) mosaicplan.BodyNightFunc {
	return func(guid, night string) (mosaics.Point, bool, bool) {
		n := ref.Load()
		if n == nil {
			return mosaics.Point{}, false, false
		}
		ra, dec, usable, isBody := n.NightPoint(guid, night)
		return mosaics.Point{RA: ra, Dec: dec}, usable, isBody
	}
}
