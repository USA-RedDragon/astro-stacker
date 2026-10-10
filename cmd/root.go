package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"syscall"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/conditions"
	"github.com/USA-RedDragon/astro-stacker/internal/config"
	"github.com/USA-RedDragon/astro-stacker/internal/events"
	"github.com/USA-RedDragon/astro-stacker/internal/goalmeasure"
	"github.com/USA-RedDragon/astro-stacker/internal/goals"
	"github.com/USA-RedDragon/astro-stacker/internal/indexer"
	"github.com/USA-RedDragon/astro-stacker/internal/mosaicplan"
	"github.com/USA-RedDragon/astro-stacker/internal/observatory"
	"github.com/USA-RedDragon/astro-stacker/internal/preview"
	"github.com/USA-RedDragon/astro-stacker/internal/previewer"
	"github.com/USA-RedDragon/astro-stacker/internal/publicframe"
	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"github.com/USA-RedDragon/astro-stacker/internal/server"
	"github.com/USA-RedDragon/astro-stacker/internal/server/middleware"
	"github.com/USA-RedDragon/astro-stacker/internal/skycutout"
	"github.com/USA-RedDragon/astro-stacker/internal/store"
	configulator "github.com/USA-RedDragon/configulator/v2"
	"github.com/lmittmann/tint"
	"github.com/minio/minio-go/v7"
	miniocreds "github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/spf13/cobra"
	"github.com/ztrue/shutdown"
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
	mosaicPlans := newMosaicPlans(cfg, appStore, schedulerDBStore)
	var restacker middleware.Restacker
	drainStacker := func() {}
	drainGoals := func() {}
	var stackBusy func() bool
	var backfill func() goals.BackfillLive
	broker := events.NewBroker()
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
			go ix.Run(indexCtx, time.Duration(cfg.Indexer.IntervalSeconds)*time.Second)
			slog.Info("Frame indexer started", "bucket", cfg.S3.Bucket, "interval_seconds", cfg.Indexer.IntervalSeconds)
		}
		if cfg.Previews.Enabled {
			pv := previewer.New(s3, cfg.S3.Bucket, cfg.S3.ProcessedBucket, appStore.DB(), cfg.Previews.Concurrency,
				preview.Options{MaxWidth: cfg.Previews.MaxWidth, Quality: cfg.Previews.Quality})
			pv.Events = broker
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

	disc, collabs := newDiscover(indexCtx, cfg, appStore, schedulerDBStore, backfill)
	commands, monitor, obs := newScheduler(indexCtx, cfg, appStore, schedulerDBStore, broker, func(r schedcmd.Record) {
		if disc != nil && r.Category == schedcmd.CategoryMatching {
			disc.Invalidate()
		}
	})
	extras := server.Extras{Commands: commands, Scheduler: monitor, Mosaics: mosaicPlans, Discover: disc, Collabs: collabs, Backfill: backfill,
		Cutouts:    &skycutout.Service{DB: appStore.DB(), Endpoint: cfg.Discover.HiPS2FITSURL, PerMinute: cfg.Discover.CutoutsPerMinute, Off: !cfg.Discover.SkyCutouts},
		Conditions: conditions.New(conditions.Options{MetricsURL: cfg.Scheduler.MetricsURL, UPS: cfg.Scheduler.UPS}, schedulerDBStore.DB())}
	if obs.Configured() {
		extras.Previews = obs
	}

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

func startGoals(cfg *config.Config, s3 *minio.Client, appStore, schedStore store.Store, boosts goals.SeasonBoostSource, busy func() bool) (func(), func() goals.BackfillLive) {
	r := goalmeasure.New(appStore.DB(), schedStore.DB(), goalmeasure.MinioGetter{Client: s3, Bucket: cfg.S3.ProcessedBucket},
		goalmeasure.VizierFetcher(nil, ""), goalmeasure.Options{
			Interval:     time.Duration(cfg.Goals.IntervalMinutes) * time.Minute,
			MaxSubs:      cfg.Goals.MaxSubs,
			Publish:      cfg.Goals.Publish,
			SeasonBoosts: boosts,
			Workers:      cfg.Goals.Workers,
			Busy:         busy,
		})
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

func credentials(cfg *config.Config) *miniocreds.Credentials {
	return miniocreds.NewStaticV4(cfg.S3.AccessKey, cfg.S3.SecretKey, "")
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
