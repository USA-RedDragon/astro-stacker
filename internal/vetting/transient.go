package vetting

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/discoverysource"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/ownimage"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/surveys"
)

const (
	// counterpartArcsec is how near a catalogued source must be to count
	// as what was at the spot before.
	counterpartArcsec = 1.5
	// gaiaDepth and ps1Depth are how faint each catalog usually lists
	// sources, from their published completeness.
	gaiaDepth     = 21.0
	ps1Depth      = 22.0
	seenSNR       = 5.0
	faintSNR      = 3.0
	sameDays      = 1.5
	confirmDays   = 5.0
	atlasZero     = 23.9
	alertArcsec   = 2.0
	repeatDays    = 30.0
	quietAmp      = 0.5
	mjdUnix       = 40587.0
	atlasBaseDays = 30.0
	// nightMid is about the middle of a night after its date, in UTC.
	nightMid = 30 * time.Hour
)

const (
	// noLightSNR and injGap judge the reported light against the frames:
	// with no light at signal to noise 2 where the light the detection
	// reported would show 4 higher, it was not there. AT 2026acsi (source
	// 182), a real nova, measured 6.2 where its reported light gave 12.0.
	noLightSNR = 2.0
	injGap     = 4.0
	// goneSNR and goneRep judge the nights 1 to followDays days after the
	// first flagged one: under 1.5 on each, where the reported light would
	// show at 3 or more, the light was gone the next night. 43 spots of M
	// 31's bulge in 2025 were; every real nova seen at 5 or more with a
	// later night that close was still there.
	goneSNR    = 1.5
	goneRep    = 3.0
	followDays = 3
	// trendDays bounds the nights a rise or fade is told over: 66053's
	// "brightened 0.5 mag" came from a night a season later.
	trendDays = 60
	// freshNights is how many nights the light must stand at seenSNR in
	// frames the detection did not use for the user's own frames alone to
	// call it real: the 36 own-frame transients of 2025 vetted real on
	// one night of the same frames were all artifacts.
	freshNights = 2
	// lumShare and lumFloor are the least signal to noise Luminance, which
	// takes in red, green and blue together, must show beside the best of
	// them: 39478 showed -0.8 in Luminance and 3.9 in Blue. The faint night
	// before AT 2025adss was found showed 2.5 against 5.4, so a share of 0.5
	// would lose it.
	lumShare = 0.3
	lumFloor = 2.0
	// nucleusMargin is how far, in signal to noise, the spot must stand
	// above the brightest place on its ring around a galaxy's nucleus, and
	// nucleusMatch how far the nucleus may be from where the galaxy
	// catalogue puts the centre, in arcsec or half its distance.
	nucleusMargin = 3.0
	nucleusMatch  = 5.0
	// starNearArcsec, starNearG and starNearMag find a catalogued star the
	// change may be: 48806 sat 3.0" from Gaia DR3 1857982378152951424, of G
	// 14.06, with 14.7 at the spot in the reference.
	starNearArcsec = 7.0
	starNearG      = 18.0
	starNearMag    = 1.5
	// ztfImageDays and ztfImageMargin judge a ZTF image with no alert: one
	// taken within a day of the night and half a magnitude deeper than the
	// new light would have alerted on it.
	ztfImageDays   = 1.0
	ztfImageMargin = 0.5
	jdMJD          = 2400000.5
)

var errNoCatalog = errors.New("no catalog")

// catalogGalaxy names a match with a catalogued galaxy.
const catalogGalaxy = "galaxy"

const explained = "Not fetched: the checks above already explain the spot."

// isTransient says whether a source is new light where no star was: a
// nova or supernova candidate, or a brightening with no catalogued star.
func isTransient(sum discoverysource.Summary) bool {
	if sum.Source.Kind == kindMover {
		return false
	}
	switch sum.Class {
	case discoveries.ClassNova, discoveries.ClassSupernova:
		return true
	}
	return sum.Best.GaiaG == nil && sum.Best.Sign > 0
}

func mjdOf(t time.Time) float64 { return float64(t.UTC().UnixNano())/86400e9 + mjdUnix }

func nightDate(s string) time.Time {
	t, _ := time.Parse(time.DateOnly, s)
	return t
}

// reported is the new light the detection reported, as a magnitude on
// its zero point's Gaia G scale. Light the detection found but could put
// no magnitude on, too faint in its aperture, keeps only found, its signal
// to noise over subs subs, until on turns it into light on a night: 52576
// on Markarian Chain Panel 8's 2026-05-13 was found at 9.5 with no
// magnitude, and no stack ran on it.
type reported struct {
	mag   float64
	ok    bool
	subs  int
	found float64
}

// bySNR says the light stands for the detection's signal to noise, not a
// magnitude it measured.
func (r reported) bySNR() bool { return r.found > 0 }

// on is the reported light on a measured night: light known only by the
// detection's signal to noise becomes the light that shows at that signal
// to noise in the night's frames, scaled by the square root of their
// count over the subs the detection tested.
func (r reported) on(fc *ownimage.FluxCurve, n ownimage.FluxNight) reported {
	if r.ok || !r.bySNR() || r.subs <= 0 || fc == nil || !(n.Err > 0) || n.N <= 0 {
		return r
	}
	zp, ok := fc.Zero[n.Filter]
	if n.Filter == ownimage.AllFilters {
		zp, ok = ownimage.AllFiltersZero, true
	}
	resp := fc.RespOf(n.Filter)
	if !ok || !(resp > 0) {
		return r
	}
	lf := r.found * math.Sqrt(float64(n.N)/float64(r.subs)) * n.Err / resp
	r.mag, r.ok = zp-2.5*math.Log10(lf), true
	return r
}

// basis says where light known only by the detection's signal to noise
// came from, empty for a measured magnitude.
func (r reported) basis() string {
	if !r.bySNR() {
		return ""
	}
	return fmt.Sprintf(" The detection put no magnitude on it: it found it at signal to noise %.1f over the %d subs it tested, and the fake is the light that gives that in these frames.", r.found, r.subs)
}

// flux is the reported light in a night's units.
func (r reported) flux(fc *ownimage.FluxCurve, filter string) (float64, bool) {
	if !r.ok || fc == nil {
		return 0, false
	}
	zp, ok := fc.Zero[filter]
	if filter == ownimage.AllFilters {
		zp, ok = ownimage.AllFiltersZero, true
	}
	if !ok {
		return 0, false
	}
	return math.Pow(10, -0.4*(r.mag-zp)), true
}

// snr is the signal to noise the reported light would give on a night,
// less the share of it the night's master holds; none on a night whose
// master holds too much of it to judge.
func (r reported) snr(fc *ownimage.FluxCurve, n ownimage.FluxNight) (float64, bool) {
	f, ok := r.flux(fc, n.Filter)
	if !ok || !(n.Err > 0) || !n.Judgeable() {
		return 0, false
	}
	return f * n.Kept() / n.Err, true
}

// reportedLights is the new light the detection reported, by filter group
// and night, on the strongest brightening of each: light reported in H-a
// says little of what broadband frames show, and AT 2025adss's faint
// first sighting of 2025-10-15 little of its 2025-11-21 peak.
func reportedLights(sum discoverysource.Summary) map[string]reported {
	out := map[string]reported{}
	best := map[string]float64{}
	for _, m := range sum.Members {
		if m.Sign <= 0 {
			continue
		}
		k := m.Group + "\x00" + m.Night.UTC().Format(time.DateOnly)
		nl := discoveries.NewLight(m)
		if nl == nil {
			if _, measured := best[k]; !measured && m.NightMag == nil && m.SNR > out[k].found {
				out[k] = reported{found: m.SNR, subs: m.SubsTested}
			}
			continue
		}
		if s, ok := best[k]; !ok || m.SNR > s {
			best[k] = m.SNR
			out[k] = reported{mag: *nl, ok: true}
		}
	}
	return out
}

// reportedFor is the reported light of a filter's group on a night; all
// filters combined stand for the broadband group.
func reportedFor(reps map[string]reported, filter, night string) reported {
	g := discoveries.GroupOf(filter)
	if filter == ownimage.AllFilters {
		g = discoveries.GroupBroadband
	}
	return reps[g+"\x00"+night]
}

// reportedBroad is the brightest new light the detection reported in
// broadband on any night.
func reportedBroad(reps map[string]reported) reported {
	var out reported
	for k, r := range reps {
		if r.ok && strings.HasPrefix(k, discoveries.GroupBroadband+"\x00") && (!out.ok || r.mag < out.mag) {
			out = r
		}
	}
	return out
}

