package server

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/binaries"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	eclipseDaysDefault = 30
	eclipseDaysMax     = 90
	pngType            = "image/png"
)

// BinaryList is the My discoveries page.
type BinaryList struct {
	Stars []binaries.Binary `json:"stars"`
	// Others are the confirmed stars that are not eclipsing binaries.
	Others     []binaries.Other    `json:"others"`
	StarTypes  []binaries.StarType `json:"star_types"`
	Discoverer string              `json:"discoverer"`
	Facts      binaries.Progress   `json:"facts"`
}

func binaryFileURL(id int, name string) string {
	return fmt.Sprintf("%s/discoveries/binaries/%d/files/%s", apiPrefix, id, name)
}

func shareURL(id, version int) string {
	return fmt.Sprintf("%s/discoveries/sources/%d/share.jpg?v=%d", apiPrefix, id, version)
}

const apiPrefix = "/api/v1"

func applyBinaryRoutes(g *gin.RouterGroup, svc *binaries.Service) {
	if svc == nil {
		return
	}
	g.GET("/discoveries/binaries", func(c *gin.Context) { listBinaries(c, svc) })
	g.PUT("/discoveries/binaries/settings", func(c *gin.Context) { binarySettings(c, svc) })
	g.PUT("/discoveries/binaries/:id", func(c *gin.Context) { updateBinary(c, svc) })
	g.PUT("/discoveries/others/:id", func(c *gin.Context) { updateOther(c, svc) })
	g.GET("/discoveries/binaries/:id/kit", func(c *gin.Context) { binaryKit(c, svc) })
	g.GET("/discoveries/binaries/:id/files/:name", func(c *gin.Context) { binaryFile(c, svc) })
	g.GET("/discoveries/binaries/:id/timings", func(c *gin.Context) { binaryTimings(c, svc) })
	g.GET("/discoveries/eclipses", func(c *gin.Context) { eclipsePlan(c, svc) })
	g.POST("/discoveries/eclipses/requests", func(c *gin.Context) { requestEclipse(c, svc) })
	g.DELETE("/discoveries/eclipses/requests/:id", func(c *gin.Context) { cancelEclipse(c, svc) })
	g.GET("/discoveries/sources/:id/share", func(c *gin.Context) { sourceShare(c, svc) })
	g.GET("/discoveries/sources/:id/share.jpg", func(c *gin.Context) { sourceShareJPEG(c, svc) })
	g.GET("/discoveries/live-curve", func(c *gin.Context) { liveCurve(c, svc) })
	g.GET("/discoveries/dip-watches", func(c *gin.Context) { dipWatches(c, svc) })
	g.PUT("/discoveries/dip-watches", func(c *gin.Context) { saveDipWatch(c, svc) })
}

// LiveCurveView is the Now page's live card, or none.
type LiveCurveView struct {
	Live *binaries.LiveCurve `json:"live"`
}

func liveCurve(c *gin.Context, svc *binaries.Service) {
	v, ok := svc.Live()
	if !ok {
		c.JSON(http.StatusOK, LiveCurveView{})
		return
	}
	c.JSON(http.StatusOK, LiveCurveView{Live: &v})
}

func dipWatches(c *gin.Context, svc *binaries.Service) {
	rows, err := svc.DipWatches(c.Request.Context())
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"dip_watches": rows})
}

