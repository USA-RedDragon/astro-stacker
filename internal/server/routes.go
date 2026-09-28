package server

import (
	"fmt"
	"net/http"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/coverage"
	"github.com/USA-RedDragon/astro-stacker/internal/events"
	"github.com/USA-RedDragon/astro-stacker/internal/previewer"
	"github.com/USA-RedDragon/astro-stacker/internal/server/middleware"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/gin-gonic/gin"
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
}

// monoFilterOrder ranks filters by how well one shows a target on its own:
// nebulae are faint in blue and green but bright in H-a.
var monoFilterOrder = []string{"H-a", "O-III", "S-II", "Luminance", "Red", "Green", "Blue"}

// monoCovers picks each object's single-filter cover: the highest-ranked
// filter with at least a quarter of its best filter's effective exposure,
// so a thin H-a master doesn't beat a deep one in another filter.
func monoCovers(stacks []app.Stack) []app.Stack {
	best := map[string]float64{}
	for _, s := range stacks {
		best[s.Object] = max(best[s.Object], s.EffectiveSeconds)
	}
	rank := func(f string) int {
		if i := slices.Index(monoFilterOrder, f); i >= 0 {
			return i
		}
		return len(monoFilterOrder)
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
	// Palette is set for a colour composite (RGB+Ha, RGB, SHO, HOO);
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

	// One preview per target and per mosaic project, for dashboard cards: a
	// colour composite where the filters allow one, else the master with the most effective exposure, or the
	// mosaic with the most panels.
	r.GET("/covers", func(c *gin.Context) {
		out := Covers{Objects: map[string]Cover{}, Mosaics: map[string]Cover{}}
		if signer == nil {
			c.JSON(http.StatusOK, out)
			return
		}
		ctx := c.Request.Context()
		db := c.MustGet(middleware.DepInjectionKey).(*middleware.DepInjection).AppStore.DB().WithContext(ctx)
		var stacks []app.Stack
		if err := db.Where("subs > 0 AND preview_key IS NOT NULL").Order("effective_seconds DESC").Find(&stacks).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		// Colour covers first; mono masters fill in the rest.
		var covers []app.Cover
		if err := db.Find(&covers).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		for _, cv := range covers {
			u, err := signer.URL(ctx, cv.PreviewKey)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
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
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			out.Objects[s.Object] = Cover{Filter: s.Filter, PreviewURL: u, EffectiveSeconds: s.EffectiveSeconds, UpdatedAt: s.UpdatedAt}
		}
		var mosaics []app.Mosaic
		if err := db.Where("preview_key IS NOT NULL").Order("panels DESC, updated_at DESC").Find(&mosaics).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		for _, m := range mosaics {
			if _, ok := out.Mosaics[m.Project]; ok {
				continue
			}
			u, err := signer.URL(ctx, *m.PreviewKey)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			out.Mosaics[m.Project] = Cover{Filter: m.Filter, PreviewURL: u, UpdatedAt: m.UpdatedAt}
		}
		c.JSON(http.StatusOK, out)
	})

	// Mosaics for one project, one per filter.
	r.GET("/mosaics", func(c *gin.Context) {
		if signer == nil {
			c.JSON(http.StatusOK, []Mosaic{})
			return
		}
		project := c.Query("project")
		if project == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "project is required"})
			return
		}
		di := c.MustGet(middleware.DepInjectionKey).(*middleware.DepInjection)
		var mosaics []app.Mosaic
		if err := di.AppStore.DB().WithContext(c.Request.Context()).
			Where("project = ? AND master_key IS NOT NULL", project).Order("filter").Find(&mosaics).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
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
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			out = append(out, o)
		}
		c.JSON(http.StatusOK, out)
	})

	// Stacked masters for one target, one per filter.
	r.GET("/stacks", func(c *gin.Context) {
		if signer == nil {
			c.JSON(http.StatusOK, []Master{})
			return
		}
		object := c.Query("object")
		if object == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "object is required"})
			return
		}
		di := c.MustGet(middleware.DepInjectionKey).(*middleware.DepInjection)
		var stacks []app.Stack
		if err := di.AppStore.DB().WithContext(c.Request.Context()).
			Where("object = ? AND subs > 0 AND master_key IS NOT NULL", object).
			Order("filter").Find(&stacks).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		out := make([]Master, 0, len(stacks))
		for _, s := range stacks {
			m := Master{
				Filter: s.Filter, Subs: s.Subs, ExposureSeconds: s.ExposureSeconds, EffectiveSeconds: s.EffectiveSeconds,
				Width: s.Width, Height: s.Height, UpdatedAt: s.UpdatedAt,
				Crop: cropOf(s.CropX, s.CropY, s.CropW, s.CropH, s.Width, s.Height),
			}
			ctx := c.Request.Context()
			var err error
			name := fmt.Sprintf("%s_%s_master.fit", strings.ReplaceAll(s.Object, " ", "_"), strings.ReplaceAll(s.Filter, " ", "_"))
			if m.MasterURL, err = signer.DownloadURL(ctx, *s.MasterKey, name); err == nil && s.PreviewKey != nil {
				m.PreviewURL, err = signer.URL(ctx, *s.PreviewKey)
			}
			if err == nil && s.LinearKey != nil {
				m.LinearURL, err = signer.URL(ctx, *s.LinearKey)
			}
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			out = append(out, m)
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