// transientFacts is what the transient checks read.
type transientFacts struct {
	flux     *ownimage.FluxCurve
	flagged  []time.Time
	groups   []string
	gaia     []counterpart
	near     []counterpart
	gaiaErr  error
	refMag   *float64
	ps1Objs  []PS1Object
	ps1Err   error
	atlas    surveys.Fetch
	rows     []surveys.ATLASRow
	ztf      []ZTFObject
	ztfErr   error
	ztfAsked bool
	ztfNote  string
	images   []surveys.ZTFImage
	imgErr   error
	imgAsked bool
	matches  []discoveries.Match
	class    string
	reps     map[string]reported
	// stack is the stack to confirm, and stackV its verdict.
	stack  check.Check
	stackV ownimage.StackVerdict
}

type counterpart struct {
	g   float64
	sep float64
}

// peak is the flagged night where the spot's own light stood out most,
// a night of thinSubs subs or more when there is one.
func peak(fc *ownimage.FluxCurve) (ownimage.FluxNight, bool) {
	return peakOf(fc, nil)
}

// peakOf is peak among the nights keep takes, all when keep is nil.
func peakOf(fc *ownimage.FluxCurve, keep func(ownimage.FluxNight) bool) (ownimage.FluxNight, bool) {
	var best ownimage.FluxNight
	found := false
	for _, n := range fc.Nights {
		if !n.Flagged || (keep != nil && !keep(n)) {
			continue
		}
		better := !found || (best.Thin && !n.Thin) || (best.Thin == n.Thin && n.SNR() > best.SNR())
		if better {
			best, found = n, true
		}
	}
	return best, found
}

func fluxCheck(fc *ownimage.FluxCurve, reps map[string]reported) check.Check {
	if fc == nil || fc.Comps < 3 {
		return check.Skipped(check.TransFlux, "Not measured yet. Too few steady stars of like brightness are near enough to compare with.")
	}
	p, ok := peak(fc)
	if !ok {
		return check.Skipped(check.TransFlux, "Not measured yet. None of your frames from the flagged nights could be measured at the spot.")
	}
	basis := fmt.Sprintf(" Measured with the star shape over %d frames against %d steady nearby stars, whose Gaia magnitudes set the scale.", p.N, fc.Comps)
	if p.Local > 0 {
		basis += " The noise is the spread of the same measurement at places around the spot."
	}
	z := p.SNR()
	if !p.Judgeable() && z < seenSNR {
		return check.Skipped(check.TransFlux, fmt.Sprintf("Not judged. On %s the spot shows at signal to noise %.1f in %s. ", p.Night, z, p.Filter)+sharedWords(p)+basis)
	}
	rep := reportedFor(reps, p.Filter, p.Night)
	inj, injOK := rep.snr(fc, p)
	switch {
	case z >= seenSNR && p.Mag > 0:
		return check.New(check.TransFlux, check.Pass, fmt.Sprintf("On %s the spot had new light of magnitude %.1f in %s, at signal to noise %.0f.", p.Night, p.Mag, p.Filter, z)+basis)
	case injOK && z < noLightSNR && inj >= injGap:
		return check.New(check.TransFlux, check.Fail, fmt.Sprintf("On %s your frames show no new light at the spot in %s (signal to noise %.1f), where the light the detection reported, magnitude %.1f, would show at %.0f.",
			p.Night, p.Filter, z, rep.mag, inj)+basis)
	case !injOK && z < faintSNR:
		lim := ""
		if p.Limit > 0 {
			lim = fmt.Sprintf(" Anything brighter than magnitude %.1f would have shown.", p.Limit)
		}
		return check.New(check.TransFlux, check.Fail, fmt.Sprintf("On %s your frames show no new light at the spot in %s (signal to noise %.1f).%s", p.Night, p.Filter, z, lim)+basis)
	case z < faintSNR && inj >= injGap:
		return check.Skipped(check.TransFlux, fmt.Sprintf("Not clear. On %s the spot had at most faint light in %s (signal to noise %.1f), where the light the detection reported would show at %.0f.", p.Night, p.Filter, z, inj)+basis)
	case z < faintSNR:
		return check.Skipped(check.TransFlux, fmt.Sprintf("Not clear. On %s the spot had no clear light in %s (signal to noise %.1f), but the light the detection reported, magnitude %.1f, is too faint for your frames to rule out (it would show at %.1f).",
			p.Night, p.Filter, z, rep.mag, inj)+basis)
	}
	return check.Skipped(check.TransFlux, fmt.Sprintf("Not clear. On %s the spot had faint new light in %s, at signal to noise %.1f only.", p.Night, p.Filter, z)+basis)
}

// inFilter is the curve's nights in one filter, oldest first.
func inFilter(fc *ownimage.FluxCurve, filter string) []ownimage.FluxNight {
	var ns []ownimage.FluxNight
	for _, n := range fc.Nights {
		if n.Filter == filter {
			ns = append(ns, n)
		}
	}
	sort.Slice(ns, func(i, j int) bool { return ns[i].Night < ns[j].Night })
	return ns
}

func firstFlagged(ns []ownimage.FluxNight) string {
	for _, n := range ns {
		if n.Flagged {
			return n.Night
		}
	}
	return ""
}

func daysBetween(a, b string) float64 { return nightDate(b).Sub(nightDate(a)).Hours() / 24 }

// nightsCheckTransient follows the new light across the nights around
// the flagged ones, in the filter the peak was measured in. A night of
// fewer than three subs is shown but neither shows the light rising nor
// confirms it.
func nightsCheckTransient(fc *ownimage.FluxCurve) check.Check {
	if fc == nil {
		return check.Skipped(check.TransNights, "Not measured yet. The spot's own light could not be measured.")
	}
	p, ok := peak(fc)
	if !ok || p.SNR() < seenSNR {
		return check.Skipped(check.TransNights, "Not measured. There is no clear new light to follow.")
	}
	ns := inFilter(fc, p.Filter)
	first := firstFlagged(ns)
	var before, earlier, after []ownimage.FluxNight
	for _, n := range ns {
		switch {
		case n.Night < first && !n.Thin && n.SNR() >= seenSNR && n.Flux >= quietAmp*p.Flux:
			earlier = append(earlier, n)
		case n.Night < first && !n.Thin:
			before = append(before, n)
		case n.Night > first:
			after = append(after, n)
		}
	}
	if len(earlier) >= 2 {
		return check.New(check.TransNights, check.Fail, fmt.Sprintf("The spot was already this bright on %d earlier nights, first on %s, so the light is not new.", len(earlier), earlier[0].Night))
	}
	level := func(n ownimage.FluxNight) string {
		thin := ""
		if n.Thin {
			thin = fmt.Sprintf(" from %d frames only", n.N)
		}
		switch {
		case n.Mag > 0:
			return fmt.Sprintf("%.1f on %s%s", n.Mag, n.Night, thin)
		case n.Limit > 0:
			return fmt.Sprintf("nothing on %s (fainter than %.1f)%s", n.Night, n.Limit, thin)
		}
		return "nothing on " + n.Night + thin
	}
	var parts []string
	var listed []ownimage.FluxNight
	if len(before) > 0 {
		parts = append(parts, level(before[len(before)-1]))
	}
	seen := 0
	for _, n := range ns {
		if n.Night == first {
			parts = append(parts, level(n))
			listed = append(listed, n)
			if n.Mag > 0 && !n.Thin {
				seen++
			}
		}
	}
	for _, n := range after {
		if len(parts) >= 6 {
			break
		}
		parts = append(parts, level(n))
		listed = append(listed, n)
		if n.Mag > 0 && !n.Thin {
			seen++
		}
	}
	ev := fmt.Sprintf("In %s: %s.", p.Filter, strings.Join(parts, ", "))
	if seen < 2 && len(before) == 0 {
		return check.Skipped(check.TransNights, "Seen on one night only, with no earlier night to show it rising. "+ev)
	}
	return check.New(check.TransNights, check.Pass, ev+trend(listed, first))
}

// trend tells how the light changed over the nights listed, within
// trendDays of the first, leaving out thin ones.
func trend(ns []ownimage.FluxNight, first string) string {
	var mags []float64
	for _, n := range ns {
		if n.Night >= first && n.Mag > 0 && !n.Thin && daysBetween(first, n.Night) <= trendDays {
			mags = append(mags, n.Mag)
		}
	}
	if len(mags) < 2 {
		return ""
	}
	d := mags[len(mags)-1] - mags[0]
	switch {
	case d > 0.2:
		return fmt.Sprintf(" It faded %.1f mag from its first night to its last.", d)
	case d < -0.2:
		return fmt.Sprintf(" It brightened %.1f mag from its first night to its last.", -d)
	}
	return " It stayed about as bright."
}

