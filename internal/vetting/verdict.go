package vetting

import (
	"fmt"
	"math"
	"strings"

	"github.com/USA-RedDragon/astro-stacker/internal/discoverysource"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/ownimage"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/period"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/surveys"
)

func find(groups []Group, id string) (check.Check, bool) {
	for _, g := range groups {
		for _, c := range g.Checks {
			if c.ID == id {
				return c, true
			}
		}
	}
	return check.Check{}, false
}

func status(groups []Group, id string) check.Status {
	c, ok := find(groups, id)
	if !ok {
		return check.NotRun
	}
	return c.Status
}

func evidence(groups []Group, id string) string {
	c, _ := find(groups, id)
	return c.Evidence
}

func suggest(sum discoverysource.Summary, flags ownimage.Flags, curve *ownimage.Curve, known Known, moving check.Check, fx fetches,
	pr *period.Result, timing *period.Timing, groups []Group, c change,
) Suggestion {
	direction := "brighter"
	if c.Fainter {
		direction = "fainter"
	}
	ownFlat := status(groups, check.OwnChange) == check.Fail
	ztfFlat := status(groups, check.ZTF) == check.Fail
	confirm := func() string {
		if ownFlat || ztfFlat {
			return High
		}
		return Medium
	}
	var ps *Suggestion
	if pr != nil && pr.Ephemeris != nil {
		p := periodic(sum, pr, timing, groups, curve, known)
		ps = &p
	}
	if ps != nil && ps.State == app.VetReal && known.Listed == "" && moving.Status != check.Fail {
		if why := ownDoubt(flags, groups); why != "" {
			ps.Why = joinSentences(ps.Why, why)
		}
		return *ps
	}
	switch {
	case known.Reported != "":
		return Suggestion{State: app.VetKnown, Say: sayAlreadyKnown, Confidence: High, Why: sentence(known.Reported)}
	case flags.RefContaminated:
		return Suggestion{State: app.VetArtifact, Say: "Artifact: reference image contaminated", Confidence: confirm(),
			Why: joinSentences(notChanged(ownFlat, c), evidence(groups, check.RefClean), fmt.Sprintf("That made this spot look %s on the new nights.", direction))}
	case flags.HotPixel:
		return Suggestion{State: app.VetArtifact, Say: "Artifact: hot pixel", Confidence: confirm(), Why: evidence(groups, check.HotPixel)}
	case flags.Trail:
		return Suggestion{State: app.VetArtifact, Say: "Artifact: satellite trail", Confidence: confirm(), Why: evidence(groups, check.Trail)}
	case flags.Edge:
		return Suggestion{State: app.VetArtifact, Say: "Artifact: saturated star or image edge", Confidence: Medium, Why: evidence(groups, check.Edge)}
	}
	if moving.Status == check.Fail {
		return Suggestion{State: app.VetKnown, Say: "Known asteroid or comet", Confidence: High, Why: moving.Evidence}
	}
	if known.Listed != "" {
		return Suggestion{State: app.VetKnown, Say: sayAlreadyKnown, Confidence: High, Why: sentence(known.Listed)}
	}
	if ps != nil && (ps.State == app.VetArtifact || ps.State == app.VetKnown) {
		return *ps
	}
	if flags.NotStar {
		return Suggestion{State: app.VetArtifact, Say: sayNotStar, Confidence: Medium, Why: evidence(groups, check.PSF)}
	}
	if ownFlat && ztfFlat {
		return Suggestion{State: app.VetArtifact, Say: "Artifact: the star did not change", Confidence: Medium,
			Why: joinSentences(evidence(groups, check.OwnChange), evidence(groups, check.ZTF))}
	}
	if ownFlat {
		return Suggestion{State: app.VetArtifact, Say: "Artifact: the star did not change", Confidence: Low, Why: evidence(groups, check.OwnChange)}
	}
	if ps != nil {
		return *ps
	}
	if pr != nil && pr.Fades.Confirms() {
		say, conf := "Looks like a new variable star, brightens now and then", Medium
		if c.Fainter {
			say = "Looks like a new variable star, fades now and then"
		}
		if status(groups, check.OwnChange) != check.Pass {
			conf = Low
		}
		return Suggestion{State: app.VetReal, Say: say, Confidence: conf,
			Why: joinSentences(evidence(groups, check.ZTF), "No catalog lists the star as variable.")}
	}
	if known.Expected != "" && !ztfFlat {
		return Suggestion{State: app.VetKnown, Say: fmt.Sprintf("Known %s, irregular changes expected", known.Expected), Confidence: Medium,
			Why: joinSentences(fmt.Sprintf("SIMBAD lists it as %s, a %s; such stars change without a fixed period.", known.ExpectedName, known.Expected), evidence(groups, check.OwnChange), evidence(groups, check.ZTF))}
	}
	pending := ""
	for _, f := range fx.all() {
		if f.State == surveys.Pending {
			pending = " Survey data still being fetched may settle it."
			break
		}
	}
	if ztfFlat && pr != nil && pr.Fades.FieldProblem() {
		return Suggestion{State: app.VetUnsure, Say: "Not sure: nearby stars change with it in ZTF", Confidence: Low,
			Why: joinSentences(evidence(groups, check.ZTF), "Its survey changes come from the sky or the camera, so only your own frames speak for it.") + pending}
	}
	if ztfFlat {
		own := "Your own measurements show a change, so it may be new or brief."
		if status(groups, check.OwnChange) != check.Pass {
			own = "Your own frames did not measure the change, so only the detection says it changed."
		}
		return Suggestion{State: app.VetUnsure, Say: "Not sure: no survey saw it change", Confidence: Low,
			Why: joinSentences(evidence(groups, check.ZTF), own) + pending}
	}
	return Suggestion{State: app.VetUnsure, Say: sayNotSure, Confidence: Low, Why: "The checks found nothing against it, and nothing that confirms it." + pending}
}

