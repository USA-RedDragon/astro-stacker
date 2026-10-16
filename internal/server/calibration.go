package server

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/calpublish"
	"github.com/USA-RedDragon/astro-stacker/internal/calreq"
	"github.com/USA-RedDragon/astro-stacker/internal/coverage"
	"github.com/USA-RedDragon/astro-stacker/internal/darkneed"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/gin-gonic/gin"
)

type biasBacklogResponse struct {
	coverage.BiasBacklog
	Publish calpublish.Status `json:"publish"`
}

func requestPublish(x Extras) func(typ string) calreq.Publish {
	return func(typ string) calreq.Publish {
		var table string
		var s darkneed.Status
		switch typ {
		case app.CalibrationFlats:
			table, s = calpublish.FlatTable, x.FlatRequests.Status()
		case app.CalibrationDarks:
			table, s = darkneed.Table, x.DarkNeed.Status()
		default:
			table, s = calpublish.BiasTable, x.BiasNeed.Status()
		}
		return calreq.Publish{Table: table, Mode: s.Mode, PublishedAt: s.PublishedAt, Skipped: s.Skipped, Error: s.Error}
	}
}

func applyCalibrationRoutes(g *gin.RouterGroup, x Extras) {
	now := x.Now
	if now == nil {
		now = time.Now
	}
	g.GET("/coverage/bias-backlog", func(c *gin.Context) {
		di, ok := depInjection(c)
		if !ok {
			return
		}
		b, err := coverage.BuildBiasBacklog(c.Request.Context(), di.AppStore.DB(), now())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{errorKey: err.Error()})
			return
		}
		c.JSON(http.StatusOK, biasBacklogResponse{BiasBacklog: b, Publish: x.BiasNeed.Status()})
	})
	g.GET("/scheduler/calibration/recent", func(c *gin.Context) {
		hours := 24
		if q := c.Query("hours"); q != "" {
			n, err := strconv.Atoi(q)
			if err != nil || n < 1 || n > 168 {
				c.JSON(http.StatusBadRequest, gin.H{errorKey: "hours must be a whole number from 1 to 168"})
				return
			}
			hours = n
		}
		di, ok := depInjection(c)
		if !ok {
			return
		}
		to := now()
		r, err := coverage.BuildRecentCalibration(c.Request.Context(), di.AppStore.DB(), to.Add(-time.Duration(hours)*time.Hour), to)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{errorKey: err.Error()})
			return
		}
		c.JSON(http.StatusOK, r)
	})
	g.GET("/calibration/requests", func(c *gin.Context) {
		di, ok := depInjection(c)
		if !ok {
			return
		}
		ctx := c.Request.Context()
		reqs, err := calreq.List(ctx, di.AppStore.DB(), c.Query("object"), c.Query("status"))
		if errors.Is(err, calreq.ErrStatus) {
			c.JSON(http.StatusBadRequest, gin.H{errorKey: err.Error()})
			return
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{errorKey: err.Error()})
			return
		}
		views, err := calreq.Views(ctx, di.AppStore.DB(), reqs, requestPublish(x))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{errorKey: err.Error()})
			return
		}
		c.JSON(http.StatusOK, views)
	})
}
