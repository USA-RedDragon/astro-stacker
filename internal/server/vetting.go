package server

import (
	"net/http"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting"
	"github.com/gin-gonic/gin"
)

func applyVettingRoutes(g *gin.RouterGroup) {
	g.GET("/discoveries/sources/:id/checks", sourceChecks)
	g.POST("/discoveries/sources/:id/checks", requestSourceChecks)
}

func sourceChecks(c *gin.Context) {
	db, ok := appDB(c)
	if !ok {
		return
	}
	id, ok := sourceID(c)
	if !ok {
		return
	}
	st, err := vetting.Load(c.Request.Context(), db, id)
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, st)
}

func requestSourceChecks(c *gin.Context) {
	db, ok := appDB(c)
	if !ok {
		return
	}
	id, ok := sourceID(c)
	if !ok {
		return
	}
	st, err := vetting.Request(c.Request.Context(), db, id, time.Now().UTC())
	if err != nil {
		sourceError(c, err)
		return
	}
	c.JSON(http.StatusOK, st)
}
