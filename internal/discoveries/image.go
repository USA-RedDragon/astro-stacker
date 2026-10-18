// Package discoveries finds what changed in a target's field from one night
// to the others: variable stars, supernovae, asteroids and anything new.
// Each night's registered subs are stacked and compared with a reference
// stacked from the same field's other nights, after matching their star
// profiles, and what stands out in the difference is checked against the
// variable star, minor planet and alert catalogues.
package discoveries

import (
	"math"
	"slices"

	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
)

// Plane is one image with row 0 at the top. NaN marks pixels no data covers.
type Plane struct {
	W, H int
	Data []float32
	// Sensor is a registered sub's map to the sensor, when its
	// registration kept one.
	Sensor *SensorMap
}

func NewPlane(w, h int) *Plane { return &Plane{W: w, H: h, Data: make([]float32, w*h)} }

func (p *Plane) At(x, y int) float32 {
	if x < 0 || y < 0 || x >= p.W || y >= p.H {
		return float32(math.NaN())
	}
	return p.Data[y*p.W+x]
}

func isNaN32(v float32) bool { return v != v }

// Mesh is a background and noise map sampled on a grid of square boxes, as
// SExtractor and sep build it.
type Mesh struct {
	NX, NY, Box int
	W, H        int
	Bg, RMS     []float32
}

const (
	meshClipSigma = 3.0
	meshClipIters = 5
	meshMinFill   = 0.5
	meshStride    = 2
)

// Background measures the sky in boxes of box pixels, ignoring NaN and
// masked pixels: a sigma-clipped mode (or median when the box is skewed by
// a source) and the clipped standard deviation, median filtered over 3x3
// boxes. Boxes with too few usable pixels take their neighbours' values.
func Background(p *Plane, box int, mask []bool) *Mesh {
	nx, ny := (p.W+box-1)/box, (p.H+box-1)/box
	m := &Mesh{NX: nx, NY: ny, Box: box, W: p.W, H: p.H, Bg: make([]float32, nx*ny), RMS: make([]float32, nx*ny)}
	have := make([]bool, nx*ny)
	buf := make([]float32, 0, (box/meshStride+1)*(box/meshStride+1))
	for by := range ny {
		for bx := range nx {
			buf = buf[:0]
			total := 0
			for y := by * box; y < min((by+1)*box, p.H); y += meshStride {
				row := y * p.W
				for x := bx * box; x < min((bx+1)*box, p.W); x += meshStride {
					total++
					v := p.Data[row+x]
					if isNaN32(v) || (mask != nil && mask[row+x]) {
						continue
					}
					buf = append(buf, v)
				}
			}
			if total == 0 || float64(len(buf)) < meshMinFill*float64(total) {
				continue
			}
			bg, rms, ok := clippedMode(buf)
			if !ok {
				continue
			}
			i := by*nx + bx
			m.Bg[i], m.RMS[i], have[i] = bg, rms, true
		}
	}
	fillMissing(m.Bg, m.RMS, have, nx, ny)
	m.Bg = medianFilter3(m.Bg, nx, ny)
	m.RMS = medianFilter3(m.RMS, nx, ny)
	return m
}

func clippedMode(v []float32) (bg, rms float32, ok bool) {
	lo, hi := math.Inf(-1), math.Inf(1)
	var mean, sd float64
	n := 0
	for range meshClipIters {
		var s, s2 float64
		n = 0
		for _, x := range v {
			f := float64(x)
			if f < lo || f > hi {
				continue
			}
			s += f
			s2 += f * f
			n++
		}
		if n < 10 {
			return 0, 0, false
		}
		mean = s / float64(n)
		sd = math.Sqrt(math.Max(s2/float64(n)-mean*mean, 0))
		nlo, nhi := mean-meshClipSigma*sd, mean+meshClipSigma*sd
		if nlo == lo && nhi == hi {
			break
		}
		lo, hi = nlo, nhi
	}
	kept := make([]float32, 0, n)
	for _, x := range v {
		if f := float64(x); f >= lo && f <= hi {
			kept = append(kept, x)
		}
	}
	if len(kept) == 0 {
		return 0, 0, false
	}
	med := float64(median32(kept))
	b := med
	if sd > 0 && math.Abs(mean-med)/sd < 0.3 {
		b = 2.5*med - 1.5*mean
	}
	return float32(b), float32(sd), true
}

