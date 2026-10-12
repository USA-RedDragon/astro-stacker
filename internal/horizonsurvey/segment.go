package horizonsurvey

import (
	"math"
	"slices"
)

const (
	ModeDusk  = "dusk"
	ModeNight = "night"

	ClassSky  = "sky"
	ClassObst = "obstructed"
	ClassEdge = "edge"

	edgeFraction  = 0.003
	minClassFrac  = 0.01
	duskContrast  = 2.5
	nightContrast = 1.4
	starSigma     = 8.0
	starRadius    = 4
	oneClassRatio = 0.35
	saturated     = 0.92
)

type Mask struct {
	BW, BH int
	Block  int
	Sky    []bool
	Level  []float64
}

func (m *Mask) Center(bx, by int) (float64, float64) {
	return float64(bx*m.Block) + float64(m.Block-1)/2, float64(by*m.Block) + float64(m.Block-1)/2
}

type Segmentation struct {
	Mask      *Mask
	Class     string
	ObstFrac  float64
	SkyLevel  float64
	Peak      float64
	Stars     int
	Saturated bool
	TwoClass  bool
}

func BlockSize(w int) int { return max(4, w/240) }

func blockStats(pix []float32, w, h, b int) (med, peak []float64, bw, bh int) {
	bw, bh = w/b, h/b
	med = make([]float64, bw*bh)
	peak = make([]float64, bw*bh)
	buf := make([]float64, 0, b*b)
	for by := range bh {
		for bx := range bw {
			buf = buf[:0]
			for y := by * b; y < (by+1)*b; y++ {
				for x := bx * b; x < (bx+1)*b; x++ {
					buf = append(buf, float64(pix[y*w+x]))
				}
			}
			slices.Sort(buf)
			med[by*bw+bx] = buf[len(buf)/4]
			peak[by*bw+bx] = buf[len(buf)-1]
		}
	}
	return medianFilter(med, bw, bh), peak, bw, bh
}

func medianFilter(v []float64, bw, bh int) []float64 {
	out := make([]float64, len(v))
	buf := make([]float64, 0, 9)
	for by := range bh {
		for bx := range bw {
			buf = buf[:0]
			for y := max(0, by-1); y <= min(bh-1, by+1); y++ {
				for x := max(0, bx-1); x <= min(bw-1, bx+1); x++ {
					buf = append(buf, v[y*bw+x])
				}
			}
			slices.Sort(buf)
			out[by*bw+bx] = buf[len(buf)/2]
		}
	}
	return out
}

func noise(pix []float32, w, h int) float64 {
	var d []float64
	for y := 0; y < h; y += 3 {
		for x := 0; x+1 < w; x += 7 {
			d = append(d, math.Abs(float64(pix[y*w+x+1]-pix[y*w+x])))
		}
	}
	if len(d) == 0 {
		return 0
	}
	slices.Sort(d)
	return d[len(d)/2] * 1.4826 / math.Sqrt2
}

func otsu(v []float64) (t, lo, hi float64, nlo int) {
	s := slices.Clone(v)
	slices.Sort(s)
	n := len(s)
	prefix := make([]float64, n+1)
	for i, x := range s {
		prefix[i+1] = prefix[i] + x
	}
	best := -1.0
	for i := 1; i < n; i++ {
		if s[i] == s[i-1] {
			continue
		}
		m0 := prefix[i] / float64(i)
		m1 := (prefix[n] - prefix[i]) / float64(n-i)
		w0 := float64(i) / float64(n)
		between := w0 * (1 - w0) * (m1 - m0) * (m1 - m0)
		if between > best {
			best, t, lo, hi, nlo = between, (s[i-1]+s[i])/2, m0, m1, i
		}
	}
	if best < 0 {
		return s[0], s[0], s[0], 0
	}
	return t, lo, hi, nlo
}

func percentile(v []float64, p float64) float64 {
	s := slices.Clone(v)
	slices.Sort(s)
	return s[int(math.Min(float64(len(s)-1), p*float64(len(s))))]
}

func starMap(med, peak []float64, bw, bh int, sigma float64) ([]int, int) {
	stars := make([]bool, len(med))
	total := 0
	for i := range med {
		if sigma > 0 && peak[i]-med[i] > starSigma*sigma {
			stars[i] = true
			total++
		}
	}
	density := make([]int, len(med))
	for by := range bh {
		for bx := range bw {
			n := 0
			for y := max(0, by-starRadius); y <= min(bh-1, by+starRadius); y++ {
				for x := max(0, bx-starRadius); x <= min(bw-1, bx+starRadius); x++ {
					if stars[y*bw+x] {
						n++
					}
				}
			}
			density[by*bw+bx] = n
		}
	}
	return density, total
}

func majority(sky []bool, bw, bh int) []bool {
	out := make([]bool, len(sky))
	for by := range bh {
		for bx := range bw {
			n, t := 0, 0
			for y := max(0, by-1); y <= min(bh-1, by+1); y++ {
				for x := max(0, bx-1); x <= min(bw-1, bx+1); x++ {
					t++
					if sky[y*bw+x] {
						n++
					}
				}
			}
			out[by*bw+bx] = 2*n > t
		}
	}
	return out
}

func Segment(pix []float32, w, h int, mode string, skyRef float64) Segmentation {
	b := BlockSize(w)
	med, peak, bw, bh := blockStats(pix, w, h, b)
	sigma := noise(pix, w, h)
	t, lo, hi, nlo := otsu(med)
	contrast := hi / math.Max(lo, 1e-6)
	need := duskContrast
	if mode == ModeNight {
		need = nightContrast
	}
	minClass := int(math.Max(3, minClassFrac*float64(len(med))))
	twoClass := contrast >= need && nlo >= minClass && len(med)-nlo >= minClass && hi-lo > 6*sigma/float64(b)
	density, stars := starMap(med, peak, bw, bh, sigma)
	sky := make([]bool, len(med))
	if twoClass && mode == ModeNight && stars > 0 {
		var sHi, nHi, sLo, nLo float64
		for i, v := range med {
			if v > t {
				sHi += float64(density[i])
				nHi++
			} else {
				sLo += float64(density[i])
				nLo++
			}
		}
		if sLo/math.Max(nLo, 1) >= 0.5*sHi/math.Max(nHi, 1) {
			twoClass = false
		}
	}
	switch {
	case twoClass:
		for i, v := range med {
			sky[i] = v > t
		}
	default:
		level := percentile(med, 0.5)
		isSky := skyRef <= 0 || level >= oneClassRatio*skyRef
		if mode == ModeNight && float64(stars) >= 0.002*float64(len(med)) {
			isSky = true
		}
		for i := range sky {
			sky[i] = isSky
		}
	}
	sky = majority(majority(sky, bw, bh), bw, bh)
	obst := 0
	var skyVals []float64
	for i, s := range sky {
		if s {
			skyVals = append(skyVals, med[i])
		} else {
			obst++
		}
	}
	seg := Segmentation{
		Mask:     &Mask{BW: bw, BH: bh, Block: b, Sky: sky, Level: med},
		ObstFrac: float64(obst) / float64(len(sky)),
		Stars:    stars,
		TwoClass: twoClass,
		Peak:     percentile(med, 0.99),
	}
	if len(skyVals) > 0 {
		seg.SkyLevel = percentile(skyVals, 0.5)
	}
	seg.Saturated = seg.Peak >= saturated
	switch {
	case seg.ObstFrac <= edgeFraction:
		seg.Class = ClassSky
	case seg.ObstFrac >= 1-edgeFraction:
		seg.Class = ClassObst
	default:
		seg.Class = ClassEdge
	}
	return seg
}
