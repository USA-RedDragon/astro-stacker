package stacking

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/calmatch"
	"github.com/USA-RedDragon/astro-stacker/internal/coverage"
	"github.com/USA-RedDragon/astro-stacker/internal/events"
	"github.com/USA-RedDragon/astro-stacker/internal/measure"
	"github.com/USA-RedDragon/astro-stacker/internal/metrics"
	"github.com/USA-RedDragon/astro-stacker/internal/quality"
	"github.com/USA-RedDragon/astro-stacker/internal/siril"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/tslink"
	"github.com/minio/minio-go/v7"
	"gorm.io/gorm"
)

// PipelineOptions configure the stacker.
type PipelineOptions struct {
	// MinScore is the lowest sub score that goes into a master.
	MinScore float64
	// Pedestal is the camera's ADU offset, for scoring.
	Pedestal float64
	// BatchSize is how many subs of one master are calibrated and
	// registered per Siril run.
	BatchSize int
	// RetryAfter is how long to wait before checking again for calibration
	// a sub lacked.
	RetryAfter time.Duration
	// FailureBackoff is the wait after a sub's first failure, doubling after
	// each further one; after MaxAttempts failures the sub is dead.
	FailureBackoff time.Duration
	MaxAttempts    int
	SirilThreads   int
	// SirilMemoryRatio is the share of memory all Siril runs together may
	// use; each worker's Siril gets its part.
	SirilMemoryRatio float64
	// Workers is how many targets are stacked at once.
	Workers int
	// MosaicInterval is how often mosaics are checked (0 turns them off);
	// a mosaic is rebuilt once its panel masters have been unchanged for
	// MosaicQuiet.
	MosaicInterval time.Duration
	MosaicQuiet    time.Duration
	MosaicSeams    bool
	// CalibrationSettle is how long a flat, dark or bias set must go
	// without a new frame before a master is built from it. Sets arrive a
	// frame at a time, a dark library over hours; a master built meanwhile
	// holds part of the set, and so do the lights calibrated with it.
	CalibrationSettle time.Duration
	// RecalibrateLimit is the most stacked lights waiting at once to be
	// calibrated again (recalibrateDarks), so a new dark library is worked
	// into the masters a few hundred lights at a time.
	RecalibrateLimit int
	// Photometry holds lights back until the indexer has measured their
	// starlight (measure.Photometry), which their transparency is scored
	// from; off, they are scored without it.
	Photometry bool
	// Verdicts tell Target Scheduler which subs were left out (see
	// sendVerdicts).
	Verdicts VerdictOptions
	Stack    Options

	RegisteredGrace        time.Duration
	RegisteredDeletePause  time.Duration
	RegisteredBackfillRate float64
}

func DefaultPipelineOptions() PipelineOptions {
	return PipelineOptions{
		MinScore:  0.3,
		Pedestal:  quality.DefaultPedestal,
		BatchSize: 12,
		// Tonight's subs usually wait for the morning's flats; rechecking is
		// only a database query.
		RetryAfter:        3 * time.Hour,
		FailureBackoff:    30 * time.Minute,
		MaxAttempts:       5,
		SirilThreads:      4,
		SirilMemoryRatio:  0.5,
		Workers:           1,
		MosaicInterval:    10 * time.Minute,
		MosaicQuiet:       30 * time.Minute,
		MosaicSeams:       true,
		CalibrationSettle: 3 * time.Hour,
		RecalibrateLimit:  300,
		Stack:             DefaultOptions(),

		RegisteredGrace:        24 * time.Hour,
		RegisteredDeletePause:  200 * time.Millisecond,
		RegisteredBackfillRate: 0.5,
	}
}

