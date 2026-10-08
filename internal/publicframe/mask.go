package publicframe

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"io"
	"math"

	"github.com/USA-RedDragon/astro-stacker/internal/preview"
)

// The sky mask fades the watermark out where the master is bright. There a
// sub's stretch is steep or clipped, which would distort it, and nebula and
// stars are what a viewer looks at. Levels are of the master's own STF
// stretch (sky at 0.25).
const (
	maskLow  = 0.5  // full watermark below this
	maskHigh = 0.85 // none above this
	maskSoft = 4    // softens the mask's edge, in mask pixels
)

// maskGrow widens bright areas, in mask pixels (a mask pixel is about half
// an output pixel), so star halos that vary with seeing are left out too.
// Wider, a rich star field perforates the letters until they don't read.
var maskGrow = 2

// SkyMask is the watermark's weight on the reference grid, from a binned
// copy of the target's master.
type SkyMask struct {
	w, h  int
	scale float64 // reference pixels per mask pixel
	m     []float32
}

// NewSkyMask builds the mask from a linear plane w×h, binned by scale from
// the reference grid.
func NewSkyMask(plane []float32, w, h int, scale float64) *SkyMask {
	s := preview.Stretch(plane)
	s = localMax(s, w, h, maskGrow)
	s = boxBlur(boxBlur(s, w, h, maskSoft), w, h, maskSoft)
	m := make([]float32, len(s))
	for i, v := range s {
		m[i] = float32(math.Max(0, math.Min(1, (maskHigh-float64(v))/(maskHigh-maskLow))))
	}
	return &SkyMask{w: w, h: h, scale: scale, m: m}
}

// At is the weight at reference pixel (x, y); 1 everywhere for a nil mask.
func (s *SkyMask) At(x, y float64) float64 {
	if s == nil {
		return 1
	}
	mx := min(max(x/s.scale-0.5, 0), float64(s.w-1))
	my := min(max(y/s.scale-0.5, 0), float64(s.h-1))
	ix, iy := int(mx), int(my)
	jx, jy := min(ix+1, s.w-1), min(iy+1, s.h-1)
	fx, fy := mx-float64(ix), my-float64(iy)
	at := func(x, y int) float64 { return float64(s.m[y*s.w+x]) }
	return (1-fy)*((1-fx)*at(ix, iy)+fx*at(jx, iy)) + fy*((1-fx)*at(ix, jy)+fx*at(jx, jy))
}

// linearMagic starts a master's linear preview (stacking.LinearMagic).
const linearMagic = "APLP"

// DecodeLinear reads a master's gzipped linear preview.
func DecodeLinear(b []byte) ([]float32, int, int, error) {
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, 0, 0, err
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		return nil, 0, 0, err
	}
	if len(raw) < 12 || string(raw[:4]) != linearMagic {
		return nil, 0, 0, fmt.Errorf("not a linear preview")
	}
	w := int(binary.LittleEndian.Uint32(raw[4:]))
	h := int(binary.LittleEndian.Uint32(raw[8:]))
	if w <= 0 || h <= 0 || len(raw) != 12+4*w*h {
		return nil, 0, 0, fmt.Errorf("linear preview is %d bytes for %dx%d", len(raw), w, h)
	}
	out := make([]float32, w*h)
	if err := binary.Read(bytes.NewReader(raw[12:]), binary.LittleEndian, out); err != nil {
		return nil, 0, 0, err
	}
	return out, w, h, nil
}

func localMax(in []float32, w, h, r int) []float32 {
	tmp := make([]float32, len(in))
	for y := range h {
		for x := range w {
			m := in[y*w+x]
			for dx := max(0, x-r); dx <= min(w-1, x+r); dx++ {
				m = max(m, in[y*w+dx])
			}
			tmp[y*w+x] = m
		}
	}
	out := make([]float32, len(in))
	for y := range h {
		for x := range w {
			m := tmp[y*w+x]
			for dy := max(0, y-r); dy <= min(h-1, y+r); dy++ {
				m = max(m, tmp[dy*w+x])
			}
			out[y*w+x] = m
		}
	}
	return out
}

func localMin(in []float32, w, h, r int) []float32 {
	neg := make([]float32, len(in))
	for i, v := range in {
		neg[i] = -v
	}
	out := localMax(neg, w, h, r)
	for i := range out {
		out[i] = -out[i]
	}
	return out
}

func boxBlur(in []float32, w, h, r int) []float32 {
	tmp := make([]float32, len(in))
	for y := range h {
		for x := range w {
			var s float32
			n := 0
			for dx := max(0, x-r); dx <= min(w-1, x+r); dx++ {
				s += in[y*w+dx]
				n++
			}
			tmp[y*w+x] = s / float32(n)
		}
	}
	out := make([]float32, len(in))
	for y := range h {
		for x := range w {
			var s float32
			n := 0
			for dy := max(0, y-r); dy <= min(h-1, y+r); dy++ {
				s += tmp[dy*w+x]
				n++
			}
			out[y*w+x] = s / float32(n)
		}
	}
	return out
}
