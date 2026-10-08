package server

import (
	"context"
	"net/http"
	"path"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Photometry states of a light, for its transparency.
const (
	PhotometryPending  = "pending"  // not measured yet; scored from the sky until it is
	PhotometryMeasured = "measured" // its stars' photometry is in its score
	PhotometryFailed   = "failed"   // couldn't be measured; scored from the sky
)

// Sub is what the stacker made of one light: its status, score and weight
// as stack_frames records them. Status is empty for a light the stacker
// hasn't processed yet.
type Sub struct {
	File     string     `json:"file"`
	Filter   string     `json:"filter"`
	Exposure *float64   `json:"exposure,omitempty"`
	DateObs  *time.Time `json:"date_obs,omitempty"`
	Status   string     `json:"status,omitempty"`
	// Score is the sub's quality against good conditions, transparency
	// included; Weight is what it adds to its master, Score × exposure.
	Score       float64    `json:"score"`
	Weight      float64    `json:"weight"`
	Error       string     `json:"error,omitempty"`
	ProcessedAt *time.Time `json:"processed_at,omitempty"`
	Photometry  string     `json:"photometry"`
}

// objectSubs lists one target's lights with the stacker's verdict on each.
func objectSubs(ctx context.Context, db *gorm.DB, object string) ([]Sub, error) {
	var rows []struct {
		Key           string
		Filter        string
		Exposure      *float64
		DateObs       *time.Time
		PhotometryRev *int
		Photometry    *string
		Status        *string
		Score         *float64
		Weight        *float64
		Error         *string
		ProcessedAt   *time.Time
	}
	if err := db.WithContext(ctx).Table("frames f").
		Select("f.key, f.filter, f.exposure, f.date_obs, f.photometry_rev, f.photometry, "+
			"sf.status, sf.score, sf.weight, sf.error, sf.processed_at").
		Joins("LEFT JOIN stack_frames sf ON sf.frame_id = f.id").
		Where("f.object = ? AND f.type = ? AND f.index_error IS NULL", object, lightType).
		Order("f.date_obs, f.key").Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]Sub, 0, len(rows))
	for _, r := range rows {
		s := Sub{File: path.Base(r.Key), Filter: r.Filter, Exposure: r.Exposure, DateObs: r.DateObs, Photometry: PhotometryPending}
		switch {
		case r.PhotometryRev != nil && r.Photometry != nil:
			s.Photometry = PhotometryMeasured
		case r.PhotometryRev != nil:
			s.Photometry = PhotometryFailed
		}
		if r.Status != nil {
			s.Status = *r.Status
			s.ProcessedAt = r.ProcessedAt
			if r.Score != nil {
				s.Score = *r.Score
			}
			if r.Weight != nil {
				s.Weight = *r.Weight
			}
			if r.Error != nil {
				s.Error = *r.Error
			}
		}
		out = append(out, s)
	}
	return out, nil
}

// subsRoute lists one target's lights with their stack status, score and
// weight, so astro-processing shows exactly what the stacker decided.
func subsRoute(c *gin.Context) {
	object := c.Query("object")
	if object == "" {
		c.JSON(http.StatusBadRequest, gin.H{errorKey: objectRequired})
		return
	}
	di, ok := depInjection(c)
	if !ok {
		return
	}
	subs, err := objectSubs(c.Request.Context(), di.AppStore.DB(), object)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{errorKey: err.Error()})
		return
	}
	c.JSON(http.StatusOK, subs)
}