// Pipeline calibrates, registers and stacks lights as they arrive.
type Pipeline struct {
	s3      *minio.Client
	source  string // raw frames
	dest    string // processed outputs
	db      *gorm.DB
	sched   *gorm.DB
	siril   siril.Runner
	workDir string
	opts    PipelineOptions

	// scorer keeps the scheduler's parsed acquired images between batches.
	scorer quality.Scorer

	// busy holds the targets workers are stacking, so no two work on the
	// same target's reference and masters.
	mu   sync.Mutex
	busy map[string]bool
	// building serializes work on one calibration master's files.
	building sync.Map // set key -> *sync.Mutex
	// sites caches each target's observing site (latitude, east longitude),
	// read from one of its lights' headers.
	sites sync.Map // object -> [2]float64, or nil when the headers have none

	// Events, if set, hears about updated masters and what workers do.
	Events *events.Broker

	statusMu sync.Mutex
	working  map[string]*events.Worker // by object
	dirty    bool

	// phots caches lights' parsed photometry, by frame id.
	photMu sync.Mutex
	phots  map[int]cachedPhotometry

	// verdictFiles caches acquired images' file names, by Id, for the
	// verdicts.
	verdictMu    sync.Mutex
	verdictFiles map[int]string

	linker     tslink.Linker
	seamBudget int

	// drain is closed when the pipeline should stop taking on work, and
	// finish what it has (Drain).
	drain     chan struct{}
	drainOnce sync.Once

	objects registeredStore
}

// Drain stops the pipeline taking on new work: each loop finishes the batch,
// master, mosaic or comet it is on and returns, and so does Run.
func (p *Pipeline) Drain() {
	p.drainOnce.Do(func() { close(p.drain) })
}

// stopping reports whether a loop should stop before its next piece of
// work: the context ended, or the pipeline is draining.
func (p *Pipeline) stopping(ctx context.Context) bool {
	if ctx.Err() != nil {
		return true
	}
	select {
	case <-p.drain:
		return true
	default:
		return false
	}
}

// pause waits d, or less if the pipeline stops meanwhile; it reports
// whether to carry on.
func (p *Pipeline) pause(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-p.drain:
		return false
	case <-time.After(d):
		return true
	}
}

func NewPipeline(s3 *minio.Client, source, dest string, db, sched *gorm.DB, runner siril.Runner, workDir string, opts PipelineOptions) *Pipeline {
	if opts.BatchSize < 1 {
		opts.BatchSize = DefaultPipelineOptions().BatchSize
	}
	opts.Workers = max(1, opts.Workers)
	if opts.MaxAttempts < 1 {
		opts.MaxAttempts = DefaultPipelineOptions().MaxAttempts
	}
	if opts.FailureBackoff <= 0 {
		opts.FailureBackoff = DefaultPipelineOptions().FailureBackoff
	}
	if opts.RecalibrateLimit < 1 {
		opts.RecalibrateLimit = DefaultPipelineOptions().RecalibrateLimit
	}
	return &Pipeline{s3: s3, source: source, dest: dest, db: db, sched: sched, siril: runner, workDir: workDir, opts: opts,
		busy: map[string]bool{}, working: map[string]*events.Worker{}, drain: make(chan struct{})}
}

// Run stacks new lights with opts.Workers workers until ctx is cancelled,
// each sleeping interval when idle.
func (p *Pipeline) Run(ctx context.Context, interval time.Duration) {
	var wg sync.WaitGroup
	wg.Go(func() { p.reportStatus(ctx) })
	wg.Go(func() {
		p.reregisterPrecalibrated(ctx)
		p.requeueWeightless(ctx)
		p.requeueLeakFailures(ctx)
		p.requeueOffTarget(ctx)
		// Recropping re-renders covers of the masters it touches.
		p.recropMasters(ctx)
		p.backfillCovers(ctx)
		p.republishMasters(ctx)
		p.requeueStackerRejected(ctx)
		p.markOldRejection(ctx)
		p.markMixedGains(ctx)
		// The exposure templates' moon avoidance can change: hourly.
		// Duplicates found in masters are left for the sweep to restack.
		// Masters are scored again as their lights' photometry comes in,
		// before the sweep, which restacks what that marks.
		for !p.stopping(ctx) {
			p.rescoreAdded(ctx)
			if err := p.dropDuplicates(ctx); err != nil && ctx.Err() == nil {
				slog.Error("Taking duplicates out of masters failed", "error", err)
			}
			if err := p.moonSweep(ctx); err != nil && ctx.Err() == nil {
				slog.Error("Moon sweep failed", "error", err)
			}
			if !p.pause(ctx, time.Hour) {
				return
			}
		}
	})
	wg.Go(func() { p.runRegisteredGC(ctx) })
	wg.Go(func() { p.runRegisteredBackfill(ctx) })
	if p.opts.MosaicInterval > 0 {
		wg.Go(func() { p.runMosaics(ctx, p.opts.MosaicInterval) })
	}
	if m := p.opts.Verdicts.Mode; m == verdictModeOn || m == verdictModeDryRun {
		wg.Go(func() { p.runVerdicts(ctx, p.opts.Verdicts) })
	}
	for range p.opts.Workers {
		wg.Go(func() { p.work(ctx, interval) })
	}
	wg.Wait()
}