func saveDipWatch(c *gin.Context, svc *binaries.Service) {
	var body struct {
		SourceID    int       `json:"source_id"`
		ProjectGUID string    `json:"project_guid"`
		Start       time.Time `json:"start"`
		End         time.Time `json:"end"`
		DepthLow    float64   `json:"depth_low"`
		DepthHigh   float64   `json:"depth_high"`
		ActiveShare float64   `json:"active_share"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{errorKey: err.Error()})
		return
	}
	w, err := svc.SaveDipWatch(c.Request.Context(), app.DipWatch{SourceID: body.SourceID, ProjectGUID: body.ProjectGUID, Start: body.Start.UTC(),
		End: body.End.UTC(), DepthLow: body.DepthLow, DepthHigh: body.DepthHigh, ActiveShare: body.ActiveShare})
	if errors.Is(err, binaries.ErrBadDipWatch) {
		c.JSON(http.StatusBadRequest, gin.H{errorKey: err.Error()})
		return
	}
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, w)
}

func binaryError(c *gin.Context, err error) {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, gin.H{errorKey: "no such confirmed variable star"})
		return
	}
	serverError(c, err)
}

func listBinaries(c *gin.Context, svc *binaries.Service) {
	stars, err := svc.Binaries(c.Request.Context())
	if err != nil {
		serverError(c, err)
		return
	}
	d, err := svc.Discoverer(c.Request.Context())
	if err != nil {
		serverError(c, err)
		return
	}
	others, err := svc.Others(c.Request.Context())
	if err != nil {
		serverError(c, err)
		return
	}
	out := BinaryList{Stars: []binaries.Binary{}, Others: []binaries.Other{}, StarTypes: binaries.StarTypes(), Discoverer: d, Facts: svc.Progress()}
	for _, s := range stars {
		out.Stars = append(out.Stars, s.View(shareURL))
	}
	for _, s := range others {
		out.Others = append(out.Others, s.OtherView(shareURL))
	}
	slices.SortStableFunc(out.Stars, func(a, b binaries.Binary) int {
		switch {
		case a.VSX.Submitted && !b.VSX.Submitted:
			return -1
		case b.VSX.Submitted && !a.VSX.Submitted:
			return 1
		}
		return a.SourceID - b.SourceID
	})
	c.Header(cacheControl, noCache)
	c.JSON(http.StatusOK, out)
}

func binarySettings(c *gin.Context, svc *binaries.Service) {
	var body struct {
		Discoverer string `json:"discoverer"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{errorKey: err.Error()})
		return
	}
	if len(body.Discoverer) > 500 {
		c.JSON(http.StatusBadRequest, gin.H{errorKey: "the discoverer line is over 500 characters"})
		return
	}
	if err := svc.SetDiscoverer(c.Request.Context(), body.Discoverer); err != nil {
		serverError(c, err)
		return
	}
	d, err := svc.Discoverer(c.Request.Context())
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"discoverer": d})
}

func bookChange(c *gin.Context) (int, binaries.BookChange, bool) {
	var body binaries.BookChange
	id, ok := sourceID(c)
	if !ok {
		return 0, body, false
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{errorKey: err.Error()})
		return 0, body, false
	}
	for _, s := range []*string{body.Notes, body.VSXRef, body.PrimaryName, body.StarType} {
		if s != nil && len(*s) > 20000 {
			c.JSON(http.StatusBadRequest, gin.H{errorKey: "too long"})
			return 0, body, false
		}
	}
	return id, body, true
}

func bookError(c *gin.Context, err error) {
	if errors.Is(err, binaries.ErrStarType) {
		c.JSON(http.StatusBadRequest, gin.H{errorKey: err.Error()})
		return
	}
	binaryError(c, err)
}

func updateBinary(c *gin.Context, svc *binaries.Service) {
	id, body, ok := bookChange(c)
	if !ok {
		return
	}
	st, err := svc.Update(c.Request.Context(), id, body)
	if err != nil {
		bookError(c, err)
		return
	}
	c.JSON(http.StatusOK, st.View(shareURL))
}

func updateOther(c *gin.Context, svc *binaries.Service) {
	id, body, ok := bookChange(c)
	if !ok {
		return
	}
	st, err := svc.UpdateOther(c.Request.Context(), id, body)
	if err != nil {
		bookError(c, err)
		return
	}
	c.JSON(http.StatusOK, st.OtherView(shareURL))
}

func binaryKit(c *gin.Context, svc *binaries.Service) {
	id, ok := sourceID(c)
	if !ok {
		return
	}
	k, err := svc.Kit(c.Request.Context(), id, binaryFileURL, shareURL)
	if err != nil {
		binaryError(c, err)
		return
	}
	c.Header(cacheControl, noCache)
	c.JSON(http.StatusOK, k)
}

func binaryTimings(c *gin.Context, svc *binaries.Service) {
	id, ok := sourceID(c)
	if !ok {
		return
	}
	r, err := svc.Timings(c.Request.Context(), id)
	if err != nil {
		binaryError(c, err)
		return
	}
	c.Header(cacheControl, noCache)
	c.JSON(http.StatusOK, r)
}

