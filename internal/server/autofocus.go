package server

import (
	"net/http"
	"strconv"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/autofocus"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const autofocusDefaultWindow = 7 * 24 * time.Hour

func applyAutofocusRoutes(g *gin.RouterGroup, now func() time.Time) {
	g.GET("/scheduler/autofocus", func(c *gin.Context) {
		di, ok := depInjection(c)
		if !ok {
			return
		}
		since := now().Add(-autofocusDefaultWindow)
		if s := c.Query("since"); s != "" {
			t, err := time.Parse(time.RFC3339, s)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{errorKey: "since must be RFC3339"})
				return
			}
			since = t
		}
		limit := autofocus.DefaultLimit
		if s := c.Query("limit"); s != "" {
			n, err := strconv.Atoi(s)
			if err != nil || n < 1 || n > autofocus.MaxLimit {
				c.JSON(http.StatusBadRequest, gin.H{errorKey: "limit must be 1 to " + strconv.Itoa(autofocus.MaxLimit)})
				return
			}
			limit = n
		}
		var db *gorm.DB
		if di.SchedulerDBStore != nil {
			db = di.SchedulerDBStore.DB()
		}
		out, err := autofocus.Load(c.Request.Context(), db, since, limit)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{errorKey: err.Error()})
			return
		}
		c.JSON(http.StatusOK, out)
	})
}
