package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discover"
	"github.com/USA-RedDragon/astro-stacker/internal/observatory"
	"github.com/USA-RedDragon/astro-stacker/internal/planning"
	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"github.com/USA-RedDragon/astro-stacker/internal/schedwatch"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	previewCacheTTL   = time.Minute
	previewStableTTL  = 5 * time.Minute
	previewAheadTTL   = 30 * time.Minute
	previewWarmEvery  = 15 * time.Second
	previewWarmLead   = 20 * time.Second
	previewRetryAfter = time.Minute
	previewWatchFor   = time.Hour
	nightTonight      = "tonight"
	nightTomorrow     = "tomorrow"
)

type PreviewSource interface {
	Preview(ctx context.Context, start *time.Time) (json.RawMessage, error)
	PreviewWith(ctx context.Context, req observatory.PreviewRequest) (json.RawMessage, error)
}

// PreviewStamp summarizes the scheduler inputs a preview depends on. It
// reports false when the inputs cannot be observed.
type PreviewStamp func(ctx context.Context) (string, bool)

// PreviewCache keeps scheduler previews ready. An entry stays valid while the
// input stamp is unchanged: for previewAheadTTL while its start is ahead,
// otherwise for previewStableTTL.
// A changed or unknown stamp limits an entry to previewCacheTTL. Run refreshes
// due entries in the background so requests do not wait on the observatory.
type PreviewCache struct {
	Source PreviewSource
	Stamp  PreviewStamp

	now     func() time.Time
	wake    chan struct{}
	mu      sync.Mutex
	gen     uint64
	entries map[string]*previewEntry
}

type previewEntry struct {
	start    *time.Time
	data     json.RawMessage
	at       time.Time
	planFrom time.Time
	stamp    string
	stamped  bool
	gen      uint64
	asked    time.Time
	failedAt time.Time
	run      *previewRun
	seq      uint64
	savedSeq uint64
}

type previewRun struct {
	done    chan struct{}
	data    json.RawMessage
	err     error
	gen     uint64
	stamp   string
	stamped bool
}

func NewPreviewCache(src PreviewSource, now func() time.Time) *PreviewCache {
	if now == nil {
		now = time.Now
	}
	return &PreviewCache{Source: src, now: now, wake: make(chan struct{}, 1), entries: map[string]*previewEntry{}}
}

func previewKey(start *time.Time) string {
	if start == nil {
		return nightTonight
	}
	return start.UTC().Format(time.RFC3339)
}

func (e *previewEntry) fresh(gen uint64, stamp string, stamped bool, at time.Time) bool {
	if e.data == nil || e.gen != gen {
		return false
	}
	age := at.Sub(e.at)
	if !stamped || !e.stamped || stamp != e.stamp {
		return age < previewCacheTTL
	}
	if e.planFrom.After(at) {
		return age < previewAheadTTL
	}
	return age < previewStableTTL
}

func (p *PreviewCache) stamp(ctx context.Context) (string, bool) {
	if p.Stamp == nil {
		return "", false
	}
	return p.Stamp(ctx)
}

func (p *PreviewCache) entry(start *time.Time) *previewEntry {
	key := previewKey(start)
	e, ok := p.entries[key]
	if !ok {
		e = &previewEntry{start: start}
		p.entries[key] = e
	}
	return e
}

