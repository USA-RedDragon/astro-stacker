package stacking

import (
	_ "embed"
	"encoding/json"
	"math"
	"path"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/frameheader"
)

//go:embed telescopes.json
var telescopesJSON []byte

// telescope is one setup of a remote telescope service, from
// telescopes.json.
type telescope struct {
	Scope    string   `json:"scope"`
	Telescop []string `json:"telescop"`
	Instrume []string `json:"instrume"`
	Camera   string   `json:"camera"`
	FocalMM  float64  `json:"focal_mm"`
	PixelUM  float64  `json:"pixel_um"`
	From     string   `json:"from"`
	Until    string   `json:"until"`
	Source   string   `json:"source"`
}

// telescopes are the setups in telescopes.json.
func telescopes() []telescope {
	var doc struct {
		Telescopes []telescope `json:"telescopes"`
	}
	if err := json.Unmarshal(telescopesJSON, &doc); err != nil {
		panic("telescopes.json: " + err.Error())
	}
	return doc.Telescopes
}

// knownTelescope finds the published setup that took a sub: by the start of
// its file name (Telescope.live names subs "SPA-2-CCD_2021-06-08T…"), else
// by its TELESCOP and INSTRUME. Where a setup changed (SPA-2-CCD with and
// without its reducer), its DATE-OBS picks the one; undated, the latest.
func knownTelescope(kw frameheader.Keywords, key string) (telescope, bool) {
	when, dated := kw.Time("DATE-OBS")
	if !dated {
		when, dated = frameheader.Keywords{"DATE-OBS": strings.TrimSuffix(kw.String("DATE-OBS"), "Z")}.Time("DATE-OBS")
	}
	scope, _, _ := strings.Cut(path.Base(key), "_")
	tel := strings.TrimSpace(kw.String("TELESCOP"))
	inst := strings.ToUpper(strings.TrimSpace(kw.String("INSTRUME")))
	var byScope, byName []telescope
	for _, t := range telescopes() {
		switch {
		case t.Scope == scope:
			byScope = append(byScope, t)
		case tel != "" && t.matches(tel, inst):
			byName = append(byName, t)
		}
	}
	for _, set := range [][]telescope{byScope, byName} {
		if t, ok := pickDated(set, when, dated); ok {
			return t, true
		}
	}
	return telescope{}, false
}

// pickDated is the setup of set in use at when, the only one, or undated
// the latest.
func pickDated(set []telescope, when time.Time, dated bool) (telescope, bool) {
	switch {
	case len(set) == 0:
		return telescope{}, false
	case len(set) == 1:
		return set[0], true
	case !dated:
		return set[len(set)-1], true
	}
	for _, t := range set {
		if t.covers(when) {
			return t, true
		}
	}
	return telescope{}, false
}

func (t telescope) covers(when time.Time) bool {
	if from, err := time.Parse(time.DateOnly, t.From); err == nil && when.Before(from) {
		return false
	}
	if until, err := time.Parse(time.DateOnly, t.Until); err == nil && !when.Before(until) {
		return false
	}
	return true
}

func (t telescope) matches(tel, inst string) bool {
	named := false
	for _, n := range t.Telescop {
		if n == tel {
			named = true
			break
		}
	}
	if !named {
		return false
	}
	for _, i := range t.Instrume {
		if inst != "" && strings.HasPrefix(inst, strings.ToUpper(i)) {
			return true
		}
	}
	return false
}

// Where a frame's optics came from.
const (
	opticsHeader   = "header"
	opticsSolution = "plate solution"
)

// frameOptics is the focal length and pixel size, in mm and µm, a frame
// was taken with, and where they came from: its FOCALLEN and XPIXSZ, else
// the pixel scale of the plate solution in its header, else the published
// optics of the telescope that took it. XPIXSZ is the pixel after binning,
// as NINA and Telescope.live write it. Both are 0 when nothing gives them.
func frameOptics(kw frameheader.Keywords, key string) (focal, pixel float64, source string) {
	focal, pixel = kw.Float("FOCALLEN"), kw.Float("XPIXSZ")
	if focal > 0 && pixel > 0 {
		return focal, pixel, opticsHeader
	}
	t, known := knownTelescope(kw, key)
	if !(pixel > 0) && known {
		pixel = t.PixelUM * binning(kw)
	}
	if focal > 0 && pixel > 0 {
		return focal, pixel, opticsHeader
	}
	if f, px := solutionOptics(kw, pixel); f > 0 && px > 0 {
		return f, px, opticsSolution
	}
	if known {
		return t.FocalMM, pixel, t.label()
	}
	return 0, 0, ""
}

func (t telescope) label() string {
	return "Telescope.live " + t.Scope + " published optics"
}

func binning(kw frameheader.Keywords) float64 {
	if b := kw.Float("XBINNING"); b >= 1 {
		return b
	}
	return 1
}

// pixelScale is the arcseconds per pixel of focal and pixel.
func pixelScale(focal, pixel float64) float64 {
	return math.Atan(pixel/1000/focal) / deg * 3600
}
