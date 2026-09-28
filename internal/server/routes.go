package server

import (
	"net/http"
	"path"

	"github.com/USA-RedDragon/pixinsight-worker/internal/coverage"
	"github.com/USA-RedDragon/pixinsight-worker/internal/previewer"
	"github.com/USA-RedDragon/pixinsight-worker/internal/server/middleware"
	"github.com/USA-RedDragon/pixinsight-worker/internal/store/models/app"
	"github.com/gin-gonic/gin"
)

func applyRoutes(r *gin.Engine, signer *previewer.Signer) {
	r.GET("/ping", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"state": "OK"})
	})

	v1(r.Group("/api/v1"), signer)
}

// PreviewURL is a presigned link to one light's auto-stretched preview.
type PreviewURL struct {
	File string `json:"file"`
	URL  string `json:"url"`
}

func v1(r *gin.RouterGroup, signer *previewer.Signer) {
	r.GET("/version", func(c *gin.Context) {
		di := c.MustGet(middleware.DepInjectionKey).(*middleware.DepInjection)
		c.String(http.StatusOK, "%s", di.Version)
	})

	// Calibration coverage per night of lights; ?object= narrows to one target.
	r.GET("/coverage", func(c *gin.Context) {
		di := c.MustGet(middleware.DepInjectionKey).(*middleware.DepInjection)
		rows, err := coverage.Report(c.Request.Context(), di.AppStore.DB(), c.Query("object"))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, rows)
	})

	// Presigned preview URLs for one target's lights, keyed by file name.
	r.GET("/previews", func(c *gin.Context) {
		if signer == nil {
			c.JSON(http.StatusOK, []PreviewURL{})
			return
		}
		object := c.Query("object")
		if object == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "object is required"})
			return
		}
		di := c.MustGet(middleware.DepInjectionKey).(*middleware.DepInjection)
		var frames []app.Frame
		if err := di.AppStore.DB().WithContext(c.Request.Context()).Select("key", "preview_key").
			Where("object = ? AND type = ? AND preview_key IS NOT NULL", object, "LIGHT").
			Find(&frames).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		out := make([]PreviewURL, 0, len(frames))
		for _, f := range frames {
			u, err := signer.URL(c.Request.Context(), *f.PreviewKey)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			out = append(out, PreviewURL{File: path.Base(f.Key), URL: u})
		}
		c.JSON(http.StatusOK, out)
	})

	// Dark library capture list: ladder setpoints lights need but no darks cover.
	r.GET("/coverage/dark-gaps", func(c *gin.Context) {
		di := c.MustGet(middleware.DepInjectionKey).(*middleware.DepInjection)
		gaps, err := coverage.Gaps(c.Request.Context(), di.AppStore.DB())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gaps)
	})
}
