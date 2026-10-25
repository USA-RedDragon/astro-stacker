package ownimage

import (
	"fmt"
	"math"
	"sort"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
)

const (
	minChangeFrames = 3
	changeSigma     = 5.0
	changeShare     = 0.3
	quietShare      = 0.5
	quietErrs       = 5.0
	quietFloor      = 0.05
	runWindow       = 5
	dipScatters     = 3.0
)

func fainter(dm float64) string {
	if dm >= 0 {
		return "fainter"
	}
	return "brighter"
}

func (sp *spotPlan) reported(night, filter string) (float64, bool) {
	for _, n := range sp.spot.Nights {
		if nightKey(n.Night) == night && discoveries.GroupOf(filter) == n.Group && n.DeltaMag != nil {
			return *n.DeltaMag, true
		}
	}
	return 0, false
}

func (sp *spotPlan) meanReported() (float64, bool) {
	var v []float64
	for _, n := range sp.spot.Nights {
		if n.DeltaMag != nil {
			x := *n.DeltaMag
			if sp.mixedSigns() {
				x = math.Abs(x)
			}
			v = append(v, x)
		}
	}
	return mean(v), len(v) > 0
}

func (sp *spotPlan) mixedSigns() bool {
	for _, n := range sp.spot.Nights {
		if n.Sign != 0 && n.Sign != sp.fadeSign {
			return true
		}
	}
	return false
}

// mixedChange judges a spot flagged fainter on some nights and brighter
// on others by how far, on average, each level moved the way it was
// reported.
func mixedChange(sp *spotPlan, c *Curve, rep float64, hasRep bool) check.Check {
	moved := -float64(sp.fadeSign) * c.Change
	sig := -float64(sp.fadeSign) * c.ChangeSigma
	way := "the reported way"
	if moved < 0 {
		way = "against the reported way"
	}
	ev := fmt.Sprintf("Over the %s, some reported fainter and some brighter, the star moved %.2f mag %s on average, give or take %.2f",
		levelsWords(c), math.Abs(moved), way, changeErr(c))
	if hasRep {
		ev += fmt.Sprintf(", against %.2f mag reported", rep)
	}
	ev += fmt.Sprintf(". Single frames scatter by %.3f mag on normal nights.", c.Scatter) + mirrorNote(c)
	if sig >= changeSigma && moved >= changeShare*rep {
		return check.New(check.OwnChange, check.Pass, ev)
	}
	return check.New(check.OwnChange, check.Fail, ev)
}

func (sp *spotPlan) changePoints(c *Curve) []Point {
	var out []Point
	for _, p := range c.Points {
		if p.Night == c.ChangeNight && sp.inGroup(p.Night, p.Filter) {
			out = append(out, p)
		}
	}
	return out
}

// runningExtreme is the most extreme running median of w points, toward
// fainter when sign is -1 and brighter when 1, either way when 0.
func runningExtreme(pts []Point, sign int) float64 {
	w := min(runWindow, len(pts))
	if w%2 == 0 {
		w--
	}
	best := 0.0
	for i := 0; i+w <= len(pts); i++ {
		v := make([]float64, w)
		for j := range w {
			v[j] = pts[i+j].DMag
		}
		m := median(v)
		switch {
		case sign < 0 && m > best, sign > 0 && m < best, sign == 0 && math.Abs(m) > math.Abs(best):
			best = m
		}
	}
	return best
}

func ownLC(sp *spotPlan, c *Curve) check.Check {
	if c == nil || c.Comps < minPresent {
		return check.Skipped(check.OwnLC, "Not measured yet. Too few steady stars of like brightness and color are near enough to compare with.")
	}
	pts := sp.changePoints(c)
	if len(pts) < minChangeFrames {
		return check.Skipped(check.OwnLC, "Not measured yet. Too few frames of the night could be measured at this spot.")
	}
	ev := fmt.Sprintf("Measured against %d steady nearby stars over %d frames.", c.Comps, len(pts))
	ext := runningExtreme(pts, sp.signOf(c.ChangeNight, pts[0].Filter))
	dip := math.Abs(ext) >= dipScatters*c.Scatter
	clearChange := math.Abs(c.ChangeSigma) >= changeSigma
	switch {
	case !dip && !clearChange:
		ev += fmt.Sprintf(" That night shows no clear change against single frames scattering by %.2f mag.", c.Scatter)
	case !dip:
		ev += fmt.Sprintf(" No dip or peak within the night stands out against single frames scattering by %.2f mag.", c.Scatter)
	case ext > 0:
		ev += fmt.Sprintf(" The dip bottomed out %.2f mag deep, against single frames scattering by %.2f mag.", ext, c.Scatter)
	default:
		ev += fmt.Sprintf(" It peaked %.2f mag brighter, against single frames scattering by %.2f mag.", -ext, c.Scatter)
	}
	if dip && !clearChange {
		ev += " The night's average alone is not clear of how much the star's level wanders from night to night."
	}
	return check.New(check.OwnLC, check.Pass, ev)
}