func (p *Pipeline) work(ctx context.Context, interval time.Duration) {
	for !p.stopping(ctx) {
		n, err := p.RunOnce(ctx, "", "")
		if err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("Stacking failed", "error", err)
		}
		if n > 0 && err == nil {
			continue
		}
		if !p.pause(ctx, interval) {
			return
		}
	}
}

// candidate is a light waiting to be stacked.
type candidate struct {
	frame app.Frame
	score quality.SubScore
	cal   calmatch.Result
	// waiting is the matched set a light waits for to settle, if any.
	waiting *calmatch.Set
	// offBy is how far, in degrees, the mount said it pointed from the
	// target, when that is more than OffTargetDegrees; 0 otherwise.
	offBy float64
}

// RunOnce processes one batch of new lights for one master and returns how
// many lights it looked at. object and filter narrow it to one master.
func (p *Pipeline) RunOnce(ctx context.Context, object, filter string) (int, error) {
	q := p.pendingLights(p.db.WithContext(ctx), time.Now())
	if object != "" {
		q = q.Where("frames.object = ?", object)
	}
	if filter != "" {
		q = q.Where("frames.filter = ?", filter)
	}
	// Work one master at a time, oldest subs first, so a target's master
	// grows in the order it was shot, on a target no other worker has.
	first, err := p.claim(q)
	if errors.Is(err, errNothingToClaim) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer p.release(first.Object)
	p.progress(first.Object, first.Filter, StageStarting, 0, 0)
	defer p.finished(first.Object)
	var frames []app.Frame
	if err := q.Where("frames.object = ? AND frames.filter = ?", first.Object, first.Filter).
		Order("frames.date_obs").Limit(4 * p.opts.BatchSize).Find(&frames).Error; err != nil {
		return 0, fmt.Errorf("load lights: %w", err)
	}

	measured, err := p.measuredSubs(ctx)
	if err != nil {
		return 0, err
	}
	scores, err := p.scorer.Load(ctx, p.sched, p.opts.Pedestal, measured)
	if err != nil {
		return 0, err
	}
	sets, err := coverage.Sets(ctx, p.db)
	if err != nil {
		return 0, err
	}
	positions, err := targetPositions(ctx, p.sched)
	if err != nil {
		return 0, err
	}
	moonlit, moonFree, err := p.moonlitFrames(ctx, positions, first, frames)
	if err != nil {
		return 0, err
	}
	batch, err := p.triage(ctx, frames, scores, sets, positions, moonlit, moonFree)
	if err != nil {
		return 0, err
	}
	if len(batch) > 0 {
		if err := p.runBatch(ctx, first, batch, sets, scores, positions); err != nil {
			return 0, err
		}
	}
	return len(frames), nil
}

