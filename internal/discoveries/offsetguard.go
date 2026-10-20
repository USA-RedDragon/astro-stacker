package discoveries

import (
	"fmt"
	"math"
)

const (
	// offsetReachPx is how far from a Gaia star a change counts as its
	// residual, past gaiaMatchPx so a lobe off the star is caught too.
	offsetReachPx = 3.0
	// offsetIsolatedPx keeps stars with no other within it.
	offsetIsolatedPx = 6.0
	// offsetMinChanges is the fewest changes on stars in a kernel tile that
	// can show a systematic offset.
	offsetMinChanges = 8
	// maxOffsetPx is the largest median offset of a tile's changes from
	// their stars: real changes on stars sit on them, the lobes of a
	// difference whose images are shifted sit to one side, all the same way.
	maxOffsetPx = 0.75
	// offsetReason is the rejection reason of a change cut by offsetGuard.
	offsetReason = "in a tile whose changes sit off their Gaia stars"
)

// OffsetTile is a kernel tile whose changes on stars sit systematically off
// their Gaia stars.
type OffsetTile struct {
	TX      int     `json:"tx"`
	TY      int     `json:"ty"`
	Changes int     `json:"changes"`
	DX      float64 `json:"dx_px"`
	DY      float64 `json:"dy_px"`
}

// offsetGuard measures, kernel tile by kernel tile, how far the changes on
// isolated Gaia stars sit from them, and cuts the changes of each tile whose
// median offset is over maxOffsetPx, so a difference whose images are shifted
// there cannot fill the list with the lobes of every star.
func (in *Input) offsetGuard(res *Result) {
	if len(res.Candidates) == 0 {
		return
	}
	var pts [][2]float64
	for _, s := range in.gaiaNight {
		if s.G > zpMaxG {
			continue
		}
		if x, y, ok := in.WCS.PixelOf(s.RA, s.Dec); ok && x > -10 && y > -10 && x < float64(in.W)+10 && y < float64(in.H)+10 {
			pts = append(pts, [2]float64{x, y})
		}
	}
	if len(pts) == 0 {
		return
	}
	grid := NewGrid(pts, 8)
	type tile struct{ dx, dy []float64 }
	tiles := map[[2]int]*tile{}
	tileOf := func(x, y float64) [2]int {
		tx := min(max(int(x*kernelTilesX/float64(in.W)), 0), kernelTilesX-1)
		ty := min(max(int(y*kernelTilesY/float64(in.H)), 0), kernelTilesY-1)
		return [2]int{tx, ty}
	}
	for _, c := range res.Candidates {
		i, _ := grid.Nearest(c.X, c.Y, offsetReachPx)
		if i < 0 || len(grid.Within(pts[i][0], pts[i][1], offsetIsolatedPx)) != 1 {
			continue
		}
		k := tileOf(c.X, c.Y)
		if tiles[k] == nil {
			tiles[k] = &tile{}
		}
		tiles[k].dx = append(tiles[k].dx, c.X-pts[i][0])
		tiles[k].dy = append(tiles[k].dy, c.Y-pts[i][1])
	}
	bad := map[[2]int]bool{}
	for k, t := range tiles {
		if len(t.dx) < offsetMinChanges {
			continue
		}
		dx, dy := median64(t.dx), median64(t.dy)
		if math.Hypot(dx, dy) > maxOffsetPx {
			bad[k] = true
			res.OffsetTiles = append(res.OffsetTiles, OffsetTile{TX: k[0], TY: k[1], Changes: len(t.dx), DX: dx, DY: dy})
		}
	}
	if len(bad) == 0 {
		return
	}
	kept := res.Candidates[:0]
	for _, c := range res.Candidates {
		if !bad[tileOf(c.X, c.Y)] {
			kept = append(kept, c)
			continue
		}
		res.Rejected = append(res.Rejected, Rejection{X: c.X, Y: c.Y, Sign: c.Sign, SNR: c.SNR, Reason: offsetReason, Dipole: c.Dipole, Frac: c.Frac})
	}
	res.Candidates = kept
}

// String describes an offset tile for the run's record.
func (t OffsetTile) String() string {
	return fmt.Sprintf("tile %d,%d: %d changes on stars sit (%.2f, %.2f) px off them", t.TX, t.TY, t.Changes, t.DX, t.DY)
}
