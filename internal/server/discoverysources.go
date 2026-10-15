package server

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoverysource"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	sourceListLimit = 200
	sourceMaxLimit  = 2000
	sourceDays      = 30
)

// DiscoverySpotNight is one night of a source: its candidates of that
// night, one per filter group, the one that matters most first.
type DiscoverySpotNight struct {
	Night      string               `json:"night"`
	Candidates []DiscoveryCandidate `json:"candidates"`
}

// DiscoverySpot is one spot that changed, across every night it changed
// on, or one moving object's track.
type DiscoverySpot struct {
	ID       int                  `json:"id"`
	Object   string               `json:"object"`
	Kind     string               `json:"kind"`
	Class    string               `json:"class"`
	RA       float64              `json:"ra"`
	Dec      float64              `json:"dec"`
	First    string               `json:"first_seen"`
	Last     string               `json:"last_seen"`
	Seen     int                  `json:"nights_seen"`
	Compared int                  `json:"nights_compared"`
	Look     bool                 `json:"needs_look"`
	Vetting  string               `json:"vetting"`
	VetNote  string               `json:"vet_note"`
	VettedAt *time.Time           `json:"vetted_at"`
	Best     DiscoveryCandidate   `json:"best"`
	Nights   []DiscoverySpotNight `json:"nights"`
	Checked  []string             `json:"checked"`
	Failed   []string             `json:"failed"`
}

// DiscoverySpotList is a page of the inbox and how many sources each
// view holds.
type DiscoverySpotList struct {
	Sources []DiscoverySpot        `json:"sources"`
	Total   int                    `json:"total"`
	Counts  discoverysource.Counts `json:"counts"`
	Since   *string                `json:"since"`
}

func sourceOf(s discoverysource.Summary) DiscoverySpot {
	out := DiscoverySpot{ID: s.Source.ID, Object: s.Source.Object, Kind: s.Source.Kind, Class: s.Class, RA: s.Best.RA, Dec: s.Best.Dec,
		First: s.First.Format(time.DateOnly), Last: s.Last.Format(time.DateOnly), Seen: s.Seen, Compared: s.Compared, Look: s.Look(),
		Vetting: s.Source.Vetting, VetNote: s.Source.VetNote, VettedAt: s.Source.VettedAt, Best: candidateOf(s.Best, false),
		Nights: []DiscoverySpotNight{}, Checked: s.Checked, Failed: s.Failed}
	if out.Checked == nil {
		out.Checked = []string{}
	}
	if out.Failed == nil {
		out.Failed = []string{}
	}
	for _, m := range s.Members {
		n := m.Night.Format(time.DateOnly)
		if len(out.Nights) == 0 || out.Nights[len(out.Nights)-1].Night != n {
			out.Nights = append(out.Nights, DiscoverySpotNight{Night: n})
		}
		last := &out.Nights[len(out.Nights)-1]
		last.Candidates = append(last.Candidates, candidateOf(m, false))
	}
	return out
}

func applySourceRoutes(g *gin.RouterGroup) {
	g.GET("/discoveries/sources", discoverySources)
	g.GET("/discoveries/sources/:id", discoverySourceDetail)
	g.PUT("/discoveries/sources/:id/vetting", vetDiscoverySource)
}

func sourceFilter(c *gin.Context, now time.Time) (discoverysource.Filter, string, bool) {
	f := discoverysource.Filter{Object: c.Query("object"), Search: strings.TrimSpace(c.Query("q")), View: c.Query("view")}
	if f.View != "" && !slices.Contains(discoverysource.Views(), f.View) {
		return f, "view must be one of " + strings.Join(discoverysource.Views(), ", "), false
	}
	if n := c.Query("night"); n != "" {
		t, ok := parseNight(n)
		if !ok {
			return f, errNightFormat, false
		}
		f.Night = &t
	}
	if u := c.Query("until"); u != "" {
		t, ok := parseNight(u)
		if !ok {
			return f, "until must be YYYY-MM-DD", false
		}
		f.Until = &t
	}
	switch s := c.Query("since"); s {
	case "all":
	case "":
		if f.Night == nil {
			d := now.UTC().AddDate(0, 0, -sourceDays)
			t := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.UTC)
			f.Since = &t
		}
	default:
		t, ok := parseNight(s)
		if !ok {
			return f, "since must be YYYY-MM-DD or all", false
		}
		f.Since = &t
	}
	return f, "", true
}

