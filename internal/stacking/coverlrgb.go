package stacking

import (
	"fmt"
	"math"
	"slices"
)

// lrgbMaxShift is the most, in linear-preview pixels, the stars of the
// luminance may sit from those of the colour channels for it to go into a
// cover: a target's masters are all registered to one reference, so they
// agree to a fraction of a pixel when they do.
const lrgbMaxShift = 0.5

// lrgbMinGain is how much less noisy, as a share, the lightness must come
// out with the luminance in it for a cover to use it: less is not worth
// the luminance's own stars and seeing in the picture.
const lrgbMinGain = 0.05

// lrgbReport says what the luminance did for a cover's lightness, as
// measured on the cover's own linear previews.
type lrgbReport struct {
	Used bool
	// Weight is the luminance's share of the lightness, by inverse
	// variance: (R+G+B)/3 takes the rest.
	Weight float64
	// Scale maps the sky-subtracted luminance onto (R+G+B)/3.
	Scale float64
	// NoiseRGB, NoiseL and NoiseOut are the sky's pixel noise in
	// (R+G+B)/3, in the scaled luminance and in the lightness used.
	NoiseRGB, NoiseL, NoiseOut float64
	// Shift is the median offset of the luminance's stars from the
	// colour's, over Stars matched stars.
	Shift float64
	Stars int
	Why   string
}

// Gain is how much less noisy the lightness is than (R+G+B)/3 alone.
func (r lrgbReport) Gain() float64 {
	if !(r.NoiseRGB > 0) {
		return 0
	}
	return 1 - r.NoiseOut/r.NoiseRGB
}

// lrgbLight combines a luminance with the colour channels' own lightness,
// synth ((R+G+B)/3 of the balanced channels), in linear space: the
// luminance's sky is taken off as the channels' is (on the same sky
// blocks), it is scaled onto synth, and the two are averaged by inverse
// variance of their sky noise. It returns the lightness, or synth itself
// when the luminance doesn't line up with the colour or wouldn't lower the
// noise by lrgbMinGain. Pixels the luminance doesn't cover keep synth.
func lrgbLight(l []float32, raw [3][]float32, synth []float32, among []bool, w, h, s, gw, gh int) ([]float32, lrgbReport) {
	var rep lrgbReport
	mean := make([]float32, w*h)
	for i := range mean {
		if raw[0][i] != 0 && l[i] != 0 {
			mean[i] = (raw[0][i] + raw[1][i] + raw[2][i]) / 3
		}
	}
	rep.Shift, rep.Stars = starShift(l, mean, w, h)
	if rep.Stars < minSeamStars || rep.Shift > lrgbMaxShift {
		rep.Why = fmt.Sprintf("its stars sit %.2g px from the colour's over %d stars; at most %.2g px over %d or more", rep.Shift, rep.Stars, lrgbMaxShift, minSeamStars)
		return synth, rep
	}
	blocks, _, _ := blockMedians(l, w, h, s)
	surface, _ := skySurfaceAmong(blocks, among, gw, gh)
	res := make([]float64, len(surface))
	for i := range res {
		res[i] = math.NaN()
		if among[i] {
			res[i] = blocks[i] - surface[i]
		}
	}
	if off := skyLevel(res); off < 0 {
		for i := range surface {
			surface[i] += off
		}
	}
	sky := upsampleGrid(surface, gw, gh, s, w, h)
	sub := make([]float32, w*h)
	var xs, ys []float64
	stride := max(1, w*h/fitSamples)
	for i := range sub {
		if mean[i] == 0 {
			continue
		}
		sub[i] = l[i] - sky[i]
		if i%stride == 0 {
			xs, ys = append(xs, float64(sub[i])), append(ys, float64(synth[i]))
		}
	}
	up, ok := fitLAD(xs, ys)
	down, ok2 := fitLAD(ys, xs)
	if !ok || !ok2 || up <= 0 || down <= 0 {
		rep.Why = "no positive relation between it and the colour's lightness"
		return synth, rep
	}
	rep.Scale = math.Sqrt(up / down)
	for i := range sub {
		sub[i] *= float32(rep.Scale)
	}
	rep.NoiseRGB = skyNoise(synth, mean, among, w, h, s, gw)
	rep.NoiseL = skyNoise(sub, mean, among, w, h, s, gw)
	if !(rep.NoiseRGB > 0) || !(rep.NoiseL > 0) {
		rep.Why = "no sky to measure the noise on"
		return synth, rep
	}
	vy, vl := rep.NoiseRGB*rep.NoiseRGB, rep.NoiseL*rep.NoiseL
	rep.Weight = vy / (vy + vl)
	rep.NoiseOut = math.Sqrt(vy * vl / (vy + vl))
	if rep.Gain() < lrgbMinGain {
		rep.Why = fmt.Sprintf("it would lower the noise by %.0f%%, less than %.0f%%", 100*rep.Gain(), 100*lrgbMinGain)
		return synth, rep
	}
	out := slices.Clone(synth)
	wl := float32(rep.Weight)
	for i := range out {
		if mean[i] != 0 {
			out[i] = wl*sub[i] + (1-wl)*synth[i]
		}
	}
	rep.Used = true
	rep.Why = fmt.Sprintf("lowers the noise by %.0f%%", 100*rep.Gain())
	return out, rep
}

// starShift is the median offset between stars found in a and b, and how
// many were matched both ways.
func starShift(a, b []float32, w, h int) (float64, int) {
	sa, sb := detectStars(a, w, h, placement{}, 1), detectStars(b, w, h, placement{}, 1)
	if len(sa) == 0 || len(sb) == 0 {
		return math.NaN(), 0
	}
	ga, gb := gridOf(sa), gridOf(sb)
	var dx, dy []float64
	for i, s := range sa {
		j, _ := gb.nearest(sb, s)
		if j < 0 {
			continue
		}
		if back, _ := ga.nearest(sa, sb[j]); back != i {
			continue
		}
		dx, dy = append(dx, sb[j].X-s.X), append(dy, sb[j].Y-s.Y)
	}
	if len(dx) == 0 {
		return math.NaN(), 0
	}
	return math.Hypot(percentile(dx, 0.5), percentile(dy, 0.5)), len(dx)
}

// skyNoise is the noise of p's pixels in the sky blocks (among), from
// the MAD of differences between horizontal neighbours, which the sky's
// gradients and faint structure barely move.
func skyNoise(p, have []float32, among []bool, w, h, s, gw int) float64 {
	var d []float64
	for y := range h {
		gy := y / s
		for x := 0; x+1 < w; x += 2 {
			if !among[gy*gw+x/s] || !among[gy*gw+(x+1)/s] {
				continue
			}
			i := y*w + x
			if have[i] == 0 || have[i+1] == 0 {
				continue
			}
			d = append(d, float64(p[i+1]-p[i]))
		}
	}
	if len(d) < 100 {
		return 0
	}
	med := percentile(d, 0.5)
	for i := range d {
		d[i] = math.Abs(d[i] - med)
	}
	slices.Sort(d)
	return d[len(d)/2] * madToSigma / math.Sqrt2
}
