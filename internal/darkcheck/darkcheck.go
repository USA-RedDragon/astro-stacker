package darkcheck

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/calmatch"
)

const (
	StateClean     = "clean"
	StateLeak      = "leak"
	StateUnchecked = "unchecked"
	StateOffTemp   = "off-temp"
)

const AtTargetToleranceC = 2.0

type Measures struct {
	Median    float64
	Spread    float64
	Noise     float64
	SpreadErr float64
}

const MeasureRevision = 2

func Measure(d []float32, w, h int) Measures {
	const adu = math.MaxUint16
	var hist [math.MaxUint16 + 1]int
	n := 0
	for _, v := range d {
		if math.IsNaN(float64(v)) {
			continue
		}
		hist[int(math.Round(math.Min(1, math.Max(0, float64(v)))*adu))]++
		n++
	}
	var m Measures
	seen := 0
	for i, c := range hist {
		seen += c
		if n > 0 && 2*seen >= n {
			m.Median = float64(i)
			break
		}
	}
	med := func(v []float64) float64 {
		s := slices.Clone(v)
		slices.Sort(s)
		return s[len(s)/2]
	}
	sample := make([]float64, 0, len(d)/16+1)
	for i := 0; i < len(d); i += 16 {
		sample = append(sample, float64(d[i])*adu)
	}
	if len(sample) == 0 || w <= 0 || h <= 0 {
		return m
	}
	c := med(sample)
	dev := make([]float64, len(sample))
	for i, v := range sample {
		dev[i] = math.Abs(v - c)
	}
	m.Noise = 1.4826 * med(dev)
	clip := 5 * math.Max(m.Noise, 0.5)
	const nx, ny = 32, 24
	blocks := make([]float64, 0, nx*ny)
	used := 0
	for by := range ny {
		for bx := range nx {
			sum, cnt := 0.0, 0
			for y := by * h / ny; y < (by+1)*h/ny; y += 2 {
				for x := bx * w / nx; x < (bx+1)*w/nx; x += 2 {
					v := float64(d[y*w+x]) * adu
					if math.Abs(v-c) <= clip {
						sum += v
						cnt++
					}
				}
			}
			if cnt > 0 {
				blocks = append(blocks, sum/float64(cnt))
				used += cnt
			}
		}
	}
	if len(blocks) == 0 {
		return m
	}
	slices.Sort(blocks)
	m.Spread = blocks[len(blocks)*95/100] - blocks[len(blocks)*5/100]
	m.SpreadErr = math.Max(m.Noise, 1/math.Sqrt(12)) / math.Sqrt(float64(used)/float64(len(blocks)))
	return m
}

type Setup struct {
	Exposure float64
	Gain     float64
	Offset   float64
	BinX     float64
	SetTemp  float64
	CCDTemp  *float64
	TakenAt  time.Time
}

type Sample struct {
	ID int
	Setup
	Measures
	Clean bool
}

type BiasRef struct {
	Level    float64
	LevelTol float64
	Frames   int
}

func BiasReference(bias []Measures) (BiasRef, bool) {
	if len(bias) < calmatch.MinFrames {
		return BiasRef{}, false
	}
	levels := make([]float64, len(bias))
	for i, b := range bias {
		levels[i] = b.Median
	}
	slices.Sort(levels)
	r := BiasRef{Level: levels[len(levels)/2], Frames: len(bias)}
	for _, l := range levels {
		r.LevelTol = math.Max(r.LevelTol, math.Abs(l-r.Level))
	}
	return r, true
}

const (
	RefSameCombo = "same-combo"
	RefBounding  = "bounding"
	RefSiblings  = "siblings"
)

type DarkRef struct {
	Kind    string
	Samples []Sample
}

func sameCombo(a, b Setup) bool {
	return math.Abs(a.Exposure-b.Exposure) <= calmatch.ExposureTolerance*a.Exposure && math.Abs(a.SetTemp-b.SetTemp) <= calmatch.SetTempExactC
}

func SelectReference(id int, d Setup, darks []Sample) (DarkRef, bool) {
	var same, bound, sib []Sample
	for _, c := range darks {
		if c.ID == id || !(c.Exposure > 0) || math.IsNaN(c.SetTemp) || math.IsNaN(d.SetTemp) {
			continue
		}
		switch {
		case sameCombo(d, c.Setup) && c.Clean:
			same = append(same, c)
		case sameCombo(d, c.Setup):
			sib = append(sib, c)
		case c.Clean && c.Exposure >= d.Exposure*(1-calmatch.ExposureTolerance) && c.SetTemp >= d.SetTemp-calmatch.SetTempExactC:
			bound = append(bound, c)
		}
	}
	switch {
	case len(same) >= 2:
		return DarkRef{Kind: RefSameCombo, Samples: same}, true
	case len(bound) >= 2:
		return DarkRef{Kind: RefBounding, Samples: bound}, true
	case len(same)+len(sib) >= 2:
		return DarkRef{Kind: RefSiblings, Samples: append(same, sib...)}, true
	}
	return DarkRef{}, false
}

