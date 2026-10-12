package server

import (
	"net/http"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/moonkeep"
	"github.com/gin-gonic/gin"
)

// MoonKeepView is the kept-rate table, its replay over past nights and,
// when start and end are given, the Moon and the class rows through that
// night.
type MoonKeepView struct {
	*moonkeep.Result
	Night []moonkeep.Slot `json:"night,omitempty"`
}

func applyMoonKeepRoutes(g *gin.RouterGroup, s *moonkeep.Service) {
	g.GET("/moonkeep", func(c *gin.Context) {
		if s == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{errorKey: "moon kept rates are not computed: the stacker has no scheduler database"})
			return
		}
		var start, end time.Time
		if v := c.Query("start"); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{errorKey: "start must be an RFC 3339 time"})
				return
			}
			start = t
		}
		if v := c.Query("end"); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{errorKey: "end must be an RFC 3339 time"})
				return
			}
			end = t
		}
		if start.IsZero() != end.IsZero() || (!start.IsZero() && (!end.After(start) || end.Sub(start) > 36*time.Hour)) {
			c.JSON(http.StatusBadRequest, gin.H{errorKey: "start and end must both be given, end after start, at most 36 hours apart"})
			return
		}
		res, err := s.Current(c.Request.Context())
		if err != nil && res == nil {
			c.JSON(http.StatusInternalServerError, gin.H{errorKey: err.Error()})
			return
		}
		view := MoonKeepView{Result: res}
		if !start.IsZero() && res.Model() != nil {
			site, err := s.SiteOf(c.Request.Context())
			if err == nil {
				view.Night = moonkeep.Night(res.Model(), res.Candidates(), site.Latitude, site.Longitude, start, end)
			}
		}
		c.JSON(http.StatusOK, view)
	})
}
