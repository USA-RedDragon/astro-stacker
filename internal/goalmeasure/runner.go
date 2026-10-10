package goalmeasure

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/frameheader"
	"github.com/USA-RedDragon/astro-stacker/internal/goals"
	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
	"github.com/USA-RedDragon/astro-stacker/internal/rigsource"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/minio/minio-go/v7"
	"golang.org/x/sync/errgroup"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	GrowthRemeasure = 1.2
	failureBackoff  = 6 * time.Hour
	columnObject    = "object"
	columnFilter    = "filter"
)

type ObjectGetter interface {
	Get(ctx context.Context, key string) ([]byte, error)
}

type MinioGetter struct {
	Client *minio.Client
	Bucket string
}

func (m MinioGetter) Get(ctx context.Context, key string) ([]byte, error) {
	o, err := m.Client.GetObject(ctx, m.Bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer o.Close()
	return io.ReadAll(o)
}

type Options struct {
	Interval     time.Duration
	MaxSubs      int
	Publish      string
	Saturation   float32
	SeasonBoosts goals.SeasonBoostSource
}

type Runner struct {
	db      *gorm.DB
	sched   *gorm.DB
	objects ObjectGetter
	stars   StarFetcher
	XP      XPFetcher
	opts    Options
	pub     *goals.Publisher
	now     func() time.Time

	drain     chan struct{}
	drainOnce sync.Once
	failed    map[int]time.Time
}

func New(db, sched *gorm.DB, objects ObjectGetter, stars StarFetcher, opts Options) *Runner {
	if opts.Interval <= 0 {
		opts.Interval = 30 * time.Minute
	}
	if opts.Saturation <= 0 {
		opts.Saturation = goals.DefaultSaturation
	}
	return &Runner{db: db, sched: sched, objects: objects, stars: stars, opts: opts,
		pub:   &goals.Publisher{App: db, Sched: sched, Mode: opts.Publish, SeasonBoosts: opts.SeasonBoosts},
		now:   func() time.Time { return time.Now().UTC() },
		drain: make(chan struct{}), failed: map[int]time.Time{}}
}

func (r *Runner) Drain() { r.drainOnce.Do(func() { close(r.drain) }) }

func (r *Runner) stopping(ctx context.Context) bool {
	if ctx.Err() != nil {
		return true
	}
	select {
	case <-r.drain:
		return true
	default:
		return false
	}
}

func (r *Runner) Run(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-r.drain:
			cancel()
		case <-ctx.Done():
		}
	}()
	for !r.stopping(ctx) {
		if err := r.Pass(ctx); err != nil && ctx.Err() == nil {
			slog.Error("Goal measurement pass failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(r.opts.Interval):
		}
	}
}

func (r *Runner) Pass(ctx context.Context) error {
	goalsByKey, guids := r.loadGoals(ctx)
	todo, err := r.due(ctx, goalsByKey)
	if err != nil {
		return err
	}
	for _, s := range todo {
		if r.stopping(ctx) {
			return ctx.Err()
		}
		if until, ok := r.failed[s.ID]; ok && r.now().Before(until) {
			continue
		}
		var region []goals.Point
		if g, ok := goalsByKey[goals.Key{Object: s.Object, Filter: s.Filter}]; ok {
			region = g.Region
		}
		if err := r.MeasureStack(ctx, s, region, guids[s.Object]); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			r.failed[s.ID] = r.now().Add(failureBackoff)
			slog.Warn("Could not measure goal progress", "object", s.Object, "filter", s.Filter, "error", err)
			continue
		}
		delete(r.failed, s.ID)
	}
	if _, err := r.pub.Publish(ctx); err != nil && ctx.Err() == nil {
		slog.Error("Publishing goal progress failed", "error", err)
	}
	return nil
}

func (r *Runner) loadGoals(ctx context.Context) (map[goals.Key]goals.Goal, map[string]string) {
	if r.sched == nil {
		return nil, nil
	}
	gs, _, err := goals.LoadGoals(ctx, r.db, r.sched)
	if err != nil {
		slog.Warn("Could not load goals from the scheduler database", "error", err)
		gs = nil
	}
	guids, err := goals.ObjectGUIDs(ctx, r.db, r.sched)
	if err != nil {
		slog.Warn("Could not map objects to Target Scheduler targets", "error", err)
		guids = nil
	}
	return gs, guids
}

