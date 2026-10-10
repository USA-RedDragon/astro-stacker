package server

import (
	"net/http"
	"strings"

	"github.com/USA-RedDragon/astro-stacker/internal/webui"
	"github.com/gin-gonic/gin"
)

func applyWebUI(r *gin.Engine) {
	h := webui.Handler(webui.FS())
	r.NoRoute(func(c *gin.Context) {
		p := c.Request.URL.Path
		if strings.HasPrefix(p, "/api/") || p == "/api" {
			c.JSON(http.StatusNotFound, gin.H{errorKey: "not found"})
			return
		}
		c.Status(http.StatusOK)
		h.ServeHTTP(c.Writer, c.Request)
	})
}
