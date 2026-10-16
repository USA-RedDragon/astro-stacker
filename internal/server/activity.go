package server

import (
	"net/http"

	"github.com/USA-RedDragon/astro-stacker/internal/activity"
	"github.com/gin-gonic/gin"
)

// activityHandler answers what every background process is doing.
func activityHandler(reg *activity.Registry) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, reg.Snapshot())
	}
}
