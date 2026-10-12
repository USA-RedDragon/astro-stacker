package horizonsurvey

import (
	"fmt"
	"io"
	"math"
	"slices"
	"strings"
)

const (
	BinWidth   = 0.1
	minSamples = 4
	maxErrFrac = 0.2
)

type BinKind int

const (
	BinNone BinKind = iota
	BinEdge
	BinBelow
	BinAbove
)

type FrameBin struct {
	Bin    int
	Kind   BinKind
	Alt    float64
	AltLo  float64
	AltHi  float64
	ErrFrc float64
}

type sample struct {
	alt float64
	sky bool
}

func binOf(az float64) int {
	n := int(math.Round(360 / BinWidth))
	return int(math.Floor(wrap360(az)/BinWidth)) % n
}

func BinCenter(b int) float64 { return (float64(b) + 0.5) * BinWidth }

func FrameSkyline(g Geometry, m *Mask) []FrameBin {
	bins := map[int][]sample{}
	for by := range m.BH {
		for bx := range m.BW {
			x, y := m.Center(bx, by)
			az, alt := AltAz(g.Dir(x, y))
			b := binOf(az)
			bins[b] = append(bins[b], sample{alt: alt, sky: m.Sky[by*m.BW+bx]})
		}
	}
	out := make([]FrameBin, 0, len(bins))
	for b, s := range bins {
		if len(s) < minSamples {
			continue
		}
		if fb, ok := changepoint(b, s); ok {
			out = append(out, fb)
		}
	}
	slices.SortFunc(out, func(a, b FrameBin) int { return a.Bin - b.Bin })
	return out
}

func changepoint(b int, s []sample) (FrameBin, bool) {
	slices.SortFunc(s, func(a, b sample) int {
		switch {
		case a.alt < b.alt:
			return -1
		case a.alt > b.alt:
			return 1
		}
		return 0
	})
	n := len(s)
	obstAbove := 0
	for _, x := range s {
		if !x.sky {
			obstAbove++
		}
	}
	skyBelow := 0
	bestK, bestErr := 0, obstAbove
	for k := 1; k <= n; k++ {
		if s[k-1].sky {
			skyBelow++
		} else {
			obstAbove--
		}
		if e := skyBelow + obstAbove; e <= bestErr {
			bestK, bestErr = k, e
		}
	}
	fb := FrameBin{Bin: b, AltLo: s[0].alt, AltHi: s[n-1].alt, ErrFrc: float64(bestErr) / float64(n)}
	if fb.ErrFrc > maxErrFrac {
		return fb, false
	}
	switch bestK {
	case 0:
		fb.Kind, fb.Alt = BinBelow, s[0].alt
	case n:
		fb.Kind, fb.Alt = BinAbove, s[n-1].alt
	default:
		fb.Kind, fb.Alt = BinEdge, (s[bestK-1].alt+s[bestK].alt)/2
	}
	return fb, true
}

type SkyBin struct {
	Az       float64
	Alt      float64
	Kind     BinKind
	Measured bool
	Edges    int
	Conflict bool
	Lower    float64
	Upper    float64
}

type Skyline struct {
	Bins []SkyBin
}

const boundTolerance = 1.0

func Combine(frames [][]FrameBin, bottom, top float64) *Skyline {
	n := int(math.Round(360 / BinWidth))
	edges := make([][]float64, n)
	sl := &Skyline{Bins: make([]SkyBin, n)}
	for i := range n {
		sl.Bins[i] = SkyBin{Az: BinCenter(i), Lower: math.Inf(-1), Upper: math.Inf(1)}
	}
	for _, f := range frames {
		for _, b := range f {
			sb := &sl.Bins[b.Bin]
			switch b.Kind {
			case BinEdge:
				edges[b.Bin] = append(edges[b.Bin], b.Alt)
			case BinAbove:
				sb.Lower = math.Max(sb.Lower, b.Alt)
			case BinBelow:
				sb.Upper = math.Min(sb.Upper, b.Alt)
			case BinNone:
			}
		}
	}
	for i := range n {
		sb := &sl.Bins[i]
		switch {
		case len(edges[i]) > 0:
			slices.Sort(edges[i])
			sb.Alt = math.Max(edges[i][len(edges[i])/2], sb.Lower)
			sb.Kind, sb.Measured, sb.Edges = BinEdge, true, len(edges[i])
			sb.Conflict = sb.Alt > sb.Upper+0.5
		case sb.Upper <= bottom+boundTolerance:
			sb.Alt, sb.Kind, sb.Measured = sb.Upper, BinBelow, true
			sb.Conflict = sb.Upper < sb.Lower-0.5
		case sb.Lower >= top-boundTolerance:
			sb.Alt, sb.Kind, sb.Measured = sb.Lower, BinAbove, true
		}
	}
	sl.interpolate()
	return sl
}

func (s *Skyline) interpolate() {
	n := len(s.Bins)
	var known []int
	for i, b := range s.Bins {
		if b.Measured {
			known = append(known, i)
		}
	}
	if len(known) == 0 {
		return
	}
	for k, i := range known {
		j := known[(k+1)%len(known)]
		gap := (j - i + n) % n
		if gap == 0 {
			gap = n
		}
		for d := 1; d < gap; d++ {
			idx := (i + d) % n
			f := float64(d) / float64(gap)
			b := &s.Bins[idx]
			b.Alt = s.Bins[i].Alt*(1-f) + s.Bins[j].Alt*f
			if b.Alt > b.Upper {
				b.Alt = b.Upper
			}
			if b.Alt < b.Lower {
				b.Alt = b.Lower
			}
		}
	}
}

func (s *Skyline) Coverage() float64 {
	m := 0
	for _, b := range s.Bins {
		if b.Measured {
			m++
		}
	}
	return float64(m) / float64(len(s.Bins))
}

type HorizonPoint struct {
	Az, Alt  float64
	Measured bool
}

func (s *Skyline) Points(step float64) []HorizonPoint {
	n := int(math.Round(360 / step))
	pts := make([]HorizonPoint, 0, n+1)
	for i := 0; i <= n; i++ {
		az := float64(i) * step
		best, measured := math.Inf(-1), false
		for _, b := range s.Bins {
			d := math.Abs(wrap180(b.Az - az))
			if d <= step/2 {
				best = math.Max(best, b.Alt)
				measured = measured || b.Measured
			}
		}
		pts = append(pts, HorizonPoint{Az: az, Alt: math.Max(0, math.Round(best*10)/10), Measured: measured})
	}
	return pts
}

func WriteHRZ(w io.Writer, pts []HorizonPoint, header []string) error {
	var sb strings.Builder
	for _, h := range header {
		sb.WriteString("# " + h + "\n")
	}
	for _, p := range pts {
		fmt.Fprintf(&sb, "%s %s\n", trimFloat(p.Az), trimFloat(p.Alt))
	}
	_, err := io.WriteString(w, sb.String())
	return err
}

func trimFloat(v float64) string {
	s := fmt.Sprintf("%.1f", v)
	s = strings.TrimSuffix(s, ".0")
	if s == "-0" {
		return "0"
	}
	return s
}