func needsMeasurement(s app.Stack, m *app.GoalMeasurement, regionHash string) bool {
	switch {
	case m == nil:
		return true
	case m.MethodRevision != goals.MethodRevision:
		return true
	case m.RegionHash != regionHash:
		return true
	case m.EffectiveHours <= 0:
		return s.EffectiveSeconds > 0
	}
	return s.EffectiveSeconds >= GrowthRemeasure*m.EffectiveHours*3600
}

func (r *Runner) due(ctx context.Context, goalsByKey map[goals.Key]goals.Goal) ([]app.Stack, error) {
	var stacks []app.Stack
	if err := r.db.WithContext(ctx).Where("subs > 0").Order("id").Find(&stacks).Error; err != nil {
		return nil, fmt.Errorf("load stacks: %w", err)
	}
	var ms []app.GoalMeasurement
	if err := r.db.WithContext(ctx).Find(&ms).Error; err != nil {
		return nil, fmt.Errorf("load goal measurements: %w", err)
	}
	byKey := make(map[goals.Key]*app.GoalMeasurement, len(ms))
	for i := range ms {
		byKey[goals.Key{Object: ms[i].Object, Filter: ms[i].Filter}] = &ms[i]
	}
	type item struct {
		s  app.Stack
		at time.Time
	}
	var todo []item
	for _, s := range stacks {
		k := goals.Key{Object: s.Object, Filter: s.Filter}
		m := byKey[k]
		if !needsMeasurement(s, m, goals.RegionHash(goalsByKey[k].Region)) {
			continue
		}
		var at time.Time
		if m != nil {
			at = m.MeasuredAt
		}
		todo = append(todo, item{s, at})
	}
	sort.SliceStable(todo, func(i, j int) bool { return todo[i].at.Before(todo[j].at) })
	out := make([]app.Stack, len(todo))
	for i, t := range todo {
		out[i] = t.s
	}
	return out, nil
}

type subRow struct {
	ID            int
	Score         float64
	Weight        float64
	Exposure      float64
	RegisteredKey string
	Gain          *float64
	FrameID       int
	DateObs       *time.Time
	Night         *time.Time
}

func gainScales(s string) map[float64]float64 {
	if s == "" {
		return nil
	}
	var m map[string]float64
	if json.Unmarshal([]byte(s), &m) != nil {
		return nil
	}
	out := make(map[float64]float64, len(m))
	for k, v := range m {
		if g, err := strconv.ParseFloat(k, 64); err == nil && v > 0 {
			out[g] = v
		}
	}
	return out
}

func (r *Runner) loadSubs(ctx context.Context, stack app.Stack) ([]subRow, error) {
	var rows []subRow
	if err := r.db.WithContext(ctx).Table("stack_frames sf").
		Select("sf.id, sf.score, sf.weight, sf.exposure, sf.registered_key, f.gain, f.id AS frame_id, f.date_obs, f.night").
		Joins("JOIN frames f ON f.id = sf.frame_id").
		Where("sf.stack_id = ? AND sf.status = ? AND sf.registered_key IS NOT NULL AND sf.weight > 0 AND sf.exposure > 0",
			stack.ID, app.StackStatusAdded).
		Order("sf.id").Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("load subs: %w", err)
	}
	return rows, nil
}

