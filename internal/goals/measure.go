package goals

import (
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
)

const (
	DrawsPerLevel      = 3
	MinSubs            = 2 * MinLevelSubs
	CoverageMin        = 0.6
	StarSigma          = 4.0
	StarHighPassSigma  = 2.0
	StarGrow           = 2
	BrightPercentile   = 99.5
	BrightGrow         = 3
	SmoothSigma        = 4.0
	BackgroundPercent  = 30.0
	NebulaSpread       = 5.0
	NoiseHighPassSigma = 16.0
	MinNoiseBins       = 2000
	FrameFillingNebula = 0.5
	SkyStepLimit       = 5.0
	DeepSkyPercent     = 10.0
	MinBandFraction    = 0.01
	DefaultSaturation  = 0.9
)

const (
	NoiseMaskFaint  = "faint"
	NoiseMaskRegion = "region"
	NoiseMaskBg     = "background"
)

var ErrInsufficientData = errors.New("insufficient data")

type Sub struct {
	Exposure  float64
	Weight    float64
	Effective float64
}

type Binned struct {
	W, H    int
	Val     []float32
	Count   []uint8
	SkyRate float64
}

func Bin(plane []float32, w, h int, exposure float64, saturation float32) (Binned, error) {
	if len(plane) != w*h {
		return Binned{}, fmt.Errorf("plane has %d pixels, want %dx%d", len(plane), w, h)
	}
	if !(exposure > 0) {
		return Binned{}, fmt.Errorf("exposure %v must be positive", exposure)
	}
	sky := validMedian(plane, w, h, saturation)
	bw, bh := w/NoiseBin, h/NoiseBin
	b := Binned{W: bw, H: bh, Val: make([]float32, bw*bh), Count: make([]uint8, bw*bh), SkyRate: sky / exposure}
	sums := make([]float64, bw)
	for by := range bh {
		clear(sums)
		cnt := b.Count[by*bw : (by+1)*bw]
		for y := by * NoiseBin; y < (by+1)*NoiseBin; y++ {
			row := plane[y*w : y*w+bw*NoiseBin]
			for x, v := range row {
				if v == 0 || v >= saturation || v != v {
					continue
				}
				bx := x / NoiseBin
				sums[bx] += float64(v)
				cnt[bx]++
			}
		}
		out := b.Val[by*bw : (by+1)*bw]
		for bx := range bw {
			if cnt[bx] > 0 {
				out[bx] = float32((sums[bx]/float64(cnt[bx]) - sky) / exposure)
			}
		}
	}
	return b, nil
}

func validMedian(plane []float32, w, h int, saturation float32) float64 {
	const stride = 7
	s := make([]float32, 0, (w/stride+1)*(h/stride+1))
	for y := 0; y < h; y += stride {
		for x := 0; x < w; x += stride {
			if v := plane[y*w+x]; v != 0 && v < saturation && v == v {
				s = append(s, v)
			}
		}
	}
	if len(s) == 0 {
		return 0
	}
	slices.Sort(s)
	if len(s)%2 == 1 {
		return float64(s[len(s)/2])
	}
	return (float64(s[len(s)/2-1]) + float64(s[len(s)/2])) / 2
}

func Seed(object, filter string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(object))
	h.Write([]byte{0})
	h.Write([]byte(filter))
	return h.Sum64()
}

func Levels(n int) []int {
	top := n / 2
	var out []int
	for k := MinLevelSubs; k <= top; k *= 2 {
		out = append(out, k)
	}
	if top >= MinLevelSubs && (len(out) == 0 || out[len(out)-1] != top) {
		out = append(out, top)
	}
	return out
}

func Subset(n, maxSubs int, seed uint64) []int {
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	if maxSubs <= 0 || n <= maxSubs {
		return idx
	}
	rng := rand.New(rand.NewPCG(seed, 0x5eed))
	rng.Shuffle(n, func(i, j int) { idx[i], idx[j] = idx[j], idx[i] })
	idx = idx[:maxSubs]
	slices.Sort(idx)
	return idx
}

