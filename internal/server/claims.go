package server

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/USA-RedDragon/astro-stacker/internal/claimswatch"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func applyClaimsRoutes(g *gin.RouterGroup, svc *claimswatch.Service) {
	if svc == nil {
		return
	}
	g.GET("/discoveries/claims", func(c *gin.Context) {
		v, err := svc.Summary(c.Request.Context())
		if err != nil {
			serverError(c, err)
			return
		}
		c.Header(cacheControl, noCache)
		c.JSON(http.StatusOK, v)
	})
	g.PUT("/discoveries/claims/mentions/:id", func(c *gin.Context) {
		id, err := strconv.Atoi(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{errorKey: errIDNumber})
			return
		}
		var body struct {
			Mark *string `json:"mark"`
		}
		if err := c.ShouldBindJSON(&body); err != nil || body.Mark == nil {
			c.JSON(http.StatusBadRequest, gin.H{errorKey: claimswatch.ErrBadMark.Error()})
			return
		}
		v, err := svc.Mark(c.Request.Context(), id, *body.Mark)
		switch {
		case errors.Is(err, claimswatch.ErrBadMark):
			c.JSON(http.StatusBadRequest, gin.H{errorKey: err.Error()})
		case errors.Is(err, gorm.ErrRecordNotFound):
			c.JSON(http.StatusNotFound, gin.H{errorKey: "no such mention"})
		case err != nil:
			serverError(c, err)
		default:
			c.JSON(http.StatusOK, v)
		}
	})
}