func (p *Pipeline) moonlitFrames(ctx context.Context, positions map[string][2]float64, first *app.Frame, frames []app.Frame) (map[int]string, bool, error) {
	moonCheck, err := p.moonChecker(ctx, positions)
	if err != nil {
		return nil, false, err
	}
	moonlit := make(map[int]string, len(frames))
	moonFree, err := p.hasMoonFree(ctx, moonCheck, first.Object, first.Filter)
	if err != nil {
		return nil, false, err
	}
	for _, f := range frames {
		moonlit[f.ID] = moonCheck.why(ctx, f)
		moonFree = moonFree || moonlit[f.ID] == ""
	}
	return moonlit, moonFree, nil
}

func (p *Pipeline) triage(ctx context.Context, frames []app.Frame, scores map[string]quality.SubScore, sets []calmatch.Set,
	positions map[string][2]float64, moonlit map[int]string, moonFree bool,
) ([]candidate, error) {
	retrying, err := p.failedBefore(ctx, frames)
	if err != nil {
		return nil, err
	}
	dups, err := p.duplicates(ctx, frames)
	if err != nil {
		return nil, err
	}
	var batch []candidate
	for _, f := range frames {
		if dups[f.ID] {
			if err := p.record(ctx, app.StackFrame{FrameID: f.ID, Status: app.StackStatusDuplicate, Exposure: val(f.Exposure)}); err != nil {
				return nil, err
			}
			continue
		}
		c, status := p.classify(f, scores, sets, positions)
		status = withMoon(status, moonlit[f.ID] != "", moonFree)
		if status != "" {
			sf := p.leftOut(c, status)
			if why := moonlit[f.ID]; status == app.StackStatusMoon {
				sf.Error = &why
			}
			if err := p.record(ctx, sf); err != nil {
				return nil, err
			}
			continue
		}
		// A sub that failed before is retried on its own, so a bad file
		// can only fail itself.
		if retrying[f.ID] {
			if len(batch) == 0 {
				batch = append(batch, c)
			}
			break
		}
		batch = append(batch, c)
		if len(batch) == p.opts.BatchSize {
			break
		}
	}
	return batch, nil
}

func (p *Pipeline) leftOut(c candidate, status string) app.StackFrame {
	f := c.frame
	sf := app.StackFrame{FrameID: f.ID, Status: status, Score: c.score.Score, Exposure: val(f.Exposure)}
	if status == app.StackStatusLowScore {
		msg := p.lowScoreReason(c.score, f.Filter)
		sf.Error = &msg
	}
	if status == app.StackStatusUnmeasured {
		msg := "not measured: " + c.score.Missing
		sf.Error = &msg
	}
	if status == app.StackStatusCalibration {
		sf.Error = missingCalibration(c.cal)
		if w := c.waiting; w != nil {
			msg := fmt.Sprintf("waiting for the %s set (%d frames) to settle: a frame was uploaded %s",
				strings.ToLower(w.Type), w.Count, w.Uploaded.UTC().Format(time.RFC3339))
			sf.Error = &msg
			next := w.Uploaded.Add(p.opts.CalibrationSettle + time.Minute)
			sf.NextAttemptAt = &next
		}
	}
	return sf
}

