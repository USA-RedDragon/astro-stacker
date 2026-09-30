package stacking

import (
	"math"
	"testing"
)

// Emission filling much of the frame keeps its colour, where stretching
// each channel on its own median turned it grey; the sky left beside it
// stays neutral and the stars white.
func TestColourStretchKeepsFrameFillingEmission(t *testing.T) {
	t.Parallel()
	f := newLineField(t, false)
	// A camera whose red and blue see stars at 1.4 and 0.8 of green.
	scale := func(p []float32, k float32) []float32 {
		out := make([]float32, len(p))
		for i, v := range p {
			out[i] = v * k
		}
		return out
	}
	r, g, b := scale(f.r, 1.4), f.g, scale(f.b, 0.8)
	emission := func(x int) bool { return x >= 130 }
	for y := range f.h {
		for x := range f.w {
			if emission(x) {
				r[y*f.w+x] += 1.4 * 0.0015
			}
		}
	}
	out := colourStretch([3][]float32{r, g, b}, [3][]float32{r, g, b}, f.w, f.h)
	mean := func(keep func(x, y int) bool) (dr, db float64) {
		var n float64
		for y := range f.h {
			for x := range f.w {
				if !keep(x, y) {
					continue
				}
				i := y*f.w + x
				dr += float64(out[0][i] - out[1][i])
				db += float64(out[2][i] - out[1][i])
				n++
			}
		}
		return 255 * dr / n, 255 * db / n
	}
	if dr, db := mean(func(x, y int) bool { return emission(x) && !f.star(x, y) }); dr < 10 || dr < db+10 {
		t.Errorf("emission R-G %.1f, B-G %.1f levels: not red", dr, db)
	}
	if dr, db := mean(func(x, y int) bool { return x < 50 && !f.star(x, y) }); math.Abs(dr) > 3 || math.Abs(db) > 3 {
		t.Errorf("sky R-G %.1f, B-G %.1f levels: not neutral", dr, db)
	}
	if dr, db := mean(func(x, y int) bool { return x < 50 && f.star(x, y) }); math.Abs(dr) > 6 || math.Abs(db) > 6 {
		t.Errorf("stars R-G %.1f, B-G %.1f levels: not white", dr, db)
	}
}
