package quality

import (
	"context"
	"fmt"
	"math"
	"strings"

	"gorm.io/gorm"
)

// Target Scheduler's grading status values.
const (
	GradingPending  = 0
	GradingAccepted = 1
	GradingRejected = 2
)

// SubScore is one acquired image's score and grading, keyed by file name.
type SubScore struct {
	File          string
	Filter        string
	Exposure      float64
	GradingStatus int
	// Score is the weight relative to the best tenth of subs for the same
	// filter and exposure across all targets, capped at 1. 0 when the
	// metadata can't be scored.
	Score float64
	// TargetBest is the best Score among its target's subs in the same
	// filter, so a target imaged only on poor nights can be judged against
	// what it has.
	TargetBest float64
	// HFR and Stars are NINA's star measurements, used to pick a sharp
	// registration reference.
	HFR          float64
	Stars        int
	Eccentricity float64
}

type group struct {
	filter   string
	exposure float64
}

type row struct {
	GradingStatus int
	Target        string
	Metadata      string
}

// Measured is a sub without a Target Scheduler record, measured from its
// pixels (ADU median and half-flux radius, as NINA measures them).
type Measured struct {
	File     string
	Target   string
	Filter   string
	Exposure float64
	SkyADU   float64
	Offset   float64 // camera offset, 0 when unknown
	// Calibrated subs came calibrated (Telescope.live), their pedestal
	// already taken off.
	Calibrated bool
	HFR        float64
	Stars      int
}

// LoadScores reads every acquired image from the scheduler database and
// scores it the same way astro-processing does.
//
// measured subs, which Target Scheduler has no record of, are scored in the
// same groups from their own measurements.
func LoadScores(ctx context.Context, db *gorm.DB, pedestal float64, measured []Measured) (map[string]SubScore, error) {
	var rows []row
	if err := db.WithContext(ctx).Table("acquiredimage").
		Select(`acquiredimage."gradingStatus" as grading_status, target.name as target, acquiredimage.metadata`).
		Joins(`LEFT JOIN target ON target."Id" = acquiredimage."targetId"`).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("load acquired images: %w", err)
	}

	type item struct {
		s      SubScore
		raw    float64
		g      group
		target string
	}
	items := make([]item, 0, len(rows))
	byGroup := map[group][]float64{}
	for _, r := range rows {
		m, err := ParseMetadata(r.Metadata)
		if err != nil || m.FileName == "" {
			continue
		}
		g := group{filter: m.FilterName, exposure: math.Round(float64(m.ExposureDuration))}
		raw := RawWeight(Sky(float64(m.ADUMedian), PedestalAt(pedestal, float64(m.Offset))), float64(m.HFR))
		it := item{
			s: SubScore{
				File:          m.FileName[strings.LastIndexAny(m.FileName, `\/`)+1:],
				Filter:        g.filter,
				Exposure:      g.exposure,
				GradingStatus: r.GradingStatus,
				HFR:           float64(m.HFR),
				Stars:         int(m.DetectedStars),
				Eccentricity:  float64(m.Eccentricity),
			},
			raw:    raw,
			g:      g,
			target: r.Target,
		}
		items = append(items, it)
		if r.GradingStatus != GradingRejected {
			byGroup[g] = append(byGroup[g], raw)
		}
	}

	recorded := make(map[string]bool, len(items))
	for _, it := range items {
		recorded[it.s.File] = true
	}
	for _, m := range measured {
		if recorded[m.File] {
			continue
		}
		g := group{filter: m.Filter, exposure: math.Round(m.Exposure)}
		ped := PedestalAt(pedestal, m.Offset)
		if m.Calibrated {
			ped = 0
		}
		raw := RawWeight(Sky(m.SkyADU, ped), m.HFR)
		items = append(items, item{
			s:      SubScore{File: m.File, Filter: g.filter, Exposure: g.exposure, GradingStatus: GradingPending, HFR: m.HFR, Stars: m.Stars},
			raw:    raw,
			g:      g,
			target: m.Target,
		})
		byGroup[g] = append(byGroup[g], raw)
	}

	refs := make(map[group]float64, len(byGroup))
	for g, ws := range byGroup {
		refs[g] = Reference(ws)
	}
	type targetFilter struct {
		target string
		filter string
	}
	best := map[targetFilter]float64{}
	for i := range items {
		it := &items[i]
		if it.s.GradingStatus != GradingRejected {
			it.s.Score = Score(it.raw, refs[it.g])
			k := targetFilter{it.target, it.s.Filter}
			best[k] = math.Max(best[k], it.s.Score)
		}
	}
	out := make(map[string]SubScore, len(items))
	for _, it := range items {
		it.s.TargetBest = best[targetFilter{it.target, it.s.Filter}]
		out[it.s.File] = it.s
	}
	return out, nil
}