func (p *Pipeline) runBatch(ctx context.Context, first *app.Frame, batch []candidate, sets []calmatch.Set,
	scores map[string]quality.SubScore, positions map[string][2]float64,
) error {
	start := time.Now()
	err := p.stackBatch(ctx, first.Object, first.Filter, batch, sets, scores, positions)
	metrics.BatchSeconds.Observe(time.Since(start).Seconds())
	if err == nil {
		metrics.Batches.WithLabelValues("ok").Inc()
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	metrics.Batches.WithLabelValues("failed").Inc()
	slog.Error("Stacking batch failed", "object", first.Object, "filter", first.Filter, "subs", len(batch), "error", err)
	msg := err.Error()
	for _, c := range batch {
		if err := p.record(ctx, app.StackFrame{FrameID: c.frame.ID, Status: app.StackStatusFailed,
			Score: c.score.Score, Exposure: val(c.frame.Exposure), Error: &msg}); err != nil {
			return err
		}
	}
	return nil
}

// failedBefore returns the frames among these with failed attempts.
func (p *Pipeline) failedBefore(ctx context.Context, frames []app.Frame) (map[int]bool, error) {
	ids := make([]int, 0, len(frames))
	for _, f := range frames {
		ids = append(ids, f.ID)
	}
	var failed []int
	if err := p.db.WithContext(ctx).Model(&app.StackFrame{}).Where("frame_id IN ? AND attempts > 0", ids).
		Pluck("frame_id", &failed).Error; err != nil {
		return nil, err
	}
	out := make(map[int]bool, len(failed))
	for _, id := range failed {
		out[id] = true
	}
	return out, nil
}

var errNothingToClaim = errors.New("no lights to claim")

// LiveWindow is how recent a sub must be to go ahead of the backfill.
const LiveWindow = 48 * time.Hour

// claim picks the first light of a target no other worker is stacking and
// marks the target busy. It returns errNothingToClaim when there is nothing
// to do.
func (p *Pipeline) claim(q *gorm.DB) (*app.Frame, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.busy) > 0 {
		busy := make([]string, 0, len(p.busy))
		for o := range p.busy {
			busy = append(busy, o)
		}
		q = q.Session(&gorm.Session{}).Where("frames.object NOT IN ?", busy)
	}
	// Subs from the last two nights go first, newest first, so tonight's
	// data reaches its master ahead of the backfill.
	var first app.Frame
	err := q.Session(&gorm.Session{}).Where("frames.date_obs > ?", time.Now().Add(-LiveWindow)).
		Order("frames.date_obs DESC").First(&first).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = q.Session(&gorm.Session{}).Order("frames.object, frames.filter, frames.date_obs").First(&first).Error
	}
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errNothingToClaim
		}
		return nil, fmt.Errorf("find lights: %w", err)
	}
	p.busy[first.Object] = true
	return &first, nil
}

// hold marks a target busy, as claim does, unless it already is.
func (p *Pipeline) hold(object string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.busy[object] {
		return false
	}
	p.busy[object] = true
	return true
}

func (p *Pipeline) release(object string) {
	p.mu.Lock()
	delete(p.busy, object)
	p.mu.Unlock()
}