func binaryFile(c *gin.Context, svc *binaries.Service) {
	id, ok := sourceID(c)
	if !ok {
		return
	}
	name := c.Param("name")
	if !slices.Contains([]string{"phased.png", "own.png", "chart.png"}, name) {
		c.JSON(http.StatusNotFound, gin.H{errorKey: "no such file"})
		return
	}
	b, err := svc.File(c.Request.Context(), id, name)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{errorKey: "not made yet"})
			return
		}
		serverError(c, err)
		return
	}
	c.Header(cacheControl, noCache)
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"find-%d-%s\"", id, name))
	c.Data(http.StatusOK, pngType, b)
}

func eclipsePlan(c *gin.Context, svc *binaries.Service) {
	days := eclipseDaysDefault
	if s := c.Query("days"); s != "" {
		v, err := strconv.Atoi(s)
		if err != nil || v < 1 || v > eclipseDaysMax {
			c.JSON(http.StatusBadRequest, gin.H{errorKey: "days must be 1 to " + strconv.Itoa(eclipseDaysMax)})
			return
		}
		days = v
	}
	all := c.Query("all") == "1" || c.Query("all") == "true"
	p, err := svc.Plan(c.Request.Context(), days, all)
	if err != nil {
		serverError(c, err)
		return
	}
	c.Header(cacheControl, noCache)
	c.JSON(http.StatusOK, p)
}

func requestEclipse(c *gin.Context, svc *binaries.Service) {
	var body struct {
		SourceID int       `json:"source_id"`
		Kind     string    `json:"kind"`
		Mid      time.Time `json:"mid"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{errorKey: err.Error()})
		return
	}
	if body.Kind != "primary" && body.Kind != "secondary" {
		c.JSON(http.StatusBadRequest, gin.H{errorKey: "kind must be primary or secondary"})
		return
	}
	r, err := svc.RequestImaging(c.Request.Context(), body.SourceID, body.Kind, body.Mid)
	switch {
	case errors.Is(err, binaries.ErrNoEclipse):
		c.JSON(http.StatusBadRequest, gin.H{errorKey: err.Error()})
		return
	case errors.Is(err, binaries.ErrNotReady), errors.Is(err, binaries.ErrOver):
		c.JSON(http.StatusConflict, gin.H{errorKey: err.Error()})
		return
	case err != nil && r.ID == 0:
		binaryError(c, err)
		return
	}
	c.JSON(http.StatusOK, binaries.RequestView{ID: r.ID, State: r.State, Note: r.Note})
}

func cancelEclipse(c *gin.Context, svc *binaries.Service) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{errorKey: errIDNumber})
		return
	}
	r, err := svc.CancelRequest(c.Request.Context(), id)
	if err != nil {
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			c.JSON(http.StatusNotFound, gin.H{errorKey: "no such request"})
		default:
			serverError(c, err)
		}
		return
	}
	c.JSON(http.StatusOK, binaries.RequestView{ID: r.ID, State: r.State, Note: r.Note})
}

func sourceShare(c *gin.Context, svc *binaries.Service) {
	id, ok := sourceID(c)
	if !ok {
		return
	}
	sh, err := svc.Share(c.Request.Context(), id)
	if err != nil {
		sourceError(c, err)
		return
	}
	out := binaries.Share{State: sh.State, Version: sh.Version, Note: sh.Note}
	if sh.State == binaries.ShareReady {
		out.URL = shareURL(id, sh.Version)
	}
	c.Header(cacheControl, noCache)
	c.JSON(http.StatusOK, out)
}

func sourceShareJPEG(c *gin.Context, svc *binaries.Service) {
	id, ok := sourceID(c)
	if !ok {
		return
	}
	b, sh, err := svc.ShareBytes(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{errorKey: "not made yet"})
			return
		}
		serverError(c, err)
		return
	}
	if v := c.Query("v"); v != "" && v == strconv.Itoa(sh.Version) {
		c.Header(cacheControl, "public, max-age=31536000, immutable")
	} else {
		c.Header(cacheControl, noCache)
	}
	if c.Query("download") == "1" {
		c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"find-%d-v%d.jpg\"", id, sh.Version))
	}
	c.Data(http.StatusOK, "image/jpeg", b)
}
