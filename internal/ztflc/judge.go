// Package ztflc asks the public ZTF light curves at IRSA whether a star
// varies, to tell a real brightness change from an artifact of ours.
package ztflc

import (
	"math"
	"slices"
)

// Kind is what the ZTF light curve says about a star.
type Kind string

const (
	// Unknown means ZTF cannot tell: too few good epochs, too faint or
	// too bright for ZTF, no ZTF source at the position, or variability
	// well below the claimed amplitude but above the noise.
	Unknown Kind = "unknown"
	// Variable means ZTF sees the star vary by at least the claimed amplitude.
	Variable Kind = "variable"
	// Flat means ZTF has many good epochs and the star stays well below
	// the claimed amplitude and near the photometric noise.
	Flat Kind = "flat"
)

const (
	// MinFlatEpochs is the number of good epochs in one band a Flat verdict needs.
	MinFlatEpochs = 20
	// MinVariableEpochs is the number of good epochs in one band a Variable verdict needs.
	MinVariableEpochs = 10
	// MinOIDEpochs is the number of good epochs a ZTF object needs to count.
	MinOIDEpochs = 5
	// VariableNoise is how many median errors the robust amplitude must
	// reach before it is variability rather than noise.
	VariableNoise = 4.5
	// VariableErrors is how many median errors below the claimed
	// amplitude the robust amplitude may be and still match it.
	VariableErrors = 2.0
	// FlatFraction is the largest share of the claimed amplitude a flat
	// star's robust amplitude may be.
	FlatFraction = 1.0 / 3
	// FlatNoise is the largest robust amplitude, in median errors, of a
	// flat star. A constant star with honest errors sits near 3.3.
	FlatNoise = 4.5
	// FlatDeviantShare is the largest share of epochs (at least one) that
	// may stray by half the claimed amplitude in a flat star. It keeps an
	// eclipsing binary, whose dips the percentiles miss, from looking flat.
	// Constant stars had at most 0.5% such epochs.
	FlatDeviantShare = 0.01
	// VariableDeviantShare is the share of epochs (at least
	// VariableDeviantMin) straying by half the claimed amplitude that
	// makes a star variable even when the percentiles miss its dips.
	VariableDeviantShare = 0.02
	// VariableDeviantMin is the fewest straying epochs that make a star variable.
	VariableDeviantMin = 5
	// FaintMag is the median ZTF magnitude beyond which ZTF is too noisy to judge.
	FaintMag = 20.5
	// SaturatedMag is the median ZTF magnitude brighter than which ZTF saturates.
	SaturatedMag = 12.5
	// LowPercentile is the lower bound of the robust amplitude.
	LowPercentile = 0.05
	// HighPercentile is the upper bound of the robust amplitude.
	HighPercentile = 0.95
	// PeakTrim is the share of epochs dropped at each end of the peak-to-peak range.
	PeakTrim = 0.01
)

// Point is one ZTF light curve epoch.
type Point struct {
	OID      int64   `json:"o"`
	Band     string  `json:"b"`
	MJD      float64 `json:"t"`
	Mag      float64 `json:"m"`
	MagErr   float64 `json:"e"`
	CatFlags int     `json:"f"`
	RA       float64 `json:"ra"`
	Dec      float64 `json:"dec"`
}

// BandStat is the robust light curve of one band.
type BandStat struct {
	Band string `json:"band"`
	// Epochs is the number of good epochs used.
	Epochs int `json:"epochs"`
	// Amplitude is the 5th to 95th percentile range of the good epochs,
	// each ZTF object's median removed, in magnitudes.
	Amplitude float64 `json:"amplitude"`
	// Peak is the peak-to-peak range of the good epochs, each object's
	// median removed, after dropping the most extreme PeakTrim share (at
	// least one epoch) at each end as outliers.
	Peak float64 `json:"peak"`
	// MedianErr is the median ZTF photometric error.
	MedianErr float64 `json:"median_err"`
	// MedianMag is the median magnitude.
	MedianMag float64 `json:"median_mag"`
	// Deviant is the number of epochs that stray from the median by more
	// than half the claimed amplitude and by more than five of their errors.
	Deviant int `json:"deviant"`
}

// Verdict is what ZTF says about a claimed brightness change.
type Verdict struct {
	Kind Kind `json:"kind"`
	// Amplitude is the ZTF amplitude in magnitudes of the band behind the
	// verdict: the trimmed peak-to-peak range for Variable and for
	// variability below the claim, the 5th to 95th percentile range for
	// Flat, and zero with no usable band.
	Amplitude float64 `json:"amplitude"`
	// Band is the ZTF band behind the verdict.
	Band   string     `json:"band,omitempty"`
	Reason string     `json:"reason"`
	Bands  []BandStat `json:"bands,omitempty"`
}

// Good says whether an epoch is clean: no catalog flags and finite values.
func Good(p Point) bool {
	return p.CatFlags == 0 && p.Mag > 0 && p.MagErr > 0 && !math.IsNaN(p.Mag) && !math.IsInf(p.Mag, 0) && !math.IsNaN(p.MagErr) && !math.IsInf(p.MagErr, 0)
}