type Verdict struct {
	State  string
	Reason string
}

func num(v float64) string {
	return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%.2f", v), "0"), ".")
}

func envelope(v []float64) (lo, hi float64) {
	lo, hi = math.Inf(1), math.Inf(-1)
	for _, x := range v {
		lo, hi = math.Min(lo, x), math.Max(hi, x)
	}
	return lo, hi
}

func refText(r DarkRef) string {
	switch r.Kind {
	case RefSameCombo:
		return fmt.Sprintf("%d clean darks of this exposure and set temperature", len(r.Samples))
	case RefBounding:
		return fmt.Sprintf("%d clean darks as long or longer at this set temperature or warmer", len(r.Samples))
	default:
		return fmt.Sprintf("the other %d darks of this exposure and set temperature, none checked yet", len(r.Samples))
	}
}

func Judge(d Setup, m Measures, bias *BiasRef, ref *DarkRef) Verdict {
	if d.CCDTemp != nil {
		if off := math.Abs(*d.CCDTemp - d.SetTemp); !math.IsNaN(off) && off > AtTargetToleranceC {
			return Verdict{State: StateOffTemp, Reason: fmt.Sprintf(
				"sensor at %s °C, %s °C from its %s °C setpoint (NINA counts a camera at its target within %s °C)",
				num(*d.CCDTemp), num(off), num(d.SetTemp), num(AtTargetToleranceC))}
		}
	}
	if ref == nil {
		return Verdict{State: StateUnchecked, Reason: fmt.Sprintf(
			"not checked yet: fewer than 2 other darks of %s s at %s °C (gain %s, offset %s) or as long or longer at a warmer set temperature to compare with",
			num(d.Exposure), num(d.SetTemp), num(d.Gain), num(d.Offset))}
	}
	spreads := make([]float64, len(ref.Samples))
	for i, c := range ref.Samples {
		spreads[i] = c.Spread
	}
	lo, hi := envelope(spreads)
	blockErr := m.SpreadErr
	for _, c := range ref.Samples {
		blockErr = math.Max(blockErr, c.SpreadErr)
	}
	tol := math.Max(hi-lo, 3*blockErr)
	limit := hi + tol
	basis := refText(*ref)
	spreadText := fmt.Sprintf("large-scale spread %s ADU against a limit of %s ADU (%s spread %s to %s ADU; the limit adds their range or 3 times the block-mean noise of %s ADU, whichever is larger)",
		num(m.Spread), num(limit), basis, num(lo), num(hi), num(blockErr))
	if m.Spread > limit {
		return Verdict{State: StateLeak, Reason: "light leak: " + spreadText}
	}
	var levelLimit float64
	var levelText string
	switch {
	case ref.Kind != RefBounding:
		meds := make([]float64, len(ref.Samples))
		for i, c := range ref.Samples {
			meds[i] = c.Median
		}
		mlo, mhi := envelope(meds)
		levelLimit = mhi + math.Max(mhi-mlo, 1)
		levelText = fmt.Sprintf("median %s ADU against a limit of %s ADU (their medians %s to %s ADU, plus their range or the 1 ADU median step)", num(m.Median), num(levelLimit), num(mlo), num(mhi))
	case bias != nil:
		rates := make([]float64, len(ref.Samples))
		for i, c := range ref.Samples {
			rates[i] = math.Max(0, (c.Median-bias.Level)/c.Exposure)
		}
		rlo, rhi := envelope(rates)
		levelLimit = bias.Level + (rhi+(rhi-rlo))*d.Exposure + math.Max(bias.LevelTol, 1)
		levelText = fmt.Sprintf("median %s ADU against a limit of %s ADU (bias %s ± %s ADU over %d frames, plus their dark current %s to %s ADU/s × %s s)",
			num(m.Median), num(levelLimit), num(bias.Level), num(bias.LevelTol), bias.Frames,
			strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%.5f", rlo), "0"), "."), strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%.5f", rhi), "0"), "."), num(d.Exposure))
	default:
		return Verdict{State: StateClean, Reason: spreadText + "; level not checked: no bias frames to remove the pedestal"}
	}
	if m.Median > levelLimit {
		return Verdict{State: StateLeak, Reason: "light leak: " + levelText}
	}
	return Verdict{State: StateClean, Reason: spreadText + "; " + levelText}
}