type draw struct {
	n         int
	role      []int8
	effective float64
	sums      [2][]float32
	weights   [2][]float32
}

type Options struct {
	Seed   uint64
	Region []Point
}

type Measurer struct {
	w, h, bw, bh int
	subs         []Sub
	opts         Options
	sum, weight  []float32
	totalWeight  float64
	draws        []*draw
	added        []bool
}

func NewMeasurer(w, h int, subs []Sub, opts Options) (*Measurer, error) {
	if len(subs) < MinSubs {
		return nil, ErrInsufficientData
	}
	bw, bh := w/NoiseBin, h/NoiseBin
	if bw < 8 || bh < 8 {
		return nil, fmt.Errorf("frame %dx%d is too small", w, h)
	}
	m := &Measurer{w: w, h: h, bw: bw, bh: bh, subs: subs, opts: opts,
		sum: make([]float32, bw*bh), weight: make([]float32, bw*bh), added: make([]bool, len(subs))}
	rng := rand.New(rand.NewPCG(opts.Seed, 0xd4a5))
	n := len(subs)
	for _, lv := range Levels(n) {
		for range DrawsPerLevel {
			perm := rng.Perm(n)
			d := &draw{n: lv, role: make([]int8, n)}
			for _, i := range perm[:lv] {
				d.role[i] = 1
				d.effective += subs[i].Effective
			}
			for _, i := range perm[lv : 2*lv] {
				d.role[i] = 2
				d.effective += subs[i].Effective
			}
			m.draws = append(m.draws, d)
		}
	}
	return m, nil
}

func (m *Measurer) BinnedSize() (int, int) { return m.bw, m.bh }

func accumulate(sum, weight []float32, b Binned, w float64) {
	for i, c := range b.Count {
		if c == 0 {
			continue
		}
		wf := float32(w * float64(c) / (NoiseBin * NoiseBin))
		sum[i] += wf * b.Val[i]
		weight[i] += wf
	}
}

func (m *Measurer) Add(i int, b Binned) error {
	if i < 0 || i >= len(m.subs) {
		return fmt.Errorf("sub %d out of range", i)
	}
	if m.added[i] {
		return fmt.Errorf("sub %d added twice", i)
	}
	if b.W != m.bw || b.H != m.bh {
		return fmt.Errorf("binned sub is %dx%d, want %dx%d", b.W, b.H, m.bw, m.bh)
	}
	w := m.subs[i].Weight
	if !(w > 0) {
		return fmt.Errorf("sub %d has weight %v", i, w)
	}
	m.added[i] = true
	m.totalWeight += w
	accumulate(m.sum, m.weight, b, w)
	for _, d := range m.draws {
		r := d.role[i]
		if r == 0 {
			continue
		}
		h := r - 1
		if d.sums[h] == nil {
			d.sums[h] = make([]float32, m.bw*m.bh)
			d.weights[h] = make([]float32, m.bw*m.bh)
		}
		accumulate(d.sums[h], d.weights[h], b, w)
	}
	return nil
}

type Masks struct {
	Covered, Stars, Background, Nebula, Band []bool
}

type Result struct {
	Subs           int
	Levels         int
	Sky            float64
	Signal         float64
	NoiseA         float64
	NoiseB         float64
	NoiseNow       float64
	SNR            float64
	GainPerHourPct float64
	HeldOutErrPct  *float64
	LowConfidence  bool
	LowReason      string
	BandFraction   float64
	NebFraction    float64
	SkyStep        float64
	BandLo         float64
	BandHi         float64
	NoiseMask      string
	Points         []DrawPoint
	BW, BH         int
	Mean           []float64
	Coverage       []float32
	Masks          Masks
}

