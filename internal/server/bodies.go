package server

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/USA-RedDragon/astro-stacker/internal/bodies"
	"github.com/USA-RedDragon/astro-stacker/internal/horizons"
	"github.com/USA-RedDragon/astro-stacker/internal/skyevents"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const bodiesNotBuilt = "the body position tables have not been built yet"

func applyBodiesRoutes(g *gin.RouterGroup, s *bodies.Service) {
	g.GET("/bodies", func(c *gin.Context) {
		res := latestBodies(c, s)
		if res == nil {
			return
		}
		c.JSON(http.StatusOK, res)
	})
	g.GET("/bodies/positions", func(c *gin.Context) {
		res := latestBodies(c, s)
		if res == nil {
			return
		}
		c.JSON(http.StatusOK, gin.H{"computed_at": res.ComputedAt, "rows": res.Positions(c.Query("target_guid"), c.Query("night"))})
	})
}

func latestBodies(c *gin.Context, s *bodies.Service) *bodies.Result {
	if s == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{errorKey: "body positions are not built: the stacker has no scheduler database"})
		return nil
	}
	res := s.Latest()
	if res == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{errorKey: bodiesNotBuilt})
	}
	return res
}

func applyCometRoutes(g *gin.RouterGroup, list *bodies.CometList) {
	g.GET("/comets", func(c *gin.Context) {
		if list == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{errorKey: "the comet list is off"})
			return
		}
		maxMag := bodies.DefaultMaxMag
		var minAlt *float64
		if v := c.Query("max_mag"); v != "" {
			f, err := strconv.ParseFloat(v, 64)
			if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
				c.JSON(http.StatusBadRequest, gin.H{errorKey: "max_mag must be a number"})
				return
			}
			maxMag = f
		}
		if v := c.Query("min_alt"); v != "" {
			f, err := strconv.ParseFloat(v, 64)
			if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < -90 || f > 90 {
				c.JSON(http.StatusBadRequest, gin.H{errorKey: "min_alt must be a number from -90 to 90"})
				return
			}
			minAlt = &f
		}
		all := c.Query("all") == "1" || c.Query("all") == "true"
		view, err := list.List(c.Request.Context(), maxMag, minAlt, all)
		switch {
		case errors.Is(err, bodies.ErrBadQuery):
			c.JSON(http.StatusBadRequest, gin.H{errorKey: err.Error()})
		case errors.Is(err, bodies.ErrNoSite), errors.Is(err, bodies.ErrNoElements):
			c.JSON(http.StatusServiceUnavailable, gin.H{errorKey: err.Error()})
		case err != nil:
			c.JSON(http.StatusInternalServerError, gin.H{errorKey: err.Error()})
		default:
			c.JSON(http.StatusOK, view)
		}
	})
}

type BodyFraming struct {
	Tracker *bodies.Tracker
	Sched   *gorm.DB
	Frame   func(context.Context) (*skyevents.FrameInfo, error)
}

func applyBodyFramingRoutes(g *gin.RouterGroup, f *BodyFraming) {
	off := func(c *gin.Context) bool {
		if f == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{errorKey: "body framing is off: the stacker has no scheduler database"})
			return true
		}
		return false
	}
	g.GET("/bodies/track", ExtendWriteDeadline(schedulerWriteTimeout), func(c *gin.Context) {
		if off(c) {
			return
		}
		view, err := f.Tracker.Track(c.Request.Context(), c.Query("designation"), c.Query("kind"))
		switch {
		case errors.Is(err, bodies.ErrNoDesignation):
			c.JSON(http.StatusBadRequest, gin.H{errorKey: err.Error()})
			return
		case errors.Is(err, bodies.ErrNoSite):
			c.JSON(http.StatusServiceUnavailable, gin.H{errorKey: err.Error()})
			return
		case errors.Is(err, horizons.ErrUnknownBody), errors.Is(err, horizons.ErrAmbiguous):
			c.JSON(http.StatusNotFound, gin.H{errorKey: err.Error()})
			return
		case err != nil:
			c.JSON(http.StatusBadGateway, gin.H{errorKey: err.Error()})
			return
		}
		out := gin.H{"track": view, "frame": nil, "frame_reason": nil}
		if f.Frame == nil {
			out["frame_reason"] = "The camera field is not measured yet."
		} else if fi, err := f.Frame(c.Request.Context()); err != nil {
			out["frame_reason"] = "The camera field is not measured yet: " + err.Error()
		} else {
			out["frame"] = fi
		}
		c.JSON(http.StatusOK, out)
	})
	g.GET("/bodies/target", func(c *gin.Context) {
		if off(c) {
			return
		}
		guid := strings.TrimSpace(c.Query("target_guid"))
		if guid == "" {
			c.JSON(http.StatusBadRequest, gin.H{errorKey: "target_guid is required"})
			return
		}
		if f.Sched == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{errorKey: "the stacker has no scheduler database"})
			return
		}
		cols, err := bodies.HasFraming(c.Request.Context(), f.Sched)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{errorKey: err.Error()})
			return
		}
		t, ok, err := bodies.TargetByGUID(c.Request.Context(), f.Sched, guid)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{errorKey: err.Error()})
			return
		}
		out := gin.H{"target": nil, "framing_columns": cols}
		if ok {
			out["target"] = t
		}
		c.JSON(http.StatusOK, out)
	})
}