// Stats measures each band's good epochs against a claimed amplitude.
// Each ZTF object (one field and chip) has its median removed, so the
// small zero-point offsets between fields do not count as variability.
func Stats(points []Point, ourAmplitude float64) []BandStat {
	byBand := map[string]map[int64][]Point{}
	for _, p := range points {
		if !Good(p) {
			continue
		}
		if byBand[p.Band] == nil {
			byBand[p.Band] = map[int64][]Point{}
		}
		byBand[p.Band][p.OID] = append(byBand[p.Band][p.OID], p)
	}
	bands := make([]string, 0, len(byBand))
	for b := range byBand {
		bands = append(bands, b)
	}
	slices.Sort(bands)
	out := make([]BandStat, 0, len(bands))
	for _, b := range bands {
		var res, errs, mags []float64
		deviant := 0
		for _, ps := range byBand[b] {
			if len(ps) < MinOIDEpochs {
				continue
			}
			m := make([]float64, len(ps))
			for i, p := range ps {
				m[i] = p.Mag
			}
			med := median(m)
			for _, p := range ps {
				r := p.Mag - med
				res = append(res, r)
				errs = append(errs, p.MagErr)
				mags = append(mags, p.Mag)
				if math.Abs(r) > ourAmplitude/2 && math.Abs(r) > 5*p.MagErr {
					deviant++
				}
			}
		}
		if len(res) == 0 {
			continue
		}
		slices.Sort(res)
		trim := max(1, int(math.Round(PeakTrim*float64(len(res)))))
		peak := 0.0
		if len(res) > 2*trim {
			peak = res[len(res)-1-trim] - res[trim]
		}
		out = append(out, BandStat{
			Band:      b,
			Epochs:    len(res),
			Amplitude: percentile(res, HighPercentile) - percentile(res, LowPercentile),
			Peak:      peak,
			MedianErr: median(errs),
			MedianMag: median(mags),
			Deviant:   deviant,
		})
	}
	return out
}

// Judge says whether ZTF confirms a brightness change of ourAmplitude
// magnitudes on the star whose ZTF epochs are points. Points come from a
// query of the star's position, so no points means no ZTF source there.
func Judge(points []Point, ourAmplitude float64) Verdict {
	ourAmplitude = math.Abs(ourAmplitude)
	if len(points) == 0 {
		return Verdict{Kind: Unknown, Reason: "no ZTF source at the position"}
	}
	stats := Stats(points, ourAmplitude)
	v := Verdict{Kind: Unknown, Bands: stats}
	if len(stats) == 0 {
		v.Reason = "no good ZTF epochs"
		return v
	}
	var usable []BandStat
	for _, s := range stats {
		if s.MedianMag >= SaturatedMag && s.MedianMag <= FaintMag {
			usable = append(usable, s)
		}
	}
	if len(usable) == 0 {
		if stats[0].MedianMag < SaturatedMag {
			v.Reason = "too bright for ZTF"
		} else {
			v.Reason = "too faint for ZTF"
		}
		return v
	}
	var best, lesser *BandStat
	for i, s := range usable {
		dips := float64(s.Deviant) >= math.Max(VariableDeviantMin, VariableDeviantShare*float64(s.Epochs))
		if s.Epochs < MinVariableEpochs || (s.Amplitude < VariableNoise*s.MedianErr && !dips) {
			continue
		}
		if s.Peak+VariableErrors*s.MedianErr >= ourAmplitude {
			if best == nil || s.Peak > best.Peak {
				best = &usable[i]
			}
		} else if lesser == nil || s.Peak > lesser.Peak {
			lesser = &usable[i]
		}
	}
	if best != nil {
		v.Kind, v.Amplitude, v.Band, v.Reason = Variable, best.Peak, best.Band, "ZTF sees the star vary by at least the claimed amplitude"
		return v
	}
	if lesser != nil {
		v.Amplitude, v.Band, v.Reason = lesser.Peak, lesser.Band, "ZTF sees the star vary, but by less than the claimed amplitude"
		return v
	}
	var flat *BandStat
	for i, s := range usable {
		if s.Epochs < MinFlatEpochs || s.MedianErr*3 > ourAmplitude {
			continue
		}
		if s.Amplitude < FlatFraction*ourAmplitude && s.Amplitude < FlatNoise*s.MedianErr && float64(s.Deviant) <= math.Max(1, FlatDeviantShare*float64(s.Epochs)) && (flat == nil || s.Epochs > flat.Epochs) {
			flat = &usable[i]
		}
	}
	if flat != nil {
		v.Kind, v.Amplitude, v.Band, v.Reason = Flat, flat.Amplitude, flat.Band, "ZTF sees the star flat well below the claimed amplitude"
		return v
	}
	v.Reason = "too few good ZTF epochs or too noisy for the claimed amplitude"
	return v
}

func median(xs []float64) float64 {
	s := slices.Clone(xs)
	slices.Sort(s)
	return percentile(s, 0.5)
}

func percentile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return math.NaN()
	}
	pos := q * float64(len(sorted)-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	return sorted[lo] + (sorted[hi]-sorted[lo])*(pos-float64(lo))
}
