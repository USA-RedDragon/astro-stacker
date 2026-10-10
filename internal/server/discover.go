package server

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/USA-RedDragon/astro-stacker/internal/catalog"
	"github.com/USA-RedDragon/astro-stacker/internal/discover"
	"github.com/gin-gonic/gin"
)

const searchConeRadius = 1.0

func floatQuery(c *gin.Context, name string) (float64, bool) {
	v := c.Query(name)
	if v == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(v, 64)
	return f, err == nil
}

func listQuery(c *gin.Context, name string) []string {
	var out []string
	for p := range strings.SplitSeq(c.Query(name), ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func discoverError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, discover.ErrNotFound), errors.Is(err, discover.ErrUnknownList):
		c.JSON(http.StatusNotFound, gin.H{errorKey: err.Error()})
	case errors.Is(err, discover.ErrBadQuery):
		c.JSON(http.StatusBadRequest, gin.H{errorKey: err.Error()})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{errorKey: err.Error()})
	}
}

func applyDiscoverRoutes(g *gin.RouterGroup, x Extras) {
	d := x.Discover
	if d == nil {
		off := func(c *gin.Context) {
			c.JSON(http.StatusServiceUnavailable, gin.H{errorKey: "the catalogue store is not loaded"})
		}
		for _, p := range []string{"/catalog/*rest", "/catalogues", "/catalogues/:key", "/finder", "/collabs"} {
			g.GET(p, off)
		}
		return
	}
	g.GET("/catalog/search", func(c *gin.Context) {
		limit, _ := strconv.Atoi(c.Query("limit"))
		q := c.Query("q")
		if ra, dec, ok := catalog.ParseCoordinates(q); ok {
			c.JSON(http.StatusOK, d.Catalog.Near(c.Request.Context(), ra, dec, searchConeRadius, limit))
			return
		}
		out := d.Catalog.Find(c.Request.Context(), q, limit)
		if out == nil {
			out = []catalog.Match{}
		}
		c.JSON(http.StatusOK, out)
	})
	g.GET("/catalog/cone", func(c *gin.Context) {
		ra, ok1 := floatQuery(c, "ra")
		dec, ok2 := floatQuery(c, "dec")
		radius, ok3 := floatQuery(c, "radius")
		if !ok1 || !ok2 || !ok3 || radius <= 0 || radius > 20 {
			c.JSON(http.StatusBadRequest, gin.H{errorKey: "ra, dec and radius (degrees, at most 20) are required"})
			return
		}
		out, err := d.Catalog.Cone(c.Request.Context(), ra, dec, radius)
		if err != nil {
			discoverError(c, err)
			return
		}
		c.JSON(http.StatusOK, out)
	})
	g.GET("/catalog/sources", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"sources": d.Catalog.Sources(), "lists": d.Catalog.Lists()})
	})
	g.GET("/catalog/objects/:id", func(c *gin.Context) {
		out, err := d.Object(c.Request.Context(), c.Param("id"))
		if err != nil {
			discoverError(c, err)
			return
		}
		c.JSON(http.StatusOK, out)
	})
	g.GET("/catalog/resolve", func(c *gin.Context) {
		var pos *discover.Position
		ra, ok1 := floatQuery(c, "ra")
		dec, ok2 := floatQuery(c, "dec")
		if ok1 && ok2 {
			pos = &discover.Position{RA: ra, Dec: dec}
		}
		radius, _ := floatQuery(c, "radius")
		out, err := d.Resolve(c.Request.Context(), c.Query("name"), pos, radius)
		if err != nil {
			discoverError(c, err)
			return
		}
		c.JSON(http.StatusOK, out)
	})
	g.GET("/catalog/matches", func(c *gin.Context) {
		out, err := d.Review(c.Request.Context(), c.Query("decided") != "")
		if err != nil {
			discoverError(c, err)
			return
		}
		c.JSON(http.StatusOK, out)
	})
	g.GET("/catalog/subjects", func(c *gin.Context) {
		out, err := d.Subjects(c.Request.Context())
		if err != nil {
			discoverError(c, err)
			return
		}
		c.JSON(http.StatusOK, out)
	})
	g.GET("/catalog/subjects/links", func(c *gin.Context) {
		out, err := d.Links(c.Request.Context(), c.Query("subject"))
		if err != nil {
			discoverError(c, err)
			return
		}
		if out == nil {
			out = []discover.Link{}
		}
		c.JSON(http.StatusOK, out)
	})
	g.GET("/catalogues", func(c *gin.Context) {
		out, err := d.Overview(c.Request.Context())
		if err != nil {
			discoverError(c, err)
			return
		}
		c.JSON(http.StatusOK, out)
	})
	g.GET("/catalogues/:key", func(c *gin.Context) {
		out, err := d.Catalogue(c.Request.Context(), c.Param("key"))
		if err != nil {
			discoverError(c, err)
			return
		}
		c.JSON(http.StatusOK, out)
	})
	g.GET("/finder", func(c *gin.Context) {
		q := discover.FinderQuery{Fits: listQuery(c, "fit"), Types: listQuery(c, "types"), Sort: c.Query("sort"), Imaged: c.Query("imaged") == "show"}
		for _, m := range listQuery(c, "months") {
			if n, err := strconv.Atoi(m); err == nil && n >= 1 && n <= 12 {
				q.Months = append(q.Months, n)
			}
		}
		q.MinFill, _ = floatQuery(c, "minFill")
		q.Limit, _ = strconv.Atoi(c.Query("limit"))
		out, err := d.Finder(c.Request.Context(), q)
		if err != nil {
			discoverError(c, err)
			return
		}
		c.JSON(http.StatusOK, out)
	})
	g.GET("/collabs", func(c *gin.Context) {
		var src discover.CollabSource
		if x.Collabs != nil {
			src = x.Collabs
		}
		out, err := d.Collabs(c.Request.Context(), src)
		if err != nil {
			discoverError(c, err)
			return
		}
		c.JSON(http.StatusOK, out)
	})
}
