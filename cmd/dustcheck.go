package cmd

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/config"
	"github.com/USA-RedDragon/astro-stacker/internal/stacking"
	"github.com/USA-RedDragon/astro-stacker/internal/store"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	configulator "github.com/USA-RedDragon/configulator/v2"
	"github.com/spf13/cobra"
)

func loadConfig(cmd *cobra.Command) (*config.Config, error) {
	c, err := configulator.FromContext[config.Config](cmd.Context())
	if err != nil {
		return nil, fmt.Errorf("failed to get config from context")
	}
	cfg, err := c.Load()
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}
	return cfg, nil
}

// newDustCheckCommand reports the dust check's findings and runs the lights
// check over archived lights.
func newDustCheckCommand() *cobra.Command {
	c := &cobra.Command{Use: "dust-check", Short: "Report dust changes found in flats, or check archived lights for dust"}
	c.AddCommand(newDustReportCommand(), newDustLightsCommand())
	return c
}

func newDustReportCommand() *cobra.Command {
	var all bool
	c := &cobra.Command{
		Use:          "report",
		Short:        "Print the flat dust changes and lights checks recorded; the database is only read",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			appStore, err := store.OpenReadOnly(cfg.Storage.Type, cfg.Storage.DSN.App)
			if err != nil {
				return err
			}
			db := appStore.DB().WithContext(cmd.Context())
			var flats []app.DustFlat
			if err := db.Order("taken_at").Find(&flats).Error; err != nil {
				return err
			}
			byID := map[int]app.DustFlat{}
			compared := 0
			for _, f := range flats {
				byID[f.ID] = f
				if f.PrevID != nil {
					compared++
				}
			}
			fmt.Printf("%d flat sets checked, %d compared with an earlier one\n", len(flats), compared)
			q := db.Order("taken_at, filter, id")
			if !all {
				q = q.Where("class IN ?", []string{"new", "gone", "moved"})
			}
			var evs []app.DustEvent
			if err := q.Find(&evs).Error; err != nil {
				return err
			}
			for _, e := range evs {
				prev := byID[e.PrevID]
				fmt.Printf("%s %-9s since %s %-8s %-7s %4d,%4d d%4d %+.2f%% in %s\n", e.TakenAt.UTC().Format("2006-01-02T15:04"),
					e.Filter, prev.TakenAt.UTC().Format(time.DateOnly), e.Class, e.Scope, e.X, e.Y, e.Diameter, e.Depth, e.Filters)
			}
			var checks []app.DustLightCheck
			if err := db.Order("night").Find(&checks).Error; err != nil {
				return err
			}
			for _, c := range checks {
				fmt.Printf("lights %s: %d subs, %d fields, %s, deepest %.2f%% %s\n", c.Night.Format(time.DateOnly), c.Subs, c.Fields, c.Status, c.Deepest, c.Discs)
			}
			return nil
		},
	}
	c.Flags().BoolVar(&all, "all", false, "also print the differences passed over (diffuse, edge, halo, rotshift)")
	return c
}

func newDustLightsCommand() *cobra.Command {
	var from, to string
	var days, per int
	var probes []string
	c := &cobra.Command{
		Use:          "lights",
		Short:        "Check archived lights for dust their flats don't hold, window by window; builds missing masters, writes residuals under dust/history",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			h := stacking.DustHistory{Days: days, Per: per}
			if h.From, err = time.Parse(time.DateOnly, from); err != nil {
				return fmt.Errorf("--from: %w", err)
			}
			if h.To, err = time.Parse(time.DateOnly, to); err != nil {
				return fmt.Errorf("--to: %w", err)
			}
			for _, pr := range probes {
				xs, ys, ok := strings.Cut(pr, ",")
				x, errX := strconv.Atoi(xs)
				y, errY := strconv.Atoi(ys)
				if !ok || errX != nil || errY != nil {
					return fmt.Errorf("--probe %q: want x,y", pr)
				}
				h.Probes = append(h.Probes, [2]int{x, y})
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
			return newPipeline(cfg, s3, appStore, schedStore).DustLightsHistory(cmd.Context(), h, os.Stdout)
		},
	}
	c.Flags().StringVar(&from, "from", "", "first night, YYYY-MM-DD")
	c.Flags().StringVar(&to, "to", "", "night after the last, YYYY-MM-DD")
	c.Flags().IntVar(&days, "days", 7, "days per window")
	c.Flags().IntVar(&per, "per", 8, "most lights of one target and filter per window")
	c.Flags().StringSliceVar(&probes, "probe", nil, "sensor position x,y (rows top first) to print the residual at; repeatable")
	return c
}
