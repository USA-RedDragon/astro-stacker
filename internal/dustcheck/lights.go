package dustcheck

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"sync"
)

// The lights check needs this many subs, from at least LightsMinFields
// fields, for stars and nebulae to median out of their own flat.
const (
	LightsMinSubs   = 40
	LightsMinFields = 2
	// LightsMin is the shallowest residual shadow, as a fraction, that
	// counts; a residual must also stand KSigma above the residual's noise,
	// which star and nebula leftovers raise on nights of few fields.
	LightsMin = 0.003
	// LightsMinDiameter is the smallest residual shadow, in sensor pixels,
	// that counts: window dust shadows are 390 px across and more, star
	// and nebula leftovers smaller.
	LightsMinDiameter = 300
)

// Lights check outcomes.
const (
	LightsChecked  = "checked"
	LightsTooFew   = "too_few_subs"
	LightsOneField = "one_field"
	LightsFailed   = "failed"
)

// SubMap bins a calibrated light 4×4 and divides it by its median, and
// fails for a light whose sky is not above zero.
func SubMap(pix []float32, w, h int) (Map, error) {
	b := Bin(pix, w, h, BinFactor)
	med := median(slices.Clone(b.Pix))
	if !(med > 0) {
		return Map{}, fmt.Errorf("sky median %g is not above zero", med)
	}
	for i := range b.Pix {
		b.Pix[i] /= med
	}
	return b, nil
}

// MaskStars sets the stars, bright nebulae and anything over 1.5 times the
// sky of a normalized sub map to NaN.
func MaskStars(b Map) {
	sm := MedianFilter(b, 9)
	resid := make([]float32, len(b.Pix))
	for i := range b.Pix {
		resid[i] = float32(math.Abs(float64(b.Pix[i] - sm.Pix[i])))
	}
	sg := 1.4826 * float64(median(resid))
	hot := make([]bool, len(b.Pix))
	for i := range b.Pix {
		hot[i] = float64(b.Pix[i]-sm.Pix[i]) > 4*sg
	}
	hot = dilate(hot, b.W, b.H, 3)
	p := percentile(sm.Pix, 99.5)
	bright := make([]bool, len(b.Pix))
	for i, v := range sm.Pix {
		bright[i] = v > p
	}
	bright = dilate(bright, b.W, b.H, 6)
	for i := range b.Pix {
		if hot[i] || bright[i] || b.Pix[i] > 1.5 {
			b.Pix[i] = float32(math.NaN())
		}
	}
}

// percentile interpolates linearly between ranks, as numpy does.
func percentile(v []float32, q float64) float32 {
	s := make([]float32, 0, len(v))
	for _, x := range v {
		if !isNaN(x) {
			s = append(s, x)
		}
	}
	if len(s) == 0 {
		return float32(math.NaN())
	}
	slices.Sort(s)
	pos := q / 100 * float64(len(s)-1)
	i := int(pos)
	if i >= len(s)-1 {
		return s[len(s)-1]
	}
	f := pos - float64(i)
	return float32(float64(s[i])*(1-f) + float64(s[i+1])*f)
}

// flatten divides a masked sub map by a quadratic fitted to its unmasked
// pixels, so sky gradients don't survive the median.
func flatten(b Map) {
	var ata [6][6]float64
	var atb [6]float64
	k := 0
	terms := func(x, y int) [6]float64 {
		fx, fy := float64(x)/float64(b.W), float64(y)/float64(b.H)
		return [6]float64{1, fx, fy, fx * fy, fx * fx, fy * fy}
	}
	for i, v := range b.Pix {
		if isNaN(v) {
			continue
		}
		k++
		if (k-1)%7 != 0 {
			continue
		}
		t := terms(i%b.W, i/b.W)
		for r := range 6 {
			for c := range 6 {
				ata[r][c] += t[r] * t[c]
			}
			atb[r] += t[r] * float64(v)
		}
	}
	coef, ok := solve6(ata, atb)
	if !ok {
		return
	}
	for i, v := range b.Pix {
		if isNaN(v) {
			continue
		}
		t := terms(i%b.W, i/b.W)
		s := 0.0
		for j := range 6 {
			s += coef[j] * t[j]
		}
		b.Pix[i] = float32(float64(v) / s)
	}
}

func solve6(a [6][6]float64, b [6]float64) ([6]float64, bool) {
	var x [6]float64
	for c := range 6 {
		p := c
		for r := c + 1; r < 6; r++ {
			if math.Abs(a[r][c]) > math.Abs(a[p][c]) {
				p = r
			}
		}
		if math.Abs(a[p][c]) < 1e-12 {
			return x, false
		}
		a[c], a[p] = a[p], a[c]
		b[c], b[p] = b[p], b[c]
		for r := c + 1; r < 6; r++ {
			f := a[r][c] / a[c][c]
			for k := c; k < 6; k++ {
				a[r][k] -= f * a[c][k]
			}
			b[r] -= f * b[c]
		}
	}
	for r := 5; r >= 0; r-- {
		s := b[r]
		for k := r + 1; k < 6; k++ {
			s -= a[r][k] * x[k]
		}
		x[r] = s / a[r][r]
	}
	return x, true
}

// Residual is what a night's lights hold that their flats don't: the
// median of the star-masked, gradient-flattened sub maps (calibrated with
// their flats), divided by a wide background, less one.
func Residual(subs []Map) (Map, error) {
	if len(subs) == 0 {
		return Map{}, errors.New("no subs")
	}
	w, h := subs[0].W, subs[0].H
	for _, s := range subs {
		if s.W != w || s.H != h {
			return Map{}, errors.New("subs differ in size")
		}
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 2)
	for _, s := range subs {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			MaskStars(s)
			flatten(s)
		})
	}
	wg.Wait()
	med := NewMap(w, h)
	col := make([]float32, len(subs))
	for i := range med.Pix {
		for j, s := range subs {
			col[j] = s.Pix[i]
		}
		v := median(col)
		if isNaN(v) {
			v = 1
		}
		med.Pix[i] = v
	}
	normalize(med)
	bg := Gaussian(Zoom(MedianFilter(Bin(med.Pix, w, h, bgStep), (BackgroundSize/bgStep)|1), w, h), 2)
	r := NewMap(w, h)
	ratio(med, bg, r)
	return r, nil
}

// ResidualDiscs are the dust shadows (negative Peak) and brightenings left
// in a residual map: at least LightsMin and KSigma noise sigmas deep, at
// least LightsMinDiameter across and clear of the border.
func ResidualDiscs(s Smoothed) []Blob {
	var out []Blob
	margin := float64(Border * BinFactor)
	w, h := float64(s.W*BinFactor), float64(s.H*BinFactor)
	thr := math.Max(LightsMin, KSigma*Sigma(s))
	for _, sign := range []int{-1, 1} {
		for _, b := range Blobs(s, thr, sign, EdgeMin) {
			if !b.Disc || b.Diameter < LightsMinDiameter {
				continue
			}
			if math.Min(math.Min(float64(b.X), float64(b.Y)), math.Min(w-float64(b.X), h-float64(b.Y)))-float64(b.Diameter)/2 < margin {
				continue
			}
			out = append(out, b)
		}
	}
	return out
}

// LightsStatus is whether a night's lights are enough to check.
func LightsStatus(subs, fields int) string {
	switch {
	case subs < LightsMinSubs:
		return LightsTooFew
	case fields < LightsMinFields:
		return LightsOneField
	}
	return LightsChecked
}
