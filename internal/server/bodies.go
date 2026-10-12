package server

import (
	"net/http"

	"github.com/USA-RedDragon/astro-stacker/internal/bodies"
	"github.com/gin-gonic/gin"
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
