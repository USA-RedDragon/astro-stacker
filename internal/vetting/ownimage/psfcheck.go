package ownimage

import (
	"context"
	"fmt"
	"math"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
)

// diffStamp is a difference image around the spot with the spot's place
// in it.
type diffStamp struct {
	v      []float64
	n      int
	cx, cy float64
	sign   int
}

// storedDiff reads the difference plane of the detection's cutout.
func storedDiff(ctx context.Context, d Data, sp *spotPlan) (diffStamp, bool) {
	for _, n := range sp.spot.Nights {
		if n.CutoutKey == "" {
			continue
		}
		b, err := d.Get(ctx, n.CutoutKey+"stamp.fits")
		if err != nil {
			continue
		}
		im, err := imagedata.Decode(b)
		if err != nil || im.C < 3 || im.W != discoveries.StampSize || im.H != discoveries.StampSize {
			continue
		}
		p := im.Plane(2)
		out := diffStamp{v: make([]float64, len(p)), n: im.W, sign: n.Sign}
		for i, x := range p {
			out.v[i] = float64(x)
		}
		half := float64(discoveries.StampSize / 2)
		out.cx = half + n.X - math.Round(n.X)
		out.cy = half + n.Y - math.Round(n.Y)
		return out, true
	}
	return diffStamp{}, false
}

func meanStamp(sp *spotPlan, e *ensemble, idx []int) ([]float64, float64) {
	sum := make([]float64, stampSide*stampSide)
	cnt := make([]float64, stampSide*stampSide)
	var sig []float64
	for _, i := range idx {
		s := scaledStamp(sp, e, i)
		if s == nil {
			continue
		}
		sig = append(sig, sp.recs[i].sigma)
		for k, v := range s {
			if finite(v) {
				sum[k] += v
				cnt[k]++
			}
		}
	}
	for k := range sum {
		if cnt[k] > 0 {
			sum[k] /= cnt[k]
		} else {
			sum[k] = math.NaN()
		}
	}
	return sum, median(sig)
}

func blur(v []float64, n int, sigma float64) []float64 {
	if !(sigma > 0.05) {
		return v
	}
	r := int(math.Ceil(3 * sigma))
	k := make([]float64, 2*r+1)
	var ks float64
	for i := range k {
		d := float64(i - r)
		k[i] = math.Exp(-d * d / (2 * sigma * sigma))
		ks += k[i]
	}
	pass := func(src []float64, dx, dy int) []float64 {
		out := make([]float64, len(src))
		for y := range n {
			for x := range n {
				var s, w float64
				for i := -r; i <= r; i++ {
					xx, yy := x+i*dx, y+i*dy
					if xx < 0 || yy < 0 || xx >= n || yy >= n || !finite(src[yy*n+xx]) {
						continue
					}
					s += k[i+r] * src[yy*n+xx]
					w += k[i+r]
				}
				out[y*n+x] = math.NaN()
				if w > 0.5*ks {
					out[y*n+x] = s / w
				}
			}
		}
		return out
	}
	return pass(pass(v, 1, 0), 0, 1)
}

// ownDiff is the night less its reference from the subs, the sharper of
// the two blurred to the other's star width.
func ownDiff(sp *spotPlan, e *ensemble) (diffStamp, bool) {
	var best refSet
	for _, s := range sp.sets {
		if len(s.nights) > len(best.nights) {
			best = s
		}
	}
	if len(best.nights) == 0 || len(best.refs) == 0 {
		return diffStamp{}, false
	}
	night, sn := meanStamp(sp, e, best.nights)
	ref, sr := meanStamp(sp, e, best.refs)
	if !finite(sn) || !finite(sr) {
		return diffStamp{}, false
	}
	if sn < sr {
		night = blur(night, stampSide, math.Sqrt(sr*sr-sn*sn))
	} else {
		ref = blur(ref, stampSide, math.Sqrt(sn*sn-sr*sr))
	}
	out := diffStamp{v: make([]float64, len(night)), n: stampSide, sign: sp.signOf(best.night, best.filter)}
	for k := range night {
		out.v[k] = night[k] - ref[k]
	}
	out.cx = stampHalf + sp.x - math.Round(sp.x)
	out.cy = stampHalf + sp.y - math.Round(sp.y)
	return out, true
}

func starWidth(sp *spotPlan) float64 {
	var nv, rv []float64
	for i, r := range sp.recs {
		if r == nil || !(r.sigma > 0) {
			continue
		}
		if sp.night[i] {
			nv = append(nv, r.sigma)
		}
		if sp.ref[i] {
			rv = append(rv, r.sigma)
		}
	}
	return math.Max(median(nv), median(rv))
}

func psf(ctx context.Context, d Data, sp *spotPlan, e *ensemble) check.Check {
	ds, ok := storedDiff(ctx, d, sp)
	if !ok {
		ds, ok = ownDiff(sp, e)
	}
	if !ok {
		return check.Skipped(check.PSF, "Not measured yet. No difference image of the spot could be made.")
	}
	sign := 1.0
	switch {
	case ds.sign < 0:
		sign = -1
	case ds.sign == 0:
		c := int(math.Round(ds.cy))*ds.n + int(math.Round(ds.cx))
		if c >= 0 && c < len(ds.v) && ds.v[c] < 0 {
			sign = -1
		}
	}
	v := make([]float64, len(ds.v))
	var ring []float64
	for k, x := range ds.v {
		v[k] = sign * x
		dx, dy := float64(k%ds.n)-ds.cx, float64(k/ds.n)-ds.cy
		if r := math.Hypot(dx, dy); r > psfFitR+3 && r < psfFitR+9 && finite(x) {
			ring = append(ring, x)
		}
	}
	noise := robustStd(ring)
	g := fitGauss2D(v, ds.n, ds.cx, ds.cy, psfFitR)
	sw := starWidth(sp)
	if !g.ok || !(noise > 0) || !finite(sw) {
		return check.Skipped(check.PSF, "Not measured yet. The shape of the change could not be fitted.")
	}
	if g.amp/noise < psfMinSNR {
		return check.Skipped(check.PSF, fmt.Sprintf("Not measured yet. The change is too faint to measure its shape (signal to noise %.1f at its peak).", g.amp/noise))
	}
	ratio := g.width() / sw
	narrow := math.Min(g.sa, g.sb) / sw
	el := g.elongation()
	minNarrow, maxElong := psfMinRatio, psfMaxElong
	if g.amp/noise < psfFirmSNR {
		minNarrow, maxElong = psfFaintRatio, psfFaintElong
	}
	switch {
	case narrow < minNarrow:
		return check.New(check.PSF, check.Fail, fmt.Sprintf("The change is %.2f times as wide as nearby stars, sharper than any star.", narrow))
	case ratio > psfMaxRatio:
		return check.New(check.PSF, check.Fail, fmt.Sprintf("The change is %.1f times as wide as nearby stars, too spread out for a star.", ratio))
	case el > maxElong:
		return check.New(check.PSF, check.Fail, fmt.Sprintf("The change is stretched %.1f to 1, where stars are round.", el))
	}
	return check.New(check.PSF, check.Pass, fmt.Sprintf("The change is as wide as nearby stars (%.2f times) and round (%.2f to 1).", ratio, el))
}
