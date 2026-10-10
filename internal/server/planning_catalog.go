package server

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/USA-RedDragon/astro-stacker/internal/catalog"
	"github.com/USA-RedDragon/astro-stacker/internal/catalog/match"
	"github.com/USA-RedDragon/astro-stacker/internal/config"
	"github.com/USA-RedDragon/astro-stacker/internal/planning"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

var (
	catalogOnce  sync.Once
	catalogIndex *catalog.Index
	catalogErr   error
)

func planningCatalog() (*catalog.Index, error) {
	catalogOnce.Do(func() { catalogIndex, catalogErr = catalog.LoadEmbedded() })
	return catalogIndex, catalogErr
}

type CatalogHit struct {
	Object     catalog.Object       `json:"object"`
	Score      float64              `json:"score,omitempty"`
	How        string               `json:"how,omitempty"`
	Catalogues []string             `json:"catalogues"`
	Fit        sky.Fit              `json:"fit"`
	InData     *match.ExistingMatch `json:"inData,omitempty"`
}

type CatalogObjectDetail struct {
	Object   catalog.Object        `json:"object"`
	Fit      sky.Fit               `json:"fit"`
	Frame    sky.Frame             `json:"frame"`
	FrameW   float64               `json:"frameWidthDeg"`
	FrameH   float64               `json:"frameHeightDeg"`
	Existing []match.ExistingMatch `json:"existing"`
}

func rigFrame(cfg *config.Config) sky.Frame {
	f := sky.Frame{FocalLength: 405, PixelSize: 3.76, WidthPx: 6248, HeightPx: 4176}
	if cfg != nil && cfg.Discover.FocalLength > 0 {
		f = sky.Frame{FocalLength: cfg.Discover.FocalLength, PixelSize: cfg.Discover.PixelSize, WidthPx: cfg.Discover.SensorWidth, HeightPx: cfg.Discover.SensorHeight}
	}
	return f
}

func cataloguesOf(o catalog.Object) []string {
	seen := map[string]bool{}
	var out []string
	for _, d := range append([]string{o.Designation}, o.Aliases...) {
		c, ok := match.NormalizeDesignation(d)
		if !ok {
			continue
		}
		prefix := strings.TrimRight(strings.SplitN(c, " ", 2)[0], "-0123456789.+")
		if prefix == "" || seen[prefix] {
			continue
		}
		seen[prefix] = true
		out = append(out, prefix)
	}
	return out
}

func existingData(ctx context.Context, s *planning.Snapshot, appDB *gorm.DB) ([]match.Existing, match.Decisions) {
	var out []match.Existing
	for _, p := range s.Projects {
		var ra, dec float64
		n := 0
		for _, t := range p.Targets {
			if t.RAHours != nil && t.Dec != nil {
				ra += *t.RAHours * 15
				dec += *t.Dec
				n++
			}
			e := match.Existing{Kind: match.KindTarget, ID: strconv.Itoa(t.ID), Name: t.Name}
			if t.RAHours != nil && t.Dec != nil {
				e.RA, e.Dec, e.HasCoords = *t.RAHours*15, *t.Dec, true
			}
			if len(p.Targets) > 1 || t.Name != p.Name {
				out = append(out, e)
			}
		}
		e := match.Existing{Kind: match.KindProject, ID: strconv.Itoa(p.ID), Name: p.Name}
		if n > 0 {
			e.RA, e.Dec, e.HasCoords = ra/float64(n), dec/float64(n), true
		}
		out = append(out, e)
	}
	var xrefs []app.ObjectXref
	if appDB != nil {
		var objects []string
		if err := appDB.WithContext(ctx).Model(&app.Stack{}).Distinct("object").Pluck("object", &objects).Error; err == nil {
			known := map[string]bool{}
			for _, p := range s.Projects {
				for _, t := range p.Targets {
					known[t.Name] = true
				}
			}
			for _, o := range objects {
				if !known[o] {
					out = append(out, match.Existing{Kind: match.KindStack, ID: o, Name: o})
				}
			}
		}
		_ = appDB.WithContext(ctx).Find(&xrefs).Error
	}
	for i := range out {
		out[i].Subject = out[i].SubjectKey()
	}
	return out, match.NewDecisions(xrefs)
}

