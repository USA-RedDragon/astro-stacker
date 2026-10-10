package server

import (
	"context"
	"log/slog"
	"math"
	"net/http"
	"path"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/quality"
	"github.com/USA-RedDragon/astro-stacker/internal/server/middleware"
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
// as stack_frames records them. Status is empty, and Score and Weight nil,
// for a light the stacker hasn't processed yet.
type Sub struct {
	File        string     `json:"file"`
	Filter      string     `json:"filter"`
	Exposure    *float64   `json:"exposure,omitempty"`
	DateObs     *time.Time `json:"date_obs,omitempty"`
	Status      string     `json:"status,omitempty"`
	Score       *float64   `json:"score"`
	Weight      *float64   `json:"weight"`
	Error       string     `json:"error,omitempty"`
	ProcessedAt *time.Time `json:"processed_at,omitempty"`
	Photometry  string     `json:"photometry"`
	Scoring     *Scoring   `json:"scoring,omitempty"`
	NoScoring   string     `json:"no_scoring,omitempty"`
}

type Scoring struct {
	MinScore            float64  `json:"min_score"`
	TargetBest          *float64 `json:"target_best"`
	Cut                 *float64 `json:"cut"`
	ReferenceWeight     *float64 `json:"reference_weight"`
	ReferenceSubs       int      `json:"reference_subs"`
	ReferencePercentile float64  `json:"reference_percentile"`
	Transparency        *float64 `json:"transparency"`
	TransparencySource  string   `json:"transparency_source,omitempty"`
	TransparencyMissing string   `json:"transparency_missing,omitempty"`
	PedestalADU         *float64 `json:"pedestal_adu"`
	PedestalSource      string   `json:"pedestal_source"`
	PedestalBasis       string   `json:"pedestal_basis"`
	SkyADU              *float64 `json:"sky_adu"`
	Unmeasured          string   `json:"unmeasured,omitempty"`
}

func finitePtr(v float64) *float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	return &v
}

func scoringOf(s quality.SubScore, minScore float64) *Scoring {
	out := &Scoring{
		MinScore:            minScore,
		ReferenceWeight:     finitePtr(s.Reference),
		ReferenceSubs:       s.ReferenceSubs,
		ReferencePercentile: quality.ReferencePercentile,
		TransparencySource:  s.TransparencySource,
		TransparencyMissing: s.TransparencyMissing,
		PedestalSource:      s.Pedestal.Source,
		PedestalBasis:       s.Pedestal.Basis,
		SkyADU:              finitePtr(s.Sky),
		Unmeasured:          s.Missing,
	}
	if s.Pedestal.Source != quality.PedestalCalibrated {
		out.PedestalADU = finitePtr(s.Pedestal.ADU)
	}
	if s.TransparencySource != "" {
		out.Transparency = finitePtr(s.Transparency)
	}
	if s.GradingStatus != quality.GradingRejected || s.StackerRejected {
		out.TargetBest = finitePtr(s.TargetBest)
		out.Cut = finitePtr(minScore * s.TargetBest)
	}
	return out
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
			s.Score, s.Weight = r.Score, r.Weight
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
	withScoring(c.Request.Context(), subs, di.Restacker, di.Config.Stacking.MinScore)
	c.JSON(http.StatusOK, subs)
}

func withScoring(ctx context.Context, subs []Sub, scorer middleware.Restacker, minScore float64) {
	var scores map[string]quality.SubScore
	why := "stacking is off"
	if scorer != nil {
		var err error
		if scores, err = scorer.Scores(ctx); err != nil {
			slog.Warn("Could not score subs", "error", err)
			why = "could not score subs: " + err.Error()
		}
	}
	for i := range subs {
		s, ok := scores[subs[i].File]
		switch {
		case ok:
			subs[i].Scoring = scoringOf(s, minScore)
		case scores != nil:
			subs[i].NoScoring = "no Target Scheduler record and no measurement of its pixels yet"
		default:
			subs[i].NoScoring = why
		}
	}
}