func median32(v []float32) float32 {
	if len(v) == 0 {
		return float32(math.NaN())
	}
	c := slices.Clone(v)
	slices.Sort(c)
	n := len(c)
	if n%2 == 1 {
		return c[n/2]
	}
	return (c[n/2-1] + c[n/2]) / 2
}

func median64(v []float64) float64 {
	if len(v) == 0 {
		return math.NaN()
	}
	c := slices.Clone(v)
	slices.Sort(c)
	n := len(c)
	if n%2 == 1 {
		return c[n/2]
	}
	return (c[n/2-1] + c[n/2]) / 2
}

func fillMissing(bg, rms []float32, have []bool, nx, ny int) {
	some := false
	for _, h := range have {
		some = some || h
	}
	if !some {
		return
	}
	for {
		missing := false
		next := slices.Clone(have)
		for y := range ny {
			for x := range nx {
				i := y*nx + x
				if have[i] {
					continue
				}
				var sb, sr float32
				n := 0
				for dy := -1; dy <= 1; dy++ {
					for dx := -1; dx <= 1; dx++ {
						xx, yy := x+dx, y+dy
						if xx < 0 || yy < 0 || xx >= nx || yy >= ny || !have[yy*nx+xx] {
							continue
						}
						sb += bg[yy*nx+xx]
						sr += rms[yy*nx+xx]
						n++
					}
				}
				if n == 0 {
					missing = true
					continue
				}
				bg[i], rms[i], next[i] = sb/float32(n), sr/float32(n), true
			}
		}
		copy(have, next)
		if !missing {
			return
		}
	}
}

func medianFilter3(v []float32, nx, ny int) []float32 {
	out := make([]float32, len(v))
	buf := make([]float32, 0, 9)
	for y := range ny {
		for x := range nx {
			buf = buf[:0]
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					xx, yy := x+dx, y+dy
					if xx >= 0 && yy >= 0 && xx < nx && yy < ny {
						buf = append(buf, v[yy*nx+xx])
					}
				}
			}
			out[y*nx+x] = median32(buf)
		}
	}
	return out
}

// interp returns the bilinear interpolation of grid at pixel (x, y), with
// the grid's samples at box centres.
func (m *Mesh) interp(grid []float32, x, y float64) float32 {
	fx := (x+0.5)/float64(m.Box) - 0.5
	fy := (y+0.5)/float64(m.Box) - 0.5
	fx = math.Max(0, math.Min(fx, float64(m.NX-1)))
	fy = math.Max(0, math.Min(fy, float64(m.NY-1)))
	x0, y0 := int(fx), int(fy)
	x1, y1 := min(x0+1, m.NX-1), min(y0+1, m.NY-1)
	tx, ty := float32(fx-float64(x0)), float32(fy-float64(y0))
	a := grid[y0*m.NX+x0]*(1-tx) + grid[y0*m.NX+x1]*tx
	b := grid[y1*m.NX+x0]*(1-tx) + grid[y1*m.NX+x1]*tx
	return a*(1-ty) + b*ty
}

func (m *Mesh) BgAt(x, y float64) float32  { return m.interp(m.Bg, x, y) }
func (m *Mesh) RMSAt(x, y float64) float32 { return m.interp(m.RMS, x, y) }

// rowInterp fills out with grid interpolated along row y.
func (m *Mesh) rowInterp(grid []float32, y int, out []float32) {
	fy := (float64(y)+0.5)/float64(m.Box) - 0.5
	fy = math.Max(0, math.Min(fy, float64(m.NY-1)))
	y0 := int(fy)
	y1 := min(y0+1, m.NY-1)
	ty := float32(fy - float64(y0))
	col := make([]float32, m.NX)
	for i := range m.NX {
		col[i] = grid[y0*m.NX+i]*(1-ty) + grid[y1*m.NX+i]*ty
	}
	for x := range out {
		fx := (float64(x)+0.5)/float64(m.Box) - 0.5
		fx = math.Max(0, math.Min(fx, float64(m.NX-1)))
		x0 := int(fx)
		x1 := min(x0+1, m.NX-1)
		tx := float32(fx - float64(x0))
		out[x] = col[x0]*(1-tx) + col[x1]*tx
	}
}

// Subtract removes the background from p in place.
func (m *Mesh) Subtract(p *Plane) {
	row := make([]float32, p.W)
	for y := range p.H {
		m.rowInterp(m.Bg, y, row)
		d := p.Data[y*p.W : (y+1)*p.W]
		for x := range d {
			d[x] -= row[x]
		}
	}
}

