// Package vetting checks each new discovery automatically: image checks,
// the spot's own light curve, survey history with a period search,
// catalogs and known moving objects, and suggests a verdict. The user's
// own vetting stays the final say.
package vetting

import (
	"slices"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/eclfacts"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/period"
)

// Version is the vetting logic's version; reports from an older one are
// run again.
const Version = 1

// Suggestion is the suggested verdict. State is one of the user's vetting
// states: real, artifact, known or unsure. Kind says what a real verdict
// on a star found it to be; it is empty on every other verdict and on
// reports written before it was kept.
type Suggestion struct {
	State      string `json:"state"`
	Say        string `json:"say"`
	Why        string `json:"why"`
	Confidence string `json:"confidence"`
	Kind       string `json:"kind,omitempty"`
}

// Kinds of a real verdict: an eclipsing binary whose period passed the
// tests for false periods, a star with any other period that passed
// them, a star that fades or brightens now and then with no period, and
// a star that changes irregularly beyond nearby stars.
const (
	KindEclipsingBinary = "eclipsing_binary"
	KindPeriodic        = "periodic"
	KindFades           = "fades"
	KindBrightens       = "brightens"
	KindIrregular       = "irregular"
)

// Confidence levels.
const (
	High   = "high"
	Medium = "medium"
	Low    = "low"
)

// Group is one heading of checks in the panel.
type Group struct {
	ID     string        `json:"id"`
	Name   string        `json:"name"`
	Checks []check.Check `json:"checks"`
}

// ChartPoint is one plotted point; Err is its one sigma error, 0 for none.
type ChartPoint struct {
	X   float64 `json:"x"`
	Y   float64 `json:"y"`
	Err float64 `json:"err,omitempty"`
}

// ChartRef is a horizontal line: "base" for the normal level, "fade" for
// where the reported change would put the star.
type ChartRef struct {
	Y     float64 `json:"y"`
	Style string  `json:"style"`
}

// LegendItem is one key entry: Style is "dot", "line" or "dash".
type LegendItem struct {
	Style string `json:"style"`
	Label string `json:"label"`
}

// Chart is one plot the panel draws. Y grows downward on screen (fainter
// is lower). XUnit is "hours", "phase" or "year" and YUnit "dmag" or
// "mag", for the hover text.
type Chart struct {
	ID     string       `json:"id"`
	Title  string       `json:"title"`
	Sub    string       `json:"sub"`
	XLabel string       `json:"x_label"`
	YLabel string       `json:"y_label"`
	XUnit  string       `json:"x_unit"`
	YUnit  string       `json:"y_unit"`
	X0     float64      `json:"x0"`
	X1     float64      `json:"x1"`
	Y0     float64      `json:"y0"`
	Y1     float64      `json:"y1"`
	XTicks []float64    `json:"x_ticks"`
	YTicks []float64    `json:"y_ticks"`
	Points []ChartPoint `json:"points"`
	Line   [][2]float64 `json:"line,omitempty"`
	Refs   []ChartRef   `json:"refs,omitempty"`
	VRef   *float64     `json:"vref,omitempty"`
	Legend []LegendItem `json:"legend,omitempty"`
	Empty  string       `json:"empty,omitempty"`
}

// Report is one source's vetting.
type Report struct {
	Version int `json:"version"`
	// Pipeline and Build name the code that wrote the report.
	Pipeline string    `json:"pipeline,omitempty"`
	Build    string    `json:"build,omitempty"`
	SourceID int       `json:"source_id"`
	RanAt    time.Time `json:"ran_at"`
	// Star names the star at the spot, "2MASS J00433343+4139446 · Gaia G
	// 15.04", or only its magnitude.
	Star       string            `json:"star"`
	Suggestion Suggestion        `json:"suggestion"`
	Groups     []Group           `json:"groups"`
	Charts     []Chart           `json:"charts"`
	Ephemeris  *period.Ephemeris `json:"ephemeris,omitempty"`
	// Survey is what the survey light curves say about an eclipsing
	// ephemeris's dips: points and eclipses caught, depths with errors,
	// normal brightness. SurveyDoubled is the same at twice the period for
	// a star with one dip per cycle.
	Survey        *eclfacts.Summary `json:"survey,omitempty"`
	SurveyDoubled *eclfacts.Summary `json:"survey_doubled,omitempty"`
	// Pending names surveys whose data is still being fetched; a later
	// run fills them in.
	Pending []string `json:"pending,omitempty"`
	// FadesRead says the report knows the star's ZTF fades: ZTF was read
	// or does not cover it, or the find is a transient with no star. Fades
	// is how they compare with nearby stars, nil when no fade was backed
	// up on two or more nights.
	FadesRead bool          `json:"fades_read,omitempty"`
	Fades     *period.Fades `json:"fades,omitempty"`
	// FadeNightsRead says ZTFFadeNights is filled in, which reports made
	// before it was added are not. ZTFFadeNights are the dates of the ZTF
	// nights, from one up, on which two or more fading points back each
	// other up and the star fades beyond nearby stars that stay steady,
	// each the evening the night began at the survey.
	FadeNightsRead bool     `json:"fade_nights_read,omitempty"`
	ZTFFadeNights  []string `json:"ztf_fade_nights,omitempty"`
}

// eveningDates are the evening dates of the nights holding the BJDs: the
// UTC date twelve hours earlier, which is the local evening date at a
// survey in the Americas.
func eveningDates(bjds []float64) []string {
	var out []string
	for _, b := range bjds {
		t := time.Unix(0, 0).UTC().Add(time.Duration((b - 2440587.5) * 86400 * float64(time.Second)))
		d := t.Add(-12 * time.Hour).Format(time.DateOnly)
		if !slices.Contains(out, d) {
			out = append(out, d)
		}
	}
	return out
}

// State of a source's vetting run.
const (
	StateQueued  = "queued"
	StateRunning = "running"
	StateDone    = "done"
	StateFailed  = "failed"
)
