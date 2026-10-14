package cmd

import (
	"fmt"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/config"
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

// newDustCheckCommand reports the dust check's findings.
func newDustCheckCommand() *cobra.Command {
	c := &cobra.Command{Use: "dust-check", Short: "Report dust changes found in flats"}
	c.AddCommand(newDustReportCommand())
	return c
}

func newDustReportCommand() *cobra.Command {
	var all bool
	c := &cobra.Command{
		Use:          "report",
		Short:        "Print the flat dust changes recorded; the database is only read",
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
			return nil
		},
	}
	c.Flags().BoolVar(&all, "all", false, "also print the differences passed over (diffuse, edge, halo, rotshift)")
	return c
}