// nextCheck looks for the light on the nights 1 to followDays days after
// the night it was claimed brightest on, each with three subs or more, in
// that night's filter. Earlier nights are not judged: a nova caught on
// the rise is rightly absent before.
func nextCheck(fc *ownimage.FluxCurve, reps map[string]reported) check.Check {
	if fc == nil {
		return check.Skipped(check.TransNext, "Not measured yet. The spot's own light could not be measured.")
	}
	p, ok := peak(fc)
	if !ok {
		return check.Skipped(check.TransNext, "Not measured yet. None of your frames from the flagged nights could be measured at the spot.")
	}
	ns := inFilter(fc, p.Filter)
	first := p.Night
	rep := reportedFor(reps, p.Filter, p.Night)
	var looked []ownimage.FluxNight
	for _, n := range ns {
		if d := daysBetween(first, n.Night); d >= 1 && d <= followDays && !n.Thin {
			looked = append(looked, n)
		}
	}
	if len(looked) == 0 {
		return check.Skipped(check.TransNext, fmt.Sprintf("No night 1 to %d days after %s has three frames or more in %s.", followDays, first, p.Filter))
	}
	expect := func(n ownimage.FluxNight) (float64, bool) {
		if z, ok := rep.snr(fc, n); ok {
			return z, true
		}
		if p.SNR() >= seenSNR && n.Err > 0 {
			return p.Flux / n.Err, true
		}
		return 0, false
	}
	gone := true
	var words []string
	for _, n := range looked {
		e, ok := expect(n)
		if !ok || n.SNR() >= goneSNR || e < goneRep {
			gone = false
		}
		words = append(words, fmt.Sprintf("%s at signal to noise %.1f", n.Night, n.SNR()))
		if ok {
			words[len(words)-1] += fmt.Sprintf(" where the reported light would show at %.0f", e)
		}
	}
	ev := fmt.Sprintf("In %s, after %s: %s.", p.Filter, first, joinWords(words))
	if gone {
		return check.New(check.TransNext, check.Fail, "The light was gone the next nights. "+ev)
	}
	for _, n := range looked {
		if n.SNR() >= faintSNR {
			return check.New(check.TransNext, check.Pass, "The light was still there. "+ev)
		}
	}
	return check.Skipped(check.TransNext, "Not clear. "+ev)
}

// freshCheck counts the nights the light stood at seenSNR in frames the
// detection did not use, which test it independently.
func freshCheck(fc *ownimage.FluxCurve) check.Check {
	if fc == nil {
		return check.Skipped(check.TransFresh, "Not measured yet. The spot's own light could not be measured.")
	}
	best := map[string]float64{}
	for _, n := range fc.Nights {
		if z := n.FreshSNR(); z >= seenSNR && z > best[n.Night] {
			best[n.Night] = z
		}
	}
	nights := make([]string, 0, len(best))
	for k := range best {
		nights = append(nights, k)
	}
	sort.Strings(nights)
	var words []string
	for _, k := range nights {
		words = append(words, fmt.Sprintf("%s at %.0f", k, best[k]))
	}
	if len(nights) >= freshNights {
		return check.New(check.TransFresh, check.Pass, fmt.Sprintf("Frames the detection did not use show the light on %d nights: %s.", len(nights), joinWords(words)))
	}
	if len(nights) == 1 {
		return check.Skipped(check.TransFresh, fmt.Sprintf("Frames the detection did not use show the light on one night only, %s; your own frames alone need two such nights.", words[0]))
	}
	return check.Skipped(check.TransFresh, "No frames the detection did not use show the light clearly; your own frames alone need two such nights.")
}

// colorCheck asks Luminance, which takes in red, green and blue
// together, to show the light the colour frames show on a flagged night.
func colorCheck(fc *ownimage.FluxCurve) check.Check {
	if fc == nil {
		return check.Skipped(check.TransColor, "Not measured yet. The spot's own light could not be measured.")
	}
	type night struct {
		l, best   float64
		bestF     string
		hasL, rgb bool
	}
	by := map[string]*night{}
	var order []string
	for _, n := range fc.Nights {
		if !n.Flagged || n.Thin {
			continue
		}
		g := by[n.Night]
		if g == nil {
			g = &night{best: math.Inf(-1)}
			by[n.Night] = g
			order = append(order, n.Night)
		}
		switch n.Filter {
		case "Luminance":
			g.l, g.hasL = n.SNR(), true
		case "Red", "Green", "Blue":
			if z := n.SNR(); z > g.best {
				g.best, g.bestF, g.rgb = z, n.Filter, true
			}
		}
	}
	sort.Strings(order)
	judged := ""
	for _, k := range order {
		g := by[k]
		if !g.hasL || !g.rgb || g.best < faintSNR {
			continue
		}
		need := math.Max(lumFloor, lumShare*g.best)
		if g.l < need {
			return check.New(check.TransColor, check.Fail, fmt.Sprintf("On %s the light shows in %s at signal to noise %.1f but only %.1f in Luminance, which takes in red, green and blue together and should show at least %.1f.",
				k, g.bestF, g.best, g.l, need))
		}
		judged = fmt.Sprintf("On %s Luminance shows it at signal to noise %.1f beside %.1f in %s.", k, g.l, g.best, g.bestF)
	}
	if judged == "" {
		return check.Skipped(check.TransColor, "Not judged: no flagged night has Luminance frames beside colour frames that show the light.")
	}
	return check.New(check.TransColor, check.Pass, judged)
}

// broadCheck asks a supernova candidate found only in narrowband frames
// for light in the broadband frames of its flagged nights: a supernova
// shines in every band, while 54004 by NGC 4313 stood at H-a 7.6 and
// 0.2, 0.8 and 4.6 in red, green and blue.
func broadCheck(fc *ownimage.FluxCurve, groups []string) check.Check {
	for _, g := range groups {
		if g == discoveries.GroupBroadband {
			return check.New(check.TransBroad, check.Pass, "It was found in broadband frames.")
		}
	}
	if fc == nil {
		return check.Skipped(check.TransBroad, "Not measured yet. The spot's own light could not be measured.")
	}
	type acc struct{ sw, swf float64 }
	by := map[string]*acc{}
	for _, n := range fc.Nights {
		zp, ok := fc.Zero[n.Filter]
		if !n.Flagged || !ok || !(n.Err > 0) || discoveries.GroupOf(n.Filter) != discoveries.GroupBroadband {
			continue
		}
		k := math.Pow(10, 0.4*(ownimage.AllFiltersZero-zp))
		e := n.Err * k
		a := by[n.Night]
		if a == nil {
			a = &acc{}
			by[n.Night] = a
		}
		a.sw += 1 / (e * e)
		a.swf += n.Flux * k / (e * e)
	}
	if len(by) == 0 {
		return check.Skipped(check.TransBroad, "Found in narrowband frames only, and no broadband frames were taken on its nights.")
	}
	bestN, best := "", math.Inf(-1)
	for k, a := range by {
		if z := a.swf / math.Sqrt(a.sw); z > best {
			bestN, best = k, z
		}
	}
	switch {
	case best >= faintSNR:
		return check.New(check.TransBroad, check.Pass, fmt.Sprintf("Its broadband frames of %s show it at signal to noise %.1f.", bestN, best))
	case best < noLightSNR:
		return check.New(check.TransBroad, check.Fail, fmt.Sprintf("Found in narrowband frames only: its broadband frames of %s show nothing at the spot (signal to noise %.1f), where a supernova shines too.", bestN, best))
	}
	return check.Skipped(check.TransBroad, fmt.Sprintf("Not clear: its broadband frames of %s show it at signal to noise %.1f only.", bestN, best))
}

// wingCheck fits a bright star next to the spot with each frame's own
// star shape and asks whether light remains at the spot beyond its level
// on far nights.
func wingCheck(fc *ownimage.FluxCurve) check.Check {
	if fc == nil {
		return check.Skipped(check.TransWing, "Not measured yet. The spot's own light could not be measured.")
	}
	w := fc.Wing
	if w == nil {
		return check.New(check.TransWing, check.Pass, "No catalogued star of G 16.5 or brighter lies within 10 px.")
	}
	where := fmt.Sprintf("a star of G %.1f %.1f px away", w.G, w.SepPx)
	if !w.Measured {
		return check.Skipped(check.TransWing, fmt.Sprintf("Not measured: %s, and its wing could not be fitted on enough nights to compare.", sentenceStart(where)))
	}
	ev := fmt.Sprintf("Fitting %s with each frame's own star shape, the spot on %s in %s stood %.1f times its error above its level on %d nights 120 days or more away.",
		where, w.Night, w.Filter, w.Sigma, w.Far)
	if w.Sigma < ownimage.WingSigma {
		return check.New(check.TransWing, check.Fail, ev+" The star's wing explains the light.")
	}
	return check.New(check.TransWing, check.Pass, ev)
}

