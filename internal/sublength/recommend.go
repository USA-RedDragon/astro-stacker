package sublength

import (
	"fmt"
	"math"
	"slices"
	"strings"
)

type Candidate struct {
	Exposure        float64  `json:"exposure"`
	Darks           bool     `json:"darks"`
	ReadNoiseShare  float64  `json:"read_noise_share"`
	DutyCycle       float64  `json:"duty_cycle"`
	Kept            float64  `json:"kept"`
	Efficiency      float64  `json:"efficiency"`
	SkyToReadNoise  float64  `json:"sky_to_read_noise"`
	SaturatedShare  *float64 `json:"saturated_share"`
	Blocked         string   `json:"blocked,omitempty"`
	RelativeSNRRate float64  `json:"relative_snr_rate"`
}

type Input struct {
	Sensor        Sensor
	Sky           float64
	SkySubs       int
	SkySource     string
	Current       float64
	Overhead      float64
	Rejection     Rejection
	Stars         []Stars
	DarkLengths   []float64
	Ladder        []float64
	MinSkySubs    int
	SaturationRef float64
}

type Recommendation struct {
	Exposure      float64     `json:"exposure"`
	Ideal         float64     `json:"ideal"`
	Current       float64     `json:"current"`
	SkyLimited    float64     `json:"sky_limited"`
	SkyRate       float64     `json:"sky_rate"`
	SkySubs       int         `json:"sky_subs"`
	SkySource     string      `json:"sky_source"`
	ReadNoise     float64     `json:"read_noise_e"`
	EPerADU       float64     `json:"e_per_adu"`
	Overhead      float64     `json:"overhead"`
	SaturationCap float64     `json:"saturation_cap"`
	SNRChange     float64     `json:"snr_rate_change"`
	IdealGain     float64     `json:"ideal_snr_rate_gain"`
	Candidates    []Candidate `json:"candidates"`
	Basis         string      `json:"basis"`
}

func (in Input) ladder() []float64 {
	l := in.Ladder
	if len(l) == 0 {
		l = Ladder()
	}
	l = slices.Clone(l)
	if in.Current > 0 && !slices.Contains(l, in.Current) {
		l = append(l, in.Current)
	}
	slices.Sort(l)
	return l
}

func secs(v float64) string { return fmt.Sprintf("%g s", math.Round(v*10)/10) }

func pct(v float64) string { return fmt.Sprintf("%.1f%%", v*100) }

const NewLengthGain = 0.05

func saturationCap(in Input) float64 {
	satCap := SaturationFloor
	if in.Current > 0 {
		if ref, n := SaturatedShareMedian(in.Stars, in.Current); n > 0 && ref > satCap {
			satCap = ref
		}
	}
	return satCap
}

func candidates(in Input, satCap float64) []Candidate {
	rn := in.Sensor.ReadNoise
	ladder := in.ladder()
	cands := make([]Candidate, 0, len(ladder))
	for _, t := range ladder {
		c := Candidate{
			Exposure: t, Darks: slices.Contains(in.DarkLengths, t),
			ReadNoiseShare: ReadNoiseShare(in.Sky, t, rn), DutyCycle: t / (t + in.Overhead), Kept: in.Rejection.Kept(t),
			SkyToReadNoise: math.Sqrt(in.Sky*t) / rn,
		}
		c.Efficiency = c.ReadNoiseShare * c.DutyCycle * c.Kept
		if f, n := SaturatedShareMedian(in.Stars, t); n > 0 {
			c.SaturatedShare = &f
			if f > satCap+1e-9 {
				c.Blocked = fmt.Sprintf("saturates %s of the stars, over the %s cap", pct(f), pct(satCap))
			}
		}
		if c.Blocked == "" && in.Rejection.MaxLength > 0 && t > in.Rejection.MaxLength {
			c.Blocked = fmt.Sprintf("has no graded subs longer than %s to measure rejection by", secs(in.Rejection.MaxLength))
		}
		cands = append(cands, c)
	}
	return cands
}

