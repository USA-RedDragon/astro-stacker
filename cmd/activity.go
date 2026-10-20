package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/activity"
	"github.com/USA-RedDragon/astro-stacker/internal/config"
	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/events"
	"github.com/USA-RedDragon/astro-stacker/internal/goals"
	"github.com/USA-RedDragon/astro-stacker/internal/observatory"
	"github.com/USA-RedDragon/astro-stacker/internal/store"
)

// startActivity reads the outside work and sends the snapshot to the event
// stream.
func startActivity(ctx context.Context, reg *activity.Registry, cfg *config.Config, appStore, schedStore store.Store, broker *events.Broker, backfill func() goals.BackfillLive,
	discoveriesLive func() discoveries.Live, monitor *observatory.Monitor) {
	jobs := &activity.Jobs{Watches: jobWatches()}
	if k, err := activity.InCluster(); err == nil {
		jobs.Kube = k
		go jobs.Run(ctx, 20*time.Second, func(err error) {
			slog.Warn("Reading the batch Jobs failed", "error", err)
		})
	} else {
		slog.Info("Batch Jobs not shown: no Kubernetes service account", "error", err)
	}
	src := &activitySources{DB: appStore.DB(), Sched: schedStore.DB(), Jobs: jobs, Discoveries: discoveriesLive, Monitor: monitor,
		SmallBodies: cfg.Discoveries.SmallBodiesURL, Processed: cfg.S3.ProcessedBucket, VetCache: cfg.Vetting.CachePrefix, BackfillDays: cfg.Discoveries.BackfillDays}
	if cfg.Discover.CatalogMirror {
		src.Survey = cfg.Discover.SurveyBucket
	}
	if needsS3(cfg) {
		if s3, err := newS3(cfg); err == nil {
			src.S3 = s3
		}
	}
	src.register(reg)
	if backfill != nil {
		reg.AddSource(activity.Source{Name: "goals", Every: 5 * time.Second, Read: func(context.Context) ([]activity.Item, error) {
			return []activity.Item{goalsItem(backfill())}, nil
		}})
	}
	go reg.Run(ctx, broker)
	slog.Info("Background activity reporting started", "jobs", jobs.Kube != nil)
}

// goalsItem is the goal measurement backlog as an item.
func goalsItem(l goals.BackfillLive) activity.Item {
	it := activity.Item{Kind: activity.KindGoals, Label: "Goal depth measurement", State: activity.StateIdle, Unit: "subs",
		Done: int64(l.DoneInPass), Total: int64(l.DoneInPass + l.Queued), Started: l.PassStarted}
	switch l.State {
	case goals.BackfillRunning:
		it.State = activity.StateRunning
		it.Current = fmt.Sprintf("%d workers measuring", l.Workers)
		if l.Workers == 1 {
			it.Current = "1 worker measuring"
		}
	case goals.BackfillPaused:
		it.State = activity.StateQueued
		it.Current = "waiting for stacking to finish"
	}
	return it
}