func sentenceStart(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// nucleusCheck compares a supernova candidate by a galaxy's nucleus with
// the places on the same circle around the nucleus, where a poor match
// of the nucleus between night and reference leaves light too.
func nucleusCheck(fc *ownimage.FluxCurve, matches []discoveries.Match) check.Check {
	galSep := -1.0
	for _, m := range matches {
		if m.Catalog == catalogGalaxy {
			galSep = m.SepArcsec
			break
		}
	}
	if fc == nil || fc.Ring == nil || !(fc.Scale > 0) {
		return check.New(check.TransNucleus, check.Pass, "No galaxy nucleus lies within 15 px of the spot.")
	}
	if len(fc.Ring.Nights) == 0 {
		return check.Skipped(check.TransNucleus, "Not measured: the places around the galaxy's nucleus could not be measured on the flagged nights.")
	}
	sep := fc.Ring.SepPx * fc.Scale
	if galSep < 0 || math.Abs(sep-galSep) > math.Max(nucleusMatch, 0.5*galSep) {
		return check.New(check.TransNucleus, check.Pass, fmt.Sprintf("The brightest place within 15 px, %.0f arcsec away, is not the catalogued galaxy's centre.", sep))
	}
	best := fc.Ring.Nights[0]
	for _, n := range fc.Ring.Nights[1:] {
		if n.Spot > best.Spot {
			best = n
		}
	}
	ev := fmt.Sprintf("On %s in %s the spot, %.0f arcsec from the nucleus, stood at signal to noise %.1f, and places on the same circle around the nucleus up to %.1f.", best.Night, best.Filter, sep, best.Spot, best.Max)
	if best.Spot < best.Max+nucleusMargin {
		return check.New(check.TransNucleus, check.Fail, ev+" The spot does not stand out of what a poor match of the nucleus leaves on that circle.")
	}
	return check.New(check.TransNucleus, check.Pass, ev)
}

// clearCheck asks that the spot be clear of a bright star's halo and
// spikes and of the covered area's edge, where light comes and goes with
// the sky and the frames' own error is too small.
func clearCheck(fc *ownimage.FluxCurve) check.Check {
	if fc == nil {
		return check.Skipped(check.TransClear, "Not measured yet. The spot's own light could not be measured.")
	}
	if h := fc.Halo; h != nil {
		return check.New(check.TransClear, check.Fail, fmt.Sprintf("A star of magnitude %.1f lies %.1f arcmin away, inside the %.1f arcmin its halo and spikes reach.", h.V, h.SepArcsec/60, h.Reach/60))
	}
	if fc.EdgeOK && fc.EdgePx < 150 {
		return check.New(check.TransClear, check.Fail, fmt.Sprintf("The spot is %.0f px from the edge of the covered area, where the sky's level steps and bands.", fc.EdgePx))
	}
	if !fc.EdgeOK {
		return check.Skipped(check.TransClear, "No bright star's halo reaches the spot, but how far it is from the covered area's edge could not be measured.")
	}
	return check.New(check.TransClear, check.Pass, "No bright star's halo reaches the spot, and it is 150 px or more inside the covered area.")
}

// ztfImagesCheck asks whether ZTF imaged the spot within a day of a
// flagged night, deep enough to have alerted on the new light in
// broadband, with ALeRCE listing nothing there at all: 14 of the 36
// own-frame transients of 2025 had such images. In a galaxy it is not
// judged: ZTF imaged AT 2025xtl, AT 2025ahwi and AT 2025abws, real M 31
// novae, to 20.6 to 21.2 near their discovery and ALeRCE lists none of
// them.
func ztfImagesCheck(fx transientFacts, alerted bool) check.Check {
	inGalaxy := false
	for _, m := range fx.matches {
		inGalaxy = inGalaxy || m.Catalog == catalogGalaxy
	}
	switch {
	case alerted:
		return check.New(check.TransZTFImages, check.Pass, "ZTF alerted on it.")
	case inGalaxy:
		return check.Skipped(check.TransZTFImages, "Not judged in a galaxy, where ZTF's alerts miss many novae.")
	case !fx.imgAsked:
		return check.Skipped(check.TransZTFImages, "Not run.")
	case fx.imgErr != nil:
		return check.Skipped(check.TransZTFImages, "Not run: IRSA's list of ZTF images could not be read.")
	case !fx.ztfAsked || fx.ztfErr != nil:
		return check.Skipped(check.TransZTFImages, "Not run: ALeRCE, which collects ZTF's alerts, could not be asked.")
	case len(fx.ztf) > 0:
		return check.Skipped(check.TransZTFImages, fmt.Sprintf("Not judged: ALeRCE lists ZTF object %s here.", fx.ztf[0].OID))
	}
	light, what := broadLight(fx.flux), "the new light in your broadband frames"
	if rep := reportedBroad(fx.reps); light == 0 && rep.ok {
		light, what = rep.mag, "the new light the detection reported in broadband"
	}
	if light == 0 {
		return check.Skipped(check.TransZTFImages, "Not judged: no new light was measured in broadband to compare with.")
	}
	var deep *surveys.ZTFImage
	near := 0
	for i := range fx.images {
		im := &fx.images[i]
		ok := false
		for _, f := range fx.flagged {
			if math.Abs(im.JD-jdMJD-mjdOf(f.Add(nightMid))) <= ztfImageDays {
				ok = true
			}
		}
		if !ok {
			continue
		}
		near++
		if im.MagLimit >= light+ztfImageMargin && (deep == nil || im.MagLimit > deep.MagLimit) {
			deep = im
		}
	}
	if near == 0 {
		return check.Skipped(check.TransZTFImages, "ZTF has no public images of the spot within a day of your nights.")
	}
	if deep == nil {
		return check.Skipped(check.TransZTFImages, fmt.Sprintf("ZTF imaged the spot within a day of your nights, but not half a magnitude deeper than %s, %.1f.", what, light))
	}
	return check.New(check.TransZTFImages, check.Fail, fmt.Sprintf("ZTF imaged the spot on %s to magnitude %.1f in %s, deeper than %s (%.1f), and ALeRCE lists no alert within %.0f arcsec.",
		mjdDate(deep.JD-jdMJD), deep.MagLimit, deep.Band, what, light, alertArcsec))
}

// broadLight is the brightest new light a flagged night showed in a
// broadband filter, 0 when none stood clear.
func broadLight(fc *ownimage.FluxCurve) float64 {
	if fc == nil {
		return 0
	}
	best := 0.0
	for _, n := range fc.Nights {
		if !n.Flagged || n.Thin || n.Mag <= 0 || n.SNR() < seenSNR || n.Filter == ownimage.AllFilters || discoveries.GroupOf(n.Filter) != discoveries.GroupBroadband {
			continue
		}
		if best == 0 || n.Mag < best {
			best = n.Mag
		}
	}
	return best
}

func beforeCheck(fx transientFacts) check.Check {
	if fx.gaiaErr != nil && fx.ps1Err != nil {
		return check.Skipped(check.TransBefore, "Not run: neither Gaia nor Pan-STARRS could be read.")
	}
	if fx.refMag != nil {
		for _, c := range fx.near {
			if c.sep > counterpartArcsec && math.Abs(c.g-*fx.refMag) < starNearMag {
				return check.New(check.TransBefore, check.Fail, fmt.Sprintf("Gaia lists a star of G %.1f %.1f arcsec away, and the reference already held light of %.1f at the spot, so the change is most likely that star's.",
					c.g, c.sep, *fx.refMag))
			}
		}
	}
	peakMag := 0.0
	if fx.flux != nil {
		if p, ok := peak(fx.flux); ok && p.SNR() >= seenSNR {
			peakMag = p.Mag
		}
	}
	var found []string
	brightest := math.Inf(1)
	for _, c := range fx.gaia {
		found = append(found, fmt.Sprintf("Gaia lists a star of G %.1f %.1f arcsec away", c.g, c.sep))
		brightest = math.Min(brightest, c.g)
	}
	for _, o := range fx.ps1Objs {
		m := o.R
		band := "r"
		if m == 0 {
			m, band = o.G, "g"
		}
		if m == 0 {
			continue
		}
		found = append(found, fmt.Sprintf("Pan-STARRS lists a source of %s %.1f %.1f arcsec away", band, m, o.SepArcsec))
		brightest = math.Min(brightest, m)
	}
	var looked []string
	if fx.gaiaErr == nil {
		looked = append(looked, fmt.Sprintf("Gaia (to about G %.0f)", gaiaDepth))
	}
	if fx.ps1Err == nil {
		looked = append(looked, fmt.Sprintf("Pan-STARRS (to about magnitude %.0f)", ps1Depth))
	}
	if len(found) == 0 {
		ev := fmt.Sprintf("Nothing within %.1f arcsec in %s.", counterpartArcsec, joinWords(looked))
		if peakMag > 0 {
			depth := gaiaDepth
			if fx.ps1Err == nil {
				depth = ps1Depth
			}
			ev += fmt.Sprintf(" At magnitude %.1f it rose at least %.1f mag from below what they list.", peakMag, depth-peakMag)
		}
		return check.New(check.TransBefore, check.Pass, ev)
	}
	ev := sentence(joinWords(found))
	if peakMag == 0 {
		return check.New(check.TransBefore, check.Pass, ev)
	}
	amp := brightest - peakMag
	if amp < quietAmp {
		return check.New(check.TransBefore, check.Fail, ev+fmt.Sprintf(" That is about as bright as the new light (%.1f), so the change may be that source, not something new.", peakMag))
	}
	return check.New(check.TransBefore, check.Pass, ev+fmt.Sprintf(" The new light, %.1f, is %.1f mag brighter than that.", peakMag, amp))
}

// atlasNight is ATLAS's difference light on one date near a flagged
// night, against its own level away from them.
type atlasNight struct {
	mjd   float64
	band  string
	flux  float64
	err   float64
	count int
}

func atlasLevels(rows []surveys.ATLASRow, flagged []time.Time) []atlasNight {
	return atlasNights(rows, flagged, func(near func(float64, float64) bool, mjd float64) bool { return near(mjd, confirmDays) })
}

// atlasAway is ATLAS's difference light on each date atlasBaseDays or
// more from the flagged nights, measured as atlasLevels measures the
// dates near them.
func atlasAway(rows []surveys.ATLASRow, flagged []time.Time) []atlasNight {
	return atlasNights(rows, flagged, func(near func(float64, float64) bool, mjd float64) bool { return !near(mjd, atlasBaseDays) })
}

func atlasNights(rows []surveys.ATLASRow, flagged []time.Time, keep func(near func(float64, float64) bool, mjd float64) bool) []atlasNight {
	base := map[string][]float64{}
	near := func(mjd, days float64) bool {
		for _, f := range flagged {
			if math.Abs(mjd-mjdOf(f.Add(nightMid))) <= days {
				return true
			}
		}
		return false
	}
	levelOf := func(r surveys.ATLASRow) string {
		span := 0
		for _, m := range surveys.ATLASTemplateMJD() {
			if r.MJD >= m {
				span++
			}
		}
		cam := r.Obs
		if len(cam) > 3 {
			cam = cam[:3]
		}
		return fmt.Sprintf("%s %s %d", r.Filter, cam, span)
	}
	for _, r := range rows {
		if !near(r.MJD, atlasBaseDays) {
			base[levelOf(r)] = append(base[levelOf(r)], r.UJy)
		}
	}
	type key struct {
		day  int
		band string
	}
	groups := map[key]*atlasNight{}
	var keys []key
	for _, r := range rows {
		if !keep(near, r.MJD) {
			continue
		}
		b := median(base[levelOf(r)])
		k := key{int(math.Floor(r.MJD)), r.Filter}
		g, ok := groups[k]
		if !ok {
			g = &atlasNight{band: r.Filter}
			groups[k] = g
			keys = append(keys, k)
		}
		w := 1 / (r.DUJy * r.DUJy)
		g.flux += (r.UJy - b) * w
		g.err += w
		g.mjd += r.MJD
		g.count++
	}
	out := make([]atlasNight, 0, len(keys))
	for _, k := range keys {
		g := groups[k]
		g.flux /= g.err
		g.err = 1 / math.Sqrt(g.err)
		g.mjd /= float64(g.count)
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].mjd < out[j].mjd })
	return out
}

