package server

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/goals"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type GoalMaskInfo struct {
	Object        string    `json:"object"`
	Filter        string    `json:"filter"`
	Width         int       `json:"width"`
	Height        int       `json:"height"`
	Bin           int       `json:"bin"`
	FrameWidth    int       `json:"frame_width"`
	FrameHeight   int       `json:"frame_height"`
	Source        string    `json:"source"`
	NoiseMask     string    `json:"noise_mask"`
	Sky           float64   `json:"sky"`
	BandLo        *float64  `json:"band_lo"`
	BandHi        *float64  `json:"band_hi"`
	BandLowPct    float64   `json:"band_low_percentile"`
	BandHighPct   float64   `json:"band_high_percentile"`
	TotalPixels   int       `json:"total_pixels"`
	CoveredPixels int       `json:"covered_pixels"`
	BandPixels    int       `json:"band_pixels"`
	StarPixels    int       `json:"star_pixels"`
	SkyPixels     int       `json:"sky_pixels"`
	BandPct       float64   `json:"band_pct"`
	StarPct       float64   `json:"star_pct"`
	Subs          int       `json:"subs"`
	MeasuredAt    time.Time `json:"measured_at"`
}

func goalMaskInfo(gm app.GoalMask) GoalMaskInfo {
	info := GoalMaskInfo{Object: gm.Object, Filter: gm.Filter, Width: gm.Width, Height: gm.Height, Bin: gm.Bin,
		FrameWidth: gm.FrameWidth, FrameHeight: gm.FrameHeight, Source: gm.Source, NoiseMask: gm.NoiseMask,
		Sky: gm.Sky, BandLo: gm.BandLo, BandHi: gm.BandHi, TotalPixels: gm.Width * gm.Height,
		CoveredPixels: gm.Covered, BandPixels: gm.Band, StarPixels: gm.Stars, SkyPixels: gm.SkyPixels,
		Subs: gm.Subs, MeasuredAt: gm.MeasuredAt}
	if gm.Source == goals.NoiseMaskFaint {
		info.BandLowPct, info.BandHighPct = goals.BandLowPercentile, goals.BandHighPercent
	}
	if gm.Covered > 0 {
		info.BandPct = 100 * float64(gm.Band) / float64(gm.Covered)
		info.StarPct = 100 * float64(gm.Stars) / float64(gm.Covered)
	}
	return info
}

func loadGoalMask(c *gin.Context, columns ...string) (app.GoalMask, bool) {
	object, filter := c.Query("object"), c.Query("filter")
	if object == "" || filter == "" {
		c.JSON(http.StatusBadRequest, gin.H{errorKey: "object and filter are required"})
		return app.GoalMask{}, false
	}
	di, ok := depInjection(c)
	if !ok {
		return app.GoalMask{}, false
	}
	var gm app.GoalMask
	q := di.AppStore.DB().WithContext(c.Request.Context())
	if len(columns) > 0 {
		q = q.Select(columns)
	}
	err := q.Where("object = ? AND filter = ?", object, filter).Take(&gm).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, gin.H{errorKey: "not measured yet"})
		return app.GoalMask{}, false
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{errorKey: err.Error()})
		return app.GoalMask{}, false
	}
	return gm, true
}

func goalMaskInfoRoute(c *gin.Context) {
	gm, ok := loadGoalMask(c, "object", "filter", "width", "height", "bin", "frame_width", "frame_height", "source",
		"noise_mask", "sky", "band_lo", "band_hi", "covered", "band", "stars", "sky_pixels", "subs", "measured_at")
	if !ok {
		return
	}
	c.Header(cacheControl, noCache)
	c.JSON(http.StatusOK, goalMaskInfo(gm))
}

func goalMaskRoute(c *gin.Context) {
	bit, err := goals.LayerBit(c.Query("layer"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{errorKey: err.Error()})
		return
	}
	gm, ok := loadGoalMask(c)
	if !ok {
		return
	}
	etag := `"` + strconv.FormatInt(gm.MeasuredAt.UnixNano(), 36) + "-" + strconv.Itoa(int(bit)) + `"`
	c.Header(cacheControl, noCache)
	c.Header("ETag", etag)
	if c.GetHeader("If-None-Match") == etag {
		c.Status(http.StatusNotModified)
		return
	}
	bits, err := goals.DecompressMask(gm.Data, gm.Width, gm.Height)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{errorKey: err.Error()})
		return
	}
	img, err := goals.MaskPNG(bits, gm.Width, gm.Height, bit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{errorKey: err.Error()})
		return
	}
	c.Header("X-Mask-Width", strconv.Itoa(gm.Width))
	c.Header("X-Mask-Height", strconv.Itoa(gm.Height))
	c.Data(http.StatusOK, "image/png", img)
}
