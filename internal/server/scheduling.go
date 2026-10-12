package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/USA-RedDragon/astro-stacker/internal/schedauto"
	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"github.com/USA-RedDragon/astro-stacker/internal/schedglobal"
	"github.com/gin-gonic/gin"
)

const schedulingUnavailable = "global scheduling settings are not available: the stacker has no scheduler database"

func applySchedulingRoutes(g *gin.RouterGroup, s *schedauto.Service, cmds *schedcmd.Service, previews PreviewSource) {
	g.GET("/scheduling", func(c *gin.Context) {
		if s == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{errorKey: schedulingUnavailable})
			return
		}
		res, err := s.Current(c.Request.Context())
		if err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, schedauto.ErrNoSite) {
				status = http.StatusServiceUnavailable
			}
			c.JSON(status, gin.H{errorKey: err.Error()})
			return
		}
		c.JSON(http.StatusOK, res)
	})
	g.GET("/scheduling/originals", func(c *gin.Context) {
		if s == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{errorKey: schedulingUnavailable})
			return
		}
		out, err := s.Originals(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{errorKey: err.Error()})
			return
		}
		c.JSON(http.StatusOK, out)
	})
	g.GET("/scheduling/projection", func(c *gin.Context) {
		if s == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{errorKey: schedulingUnavailable})
			return
		}
		if previews == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{errorKey: "no observatory scheduler to simulate the night on"})
			return
		}
		v, err := s.Projection(c.Request.Context(), previews)
		if err != nil {
			observatoryError(c, err)
			return
		}
		c.JSON(http.StatusOK, v)
	})
	g.POST("/scheduling/global", func(c *gin.Context) {
		if s == nil || cmds == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{errorKey: schedulingUnavailable})
			return
		}
		var body struct {
			Settings *schedglobal.Settings `json:"settings"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{errorKey: err.Error()})
			return
		}
		ctx := c.Request.Context()
		saved, err := schedglobal.Load(ctx, s.App)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{errorKey: err.Error()})
			return
		}
		raw, err := json.Marshal(schedcmd.GlobalSchedulingPayload{Before: saved.Settings, After: body.Settings})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{errorKey: err.Error()})
			return
		}
		r, err := cmds.Submit(ctx, schedcmd.KindSchedulingGlobal, raw, author(c))
		if err != nil {
			commandError(c, err)
			return
		}
		if _, err := s.Refresh(ctx); err != nil {
			c.JSON(http.StatusOK, gin.H{"command": r, "refresh_error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"command": r})
	})
}