// atlasCommonShare is the share of ATLAS's nights away from the flagged
// ones that may reach a detection's signal to noise for it to stand out.
// At 54893 in the Crab Nebula, beside a G 14.1 star, 131 of 928 ATLAS
// nights reached signal to noise 8, the level of the one night that
// "saw" new light that the user's own frames did not show.
const atlasCommonShare = 0.02

// atlasCommon is how many of ATLAS's nights away from the flagged ones
// reach snr in the same direction, and of how many, when more than
// atlasCommonShare of them do; 0 otherwise.
func atlasCommon(rows []surveys.ATLASRow, flagged []time.Time, snr float64) (int, int) {
	away := atlasAway(rows, flagged)
	n := 0
	for _, a := range away {
		if a.flux/a.err >= snr {
			n++
		}
	}
	if len(away) == 0 || float64(n) <= atlasCommonShare*float64(len(away)) {
		return 0, len(away)
	}
	return n, len(away)
}

// atlasMagSlack is how much brighter than the reported new light an ATLAS
// detection may be and still confirm it: forced photometry beside a bright
// star measures the star.
const atlasMagSlack = 1.0

// brightestReported is the brightest new light the detection reported.
func brightestReported(reps map[string]reported) (float64, bool) {
	best, ok := 0.0, false
	for _, r := range reps {
		if r.ok && r.mag > 0 && (!ok || r.mag < best) {
			best, ok = r.mag, true
		}
	}
	return best, ok
}

func mjdDate(mjd float64) string {
	return lc.MJDTime(mjd).Format(time.DateOnly)
}

func atlasCheckTransient(fx transientFacts) check.Check {
	switch fx.atlas.State {
	case surveys.Pending:
		return check.Skipped(check.TransATLAS, "Not fetched yet. The request is queued at ATLAS and a later run fills this in.")
	case surveys.Failed, "":
		n := strings.TrimSpace(fx.atlas.Note)
		switch {
		case n == "":
			return check.Skipped(check.TransATLAS, "Not fetched.")
		case strings.HasPrefix(n, "Not "):
			return check.Skipped(check.TransATLAS, n)
		}
		return check.Skipped(check.TransATLAS, sentence("Not fetched: "+n))
	case surveys.Uncovered:
		return check.Skipped(check.TransATLAS, "ATLAS never looked at this spot.")
	case surveys.Done:
	}
	ns := atlasLevels(fx.rows, fx.flagged)
	if len(ns) == 0 {
		return check.Skipped(check.TransATLAS, fmt.Sprintf("ATLAS has no good frames within %.0f days of the flagged nights.", confirmDays))
	}
	best := ns[0]
	for _, n := range ns {
		if n.flux/n.err > best.flux/best.err {
			best = n
		}
	}
	if best.flux/best.err >= seenSNR && best.flux > 0 {
		mag := atlasZero - 2.5*math.Log10(best.flux)
		if rep, ok := brightestReported(fx.reps); ok && mag < rep-atlasMagSlack {
			return check.Skipped(check.TransATLAS, fmt.Sprintf("ATLAS measured magnitude %.1f on %s, far brighter than the %.1f your frames reported; that is a nearby star's light, not the new light.",
				mag, mjdDate(best.mjd), rep))
		}
		if n, of := atlasCommon(fx.rows, fx.flagged, best.flux/best.err); n > 0 {
			return check.Skipped(check.TransATLAS, fmt.Sprintf("ATLAS measured magnitude %.1f on %s (signal to noise %.0f), but %d of %d other ATLAS nights at this spot reach that too, so it does not stand out from what ATLAS always measures here.",
				mag, mjdDate(best.mjd), best.flux/best.err, n, of))
		}
		return check.New(check.TransATLAS, check.Pass, fmt.Sprintf("ATLAS saw it on %s at magnitude %.1f in its %s band (signal to noise %.0f, from %d frames).",
			mjdDate(best.mjd), mag, atlasBand(best.band), best.flux/best.err, best.count))
	}
	peakFlux := 0.0
	peakNight := ""
	if fx.flux != nil {
		if p, ok := peak(fx.flux); ok && p.Mag > 0 && p.SNR() >= seenSNR {
			peakFlux, peakNight = math.Pow(10, -0.4*(p.Mag-atlasZero)), p.Night
		}
	}
	if peakFlux > 0 {
		for _, n := range ns {
			if math.Abs(n.mjd-mjdOf(nightDate(peakNight).Add(nightMid))) > sameDays {
				continue
			}
			if peakFlux/n.err >= 2*seenSNR && n.flux/n.err < 2 {
				return check.New(check.TransATLAS, check.Fail, fmt.Sprintf("ATLAS looked on %s, within a day of your night, and saw nothing: the new light at its brightness would show at signal to noise %.0f, but ATLAS measured %.0f ± %.0f microjansky (signal to noise %.1f).",
					mjdDate(n.mjd), peakFlux/n.err, n.flux, n.err, n.flux/n.err))
			}
		}
	}
	if peakFlux == 0 {
		if c, ok := atlasMissedReported(fx, ns); ok {
			return c
		}
	}
	return check.Skipped(check.TransATLAS, fmt.Sprintf("ATLAS has %d nights of frames within %.0f days, but none shows it clearly; the best is signal to noise %.1f on %s.", len(ns), confirmDays, best.flux/best.err, mjdDate(best.mjd)))
}

