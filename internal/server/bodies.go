package server

import (
	"errors"
	"net/http"
	"strconv"

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

func applyCometRoutes(g *gin.RouterGroup, list *bodies.CometList) {
	g.GET("/comets", func(c *gin.Context) {
		if list == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{errorKey: "the comet list is off"})
			return
		}
		maxMag, minAlt := bodies.DefaultMaxMag, 0.0
		for key, dst := range map[string]*float64{"max_mag": &maxMag, "min_alt": &minAlt} {
			v := c.Query(key)
			if v == "" {
				continue
			}
			f, err := strconv.ParseFloat(v, 64)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{errorKey: key + " must be a number"})
				return
			}
			*dst = f
		}
		all := c.Query("all") == "1" || c.Query("all") == "true"
		view, err := list.List(c.Request.Context(), maxMag, minAlt, all)
		switch {
		case errors.Is(err, bodies.ErrNoSite), errors.Is(err, bodies.ErrNoElements):
			c.JSON(http.StatusServiceUnavailable, gin.H{errorKey: err.Error()})
		case err != nil:
			c.JSON(http.StatusInternalServerError, gin.H{errorKey: err.Error()})
		default:
			c.JSON(http.StatusOK, view)
		}
	})
}