// lockKey serializes work on one calibration master across workers.
func (p *Pipeline) lockKey(key string) func() {
	m, _ := p.building.LoadOrStore(key, &sync.Mutex{})
	mu, _ := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// classify decides whether a light goes into its master. It returns a status
// when it doesn't.
func (p *Pipeline) classify(f app.Frame, scores map[string]quality.SubScore, sets []calmatch.Set, positions map[string][2]float64) (candidate, string) {
	c := candidate{frame: f}
	// A sub whose mount pointing is off target is still stacked if it
	// registers to the target's reference (see OffTargetDegrees).
	if pos, ok := positions[f.Object]; ok && f.MountRA != nil && f.MountDec != nil {
		if d := separation(*f.MountRA, *f.MountDec, pos[0], pos[1]); d > OffTargetDegrees {
			c.offBy = d
		}
	}
	s, ok := scores[path.Base(f.Key)]
	if !ok {
		return c, app.StackStatusNoMetadata
	}
	c.score = s
	switch {
	case s.GradingStatus == quality.GradingRejected && !s.StackerRejected:
		// Rejected by Target Scheduler or by hand. One the stacker rejected
		// itself (its verdicts) is judged again like any other, so a sub
		// that qualifies again comes back and its verdict is undone.
		return c, app.StackStatusRejected
	case !(s.Score > 0) && s.Missing != "":
		return c, app.StackStatusUnmeasured
	case !(s.Score > 0):
		return c, app.StackStatusLowScore
	case s.Score < p.opts.MinScore*s.TargetBest:
		// Against the target's best rather than MinScore alone: a panel
		// imaged only under the moon is better stacked, weighted low as its
		// score says, than a hole in the mosaic.
		return c, app.StackStatusLowScore
	case f.Exposure == nil || *f.Exposure <= 0 || f.Night == nil:
		return c, app.StackStatusNoMetadata
	}
	if precalibrated(f) {
		return c, ""
	}
	c.cal = calmatch.Choose(calmatch.Group{
		Night: *f.Night, Filter: f.Filter, Exposure: *f.Exposure, Gain: val(f.Gain), Offset: val(f.Offset),
		SetTemp: val(f.SetTemp), BinX: val(f.BinX), Rotator: val(f.Rotator),
	}, sets)
	// A missing dark doesn't hold a light back: at the cold setpoints darks
	// are missing for, dark current is small and rejection takes the hot
	// pixels. It is calibrated again once a dark comes (recalibrateDarks).
	if c.cal.Flat.Set == nil || c.cal.Bias.Set == nil {
		return c, app.StackStatusCalibration
	}
	// A matched set still arriving is waited for rather than worked around
	// with another: the light would be calibrated twice, and the wait is
	// only CalibrationSettle.
	for _, s := range []*calmatch.Set{c.cal.Bias.Set, c.cal.Flat.Set, c.cal.Dark.Set} {
		if s != nil && !p.settled(*s, time.Now()) {
			c.waiting = s
			return c, app.StackStatusCalibration
		}
	}
	return c, ""
}

// withMoon is a light's status once moon avoidance is applied: a light
// classify would stack is left out if it breaks its filter's moon avoidance
// and its master has moon-free lights. It comes after the score, so a moonlit
// light the stacker rejected in Target Scheduler (a "stacker: moon" verdict)
// and classifies again is left out again, whatever it scores, and its
// verdict stands. In a master of nothing but moonlit lights it is stacked,
// as it would have been before.
func withMoon(status string, moonlit, moonFree bool) string {
	if status == "" && moonlit && moonFree {
		return app.StackStatusMoon
	}
	return status
}

// settled reports whether a calibration set has stopped growing: none of
// its frames was uploaded in the last CalibrationSettle. Imported masters
// and sets of unknown upload time are taken as they are.
func (p *Pipeline) settled(s calmatch.Set, now time.Time) bool {
	return s.Master != "" || s.Uploaded.IsZero() || now.Sub(s.Uploaded) >= p.opts.CalibrationSettle
}

// requeueWeightless sends subs stacked with a weight of 0, which added
// nothing, back to be scored again, and marks their masters for a rebuild.
func (p *Pipeline) requeueWeightless(ctx context.Context) {
	now := time.Now()
	err := p.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var stacks []int
		if err := tx.Model(&app.StackFrame{}).Where("status = ? AND NOT (weight > 0) AND stack_id IS NOT NULL", app.StackStatusAdded).
			Distinct("stack_id").Pluck("stack_id", &stacks).Error; err != nil || len(stacks) == 0 {
			return err
		}
		res := tx.Model(&app.StackFrame{}).Where("status = ? AND NOT (weight > 0)", app.StackStatusAdded).
			UpdateColumns(map[string]any{columnStatus: app.StackStatusLowScore, columnNextAttemptAt: now})
		if res.Error != nil {
			return res.Error
		}
		slog.Info("Scoring again subs stacked with no weight", "subs", res.RowsAffected, "masters", len(stacks))
		return tx.Model(&app.Stack{}).Where("id IN ?", stacks).UpdateColumn("needs_rebuild", true).Error
	})
	if err != nil && ctx.Err() == nil {
		slog.Error("Requeueing weightless subs failed", "error", err)
	}
}

// requeueLeakFailures gives lights that failed because the dark set they
// matched had too few clean frames left another go from scratch: the matcher
// used to pick such a set again every time, so they failed until given up
// on; it now passes it over for the next best.
func (p *Pipeline) requeueLeakFailures(ctx context.Context) {
	res := p.db.WithContext(ctx).Model(&app.StackFrame{}).
		Where("status IN ? AND error LIKE ?", []string{app.StackStatusFailed, app.StackStatusDead}, "%light leak%").
		UpdateColumns(map[string]any{columnStatus: app.StackStatusFailed, "attempts": 0, columnNextAttemptAt: time.Now()})
	if res.Error != nil {
		if ctx.Err() == nil {
			slog.Error("Requeueing lights failed on leaky darks failed", "error", res.Error)
		}
		return
	}
	if res.RowsAffected > 0 {
		slog.Info("Trying again lights that failed on a leaky dark set", "lights", res.RowsAffected)
	}
}