func bestExisting(o catalog.Object, existing []match.Existing, dec match.Decisions) *match.ExistingMatch {
	ms := dec.ApplyExisting(o, existing, match.ExistingMatches(o, existing))
	if len(ms) == 0 {
		return nil
	}
	sort.SliceStable(ms, func(i, j int) bool { return ms[i].Confidence > ms[j].Confidence })
	m := ms[0]
	return &m
}

func applyPlanningCatalogRoutes(g *gin.RouterGroup) {
	g.GET("/planning/catalog/search", func(c *gin.Context) {
		ix, err := planningCatalog()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{errorKey: err.Error()})
			return
		}
		q := strings.TrimSpace(c.Query("q"))
		limit, _ := strconv.Atoi(c.Query("limit"))
		if limit <= 0 || limit > 100 {
			limit = 40
		}
		ctx := c.Request.Context()
		var hits []CatalogHit
		if ra, dec, ok := match.ParseCoordinates(q); ok {
			objs, err := ix.Cone(ctx, ra, dec, 1)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{errorKey: err.Error()})
				return
			}
			for i, o := range objs {
				if i >= limit {
					break
				}
				hits = append(hits, CatalogHit{Object: o, How: "coordinates"})
			}
		} else if q != "" {
			for _, m := range ix.Find(ctx, q, limit) {
				hits = append(hits, CatalogHit{Object: m.Object, Score: m.Score, How: m.How})
			}
		}
		di, ok := depInjection(c)
		if !ok {
			return
		}
		frame := rigFrame(di.Config)
		var existing []match.Existing
		var dec match.Decisions
		if s, ok := planningSnapshotQuiet(c); ok {
			var appDB *gorm.DB
			if di.AppStore != nil {
				appDB = di.AppStore.DB()
			}
			existing, dec = existingData(ctx, s, appDB)
		}
		for i := range hits {
			hits[i].Catalogues = cataloguesOf(hits[i].Object)
			hits[i].Fit = frame.Fit(hits[i].Object.MajorArcmin, hits[i].Object.MinorArcmin, sky.DefaultOverlap)
			hits[i].InData = bestExisting(hits[i].Object, existing, dec)
		}
		if hits == nil {
			hits = []CatalogHit{}
		}
		c.JSON(http.StatusOK, gin.H{"query": q, "results": hits, "frame": gin.H{"widthDeg": frame.WidthDeg(), "heightDeg": frame.HeightDeg(), "scale": frame.Scale()}})
	})
	g.GET("/planning/catalog/objects/:id", func(c *gin.Context) {
		ix, err := planningCatalog()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{errorKey: err.Error()})
			return
		}
		o, found := ix.Get(c.Param("id"))
		if !found {
			o, found = ix.Lookup(c.Param("id"))
		}
		if !found {
			c.JSON(http.StatusNotFound, gin.H{errorKey: "no such catalogue object"})
			return
		}
		di, ok := depInjection(c)
		if !ok {
			return
		}
		frame := rigFrame(di.Config)
		out := CatalogObjectDetail{Object: o, Frame: frame, FrameW: frame.WidthDeg(), FrameH: frame.HeightDeg(), Fit: frame.Fit(o.MajorArcmin, o.MinorArcmin, sky.DefaultOverlap), Existing: []match.ExistingMatch{}}
		if s, ok := planningSnapshotQuiet(c); ok {
			var appDB *gorm.DB
			if di.AppStore != nil {
				appDB = di.AppStore.DB()
			}
			existing, dec := existingData(c.Request.Context(), s, appDB)
			ms := dec.ApplyExisting(o, existing, match.ExistingMatches(o, existing))
			sort.SliceStable(ms, func(i, j int) bool { return ms[i].Confidence > ms[j].Confidence })
			if ms != nil {
				out.Existing = ms
			}
		}
		c.JSON(http.StatusOK, out)
	})
}

func planningSnapshotQuiet(c *gin.Context) (*planning.Snapshot, bool) {
	di, ok := depInjection(c)
	if !ok || di.SchedulerDBStore == nil {
		return nil, false
	}
	var appDB *gorm.DB
	if di.AppStore != nil {
		appDB = di.AppStore.DB()
	}
	s, err := planning.Load(c.Request.Context(), di.SchedulerDBStore.DB(), appDB, planning.Inputs{})
	if err != nil {
		return nil, false
	}
	return s, true
}
