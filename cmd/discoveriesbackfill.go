package cmd

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/catalog"
	"github.com/USA-RedDragon/astro-stacker/internal/config"
	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/store"
	configulator "github.com/USA-RedDragon/configulator/v2"
	"github.com/spf13/cobra"
)

// newDiscoveriesCommand compares the due nights in a process of its own,
// beside the server's runner, then exits. Runs either one claims are left
// to it.
func newDiscoveriesCommand() *cobra.Command {
	var object, night, group string
	var passes int
	c := &cobra.Command{
		Use:          "discoveries",
		Short:        "Compare the nights that are due (or one --object/--night) with their targets' other nights, then exit",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cl, err := configulator.FromContext[config.Config](cmd.Context())
			if err != nil {
				return fmt.Errorf("failed to get config from context")
			}
			cfg, err := cl.Load()
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
			d := cfg.Discoveries
			cat := tnsCatalogs(d)
			if m := loadTNSMirror(cmd.Context(), cfg, s3); m != nil {
				cat.TNSLocal, cat.TNSMatchArcsec = discoveries.LocalTNS(m.Index), d.TNSMatchArcsec
			}
			cat.Local = smallBodies(cfg, appStore)
			cat.GaiaMirror, cat.VSXMirror = catalogMirrors(cfg, s3)
			r := discoveries.New(appStore.DB(), schedStore.DB(), discoveries.MinioStore{Client: s3, Bucket: cfg.S3.ProcessedBucket}, cat,
				discoveries.Options{Threads: d.Threads, RefSubs: d.RefSubs, BackfillDays: d.BackfillDays, Catalog: sync.OnceValues(catalog.LoadEmbedded),
					SurveyVariables: surveyVariables(cfg, s3)})
			if object != "" {
				t, err := time.Parse(time.DateOnly, night)
				if err != nil {
					return fmt.Errorf("--night must be YYYY-MM-DD: %w", err)
				}
				r.Request(object, t, group)
			}
			for i := range passes {
				start := time.Now()
				if err := r.Pass(cmd.Context()); err != nil {
					return err
				}
				live := r.Live()
				slog.Info("Discoveries pass done", "pass", i+1, "runs", live.Done, "took", time.Since(start).Round(time.Second))
				if live.Done == 0 {
					break
				}
			}
			return nil
		},
	}
	c.Flags().StringVar(&object, "object", "", "compare only this target's --night (all its filter groups unless --group)")
	c.Flags().StringVar(&night, "night", "", "the night's evening date, YYYY-MM-DD, with --object")
	c.Flags().StringVar(&group, "group", "", "broadband or one narrowband filter, with --object")
	c.Flags().IntVar(&passes, "passes", 1, "passes over the due nights; stops early when a pass finds none")
	return c
}
