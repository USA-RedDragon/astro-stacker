package dustcheck

import (
	"math"
	"slices"
)

// Map is a single-plane float image, rows top first.
type Map struct {
	W, H int
	Pix  []float32
}

// NewMap is a zeroed w×h map.
func NewMap(w, h int) Map { return Map{W: w, H: h, Pix: make([]float32, w*h)} }

// At is the pixel at x, y.
func (m Map) At(x, y int) float32 { return m.Pix[y*m.W+x] }

func (m Map) clone() Map {
	return Map{W: m.W, H: m.H, Pix: slices.Clone(m.Pix)}
}

// Bin averages b×b blocks, dropping the ragged edge, as numpy reshape-mean.
func Bin(pix []float32, w, h, b int) Map {
	ow, oh := w/b, h/b
	out := NewMap(ow, oh)
	acc := make([]float64, ow)
	n := float64(b * b)
	for oy := range oh {
		clear(acc)
		for y := oy * b; y < oy*b+b; y++ {
			row := pix[y*w : y*w+ow*b]
			for ox := range ow {
				s := 0.0
				for _, v := range row[ox*b : ox*b+b] {
					s += float64(v)
				}
				acc[ox] += s
			}
		}
		for ox := range ow {
			out.Pix[oy*ow+ox] = float32(acc[ox] / n)
		}
	}
	return out
}

// median of the non-NaN values; it reorders vals.
func median(vals []float32) float32 {
	n := 0
	for _, v := range vals {
		if !isNaN(v) {
			vals[n] = v
			n++
		}
	}
	vals = vals[:n]
	if n == 0 {
		return float32(math.NaN())
	}
	k := n / 2
	hi := selectK(vals, k)
	if n%2 == 1 {
		return hi
	}
	lo := vals[0]
	for _, v := range vals[:k] {
		lo = max(lo, v)
	}
	return float32((float64(lo) + float64(hi)) / 2)
}

// selectK puts the k-th smallest value at index k, smaller ones before it.
func selectK(a []float32, k int) float32 {
	lo, hi := 0, len(a)-1
	for lo < hi {
		p := a[(lo+hi)/2]
		i, j := lo, hi
		for i <= j {
			for a[i] < p {
				i++
			}
			for a[j] > p {
				j--
			}
			if i <= j {
				a[i], a[j] = a[j], a[i]
				i++
				j--
			}
		}
		switch {
		case k <= j:
			hi = j
		case k >= i:
			lo = i
		default:
			return a[k]
		}
	}
	return a[k]
}

func isNaN(v float32) bool { return v != v }

// reflect maps an index outside [0, n) back inside, mirroring about the
// edge with the edge sample repeated (scipy's "reflect").
func reflect(i, n int) int {
	if n == 1 {
		return 0
	}
	p := 2 * n
	i %= p
	if i < 0 {
		i += p
	}
	if i >= n {
		i = p - 1 - i
	}
	return i
}

func clamp(i, n int) int { return min(max(i, 0), n-1) }

// MedianFilter is a size×size moving median of the non-NaN values with
// reflected edges. Each row keeps its window sorted and slides it one
// column at a time.
func MedianFilter(m Map, size int) Map {
	r := size / 2
	out := NewMap(m.W, m.H)
	rows := make([]int, 2*r+1)
	win := make([]float32, 0, (2*r+1)*(2*r+1))
	for y := range m.H {
		for dy := -r; dy <= r; dy++ {
			rows[dy+r] = reflect(y+dy, m.H) * m.W
		}
		win = win[:0]
		for dx := -r; dx <= r; dx++ {
			c := reflect(dx, m.W)
			for _, row := range rows {
				if v := m.Pix[row+c]; !isNaN(v) {
					win = append(win, v)
				}
			}
		}
		slices.Sort(win)
		for x := range m.W {
			if x > 0 {
				drop, add := reflect(x-1-r, m.W), reflect(x+r, m.W)
				for _, row := range rows {
					win = slide(win, m.Pix[row+drop], m.Pix[row+add])
				}
			}
			out.Pix[y*m.W+x] = sortedMedian(win)
		}
	}
	return out
}

// slide takes a out of the sorted window and puts b in, either of which
// may be NaN for none.
func slide(win []float32, a, b float32) []float32 {
	switch {
	case isNaN(a) && isNaN(b):
		return win
	case isNaN(a):
		return slices.Insert(win, lowerBound(win, b), b)
	case isNaN(b):
		i := lowerBound(win, a)
		return slices.Delete(win, i, i+1)
	}
	i, j := lowerBound(win, a), lowerBound(win, b)
	if j > i {
		copy(win[i:j-1], win[i+1:j])
		win[j-1] = b
	} else {
		copy(win[j+1:i+1], win[j:i])
		win[j] = b
	}
	return win
}

func lowerBound(s []float32, v float32) int {
	lo, hi := 0, len(s)
	for lo < hi {
		m := int(uint(lo+hi) >> 1)
		if s[m] < v {
			lo = m + 1
		} else {
			hi = m
		}
	}
	return lo
}

func sortedMedian(s []float32) float32 {
	n := len(s)
	if n == 0 {
		return float32(math.NaN())
	}
	k := n / 2
	if n%2 == 1 {
		return s[k]
	}
	return float32((float64(s[k-1]) + float64(s[k])) / 2)
}