func shortestNearBest(cands []Candidate, needDarks bool) int {
	best := 0.0
	for _, c := range cands {
		if c.Blocked == "" && (!needDarks || c.Darks) {
			best = math.Max(best, c.Efficiency)
		}
	}
	if best <= 0 {
		return -1
	}
	for i, c := range cands {
		if c.Blocked == "" && (!needDarks || c.Darks) && math.Sqrt(c.Efficiency/best) >= NearBest {
			return i
		}
	}
	return -1
}

func choose(cands []Candidate) (use, ideal int) {
	use = shortestNearBest(cands, true)
	if use < 0 {
		return use, shortestNearBest(cands, false)
	}
	ideal = use
	if c := shortestNearBest(cands, false); c >= 0 && !cands[c].Darks && ratio(cands[c].Efficiency, cands[use].Efficiency) >= NewLengthGain {
		ideal = c
	}
	return use, ideal
}

func ratio(a, b float64) float64 {
	if !(b > 0) {
		return 0
	}
	return math.Sqrt(a/b) - 1
}

func Recommend(in Input) (Recommendation, bool) {
	if !(in.Sky > 0) || in.Sensor.EPerADU == 0 {
		return Recommendation{}, false
	}
	satCap := saturationCap(in)
	cands := candidates(in, satCap)
	use, ideal := choose(cands)
	if ideal < 0 {
		return Recommendation{}, false
	}
	rec := Recommendation{
		Current: in.Current, SkyLimited: SkyLimited(in.Sky, in.Sensor.ReadNoise), SkyRate: in.Sky, SkySubs: in.SkySubs, SkySource: in.SkySource,
		ReadNoise: in.Sensor.ReadNoise, EPerADU: in.Sensor.EPerADU, Overhead: in.Overhead, SaturationCap: satCap, Ideal: cands[ideal].Exposure,
	}
	cur := -1.0
	for _, c := range cands {
		if c.Exposure == in.Current {
			cur = c.Efficiency
		}
	}
	for i := range cands {
		if cur > 0 {
			cands[i].RelativeSNRRate = ratio(cands[i].Efficiency, cur) + 1
		}
	}
	if use >= 0 {
		rec.Exposure = cands[use].Exposure
		rec.SNRChange = ratio(cands[use].Efficiency, cur)
		if ideal != use {
			rec.IdealGain = ratio(cands[ideal].Efficiency, cands[use].Efficiency)
		}
	}
	rec.Candidates = cands
	rec.Basis = basis(rec, in, cands, ideal, use)
	return rec, true
}

func basis(rec Recommendation, in Input, cands []Candidate, ideal, use int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Sky %.3g e⁻/s per pixel (median of %d subs, %s); read noise %.2f e⁻ at gain %g. ", rec.SkyRate, rec.SkySubs, rec.SkySource, rec.ReadNoise, in.Sensor.Gain)
	fmt.Fprintf(&b, "Sky noise reaches 3× read noise (5%% SNR cost) at %s. ", secs(rec.SkyLimited))
	if use >= 0 {
		c := cands[use]
		fmt.Fprintf(&b, "%s gets %s of an ideal sub's SNR per hour (time lost to read noise %s, to overhead %s, to rejected subs %s), within 5%% of the best length allowed",
			secs(c.Exposure), pct(math.Sqrt(c.Efficiency)), pct(1-c.ReadNoiseShare), pct(1-c.DutyCycle), pct(1-c.Kept))
		if c.SaturatedShare != nil {
			fmt.Fprintf(&b, "; it saturates %s of the stars (cap %s)", pct(*c.SaturatedShare), pct(rec.SaturationCap))
		}
		b.WriteString(". ")
	} else {
		b.WriteString("No length with a full dark set is allowed. ")
	}
	for _, c := range cands {
		if c.Blocked != "" && c.Exposure > rec.Exposure {
			fmt.Fprintf(&b, "%s is not used: it %s. ", secs(c.Exposure), c.Blocked)
			break
		}
	}
	if ideal >= 0 && ideal != use {
		fmt.Fprintf(&b, "%s would gain %s SNR per hour, worth a new set of dark frames.", secs(cands[ideal].Exposure), pct(rec.IdealGain))
	}
	return strings.TrimSpace(b.String())
}
