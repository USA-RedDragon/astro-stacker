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
	switch {
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
		return Suggestion{State: app.VetKnown, Say: "Already known", Confidence: High, Why: sentence(known.Listed)}
	}
	if pr != nil && pr.Ephemeris != nil {
		return periodic(sum, pr, timing, groups, curve)
	}
	if flags.NotStar {
		return Suggestion{State: app.VetArtifact, Say: "Artifact: not shaped like a star", Confidence: Medium, Why: evidence(groups, check.PSF)}
	}
	if ownFlat && ztfFlat {
		return Suggestion{State: app.VetArtifact, Say: "Artifact: the star did not change", Confidence: Medium,
			Why: joinSentences(evidence(groups, check.OwnChange), evidence(groups, check.ZTF))}
	}
	if ownFlat {
		return Suggestion{State: app.VetArtifact, Say: "Artifact: the star did not change", Confidence: Low, Why: evidence(groups, check.OwnChange)}
	}
	pending := ""
	for _, f := range fx.all() {
		if f.State == surveys.Pending {
			pending = " Survey data still being fetched may settle it."
			break
		}
	}
	if ztfFlat {
		return Suggestion{State: app.VetUnsure, Say: "Not sure: no survey saw it change", Confidence: Low,
			Why: joinSentences(evidence(groups, check.ZTF), "Your own measurements show a change, so it may be new or brief.") + pending}
	}
	return Suggestion{State: app.VetUnsure, Say: "Not sure", Confidence: Low, Why: "The checks found nothing against it, and nothing that confirms it." + pending}
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

func periodic(sum discoverysource.Summary, pr *period.Result, timing *period.Timing, groups []Group, curve *ownimage.Curve) Suggestion {
	e := pr.Ephemeris
	p := periodText(e.Period)
	surveysSeen := surveyWords(pr)
	if e.Kind != period.KindEclipsing {
		return Suggestion{State: app.VetReal, Say: fmt.Sprintf("Looks like a new variable star, repeats every %s days", shortPeriod(e.Period)), Confidence: Medium,
			Why: fmt.Sprintf("%s show it varying every %s days, and no catalog lists the star as variable.", surveysSeen, p)}
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

func shortPeriod(p float64) string {
	if p >= 10 {
		return trimZeros(fmt.Sprintf("%.3f", p))
	}
	return trimZeros(fmt.Sprintf("%.4f", p))
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
