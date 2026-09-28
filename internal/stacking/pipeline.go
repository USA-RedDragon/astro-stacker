package stacking

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sync"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/calmatch"
	"github.com/USA-RedDragon/astro-stacker/internal/coverage"
	"github.com/USA-RedDragon/astro-stacker/internal/events"
	"github.com/USA-RedDragon/astro-stacker/internal/metrics"
	"github.com/USA-RedDragon/astro-stacker/internal/quality"
	"github.com/USA-RedDragon/astro-stacker/internal/siril"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
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
	Stack          Options
}

var DefaultPipelineOptions = PipelineOptions{
	MinScore:  0.3,
	Pedestal:  quality.DefaultPedestal,
	BatchSize: 12,
	// Tonight's subs usually wait for the morning's flats; rechecking is
	// only a database query.
	RetryAfter:       3 * time.Hour,
	FailureBackoff:   30 * time.Minute,
	MaxAttempts:      5,
	SirilThreads:     4,
	SirilMemoryRatio: 0.5,
	Workers:          1,
	MosaicInterval:   10 * time.Minute,
	MosaicQuiet:      30 * time.Minute,
	Stack:            DefaultOptions,
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

	// busy holds the targets workers are stacking, so no two work on the
	// same target's reference and masters.
	mu   sync.Mutex
	busy map[string]bool
	// building serializes work on one calibration master's files.
	building sync.Map // set key -> *sync.Mutex

	// Events, if set, hears about updated masters and what workers do.
	Events *events.Broker

	statusMu sync.Mutex
	working  map[string]*events.Worker // by object
	dirty    bool
}

func NewPipeline(s3 *minio.Client, source, dest string, db, sched *gorm.DB, runner siril.Runner, workDir string, opts PipelineOptions) *Pipeline {
	if opts.BatchSize < 1 {
		opts.BatchSize = DefaultPipelineOptions.BatchSize
	}
	opts.Workers = max(1, opts.Workers)
	if opts.MaxAttempts < 1 {
		opts.MaxAttempts = DefaultPipelineOptions.MaxAttempts
	}
	if opts.FailureBackoff <= 0 {
		opts.FailureBackoff = DefaultPipelineOptions.FailureBackoff
	}
	return &Pipeline{s3: s3, source: source, dest: dest, db: db, sched: sched, siril: runner, workDir: workDir, opts: opts,
		busy: map[string]bool{}, working: map[string]*events.Worker{}}
}

// Run stacks new lights with opts.Workers workers until ctx is cancelled,
// each sleeping interval when idle.
func (p *Pipeline) Run(ctx context.Context, interval time.Duration) {
	var wg sync.WaitGroup
	wg.Go(func() { p.reportStatus(ctx) })
	wg.Go(func() {
		// Recropping re-renders covers of the masters it touches.
		p.recropMasters(ctx)
		p.backfillCovers(ctx)
	})
	if p.opts.MosaicInterval > 0 {
		wg.Go(func() { p.runMosaics(ctx, p.opts.MosaicInterval) })
	}
	for range p.opts.Workers {
		wg.Go(func() { p.work(ctx, interval) })
	}
	wg.Wait()
}

