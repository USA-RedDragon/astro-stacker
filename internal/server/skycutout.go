package server

import (
	"errors"
	"math"
	"net/http"
	"strconv"

	"github.com/USA-RedDragon/astro-stacker/internal/skycutout"
	"github.com/gin-gonic/gin"
)

const cutoutCacheControl = "public, max-age=604800"

func applySkyCutoutRoutes(g *gin.RouterGroup, svc *skycutout.Service) {
	g.GET("/sky/cutout", func(c *gin.Context) {
		if svc == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{errorKey: skycutout.ErrOff.Error()})
			return
		}
		p, err := skycutout.Parse(c.Query("ra"), c.Query("dec"), c.Query("fov"), c.Query("rotation"), c.Query("width"), c.Query("height"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{errorKey: err.Error()})
			return
		}
		out, err := svc.Get(c.Request.Context(), p)
		var limited *skycutout.RateLimitError
		switch {
		case errors.As(err, &limited):
			c.Header("Retry-After", strconv.Itoa(int(math.Max(1, math.Ceil(limited.RetryAfter.Seconds())))))
			c.JSON(http.StatusTooManyRequests, gin.H{errorKey: err.Error()})
			return
		case errors.Is(err, skycutout.ErrOff):
			c.JSON(http.StatusServiceUnavailable, gin.H{errorKey: err.Error()})
			return
		case err != nil:
			c.JSON(http.StatusBadGateway, gin.H{errorKey: err.Error()})
			return
		}
		c.Header(cacheControl, cutoutCacheControl)
		c.Header("ETag", out.ETag)
		if c.GetHeader("If-None-Match") == out.ETag {
			c.Status(http.StatusNotModified)
			return
		}
		c.Data(http.StatusOK, skycutout.ContentType, out.Data)
	})
}
