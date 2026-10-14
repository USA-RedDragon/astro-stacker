package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/bodies"
	"github.com/USA-RedDragon/astro-stacker/internal/calpublish"
	"github.com/USA-RedDragon/astro-stacker/internal/collabswitch"
	"github.com/USA-RedDragon/astro-stacker/internal/conditions"
	"github.com/USA-RedDragon/astro-stacker/internal/darkneed"
	"github.com/USA-RedDragon/astro-stacker/internal/discover"
	"github.com/USA-RedDragon/astro-stacker/internal/goals"
	"github.com/USA-RedDragon/astro-stacker/internal/moonkeep"
	"github.com/USA-RedDragon/astro-stacker/internal/mosaicplan"
	"github.com/USA-RedDragon/astro-stacker/internal/previewer"
	"github.com/USA-RedDragon/astro-stacker/internal/schedauto"
	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"github.com/USA-RedDragon/astro-stacker/internal/skycutout"
	"github.com/USA-RedDragon/astro-stacker/internal/skyevents"
	"github.com/USA-RedDragon/astro-stacker/internal/starfront"
	"github.com/USA-RedDragon/astro-stacker/internal/sublength"
	"github.com/gin-gonic/gin"
)

type SchedulerStatusSource interface {
	Status() any
}

type unconfiguredStatus struct{}

func (unconfiguredStatus) Status() any { return gin.H{"reachable": "unconfigured"} }

type Extras struct {
	Commands     *schedcmd.Service
	Scheduler    SchedulerStatusSource
	Mosaics      *mosaicplan.Service
	Discover     *discover.Service
	Collabs      *starfront.Poller
	CollabSwitch *collabswitch.Service
	Previews     PreviewSource
	PreviewCache *PreviewCache
	Now          func() time.Time
	Conditions   *conditions.Service
	Cutouts      *skycutout.Service
	Backfill     func() goals.BackfillLive
	Grader       GraderSource
	DarkNeed     *darkneed.Publisher
	BiasNeed     *calpublish.BiasPublisher
	FlatRequests *calpublish.FlatPublisher
	SubLength    *sublength.Service
	MoonKeep     *moonkeep.Service
	Scheduling   *schedauto.Service
	Bodies       *bodies.Service
	BodyNights   *bodies.NightService
	Comets       *bodies.CometList
	Framing      *BodyFraming
	Asteroids    *bodies.AsteroidList
	SkyEvents    *skyevents.Service
	Site         SiteSource
	Discoveries  DiscoverySource
}

const schedulerWriteTimeout = 45 * time.Second

// asteroidRequestTimeout bounds an asteroid list request: its SBDB and
// close-approach fetches and its Horizons budget. asteroidWriteTimeout
// outlasts it, so the answer, an error at worst, is always written.
const (
	asteroidRequestTimeout = 100 * time.Second
	asteroidWriteTimeout   = 2 * time.Minute
)

func ExtendWriteDeadline(d time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		_ = http.NewResponseController(c.Writer).SetWriteDeadline(time.Now().Add(d))
		c.Next()
	}
}

