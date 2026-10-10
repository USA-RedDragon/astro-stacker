package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/conditions"
	"github.com/USA-RedDragon/astro-stacker/internal/discover"
	"github.com/USA-RedDragon/astro-stacker/internal/goals"
	"github.com/USA-RedDragon/astro-stacker/internal/mosaicplan"
	"github.com/USA-RedDragon/astro-stacker/internal/previewer"
	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"github.com/USA-RedDragon/astro-stacker/internal/skycutout"
	"github.com/USA-RedDragon/astro-stacker/internal/starfront"
	"github.com/gin-gonic/gin"
)

type SchedulerStatusSource interface {
	Status() any
}

type unconfiguredStatus struct{}

func (unconfiguredStatus) Status() any { return gin.H{"reachable": "unconfigured"} }

type Extras struct {
	Commands   *schedcmd.Service
	Scheduler  SchedulerStatusSource
	Mosaics    *mosaicplan.Service
	Discover   *discover.Service
	Collabs    *starfront.Poller
	Previews   PreviewSource
	Now        func() time.Time
	Conditions *conditions.Service
	Cutouts    *skycutout.Service
	Backfill   func() goals.BackfillLive
	Grader     GraderSource
}

const schedulerWriteTimeout = 45 * time.Second

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
	applyPreviewRoutes(g, x.Previews, now)
	applySchedDataRoutes(g, signer, x.Grader, now)
	applyConditionsRoutes(g, x, now)
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
		f := schedcmd.Filter{
			Author:   c.Query("author"),
			Category: schedcmd.Category(c.Query("category")),
			Query:    c.Query("q"),
		}
		if s := c.Query("status"); s != "" {
			for _, p := range strings.Split(s, ",") {
				if p = strings.TrimSpace(p); p != "" {
					f.Status = append(f.Status, schedcmd.Status(p))
				}
			}
		}
		if n, err := strconv.Atoi(c.Query("limit")); err == nil {
			f.Limit = n
		}
		if b := c.Query("before"); b != "" {
			if t, err := time.Parse(time.RFC3339, b); err == nil {
				f.Before = t
			}
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