// ownDoubt names what the own image checks found at the spot of a star
// whose survey period stands on its own.
func ownDoubt(flags ownimage.Flags, groups []Group) string {
	var what string
	switch {
	case flags.RefContaminated:
		what = "a contaminated reference image"
	case flags.HotPixel:
		what = "a hot pixel"
	case flags.Trail:
		what = "a satellite trail"
	case flags.Edge:
		what = "a saturated star or the image edge"
	case flags.NotStar:
		what = "a spot not shaped like a star"
	case status(groups, check.OwnChange) == check.Fail:
		what = "no clear change"
	default:
		return ""
	}
	return fmt.Sprintf("Your own frames show %s there, so the change you caught may not be an eclipse; the survey data stand on their own.", what)
}

func notChanged(ownFlat bool, c change) string {
	if !ownFlat {
		return ""
	}
	if c.Fainter {
		return "The star did not fade."
	}
	return "The star did not brighten."
}

func joinSentences(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, " ")
}

// periodic is the verdict on a survey period. Only the tests for false
// periods make it real: the cycle survives taking out the star's slow
// changes and is not an echo of the day, month or year (or TESS or
// changes within a night show it), and nearby steady stars of the same
// brightness do not show it. A period that drifts from season to season
// stays unsure. The own image checks do not decide it: confirmed
// binaries fail them as often as false periods do.
func periodic(sum discoverysource.Summary, pr *period.Result, timing *period.Timing, groups []Group, curve *ownimage.Curve, known Known) Suggestion {
	e := pr.Ephemeris
	ps := shortPeriod(e.Period)
	if pr.Robust == nil {
		return Suggestion{State: app.VetUnsure, Say: sayNotSure, Confidence: Low, Why: fmt.Sprintf("Survey data show a %s-day period, but it could not be tested against false periods.", ps)}
	}
	if s := periodDoubt(e, pr.Robust, groups, known); s != nil {
		return *s
	}
	p := periodText(e.Period)
	surveysSeen := surveyWords(pr)
	if e.Kind != period.KindEclipsing && status(groups, check.ZTF) != check.Pass && status(groups, check.ATLAS) != check.Pass {
		return Suggestion{State: app.VetUnsure, Say: fmt.Sprintf("Not sure: the %s-day cycle is too faint in the survey data", ps), Confidence: Low,
			Why: joinSentences(evidence(groups, check.ZTF), "A cycle no survey's own fit shows is not a new variable star yet.")}
	}
	if e.Kind != period.KindEclipsing {
		return Suggestion{State: app.VetReal, Say: fmt.Sprintf("Looks like a new variable star, repeats every %s days", ps), Confidence: Medium,
			Why: joinSentences(fmt.Sprintf("%s show it varying every %s days, and no catalog lists the star as variable.", surveysSeen, p),
				evidence(groups, check.Echo), evidence(groups, check.Compare))}
	}
	conf := Medium
	foldOK := status(groups, check.Fold) == check.Pass
	halfOK := status(groups, check.Half) != check.Fail
	doubleOK := status(groups, check.Double) != check.Fail
	timeOK := status(groups, check.OwnTiming) == check.Pass
	tessOK := status(groups, check.TESS) == check.Pass
	if foldOK && halfOK && doubleOK && (timeOK || tessOK) {
		conf = High
	}
	if status(groups, check.OwnTiming) == check.Fail || status(groups, check.Fold) == check.Fail {
		conf = Low
	}
	which := ""
	if curve != nil && len(e.Eclipses) > 1 {
		which = dipCaught(e, sum, timing, curve)
	}
	why := joinSentences(which, fmt.Sprintf("%s show the same dips every %s days, and no catalog lists the star as variable.", surveysSeen, p))
	return Suggestion{State: app.VetReal, Say: fmt.Sprintf("Looks like a new eclipsing binary, repeats every %s days", shortPeriod(e.Period)), Confidence: conf, Why: why}
}