func ownChange(sp *spotPlan, c *Curve) check.Check {
	if c == nil || c.ChangeNight == "" || len(sp.changePoints(c)) < minChangeFrames {
		return check.Skipped(check.OwnChange, "Not measured yet. The flagged night could not be measured at this spot.")
	}
	lead := "The night was"
	var filter string
	for _, p := range sp.changePoints(c) {
		filter = p.Filter
	}
	rep, hasRep := sp.reported(c.ChangeNight, filter)
	if c.ChangeLevels > 1 {
		lead = fmt.Sprintf("Over the %s the star was", levelsWords(c))
		rep, hasRep = sp.meanReported()
		if sp.mixedSigns() {
			return mixedChange(sp, c, rep, hasRep)
		}
	}
	scatter := fmt.Sprintf(" Single frames scatter by %.3f mag on normal nights.", c.Scatter) + mirrorNote(c)
	ok := c.ChangeSigma >= changeSigma
	if sp.fadeSign != 0 {
		ok = -float64(sp.fadeSign)*c.ChangeSigma >= changeSigma
	}
	if hasRep && math.Abs(c.Change) < changeShare*math.Abs(rep) {
		ok = false
	}
	if ok {
		return check.New(check.OwnChange, check.Pass, fmt.Sprintf("%s %.2f mag %s on average.%s", lead, math.Abs(c.Change), fainter(c.Change), scatter))
	}
	return check.New(check.OwnChange, check.Fail, changeFail(sp, c, lead, rep, hasRep)+scatter)
}

// levelsWords names the flagged night and filter levels the change
// averages, as independent ones when a mirror joined two of them.
func levelsWords(c *Curve) string {
	if len(c.Mirrors) > 0 {
		return fmt.Sprintf("%d independent flagged night and filter levels", c.ChangeLevels)
	}
	return fmt.Sprintf("%d flagged nights and filters", c.ChangeLevels)
}

// changeFail says why the flagged nights fail the change check: their
// level moved the other way from the reported change, by less than
// changeShare of it, or by fewer than changeSigma times its error, which
// takes in how much the star's level wanders from night to night.
func changeFail(sp *spotPlan, c *Curve, lead string, rep float64, hasRep bool) string {
	ev := fmt.Sprintf("%s %.2f mag %s on average, give or take %.2f", lead, math.Abs(c.Change), fainter(c.Change), changeErr(c))
	if hasRep {
		ev += fmt.Sprintf(", against %.2f mag %s reported", math.Abs(rep), fainter(rep))
	}
	sig := math.Abs(c.ChangeSigma)
	if sp.fadeSign != 0 {
		sig = -float64(sp.fadeSign) * c.ChangeSigma
	}
	switch {
	case sp.fadeSign != 0 && sig <= 0:
		return ev + ". That is the other way from the reported change."
	case hasRep && math.Abs(c.Change) < changeShare*math.Abs(rep):
		return ev + fmt.Sprintf(". That is less than %.0f%% of the reported change.", 100*changeShare)
	}
	return ev + fmt.Sprintf(". That is %.1f times its error, which takes in how much the star's level wanders from night to night; %.0f times is needed.", sig, changeSigma)
}

func changeErr(c *Curve) float64 {
	if c.ChangeSigma == 0 {
		return 0
	}
	return math.Abs(c.Change / c.ChangeSigma)
}

func ownQuiet(sp *spotPlan, c *Curve) check.Check {
	if c == nil {
		return check.Skipped(check.OwnQuiet, "Not measured yet. The star's own light curve could not be measured.")
	}
	var others []NightLevel
	var errs []float64
	for _, n := range c.Nights {
		if quietNight(sp, n) {
			others = append(others, n)
			errs = append(errs, n.Err)
		}
	}
	nights := map[string]bool{}
	for _, n := range others {
		nights[n.Night] = true
	}
	if len(nights) == 0 {
		return check.Skipped(check.OwnQuiet, "Not measured yet. No other night has frames enough to measure the star to 0.1 mag.")
	}
	limit := math.Max(math.Max(quietShare*math.Abs(c.Change), quietErrs*median(errs)), quietFloor)
	var loud []NightLevel
	for _, n := range others {
		if math.Abs(n.DMag) > limit && math.Abs(n.DMag) > quietErrs*n.Err {
			loud = append(loud, n)
		}
	}
	if len(loud) == 0 {
		return check.New(check.OwnQuiet, check.Pass, fmt.Sprintf("On %d other nights the star stayed within %.3f mag of normal.", len(nights), math.Abs(c.Quiet)))
	}
	sort.Slice(loud, func(a, b int) bool { return math.Abs(loud[a].DMag) > math.Abs(loud[b].DMag) })
	w := loud[0]
	ev := fmt.Sprintf("On %s the star was %.2f mag %s in %s, so it changes on other nights too.", w.Night, math.Abs(w.DMag), fainter(w.DMag), w.Filter)
	if n := countNights(loud) - 1; n > 0 {
		ev += fmt.Sprintf(" %d more of the %d other nights also stood out.", n, len(nights))
	}
	return check.New(check.OwnQuiet, check.Fail, ev)
}

func countNights(v []NightLevel) int {
	m := map[string]bool{}
	for _, n := range v {
		m[n.Night] = true
	}
	return len(m)
}
