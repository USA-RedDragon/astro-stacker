package goals

import (
	"math"
	"slices"
)

const madScale = 1.4826

func gauss1D(sigma float64) []float64 {
	r := int(4*sigma + 0.5)
	k := make([]float64, 2*r+1)
	var s float64
	for i := -r; i <= r; i++ {
		v := math.Exp(-0.5 * float64(i*i) / (sigma * sigma))
		k[i+r] = v
		s += v
	}
	for i := range k {
		k[i] /= s
	}
	return k
}

func reflectIndex(i, n int) int {
	if n == 1 {
		return 0
	}
	period := 2 * n
	i %= period
	if i < 0 {
		i += period
	}
	if i >= n {
		i = period - 1 - i
	}
	return i
}

func gaussian(in []float64, w, h int, sigma float64) []float64 {
	k := gauss1D(sigma)
	r := len(k) / 2
	tmp := make([]float64, len(in))
	line := make([]float64, max(w, h)+2*r)
	for y := range h {
		row := in[y*w : (y+1)*w]
		for i := range w + 2*r {
			line[i] = row[reflectIndex(i-r, w)]
		}
		out := tmp[y*w : (y+1)*w]
		for x := range w {
			var s float64
			seg := line[x : x+2*r+1]
			for j, kv := range k {
				s += kv * seg[j]
			}
			out[x] = s
		}
	}
	res := make([]float64, len(in))
	col := make([]float64, h)
	for x := range w {
		for i := range h + 2*r {
			line[i] = tmp[reflectIndex(i-r, h)*w+x]
		}
		for y := range h {
			var s float64
			seg := line[y : y+2*r+1]
			for j, kv := range k {
				s += kv * seg[j]
			}
			col[y] = s
		}
		for y := range h {
			res[y*w+x] = col[y]
		}
	}
	return res
}

func maskedGaussian(img []float64, mask []bool, w, h int, sigma float64) []float64 {
	num := make([]float64, len(img))
	den := make([]float64, len(img))
	for i, m := range mask {
		if m {
			num[i] = img[i]
			den[i] = 1
		}
	}
	num = gaussian(num, w, h, sigma)
	den = gaussian(den, w, h, sigma)
	for i := range num {
		num[i] /= math.Max(den[i], 1e-6)
	}
	return num
}

func dilate(mask []bool, w, h, r int) []bool {
	out := make([]bool, len(mask))
	type off struct{ dx, dy int }
	var offs []off
	for dy := -r; dy <= r; dy++ {
		for dx := -r; dx <= r; dx++ {
			if dx*dx+dy*dy <= r*r {
				offs = append(offs, off{dx, dy})
			}
		}
	}
	for y := range h {
		for x := range w {
			if !mask[y*w+x] {
				continue
			}
			for _, o := range offs {
				xx, yy := x+o.dx, y+o.dy
				if xx >= 0 && xx < w && yy >= 0 && yy < h {
					out[yy*w+xx] = true
				}
			}
		}
	}
	return out
}

func selectValues(img []float64, mask []bool) []float64 {
	var out []float64
	for i, m := range mask {
		if m && !math.IsNaN(img[i]) {
			out = append(out, img[i])
		}
	}
	return out
}

func percentileSorted(s []float64, p float64) float64 {
	if len(s) == 0 {
		return math.NaN()
	}
	pos := p / 100 * float64(len(s)-1)
	lo := int(math.Floor(pos))
	hi := min(lo+1, len(s)-1)
	f := pos - float64(lo)
	return s[lo]*(1-f) + s[hi]*f
}

func percentile(v []float64, p float64) float64 {
	s := slices.Clone(v)
	slices.Sort(s)
	return percentileSorted(s, p)
}

func median(v []float64) float64 {
	return percentile(v, 50)
}

func mad(v []float64) float64 {
	if len(v) == 0 {
		return math.NaN()
	}
	m := median(v)
	d := make([]float64, len(v))
	for i, x := range v {
		d[i] = math.Abs(x - m)
	}
	return madScale * median(d)
}

func count(mask []bool) int {
	n := 0
	for _, m := range mask {
		if m {
			n++
		}
	}
	return n
}

func and(a, b []bool) []bool {
	out := make([]bool, len(a))
	for i := range a {
		out[i] = a[i] && b[i]
	}
	return out
}

func rasterize(poly []Point, bw, bh, w, h int) []bool {
	out := make([]bool, bw*bh)
	if len(poly) < 3 || w <= 0 || h <= 0 {
		return out
	}
	for by := range bh {
		fy := (float64(by)*NoiseBin + NoiseBin/2.0) / float64(h)
		for bx := range bw {
			fx := (float64(bx)*NoiseBin + NoiseBin/2.0) / float64(w)
			out[by*bw+bx] = insidePolygon(poly, fx, fy)
		}
	}
	return out
}

func insidePolygon(poly []Point, x, y float64) bool {
	in := false
	j := len(poly) - 1
	for i := range poly {
		pi, pj := poly[i], poly[j]
		if (pi.Y > y) != (pj.Y > y) && x < (pj.X-pi.X)*(y-pi.Y)/(pj.Y-pi.Y)+pi.X {
			in = !in
		}
		j = i
	}
	return in
}