func (p *Pipeline) work(ctx context.Context, interval time.Duration) {
	for {
		n, err := p.RunOnce(ctx, "", "")
		if err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("Stacking failed", "error", err)
		}
		if n > 0 && err == nil && ctx.Err() == nil {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

// candidate is a light waiting to be stacked.
type candidate struct {
	frame app.Frame
	score quality.SubScore
	cal   calmatch.Result
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
	if err != nil || first == nil {
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

	scores, err := quality.LoadScores(ctx, p.sched, p.opts.Pedestal)
	if err != nil {
		return 0, err
	}
	sets, err := coverage.Sets(ctx, p.db)
	if err != nil {
		return 0, err
	}

	retrying, err := p.failedBefore(ctx, frames)
	if err != nil {
		return 0, err
	}
	var batch []candidate
	for _, f := range frames {
		c, status := p.classify(f, scores, sets)
		if status != "" {
			if err := p.record(ctx, app.StackFrame{FrameID: f.ID, Status: status, Score: c.score.Score, Exposure: val(f.Exposure)}); err != nil {
				return 0, err
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
	if len(batch) > 0 {
		start := time.Now()
		err := p.stackBatch(ctx, first.Object, first.Filter, batch, sets, scores)
		metrics.BatchSeconds.Observe(time.Since(start).Seconds())
		if err == nil {
			metrics.Batches.WithLabelValues("ok").Inc()
		} else {
			if ctx.Err() != nil {
				return 0, ctx.Err()
			}
			metrics.Batches.WithLabelValues("failed").Inc()
			slog.Error("Stacking batch failed", "object", first.Object, "filter", first.Filter, "subs", len(batch), "error", err)
			msg := err.Error()
			for _, c := range batch {
				if err := p.record(ctx, app.StackFrame{FrameID: c.frame.ID, Status: app.StackStatusFailed,
					Score: c.score.Score, Exposure: val(c.frame.Exposure), Error: &msg}); err != nil {
					return 0, err
				}
			}
		}
	}
	return len(frames), nil
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

// LiveWindow is how recent a sub must be to go ahead of the backfill.
const LiveWindow = 48 * time.Hour

// claim picks the first light of a target no other worker is stacking and
// marks the target busy. It returns nil when there is nothing to do.
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
			return nil, nil
		}
		return nil, fmt.Errorf("find lights: %w", err)
	}
	p.busy[first.Object] = true
	return &first, nil
}

func (p *Pipeline) release(object string) {
	p.mu.Lock()
	delete(p.busy, object)
	p.mu.Unlock()
}

// lockKey serializes work on one calibration master across workers.
func (p *Pipeline) lockKey(key string) func() {
	m, _ := p.building.LoadOrStore(key, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// classify decides whether a light goes into its master. It returns a status
// when it doesn't.
func (p *Pipeline) classify(f app.Frame, scores map[string]quality.SubScore, sets []calmatch.Set) (candidate, string) {
	c := candidate{frame: f}
	s, ok := scores[path.Base(f.Key)]
	if !ok {
		return c, app.StackStatusNoMetadata
	}
	c.score = s
	switch {
	case s.GradingStatus == quality.GradingRejected:
		return c, app.StackStatusRejected
	case s.Score < p.opts.MinScore:
		return c, app.StackStatusLowScore
	case f.Exposure == nil || *f.Exposure <= 0 || f.Night == nil:
		return c, app.StackStatusNoMetadata
	}
	c.cal = calmatch.Choose(calmatch.Group{
		Night: *f.Night, Filter: f.Filter, Exposure: *f.Exposure, Gain: val(f.Gain), Offset: val(f.Offset),
		SetTemp: val(f.SetTemp), BinX: val(f.BinX), Rotator: val(f.Rotator),
	}, sets)
	if c.cal.Flat.Set == nil || c.cal.Dark.Set == nil || c.cal.Bias.Set == nil {
		return c, app.StackStatusCalibration
	}
	return c, ""
}

// record upserts what happened to one light.
// Failures (failed, registration) count attempts and are retried with a
// doubling backoff until MaxAttempts, when the sub is dead. Missing
// calibration is rechecked every RetryAfter, as new frames may arrive.
func (p *Pipeline) record(ctx context.Context, sf app.StackFrame) error {
	sf.ProcessedAt = time.Now()
	var existing app.StackFrame
	err := p.db.WithContext(ctx).Where("frame_id = ?", sf.FrameID).First(&existing).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	switch sf.Status {
	case app.StackStatusCalibration:
		next := sf.ProcessedAt.Add(p.opts.RetryAfter)
		sf.NextAttemptAt = &next
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
		return nan
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
