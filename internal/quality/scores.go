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
}

type group struct {
	filter   string
	exposure float64
}

type row struct {
	GradingStatus int
	Metadata      string
}

// LoadScores reads every acquired image from the scheduler database and
// scores it the same way astro-processing does.
func LoadScores(ctx context.Context, db *gorm.DB, pedestal float64) (map[string]SubScore, error) {
	var rows []row
	if err := db.WithContext(ctx).Table("acquiredimage").
		Select(`"gradingStatus" as grading_status, metadata`).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("load acquired images: %w", err)
	}

	type item struct {
		s   SubScore
		raw float64
		g   group
	}
	items := make([]item, 0, len(rows))
	byGroup := map[group][]float64{}
	for _, r := range rows {
		m, err := ParseMetadata(r.Metadata)
		if err != nil || m.FileName == "" {
			continue
		}
		g := group{filter: m.FilterName, exposure: math.Round(float64(m.ExposureDuration))}
		raw := RawWeight(Sky(float64(m.ADUMedian), pedestal), float64(m.HFR))
		it := item{
			s: SubScore{
				File:          m.FileName[strings.LastIndexAny(m.FileName, `\/`)+1:],
				Filter:        g.filter,
				Exposure:      g.exposure,
				GradingStatus: r.GradingStatus,
			},
			raw: raw,
			g:   g,
		}
		items = append(items, it)
		if r.GradingStatus != GradingRejected {
			byGroup[g] = append(byGroup[g], raw)
		}
	}

	refs := make(map[group]float64, len(byGroup))
	for g, ws := range byGroup {
		refs[g] = Reference(ws)
	}
	out := make(map[string]SubScore, len(items))
	for _, it := range items {
		if it.s.GradingStatus != GradingRejected {
			it.s.Score = Score(it.raw, refs[it.g])
		}
		out[it.s.File] = it.s
	}
	return out, nil
}
