package server

import (
	"errors"
	"net/http"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/collabswitch"
	"github.com/gin-gonic/gin"
)

const collabSwitchWriteTimeout = 8 * time.Minute

func applyCollabSwitchRoutes(g *gin.RouterGroup, sw *collabswitch.Service) {
	g.Use(ExtendWriteDeadline(collabSwitchWriteTimeout))
	if sw == nil {
		off := func(c *gin.Context) {
			c.JSON(http.StatusOK, collabswitch.Status{Running: []string{}})
		}
		g.GET("/collab-switch", off)
		g.POST("/collab-switch", func(c *gin.Context) {
			c.JSON(http.StatusServiceUnavailable, gin.H{errorKey: "the collab switch is not running"})
		})
		return
	}
	g.GET("/collab-switch", func(c *gin.Context) {
		st, err := sw.Status(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{errorKey: err.Error()})
			return
		}
		c.JSON(http.StatusOK, st)
	})
	g.POST("/collab-switch", func(c *gin.Context) {
		var body struct {
			On *bool `json:"on"`
		}
		if err := c.ShouldBindJSON(&body); err != nil || body.On == nil {
			c.JSON(http.StatusBadRequest, gin.H{errorKey: "on (true or false) is required"})
			return
		}
		st, rec, err := sw.Set(c.Request.Context(), *body.On, author(c))
		if err != nil {
			commandError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": st, "record": rec})
	})
	g.POST("/collab-switch/read", func(c *gin.Context) {
		st, err := sw.Read(c.Request.Context())
		if err != nil && !errors.Is(err, collabswitch.ErrNINA) {
			c.JSON(http.StatusInternalServerError, gin.H{errorKey: err.Error()})
			return
		}
		c.JSON(http.StatusOK, st)
	})
}