// Invalidate drops every cached preview, for example after a command changed
// the scheduler, and asks Run to refresh them at once.
func (p *PreviewCache) Invalidate() {
	p.mu.Lock()
	p.gen++
	p.mu.Unlock()
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// Notice invalidates the cache when a command was applied on the observatory.
func (p *PreviewCache) Notice(r schedcmd.Record) {
	if r.Status == schedcmd.StatusApplied {
		p.Invalidate()
	}
}

// Get returns the preview that starts at start, or tonight's when start is
// nil. force skips the cache.
func (p *PreviewCache) Get(ctx context.Context, start *time.Time, force bool) (json.RawMessage, error) {
	stamp, stamped := p.stamp(ctx)
	now := p.now()
	p.mu.Lock()
	e := p.entry(start)
	e.asked = now
	if !force && e.fresh(p.gen, stamp, stamped, now) {
		data := e.data
		p.mu.Unlock()
		return data, nil
	}
	run := p.launch(ctx, e, stamp, stamped, now)
	p.mu.Unlock()
	select {
	case <-run.done:
		return run.data, run.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (r *previewRun) serves(gen uint64, stamp string, stamped bool) bool {
	return r != nil && r.gen == gen && r.stamped == stamped && r.stamp == stamp
}

func (p *PreviewCache) launch(ctx context.Context, e *previewEntry, stamp string, stamped bool, now time.Time) *previewRun {
	if e.run.serves(p.gen, stamp, stamped) {
		return e.run
	}
	gen := p.gen
	run := &previewRun{done: make(chan struct{}), gen: gen, stamp: stamp, stamped: stamped}
	e.run = run
	e.seq++
	seq := e.seq
	go func() {
		data, err := p.Source.Preview(context.WithoutCancel(ctx), e.start)
		p.mu.Lock()
		if e.run == run {
			e.run = nil
		}
		switch {
		case seq < e.savedSeq:
		case err != nil:
			e.failedAt = p.now()
		default:
			e.data, e.at, e.stamp, e.stamped, e.gen = data, now, stamp, stamped, gen
			e.planFrom = previewPlanStart(data)
			e.failedAt = time.Time{}
			e.savedSeq = seq
		}
		p.mu.Unlock()
		run.data, run.err = data, err
		close(run.done)
	}()
	return run
}

func previewPlanStart(data json.RawMessage) time.Time {
	var v struct {
		Start time.Time `json:"start"`
	}
	_ = json.Unmarshal(data, &v)
	return v.Start
}

// Run keeps tonight's preview, and any other preview asked for in the last
// previewWatchFor, refreshed ahead of requests until ctx ends.
func (p *PreviewCache) Run(ctx context.Context) {
	t := time.NewTicker(previewWarmEvery)
	defer t.Stop()
	for {
		p.Warm(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-p.wake:
		}
	}
}

// Warm starts a refresh of every watched preview that would expire before the
// next tick and waits for them.
func (p *PreviewCache) Warm(ctx context.Context) {
	stamp, stamped := p.stamp(ctx)
	now := p.now()
	lead := now.Add(previewWarmLead)
	p.mu.Lock()
	p.entry(nil)
	var runs []*previewRun
	for key, e := range p.entries {
		if key != nightTonight && now.Sub(e.asked) > previewWatchFor {
			delete(p.entries, key)
			continue
		}
		if e.run != nil || e.fresh(p.gen, stamp, stamped, lead) {
			continue
		}
		if !e.failedAt.IsZero() && now.Sub(e.failedAt) < previewRetryAfter {
			continue
		}
		runs = append(runs, p.launch(ctx, e, stamp, stamped, now))
	}
	p.mu.Unlock()
	for _, r := range runs {
		select {
		case <-r.done:
		case <-ctx.Done():
			return
		}
	}
}

type SiteSource func(ctx context.Context) (sky.Site, error)

type siteMemo struct {
	mu   sync.Mutex
	src  SiteSource
	site *sky.Site
}

func (m *siteMemo) get(ctx context.Context) (sky.Site, error) {
	if m == nil || m.src == nil {
		return sky.Site{}, discover.ErrNoSite
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.site != nil {
		return *m.site, nil
	}
	s, err := m.src(ctx)
	if err != nil {
		return sky.Site{}, err
	}
	m.site = &s
	return s, nil
}

type nightSince struct {
	At    time.Time
	Basis string
}

func tonightSince(ctx context.Context, site *siteMemo, now time.Time) nightSince {
	s, err := site.get(ctx)
	if err != nil {
		return nightSince{At: now.Add(-24 * time.Hour), Basis: "the last 24 hours, because the previous sunset is unknown: " + err.Error()}
	}
	at, ok := s.PreviousSunset(now)
	if !ok {
		return nightSince{At: now.Add(-24 * time.Hour), Basis: "the last 24 hours, because the sun did not set at the observatory site in the last 48 hours"}
	}
	return nightSince{At: at, Basis: "the previous sunset at the observatory site"}
}

var errNoTomorrowSunset = errors.New("the sun does not set at the observatory site tomorrow")

func previewSunrise(data json.RawMessage) (time.Time, bool) {
	var v struct {
		Twilight struct {
			Sunrise *time.Time `json:"sunrise"`
		} `json:"twilight"`
	}
	if json.Unmarshal(data, &v) != nil || v.Twilight.Sunrise == nil || v.Twilight.Sunrise.IsZero() {
		return time.Time{}, false
	}
	return *v.Twilight.Sunrise, true
}

func tomorrowSunset(ctx context.Context, site *siteMemo, now time.Time, tonightPlan func(context.Context) (json.RawMessage, error)) (time.Time, error) {
	s, err := site.get(ctx)
	if err != nil {
		return time.Time{}, fmt.Errorf("tomorrow's plan starts at tomorrow's sunset, which needs the observatory site: %w", err)
	}
	if tonightPlan != nil {
		if data, err := tonightPlan(ctx); err == nil {
			if sunrise, ok := previewSunrise(data); ok {
				if next, ok := s.NextSunset(sunrise); ok {
					return next.UTC(), nil
				}
			}
		}
	}
	tonight, ok := s.TonightSunset(now)
	if !ok {
		return time.Time{}, errNoTomorrowSunset
	}
	next, ok := s.NextSunset(tonight.Add(time.Hour))
	if !ok {
		return time.Time{}, errNoTomorrowSunset
	}
	return next.UTC(), nil
}

func previewStart(c *gin.Context, site *siteMemo, cache *PreviewCache, night string, now time.Time) (*time.Time, bool) {
	if night != nightTomorrow {
		return nil, true
	}
	var tonightPlan func(context.Context) (json.RawMessage, error)
	if cache != nil {
		tonightPlan = func(ctx context.Context) (json.RawMessage, error) { return cache.Get(ctx, nil, false) }
	}
	start, err := tomorrowSunset(c.Request.Context(), site, now, tonightPlan)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{errorKey: err.Error()})
		return nil, false
	}
	return &start, true
}

func observatoryError(c *gin.Context, err error) {
	if errors.Is(err, schedcmd.ErrUnreachable) {
		c.JSON(http.StatusServiceUnavailable, gin.H{errorKey: "The scheduler is unreachable: " + err.Error()})
		return
	}
	c.JSON(http.StatusBadGateway, gin.H{errorKey: err.Error()})
}

func applyPreviewRoutes(g *gin.RouterGroup, src PreviewSource, cache *PreviewCache, site *siteMemo, now func() time.Time) {
	if cache == nil && src != nil {
		cache = NewPreviewCache(src, now)
	}
	unavailable := func(c *gin.Context) bool {
		if src == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{errorKey: "The scheduler is not configured."})
			return true
		}
		return false
	}
	g.GET("/scheduler/preview", func(c *gin.Context) {
		if unavailable(c) {
			return
		}
		night := c.DefaultQuery("night", nightTonight)
		if night != nightTonight && night != nightTomorrow {
			c.JSON(http.StatusBadRequest, gin.H{errorKey: "night must be tonight or tomorrow"})
			return
		}
		start, ok := previewStart(c, site, cache, night, now())
		if !ok {
			return
		}
		data, err := cache.Get(c.Request.Context(), start, c.Query("fresh") != "")
		if err != nil {
			observatoryError(c, err)
			return
		}
		c.Data(http.StatusOK, gin.MIMEJSON, data)
	})
	g.POST("/scheduler/preview", func(c *gin.Context) {
		if unavailable(c) {
			return
		}
		var body struct {
			Night     string                 `json:"night"`
			Start     *time.Time             `json:"start"`
			Overrides []observatory.Override `json:"overrides"`
			Weights   map[string]float64     `json:"weights"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{errorKey: err.Error()})
			return
		}
		weights, ok := weightOverrides(c, body.Weights)
		if !ok {
			return
		}
		req := observatory.PreviewRequest{Start: body.Start, Overrides: append(body.Overrides, weights...)}
		if req.Start == nil {
			start, ok := previewStart(c, site, cache, body.Night, now())
			if !ok {
				return
			}
			req.Start = start
		}
		if req.Overrides == nil {
			req.Overrides = []observatory.Override{}
		}
		data, err := src.PreviewWith(c.Request.Context(), req)
		if err != nil {
			observatoryError(c, err)
			return
		}
		c.Data(http.StatusOK, gin.MIMEJSON, data)
	})
}

// SyncStamp stamps preview inputs with the newest SymmetricDS batches the
// scheduler database exchanged with the observatory, plus live state from
// the plugin that is not in the database.
func SyncStamp(db *gorm.DB, live func() string) PreviewStamp {
	return func(ctx context.Context) (string, bool) {
		if db == nil {
			return "", false
		}
		var row struct {
			Incoming int64
			Outgoing int64
		}
		if err := db.WithContext(ctx).Raw(schedwatch.SyncStampQuery).Scan(&row).Error; err != nil {
			return "", false
		}
		s := strconv.FormatInt(row.Incoming, 10) + "/" + strconv.FormatInt(row.Outgoing, 10)
		if live != nil {
			s += "/" + live()
		}
		return s, true
	}
}

// weightOverrides turns what-if global rule weights into preview overrides.
// A plugin that predates rule weight overrides skips entities it does not know.
func weightOverrides(c *gin.Context, weights map[string]float64) ([]observatory.Override, bool) {
	if len(weights) == 0 {
		return nil, true
	}
	di, ok := depInjection(c)
	if !ok {
		return nil, false
	}
	if di.SchedulerDBStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{errorKey: "no scheduler database"})
		return nil, false
	}
	mosaic, err := planning.MosaicFlags(c.Request.Context(), di.SchedulerDBStore.DB())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{errorKey: "load projects: " + err.Error()})
		return nil, false
	}
	list, err := planning.WhatIfWeights(weights, mosaic)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{errorKey: err.Error()})
		return nil, false
	}
	out := make([]observatory.Override, 0, len(list))
	for _, w := range list {
		v, err := json.Marshal(w.Weight)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{errorKey: err.Error()})
			return nil, false
		}
		out = append(out, observatory.Override{Entity: observatory.OverrideRuleWeight, ID: int64(w.ProjectID), Field: w.Rule, Value: v})
	}
	return out, true
}
