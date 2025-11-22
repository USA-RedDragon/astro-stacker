package server

import (
	"net/http"

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
}
