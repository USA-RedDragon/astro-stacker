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
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/calmatch"
	"github.com/USA-RedDragon/astro-stacker/internal/coverage"
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
	// RetryAfter is how long to wait before retrying a sub that lacked
	// calibration or failed.
	RetryAfter       time.Duration
	SirilThreads     int
	SirilMemoryRatio float64
	Stack            Options
}

var DefaultPipelineOptions = PipelineOptions{
	MinScore:         0.3,
	Pedestal:         quality.DefaultPedestal,
	BatchSize:        12,
	RetryAfter:       24 * time.Hour,
	SirilThreads:     4,
	SirilMemoryRatio: 0.5,
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
}

func NewPipeline(s3 *minio.Client, source, dest string, db, sched *gorm.DB, runner siril.Runner, workDir string, opts PipelineOptions) *Pipeline {
	if opts.BatchSize < 1 {
		opts.BatchSize = DefaultPipelineOptions.BatchSize
	}
	return &Pipeline{s3: s3, source: source, dest: dest, db: db, sched: sched, siril: runner, workDir: workDir, opts: opts}
}

// Run stacks new lights until ctx is cancelled, sleeping interval when idle.
func (p *Pipeline) Run(ctx context.Context, interval time.Duration) {
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
	q := p.db.WithContext(ctx).Model(&app.Frame{}).
		Joins("LEFT JOIN stack_frames sf ON sf.frame_id = frames.id").
		Where("frames.type = ? AND frames.index_error IS NULL AND frames.object <> '' AND frames.filter <> ''", "LIGHT").
		Where("sf.id IS NULL OR (sf.status IN ? AND sf.processed_at < ?)",
			[]string{app.StackStatusCalibration, app.StackStatusFailed, app.StackStatusRegistration}, time.Now().Add(-p.opts.RetryAfter))
	if object != "" {
		q = q.Where("frames.object = ?", object)
	}
	if filter != "" {
		q = q.Where("frames.filter = ?", filter)
	}
	// Work one master at a time, oldest subs first, so a target's master
	// grows in the order it was shot.
	var first app.Frame
	if err := q.Order("frames.object, frames.filter, frames.date_obs").First(&first).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, nil
		}
		return 0, fmt.Errorf("find lights: %w", err)
	}
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

	var batch []candidate
	for _, f := range frames {
		c, status := p.classify(f, scores, sets)
		if status != "" {
			if err := p.record(ctx, app.StackFrame{FrameID: f.ID, Status: status, Score: c.score.Score, Exposure: val(f.Exposure)}); err != nil {
				return 0, err
			}
			continue
		}
		batch = append(batch, c)
		if len(batch) == p.opts.BatchSize {
			break
		}
	}
	if len(batch) > 0 {
		if err := p.stackBatch(ctx, first.Object, first.Filter, batch, sets); err != nil {
			return len(frames), err
		}
	}
	return len(frames), nil
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
func (p *Pipeline) record(ctx context.Context, sf app.StackFrame) error {
	sf.ProcessedAt = time.Now()
	var existing app.StackFrame
	err := p.db.WithContext(ctx).Where("frame_id = ?", sf.FrameID).First(&existing).Error
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