// periodDoubt is the verdict on a survey period that does not pass the
// tests for false periods, or nil when it passes them. Nearby steady stars
// that plainly show the same signal make it an artifact; ones that come
// close leave it unsure. Eclipses that TESS shows deep and on time pass
// the other tests whatever ZTF says, unless ZTF and ATLAS rule out the
// TESS depth at the star.
func periodDoubt(e *period.Ephemeris, r *period.Robust, groups []Group, known Known) *Suggestion {
	ecl := e.Kind == period.KindEclipsing
	what, cycle, drift, is, does := "variable star", "cycle", "drifts", "is", "does"
	if ecl {
		what, cycle, drift, is, does = "eclipsing binary", "dips", "drift", "are", "do"
	}
	ps := shortPeriod(e.Period)
	tess := ecl && r.TESS
	slowOK := r.Echo == "" && r.Stable || r.Confirmed != "" || tess
	var s Suggestion
	switch {
	case r.Comparisons > 0 && r.CompClear:
		s = Suggestion{State: app.VetArtifact, Say: fmt.Sprintf("Artifact: the %s %s no stronger than in nearby steady stars", cycle, is), Confidence: Medium,
			Why: joinSentences(evidence(groups, check.Compare), fmt.Sprintf("The %s-day period is noise, not the star.", ps))}
		if r.CompSignal > 1 {
			s.Say = fmt.Sprintf("Artifact: nearby steady stars show the same %s", cycle)
			s.Why = joinSentences(evidence(groups, check.Compare), fmt.Sprintf("The %s-day period comes from the sky or the camera, not the star.", ps))
		}
	case r.TESSOnly:
		s = Suggestion{State: app.VetArtifact, Say: "Artifact: the dips show only in TESS", Confidence: Medium,
			Why: joinSentences(fmt.Sprintf("TESS shows a %.2f mag dip every %s days, but ZTF and ATLAS measure it at %.2f mag or less at the same times.", r.TESSDepth, ps, r.GroundDepth),
				"Their small pixels split the stars TESS's 21 arcsec pixels blend, so the dips are another star's or TESS's noise, not this star's.")}
	case tess:
		return nil
	case !slowOK && known.Expected != "":
		s = Suggestion{State: app.VetKnown, Say: fmt.Sprintf("Known %s, irregular changes expected", known.Expected), Confidence: Medium,
			Why: joinSentences(fmt.Sprintf("SIMBAD lists it as %s, a %s; such stars change without a fixed period.", known.ExpectedName, known.Expected), evidence(groups, check.Echo))}
	case r.Echo != "" && r.Confirmed == "":
		s = Suggestion{State: app.VetUnsure, Say: fmt.Sprintf("Not sure: a %s-day period next to %s needs TESS", ps, r.Echo), Confidence: Low,
			Why: joinSentences(evidence(groups, check.Echo), "The star may still vary, but not on this period.")}
	case !slowOK:
		s = Suggestion{State: app.VetUnsure, Say: fmt.Sprintf("Not sure: the %s-day %s %s not hold up", ps, cycle, does), Confidence: Low,
			Why: joinSentences(evidence(groups, check.Echo), "The star may vary, but no steady period stands out.")}
	case r.Comparisons > 0 && !r.CompPass:
		s = Suggestion{State: app.VetUnsure, Say: fmt.Sprintf("Not sure: nearby steady stars come close to the %s-day %s", ps, cycle), Confidence: Low,
			Why: joinSentences(evidence(groups, check.Compare), "The star may be periodic, but its signal is not clearly above what steady stars show.")}
	case !r.Real:
		s = Suggestion{State: app.VetUnsure, Say: fmt.Sprintf("Not sure: the %s-day %s %s weak", ps, cycle, is), Confidence: Low,
			Why: joinSentences(evidence(groups, check.Echo), evidence(groups, check.Compare), "No single survey band shows it clearly enough on its own.")}
	case status(groups, check.Fold) == check.Fail:
		s = Suggestion{State: app.VetUnsure, Say: fmt.Sprintf("Not sure: periodic, but the %s %s", cycle, drift), Confidence: Medium,
			Why: joinSentences(evidence(groups, check.Fold), fmt.Sprintf("The %s-day period is real in the survey data, but its timing wanders from season to season, so it is not a strict %s.", ps, what))}
	case !ecl && status(groups, check.Half) == check.Fail:
		s = Suggestion{State: app.VetUnsure, Say: fmt.Sprintf("Not sure: the period may be half of %s days", ps), Confidence: Low, Why: evidence(groups, check.Half)}
	default:
		return nil
	}
	return &s
}

