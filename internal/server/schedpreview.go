package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/observatory"
	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"github.com/gin-gonic/gin"
)

const (
	previewCacheTTL = time.Minute
	nightTonight    = "tonight"
	nightTomorrow   = "tomorrow"
	siteTZ          = "America/Chicago"
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

func siteLocation() *time.Location {
	if loc, err := time.LoadLocation(siteTZ); err == nil {
		return loc
	}
	return time.FixedZone("CST", -6*3600)
}

func nightStart(now time.Time) time.Time {
	local := now.In(siteLocation())
	day := time.Date(local.Year(), local.Month(), local.Day(), 12, 0, 0, 0, local.Location())
	if local.Hour() < 12 {
		day = day.AddDate(0, 0, -1)
	}
	return day
}

func previewStart(night string, now time.Time) *time.Time {
	if night != nightTomorrow {
		return nil
	}
	s := nightStart(now).AddDate(0, 0, 1).UTC()
	return &s
}

func observatoryError(c *gin.Context, err error) {
	if errors.Is(err, schedcmd.ErrUnreachable) {
		c.JSON(http.StatusServiceUnavailable, gin.H{errorKey: "The scheduler is unreachable: " + err.Error()})
		return
	}
	c.JSON(http.StatusBadGateway, gin.H{errorKey: err.Error()})
}

func applyPreviewRoutes(g *gin.RouterGroup, src PreviewSource, now func() time.Time) {
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
		data, err := src.Preview(c.Request.Context(), previewStart(night, t))
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
			req.Start = previewStart(body.Night, now())
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