func (m *Measurer) Finish(totalHours float64) (Result, error) {
	for i, ok := range m.added {
		if !ok {
			return Result{}, fmt.Errorf("sub %d was never added", i)
		}
	}
	bw, bh := m.bw, m.bh
	np := bw * bh
	res := Result{Subs: len(m.subs), BW: bw, BH: bh, Mean: make([]float64, np), Coverage: make([]float32, np)}
	covered := make([]bool, np)
	for i := range np {
		if m.weight[i] > 0 {
			res.Mean[i] = float64(m.sum[i] / m.weight[i])
		}
		res.Coverage[i] = float32(float64(m.weight[i]) / m.totalWeight)
		covered[i] = float64(res.Coverage[i]) >= CoverageMin
	}
	if count(covered) == 0 {
		return res, errors.New("no pixel is covered by most subs")
	}
	mk := buildMasks(res.Mean, covered, bw, bh, m.w, m.h, m.opts.Region)
	res.Masks = mk.Masks
	res.Sky = mk.sky
	res.SkyStep = mk.skyStep
	res.BandLo, res.BandHi = mk.bandLo, mk.bandHi
	nCovered := count(covered)
	nOK := mk.ok
	res.BandFraction = float64(count(mk.Band)) / float64(nCovered)
	if nOK > 0 {
		res.NebFraction = float64(count(mk.Nebula)) / float64(nOK)
	}
	region := len(m.opts.Region) >= 3
	band := selectValues(res.Mean, mk.Band)
	switch {
	case len(band) == 0:
		res.Signal = math.NaN()
	case region:
		res.Signal = median(band) - mk.sky
	default:
		res.Signal = median(band) - median(selectValues(res.Mean, mk.Background))
	}

	noiseMask := mk.Band
	res.NoiseMask = NoiseMaskFaint
	if region {
		res.NoiseMask = NoiseMaskRegion
	}
	if count(noiseMask) < MinNoiseBins {
		noiseMask = mk.Background
		res.NoiseMask = NoiseMaskBg
	}
	okBase := and(covered, not(mk.Stars))
	levels := map[int]bool{}
	for _, d := range m.draws {
		s, ok := m.drawNoise(d, okBase, noiseMask)
		if !ok {
			continue
		}
		res.Points = append(res.Points, DrawPoint{N: d.n, Hours: d.effective / 2 / 3600, Sigma: s})
		levels[d.n] = true
	}
	res.Levels = len(levels)
	a, b, ok := FitNoise(res.Points)
	if !ok {
		return res, errors.New("too few noise measurements to fit")
	}
	res.NoiseA, res.NoiseB = a, b
	res.NoiseNow = NoiseAt(a, b, totalHours)
	res.GainPerHourPct = GainPerHour(a, b, totalHours)
	if !math.IsNaN(res.Signal) && res.NoiseNow > 0 {
		res.SNR = res.Signal / res.NoiseNow
	}
	if e, ok := heldOut(res.Points, len(m.subs)); ok {
		res.HeldOutErrPct = &e
	}
	var reasons []string
	if res.NebFraction > FrameFillingNebula {
		reasons = append(reasons, "frame-filling nebula")
	}
	if res.SkyStep > SkyStepLimit {
		reasons = append(reasons, "background is mostly nebula")
	}
	if res.BandFraction < MinBandFraction {
		reasons = append(reasons, "faint band too small")
	}
	if math.IsNaN(res.Signal) || res.Signal <= 0 {
		reasons = append(reasons, "no faint signal")
		if math.IsNaN(res.Signal) {
			res.Signal = 0
		}
	}
	res.LowConfidence = len(reasons) > 0
	res.LowReason = strings.Join(reasons, "; ")
	return res, nil
}

func not(a []bool) []bool {
	out := make([]bool, len(a))
	for i, v := range a {
		out[i] = !v
	}
	return out
}

