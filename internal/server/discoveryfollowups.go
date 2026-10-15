package server

import (
	"errors"
	"net/http"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveryfollowup"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/gin-gonic/gin"
)

// DiscoveryFollowup is a request to image a find's field again and what
// came of it.
type DiscoveryFollowup struct {
	ID          int        `json:"id"`
	Object      string     `json:"object"`
	Group       string     `json:"group"`
	Night       string     `json:"night"`
	Until       string     `json:"until"`
	Kind        string     `json:"kind"`
	Class       string     `json:"class"`
	RA          float64    `json:"ra"`
	Dec         float64    `json:"dec"`
	Filter      string     `json:"filter,omitempty"`
	Exposure    float64    `json:"exposure,omitempty"`
	Subs        int        `json:"subs,omitempty"`
	ProjectGUID string     `json:"project_guid"`
	Command     string     `json:"command,omitempty"`
	State       string     `json:"state"`
	Note        string     `json:"note"`
	FollowNight *string    `json:"follow_night"`
	FollowRunID *int       `json:"follow_run_id"`
	SeenID      *int       `json:"seen_candidate_id"`
	SeenSepArc  *float64   `json:"seen_sep_arcsec"`
	SeenMag     *float64   `json:"seen_mag"`
	CreatedAt   time.Time  `json:"created_at"`
	ClosedAt    *time.Time `json:"closed_at"`
}

func followupOf(f app.DiscoveryFollowup) DiscoveryFollowup {
	out := DiscoveryFollowup{ID: f.ID, Object: f.Object, Group: f.Group, Night: f.Night.Format(time.DateOnly), Until: f.Until.Format(time.DateOnly),
		Kind: f.Kind, Class: f.Class, RA: f.RA, Dec: f.Dec, Filter: f.Filter, Exposure: f.Exposure, Subs: f.Subs, ProjectGUID: f.ProjectGUID,
		Command: f.CreateCmd, State: f.State, Note: f.Note, FollowRunID: f.FollowRunID, SeenID: f.SeenID, SeenSepArc: f.SeenSepArc,
		SeenMag: f.SeenMag, CreatedAt: f.CreatedAt, ClosedAt: f.ClosedAt}
	if f.FollowNight != nil {
		n := f.FollowNight.Format(time.DateOnly)
		out.FollowNight = &n
	}
	return out
}

func applyFollowupRoutes(g *gin.RouterGroup) {
	g.GET("/discoveries/followups", discoveryFollowups)
	g.GET("/discoveries/candidates/:id/followup", candidateFollowup)
}

func discoveryFollowups(c *gin.Context) {
	db, ok := appDB(c)
	if !ok {
		return
	}
	q := db.Order("id DESC").Limit(discoveryListLimit)
	if n := c.Query("night"); n != "" {
		night, ok := parseNight(n)
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{errorKey: errNightFormat})
			return
		}
		q = q.Where("night = ?", night)
	}
	if o := c.Query("object"); o != "" {
		q = q.Where("object = ?", o)
	}
	if s := c.Query("state"); s != "" {
		q = q.Where("state = ?", s)
	}
	var rows []app.DiscoveryFollowup
	if err := q.Find(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{errorKey: err.Error()})
		return
	}
	out := make([]DiscoveryFollowup, 0, len(rows))
	for _, r := range rows {
		out = append(out, followupOf(r))
	}
	c.JSON(http.StatusOK, out)
}

func candidateFollowup(c *gin.Context) {
	db, ok := appDB(c)
	if !ok {
		return
	}
	cand, ok := candidateByID(c, db)
	if !ok {
		return
	}
	f, err := discoveryfollowup.ForCandidate(c.Request.Context(), db, cand)
	if errors.Is(err, discoveryfollowup.ErrNone) {
		c.JSON(http.StatusOK, gin.H{"followup": nil})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{errorKey: err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"followup": followupOf(*f)})
}