// atlasMissedReported fails new light too faint in the user's own frames
// to stand on its own when ATLAS looked within a day of a flagged night,
// in frames deep enough to show the broadband light the detection reported
// there at twice seenSNR, and saw nothing: 52576 on 2026-05-13 was found
// at 9.5 while ATLAS's five nights around it reached at best 0.8. A star
// brighter than starNearG within starNearArcsec or a catalogued galaxy at
// the spot leaves it open, as their light enters ATLAS's forced
// photometry too.
func atlasMissedReported(fx transientFacts, ns []atlasNight) (check.Check, bool) {
	if len(fx.near) > 0 || inGalaxy(fx.matches) {
		return check.Check{}, false
	}
	nights := make([]string, 0, len(fx.reps))
	for k := range fx.reps {
		if g, night, ok := strings.Cut(k, "\x00"); ok && g == discoveries.GroupBroadband {
			nights = append(nights, night)
		}
	}
	sort.Strings(nights)
	for _, night := range nights {
		rep := fx.reps[discoveries.GroupBroadband+"\x00"+night]
		if n, ok := broadNightOn(fx.flux, night); ok {
			rep = rep.on(fx.flux, n)
		}
		if !rep.ok || rep.mag <= 0 {
			continue
		}
		lf := math.Pow(10, -0.4*(rep.mag-atlasZero))
		at := mjdOf(nightDate(night).Add(nightMid))
		for _, n := range ns {
			if math.Abs(n.mjd-at) > sameDays || !(n.err > 0) {
				continue
			}
			if lf/n.err >= 2*seenSNR && n.flux/n.err < 2 {
				return check.New(check.TransATLAS, check.Fail, fmt.Sprintf("ATLAS looked on %s, within a day of your night of %s, and saw nothing: the light the detection reported, magnitude %.1f, would show at signal to noise %.0f, but ATLAS measured %.0f ± %.0f microjansky (signal to noise %.1f).",
					mjdDate(n.mjd), night, rep.mag, lf/n.err, n.flux, n.err, n.flux/n.err)+rep.basis()), true
			}
		}
	}
	return check.Check{}, false
}

// broadNightOn is the flagged broadband or all-filters night of a date
// with the most light against its error.
func broadNightOn(fc *ownimage.FluxCurve, night string) (ownimage.FluxNight, bool) {
	var best ownimage.FluxNight
	found := false
	if fc == nil {
		return best, false
	}
	for _, n := range fc.Nights {
		if n.Night != night || !n.Flagged || n.Thin || !(n.Err > 0) {
			continue
		}
		if n.Filter != ownimage.AllFilters && discoveries.GroupOf(n.Filter) != discoveries.GroupBroadband {
			continue
		}
		if !found || n.SNR() > best.SNR() {
			best, found = n, true
		}
	}
	return best, found
}

// inGalaxy says the spot lies in a catalogued galaxy.
func inGalaxy(ms []discoveries.Match) bool {
	for _, m := range ms {
		if m.Catalog == catalogGalaxy {
			return true
		}
	}
	return false
}

func atlasBand(b string) string {
	switch b {
	case "c":
		return "cyan"
	case "o":
		return "orange"
	}
	return b
}

func ztfCheckTransient(fx transientFacts) check.Check {
	if !fx.ztfAsked {
		if fx.ztfNote != "" {
			return check.Skipped(check.TransZTF, fx.ztfNote)
		}
		return check.Skipped(check.TransZTF, "Not run.")
	}
	if fx.ztfErr != nil {
		return check.Skipped(check.TransZTF, "Not run: ALeRCE, which collects ZTF's alerts, could not be reached.")
	}
	if len(fx.ztf) == 0 {
		return check.Skipped(check.TransZTF, fmt.Sprintf("ALeRCE lists no ZTF alert within %.0f arcsec. ZTF alerts only reach about magnitude 20.5.", alertArcsec))
	}
	first := math.Inf(1)
	for _, f := range fx.flagged {
		first = math.Min(first, mjdOf(f.Add(nightMid)))
	}
	var near, early []string
	var nearDet ZTFDetection
	for _, o := range fx.ztf {
		for _, d := range o.Detections {
			if !d.Positive {
				continue
			}
			for _, f := range fx.flagged {
				if math.Abs(d.MJD-mjdOf(f.Add(nightMid))) <= confirmDays {
					near = append(near, o.OID)
					if nearDet.MJD == 0 || d.Mag < nearDet.Mag {
						nearDet = d
					}
					break
				}
			}
			if d.MJD < first-repeatDays {
				early = append(early, fmt.Sprintf("%s on %s", o.OID, mjdDate(d.MJD)))
			}
		}
	}
	if len(near) > 0 {
		return check.New(check.TransZTF, check.Pass, fmt.Sprintf("ZTF alerted on it: %s, magnitude %.1f in %s on %s, %.1f arcsec from your spot.",
			near[0], nearDet.Mag, nearDet.Band, mjdDate(nearDet.MJD), fx.ztf[0].SepArcsec))
	}
	if len(early) > 0 {
		sort.Strings(early)
		return check.New(check.TransZTF, check.Fail, fmt.Sprintf("ZTF alerted on this spot long before, first %s, so it brightens again and again.", early[0]))
	}
	return check.Skipped(check.TransZTF, fmt.Sprintf("ALeRCE lists ZTF object %s here, but with no alert within %.0f days of your nights.", fx.ztf[0].OID, confirmDays))
}

func hostCheck(fx transientFacts) check.Check {
	for _, m := range fx.matches {
		if m.Catalog != catalogGalaxy {
			continue
		}
		kind := ""
		if m.Type != "" {
			kind = ", " + lowerFirst(m.Type)
		}
		return check.New(check.TransHost, check.Pass, fmt.Sprintf("It lies within %s%s, %.0f arcsec from its centre.", m.Name, kind, m.SepArcsec))
	}
	if fx.class == discoveries.ClassSupernova {
		return check.Skipped(check.TransHost, "No catalogued galaxy at the spot.")
	}
	return check.Skipped(check.TransHost, "No catalogued galaxy at the spot; new light outside a galaxy is more likely a nova or flare in our own galaxy.")
}

func matchesOf(c app.DiscoveryCandidate) []discoveries.Match {
	var ms []discoveries.Match
	_ = json.Unmarshal([]byte(c.Matches), &ms)
	return ms
}

// maxImageNights bounds the ZTF image lists asked for, one per flagged
// night.
const maxImageNights = 5

// transientFacts gathers what the transient checks read.
func (s *Service) transientFacts(ctx context.Context, f surveys.Field, sum discoverysource.Summary, own *ownimage.Result, remote bool) transientFacts {
	ra, dec := sum.Source.RA, sum.Source.Dec
	fx := transientFacts{class: sum.Class, matches: matchesOf(sum.Best), reps: reportedLights(sum), refMag: sum.Best.RefMag}
	if own != nil {
		fx.flux = own.Flux
	}
	fx.stack, fx.stackV = transientStack(fx.flux, fx.reps)
	seen := map[string]bool{}
	groups := map[string]bool{}
	for _, m := range sum.Members {
		k := m.Night.UTC().Format(time.DateOnly)
		if !seen[k] {
			seen[k] = true
			fx.flagged = append(fx.flagged, m.Night.UTC())
		}
		if !groups[m.Group] {
			groups[m.Group] = true
			fx.groups = append(fx.groups, m.Group)
		}
	}
	if s.Gaia != nil {
		stars, err := s.Gaia.Cone(ctx, ra, dec, starNearArcsec/3600, nil)
		fx.gaiaErr = err
		for _, g := range stars {
			gra, gdec := g.At(epochYear(s.now()))
			if math.IsNaN(float64(g.G)) {
				continue
			}
			sep := arcsec(ra, dec, gra, gdec)
			if sep <= counterpartArcsec {
				fx.gaia = append(fx.gaia, counterpart{g: float64(g.G), sep: sep})
			}
			if sep <= starNearArcsec && float64(g.G) < starNearG {
				fx.near = append(fx.near, counterpart{g: float64(g.G), sep: sep})
			}
		}
	} else {
		fx.gaiaErr = errNoCatalog
	}
	if !remote {
		fx.ps1Err, fx.atlas, fx.ztfNote = errNoCatalog, surveys.Fetch{Survey: lc.ATLAS, Note: explained}, explained
		return fx
	}
	if s.PS1 != nil {
		objs, err := s.PS1(ctx, ra, dec, counterpartArcsec)
		fx.ps1Err = err
		for _, o := range objs {
			if o.SepArcsec <= counterpartArcsec {
				fx.ps1Objs = append(fx.ps1Objs, o)
			}
		}
	} else {
		fx.ps1Err = errNoCatalog
	}
	if s.Surveys != nil {
		fx.atlas, fx.rows = s.Surveys.ATLASRows(ctx, f, surveys.Target{RA: ra, Dec: dec})
		fx.imgAsked = true
		for i, n := range fx.flagged {
			if i >= maxImageNights {
				break
			}
			mid := mjdOf(n.Add(nightMid)) + jdMJD
			imgs, err := s.Surveys.ZTFImages(ctx, ra, dec, mid-ztfImageDays, mid+ztfImageDays)
			if err != nil {
				fx.imgErr = err
				break
			}
			fx.images = append(fx.images, imgs...)
		}
	}
	if s.Alerts != nil {
		fx.ztfAsked = true
		fx.ztf, fx.ztfErr = s.Alerts(ctx, ra, dec, alertArcsec)
	}
	return fx
}