func (m *Measurer) drawNoise(d *draw, okBase, noiseMask []bool) (float64, bool) {
	if d.sums[0] == nil || d.sums[1] == nil {
		return 0, false
	}
	np := m.bw * m.bh
	diff := make([]float64, np)
	ok := make([]bool, np)
	for i := range np {
		wa, wb := d.weights[0][i], d.weights[1][i]
		if !okBase[i] || wa <= 0 || wb <= 0 {
			continue
		}
		diff[i] = (float64(d.sums[0][i]/wa) - float64(d.sums[1][i]/wb)) / math.Sqrt2
		ok[i] = true
	}
	smooth := maskedGaussian(diff, ok, m.bw, m.bh, NoiseHighPassSigma)
	var vals []float64
	for i := range np {
		if ok[i] && noiseMask[i] {
			vals = append(vals, diff[i]-smooth[i])
		}
	}
	if len(vals) < 50 {
		return 0, false
	}
	s := mad(vals)
	return s, s > 0
}

type maskSet struct {
	Masks
	sky     float64
	ok      int
	skyStep float64
	bandLo  float64
	bandHi  float64
}

func buildMasks(mean []float64, covered []bool, bw, bh, w, h int, region []Point) maskSet {
	np := bw * bh
	lowPass := maskedGaussian(mean, covered, bw, bh, StarHighPassSigma)
	hp := make([]float64, np)
	for i := range np {
		hp[i] = mean[i] - lowPass[i]
	}
	s := mad(selectValues(hp, covered))
	smoothNoise := s / (2 * math.Sqrt(math.Pi) * SmoothSigma)
	stars := make([]bool, np)
	bright := make([]bool, np)
	p995 := percentile(selectValues(mean, covered), BrightPercentile)
	for i := range np {
		if !covered[i] {
			continue
		}
		stars[i] = hp[i] > StarSigma*s
		bright[i] = mean[i] > p995
	}
	stars = dilate(stars, bw, bh, StarGrow)
	b := dilate(bright, bw, bh, BrightGrow)
	for i := range np {
		stars[i] = stars[i] || b[i]
	}
	ok := and(covered, not(stars))
	smooth := maskedGaussian(mean, ok, bw, bh, SmoothSigma)
	okVals := selectValues(smooth, ok)
	p30 := percentile(okVals, BackgroundPercent)
	bg := make([]bool, np)
	for i := range np {
		bg[i] = ok[i] && smooth[i] <= p30
	}
	bgVals := selectValues(smooth, bg)
	sky := median(bgVals)
	spread := math.Max(mad(bgVals), smoothNoise)
	deep := percentile(okVals, DeepSkyPercent)
	var deepVals []float64
	for _, v := range okVals {
		if v <= deep {
			deepVals = append(deepVals, v)
		}
	}
	skyStep := 0.0
	if deepSpread := math.Max(mad(deepVals), smoothNoise); deepSpread > 0 {
		skyStep = (sky - median(deepVals)) / deepSpread
	}
	neb := make([]bool, np)
	for i := range np {
		neb[i] = ok[i] && smooth[i]-sky > NebulaSpread*spread
	}
	var band []bool
	bandLo, bandHi := math.NaN(), math.NaN()
	if len(region) >= 3 {
		band = and(rasterize(region, bw, bh, w, h), ok)
	} else {
		band = make([]bool, np)
		if n := count(neb); n > 0 && float64(n) > MinBandFraction*float64(count(covered)) {
			nebVals := selectValues(smooth, neb)
			slices.Sort(nebVals)
			bandLo = percentileSorted(nebVals, BandLowPercentile)
			bandHi = percentileSorted(nebVals, BandHighPercent)
			for i := range np {
				band[i] = neb[i] && smooth[i] >= bandLo && smooth[i] <= bandHi
			}
		}
	}
	return maskSet{Masks: Masks{Covered: covered, Stars: stars, Background: bg, Nebula: neb, Band: band}, sky: sky, ok: count(ok), skyStep: skyStep, bandLo: bandLo, bandHi: bandHi}
}
