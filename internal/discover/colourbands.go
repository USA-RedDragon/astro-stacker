package discover

import (
	"fmt"
	"strings"

	"github.com/USA-RedDragon/astro-stacker/internal/rigsource"
	"github.com/USA-RedDragon/astro-stacker/internal/skybright"
	"github.com/USA-RedDragon/astro-stacker/internal/starfront"
)

const (
	colourPrefix = "OSC"
	criterionSky = "Sky darkness"
	bortleNote   = "classes from Wikipedia's Bortle table of sky-meter readings, whose band differs a little from the one your sky is measured in"
)

func IsColourBand(name string) bool {
	n := strings.ToUpper(strings.TrimSpace(name))
	if n == colourPrefix {
		return true
	}
	if !strings.HasPrefix(n, colourPrefix) || len(n) == len(colourPrefix) {
		return false
	}
	switch n[len(colourPrefix)] {
	case '-', ' ', '_':
		return true
	}
	return false
}

func BandName(name string) string {
	if !IsColourBand(name) {
		return CanonicalFilter(name)
	}
	n := strings.TrimSpace(name)
	if len(n) == len(colourPrefix) {
		return colourPrefix
	}
	rest := strings.TrimLeft(n[len(colourPrefix):], "-_ ")
	for _, k := range knownColourBands() {
		if strings.EqualFold(strings.ReplaceAll(rest, " ", ""), k) {
			return colourPrefix + "-" + k
		}
	}
	return colourPrefix + "-" + rest
}

func knownColourBands() []string {
	return []string{"RGB", "Ha+OIII", "SII+OIII", "Ha+SII", "Ha+OIII+SII", "Ha", "OIII", "SII"}
}

func colourBandText(band string) string {
	inner := strings.ToUpper(strings.ReplaceAll(strings.TrimPrefix(BandName(band), colourPrefix+"-"), " ", ""))
	switch inner {
	case colourPrefix:
		return "a colour camera"
	case "RGB":
		return "a colour camera with no narrowband filter: red, green and blue in one sub"
	case "HA+OIII":
		return "a colour camera behind a dual-band filter: Ha and OIII in one sub"
	case "SII+OIII":
		return "a colour camera behind a dual-band filter: SII and OIII in one sub"
	case "HA+SII":
		return "a colour camera behind a dual-band filter: Ha and SII in one sub"
	case "HA+OIII+SII":
		return "a colour camera behind a tri-band filter: Ha, OIII and SII in one sub"
	}
	return "a colour camera: " + strings.TrimPrefix(BandName(band), colourPrefix+"-") + " in one sub"
}

func needs(n int) string {
	if n == 1 {
		return "needs"
	}
	return "need"
}

func colourBands(r starfront.Requirements) []string {
	want, _ := wantedFilters(r)
	var out []string
	for _, f := range want {
		if IsColourBand(f) {
			out = append(out, f)
		}
	}
	return out
}

func colourOnlyBands(r starfront.Requirements, rig Rig, info rigsource.Rig) []string {
	if b := colourBands(r); info.Colour != nil && !rig.Colour && b != nil {
		return b
	}
	return []string{}
}

func yesNo(b *bool) string {
	switch {
	case b == nil:
		return "not set"
	case *b:
		return "yes"
	}
	return "no"
}

func colourBandCriteria(r starfront.Requirements, rig Rig, info rigsource.Rig) []Criterion {
	var out []Criterion
	note := func(name, rule, you string) {
		out = append(out, Criterion{Name: name, Rule: rule, You: you, Result: CriterionNone, Scope: ScopeInfo})
	}
	camera := func() string {
		switch {
		case info.Colour == nil:
			return "camera type not measured"
		case rig.Colour:
			return "colour camera: can take it"
		}
		return "mono camera: not for this rig"
	}
	for _, b := range colourBands(r) {
		note(b, colourBandText(b), camera())
	}
	if r.AcceptOscRgb != nil || r.AcceptOscNarrowband != nil {
		you := "colour camera"
		switch {
		case info.Colour == nil:
			you = "camera type not measured"
		case !rig.Colour:
			you = "mono camera: does not apply"
		}
		note("Colour cameras", fmt.Sprintf("takes colour subs with no narrowband filter (OSC-RGB): %s · takes colour narrowband subs: %s", yesNo(r.AcceptOscRgb), yesNo(r.AcceptOscNarrowband)), you)
	}
	return out
}

func siteCriteria(r starfront.Requirements, rig Rig, info rigsource.Rig, ev evidence) []Criterion {
	out := colourBandCriteria(r, rig, info)
	if c, ok := bortleCriterion(r, ev.sky); ok {
		out = append(out, c)
	}
	return out
}

func Bortle(mag float64) float64 {
	switch {
	case mag >= 21.76:
		return 1
	case mag >= 21.6:
		return 2
	case mag >= 21.3:
		return 3
	case mag >= 20.8:
		return 4
	case mag >= 20.3:
		return 4.5
	case mag >= 19.25:
		return 5
	case mag >= 18.5:
		return 6
	case mag >= 18:
		return 7
	}
	return 8
}

func bortleText(b float64) string {
	if b >= 8 {
		return "Bortle 8 or 9"
	}
	return fmt.Sprintf("Bortle %g", b)
}

func bortleCriterion(r starfront.Requirements, v skybright.Value) (Criterion, bool) {
	if r.MaxBortle == nil {
		return Criterion{}, false
	}
	c := Criterion{Name: criterionSky, Rule: fmt.Sprintf("Bortle %d or darker", *r.MaxBortle), Scope: ScopeRig}
	switch {
	case v.Mag == nil:
		reason := "no sky brightness from subs yet"
		if v.Basis.Reason != nil && *v.Basis.Reason != "" {
			reason = *v.Basis.Reason
		}
		c.You, c.Result = "not measured: "+reason, CriterionOpen
	case v.Basis.Source != skybright.SourceMeasured:
		c.You = fmt.Sprintf("not measured: the sky brightness is only set by hand in discover.sky-brightness (%.2f mag/arcsec²)", *v.Mag)
		c.Result = CriterionOpen
	default:
		b := Bortle(*v.Mag)
		from := "broadband"
		if v.Basis.Filter != "" {
			from = v.Basis.Filter
		}
		nights := "nights"
		if v.Basis.Nights == 1 {
			nights = "night"
		}
		c.You = fmt.Sprintf("%s, from measured sky %.2f mag/arcsec² in %s subs over %d %s; %s",
			bortleText(b), *v.Mag, from, v.Basis.Nights, nights, bortleNote)
		switch {
		case b >= 8 && *r.MaxBortle == 8:
			c.Result = CriterionOpen
		default:
			c.Result = passIf(b <= float64(*r.MaxBortle))
		}
	}
	return c, true
}
