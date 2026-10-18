package minima

import "math"

// ScanRow is the dip fitted with its middle Offset days from the
// prediction, each filter on a flat normal level: the filters' mean
// depth, its error, the chi-square and how many points fall in the dip.
type ScanRow struct {
	Offset float64 `json:"offset"`
	Depth  float64 `json:"depth"`
	Err    float64 `json:"err"`
	Chi    float64 `json:"chi"`
	InDip  int     `json:"in_dip"`
}

// Scan fits the dip with its middle stepped every step days over the
// points' whole span, for a look at where in a night the dip lies.
func Scan(pts []Point, d Dip, step float64) []ScanRow {
	use, ng := gather(pts, d.Mid, math.Inf(1))
	if len(use) == 0 || ng == 0 || !(step > 0) || d.Shape == nil {
		return nil
	}
	var out []ScanRow
	for tau := use[0].T - d.Mid; tau <= use[len(use)-1].T-d.Mid; tau += step {
		betas, vds, chi, ok := solveAt(use, ng, d, tau, false)
		if !ok {
			continue
		}
		dep, e, ok := meanDepth(betas, vds, 1)
		if !ok {
			continue
		}
		n := 0
		for _, p := range use {
			if d.Shape(p.T-d.Mid-tau) > inDip {
				n++
			}
		}
		out = append(out, ScanRow{Offset: tau, Depth: dep, Err: e, Chi: chi, InDip: n})
	}
	return out
}