// RMSPlane is the noise map at every pixel.
func (m *Mesh) RMSPlane() []float32 {
	out := make([]float32, m.W*m.H)
	for y := range m.H {
		m.rowInterp(m.RMS, y, out[y*m.W:(y+1)*m.W])
	}
	return out
}

// GlobalRMS is the median of the boxes' noise.
func (m *Mesh) GlobalRMS() float64 { return float64(median32(m.RMS)) }

const apertureSubpix = 5

// Aperture sums p in a circle of radius r around (x, y), splitting pixels
// on the edge into 5x5 subpixels as sep does. NaN pixels count as zero.
func Aperture(p *Plane, x, y, r float64) float64 {
	x0, x1 := int(math.Floor(x-r-0.5)), int(math.Ceil(x+r+0.5))
	y0, y1 := int(math.Floor(y-r-0.5)), int(math.Ceil(y+r+0.5))
	r2 := r * r
	var s float64
	for yy := max(y0, 0); yy <= min(y1, p.H-1); yy++ {
		for xx := max(x0, 0); xx <= min(x1, p.W-1); xx++ {
			v := p.Data[yy*p.W+xx]
			if isNaN32(v) {
				continue
			}
			dx, dy := float64(xx)-x, float64(yy)-y
			near := math.Hypot(math.Max(math.Abs(dx)-0.5, 0), math.Max(math.Abs(dy)-0.5, 0))
			if near*near > r2 {
				continue
			}
			far := math.Hypot(math.Abs(dx)+0.5, math.Abs(dy)+0.5)
			if far*far <= r2 {
				s += float64(v)
				continue
			}
			in := 0
			for sy := range apertureSubpix {
				for sx := range apertureSubpix {
					ox := dx - 0.5 + (float64(sx)+0.5)/apertureSubpix
					oy := dy - 0.5 + (float64(sy)+0.5)/apertureSubpix
					if ox*ox+oy*oy <= r2 {
						in++
					}
				}
			}
			s += float64(v) * float64(in) / (apertureSubpix * apertureSubpix)
		}
	}
	return s
}

// HalfFluxRadius is the radius holding half the flux inside rmax, as sep's
// flux_radius with frac 0.5; NaN when the flux is not positive.
func HalfFluxRadius(p *Plane, x, y, rmax float64) float64 {
	total := Aperture(p, x, y, rmax)
	if !(total > 0) {
		return math.NaN()
	}
	lo, hi := 0.0, rmax
	for range 24 {
		mid := (lo + hi) / 2
		if Aperture(p, x, y, mid) < total/2 {
			lo = mid
		} else {
			hi = mid
		}
	}
	return (lo + hi) / 2
}

// clippedStd is the median and standard deviation of v after 3-sigma
// clipping about the median, as astropy's sigma_clipped_stats.
func clippedStd(v []float64) (med, sd float64) {
	lo, hi := math.Inf(-1), math.Inf(1)
	kept := make([]float64, 0, len(v))
	for range 5 {
		kept = kept[:0]
		for _, x := range v {
			if x >= lo && x <= hi {
				kept = append(kept, x)
			}
		}
		if len(kept) < 2 {
			return math.NaN(), math.NaN()
		}
		med = median64(kept)
		var s, s2 float64
		for _, x := range kept {
			s += x
			s2 += x * x
		}
		n := float64(len(kept))
		mean := s / n
		sd = math.Sqrt(math.Max(s2/n-mean*mean, 0))
		nlo, nhi := med-3*sd, med+3*sd
		if nlo == lo && nhi == hi {
			break
		}
		lo, hi = nlo, nhi
	}
	return med, sd
}

// PlaneOf takes an image's first channel, with its empty (exactly zero)
// pixels as NaN. A single-channel image's samples are reused, not copied.
func PlaneOf(im *imagedata.Image) *Plane {
	src := im.Plane(0)
	data := src
	if im.C != 1 {
		data = make([]float32, len(src))
	}
	for i, v := range src {
		if v == 0 || isNaN32(v) {
			data[i] = float32(math.NaN())
		} else {
			data[i] = v
		}
	}
	p := &Plane{W: im.W, H: im.H, Data: data}
	if m, ok := SensorMapFrom(im.Numbers[imagedata.PropSensorMap]); ok {
		p.Sensor = &m
	}
	return p
}
