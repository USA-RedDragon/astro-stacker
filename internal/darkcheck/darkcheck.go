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
	Median float64
	Spread float64
	Noise  float64
}

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
	med := func(v []float32) float32 {
		s := slices.Clone(v)
		slices.Sort(s)
		return s[len(s)/2]
	}
	sample := make([]float32, 0, len(d)/16+1)
	for i := 0; i < len(d); i += 16 {
		sample = append(sample, d[i])
	}
	if len(sample) == 0 || w <= 0 || h <= 0 {
		return m
	}
	c := med(sample)
	for i, v := range sample {
		sample[i] = float32(math.Abs(float64(v - c)))
	}
	m.Noise = 1.4826 * float64(med(sample)) * adu
	const nx, ny = 32, 24
	blocks := make([]float32, 0, nx*ny)
	v := make([]float32, 0, (w/nx+1)*(h/ny+1)/4)
	for by := range ny {
		for bx := range nx {
			v = v[:0]
			for y := by * h / ny; y < (by+1)*h/ny; y += 2 {
				for x := bx * w / nx; x < (bx+1)*w/nx; x += 2 {
					v = append(v, d[y*w+x])
				}
			}
			if len(v) > 0 {
				blocks = append(blocks, med(v))
			}
		}
	}
	if len(blocks) == 0 {
		return m
	}
	slices.Sort(blocks)
	m.Spread = float64(blocks[len(blocks)*95/100]-blocks[len(blocks)*5/100]) * adu
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
	Setup
	Measures
}

type BiasRef struct {
	Level     float64
	LevelTol  float64
	SpreadMax float64
	SpreadTol float64
	Frames    int
}

func BiasReference(bias []Measures) (BiasRef, bool) {
	if len(bias) < calmatch.MinFrames {
		return BiasRef{}, false
	}
	levels := make([]float64, len(bias))
	spreads := make([]float64, len(bias))
	for i, b := range bias {
		levels[i], spreads[i] = b.Median, b.Spread
	}
	slices.Sort(levels)
	slices.Sort(spreads)
	r := BiasRef{Level: levels[len(levels)/2], SpreadMax: spreads[len(spreads)-1], SpreadTol: spreads[len(spreads)-1] - spreads[0], Frames: len(bias)}
	for _, l := range levels {
		r.LevelTol = math.Max(r.LevelTol, math.Abs(l-r.Level))
	}
	return r, true
}

type DarkRef struct {
	Rate         float64
	RateFrames   int
	SpreadMax    float64
	SpreadFrames int
	WarmestC     float64
	ColdestC     float64
}

func DarkReference(d Setup, bias BiasRef, clean []Sample, sessionGap time.Duration) (DarkRef, bool) {
	var r DarkRef
	r.ColdestC, r.WarmestC = math.Inf(1), math.Inf(-1)
	for _, c := range clean {
		if !(c.Exposure > 0) || math.IsNaN(c.SetTemp) || math.IsNaN(d.SetTemp) {
			continue
		}
		if c.SetTemp < d.SetTemp-calmatch.SetTempExactC {
			continue
		}
		if gap := c.TakenAt.Sub(d.TakenAt); gap <= sessionGap && gap >= -sessionGap {
			continue
		}
		rate := math.Max(0, (c.Median-bias.Level)/c.Exposure)
		if r.RateFrames == 0 || rate > r.Rate {
			r.Rate = rate
		}
		r.RateFrames++
		r.ColdestC, r.WarmestC = math.Min(r.ColdestC, c.SetTemp), math.Max(r.WarmestC, c.SetTemp)
		if c.Exposure >= d.Exposure*(1-calmatch.ExposureTolerance) {
			r.SpreadMax = math.Max(r.SpreadMax, c.Spread)
			r.SpreadFrames++
		}
	}
	return r, r.RateFrames > 0
}

type Verdict struct {
	State  string
	Reason string
}

func num(v float64) string {
	return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%.2f", v), "0"), ".")
}

func Judge(d Setup, m Measures, bias *BiasRef, dark *DarkRef) Verdict {
	if d.CCDTemp != nil {
		if off := math.Abs(*d.CCDTemp - d.SetTemp); !math.IsNaN(off) && off > AtTargetToleranceC {
			return Verdict{State: StateOffTemp, Reason: fmt.Sprintf(
				"sensor at %s °C, %s °C from its %s °C setpoint (NINA counts a camera at its target within %s °C)",
				num(*d.CCDTemp), num(off), num(d.SetTemp), num(AtTargetToleranceC))}
		}
	}
	if bias == nil {
		return Verdict{State: StateUnchecked, Reason: fmt.Sprintf(
			"not checked yet: fewer than %d bias frames measured at gain %s, offset %s, bin %s", calmatch.MinFrames, num(d.Gain), num(d.Offset), num(d.BinX))}
	}
	spreadRef := bias.SpreadMax
	spreadBasis := fmt.Sprintf("bias frames up to %s ADU over %d frames", num(bias.SpreadMax), bias.Frames)
	if dark != nil && dark.SpreadFrames > 0 && dark.SpreadMax > spreadRef {
		spreadRef = dark.SpreadMax
		spreadBasis = fmt.Sprintf("clean darks of this exposure or longer at %s °C or warmer up to %s ADU over %d frames",
			num(d.SetTemp-calmatch.SetTempExactC), num(dark.SpreadMax), dark.SpreadFrames)
	}
	spreadLimit := spreadRef + bias.SpreadTol
	spreadText := fmt.Sprintf("large-scale spread %s ADU against a limit of %s ADU (%s, plus the %s ADU range of the bias frames' spreads)",
		num(m.Spread), num(spreadLimit), spreadBasis, num(bias.SpreadTol))
	if m.Spread > spreadLimit {
		return Verdict{State: StateLeak, Reason: "light leak: " + spreadText}
	}
	if dark == nil {
		return Verdict{State: StateClean, Reason: fmt.Sprintf(
			"%s; level not checked: no clean darks at %s °C or warmer from another session to measure dark current",
			spreadText, num(d.SetTemp-calmatch.SetTempExactC))}
	}
	expected := bias.Level + dark.Rate*d.Exposure
	levelLimit := expected + bias.LevelTol
	levelText := fmt.Sprintf("median %s ADU against a limit of %s ADU (bias %s ± %s ADU over %d frames, plus dark current up to %s ADU/s × %s s from %d clean darks at %s to %s °C)",
		num(m.Median), num(levelLimit), num(bias.Level), num(bias.LevelTol), bias.Frames,
		strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%.5f", dark.Rate), "0"), "."), num(d.Exposure), dark.RateFrames, num(dark.ColdestC), num(dark.WarmestC))
	if m.Median > levelLimit {
		return Verdict{State: StateLeak, Reason: "light leak: " + levelText}
	}
	return Verdict{State: StateClean, Reason: spreadText + "; " + levelText}
}