func (r *Runner) MeasureStack(ctx context.Context, stack app.Stack, region []goals.Point, guid string) error {
	start := time.Now()
	rows, err := r.loadSubs(ctx, stack)
	if err != nil {
		return err
	}
	seed := goals.Seed(stack.Object, stack.Filter)
	pick := goals.Subset(len(rows), r.opts.MaxSubs, seed)
	scales := gainScales(stack.GainScales)
	subs := make([]goals.Sub, len(pick))
	used := make([]subRow, len(pick))
	for i, j := range pick {
		row := rows[j]
		exp := row.Exposure
		if row.Gain != nil {
			if s, ok := scales[*row.Gain]; ok {
				exp *= s
			}
		}
		subs[i] = goals.Sub{Exposure: exp, Weight: row.Weight, Effective: row.Score * row.Exposure}
		used[i] = row
	}
	totalHours := stack.EffectiveSeconds / 3600
	m := app.GoalMeasurement{
		Object: stack.Object, Filter: stack.Filter, TargetGUID: guid, MethodRevision: goals.MethodRevision,
		Subs: len(pick), SubsTotal: len(rows), EffectiveHours: totalHours, RegionHash: goals.RegionHash(region),
		MeasuredAt: r.now(),
	}
	meas, err := goals.NewMeasurer(stack.Width, stack.Height, subs, goals.Options{Seed: seed, Region: region})
	if errors.Is(err, goals.ErrInsufficientData) {
		msg := goals.ErrInsufficientData.Error()
		m.Error = &msg
		m.Seconds = time.Since(start).Seconds()
		slog.Info("Goal measurement", "object", stack.Object, columnFilter, stack.Filter, "subs", len(rows), "result", msg)
		return r.save(ctx, m)
	}
	if err != nil {
		return err
	}
	skyRates := make([]float64, len(used))
	if err := r.stream(ctx, stack, used, subs, meas, skyRates); err != nil {
		return err
	}
	res, err := meas.Finish(totalHours)
	if err != nil {
		return err
	}
	pts, err := json.Marshal(res.Points)
	if err != nil {
		return err
	}
	m.Levels, m.Sky, m.Signal = res.Levels, res.Sky, res.Signal
	m.NoiseA, m.NoiseB, m.NoiseNow, m.SNR = res.NoiseA, res.NoiseB, res.NoiseNow, res.SNR
	m.GainPerHourPct, m.HeldOutErrPct = res.GainPerHourPct, res.HeldOutErrPct
	m.LowConfidence, m.LowReason = res.LowConfidence, res.LowReason
	m.BandFraction, m.NebFraction, m.NoiseMask = res.BandFraction, res.NebFraction, res.NoiseMask
	m.Points = string(pts)
	r.depth(ctx, stack, res, &m)
	if err := r.saveSkySamples(ctx, stack, m, used, skyRates); err != nil {
		slog.Warn("Could not save sky samples", "object", stack.Object, columnFilter, stack.Filter, "error", err)
	}
	m.Seconds = time.Since(start).Seconds()
	args := []any{"object", stack.Object, columnFilter, stack.Filter, "subs", m.Subs, "subs_total", m.SubsTotal,
		"hours", round(totalHours, 2), "snr", round(m.SNR, 2), "sigma_now", m.NoiseNow, "b", m.NoiseB,
		"gain_per_hour_pct", round(m.GainPerHourPct, 2), "low_confidence", m.LowConfidence}
	if m.Depth != nil {
		args = append(args, "depth", round(*m.Depth, 2), "zero_point", round(*m.ZeroPoint, 3))
	}
	if m.LowReason != "" {
		args = append(args, "reason", m.LowReason)
	}
	args = append(args, "duration", time.Since(start).Round(time.Second))
	slog.Info("Goal measurement", args...)
	return r.save(ctx, m)
}

func round(v float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(v*p) / p
}

func (r *Runner) stream(ctx context.Context, stack app.Stack, rows []subRow, subs []goals.Sub, meas *goals.Measurer, skyRates []float64) error {
	g, gctx := errgroup.WithContext(ctx)
	ch := make(chan goals.Binned, 1)
	g.Go(func() error {
		defer close(ch)
		for i, row := range rows {
			if r.stopping(gctx) {
				return context.Canceled
			}
			binned, err := r.load(gctx, stack, row.RegisteredKey, subs[i].Exposure)
			if err != nil {
				return err
			}
			skyRates[i] = binned.SkyRate
			select {
			case ch <- binned:
			case <-gctx.Done():
				return gctx.Err()
			}
		}
		return nil
	})
	g.Go(func() error {
		i := 0
		for b := range ch {
			if err := meas.Add(i, b); err != nil {
				return err
			}
			i++
		}
		if gctx.Err() == nil && i != len(rows) {
			return fmt.Errorf("added %d of %d subs", i, len(rows))
		}
		return nil
	})
	return g.Wait()
}

