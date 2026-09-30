package stacking

import (
	"log/slog"
	"math"
	"slices"

	"github.com/USA-RedDragon/astro-stacker/internal/preview"
)

// coverSkyShare is the share of darkest blocks the sky's spread, the
// pedestal under the colour ratios, is measured on.
const coverSkyShare = 0.2

// coverPedestal is the pedestal, in the sky's spread, under the colour
// ratios: the sky's noise comes out grey, not coloured.
const coverPedestal = 3.0

// coverWeightMax bounds the star white balance: a channel is scaled by at
// most this much, or this little, relative to green.
const coverWeightMax = 3.0

// colourStretch balances and stretches a cover's red, green and blue
// together, so the colour of emission that fills the frame (the North
// America Nebula) survives, where stretching each channel on its own
// median cancelled it.
//
//   - Background: each channel's sky, a quadratic surface fitted to the
//     star-free sky blocks of all three (skySurfaceAmong) and lowered to
//     the darkest of them, is taken off, so dark sky and dark clouds are
//     neutral and gradients the filters don't share go.
//   - White balance: each channel is scaled so the brighter stars come out
//     white on average, from the same star photometry the line addition
//     uses (starRatios).
//   - One stretch for all three: the STF auto-stretch (median at
//     TargetBackground, black point ShadowsClip σ below) of their mean,
//     and colour from their linear ratios, in display form, onto it, as
//     the manual North America and Cygnus Loop versions did. Stretching
//     each balanced channel with the same curve instead left bright
//     galaxies white, their H II regions lost.
//
// raw are the channels without narrowband added; the balance and stretch
// come from them, so where no line is added the cover is as without it.
// Pixels empty (0) in raw red stay black.
func colourStretch(chans, raw [3][]float32, w, h int) [3][]float32 {
	s := max(4, int(math.Round(float64(max(w, h))/200)))
	var blocks [3][]float64
	var gw, gh int
	for c := range 3 {
		blocks[c], gw, gh = blockMedians(raw[c], w, h, s)
	}
	// The sky blocks, the same for all three channels: those that follow
	// a smooth surface under the three channels together, each relative to
	// its darkest level. Fitting each channel on its own took faint H-a
	// among the North America Nebula's dust for red sky, and the dust came
	// out green.
	var darkest [3]float64
	for c := range 3 {
		darkest[c] = max(skyLevel(blocks[c]), 1e-12)
	}
	dark := make([]float64, len(blocks[0]))
	for i := range dark {
		for c := range 3 {
			dark[i] += blocks[c][i] / darkest[c]
		}
	}
	_, among := skySurface(dark, gw, gh)
	var sky [3][]float32
	var skyMean [3]float64
	for c := range 3 {
		surface, _ := skySurfaceAmong(blocks[c], among, gw, gh)
		// Then down to the darkest of the sky blocks, so dark clouds come
		// out neutral: sky blocks still hold faint emission (H-a in red).
		res := make([]float64, len(surface))
		for i := range res {
			res[i] = math.NaN()
			if among[i] {
				res[i] = blocks[c][i] - surface[i]
			}
		}
		if off := skyLevel(res); off < 0 {
			for i := range surface {
				surface[i] += off
			}
		}
		sky[c] = upsampleGrid(surface, gw, gh, s, w, h)
		skyMean[c] = skyLevel(surface)
	}
	weight := [3]float64{1, 1, 1}
	ratios, n := starRatios([][]float32{raw[0], raw[1], raw[2]}, blocks[:], w, h, s, gw, gh)
	if n >= lineStars && ratios[1] > 0 {
		// ratios are red/green and green/blue.
		weight[0] = math.Max(1/coverWeightMax, math.Min(coverWeightMax, 1/ratios[0]))
		weight[2] = math.Max(1/coverWeightMax, math.Min(coverWeightMax, ratios[1]))
	}
	balance := func(c, i int, v float32) float64 { return float64(v-sky[c][i]) * weight[c] }

	// The stretch, from the mean of the balanced raw channels.
	lum := make([]float32, w*h)
	for i := range lum {
		if raw[0][i] != 0 {
			lum[i] = float32((balance(0, i, raw[0][i]) + balance(1, i, raw[1][i]) + balance(2, i, raw[2][i])) / 3)
		}
	}
	out := [3][]float32{make([]float32, w*h), make([]float32, w*h), make([]float32, w*h)}
	var hi float64
	for i := 0; i < len(lum); i += 7 {
		if raw[0][i] != 0 {
			hi = max(hi, float64(lum[i]))
		}
	}
	med, sigma := statsNonZero(lum)
	c0 := med + preview.ShadowsClip*sigma
	_, spread, ok := darkSky(lum, raw[0], w, h, s)
	if !ok {
		return out
	}
	if hi <= c0 {
		hi = c0 + 1
	}
	span := hi - c0
	m := preview.MTF(preview.TargetBackground, math.Max(0, math.Min(1, (med-c0)/span)))
	// Colour from the linear ratios of the balanced channels, blurred a
	// little and over a pedestal of the sky's spread so the sky's noise
	// isn't coloured, in display (gamma 2.2) form, onto the stretched mean.
	var bal, blur [3][]float32
	for c := range 3 {
		bal[c] = make([]float32, w*h)
		for i, v := range chans[c] {
			if raw[0][i] != 0 {
				bal[c][i] = float32(balance(c, i, v))
			}
		}
		blur[c] = gaussPlane(bal[c], w, h, 1)
	}
	ped := coverPedestal * spread
	for i := range lum {
		if raw[0][i] == 0 {
			continue
		}
		y := (float64(bal[0][i]) + float64(bal[1][i]) + float64(bal[2][i])) / 3
		ls := preview.MTF(m, math.Max(0, math.Min(1, (y-c0)/span)))
		var q [3]float64
		var qm float64
		for c := range 3 {
			q[c] = math.Max(0, float64(blur[c][i])) + ped
			qm += q[c] / 3
		}
		top := 1.0
		for c := range 3 {
			q[c] = ls * math.Pow(q[c]/qm, 1/2.2)
			top = max(top, q[c])
		}
		for c := range 3 {
			out[c][i] = float32(q[c] / top)
		}
	}
	slog.Debug("Cover stretch", "sky", skyMean, "weight", weight, "stars", n, "median", med, "sigma", sigma, "hi", hi)
	return out
}

