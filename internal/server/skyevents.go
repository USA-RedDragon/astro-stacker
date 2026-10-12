package server

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/USA-RedDragon/astro-stacker/internal/goals"
	"github.com/USA-RedDragon/astro-stacker/internal/skyevents"
	"github.com/gin-gonic/gin"
)

const (
	skyEventsOff = "the sky event finder is off"
	keyCommand   = "command"
	keyEvent     = "event"
)

type skyEventChoice struct {
	Priority string `json:"priority"`
}

func skyEventError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, skyevents.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{errorKey: err.Error()})
	case errors.Is(err, skyevents.ErrNotReady):
		c.JSON(http.StatusServiceUnavailable, gin.H{errorKey: err.Error()})
	case errors.Is(err, skyevents.ErrCannotPlan), errors.Is(err, skyevents.ErrAlreadyPlanned), errors.Is(err, skyevents.ErrNotPlanned), errors.Is(err, skyevents.ErrOver):
		c.JSON(http.StatusConflict, gin.H{errorKey: err.Error()})
	default:
		commandError(c, err)
	}
}

func applySkyEventRoutes(g *gin.RouterGroup, s *skyevents.Service, backfill func() goals.BackfillLive) {
	g.GET("/sky-events", func(c *gin.Context) {
		if s == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{errorKey: skyEventsOff})
			return
		}
		months := skyevents.DefaultMonths
		if v := c.Query("months"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > skyevents.MonthsAhead {
				c.JSON(http.StatusBadRequest, gin.H{errorKey: "months must be a whole number from 1 to " + strconv.Itoa(skyevents.MonthsAhead)})
				return
			}
			months = n
		}
		v, err := s.View(c.Request.Context(), months)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{errorKey: err.Error()})
			return
		}
		c.JSON(http.StatusOK, v)
	})
	choice := func(c *gin.Context) (string, bool) {
		if s == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{errorKey: skyEventsOff})
			return "", false
		}
		var body skyEventChoice
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{errorKey: err.Error()})
			return "", false
		}
		if body.Priority == "" {
			body.Priority = skyevents.PriorityNormal
		}
		return body.Priority, true
	}
	g.POST("/sky-events/:id/plan", func(c *gin.Context) {
		priority, ok := choice(c)
		if !ok {
			return
		}
		if p := s.Planning(c.Request.Context()); !p.Available {
			c.JSON(http.StatusConflict, gin.H{errorKey: *p.Reason})
			return
		}
		snap, ok := planningSnapshot(c, backfill)
		if !ok {
			return
		}
		e, rec, err := s.Plan(c.Request.Context(), snap, c.Param("id"), priority, author(c))
		if err != nil {
			skyEventError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{keyEvent: e, keyCommand: rec})
	})
	g.POST("/sky-events/:id/priority", func(c *gin.Context) {
		priority, ok := choice(c)
		if !ok {
			return
		}
		e, rec, err := s.SetPriority(c.Request.Context(), c.Param("id"), priority, author(c))
		if err != nil {
			skyEventError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{keyEvent: e, keyCommand: rec})
	})
	g.POST("/sky-events/:id/unplan", func(c *gin.Context) {
		if s == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{errorKey: skyEventsOff})
			return
		}
		e, rec, err := s.Unplan(c.Request.Context(), c.Param("id"), author(c))
		if err != nil {
			skyEventError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{keyEvent: e, keyCommand: rec})
	})
}