func applySchedulerRoutes(g *gin.RouterGroup, x Extras, signer *previewer.Signer) {
	g.Use(ExtendWriteDeadline(schedulerWriteTimeout))
	now := x.Now
	if now == nil {
		now = time.Now
	}
	site := &siteMemo{src: x.Site}
	applyPreviewRoutes(g, x.Previews, x.PreviewCache, site, now)
	applySchedDataRoutes(g, signer, x.Grader, site, now)
	applyConditionsRoutes(g, x, now)
	applyAutofocusRoutes(g, now)
	status := x.Scheduler
	if status == nil {
		status = unconfiguredStatus{}
	}
	g.GET("/scheduler/status", func(c *gin.Context) {
		c.JSON(http.StatusOK, status.Status())
	})
	if x.Commands == nil {
		return
	}
	svc := x.Commands
	g.GET("/commands", func(c *gin.Context) {
		f, err := commandFilter(c)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{errorKey: err.Error()})
			return
		}
		out, err := svc.Log.List(c.Request.Context(), f)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{errorKey: err.Error()})
			return
		}
		if out == nil {
			out = []schedcmd.Record{}
		}
		c.JSON(http.StatusOK, out)
	})
	g.GET("/commands/:id", func(c *gin.Context) {
		r, err := svc.Log.Get(c.Request.Context(), c.Param("id"))
		if err != nil {
			commandError(c, err)
			return
		}
		c.JSON(http.StatusOK, r)
	})
	g.POST("/commands", func(c *gin.Context) {
		var body struct {
			Kind    schedcmd.Kind   `json:"kind"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{errorKey: err.Error()})
			return
		}
		r, err := svc.Submit(c.Request.Context(), body.Kind, body.Payload, author(c))
		if err != nil {
			commandError(c, err)
			return
		}
		c.JSON(http.StatusAccepted, r)
	})
	g.POST("/commands/:id/undo", func(c *gin.Context) {
		r, err := svc.Undo(c.Request.Context(), c.Param("id"), author(c))
		if err != nil {
			commandError(c, err)
			return
		}
		c.JSON(http.StatusAccepted, r)
	})
	g.POST("/commands/:id/cancel", func(c *gin.Context) {
		r, err := svc.Cancel(c.Request.Context(), c.Param("id"))
		if err != nil {
			if errors.Is(err, schedcmd.ErrNotWaiting) && r.ID != "" {
				c.JSON(http.StatusConflict, gin.H{errorKey: "It already reached the scheduler; undo it instead.", "record": r})
				return
			}
			commandError(c, err)
			return
		}
		c.JSON(http.StatusOK, r)
	})
}

func commandFilter(c *gin.Context) (schedcmd.Filter, error) {
	f := schedcmd.Filter{
		Author:      c.Query("author"),
		Category:    schedcmd.Category(c.Query("category")),
		Destination: schedcmd.Destination(c.Query("destination")),
		Query:       c.Query("q"),
		BeforeID:    c.Query("before_id"),
	}
	if s := c.Query("status"); s != "" {
		for _, p := range strings.Split(s, ",") {
			if p = strings.TrimSpace(p); p != "" {
				f.Status = append(f.Status, schedcmd.Status(p))
			}
		}
	}
	if l := c.Query("limit"); l != "" {
		n, err := strconv.Atoi(l)
		if err != nil || n < 1 || n > schedcmd.MaxListLimit {
			return f, fmt.Errorf("limit must be a whole number from 1 to %d", schedcmd.MaxListLimit)
		}
		f.Limit = n
	}
	for _, p := range []struct {
		name string
		to   *time.Time
	}{{"before", &f.Before}, {"since", &f.Since}} {
		if v := c.Query(p.name); v != "" {
			t, err := time.Parse(time.RFC3339Nano, v)
			if err != nil {
				return f, fmt.Errorf("%s must be RFC3339", p.name)
			}
			*p.to = t
		}
	}
	if f.BeforeID != "" && f.Before.IsZero() {
		return f, errors.New("before_id needs before")
	}
	return f, nil
}

func author(c *gin.Context) string {
	if u := c.GetHeader("X-Webauth-User"); u != "" {
		return u
	}
	return c.GetHeader("Tailscale-User-Login")
}

func commandError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, schedcmd.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{errorKey: err.Error()})
	case errors.Is(err, schedcmd.ErrInvalid), errors.Is(err, schedcmd.ErrUnknownKind):
		c.JSON(http.StatusBadRequest, gin.H{errorKey: err.Error()})
	case errors.Is(err, schedcmd.ErrNoInverse), errors.Is(err, schedcmd.ErrNotUndoable), errors.Is(err, schedcmd.ErrNotWaiting):
		c.JSON(http.StatusConflict, gin.H{errorKey: err.Error()})
	case errors.Is(err, schedcmd.ErrUnreachable):
		c.JSON(http.StatusServiceUnavailable, gin.H{errorKey: "The observatory is unreachable, so it could not be cancelled there."})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{errorKey: err.Error()})
	}
}