// darkSky is the level and spread (MAD σ) of p's pixels in its darkest
// blocks: those whose median is in the lowest coverSkyShare. The spread,
// unlike the pixel noise, includes the faint stars of the sky.
func darkSky(p, have []float32, w, h, s int) (level, spread float64, ok bool) {
	blocks, gw, gh := blockMedians(p, w, h, s)
	var sorted []float64
	for _, v := range blocks {
		if !math.IsNaN(v) {
			sorted = append(sorted, v)
		}
	}
	if len(sorted) == 0 {
		return 0, 0, false
	}
	slices.Sort(sorted)
	limit := sorted[int(coverSkyShare*float64(len(sorted)-1))]
	var px []float64
	for gy := range gh {
		for gx := range gw {
			if v := blocks[gy*gw+gx]; math.IsNaN(v) || v > limit {
				continue
			}
			for y := gy * s; y < min(h, (gy+1)*s); y++ {
				for x := gx * s; x < min(w, (gx+1)*s); x++ {
					if have[y*w+x] != 0 {
						px = append(px, float64(p[y*w+x]))
					}
				}
			}
		}
	}
	if len(px) == 0 {
		return 0, 0, false
	}
	slices.Sort(px)
	level = px[len(px)/2]
	for i := range px {
		px[i] = math.Abs(px[i] - level)
	}
	slices.Sort(px)
	return level, px[len(px)/2] * madToSigma, true
}
