package cmd

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/catalog"
	"github.com/USA-RedDragon/astro-stacker/internal/config"
	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/events"
	"github.com/USA-RedDragon/astro-stacker/internal/server"
	"github.com/USA-RedDragon/astro-stacker/internal/store"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/minio/minio-go/v7"
)

// needsS3 says whether any enabled service reads the buckets.
func needsS3(cfg *config.Config) bool {
	return cfg.Indexer.Enabled || cfg.Previews.Enabled || cfg.Stacking.Enabled || cfg.PublicFrames.Enabled || cfg.Goals.Enabled || cfg.Discoveries.Enabled
}

// discoveryHandle is the running night comparisons, or no-ops when they
// are off.
type discoveryHandle struct {
	source server.DiscoverySource
	kick   func()
	drain  func()
}

// startDiscoveries starts the night comparisons in the background when
// they are on.
func startDiscoveries(cfg *config.Config, s3 *minio.Client, appStore, schedStore store.Store, busy func() bool, broker *events.Broker) discoveryHandle {
	d := cfg.Discoveries
	if !d.Enabled {
		return discoveryHandle{drain: func() {}}
	}
	cat := &discoveries.Catalogs{TNSKey: d.TNSKey, TNSBotID: d.TNSBotID, TNSBotName: d.TNSBotName}
	r := discoveries.New(appStore.DB(), schedStore.DB(), discoveries.MinioStore{Client: s3, Bucket: cfg.S3.ProcessedBucket}, cat, discoveries.Options{
		Interval:     time.Duration(d.IntervalMinutes) * time.Minute,
		Threads:      d.Threads,
		RefSubs:      d.RefSubs,
		BackfillDays: d.BackfillDays,
		Busy:         busy,
		Catalog:      sync.OnceValues(catalog.LoadEmbedded),
		OnRun: func(run app.DiscoveryRun) {
			broker.Publish(events.Event{Type: events.TypeDiscovery, Object: run.Object, Filter: run.Group, Key: run.Night.Format(time.DateOnly)})
		},
	})
	done := make(chan struct{})
	go func() {
		r.Run(context.Background())
		close(done)
	}()
	slog.Info("Discoveries started", "backstop_minutes", d.IntervalMinutes, "threads", d.Threads, "reference_subs", d.RefSubs,
		"backfill_days", d.BackfillDays, "tns", cat.TNSEnabled(), "waits_for_stacking", busy != nil)
	return discoveryHandle{source: r, kick: r.Kick, drain: func() {
		r.Drain()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			slog.Warn("Discoveries still busy at shutdown")
		}
	}}
}
