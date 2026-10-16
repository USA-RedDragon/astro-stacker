// Package ownimage runs the checks that need the user's own subs: hot
// pixel, reference contamination, satellite trail, star shape, saturation
// and edge, and forced PSF photometry of the spot on every sub against an
// ensemble of steady Gaia stars.
package ownimage

import (
	"context"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
)

// Night is one night and filter group the spot was flagged on.
type Night struct {
	Night       time.Time `json:"night"`
	Group       string    `json:"group"`
	CandidateID int       `json:"candidate_id"`
	RunID       int       `json:"run_id"`
	X           float64   `json:"x"`
	Y           float64   `json:"y"`
	Sign        int       `json:"sign"`
	DeltaMag    *float64  `json:"delta_mag,omitempty"`
	SNR         float64   `json:"snr"`
	PresentIn   int       `json:"present_in"`
	SubsTested  int       `json:"subs_tested"`
	// Light is the detection's per sub difference photometry.
	Light []discoveries.LightPoint `json:"light,omitempty"`
	// CutoutKey is the prefix of the stored night, ref, diff and stamp
	// files in the processed bucket.
	CutoutKey string `json:"cutout_key,omitempty"`
	// RunAt is when the comparison ran; subs added after it were not in
	// its reference.
	RunAt time.Time `json:"run_at"`
}

// Spot is one source to vet.
type Spot struct {
	SourceID int      `json:"source_id"`
	Object   string   `json:"object"`
	Kind     string   `json:"kind"`
	RA       float64  `json:"ra"`
	Dec      float64  `json:"dec"`
	GaiaG    *float64 `json:"gaia_g,omitempty"`
	Nights   []Night  `json:"nights"`
}

// Sub is one registered sub of a target.
type Sub struct {
	FrameID  int
	Filter   string
	Night    time.Time
	Start    time.Time
	Exposure float64
	Weight   float64
	Key      string
	// DarkKey is the master dark the sub was calibrated with, when known.
	DarkKey *string
	// AddedAt is when the sub was added to its stack.
	AddedAt time.Time
}

// Data is what the checks read.
type Data interface {
	// Subs lists the target's registered subs, oldest first.
	Subs(ctx context.Context, object string) ([]Sub, error)
	// Load reads one registered sub; pixels with no data are NaN.
	Load(ctx context.Context, key string) (*discoveries.Plane, error)
	// Get reads any object from the processed bucket: stamps and masters.
	Get(ctx context.Context, key string) ([]byte, error)
	// Frame is the target's registration frame: its plate solution and
	// size.
	Frame(ctx context.Context, object string) (*discoveries.WCS, int, int, error)
	// Stars are the Gaia stars within radius degrees, with BP and RP.
	Stars(ctx context.Context, ra, dec, radius float64) ([]Star, error)
}

// Star is a Gaia star near the spot.
type Star struct {
	SourceID int64   `json:"source_id"`
	RA       float64 `json:"ra"`
	Dec      float64 `json:"dec"`
	G        float64 `json:"g"`
	BPRP     float64 `json:"bp_rp"`
	Variable bool    `json:"variable"`
}

// Flags are the image check outcomes the suggested verdict reads.
type Flags struct {
	HotPixel        bool `json:"hot_pixel"`
	RefContaminated bool `json:"ref_contaminated"`
	Trail           bool `json:"trail"`
	NotStar         bool `json:"not_star"`
	Edge            bool `json:"edge"`
}

// Point is one sub's forced PSF measurement of the spot, in magnitudes
// fainter than the star's normal level in that filter.
type Point struct {
	FrameID int     `json:"frame_id"`
	BJD     float64 `json:"bjd"`
	Night   string  `json:"night"`
	Filter  string  `json:"filter"`
	DMag    float64 `json:"dmag"`
	Err     float64 `json:"err"`
}

// NightLevel is one night's mean level in one filter.
type NightLevel struct {
	Night  string  `json:"night"`
	Filter string  `json:"filter"`
	N      int     `json:"n"`
	DMag   float64 `json:"dmag"`
	Err    float64 `json:"err"`
}

// Curve is the spot's own light curve: every sub covering it, measured by
// PSF fit against Comps steady Gaia stars within 1 mag, Dropped of them
// left out for scattering more than 1.5 times the median.
type Curve struct {
	Comps   int          `json:"comps"`
	Dropped int          `json:"dropped"`
	Points  []Point      `json:"points"`
	Nights  []NightLevel `json:"nights"`
	// Scatter is the typical single sub scatter on normal nights.
	Scatter float64 `json:"scatter"`
	// Change is the flagged night's mean level and its significance.
	ChangeNight string  `json:"change_night"`
	Change      float64 `json:"change"`
	ChangeSigma float64 `json:"change_sigma"`
	// Quiet is the largest departure of any other night.
	Quiet float64 `json:"quiet"`
}

// Series is the curve as light curves, one per filter.
func (c *Curve) Series() []lc.Series { return seriesOf(c) }

// Result is the image checks and own light curve of one spot.
type Result struct {
	SourceID int           `json:"source_id"`
	Checks   []check.Check `json:"checks"`
	Flags    Flags         `json:"flags"`
	Curve    *Curve        `json:"curve,omitempty"`
}

// Options bound the work.
type Options struct {
	Threads int
	// RefSubs is the most subs per filter a night's reference took, as
	// the discoveries runner's setting; 0 is its default of 48.
	RefSubs int
}

// Measure checks every spot of one target, reading each sub once.
func Measure(ctx context.Context, d Data, object string, spots []Spot, opts Options) ([]Result, error) {
	return measure(ctx, d, object, spots, opts)
}
