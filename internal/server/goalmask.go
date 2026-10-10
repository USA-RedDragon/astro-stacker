package server

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/goals"
	"github.com/USA-RedDragon/astro-stacker/internal/server/middleware"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type GoalMaskInfo struct {
	Measured      bool      `json:"measured"`
	Reason        string    `json:"reason,omitempty"`
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
	info := GoalMaskInfo{Measured: true, Object: gm.Object, Filter: gm.Filter, Width: gm.Width, Height: gm.Height, Bin: gm.Bin,
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
	gm, found, ok := findGoalMask(c, columns...)
	if ok && !found {
		c.JSON(http.StatusNotFound, gin.H{errorKey: "not measured yet"})
		return gm, false
	}
	return gm, ok && found
}

func goalMaskInfoRoute(c *gin.Context) {
	gm, found, ok := findGoalMask(c, "object", "filter", "width", "height", "bin", "frame_width", "frame_height", "source",
		"noise_mask", "sky", "band_lo", "band_hi", "covered", "band", "stars", "sky_pixels", "subs", "measured_at")
	if !ok {
		return
	}
	c.Header(cacheControl, noCache)
	if found {
		c.JSON(http.StatusOK, goalMaskInfo(gm))
		return
	}
	object, filter := c.Query("object"), c.Query("filter")
	di, _ := depInjection(c)
	reason, exists, err := noMaskReason(c, di, object, filter)
	switch {
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{errorKey: err.Error()})
	case !exists:
		c.JSON(http.StatusNotFound, gin.H{errorKey: "no master for this object and filter"})
	default:
		c.JSON(http.StatusOK, GoalMaskInfo{Object: object, Filter: filter, Reason: reason})
	}
}

func noMaskReason(c *gin.Context, di *middleware.DepInjection, object, filter string) (string, bool, error) {
	db := di.AppStore.DB().WithContext(c.Request.Context())
	var stacks int64
	if err := db.Model(&app.Stack{}).Where("object = ? AND filter = ? AND subs > 0", object, filter).Count(&stacks).Error; err != nil {
		return "", false, err
	}
	if stacks == 0 {
		return "", false, nil
	}
	var ms []app.GoalMeasurement
	if err := db.Select("error", "measured_at").Where("object = ? AND filter = ?", object, filter).Limit(1).Find(&ms).Error; err != nil {
		return "", false, err
	}
	switch {
	case len(ms) == 0 && di.Config != nil && !di.Config.Goals.Enabled:
		return "goal measurement is off in the stacker", true, nil
	case len(ms) == 0:
		return "this master has had no goal pass yet", true, nil
	}
	m := ms[0]
	if m.Error != nil {
		return "the last goal pass failed: " + *m.Error, true, nil
	}
	return "measured " + m.MeasuredAt.UTC().Format(time.RFC3339) + ", before masks were stored; the mask is stored on this master's next goal pass", true, nil
}

func findGoalMask(c *gin.Context, columns ...string) (app.GoalMask, bool, bool) {
	object, filter := c.Query("object"), c.Query("filter")
	if object == "" || filter == "" {
		c.JSON(http.StatusBadRequest, gin.H{errorKey: "object and filter are required"})
		return app.GoalMask{}, false, false
	}
	di, ok := depInjection(c)
	if !ok {
		return app.GoalMask{}, false, false
	}
	var gm app.GoalMask
	q := di.AppStore.DB().WithContext(c.Request.Context())
	if len(columns) > 0 {
		q = q.Select(columns)
	}
	err := q.Where("object = ? AND filter = ?", object, filter).Take(&gm).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return app.GoalMask{}, false, true
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{errorKey: err.Error()})
		return app.GoalMask{}, false, false
	}
	return gm, true, true
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
