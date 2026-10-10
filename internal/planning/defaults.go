package planning

import (
	"slices"

	"github.com/USA-RedDragon/astro-stacker/internal/frameheader"
	"github.com/USA-RedDragon/astro-stacker/internal/goals"
	"github.com/USA-RedDragon/astro-stacker/internal/mosaics"
)

type FilterDepth struct {
	Filter string  `json:"filter"`
	Depth  float64 `json:"depth"`
}

type GoalDefaults struct {
	Kind               goals.Kind    `json:"kind"`
	SNR                float64       `json:"snr"`
	DepthSNR           float64       `json:"depthSnr"`
	Depths             []FilterDepth `json:"depths"`
	PlateauStop        bool          `json:"plateauStop"`
	PlateauGainPct     float64       `json:"plateauGainPct"`
	BandLowPercentile  float64       `json:"bandLowPercentile"`
	BandHighPercentile float64       `json:"bandHighPercentile"`
}

type Defaults struct {
	Goal               GoalDefaults `json:"goal"`
	PanelDeficitWeight float64      `json:"panelDeficitWeight"`
}

func defaultsFor(templates []Template) Defaults {
	g := goals.DefaultGoal("")
	d := Defaults{
		Goal: GoalDefaults{Kind: g.Kind, SNR: g.SNR, DepthSNR: goals.DepthSNR, Depths: []FilterDepth{}, PlateauStop: g.PlateauStop,
			PlateauGainPct: goals.PlateauGainPct, BandLowPercentile: goals.BandLowPercentile, BandHighPercentile: goals.BandHighPercent},
		PanelDeficitWeight: mosaics.PanelDeficitOnWeight,
	}
	for _, t := range templates {
		f := frameheader.NormalizeFilter(t.Filter)
		if f == "" || slices.ContainsFunc(d.Goal.Depths, func(x FilterDepth) bool { return x.Filter == f }) {
			continue
		}
		d.Goal.Depths = append(d.Goal.Depths, FilterDepth{Filter: f, Depth: goals.DefaultDepth(f)})
	}
	return d
}
