package server

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/conditions"
	"github.com/gin-gonic/gin"
)

func applyConditionsRoutes(g *gin.RouterGroup, x Extras, now func() time.Time) {
	g.GET("/scheduler/conditions", func(c *gin.Context) {
		if x.Conditions == nil {
			none := conditions.Source{Source: conditions.SourceNone}
			r := conditions.Report{At: now()}
			r.SetObservatorySources(none)
			r.Power.Source, r.Sync.Source = none, none
			c.JSON(http.StatusOK, r)
			return
		}
		c.JSON(http.StatusOK, x.Conditions.Report(c.Request.Context()))
	})
	g.GET("/scheduler/conditions/camera-history", func(c *gin.Context) {
		minutes := 60
		if q := c.Query("minutes"); q != "" {
			n, err := strconv.Atoi(q)
			if err != nil || n < conditions.HistoryMinMinutes || n > conditions.HistoryMaxMinutes {
				c.JSON(http.StatusBadRequest, gin.H{errorKey: fmt.Sprintf("minutes must be a whole number from %d to %d",
					conditions.HistoryMinMinutes, conditions.HistoryMaxMinutes)})
				return
			}
			minutes = n
		}
		if x.Conditions == nil {
			c.JSON(http.StatusOK, conditions.CameraHistory{
				Source:      conditions.Source{Source: conditions.SourceNone, Error: "no metrics store is configured"},
				StepSeconds: int(conditions.HistoryStep(minutes) / time.Second), Points: []conditions.CameraPoint{},
			})
			return
		}
		c.JSON(http.StatusOK, x.Conditions.CameraHistory(c.Request.Context(), minutes))
	})
	g.GET("/scheduler/moon", func(c *gin.Context) {
		start, err1 := time.Parse(time.RFC3339, c.Query("start"))
		end, err2 := time.Parse(time.RFC3339, c.Query("end"))
		if err1 != nil || err2 != nil || !end.After(start) {
			c.JSON(http.StatusBadRequest, gin.H{errorKey: "start and end must be RFC3339 times, end after start"})
			return
		}
		if x.Mosaics == nil || x.Mosaics.Site == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{errorKey: "The observatory site is unknown."})
			return
		}
		site, ok := x.Mosaics.Site(c.Request.Context())
		if !ok {
			c.JSON(http.StatusServiceUnavailable, gin.H{errorKey: "The observatory site is unknown."})
			return
		}
		c.JSON(http.StatusOK, conditions.Moon(site.Lat, site.Lon, start, end))
	})
}