func discoverySources(c *gin.Context) {
	db, ok := appDB(c)
	if !ok {
		return
	}
	f, msg, ok := sourceFilter(c, time.Now())
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{errorKey: msg})
		return
	}
	limit := sourceListLimit
	if s := c.Query("limit"); s != "" {
		if v, err := strconv.Atoi(s); err == nil && v > 0 {
			limit = min(v, sourceMaxLimit)
		}
	}
	list, counts, err := discoverysource.List(c.Request.Context(), db, f)
	if err != nil {
		serverError(c, err)
		return
	}
	out := DiscoverySpotList{Sources: []DiscoverySpot{}, Total: len(list), Counts: counts}
	if f.Since != nil {
		s := f.Since.Format(time.DateOnly)
		out.Since = &s
	}
	for _, s := range list[:min(limit, len(list))] {
		out.Sources = append(out.Sources, sourceOf(s))
	}
	c.JSON(http.StatusOK, out)
}

func sourceID(c *gin.Context) (int, bool) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{errorKey: errIDNumber})
		return 0, false
	}
	return id, true
}

func discoverySourceDetail(c *gin.Context) {
	db, ok := appDB(c)
	if !ok {
		return
	}
	id, ok := sourceID(c)
	if !ok {
		return
	}
	s, err := discoverysource.Get(c.Request.Context(), db, id)
	if err != nil {
		sourceError(c, err)
		return
	}
	c.JSON(http.StatusOK, sourceOf(s))
}

func sourceError(c *gin.Context, err error) {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, gin.H{errorKey: "no such source"})
		return
	}
	serverError(c, err)
}

type vetBody struct {
	State string `json:"state"`
	Note  string `json:"note"`
}

func readVetting(c *gin.Context) (vetBody, *time.Time, bool) {
	var body vetBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{errorKey: err.Error()})
		return body, nil, false
	}
	if body.State != "" && !validVetting(body.State) {
		c.JSON(http.StatusBadRequest, gin.H{errorKey: "state must be real, artifact, known, unsure or empty"})
		return body, nil, false
	}
	body.Note = strings.TrimSpace(body.Note)
	if body.State == "" {
		return body, nil, true
	}
	now := time.Now().UTC()
	return body, &now, true
}

func vetDiscoverySource(c *gin.Context) {
	db, ok := appDB(c)
	if !ok {
		return
	}
	id, ok := sourceID(c)
	if !ok {
		return
	}
	body, at, ok := readVetting(c)
	if !ok {
		return
	}
	if _, err := discoverysource.Get(c.Request.Context(), db, id); err != nil {
		sourceError(c, err)
		return
	}
	if _, err := discoverysource.Vet(c.Request.Context(), db, id, body.State, body.Note, at); err != nil {
		sourceError(c, err)
		return
	}
	s, err := discoverysource.Get(c.Request.Context(), db, id)
	if err != nil {
		sourceError(c, err)
		return
	}
	c.JSON(http.StatusOK, sourceOf(s))
}

// vetCandidate vets the source a candidate belongs to, so every night of
// the spot carries the same vetting; a candidate with no source is vetted
// alone.
func vetCandidate(c *gin.Context, db *gorm.DB, cand *app.DiscoveryCandidate, body vetBody, at *time.Time) error {
	id, err := discoverysource.SourceOf(c.Request.Context(), db, cand.ID)
	if err == nil {
		_, err = discoverysource.Vet(c.Request.Context(), db, id, body.State, body.Note, at)
		cand.SourceID = &id
	} else if errors.Is(err, gorm.ErrRecordNotFound) {
		err = db.Model(cand).Updates(discoverysource.VetFields(body.State, body.Note, at)).Error
	}
	cand.Vetting, cand.VetNote, cand.VettedAt = body.State, body.Note, at
	return err
}
