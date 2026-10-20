package discoveries

import (
	"testing"
)

func TestOffsetGuardCutsShiftedTile(t *testing.T) {
	const W, H = 6000, 4000
	w := &WCS{CRPIX1: W / 2, CRPIX2: H / 2, CRVAL1: 313, CRVAL2: 30, CD: [2][2]float64{{-1.92 / 3600, 0}, {0, 1.92 / 3600}}, H: H}
	in := &Input{W: W, H: H, WCS: w}
	res := &Result{}
	add := func(x0, y0, dx, dy float64, n int) {
		for i := range n {
			x, y := x0+float64(i%5)*60, y0+float64(i/5)*60
			ra, dec := w.SkyOf(x, y)
			in.gaiaNight = append(in.gaiaNight, Star{RA: ra, Dec: dec, G: 14})
			res.Candidates = append(res.Candidates, Candidate{X: x + dx, Y: y + dy, Sign: -1, SNR: 10})
		}
	}
	add(2200, 100, -1.7, 0.05, 15)
	add(200, 100, 0.1, -0.1, 15)
	add(4200, 2200, 1.0, 0, 5)
	in.offsetGuard(res)
	if len(res.OffsetTiles) != 1 || res.OffsetTiles[0].TX != 2 || res.OffsetTiles[0].TY != 0 {
		t.Fatalf("offset tiles %+v, want only tile 2,0", res.OffsetTiles)
	}
	if len(res.Candidates) != 20 || len(res.Rejected) != 15 {
		t.Fatalf("kept %d cut %d, want 20 and 15", len(res.Candidates), len(res.Rejected))
	}
	for _, c := range res.Candidates {
		if c.X >= 2000 && c.X < 3000 && c.Y < 1000 {
			t.Fatalf("kept %+v from the shifted tile", c)
		}
	}
}
