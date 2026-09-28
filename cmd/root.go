package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"syscall"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/config"
	"github.com/USA-RedDragon/astro-stacker/internal/events"
	"github.com/USA-RedDragon/astro-stacker/internal/indexer"
	"github.com/USA-RedDragon/astro-stacker/internal/preview"
	"github.com/USA-RedDragon/astro-stacker/internal/previewer"
	"github.com/USA-RedDragon/astro-stacker/internal/server"
	"github.com/USA-RedDragon/astro-stacker/internal/store"
	"github.com/USA-RedDragon/configulator"
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
		logger = slog.New(tint.NewHandler(os.Stdout, &tint.Options{Level: slog.LevelDebug}))
	case config.LogLevelInfo:
		logger = slog.New(tint.NewHandler(os.Stdout, &tint.Options{Level: slog.LevelInfo}))
	case config.LogLevelWarn:
		logger = slog.New(tint.NewHandler(os.Stderr, &tint.Options{Level: slog.LevelWarn}))
	case config.LogLevelError:
		logger = slog.New(tint.NewHandler(os.Stderr, &tint.Options{Level: slog.LevelError}))
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
	broker := events.NewBroker()
	if cfg.Indexer.Enabled || cfg.Previews.Enabled || cfg.Stacking.Enabled {
		creds := credentials(cfg)
		s3, err := newS3(cfg)
		if err != nil {
			return fmt.Errorf("failed to create S3 client: %w", err)
		}
		if cfg.Indexer.Enabled {
			ix := indexer.New(s3, cfg.S3.Bucket, appStore.DB(), cfg.Indexer.Concurrency)
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
		// Presigned URLs, for previews and masters, are signed for the public
		// host browsers use.
		public, err := minio.New(cfg.S3.PublicEndpoint, &minio.Options{Creds: creds, Secure: cfg.S3.PublicUseSSL, Region: cfg.S3.Region})
		if err != nil {
			return fmt.Errorf("failed to create public S3 client: %w", err)
		}
		signer = previewer.NewSigner(public, cfg.S3.ProcessedBucket, time.Duration(cfg.Previews.URLTTLSeconds)*time.Second)
		if cfg.Stacking.Enabled {
			p := newPipeline(cfg, s3, appStore, schedulerDBStore)
			p.Events = broker
			go p.Run(indexCtx, time.Duration(cfg.Stacking.IntervalSeconds)*time.Second)
			slog.Info("Stacker started", "min_score", cfg.Stacking.MinScore, "work_dir", cfg.Stacking.WorkDir)
		}
	}

	server := server.NewServer(cfg, appStore, schedulerDBStore, signer, broker, cmd.Annotations["version"])
	if err := server.Start(); err != nil {
		return fmt.Errorf("failed to start server: %w", err)
	}
	slog.Info("Server started successfully")

	stop := func(sig os.Signal) {
		// Remove control codes from the current line in the terminal
		fmt.Println("")

		slog.Info("Received signal", "signal", sig)
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

func credentials(cfg *config.Config) *miniocreds.Credentials {
	return miniocreds.NewStaticV4(cfg.S3.AccessKey, cfg.S3.SecretKey, "")
}