func shortPeriod(p float64) string {
	if p < 1 {
		return trimZeros(fmt.Sprintf("%.4f", p))
	}
	return trimZeros(fmt.Sprintf("%.3f", p))
}

func dipCaught(e *period.Ephemeris, sum discoverysource.Summary, timing *period.Timing, curve *ownimage.Curve) string {
	if timing != nil {
		if timing.Dip == 0 {
			return "Your night caught the deeper of its two dips."
		}
		return "Your night caught the shallower of its two dips."
	}
	night := sum.Best.Night.UTC().Format("2006-01-02")
	var sum0 float64
	n := 0
	for _, p := range curve.Points {
		if p.Night == night {
			sum0 += p.BJD
			n++
		}
	}
	if n == 0 {
		return ""
	}
	ph := e.Phase(sum0 / float64(n))
	if math.Abs(ph) < 0.05 {
		return "Your night caught the deeper of its two dips."
	}
	if math.Abs(ph-e.Eclipses[1].Phase) < 0.05 {
		return "Your night caught the shallower of its two dips."
	}
	return ""
}

func surveyWords(pr *period.Result) string {
	var names []string
	seen := map[string]bool{}
	for _, f := range pr.Fits {
		if !f.Agrees || seen[f.Survey] {
			continue
		}
		seen[f.Survey] = true
		names = append(names, f.Survey)
	}
	if len(names) == 0 {
		return "Survey data"
	}
	return joinWords(names) + " data"
}
