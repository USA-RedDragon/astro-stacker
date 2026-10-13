package cmd

import (
	"context"
	"log/slog"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/bodies"
	"github.com/USA-RedDragon/astro-stacker/internal/config"
	"github.com/USA-RedDragon/astro-stacker/internal/observatory"
	"github.com/USA-RedDragon/astro-stacker/internal/schedauto"
	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"github.com/USA-RedDragon/astro-stacker/internal/store"
)

func startScheduling(ctx context.Context, cfg *config.Config, appStore, schedStore store.Store, commands *schedcmd.Service,
	bodyNights *bodies.NightService, horizon *observatory.HorizonProvider) *schedauto.Service {
	s := &schedauto.Service{App: appStore.DB(), Sched: schedStore.DB(), Commands: commands,
		Site: schedauto.SiteFunc(siteFunc(cfg, appStore)), Mode: cfg.Scheduling.Auto}
	if bodyNights != nil {
		s.BodyWindows = bodyNights.Windows
	}
	if horizon != nil {
		s.Horizon = horizon.Get
	}
	go s.Run(ctx, time.Duration(cfg.Scheduling.IntervalHours)*time.Hour)
	slog.Info("Global scheduling settings started", "auto", cfg.Scheduling.Auto, "backstop_hours", cfg.Scheduling.IntervalHours)
	return s
}
