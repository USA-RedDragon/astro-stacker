package cmd

import (
	"context"
	"log/slog"

	"github.com/USA-RedDragon/astro-stacker/internal/activity"
	"github.com/USA-RedDragon/astro-stacker/internal/config"
	"github.com/USA-RedDragon/astro-stacker/internal/discoverywatch"
	"github.com/USA-RedDragon/astro-stacker/internal/events"
	"github.com/USA-RedDragon/astro-stacker/internal/goals"
	"github.com/USA-RedDragon/astro-stacker/internal/planning"
	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"github.com/USA-RedDragon/astro-stacker/internal/server"
	"github.com/USA-RedDragon/astro-stacker/internal/store"
)

// startDiscoveryWatch keeps the finds the vet calls real at low confidence
// in Watching and asks the scheduler for their fields in the gaps, kicked
// by compared nights.
func startDiscoveryWatch(ctx context.Context, cfg *config.Config, appStore, schedStore store.Store, broker *events.Broker, commands *schedcmd.Service,
	backfill func() goals.BackfillLive, reg *activity.Registry) *discoverywatch.Service {
	svc := &discoverywatch.Service{DB: appStore.DB(), Loc: alertLoc(cfg), Task: reg.Task(activity.KindWatching, "Watched finds"),
		Snapshot: func(ctx context.Context) (*planning.Snapshot, error) {
			return server.LoadPlanningSnapshot(ctx, appStore.DB(), schedStore.DB(), backfill)
		}}
	if commands != nil {
		svc.Commands = schedCommands{s: commands}
	}
	broker.Listen(func(e events.Event) {
		if e.Type == events.TypeDiscovery {
			svc.Kick()
		}
	})
	go svc.Run(ctx)
	slog.Info("Discovery watch started")
	return svc
}
