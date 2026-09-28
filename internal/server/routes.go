package server

import (
	"net/http"

	"github.com/USA-RedDragon/pixinsight-worker/internal/coverage"
	"github.com/USA-RedDragon/pixinsight-worker/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

func applyRoutes(r *gin.Engine) {
	r.GET("/ping", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"state": "OK"})
	})

	v1(r.Group("/api/v1"))
}

func v1(r *gin.RouterGroup) {
	r.GET("/version", func(c *gin.Context) {
		di := c.MustGet(middleware.DepInjectionKey).(*middleware.DepInjection)
		c.String(http.StatusOK, "%s", di.Version)
	})

	// Calibration coverage per night of lights; ?object= narrows to one target.
	r.GET("/coverage", func(c *gin.Context) {
		di := c.MustGet(middleware.DepInjectionKey).(*middleware.DepInjection)
		rows, err := coverage.Report(c.Request.Context(), di.AppStore.DB(), c.Query("object"))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, rows)
	})

	// Dark library capture list: ladder setpoints lights need but no darks cover.
	r.GET("/coverage/dark-gaps", func(c *gin.Context) {
		di := c.MustGet(middleware.DepInjectionKey).(*middleware.DepInjection)
		gaps, err := coverage.Gaps(c.Request.Context(), di.AppStore.DB())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gaps)
	})
}
