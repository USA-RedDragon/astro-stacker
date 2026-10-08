package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/coverage"
	"github.com/USA-RedDragon/astro-stacker/internal/events"
	"github.com/USA-RedDragon/astro-stacker/internal/previewer"
	"github.com/USA-RedDragon/astro-stacker/internal/publicframe"
	"github.com/USA-RedDragon/astro-stacker/internal/server/middleware"
	"github.com/USA-RedDragon/astro-stacker/internal/stacking"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func applyRoutes(r *gin.Engine, signer *previewer.Signer, broker *events.Broker) {
	r.GET("/ping", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"state": "OK"})
	})
	// Server-sent events for rendered previews and updated masters.
	r.GET("/api/v1/events", gin.WrapH(broker))

	v1(r.Group("/api/v1"), signer)
}

// Master is one filter's stacked master for a target, with presigned links.
type Master struct {
	Filter           string    `json:"filter"`
	Subs             int       `json:"subs"`
	ExposureSeconds  float64   `json:"exposure_seconds"`
	EffectiveSeconds float64   `json:"effective_seconds"`
	Width            int       `json:"width"`
	Height           int       `json:"height"`
	UpdatedAt        time.Time `json:"updated_at"`
	MasterURL        string    `json:"master_url"`
	PreviewURL       string    `json:"preview_url"`
	LinearURL        string    `json:"linear_url"`
	Crop             *Crop     `json:"crop,omitempty"`
	// XISFURL is the master for PixInsight, which opens it upright and
	// plate solved.
	XISFURL string `json:"xisf_url,omitempty"`
	// FittedURL is the master linear fitted to FitReference, when the
	// target has other colour or other narrowband filters.
	FittedURL    string `json:"fitted_url,omitempty"`
	FitReference string `json:"fit_reference,omitempty"`
	// FitError is why the master couldn't be fitted to the others.
	FitError string `json:"fit_error,omitempty"`
	// Comet* are a comet's master aligned on the comet, when there is one.
	CometPreviewURL string `json:"comet_preview_url,omitempty"`
	CometURL        string `json:"comet_url,omitempty"`
	CometXISFURL    string `json:"comet_xisf_url,omitempty"`
}

// Crop is the well-covered part of an image as fractions of its width and
// height, for cropping previews and the linear preview alike.
type Crop struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

func cropOf(x, y, w, h, width, height int) *Crop {
	if w <= 0 || h <= 0 || width <= 0 || height <= 0 {
		return nil
	}
	fw, fh := float64(width), float64(height)
	return &Crop{X: float64(x) / fw, Y: float64(y) / fh, W: float64(w) / fw, H: float64(h) / fh}
}

// Mosaic is one filter's mosaic of a project's panels, with presigned links.
type Mosaic struct {
	Filter      string    `json:"filter"`
	Panels      int       `json:"panels"`
	PanelsTotal int       `json:"panels_total"`
	Width       int       `json:"width"`
	Height      int       `json:"height"`
	UpdatedAt   time.Time `json:"updated_at"`
	MasterURL   string    `json:"master_url"`
	PreviewURL  string    `json:"preview_url"`
	LinearURL   string    `json:"linear_url"`
	Crop        *Crop     `json:"crop,omitempty"`
	// XISFURL is the mosaic for PixInsight, which opens it upright and
	// plate solved.
	XISFURL string `json:"xisf_url,omitempty"`
}

// monoFilterOrder ranks filters by how well one shows a target on its own:
// nebulae are faint in blue and green but bright in H-a.
func monoFilterOrder() []string {
	return []string{"H-a", "O-III", "S-II", "Luminance", "Red", "Green", "Blue"}
}