// requeueOffTarget gives subs left out as off target on their mount
// pointing alone another go, to be stacked if they register to the target's
// reference (see OffTargetDegrees). Those have no error recorded; subs left
// out since carry offTargetError and stay out, so this runs once. Most of
// the 330 rows are a parked mount's subs (0/0 or the pole), which fail to
// register and return to off target. They wait as failed with no
// attempts, as requeueLeakFailures does, not as registration failures,
// which maybeReReference would count against the reference.
func (p *Pipeline) requeueOffTarget(ctx context.Context) {
	msg := "the mount pointed off target; trying whether the sub registers to the target reference"
	res := p.db.WithContext(ctx).Model(&app.StackFrame{}).
		Where("status = ? AND error IS NULL", app.StackStatusOffTarget).
		UpdateColumns(map[string]any{columnStatus: app.StackStatusFailed, "attempts": 0, columnNextAttemptAt: time.Now(), columnError: msg})
	if res.Error != nil {
		if ctx.Err() == nil {
			slog.Error("Requeueing off-target subs failed", "error", res.Error)
		}
		return
	}
	if res.RowsAffected > 0 {
		slog.Info("Trying again subs left out on their mount pointing alone", "subs", res.RowsAffected)
	}
}

// precalibrated reports whether a light came calibrated, as Telescope.live
// delivers them ("…_cal.fits"): it is registered as it is.
func precalibrated(f app.Frame) bool {
	return strings.Contains(path.Base(f.Key), "_cal.")
}

// record upserts what happened to one light.
// Failures (failed, registration) count attempts and are retried with a
// doubling backoff until MaxAttempts, when the sub is dead. Missing
// calibration is rechecked every RetryAfter, as new frames may arrive.
// measuredSubs are the lights measured from their pixels: their sky and
// stars when Target Scheduler has no record of them, and their photometry.
func (p *Pipeline) measuredSubs(ctx context.Context) ([]quality.Measured, error) {
	var frames []app.Frame
	if err := p.db.WithContext(ctx).Select("id", "key", "object", "filter", "exposure", "offset", "sky_adu", "star_hfr", "star_count", "photometry_rev").
		Where("(measured_at IS NOT NULL AND star_hfr > 0) OR photometry_rev IS NOT NULL").Find(&frames).Error; err != nil {
		return nil, fmt.Errorf("load measured lights: %w", err)
	}
	phots, err := p.photometry(ctx, frames)
	if err != nil {
		return nil, err
	}
	out := make([]quality.Measured, 0, len(frames))
	for _, f := range frames {
		m := quality.Measured{File: path.Base(f.Key), Target: f.Object, Filter: f.Filter, Exposure: val(f.Exposure),
			Offset: val(f.Offset), Calibrated: precalibrated(f), Photometry: phots[f.ID]}
		if f.StarHFR != nil && *f.StarHFR > 0 {
			m.SkyADU, m.HFR, m.Stars = val(f.SkyADU), val(f.StarHFR), intVal(f.StarCount)
		}
		out = append(out, m)
	}
	return out, nil
}

type cachedPhotometry struct {
	rev  int
	phot *measure.Photometry
}