// transientSuggest turns the transient checks into a suggestion, after
// the image checks and catalogs have had their say. ATLAS or ZTF seeing
// it makes it real even with its shape unmeasured, unless the user's own
// frames stacked at the spot do not show it. The user's own frames alone
// never make it real unless the light stood out on two nights in frames
// the detection did not use, its stack confirmed it, its shape was
// measured, the asteroid check ran, and nothing else left it open: the 36
// own-frame transients vetted real in 2025, each on one night of the
// frames that found it, were all artifacts, and 67045 on Sombrero
// Galaxy's 2025-12-29 was diffuse light near a frame corner with its
// shape unmeasured. Frames stacked at the spot that refute it make it an
// artifact before light there earlier or a catalogued star beside it
// leave it open, unless a survey saw it: 46939 (Orion, 2026-10-05), 2.2
// px from a G 15.0 star, stood 0.5 times its error above its far nights
// with the star fitted, and stayed "a catalogued source sits there".
func transientSuggest(groups []Group, fx transientFacts) Suggestion {
	ev := func(id string) string { return evidence(groups, id) }
	st := func(id string) check.Status { return status(groups, id) }
	what := "a new transient"
	switch fx.class {
	case discoveries.ClassNova:
		what = "a nova"
	case discoveries.ClassSupernova:
		what = "a supernova"
	}
	pendingNote := ""
	if fx.atlas.State == surveys.Pending {
		pendingNote = " ATLAS data still being fetched may settle it."
	}
	switch {
	case st(check.TransATLAS) == check.Fail:
		return Suggestion{State: app.VetArtifact, Say: "Artifact: ATLAS saw nothing on the same date", Confidence: Medium, Why: ev(check.TransATLAS)}
	case st(check.TransFlux) == check.Fail:
		return Suggestion{State: app.VetArtifact, Say: "Artifact: no new light in your own frames", Confidence: Medium, Why: ev(check.TransFlux)}
	case fx.stackV == ownimage.StackRefuted && st(check.TransATLAS) != check.Pass && st(check.TransZTF) != check.Pass:
		return ownFramesSuggest(groups, what, pendingNote, fx.stackV, fx.flux != nil && fx.flux.Halo != nil)
	case st(check.TransNights) == check.Fail:
		return Suggestion{State: app.VetUnsure, Say: "Not sure: the light was there before", Confidence: Low, Why: ev(check.TransNights)}
	case st(check.TransBefore) == check.Fail:
		return Suggestion{State: app.VetUnsure, Say: "Not sure: a catalogued source sits there at that brightness", Confidence: Low, Why: ev(check.TransBefore)}
	case st(check.TransZTF) == check.Fail:
		return Suggestion{State: app.VetUnsure, Say: "Not sure: ZTF saw this spot brighten before", Confidence: Low, Why: ev(check.TransZTF)}
	}
	if st(check.TransATLAS) == check.Pass || st(check.TransZTF) == check.Pass {
		if fx.stackV == ownimage.StackRefuted && st(check.TransWing) == check.Fail {
			return Suggestion{State: app.VetArtifact, Say: "Artifact: a bright star's wing, and your frames stacked at the spot do not show it", Confidence: Medium,
				Why: joinSentences(ev(check.TransWing), ev(check.TransStack), "A survey's light this near a star can be the star's own.")}
		}
		if fx.stackV == ownimage.StackRefuted && st(check.TransClear) == check.Fail {
			return Suggestion{State: app.VetArtifact, Say: "Artifact: a bright star's halo, and your frames stacked at the spot do not show it", Confidence: Medium,
				Why: joinSentences(ev(check.TransClear), ev(check.TransStack), "A survey's light inside a bright star's halo can be the halo's own.")}
		}
		if fx.stackV == ownimage.StackRefuted {
			return Suggestion{State: app.VetUnsure, Say: "Not sure: a survey saw it, but your frames stacked at the spot do not show it", Confidence: Low,
				Why: joinSentences(ev(check.TransStack), ev(check.TransATLAS), ev(check.TransZTF))}
		}
		conf := Medium
		if st(check.TransFlux) == check.Pass {
			conf = High
		}
		var why []string
		for _, id := range []string{check.TransATLAS, check.TransZTF, check.TransFlux, check.TransBefore} {
			if st(id) == check.Pass {
				why = append(why, ev(id))
			}
		}
		return Suggestion{State: app.VetReal, Say: "Looks like " + what + ", seen by a survey too", Confidence: conf, Why: joinSentences(why...)}
	}
	return ownFramesSuggest(groups, what, pendingNote, fx.stackV, fx.flux != nil && fx.flux.Halo != nil)
}

// ownFramesSuggest is the suggestion for new light no survey has seen,
// from the user's own frames alone. Inside a bright star's halo a stack
// that does not confirm the light makes it an artifact: the halo's own
// light comes and goes with the sky, and on Horsehead Nebula's 2025-11-16,
// 11-17 and 11-21 the spots 3' to 9' from Alnilam stacked to 40% to 53%
// of their fakes while the detection found dozens more around it. A wing
// fit that leaves no light beyond the star's own says so before an
// unmeasured shape does: 54893 (Crab Nebula, 2026-02-05) stood 1.3 times
// its error above its far nights beside a G 14.1 star, where the fake of
// its reported light shows at 3.2, too faint to refute it.
func ownFramesSuggest(groups []Group, what, pendingNote string, stack ownimage.StackVerdict, halo bool) Suggestion {
	ev := func(id string) string { return evidence(groups, id) }
	st := func(id string) check.Status { return status(groups, id) }
	has := func(id string) bool {
		_, ok := find(groups, id)
		return ok
	}
	switch {
	case stack == ownimage.StackRefuted && st(check.TransWing) == check.Fail:
		return Suggestion{State: app.VetArtifact, Say: "Artifact: a bright star's wing", Confidence: Medium, Why: joinSentences(ev(check.TransWing), ev(check.TransStack))}
	case stack == ownimage.StackRefuted:
		return Suggestion{State: app.VetArtifact, Say: "Artifact: not there when your frames are stacked at the spot", Confidence: Medium, Why: ev(check.TransStack)}
	case st(check.TransNext) == check.Fail:
		return Suggestion{State: app.VetArtifact, Say: "Artifact: gone the next night", Confidence: Medium, Why: ev(check.TransNext)}
	case st(check.TransColor) == check.Fail:
		return Suggestion{State: app.VetArtifact, Say: "Artifact: light in colour frames but not in Luminance", Confidence: Medium, Why: ev(check.TransColor)}
	case st(check.TransNucleus) == check.Fail:
		return Suggestion{State: app.VetArtifact, Say: "Artifact: galaxy nucleus residual", Confidence: Medium, Why: ev(check.TransNucleus)}
	case st(check.TransZTFImages) == check.Fail:
		return Suggestion{State: app.VetArtifact, Say: "Artifact: ZTF looked the same night and saw nothing", Confidence: Medium, Why: ev(check.TransZTFImages)}
	case stack == ownimage.StackDoubtful && halo:
		return Suggestion{State: app.VetArtifact, Say: "Artifact: a bright star's halo, and your frames stacked at the spot do not confirm it", Confidence: Medium,
			Why: joinSentences(ev(check.TransClear), ev(check.TransStack), "New light this faint inside a bright star's halo is the halo's own.")}
	case stack == ownimage.StackDoubtful:
		return Suggestion{State: app.VetUnsure, Say: "Not sure: your frames stacked at the spot do not confirm it", Confidence: Low, Why: ev(check.TransStack) + pendingNote}
	case st(check.TransWing) == check.Fail:
		return Suggestion{State: app.VetUnsure, Say: "Not sure: a bright star's wing", Confidence: Low, Why: joinSentences(ev(check.TransWing), ev(check.TransStack)) + pendingNote}
	case st(check.PSF) != check.Pass:
		why := joinSentences(ev(check.PSF), "Diffuse light, not a star, can show as new light at a spot until its shape is measured.")
		return Suggestion{State: app.VetUnsure, Say: "Not sure: the shape of the new light was not measured", Confidence: Low, Why: why + pendingNote}
	case st(check.TransWing) != check.Pass && has(check.TransWing):
		return Suggestion{State: app.VetUnsure, Say: "Not sure: a bright star's wing", Confidence: Low, Why: ev(check.TransWing) + pendingNote}
	case st(check.TransClear) == check.Fail:
		return Suggestion{State: app.VetUnsure, Say: "Not sure: by a bright star or the edge", Confidence: Low,
			Why: joinSentences(ev(check.TransClear), "There your own frames alone cannot tell new light from the sky's.") + pendingNote}
	case st(check.TransBroad) != check.Pass && has(check.TransBroad):
		return Suggestion{State: app.VetUnsure, Say: "Not sure: seen in narrowband only", Confidence: Low, Why: ev(check.TransBroad) + pendingNote}
	case st(check.Stays) != check.Pass:
		return Suggestion{State: app.VetUnsure, Say: "Not sure: not checked against known asteroids", Confidence: Low,
			Why: joinSentences(ev(check.Stays), "A known asteroid passing on one night looks just like this.") + pendingNote}
	}
	if stack == ownimage.StackConfirmed && st(check.TransFlux) == check.Pass && st(check.TransNights) == check.Pass && st(check.TransBefore) == check.Pass && st(check.TransFresh) == check.Pass {
		return Suggestion{State: app.VetReal, Say: "Looks like " + what + " in your own frames", Confidence: Medium,
			Why: joinSentences(ev(check.TransFlux), ev(check.TransStack), ev(check.TransFresh), ev(check.TransNights), ev(check.TransBefore), "No survey has confirmed it yet.")}
	}
	var missing []string
	for _, id := range []string{check.TransFlux, check.TransStack, check.TransFresh, check.TransNights, check.TransBefore, check.TransATLAS, check.TransZTF} {
		if st(id) != check.Pass {
			missing = append(missing, lowerFirst(check.Names()[id]))
		}
	}
	why := "Nothing found against it, and nothing that confirms it."
	if len(missing) > 0 {
		why = fmt.Sprintf("Nothing found against it, but these did not settle it: %s.", joinWords(missing))
	}
	return Suggestion{State: app.VetUnsure, Say: sayNotSure, Confidence: Low, Why: why + pendingNote}
}

