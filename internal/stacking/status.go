package stacking

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/events"
	"github.com/USA-RedDragon/astro-stacker/internal/metrics"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
)

// Stages a worker reports while stacking a batch.
const (
	StageStarting    = "starting"
	StageCalibrating = "calibrating"
	StageRegistering = "registering"
	StageAdding      = "adding"
	StageRebuilding  = "rebuilding"
	StagePublishing  = "publishing"
)

// progress records what the worker on object is doing.
func (p *Pipeline) progress(object, filter, stage string, done, total int) {
	p.statusMu.Lock()
	defer p.statusMu.Unlock()
	w, ok := p.working[object]
	if !ok {
		w = &events.Worker{Object: object, Started: time.Now()}
		p.working[object] = w
	}
	w.Filter, w.Stage, w.Done, w.Total = filter, stage, done, total
	p.dirty = true
	metrics.WorkersBusy.Set(float64(len(p.working)))
}

func (p *Pipeline) finished(object string) {
	p.statusMu.Lock()
	delete(p.working, object)
	p.dirty = true
	metrics.WorkersBusy.Set(float64(len(p.working)))
	p.statusMu.Unlock()
}

// reportStatus sends a status snapshot when the work changes, at most once
// a second, and recounts the backlog every 30 seconds.
func (p *Pipeline) reportStatus(ctx context.Context) {
	if p.Events == nil {
		return
	}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	var backlog events.Backlog
	var counted time.Time
	for {
		// It stops with the rest on a drain, or Run waits for it until
		// the drain's limit with nothing left to do.
		select {
		case <-ctx.Done():
			return
		case <-p.drain:
			return
		case <-tick.C:
		}
		recount := time.Since(counted) > 30*time.Second
		if recount {
			b, err := p.countBacklog(ctx)
			if err != nil {
				slog.Warn("Could not count the stacking backlog", "error", err)
			} else {
				recount = b != backlog
				backlog = b
			}
			counted = time.Now()
		}
		p.statusMu.Lock()
		send := p.dirty || recount
		p.dirty = false
		workers := make([]events.Worker, 0, len(p.working))
		for _, w := range p.working {
			workers = append(workers, *w)
		}
		p.statusMu.Unlock()
		if !send {
			continue
		}
		slices.SortFunc(workers, func(a, b events.Worker) int { return strings.Compare(a.Object, b.Object) })
		p.Events.SetStatus(events.Status{Workers: workers, Backlog: backlog})
	}
}

func (p *Pipeline) countBacklog(ctx context.Context) (events.Backlog, error) {
	var b events.Backlog
	var pending, total, dead, previews int64
	db := p.db.WithContext(ctx)
	if err := p.pendingLights(db, time.Now()).Count(&pending).Error; err != nil {
		return b, err
	}
	if err := lights(db).Count(&total).Error; err != nil {
		return b, err
	}
	if err := db.Model(&app.StackFrame{}).Where("status = ?", app.StackStatusDead).Count(&dead).Error; err != nil {
		return b, err
	}
	if err := db.Model(&app.Frame{}).
		Where("type = ? AND index_error IS NULL AND preview_key IS NULL AND preview_error IS NULL", "LIGHT").
		Count(&previews).Error; err != nil {
		return b, err
	}
	b.LightsPending, b.LightsDone = int(pending), int(total-pending)
	b.Dead, b.PreviewsPending = int(dead), int(previews)
	metrics.LightsPending.Set(float64(b.LightsPending))
	metrics.LightsDone.Set(float64(b.LightsDone))
	metrics.SubsDead.Set(float64(b.Dead))
	metrics.PreviewsPending.Set(float64(b.PreviewsPending))
	return b, nil
}

// lights are the frames the stacker considers.
func lights(db *gorm.DB) *gorm.DB {
	return db.Model(&app.Frame{}).
		Where("frames.type = ? AND frames.index_error IS NULL AND frames.object <> '' AND frames.filter <> ''", "LIGHT")
}

// retried are the statuses a light is looked at again after RetryAfter:
// missing calibration and scores can arrive later, a low score is relative
// to the target's best, which changes as it is imaged, and moon avoidance
// follows the exposure templates, which can change.
var retried = []string{app.StackStatusCalibration, app.StackStatusFailed, app.StackStatusRegistration,
	app.StackStatusLowScore, app.StackStatusNoMetadata, app.StackStatusRecalibrate, app.StackStatusMoon}

// pendingLights are lights not yet decided, or due for a retry.
func (p *Pipeline) pendingLights(db *gorm.DB, now time.Time) *gorm.DB {
	return lights(db).
		Joins("LEFT JOIN stack_frames sf ON sf.frame_id = frames.id").
		// Rows from before next_attempt_at existed wait RetryAfter.
		Where("sf.id IS NULL OR (sf.status IN ? AND (sf.next_attempt_at < ? OR (sf.next_attempt_at IS NULL AND sf.processed_at < ?)))",
			retried, now, now.Add(-p.opts.RetryAfter))
}