// monoCovers picks each object's single-filter cover: the highest-ranked
// filter with at least a quarter of its best filter's effective exposure,
// so a thin H-a master doesn't beat a deep one in another filter.
func monoCovers(stacks []app.Stack) []app.Stack {
	best := map[string]float64{}
	for _, s := range stacks {
		best[s.Object] = max(best[s.Object], s.EffectiveSeconds)
	}
	order := monoFilterOrder()
	rank := func(f string) int {
		if i := slices.Index(order, f); i >= 0 {
			return i
		}
		return len(order)
	}
	chosen := map[string]app.Stack{}
	for _, s := range stacks {
		if s.EffectiveSeconds < best[s.Object]/4 {
			continue
		}
		c, ok := chosen[s.Object]
		if !ok || rank(s.Filter) < rank(c.Filter) || (rank(s.Filter) == rank(c.Filter) && s.EffectiveSeconds > c.EffectiveSeconds) {
			chosen[s.Object] = s
		}
	}
	out := make([]app.Stack, 0, len(chosen))
	for _, s := range chosen {
		out = append(out, s)
	}
	return out
}

// Cover is the preview that best shows a target or project.
type Cover struct {
	// Palette is set for a colour composite (RGB+Ha, RGB, SHO, HOO, with
	// +OIII when O-III is added to RGB);
	// Filter for a single filter's preview.
	Palette          string    `json:"palette,omitempty"`
	Filter           string    `json:"filter,omitempty"`
	PreviewURL       string    `json:"preview_url"`
	EffectiveSeconds float64   `json:"effective_seconds,omitempty"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// Covers maps targets and mosaic projects to their cover previews.
type Covers struct {
	Objects map[string]Cover `json:"objects"`
	Mosaics map[string]Cover `json:"mosaics"`
}

// PreviewURL is a presigned link to one light's auto-stretched preview.
type PreviewURL struct {
	File string `json:"file"`
	URL  string `json:"url"`
}

const (
	errorKey            = "error"
	objectRequired      = "object is required"
	lightType           = "LIGHT"
	jpegContentType     = "image/jpeg"
	cacheControl        = "Cache-Control"
	noCache             = "no-cache"
	depInjectionMissing = "dependency injection unavailable"
)

func depInjection(c *gin.Context) (*middleware.DepInjection, bool) {
	di, ok := c.MustGet(middleware.DepInjectionKey).(*middleware.DepInjection)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{errorKey: depInjectionMissing})
	}
	return di, ok
}

func v1(r *gin.RouterGroup, signer *previewer.Signer) {
	r.GET("/version", versionRoute)

	// Calibration coverage per night of lights; ?object= narrows to one target.
	r.GET("/coverage", coverageRoute)

	// Presigned preview URLs for one target's lights, keyed by file name.
	r.GET("/previews", previewsRoute(signer))

	// The newest light's preview as a JPEG, for dashboard cameras. NINA's own
	// prepared-image endpoint has nothing to serve between its restart each
	// morning and the night's first exposure.
	r.GET("/latest-light.jpg", latestLightRoute(signer))

	// The public site's frame of a target, or with no object the newest of
	// any target: the light the renderer last stored for it (small,
	// stretched, watermarked; see publicframe). Only streams those bytes,
	// with an ETag for conditional requests; nothing is rendered here, and
	// it is 404 until the renderer has stored one. For wheresmyscope, in the
	// cluster: no gateway routes it.
	r.GET("/public-light.jpg", publicLightRoute(signer))

	// One preview per target and per mosaic project, for dashboard cards: a
	// colour composite where the filters allow one, else the master with the most effective exposure, or the
	// mosaic with the most panels.
	r.GET("/covers", coversRoute(signer))

	// Mosaics for one project, one per filter.
	r.GET("/mosaics", mosaicsRoute(signer))

	// One target's lights with what the stacker made of each.
	r.GET("/subs", subsRoute)

	// Stacked masters for one target, one per filter.
	r.GET("/stacks", stacksRoute(signer))

	// Every target with lights, and whether Target Scheduler knows it, so
	// targets imaged outside it can be listed too. A target counts as
	// scheduled only when Target Scheduler has a record for at least half
	// its lights: the TS5 upgrade dropped every earlier record, so targets
	// imaged mostly before it are listed with the others. Panels of a
	// mosaic never are.
	r.GET("/objects", objectsRoute)

	// Stack a target again from scratch, with a new registration reference.
	r.POST("/targets/restack", restackRoute)

	// Dark library capture list: ladder setpoints lights need but no darks cover.
	r.GET("/coverage/dark-gaps", darkGapsRoute)
}

func versionRoute(c *gin.Context) {
	di, ok := depInjection(c)
	if !ok {
		return
	}
	c.String(http.StatusOK, "%s", di.Version)
}

func coverageRoute(c *gin.Context) {
	di, ok := depInjection(c)
	if !ok {
		return
	}
	rows, err := coverage.Report(c.Request.Context(), di.AppStore.DB(), c.Query("object"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{errorKey: err.Error()})
		return
	}
	c.JSON(http.StatusOK, rows)
}

func previewsRoute(signer *previewer.Signer) gin.HandlerFunc {
	return func(c *gin.Context) {
		if signer == nil {
			c.JSON(http.StatusOK, []PreviewURL{})
			return
		}
		object := c.Query("object")
		if object == "" {
			c.JSON(http.StatusBadRequest, gin.H{errorKey: objectRequired})
			return
		}
		di, ok := depInjection(c)
		if !ok {
			return
		}
		var frames []app.Frame
		if err := di.AppStore.DB().WithContext(c.Request.Context()).Select("key", "preview_key").
			Where("object = ? AND type = ? AND preview_key IS NOT NULL", object, lightType).
			Find(&frames).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{errorKey: err.Error()})
			return
		}
		out := make([]PreviewURL, 0, len(frames))
		for _, f := range frames {
			u, err := signer.URL(c.Request.Context(), *f.PreviewKey)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{errorKey: err.Error()})
				return
			}
			out = append(out, PreviewURL{File: path.Base(f.Key), URL: u})
		}
		c.JSON(http.StatusOK, out)
	}
}

func latestLightRoute(signer *previewer.Signer) gin.HandlerFunc {
	return func(c *gin.Context) {
		if signer == nil {
			c.Status(http.StatusNotFound)
			return
		}
		ctx := c.Request.Context()
		di, ok := depInjection(c)
		if !ok {
			return
		}
		// ?object= keeps the image on the target a dashboard is labelling,
		// even before that target's first sub of the night is indexed.
		q := di.AppStore.DB().WithContext(ctx).Select("key", "preview_key", "date_obs").
			Where("type = ? AND preview_key IS NOT NULL AND date_obs IS NOT NULL", lightType)
		if object := c.Query("object"); object != "" {
			q = q.Where("object = ?", object)
		}
		var f app.Frame
		err := q.Order("date_obs DESC").Limit(1).Take(&f).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.Status(http.StatusNotFound)
			return
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{errorKey: err.Error()})
			return
		}
		obj, err := signer.Open(ctx, *f.PreviewKey)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{errorKey: err.Error()})
			return
		}
		defer obj.Close()
		info, err := obj.Stat()
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{errorKey: err.Error()})
			return
		}
		c.Header(cacheControl, noCache)
		c.Header("X-Frame-Key", f.Key)
		c.DataFromReader(http.StatusOK, info.Size, jpegContentType, obj, nil)
	}
}

func publicLightRoute(signer *previewer.Signer) gin.HandlerFunc {
	return func(c *gin.Context) {
		if signer == nil {
			c.Status(http.StatusNotFound)
			return
		}
		if !publicSize(c.Query("width"), c.Query("height")) {
			c.JSON(http.StatusBadRequest, gin.H{errorKey: fmt.Sprintf("public frames are %dx%d",
				publicframe.DefaultOptions().Width, publicframe.DefaultOptions().Height)})
			return
		}
		ctx := c.Request.Context()
		di, ok := depInjection(c)
		if !ok {
			return
		}
		f, err := publicFrame(ctx, di.AppStore.DB(), c.Query("object"))
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.Status(http.StatusNotFound)
			return
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{errorKey: err.Error()})
			return
		}
		c.Header("ETag", f.ETag)
		c.Header("Last-Modified", f.RenderedAt.UTC().Format(http.TimeFormat))
		c.Header(cacheControl, noCache)
		c.Header("X-Object", f.Object)
		c.Header("X-Filter", f.Filter)
		if f.DateObs != nil {
			c.Header("X-Date-Obs", f.DateObs.UTC().Format(time.RFC3339))
		}
		if match := c.GetHeader("If-None-Match"); match != "" && (match == f.ETag || match == "*") {
			c.Status(http.StatusNotModified)
			return
		}
		obj, err := signer.Open(ctx, f.Key)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{errorKey: err.Error()})
			return
		}
		defer obj.Close()
		info, err := obj.Stat()
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{errorKey: err.Error()})
			return
		}
		c.DataFromReader(http.StatusOK, info.Size, jpegContentType, obj, nil)
	}
}

func coversRoute(signer *previewer.Signer) gin.HandlerFunc {
	return func(c *gin.Context) {
		out := Covers{Objects: map[string]Cover{}, Mosaics: map[string]Cover{}}
		if signer == nil {
			c.JSON(http.StatusOK, out)
			return
		}
		ctx := c.Request.Context()
		di, ok := depInjection(c)
		if !ok {
			return
		}
		db := di.AppStore.DB().WithContext(ctx)
		var stacks []app.Stack
		if err := db.Where("subs > 0 AND preview_key IS NOT NULL").Order("effective_seconds DESC").Find(&stacks).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{errorKey: err.Error()})
			return
		}
		// Colour covers first; mono masters fill in the rest.
		var covers []app.Cover
		if err := db.Find(&covers).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{errorKey: err.Error()})
			return
		}
		for _, cv := range covers {
			u, err := signer.URL(ctx, cv.PreviewKey)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{errorKey: err.Error()})
				return
			}
			cover := Cover{Palette: cv.Palette, PreviewURL: u, UpdatedAt: cv.UpdatedAt}
			if project, ok := strings.CutPrefix(cv.Subject, app.MosaicSubject("")); ok {
				out.Mosaics[project] = cover
			} else {
				out.Objects[cv.Subject] = cover
			}
		}
		for _, s := range monoCovers(stacks) {
			if _, ok := out.Objects[s.Object]; ok {
				continue
			}
			u, err := signer.URL(ctx, *s.PreviewKey)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{errorKey: err.Error()})
				return
			}
			out.Objects[s.Object] = Cover{Filter: s.Filter, PreviewURL: u, EffectiveSeconds: s.EffectiveSeconds, UpdatedAt: s.UpdatedAt}
		}
		var mosaics []app.Mosaic
		if err := db.Where("preview_key IS NOT NULL").Order("panels DESC, updated_at DESC").Find(&mosaics).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{errorKey: err.Error()})
			return
		}
		for _, m := range mosaics {
			if _, ok := out.Mosaics[m.Project]; ok {
				continue
			}
			u, err := signer.URL(ctx, *m.PreviewKey)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{errorKey: err.Error()})
				return
			}
			out.Mosaics[m.Project] = Cover{Filter: m.Filter, PreviewURL: u, UpdatedAt: m.UpdatedAt}
		}
		c.JSON(http.StatusOK, out)
	}
}

func mosaicsRoute(signer *previewer.Signer) gin.HandlerFunc {
	return func(c *gin.Context) {
		if signer == nil {
			c.JSON(http.StatusOK, []Mosaic{})
			return
		}
		project := c.Query("project")
		if project == "" {
			c.JSON(http.StatusBadRequest, gin.H{errorKey: "project is required"})
			return
		}
		di, ok := depInjection(c)
		if !ok {
			return
		}
		var mosaics []app.Mosaic
		if err := di.AppStore.DB().WithContext(c.Request.Context()).
			Where("project = ? AND master_key IS NOT NULL", project).Order("filter").Find(&mosaics).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{errorKey: err.Error()})
			return
		}
		out := make([]Mosaic, 0, len(mosaics))
		for _, m := range mosaics {
			o := Mosaic{Filter: m.Filter, Panels: m.Panels, PanelsTotal: m.PanelsTotal, Width: m.Width, Height: m.Height, UpdatedAt: m.UpdatedAt,
				Crop: cropOf(m.CropX, m.CropY, m.CropW, m.CropH, m.Width, m.Height)}
			ctx := c.Request.Context()
			name := fmt.Sprintf("%s_%s_mosaic.fit", strings.ReplaceAll(m.Project, " ", "_"), strings.ReplaceAll(m.Filter, " ", "_"))
			var err error
			if o.MasterURL, err = signer.DownloadURL(ctx, *m.MasterKey, name); err == nil && m.PreviewKey != nil {
				o.PreviewURL, err = signer.URL(ctx, *m.PreviewKey)
			}
			if err == nil && m.LinearKey != nil {
				o.LinearURL, err = signer.URL(ctx, *m.LinearKey)
			}
			if err == nil && m.XISFKey != nil {
				o.XISFURL, err = signer.DownloadURL(ctx, *m.XISFKey, strings.TrimSuffix(name, ".fit")+".xisf")
			}
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{errorKey: err.Error()})
				return
			}
			out = append(out, o)
		}
		c.JSON(http.StatusOK, out)
	}
}

func stacksRoute(signer *previewer.Signer) gin.HandlerFunc {
	return func(c *gin.Context) {
		if signer == nil {
			c.JSON(http.StatusOK, []Master{})
			return
		}
		object := c.Query("object")
		if object == "" {
			c.JSON(http.StatusBadRequest, gin.H{errorKey: objectRequired})
			return
		}
		di, ok := depInjection(c)
		if !ok {
			return
		}
		var stacks []app.Stack
		if err := di.AppStore.DB().WithContext(c.Request.Context()).
			Where("object = ? AND subs > 0 AND master_key IS NOT NULL", object).
			Order("filter").Find(&stacks).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{errorKey: err.Error()})
			return
		}
		out := make([]Master, 0, len(stacks))
		for _, s := range stacks {
			m, err := stackMaster(c.Request.Context(), signer, s)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{errorKey: err.Error()})
				return
			}
			out = append(out, m)
		}
		c.JSON(http.StatusOK, out)
	}
}

func stackMaster(ctx context.Context, signer *previewer.Signer, s app.Stack) (Master, error) {
	m := Master{
		Filter: s.Filter, Subs: s.Subs, ExposureSeconds: s.ExposureSeconds, EffectiveSeconds: s.EffectiveSeconds,
		Width: s.Width, Height: s.Height, UpdatedAt: s.UpdatedAt,
		Crop: cropOf(s.CropX, s.CropY, s.CropW, s.CropH, s.Width, s.Height),
	}
	var err error
	name := fmt.Sprintf("%s_%s_master.fit", strings.ReplaceAll(s.Object, " ", "_"), strings.ReplaceAll(s.Filter, " ", "_"))
	if m.MasterURL, err = signer.DownloadURL(ctx, *s.MasterKey, name); err == nil && s.PreviewKey != nil {
		m.PreviewURL, err = signer.URL(ctx, *s.PreviewKey)
	}
	if err == nil && s.LinearKey != nil {
		m.LinearURL, err = signer.URL(ctx, *s.LinearKey)
	}
	base := strings.TrimSuffix(name, "master.fit")
	if err == nil && s.XISFKey != nil {
		m.XISFURL, err = signer.DownloadURL(ctx, *s.XISFKey, base+"master.xisf")
	}
	if err == nil && s.CometPreviewKey != nil && s.CometKey != nil && s.CometXISFKey != nil {
		m.CometPreviewURL, err = signer.URL(ctx, *s.CometPreviewKey)
		if err == nil {
			m.CometURL, err = signer.DownloadURL(ctx, *s.CometKey, base+"comet.fit")
		}
		if err == nil {
			m.CometXISFURL, err = signer.DownloadURL(ctx, *s.CometXISFKey, base+"comet.xisf")
		}
	}
	if s.FitError != nil {
		m.FitError = *s.FitError
	}
	if err == nil && s.FittedKey != nil {
		m.FitReference = s.FitReference
		m.FittedURL, err = signer.DownloadURL(ctx, *s.FittedKey, base+"linearfit"+path.Ext(*s.FittedKey))
	}
	return m, err
}

func objectsRoute(c *gin.Context) {
	di, ok := depInjection(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	var rows []struct {
		Object     string
		Lights     int
		Stacked    int
		Nights     int
		FirstNight *time.Time
		LastNight  *time.Time
	}
	if err := di.AppStore.DB().WithContext(ctx).Table("frames f").
		Select("f.object, COUNT(*) AS lights, COUNT(sf.id) FILTER (WHERE sf.status = ?) AS stacked, "+
			"COUNT(DISTINCT f.night) AS nights, MIN(f.night) AS first_night, MAX(f.night) AS last_night", app.StackStatusAdded).
		Joins("LEFT JOIN stack_frames sf ON sf.frame_id = f.id").
		Where("f.type = ? AND f.object <> '' AND f.index_error IS NULL", lightType).
		Group("f.object").Order("f.object").Scan(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{errorKey: err.Error()})
		return
	}
	var scheduled []struct {
		Name     string
		Acquired int
	}
	if err := di.SchedulerDBStore.DB().WithContext(ctx).Table("target t").
		Select(`t.name, COUNT(a."Id") AS acquired`).
		Joins(`LEFT JOIN acquiredimage a ON a."targetId" = t."Id"`).
		Group("t.name").Scan(&scheduled).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{errorKey: err.Error()})
		return
	}
	acquired := map[string]int{}
	for _, t := range scheduled {
		acquired[t.Name] += t.Acquired
	}
	// A mosaic's panels show in its mosaic, built from their files
	// whatever Target Scheduler recorded.
	panels, err := stacking.MosaicPanels(ctx, di.SchedulerDBStore.DB())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{errorKey: err.Error()})
		return
	}
	type object struct {
		Name       string  `json:"name"`
		Lights     int     `json:"lights"`
		Stacked    int     `json:"stacked"`
		Nights     int     `json:"nights"`
		FirstNight *string `json:"first_night,omitempty"`
		LastNight  *string `json:"last_night,omitempty"`
		Scheduled  bool    `json:"scheduled"`
	}
	day := func(t *time.Time) *string {
		if t == nil {
			return nil
		}
		s := t.Format("2006-01-02")
		return &s
	}
	out := make([]object, 0, len(rows))
	for _, r := range rows {
		out = append(out, object{Name: r.Object, Lights: r.Lights, Stacked: r.Stacked, Nights: r.Nights,
			FirstNight: day(r.FirstNight), LastNight: day(r.LastNight), Scheduled: panels[r.Object] || 2*acquired[r.Object] >= r.Lights})
	}
	c.JSON(http.StatusOK, out)
}

func restackRoute(c *gin.Context) {
	di, ok := depInjection(c)
	if !ok {
		return
	}
	object := c.Query("object")
	if di.Restacker == nil || object == "" {
		c.JSON(http.StatusBadRequest, gin.H{errorKey: "stacking is off or no object given"})
		return
	}
	if err := di.Restacker.Restack(c.Request.Context(), object); err != nil {
		c.JSON(http.StatusConflict, gin.H{errorKey: err.Error()})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"object": object})
}

func darkGapsRoute(c *gin.Context) {
	di, ok := depInjection(c)
	if !ok {
		return
	}
	gaps, err := coverage.Gaps(c.Request.Context(), di.AppStore.DB())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{errorKey: err.Error()})
		return
	}
	c.JSON(http.StatusOK, gaps)
}
