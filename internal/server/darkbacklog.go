package server

import (
	"net/http"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/coverage"
	"github.com/USA-RedDragon/astro-stacker/internal/darkneed"
	"github.com/gin-gonic/gin"
)

type darkBacklogResponse struct {
	coverage.DarkBacklog
	Publish darkneed.Status `json:"publish"`
}

func applyDarkBacklogRoutes(g *gin.RouterGroup, p *darkneed.Publisher, now func() time.Time) {
	if now == nil {
		now = time.Now
	}
	g.GET("/coverage/dark-backlog", func(c *gin.Context) {
		di, ok := depInjection(c)
		if !ok {
			return
		}
		b, err := coverage.BuildDarkBacklog(c.Request.Context(), di.AppStore.DB(), now())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{errorKey: err.Error()})
			return
		}
		c.JSON(http.StatusOK, darkBacklogResponse{DarkBacklog: b, Publish: p.Status()})
	})
}
