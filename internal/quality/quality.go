// Package quality scores subframes from the metadata Target Scheduler
// records for every acquired image, without reading any pixels. It is a copy
// of astro-processing's package of the same name; keep the two in step.
//
// The weight model is 1 / (sky * HFR^4), where sky is the ADU median above
// the camera pedestal. It mirrors what PixInsight's PSF Signal Weight
// measures: a star's peak scales with 1/FWHM^2, SNR^2 squares that, and in
// sky-limited data the noise variance scales with the sky background.
// Checked against WBPP's PSF Signal Weight on 911 M31 subs, it ranks subs
// with a Spearman correlation of 0.94 to 0.99 in every filter/exposure group.
package quality

import (
	"encoding/json"
	"math"
	"sort"
	"strconv"
)

// DefaultPedestal is the ADU offset the ASI2600MM adds at offset 50,
// measured as raw ADU median minus WBPP's calibrated median (p10 505, p90 518).
const DefaultPedestal = 506.0

// pedestalOffset is the camera offset DefaultPedestal was measured at, and
// pedestalPerOffset the ADU each unit of offset adds: the offset-240 master
// bias sits at 2403 ADU, 1897 above offset 50's.
const (
	pedestalOffset    = 50
	pedestalPerOffset = 10.0
)

// PedestalAt is the pedestal at a camera offset, from the pedestal at offset
// 50. An unknown offset (0 or NaN) is taken as 50.
func PedestalAt(pedestal, offset float64) float64 {
	if !(offset > 0) {
		return pedestal
	}
	return pedestal + (offset-pedestalOffset)*pedestalPerOffset
}

// ReferencePercentile picks the "good conditions" weight a group is
// normalized against. Using a high percentile rather than the maximum keeps
// one freak sub from deflating every other sub's score.
const ReferencePercentile = 0.9

// Float decodes a JSON number or a string such as "NaN", which is how
// Target Scheduler writes metrics it could not measure.
type Float float64

func (f *Float) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			*f = Float(math.NaN())
			return nil //nolint:nilerr // unparseable metrics count as missing
		}
		*f = Float(v)
		return nil
	}
	var v float64
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*f = Float(v)
	return nil
}

// Metadata is the subset of acquiredimage.metadata used for scoring.
type Metadata struct {
	FileName          string `json:"FileName"`
	FilterName        string `json:"FilterName"`
	ExposureStartTime string `json:"ExposureStartTime"`
	ExposureDuration  Float  `json:"ExposureDuration"`
	DetectedStars     Float  `json:"DetectedStars"`
	HFR               Float  `json:"HFR"`
	FWHM              Float  `json:"FWHM"`
	Eccentricity      Float  `json:"Eccentricity"`
	ADUMedian         Float  `json:"ADUMedian"`
	Offset            Float  `json:"Offset"`
	GuidingRMSArcSec  Float  `json:"GuidingRMSArcSec"`
	Airmass           Float  `json:"Airmass"`
}

// ParseMetadata decodes a Target Scheduler metadata blob.
func ParseMetadata(raw string) (Metadata, error) {
	var m Metadata
	err := json.Unmarshal([]byte(raw), &m)
	return m, err
}

// Sky returns the background above the pedestal, or NaN if it is not positive.
func Sky(aduMedian, pedestal float64) float64 {
	s := aduMedian - pedestal
	if !(s > 0) {
		return math.NaN()
	}
	return s
}

// RawWeight returns 1 / (sky * HFR^4), or NaN when either input is missing.
// It is only meaningful relative to other subs of the same filter and exposure.
func RawWeight(sky, hfr float64) float64 {
	if !(sky > 0) || !(hfr > 0) {
		return math.NaN()
	}
	return 1 / (sky * math.Pow(hfr, 4))
}

// Reference returns the ReferencePercentile raw weight of a group, ignoring
// NaNs. It returns NaN for an empty group.
func Reference(weights []float64) float64 {
	vals := make([]float64, 0, len(weights))
	for _, w := range weights {
		if !math.IsNaN(w) {
			vals = append(vals, w)
		}
	}
	if len(vals) == 0 {
		return math.NaN()
	}
	sort.Float64s(vals)
	pos := ReferencePercentile * float64(len(vals)-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	return vals[lo] + (vals[hi]-vals[lo])*(pos-float64(lo))
}

// Score is a sub's weight relative to good conditions for its group, capped
// at 1. A score of 0.25 means the sub is worth a quarter of a good sub.
// It returns 0 when the weight or reference is missing.
func Score(raw, reference float64) float64 {
	if math.IsNaN(raw) || !(reference > 0) {
		return 0
	}
	return math.Min(1, raw/reference)
}
