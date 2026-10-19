package binaries

import (
	"fmt"
	"math"
	"strings"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/eclfacts"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/period"
)

// Flag ids.
const (
	FlagTwice = "twice"
	FlagHalf  = "half"
	FlagFew   = "few"
	FlagBlend = "blend"
)

// Flag labels.
const (
	labelTwice = "Period could be twice this"
	labelHalf  = "Period could be half this"
)

// Flag is a doubt the checks raise about a binary, with the reason in
// plain words.
type Flag struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Why   string `json:"why"`
}

const (
	// minDeepPoints is the fewest survey points the deeper dip needs, and
	// minEvents the fewest separate eclipses, before the record counts as
	// enough.
	minDeepPoints = 10
	minEvents     = 8
	// strongZ is how many sigma the two dips must differ by, in depth or
	// in the second dip's offset from mid-cycle, to rule out half the
	// period.
	strongZ = 3
	// blendRUWE is the Gaia RUWE above which its single-star fit fails,
	// the usual sign of an unresolved pair.
	blendRUWE = 1.4
)

// Flags are the doubts r's checks and f raise, in a fixed order.
func Flags(r *vetting.Report, f Facts) []Flag {
	var out []Flag
	e := f.Ephemeris
	if e == nil {
		return out
	}
	p := periodText(e.Period, e.PeriodErr)
	switch {
	case f.Doubled && len(e.Eclipses) == 1:
	case f.Doubled:
		if t := f.twin(); t == nil || (t.DepthZ < strongZ && t.PhaseZ < strongZ) {
			why := fmt.Sprintf("The vet's odd and even cycles disagreed, so the app takes twice its period, %s days.", p)
			if t != nil && t.Band != "" {
				why += fmt.Sprintf(" At that period the two eclipses are equally deep (%s and %s mag in %s, %.1f sigma apart) and half a cycle apart, so %s days fits too.",
					mag2(t.Depth1.Value), mag2(t.Depth2.Value), bandWords(t.Band), t.DepthZ, periodText(e.Period/2, e.PeriodErr))
			} else {
				why += fmt.Sprintf(" Nothing in the survey data shows the two halves differ, so %s days fits too.", periodText(e.Period/2, e.PeriodErr))
			}
			out = append(out, Flag{ID: FlagHalf, Label: labelHalf, Why: why})
		}
	case len(e.Eclipses) == 1:
		if c, ok := checkOf(r, check.Double); !ok || c.Status != check.Pass || sameDepthOnly(c) {
			why := fmt.Sprintf("Only one eclipse per cycle is seen, and the double-period check did not rule out two equal eclipses every %s days.", periodText(2*e.Period, e.PeriodErr))
			if ok && c.Status == check.Fail {
				why = "The double-period check did not pass: " + lowerFirst(c.Evidence)
			}
			out = append(out, Flag{ID: FlagTwice, Label: labelTwice, Why: why})
		}
		if c, ok := checkOf(r, check.Half); !ok || c.Status != check.Pass {
			out = append(out, Flag{ID: FlagHalf, Label: labelHalf, Why: "The half-period check did not pass: " + lowerFirst(evidence(c, ok))})
		}
	default:
		c, ok := checkOf(r, check.Half)
		switch {
		case !ok || c.Status != check.Pass:
			out = append(out, Flag{ID: FlagHalf, Label: labelHalf, Why: "The half-period check did not pass: " + lowerFirst(evidence(c, ok))})
		case strings.HasPrefix(c.Evidence, period.NoDarkCompanion):
			out = append(out, Flag{ID: FlagHalf, Label: labelHalf, Why: fmt.Sprintf(
				"The survey data show one dip every %s days; the vet takes two equal eclipses every %s days because no companion dark enough to hide its own eclipse could make a dip that deep. %s",
				periodText(e.Period/2, e.PeriodErr/2), p, c.Evidence)})
		default:
			if t := f.twin(); t != nil && t.Band != "" && t.PhaseErr > 0 && t.DepthZ < strongZ && t.PhaseZ < strongZ {
				off := math.Abs(t.Phase2-0.5) * e.Period * 24
				out = append(out, Flag{ID: FlagHalf, Label: labelHalf, Why: fmt.Sprintf(
					"The half-period check passed only weakly: the eclipse depths differ by %.1f sigma (%s and %s mag in %s) and the second eclipse sits %s from mid-cycle, %.1f sigma.",
					t.DepthZ, mag2(t.Depth1.Value), mag2(t.Depth2.Value), bandWords(t.Band), hoursText(off), t.PhaseZ)})
			}
		}
	}
	return append(out, recordFlags(f)...)
}

// recordFlags are the doubts about the survey record and the Gaia star.
func recordFlags(f Facts) []Flag {
	var out []Flag
	if s := f.Survey; s != nil && f.Deeper < len(s.Coverage) {
		deep := s.Coverage[f.Deeper]
		events := 0
		for _, c := range s.Coverage {
			events += c.Events
		}
		switch {
		case deep.Points < minDeepPoints:
			out = append(out, Flag{ID: FlagFew, Label: "Few eclipses on record", Why: fmt.Sprintf("Only %s in the deeper eclipse, from %s.", plural(deep.Points, "survey point"), plural(deep.Events, "cycle"))})
		case events < minEvents:
			out = append(out, Flag{ID: FlagFew, Label: "Few eclipses on record", Why: fmt.Sprintf("Only %s in the survey data.", plural(events, "eclipse"))})
		}
	}
	if g := f.Gaia; g != nil && g.RUWE > blendRUWE {
		out = append(out, Flag{ID: FlagBlend, Label: "Blended with a close star", Why: fmt.Sprintf("Gaia sees an unresolved pair at this spot (RUWE %s).", ruweText(g.RUWE))})
	}
	return out
}

func (f Facts) twin() *eclfacts.Twin {
	if f.Survey == nil {
		return nil
	}
	return f.Survey.Twin
}

func evidence(c check.Check, ok bool) string {
	if !ok {
		return "the vet did not run it."
	}
	return c.Evidence
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	if len(s) > 1 && s[1] >= 'A' && s[1] <= 'Z' {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func ruweText(v float64) string {
	if v >= 10 {
		return fmt.Sprintf("%.0f", v)
	}
	return fmt.Sprintf("%.2f", v)
}

func hoursText(h float64) string {
	if h < 1 {
		return fmt.Sprintf("%.0f minutes", h*60)
	}
	return fmt.Sprintf("%.1f hours", h)
}

func mag2(v float64) string { return fmt.Sprintf("%.2f", v) }

// bandWords is a depth key as the pages say it: keyZTFr is r, "ATLAS o"
// is ATLAS o, "TESS" is TESS.
func bandWords(key string) string {
	if b, ok := strings.CutPrefix(key, "ZTF "); ok {
		return b
	}
	return key
}

// periodText is a period with as many decimals as its error allows: to
// the error's first digit, at least three and at most seven.
func periodText(p, err float64) string {
	d := 5
	if err > 0 {
		d = int(math.Ceil(-math.Log10(err)))
	}
	d = min(max(d, 3), 7)
	return fmt.Sprintf("%.*f", d, p)
}
