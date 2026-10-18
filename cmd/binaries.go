package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/activity"
	"github.com/USA-RedDragon/astro-stacker/internal/binaries"
	"github.com/USA-RedDragon/astro-stacker/internal/config"
	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/events"
	"github.com/USA-RedDragon/astro-stacker/internal/goals"
	"github.com/USA-RedDragon/astro-stacker/internal/planning"
	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"github.com/USA-RedDragon/astro-stacker/internal/server"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
	"github.com/USA-RedDragon/astro-stacker/internal/store"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/ownimage"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/surveys"
	configulator "github.com/USA-RedDragon/configulator/v2"
	"github.com/minio/minio-go/v7"
	"github.com/spf13/cobra"
)

func (c schedCommands) Undo(ctx context.Context, id, author string) (schedcmd.Record, error) {
	return c.s.Undo(ctx, id, author)
}

// newBinariesService builds the discovery pages' service: offline reads of
// the vet's caches and the catalog mirrors, the processed bucket for its
// files, and the scheduler for imaging requests. readOnly keeps the
// object store from being written.
func newBinariesService(cfg *config.Config, s3 *minio.Client, appStore, schedStore store.Store, commands *schedcmd.Service, backfill func() goals.BackfillLive, task *activity.Task) *binaries.Service {
	processed := discoveries.MinioStore{Client: s3, Bucket: cfg.S3.ProcessedBucket}
	gaia, vsx := catalogMirrors(cfg, s3)
	fields := surveys.MinioStore{Client: s3, Bucket: cfg.S3.ProcessedBucket}
	data := ownimage.DBData{App: appStore.DB(), Store: processed, Gaia: gaia}
	vs := &vetting.Service{App: appStore.DB(), Data: data, Gaia: gaia, Surveys: surveys.Offline(fields, cfg.Vetting.CachePrefix, time.Now)}
	site := siteFunc(cfg, appStore)
	svc := &binaries.Service{DB: appStore.DB(), Stars: vs, VSX: vsx, Objects: processed, Frame: data.Frame, Loc: alertLoc(cfg), Task: task,
		Site: func(ctx context.Context) (sky.Site, error) { return site(ctx) },
		Snapshot: func(ctx context.Context) (*planning.Snapshot, error) {
			return server.LoadPlanningSnapshot(ctx, appStore.DB(), schedStore.DB(), backfill)
		}}
	if commands != nil {
		svc.Commands = schedCommands{s: commands}
	}
	if gaia != nil {
		svc.Photometry = binaries.OwnPhotometry{Data: data, Threads: cfg.Vetting.Threads, WorkDir: cfg.Vetting.WorkDir}
	}
	return svc
}

// startBinaries runs the discovery pages' background work, kicked by new
// masters and vet reports.
func startBinaries(ctx context.Context, cfg *config.Config, appStore, schedStore store.Store, broker *events.Broker, commands *schedcmd.Service,
	backfill func() goals.BackfillLive, reg *activity.Registry) *binaries.Service {
	if !needsS3(cfg) {
		return nil
	}
	s3, err := newS3(cfg)
	if err != nil {
		slog.Warn("Discovery pages are off: no S3 client", "error", err)
		return nil
	}
	svc := newBinariesService(cfg, s3, appStore, schedStore, commands, backfill, reg.Task(activity.KindBinaries, "Confirmed variable stars"))
	svc.TimingTask = reg.Task(activity.KindEclipseTiming, "Eclipse timings")
	broker.Listen(func(e events.Event) {
		if e.Type == events.TypeMaster || e.Type == events.TypeDiscovery {
			svc.Kick()
		}
	})
	go svc.Run(ctx)
	slog.Info("Discovery pages background work started")
	return svc
}

// newBinariesCommand prints every confirmed variable star's facts and
// flags as the pages would show them, reading only: no rows, files or
// pictures are written.
func newBinariesCommand() *cobra.Command {
	var dryRun, timings, debug bool
	var sources []string
	c := &cobra.Command{
		Use:          "binaries",
		Short:        "Print the confirmed variable stars' survey facts and flags (--dry-run; writes nothing)",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !dryRun {
				return fmt.Errorf("only --dry-run is supported; the stacker works the facts out in the background")
			}
			cl, err := configulator.FromContext[config.Config](cmd.Context())
			if err != nil {
				return fmt.Errorf("failed to get config from context")
			}
			cfg, err := cl.Load()
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}
			appStore, err := store.OpenReadOnly(cfg.Storage.Type, cfg.Storage.DSN.App)
			if err != nil {
				return err
			}
			s3, err := newS3(cfg)
			if err != nil {
				return err
			}
			svc := newBinariesService(cfg, s3, appStore, appStore, nil, nil, nil)
			var ids []int
			for _, s := range sources {
				id, err := strconv.Atoi(s)
				if err != nil {
					return fmt.Errorf("--source must be a number: %w", err)
				}
				ids = append(ids, id)
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			if timings && debug {
				out, err := svc.DebugTimings(cmd.Context(), ids)
				if err != nil {
					return err
				}
				return enc.Encode(out)
			}
			if timings {
				out, err := svc.DryTimings(cmd.Context(), ids)
				if err != nil {
					return err
				}
				return enc.Encode(out)
			}
			out, err := svc.DryRun(cmd.Context(), ids)
			if err != nil {
				return err
			}
			return enc.Encode(out)
		},
	}
	c.Flags().BoolVar(&dryRun, "dry-run", false, "work the facts out and print them with the flags; write nothing")
	c.Flags().BoolVar(&timings, "timings", false, "with --dry-run, time every eclipse your frames cover and print the timing records instead")
	c.Flags().BoolVar(&debug, "debug", false, "with --timings, print what each timing is based on instead: every frame's measurement, each filter's fit and the whole night's light curve")
	c.Flags().StringSliceVar(&sources, "source", nil, "only these source ids")
	return c
}
