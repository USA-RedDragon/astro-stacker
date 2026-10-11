package sublength

import (
	"math"
	"slices"
)

func Ladder() []float64 { return []float64{60, 120, 180, 300, 600} }

const ReadNoiseFactor = 9.26

const NearBest = 0.95

const SaturationFloor = 0.02

type Rejection struct {
	Base      float64 `json:"base"`
	PerSecond float64 `json:"per_second"`
	MaxLength float64 `json:"max_length"`
	Points    []Point `json:"points"`
}

type Point struct {
	Exposure float64 `json:"exposure"`
	Graded   int     `json:"graded"`
	Rejected int     `json:"rejected"`
}

func FitRejection(points []Point) Rejection {
	r := Rejection{Points: points}
	var sw, sx, sy, sxx, sxy float64
	for _, p := range points {
		if p.Graded <= 0 {
			continue
		}
		w := float64(p.Graded)
		x, y := p.Exposure, float64(p.Rejected)/float64(p.Graded)
		sw, sx, sy, sxx, sxy = sw+w, sx+w*x, sy+w*y, sxx+w*x*x, sxy+w*x*y
		r.MaxLength = math.Max(r.MaxLength, p.Exposure)
	}
	if sw == 0 {
		return r
	}
	if d := sw*sxx - sx*sx; d > 0 {
		r.PerSecond = math.Max(0, (sw*sxy-sx*sy)/d)
	}
	r.Base = math.Max(0, (sy-r.PerSecond*sx)/sw)
	return r
}

func (r Rejection) Kept(t float64) float64 {
	return clamp01(1 - r.Base - r.PerSecond*t)
}

func ReadNoiseShare(sky, t, rn float64) float64 {
	if !(sky > 0) || !(t > 0) {
		return 0
	}
	return sky * t / (sky*t + rn*rn)
}

func SkyLimited(sky, rn float64) float64 {
	if !(sky > 0) {
		return math.Inf(1)
	}
	return ReadNoiseFactor * rn * rn / sky
}

func Efficiency(sky, t, rn, overhead float64, rej Rejection) float64 {
	return ReadNoiseShare(sky, t, rn) * t / (t + overhead) * rej.Kept(t)
}

type Stars struct {
	Exposure  float64
	Ranks     []int
	Flux      []float64
	Saturated []bool
	Detected  int
}

func (s Stars) SaturatedShare(t float64) (float64, bool) {
	if s.Detected <= 0 || !(s.Exposure > 0) || len(s.Flux) != len(s.Ranks) || len(s.Saturated) != len(s.Ranks) {
		return 0, false
	}
	limit := math.Inf(1)
	for i, sat := range s.Saturated {
		if sat && s.Flux[i] > 0 {
			limit = math.Min(limit, s.Flux[i])
		}
	}
	if math.IsInf(limit, 1) {
		return 0, false
	}
	scale := t / s.Exposure
	rank := 0
	for i, f := range s.Flux {
		if f > 0 && f*scale >= limit {
			rank = max(rank, s.Ranks[i])
		}
	}
	return math.Min(1, float64(rank)/float64(s.Detected)), true
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return math.NaN()
	}
	s := slices.Clone(v)
	slices.Sort(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

func SaturatedShareMedian(subs []Stars, t float64) (float64, int) {
	var v []float64
	for _, s := range subs {
		if f, ok := s.SaturatedShare(t); ok {
			v = append(v, f)
		}
	}
	return median(v), len(v)
}
