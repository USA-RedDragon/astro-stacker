// Package period searches survey light curves for a period, fits an
// eclipse shape, tests half and double the period and predicts the
// star's brightness at any time.
package period

import (
	"errors"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
)

// ErrNoPeriod says no period stood out.
var ErrNoPeriod = errors.New("no period stood out")

// Kind is the shape of the light curve's variation.
type Kind string

const (
	KindEclipsing  Kind = "eclipsing"
	KindSinusoidal Kind = "sinusoidal"
	KindFlat       Kind = "flat"
	KindIrregular  Kind = "irregular"
)

// Eclipse is one dip of the fitted model: the star's own depth per survey
// band in magnitudes (keyed "ZTF r", "ATLAS o", "TESS", "NEOWISE W1", ...,
// with any blending taken out), its phase (0 for the deeper dip) and its
// half width W as a share of the period. Shape is the exponent a of the
// dip (1 - (dt/W)^2)^a, for |dt| < W with dt in cycles from the minimum.
type Eclipse struct {
	Phase float64            `json:"phase"`
	Width float64            `json:"width"`
	Shape float64            `json:"shape"`
	Depth map[string]float64 `json:"depth"`
}

// Ephemeris is the fitted period and the time of the deeper minimum, in
// BJD TDB, with their one sigma errors.
type Ephemeris struct {
	Period    float64   `json:"period"`
	PeriodErr float64   `json:"period_err"`
	T0        float64   `json:"t0"`
	T0Err     float64   `json:"t0_err"`
	Kind      Kind      `json:"kind"`
	Eclipses  []Eclipse `json:"eclipses"`
	// Amplitude is the full range in magnitudes of a sinusoidal fit, per
	// survey band.
	Amplitude map[string]float64 `json:"amplitude,omitempty"`
}

// Phase is where bjd falls in the cycle, 0 at the deeper minimum, in
// [-0.25, 0.75).
func (e *Ephemeris) Phase(bjd float64) float64 { return phaseOf(e, bjd) }

// Predict is the model's magnitudes fainter than normal at bjd, for a
// survey band key; an unknown band takes the mean depth of the known ones.
func (e *Ephemeris) Predict(bjd float64, band string) float64 { return predict(e, bjd, band) }

// SurveyFit is how one survey's own data agree with the ephemeris: how
// many of its points fall in each dip, whether those points are fainter
// by the model's depth, and for well sampled dips (TESS) their timing.
type SurveyFit struct {
	Survey  string  `json:"survey"`
	Band    string  `json:"band"`
	Points  int     `json:"points"`
	InDip1  int     `json:"in_dip1"`
	InDip2  int     `json:"in_dip2"`
	Depth1  float64 `json:"depth1"`
	Depth2  float64 `json:"depth2"`
	Agrees  bool    `json:"agrees"`
	Minima  int     `json:"minima"`
	MaxLagM float64 `json:"max_lag_min"`
}

// Result is the period analysis of every series of one star.
type Result struct {
	Ephemeris *Ephemeris  `json:"ephemeris,omitempty"`
	Fits      []SurveyFit `json:"fits"`
	// Checks has the survey history rows: Period (when no period was
	// found), Fold, Half, Double, Echo, Comparison, and the per survey
	// rows ZTF, TESS, ATLAS and NEOWISE.
	Checks []check.Check `json:"checks"`
	// Robust is how the period stands up to the tests for false periods,
	// when one was found.
	Robust *Robust `json:"robust,omitempty"`
}

// Options bound the search. MinPeriod and MaxPeriod are in days, 0.2
// and 60 when zero. ChangeMag is the change the user reported, in
// magnitudes, positive for a fade; a flat survey light curve is checked
// against it. Comparisons, when set, gives the ZTF light curves of nearby
// steady stars of the same brightness, asked for only once a period is
// found.
type Options struct {
	MinPeriod   float64
	MaxPeriod   float64
	ChangeMag   float64
	Comparisons func() [][]lc.Series
}

// Analyze searches series for a period and tests it. series holds every
// survey's light curve of the star; the ones from the user's own images
// (lc.Own) are only used for the timing check, not the search. Each
// series is put on its own median first. The period, epoch and dip shapes
// come from ZTF and TESS alone when they catch every dip in at least three
// cycles, since ATLAS and NEOWISE timing scatters more than their errors
// say; those surveys then add depths and odd and even cycle coverage.
// A TESS series names its sector
// in Band or Note ("S17" or "Sector 17"). When no period stands out the
// error is ErrNoPeriod and the Result still holds the survey rows; when
// there were survey points its Ephemeris has Kind flat or irregular and
// no period.
func Analyze(series []lc.Series, opts Options) (*Result, error) {
	return analyze(series, opts, nil)
}

// AnalyzeAt tests a known ephemeris as Analyze tests the one it finds:
// the model is fitted from e's kind, period, epoch and dips, and every
// check and test for false periods runs on it.
func AnalyzeAt(series []lc.Series, e *Ephemeris, opts Options) (*Result, error) {
	return analyze(series, opts, e)
}

// Timing is the user's own minimum against the ephemeris: Offset in
// minutes, negative when early, with its one sigma error, from a fit of
// the model's dip shape to points around the predicted minimum.
type Timing struct {
	Offset    float64 `json:"offset_min"`
	OffsetErr float64 `json:"offset_err_min"`
	Depth     float64 `json:"depth"`
	Dip       int     `json:"dip"`
	Points    int     `json:"points"`
}

// TimeMinimum fits the dip the points of one night cover. ok is false
// when the points do not cover a dip's minimum.
func TimeMinimum(e *Ephemeris, points []lc.Point, band string) (Timing, bool) {
	return timeMinimum(e, points, band)
}