// photometry is the parsed photometry of frames, by id, read from the
// database only for frames not cached at their revision.
func (p *Pipeline) photometry(ctx context.Context, frames []app.Frame) (map[int]*measure.Photometry, error) {
	p.photMu.Lock()
	defer p.photMu.Unlock()
	if p.phots == nil {
		p.phots = map[int]cachedPhotometry{}
	}
	var stale []int
	for _, f := range frames {
		if f.PhotometryRev == nil {
			continue
		}
		if c, ok := p.phots[f.ID]; !ok || c.rev != *f.PhotometryRev {
			stale = append(stale, f.ID)
		}
	}
	for i := 0; i < len(stale); i += 1000 {
		var rows []app.Frame
		if err := p.db.WithContext(ctx).Select("id", "photometry", "photometry_rev").
			Where("id IN ?", stale[i:min(i+1000, len(stale))]).Find(&rows).Error; err != nil {
			return nil, fmt.Errorf("load photometry: %w", err)
		}
		for _, r := range rows {
			c := cachedPhotometry{rev: intVal(r.PhotometryRev)}
			if r.Photometry != nil {
				var ph measure.Photometry
				if json.Unmarshal([]byte(*r.Photometry), &ph) == nil && len(ph.Flux) == len(ph.Saturated) {
					c.phot = &ph
				}
			}
			p.phots[r.ID] = c
		}
	}
	out := make(map[int]*measure.Photometry, len(frames))
	for _, f := range frames {
		if c, ok := p.phots[f.ID]; ok && c.phot != nil && f.PhotometryRev != nil && c.rev == *f.PhotometryRev {
			out[f.ID] = c.phot
		}
	}
	return out, nil
}

func intVal(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}

// missingCalibration names the calibration frames a light has no match for.
func missingCalibration(m calmatch.Result) *string {
	var missing []string
	for _, c := range []struct {
		name string
		ok   bool
	}{{"flat", m.Flat.Set != nil}, {"dark", m.Dark.Set != nil}, {"bias", m.Bias.Set != nil}} {
		if !c.ok {
			missing = append(missing, c.name)
		}
	}
	msg := "no matching " + strings.Join(missing, ", ")
	return &msg
}

func (p *Pipeline) record(ctx context.Context, sf app.StackFrame) error {
	sf.ProcessedAt = time.Now()
	var existing app.StackFrame
	err := p.db.WithContext(ctx).Where("frame_id = ?", sf.FrameID).First(&existing).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	switch sf.Status {
	case app.StackStatusCalibration, app.StackStatusLowScore, app.StackStatusUnmeasured, app.StackStatusNoMetadata, app.StackStatusMoon:
		// Unless the caller knows better, as for a set about to settle.
		if sf.NextAttemptAt == nil {
			next := sf.ProcessedAt.Add(p.opts.RetryAfter)
			sf.NextAttemptAt = &next
		}
	case app.StackStatusFailed, app.StackStatusRegistration:
		sf.Attempts = existing.Attempts + 1
		if sf.Attempts >= p.opts.MaxAttempts {
			slog.Warn("Giving up on sub", "frame_id", sf.FrameID, "attempts", sf.Attempts, "status", sf.Status)
			sf.Status = app.StackStatusDead
		} else {
			next := sf.ProcessedAt.Add(p.opts.FailureBackoff << (sf.Attempts - 1))
			sf.NextAttemptAt = &next
		}
	}
	metrics.Subs.WithLabelValues(sf.Status).Inc()
	switch {
	case err == nil:
		sf.ID = existing.ID
		return p.db.WithContext(ctx).Save(&sf).Error
	case errors.Is(err, gorm.ErrRecordNotFound):
		return p.db.WithContext(ctx).Create(&sf).Error
	default:
		return err
	}
}

func val(p *float64) float64 {
	if p == nil {
		return math.NaN()
	}
	return *p
}

func (p *Pipeline) download(ctx context.Context, bucket, key, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	obj, err := p.s3.GetObject(ctx, bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return fmt.Errorf("get %s: %w", key, err)
	}
	defer obj.Close()
	tmp := dst + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, obj); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("download %s: %w", key, err)
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

func (p *Pipeline) upload(ctx context.Context, src, key, contentType string) error {
	_, err := p.s3.FPutObject(ctx, p.dest, key, src, minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return fmt.Errorf("upload %s: %w", key, err)
	}
	return nil
}
