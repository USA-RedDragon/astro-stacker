package period

import (
	"math"
)

type lsPoint struct{ t, y, w float64 }

func lsPoints(d *data) []lsPoint {
	var out []lsPoint
	for _, g := range d.groups {
		var ys []float64
		for _, si := range g.series {
			for _, p := range d.series[si].srch {
				ys = append(ys, p.y)
			}
		}
		sd := mad(ys, median(ys))
		if !(sd > 0) {
			continue
		}
		for _, si := range g.series {
			for _, p := range d.series[si].srch {
				e := fitErr(p.e) / sd
				out = append(out, lsPoint{t: p.t - d.t0, y: p.y / sd, w: p.w / (e * e)})
			}
		}
	}
	var sw float64
	for _, p := range out {
		sw += p.w
	}
	for i := range out {
		out[i].w /= sw
	}
	return out
}

func lsPower(pts []lsPoint, f0, df float64, n int) []float64 {
	var ym, yy float64
	for _, p := range pts {
		ym += p.w * p.y
	}
	for _, p := range pts {
		yy += p.w * (p.y - ym) * (p.y - ym)
	}
	pow := make([]float64, n)
	const block = 256
	parallel(n, func(lo, hi int) {
		c := make([]float64, block)
		s := make([]float64, block)
		yc := make([]float64, block)
		ys := make([]float64, block)
		cc := make([]float64, block)
		cs := make([]float64, block)
		for b := lo; b < hi; b += block {
			m := min(block, hi-b)
			clear(c)
			clear(s)
			clear(yc)
			clear(ys)
			clear(cc)
			clear(cs)
			fb := f0 + float64(b)*df
			for _, p := range pts {
				a := 2 * math.Pi * fb * p.t
				ca, sa := math.Cos(a), math.Sin(a)
				da := 2 * math.Pi * df * p.t
				cd, sd := math.Cos(da), math.Sin(da)
				yw := p.w * (p.y - ym)
				for k := range m {
					c[k] += p.w * ca
					s[k] += p.w * sa
					yc[k] += yw * ca
					ys[k] += yw * sa
					cc[k] += p.w * ca * ca
					cs[k] += p.w * ca * sa
					ca, sa = ca*cd-sa*sd, sa*cd+ca*sd
				}
			}
			for k := range m {
				ccx := cc[k] - c[k]*c[k]
				ssx := 1 - cc[k] - s[k]*s[k]
				csx := cs[k] - c[k]*s[k]
				dd := ccx*ssx - csx*csx
				if dd <= 0 || yy <= 0 {
					continue
				}
				pow[b+k] = (ssx*yc[k]*yc[k] + ccx*ys[k]*ys[k] - 2*csx*yc[k]*ys[k]) / (yy * dd)
			}
		}
	})
	return pow
}

func lsSearch(d *data, minP, maxP float64) []float64 {
	pts := lsPoints(d)
	if len(pts) < 20 {
		return nil
	}
	span := d.t1 - d.t0
	df := 1 / (3 * span)
	f0 := 1 / maxP
	n := int((1/minP - f0) / df)
	if n < 10 {
		return nil
	}
	pow := lsPower(pts, f0, df, n)
	var out []float64
	for _, i := range localMaxima(pow, 5) {
		fine := lsPower(pts, f0+(float64(i)-1)*df, df/20, 41)
		bj := 0
		for j := range fine {
			if fine[j] > fine[bj] {
				bj = j
			}
		}
		out = append(out, 1/(f0+(float64(i)-1)*df+float64(bj)*df/20))
	}
	return out
}