// fillNaN replaces NaN pixels with the median of the valid ones in the 5×5
// around them, growing inwards until none are left or after 40 passes.
func fillNaN(m Map) {
	buf := make([]float32, 0, 25)
	next := m.clone()
	for range 40 {
		holes := false
		for y := range m.H {
			for x := range m.W {
				i := y*m.W + x
				if !isNaN(m.Pix[i]) {
					continue
				}
				holes = true
				buf = buf[:0]
				for dy := -2; dy <= 2; dy++ {
					row := clamp(y+dy, m.H) * m.W
					for dx := -2; dx <= 2; dx++ {
						buf = append(buf, m.Pix[row+clamp(x+dx, m.W)])
					}
				}
				next.Pix[i] = median(buf)
			}
		}
		if !holes {
			return
		}
		copy(m.Pix, next.Pix)
	}
}

// Gaussian blurs with a Gaussian of sigma pixels, truncated at 4 sigma, with
// reflected edges, as scipy's gaussian_filter.
func Gaussian(m Map, sigma float64) Map {
	r := int(4*sigma + 0.5)
	k := make([]float64, 2*r+1)
	s := 0.0
	for i := -r; i <= r; i++ {
		k[i+r] = math.Exp(-0.5 * float64(i*i) / (sigma * sigma))
		s += k[i+r]
	}
	for i := range k {
		k[i] /= s
	}
	tmp := NewMap(m.W, m.H)
	for y := range m.H {
		row := m.Pix[y*m.W : (y+1)*m.W]
		for x := range m.W {
			a := 0.0
			for i := -r; i <= r; i++ {
				a += k[i+r] * float64(row[reflect(x+i, m.W)])
			}
			tmp.Pix[y*m.W+x] = float32(a)
		}
	}
	out := NewMap(m.W, m.H)
	for y := range m.H {
		for x := range m.W {
			a := 0.0
			for i := -r; i <= r; i++ {
				a += k[i+r] * float64(tmp.Pix[reflect(y+i, m.H)*m.W+x])
			}
			out.Pix[y*m.W+x] = float32(a)
		}
	}
	return out
}

// dilate grows a mask by n steps of 4-connected neighbours: the pixels
// within taxicab distance n of a set pixel.
func dilate(mask []bool, w, h, n int) []bool {
	cur := slices.Clone(mask)
	next := make([]bool, len(mask))
	for range n {
		for y := range h {
			for x := range w {
				i := y*w + x
				next[i] = cur[i] || (x > 0 && cur[i-1]) || (x < w-1 && cur[i+1]) || (y > 0 && cur[i-w]) || (y < h-1 && cur[i+w])
			}
		}
		cur, next = next, cur
	}
	return cur
}

// splineCoeffs turns samples into cubic B-spline coefficients in place,
// with mirrored edges.
func splineCoeffs(c []float64) {
	n := len(c)
	if n < 2 {
		return
	}
	z := math.Sqrt(3) - 2
	const gain = 6.0
	for i := range c {
		c[i] *= gain
	}
	horizon := min(n, int(math.Ceil(math.Log(1e-12)/math.Log(math.Abs(z)))))
	zn := z
	s := c[0]
	for i := 1; i < horizon; i++ {
		s += zn * c[i]
		zn *= z
	}
	c[0] = s
	for i := 1; i < n; i++ {
		c[i] += z * c[i-1]
	}
	c[n-1] = (z / (z*z - 1)) * (z*c[n-2] + c[n-1])
	for i := n - 2; i >= 0; i-- {
		c[i] = z * (c[i+1] - c[i])
	}
}

func bspline3(t float64) float64 {
	t = math.Abs(t)
	switch {
	case t < 1:
		return 2.0/3 - t*t + t*t*t/2
	case t < 2:
		u := 2 - t
		return u * u * u / 6
	}
	return 0
}

// Zoom resamples to w×h with cubic B-splines, mapping the corner samples
// onto each other as scipy's zoom does.
func Zoom(m Map, w, h int) Map {
	coef := make([]float64, m.W*m.H)
	for i, v := range m.Pix {
		coef[i] = float64(v)
	}
	row := make([]float64, m.W)
	for y := range m.H {
		copy(row, coef[y*m.W:(y+1)*m.W])
		splineCoeffs(row)
		copy(coef[y*m.W:], row)
	}
	col := make([]float64, m.H)
	for x := range m.W {
		for y := range m.H {
			col[y] = coef[y*m.W+x]
		}
		splineCoeffs(col)
		for y := range m.H {
			coef[y*m.W+x] = col[y]
		}
	}
	sx := float64(m.W-1) / float64(max(1, w-1))
	sy := float64(m.H-1) / float64(max(1, h-1))
	tmp := make([]float64, w*m.H)
	for y := range m.H {
		for x := range w {
			p := float64(x) * sx
			i0 := int(math.Floor(p))
			a := 0.0
			for i := i0 - 1; i <= i0+2; i++ {
				a += coef[y*m.W+reflect(i, m.W)] * bspline3(p-float64(i))
			}
			tmp[y*w+x] = a
		}
	}
	out := NewMap(w, h)
	for y := range h {
		p := float64(y) * sy
		i0 := int(math.Floor(p))
		for x := range w {
			a := 0.0
			for i := i0 - 1; i <= i0+2; i++ {
				a += tmp[reflect(i, m.H)*w+x] * bspline3(p-float64(i))
			}
			out.Pix[y*w+x] = float32(a)
		}
	}
	return out
}
