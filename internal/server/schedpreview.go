package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discover"
	"github.com/USA-RedDragon/astro-stacker/internal/observatory"
	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
	"github.com/gin-gonic/gin"
)

const (
	previewCacheTTL = time.Minute
	nightTonight    = "tonight"
	nightTomorrow   = "tomorrow"
)

type PreviewSource interface {
	Preview(ctx context.Context, start *time.Time) (json.RawMessage, error)
	PreviewWith(ctx context.Context, req observatory.PreviewRequest) (json.RawMessage, error)
}

type previewCache struct {
	mu      sync.Mutex
	entries map[string]cachedPreview
}

type cachedPreview struct {
	at   time.Time
	data json.RawMessage
}

func (p *previewCache) get(key string, now time.Time) (json.RawMessage, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.entries[key]
	if !ok || now.Sub(e.at) >= previewCacheTTL {
		return nil, false
	}
	return e.data, true
}

func (p *previewCache) put(key string, data json.RawMessage, now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.entries == nil {
		p.entries = map[string]cachedPreview{}
	}
	p.entries[key] = cachedPreview{at: now, data: data}
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

func tomorrowSunset(ctx context.Context, site *siteMemo, now time.Time) (time.Time, error) {
	s, err := site.get(ctx)
	if err != nil {
		return time.Time{}, fmt.Errorf("tomorrow's plan starts at tomorrow's sunset, which needs the observatory site: %w", err)
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

func previewStart(c *gin.Context, site *siteMemo, night string, now time.Time) (*time.Time, bool) {
	if night != nightTomorrow {
		return nil, true
	}
	start, err := tomorrowSunset(c.Request.Context(), site, now)
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

func applyPreviewRoutes(g *gin.RouterGroup, src PreviewSource, site *siteMemo, now func() time.Time) {
	cache := &previewCache{}
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
		t := now()
		if c.Query("fresh") == "" {
			if data, ok := cache.get(night, t); ok {
				c.Data(http.StatusOK, gin.MIMEJSON, data)
				return
			}
		}
		start, ok := previewStart(c, site, night, t)
		if !ok {
			return
		}
		data, err := src.Preview(c.Request.Context(), start)
		if err != nil {
			observatoryError(c, err)
			return
		}
		cache.put(night, data, t)
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
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{errorKey: err.Error()})
			return
		}
		req := observatory.PreviewRequest{Start: body.Start, Overrides: body.Overrides}
		if req.Start == nil {
			start, ok := previewStart(c, site, body.Night, now())
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
