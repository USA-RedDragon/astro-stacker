package server

import (
	"errors"
	"net/http"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/mosaicplan"
	"github.com/gin-gonic/gin"
)

func applyMosaicPlanRoutes(g *gin.RouterGroup, svc *mosaicplan.Service) {
	if svc == nil {
		return
	}
	g.GET("/mosaics/projects", func(c *gin.Context) {
		out, err := svc.List(c.Request.Context())
		if err != nil {
			mosaicError(c, err)
			return
		}
		c.JSON(http.StatusOK, out)
	})
	g.GET("/mosaics/projects/:key", func(c *gin.Context) {
		out, err := svc.Detail(c.Request.Context(), c.Param("key"))
		if err != nil {
			mosaicError(c, err)
			return
		}
		c.JSON(http.StatusOK, out)
	})
	g.GET("/mosaics/projects/:key/seasons", func(c *gin.Context) {
		out, err := svc.Seasons(c.Request.Context(), c.Param("key"), c.Query("strategy"), c.Query("pace"), time.Now())
		if err != nil {
			mosaicError(c, err)
			return
		}
		c.JSON(http.StatusOK, out)
	})
	g.GET("/mosaics/projects/:key/history", func(c *gin.Context) {
		out, err := svc.History(c.Request.Context(), c.Param("key"))
		if err != nil {
			mosaicError(c, err)
			return
		}
		c.JSON(http.StatusOK, out)
	})
	g.GET("/mosaics/adoption", func(c *gin.Context) {
		out, err := svc.Adoptions(c.Request.Context())
		if err != nil {
			mosaicError(c, err)
			return
		}
		c.JSON(http.StatusOK, out)
	})
	g.POST("/mosaics/adoption/run", func(c *gin.Context) {
		var body struct {
			DryRun bool `json:"dryRun"`
		}
		if c.Request.ContentLength > 0 {
			if err := c.ShouldBindJSON(&body); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{errorKey: err.Error()})
				return
			}
		}
		out, err := svc.RunAdoption(c.Request.Context(), body.DryRun)
		if err != nil {
			mosaicError(c, err)
			return
		}
		c.JSON(http.StatusOK, out)
	})
	g.POST("/mosaics/framing", func(c *gin.Context) {
		var req mosaicplan.FramingRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{errorKey: err.Error()})
			return
		}
		out, err := svc.Frame(c.Request.Context(), req, time.Now())
		if err != nil {
			mosaicError(c, err)
			return
		}
		c.JSON(http.StatusOK, out)
	})
}

func mosaicError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, mosaicplan.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{errorKey: err.Error()})
	case errors.Is(err, mosaicplan.ErrBadRequest), errors.Is(err, mosaicplan.ErrBadDecision):
		c.JSON(http.StatusBadRequest, gin.H{errorKey: err.Error()})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{errorKey: err.Error()})
	}
}