func (r *Runner) load(ctx context.Context, stack app.Stack, key string, exposure float64) (goals.Binned, error) {
	b, err := r.objects.Get(ctx, key)
	if err != nil {
		return goals.Binned{}, fmt.Errorf("fetch %s: %w", key, err)
	}
	im, err := imagedata.Decode(b)
	if err != nil {
		return goals.Binned{}, fmt.Errorf("decode %s: %w", key, err)
	}
	if im.W != stack.Width || im.H != stack.Height {
		return goals.Binned{}, fmt.Errorf("%s is %dx%d, master %dx%d", key, im.W, im.H, stack.Width, stack.Height)
	}
	binned, err := goals.Bin(im.Plane(0), im.W, im.H, exposure, r.opts.Saturation)
	if err != nil {
		return goals.Binned{}, fmt.Errorf("bin %s: %w", key, err)
	}
	return binned, nil
}

func (r *Runner) depth(ctx context.Context, stack app.Stack, res goals.Result, m *app.GoalMeasurement) {
	m.PixelScale = 0
	var tr app.TargetReference
	if err := r.db.WithContext(ctx).Where(columnObject+" = ?", stack.Object).First(&tr).Error; err != nil || tr.WCS == nil {
		return
	}
	var cards []frameheader.Card
	if err := json.Unmarshal([]byte(*tr.WCS), &cards); err != nil {
		return
	}
	kw := frameheader.Keywords{}
	for _, c := range cards {
		if _, dup := kw[c.Name]; !dup {
			kw[c.Name] = c.Value
		}
	}
	w, ok := goals.ParseWCS(kw)
	if !ok {
		return
	}
	m.PixelScale = w.PixelScaleArcsec()
	ra, dec, radius := w.Field(stack.Width, stack.Height)
	if line, ok := goals.NarrowbandLine(stack.Filter); ok {
		xp, err := xpStars(ctx, r.db, r.XP, stack.Object, ra, dec, radius)
		if err == nil && r.applyZeroPoint(res, stack, w, goals.LineStars(xp, line.Lambda), m) {
			m.DepthSystem, m.DepthBand, m.DepthApprox = goals.SystemXPAB, line.Label, false
			return
		}
		if err != nil {
			slog.Warn("No Gaia XP photometry for the narrowband zero point; using Gaia G", "object", stack.Object, columnFilter, stack.Filter, "error", err)
		}
	}
	stars, err := catalogStars(ctx, r.db, r.stars, stack.Object, ra, dec, radius)
	if err != nil {
		slog.Warn("No Gaia stars for the zero point", "object", stack.Object, "error", err)
		return
	}
	if r.applyZeroPoint(res, stack, w, stars, m) {
		m.DepthSystem, m.DepthBand, m.DepthApprox = goals.SystemGaiaG, goals.BandGaiaG, goals.IsNarrowband(stack.Filter)
	}
}

func (r *Runner) applyZeroPoint(res goals.Result, stack app.Stack, w goals.WCS, stars []goals.CatalogStar, m *app.GoalMeasurement) bool {
	zp, n, ok := goals.ZeroPoint(res, stack.Height, w, stars)
	m.ZeroPointStars = n
	if !ok {
		return false
	}
	d, ok := goals.Depth(zp, res.NoiseNow, m.PixelScale)
	if !ok {
		return false
	}
	m.ZeroPoint, m.Depth = &zp, &d
	return true
}

func (r *Runner) saveSkySamples(ctx context.Context, stack app.Stack, m app.GoalMeasurement, rows []subRow, skyRates []float64) error {
	if m.ZeroPoint == nil || m.DepthSystem != goals.SystemGaiaG || rigsource.CanonicalFilter(stack.Filter) != "L" {
		return nil
	}
	var samples []app.SkySample
	for i, row := range rows {
		mag, ok := goals.SkyBrightness(*m.ZeroPoint, skyRates[i], m.PixelScale)
		if !ok || row.FrameID == 0 {
			continue
		}
		samples = append(samples, app.SkySample{FrameID: row.FrameID, Object: stack.Object, Filter: stack.Filter, Night: row.Night, DateObs: row.DateObs,
			SkyRate: skyRates[i], ZeroPoint: *m.ZeroPoint, PixelScale: m.PixelScale, SkyMag: mag, MeasuredAt: m.MeasuredAt})
	}
	if len(samples) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "frame_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"object", "filter", "night", "date_obs", "sky_rate", "zero_point", "pixel_scale", "sky_mag", "measured_at"}),
	}).Create(&samples).Error
}

func (r *Runner) save(ctx context.Context, m app.GoalMeasurement) error {
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: columnObject}, {Name: "filter"}},
		UpdateAll: true,
	}).Create(&m).Error
}