// transientChart is the spot's own magnitude in each frame of its best
// night.
func transientChart(fc *ownimage.FluxCurve) Chart {
	ch := Chart{ID: chartNight, Title: "Your night", XUnit: unitHours, YUnit: unitMag, YLabel: labelMag, Sub: "The new light at the spot in each of your frames.", Empty: notMeasured}
	if fc == nil {
		return ch
	}
	p, ok := peak(fc)
	if !ok {
		return ch
	}
	zp, okz := fc.Zero[p.Filter]
	if !okz {
		return ch
	}
	var pts []ownimage.FluxPoint
	for _, q := range fc.Points {
		if q.Night == p.Night && q.Filter == p.Filter && q.Flux-fc.Base[p.Filter] > 0 {
			pts = append(pts, q)
		}
	}
	if len(pts) == 0 {
		return ch
	}
	ch.Title, ch.Empty = "Your night, "+dateWords(nightDate(p.Night)), ""
	mid := pts[len(pts)/2].BJD
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, q := range pts {
		f := q.Flux - fc.Base[p.Filter]
		m := zp - 2.5*math.Log10(f)
		e := math.Min(1.0857*q.Err/f, 1)
		ch.Points = append(ch.Points, ChartPoint{X: round((q.BJD-mid)*24, 4), Y: round(m, 4), Err: round(e, 4)})
		lo, hi = math.Min(lo, m-e), math.Max(hi, m+e)
	}
	x0, x1 := ch.Points[0].X, ch.Points[len(ch.Points)-1].X
	pad := math.Max(0.1, (x1-x0)*0.08)
	ch.X0, ch.X1 = x0-pad, x1+pad
	ch.Y0, ch.Y1 = padRange(lo, hi)
	ch.XTicks, ch.YTicks = ticks(ch.X0, ch.X1, 5), ticks(ch.Y0, ch.Y1, 5)
	ch.XLabel = "Hours from the middle of your frames"
	ch.Sub = fmt.Sprintf("%d %s frames, against %d steady nearby stars. Lower is fainter.", len(pts), p.Filter, fc.Comps)
	ch.Legend = []LegendItem{{Style: styleDot, Label: "Your frames"}}
	return ch
}

func transientGroup(fx transientFacts) []check.Check {
	stack := fx.stack
	if stack.ID == "" {
		stack, _ = transientStack(fx.flux, fx.reps)
	}
	cs := []check.Check{fluxCheck(fx.flux, fx.reps), stack, nightsCheckTransient(fx.flux), nextCheck(fx.flux, fx.reps), freshCheck(fx.flux), colorCheck(fx.flux)}
	if fx.class == discoveries.ClassSupernova {
		cs = append(cs, broadCheck(fx.flux, fx.groups))
	}
	cs = append(cs, wingCheck(fx.flux))
	if fx.class == discoveries.ClassSupernova {
		cs = append(cs, nucleusCheck(fx.flux, fx.matches))
	}
	zc := ztfCheckTransient(fx)
	cs = append(cs, clearCheck(fx.flux), beforeCheck(fx), atlasCheckTransient(fx), zc, ztfImagesCheck(fx, zc.Status == check.Pass), hostCheck(fx))
	return cs
}

func (s *Service) vetTransient(ctx context.Context, f surveys.Field, sum discoverysource.Summary, rep *Report, own *ownimage.Result, ownChecks []check.Check,
	known Known, moving check.Check, flags ownimage.Flags,
) {
	names := groupNames()
	g := func(id string, cs []check.Check) Group { return Group{ID: id, Name: names[id], Checks: cs} }
	image := g(check.GroupImage, pick(ownChecks, []string{check.HotPixel, check.RefClean, check.Trail, check.PSF, check.Edge}, "Not measured yet."))
	catalogs := g(check.GroupCatalog, pick(known.Checks, []string{check.VSX, check.GaiaVari, check.SurveyVar, check.TNS, check.SIMBAD}, "Not run yet."))
	stays := g(check.GroupMoving, []check.Check{moving})
	settled := imageSuggest([]Group{image, catalogs, stays}, flags, known, moving)
	fx := s.transientFacts(ctx, f, sum, own, settled == nil)
	rep.Groups = []Group{image, g(check.GroupTransient, transientGroup(fx)), catalogs, stays}
	if fx.atlas.State == surveys.Pending {
		rep.Pending = append(rep.Pending, lc.ATLAS)
	}
	rep.Charts = []Chart{transientChart(fx.flux)}
	if settled != nil {
		rep.Suggestion = *settled
		return
	}
	rep.Suggestion = transientSuggest(rep.Groups, fx)
}

// imageSuggest is the suggestion the image checks, catalogs and known
// moving objects settle on their own, nil when they leave it open.
func imageSuggest(groups []Group, flags ownimage.Flags, known Known, moving check.Check) *Suggestion {
	switch {
	case known.Reported != "":
		return &Suggestion{State: app.VetKnown, Say: sayAlreadyKnown, Confidence: High, Why: sentence(known.Reported)}
	case flags.RefContaminated:
		return &Suggestion{State: app.VetArtifact, Say: "Artifact: reference image contaminated", Confidence: Medium, Why: evidence(groups, check.RefClean)}
	case flags.HotPixel:
		return &Suggestion{State: app.VetArtifact, Say: "Artifact: hot pixel", Confidence: Medium, Why: evidence(groups, check.HotPixel)}
	case flags.Trail:
		return &Suggestion{State: app.VetArtifact, Say: "Artifact: satellite trail or one-frame flash", Confidence: Medium, Why: evidence(groups, check.Trail)}
	case flags.Edge:
		return &Suggestion{State: app.VetArtifact, Say: "Artifact: saturated star or image edge", Confidence: Medium, Why: evidence(groups, check.Edge)}
	case moving.Status == check.Fail:
		return &Suggestion{State: app.VetKnown, Say: "Known asteroid or comet", Confidence: High, Why: moving.Evidence}
	case known.Listed != "":
		return &Suggestion{State: app.VetKnown, Say: sayAlreadyKnown, Confidence: High, Why: sentence(known.Listed)}
	case flags.NotStar:
		return &Suggestion{State: app.VetArtifact, Say: sayNotStar, Confidence: Medium, Why: evidence(groups, check.PSF)}
	}
	return nil
}
