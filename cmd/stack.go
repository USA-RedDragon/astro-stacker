package cmd

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/config"
	"github.com/USA-RedDragon/astro-stacker/internal/siril"
	"github.com/USA-RedDragon/astro-stacker/internal/stacking"
	"github.com/USA-RedDragon/astro-stacker/internal/store"
	"github.com/USA-RedDragon/configulator"
	"github.com/minio/minio-go/v7"
	"github.com/spf13/cobra"
)

// newStackCommand runs the stacker once, for one target and filter, without
// the server. It's for trying the pipeline on real data.
func newStackCommand() *cobra.Command {
	var object, filter string
	var batches int
	c := &cobra.Command{
		Use:          "stack",
		Short:        "Stack new lights for one target and filter, then exit",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := configulator.FromContext[config.Config](cmd.Context())
			if err != nil {
				return fmt.Errorf("failed to get config from context")
			}
			cfg, err := c.Load()
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}
			appStore, err := store.NewAppStore(cfg)
			if err != nil {
				return err
			}
			schedStore, err := store.NewSchedulerDBStore(cfg)
			if err != nil {
				return err
			}
			s3, err := newS3(cfg)
			if err != nil {
				return err
			}
			p := newPipeline(cfg, s3, appStore, schedStore)
			for i := range batches {
				start := time.Now()
				n, err := p.RunOnce(cmd.Context(), object, filter)
				if err != nil {
					return err
				}
				slog.Info("Batch done", "batch", i+1, "lights", n, "duration", time.Since(start).Round(time.Second))
				if n == 0 {
					break
				}
			}
			return nil
		},
	}
	c.Flags().StringVar(&object, "object", "", "target name (OBJECT header)")
	c.Flags().StringVar(&filter, "filter", "", "filter name")
	c.Flags().IntVar(&batches, "batches", 1, "batches to run")
	return c
}

func newS3(cfg *config.Config) (*minio.Client, error) {
	return minio.New(cfg.S3.Endpoint, &minio.Options{
		Creds:  credentials(cfg),
		Secure: cfg.S3.UseSSL,
		Region: cfg.S3.Region,
	})
}

func newPipeline(cfg *config.Config, s3 *minio.Client, appStore, schedStore store.Store) *stacking.Pipeline {
	if err := os.MkdirAll(cfg.Stacking.WorkDir, 0o700); err != nil {
		slog.Warn("Could not create stacking work dir", "dir", cfg.Stacking.WorkDir, "error", err)
	}
	opts := stacking.DefaultPipelineOptions
	opts.MinScore = cfg.Stacking.MinScore
	opts.Pedestal = cfg.Stacking.Pedestal
	opts.BatchSize = cfg.Stacking.BatchSize
	opts.SirilThreads = cfg.Stacking.SirilThreads
	opts.SirilMemoryRatio = cfg.Stacking.SirilMemory
	opts.Workers = cfg.Stacking.Workers
	opts.MosaicInterval = time.Duration(cfg.Stacking.MosaicMinutes) * time.Minute
	opts.MosaicQuiet = time.Duration(cfg.Stacking.MosaicQuiet) * time.Minute
	opts.CalibrationSettle = time.Duration(cfg.Stacking.CalibrationSettle) * time.Minute
	opts.RecalibrateLimit = cfg.Stacking.RecalibrateLimit
	// The indexer measures the lights' starlight; without it, nothing would.
	opts.Photometry = cfg.Indexer.Enabled
	opts.Verdicts = stacking.VerdictOptions{Mode: cfg.Stacking.TSVerdicts, Targets: cfg.Stacking.TSVerdictsTargets, Max: cfg.Stacking.TSVerdictsMax}
	if cfg.Stacking.TSVerdictsSince != "" {
		// Checked by config.Validate.
		opts.Verdicts.Since, _ = time.Parse(time.DateOnly, cfg.Stacking.TSVerdictsSince)
	}
	runner := siril.Runner{Command: cfg.Stacking.SirilCommand}
	if filepath.Base(runner.Command) == "AppRun" {
		// An extracted AppImage picks the binary from its first argument.
		runner.Args = []string{"siril-cli"}
	}
	return stacking.NewPipeline(s3, cfg.S3.Bucket, cfg.S3.ProcessedBucket, appStore.DB(), schedStore.DB(), runner, cfg.Stacking.WorkDir, opts)
}
