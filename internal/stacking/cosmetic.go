package stacking

import (
	"fmt"
	"math"
	"os"
	"slices"
	"strings"

	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
)

// Cosmetic correction of subs that come calibrated (Telescope.live): their
// calibration leaves hot pixels, and on some cameras a hot column, in every
// sub. Our own subs lose theirs to the dark (calibrate_single -cc=dark).
// Rejection can't take them out where a pixel has fewer than three subs,
// and a column that is hot in every sub isn't an outlier at all.

// cosmeSigma are find_cosme's cold and hot thresholds, in σ: Siril's own
// defaults for cosmetic correction.
const cosmeColdSigma, cosmeHotSigma = 3.0, 3.0

// badColumnSigma is how far, in the sub's pixel noise, a column's median
// must stand from its neighbours' to be replaced. A column median over
// thousands of rows is known to a few hundredths of σ, and sky and nebula
// change smoothly from one column to the next, so half a σ is a defect.
const badColumnSigma = 0.5

// maxBadColumns is the most columns, as a share of the width, replaced in
// one sub; more is some pattern in the image rather than a few defects.
const maxBadColumns = 0.01

// badColumns finds the columns of an image whose median stands out from
// the columns two and three either side of it: hot or cold columns. Their
// direct neighbours are left out of the comparison, as a hot column can
// bleed charge into them.
func badColumns(im *imagedata.Image, saturation float32) []int {
	p := im.Plane(0)
	w, h := im.W, im.H
	if w < 7 || h < 16 {
		return nil
	}
	sigma := pixelNoise(p, saturation)
	if sigma <= 0 {
		return nil
	}
	stride := max(1, h/2048)
	med := make([]float32, w)
	col := make([]float32, 0, h/stride+1)
	for x := range w {
		col = col[:0]
		for y := 0; y < h; y += stride {
			if v := p[y*w+x]; v != 0 && v < saturation {
				col = append(col, v)
			}
		}
		if len(col) < h/stride/2 {
			med[x] = float32(math.NaN())
			continue
		}
		slices.Sort(col)
		med[x] = col[len(col)/2]
	}
	var bad []int
	near := make([]float32, 0, 4)
	for x := range w {
		if med[x] != med[x] { // NaN: mostly empty or saturated
			continue
		}
		near = near[:0]
		for _, d := range []int{-3, -2, 2, 3} {
			if n := x + d; n >= 0 && n < w && med[n] == med[n] {
				near = append(near, med[n])
			}
		}
		if len(near) < 2 {
			continue
		}
		slices.Sort(near)
		ref := (near[(len(near)-1)/2] + near[len(near)/2]) / 2
		if abs32(med[x]-ref) > badColumnSigma*sigma {
			bad = append(bad, x)
		}
	}
	if float64(len(bad)) > maxBadColumns*float64(w) {
		return nil
	}
	return bad
}

// pixelNoise is the noise of one pixel (σ), from the MAD of differences
// between neighbouring pixels of a strided sample: unlike the spread of the
// pixels themselves, it doesn't grow with gradients and nebulosity.
func pixelNoise(p []float32, saturation float32) float32 {
	stride := max(2, len(p)/400_000)
	d := make([]float32, 0, len(p)/stride+1)
	for i := 0; i+1 < len(p); i += stride {
		a, b := p[i], p[i+1]
		if a != 0 && b != 0 && a < saturation && b < saturation {
			d = append(d, abs32(a-b))
		}
	}
	if len(d) == 0 {
		return 0
	}
	slices.Sort(d)
	return d[len(d)/2] * madToSigma / math.Sqrt2
}

// writeCosmeList writes the bad columns of the image in file as a list for
// Siril's cosme command ("C x 0", x from 0) and returns its name, or ""
// when there are none.
func writeCosmeList(file, list string, saturation float32) (string, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	im, err := imagedata.Decode(b)
	if err != nil {
		return "", err
	}
	cols := badColumns(im, saturation)
	if len(cols) == 0 {
		return "", nil
	}
	var sb strings.Builder
	for _, x := range cols {
		fmt.Fprintf(&sb, "C %d 0\n", x)
	}
	return list, os.WriteFile(list, []byte(sb.String()), 0o600)
}
