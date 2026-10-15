package cmd

import (
	"context"
	"log/slog"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoverysource"
	"github.com/USA-RedDragon/astro-stacker/internal/events"
	"github.com/USA-RedDragon/astro-stacker/internal/store"
	"github.com/USA-RedDragon/astro-stacker/internal/trigger"
)

// startDiscoverySources gathers new discovery candidates into their sources
// at start and soon after each stored run, so a vetted source's vetting
// reaches a new night's candidates without waiting for the inbox to open.
func startDiscoverySources(ctx context.Context, appStore store.Store, broker *events.Broker) {
	d := &trigger.Debouncer{Quiet: 5 * time.Second, MaxWait: time.Minute}
	broker.Listen(func(e events.Event) {
		if e.Type == events.TypeDiscovery {
			d.Kick()
		}
	})
	go d.Run(ctx, 30*time.Minute, func(ctx context.Context, _ []string) {
		start := time.Now()
		if err := discoverysource.Sync(ctx, appStore.DB()); err != nil {
			slog.Error("Discovery sources", "error", err)
			return
		}
		slog.Debug("Discovery sources gathered", "seconds", time.Since(start).Seconds())
	})
}
