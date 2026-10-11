package server

import (
	"net/http"

	"github.com/USA-RedDragon/astro-stacker/internal/sublength"
	"github.com/gin-gonic/gin"
)

func applySubLengthRoutes(g *gin.RouterGroup, s *sublength.Service) {
	g.GET("/sublength", func(c *gin.Context) {
		if s == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{errorKey: "sub lengths are not computed: the stacker has no scheduler database"})
			return
		}
		res := s.Latest()
		if res == nil {
			var err error
			if res, err = s.Refresh(c.Request.Context()); err != nil && res == nil {
				c.JSON(http.StatusInternalServerError, gin.H{errorKey: err.Error()})
				return
			}
		}
		c.JSON(http.StatusOK, res.For(c.Query("object"), c.Query("target_guid")))
	})
}
