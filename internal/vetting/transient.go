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

var errNoCatalog = errors.New("no catalog")

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

// transientFacts is what the transient checks read.
type transientFacts struct {
	flux     *ownimage.FluxCurve
	flagged  []time.Time
	gaia     []counterpart
	gaiaErr  error
	ps1Objs  []PS1Object
	ps1Err   error
	atlas    surveys.Fetch
	rows     []surveys.ATLASRow
	ztf      []ZTFObject
	ztfErr   error
	ztfAsked bool
	ztfNote  string
	matches  []discoveries.Match
	class    string
}

type counterpart struct {
	g   float64
	sep float64
}

// peak is the flagged night where the spot's own light stood out most.
func peak(fc *ownimage.FluxCurve) (ownimage.FluxNight, bool) {
	var best ownimage.FluxNight
	found := false
	for _, n := range fc.Nights {
		if n.Flagged && (!found || n.SNR() > best.SNR()) {
			best, found = n, true
		}
	}
	return best, found
}

func fluxCheck(fc *ownimage.FluxCurve) check.Check {
	if fc == nil || fc.Comps < 3 {
		return check.Skipped(check.TransFlux, "Not measured yet. Too few steady stars of like brightness are near enough to compare with.")
	}
	p, ok := peak(fc)
	if !ok {
		return check.Skipped(check.TransFlux, "Not measured yet. None of your frames from the flagged nights could be measured at the spot.")
	}
	basis := fmt.Sprintf(" Measured with the star shape over %d frames against %d steady nearby stars, whose Gaia magnitudes set the scale.", p.N, fc.Comps)
	switch {
	case p.SNR() >= seenSNR && p.Mag > 0:
		return check.New(check.TransFlux, check.Pass, fmt.Sprintf("On %s the spot had new light of magnitude %.1f in %s, at signal to noise %.0f.", p.Night, p.Mag, p.Filter, p.SNR())+basis)
	case p.SNR() < faintSNR:
		lim := ""
		if p.Limit > 0 {
			lim = fmt.Sprintf(" Anything brighter than magnitude %.1f would have shown.", p.Limit)
		}
		return check.New(check.TransFlux, check.Fail, fmt.Sprintf("On %s your frames show no new light at the spot in %s (signal to noise %.1f).%s", p.Night, p.Filter, p.SNR(), lim)+basis)
	}
	return check.Skipped(check.TransFlux, fmt.Sprintf("Not clear. On %s the spot had faint new light in %s, at signal to noise %.1f only.", p.Night, p.Filter, p.SNR())+basis)
}

// nightsCheckTransient follows the new light across the nights around
// the flagged ones, in the filter the peak was measured in.
func nightsCheckTransient(fc *ownimage.FluxCurve) check.Check {
	if fc == nil {
		return check.Skipped(check.TransNights, "Not measured yet. The spot's own light could not be measured.")
	}
	p, ok := peak(fc)
	if !ok || p.SNR() < seenSNR {
		return check.Skipped(check.TransNights, "Not measured. There is no clear new light to follow.")
	}
	var ns []ownimage.FluxNight
	for _, n := range fc.Nights {
		if n.Filter == p.Filter {
			ns = append(ns, n)
		}
	}
	sort.Slice(ns, func(i, j int) bool { return ns[i].Night < ns[j].Night })
	first := ""
	for _, n := range ns {
		if n.Flagged {
			first = n.Night
			break
		}
	}
	var before, earlier, after []ownimage.FluxNight
	for _, n := range ns {
		switch {
		case n.Night < first && n.SNR() >= seenSNR && n.Flux >= quietAmp*p.Flux:
			earlier = append(earlier, n)
		case n.Night < first:
			before = append(before, n)
		case n.Night > first:
			after = append(after, n)
		}
	}
	if len(earlier) >= 2 {
		return check.New(check.TransNights, check.Fail, fmt.Sprintf("The spot was already this bright on %d earlier nights, first on %s, so the light is not new.", len(earlier), earlier[0].Night))
	}
	level := func(n ownimage.FluxNight) string {
		switch {
		case n.Mag > 0:
			return fmt.Sprintf("%.1f on %s", n.Mag, n.Night)
		case n.Limit > 0:
			return fmt.Sprintf("nothing on %s (fainter than %.1f)", n.Night, n.Limit)
		}
		return "nothing on " + n.Night
	}
	var parts []string
	if len(before) > 0 {
		parts = append(parts, level(before[len(before)-1]))
	}
	seen := 0
	for _, n := range ns {
		if n.Night == first {
			parts = append(parts, level(n))
			if n.Mag > 0 {
				seen++
			}
		}
	}
	for _, n := range after {
		if len(parts) >= 6 {
			break
		}
		parts = append(parts, level(n))
		if n.Mag > 0 {
			seen++
		}
	}
	ev := fmt.Sprintf("In %s: %s.", p.Filter, strings.Join(parts, ", "))
	if seen < 2 && len(before) == 0 {
		return check.Skipped(check.TransNights, "Seen on one night only, with no earlier night to show it rising. "+ev)
	}
	return check.New(check.TransNights, check.Pass, ev+trend(ns, first))
}

func trend(ns []ownimage.FluxNight, first string) string {
	var mags []float64
	for _, n := range ns {
		if n.Night >= first && n.Mag > 0 {
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

func beforeCheck(fx transientFacts) check.Check {
	if fx.gaiaErr != nil && fx.ps1Err != nil {
		return check.Skipped(check.TransBefore, "Not run: neither Gaia nor Pan-STARRS could be read.")
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
		if !near(r.MJD, confirmDays) {
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
		return check.New(check.TransATLAS, check.Pass, fmt.Sprintf("ATLAS saw it on %s at magnitude %.1f in its %s band (signal to noise %.0f, from %d frames).",
			mjdDate(best.mjd), atlasZero-2.5*math.Log10(best.flux), atlasBand(best.band), best.flux/best.err, best.count))
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
	return check.Skipped(check.TransATLAS, fmt.Sprintf("ATLAS has %d nights of frames within %.0f days, but none shows it clearly; the best is signal to noise %.1f on %s.", len(ns), confirmDays, best.flux/best.err, mjdDate(best.mjd)))
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
		return check.Skipped(check.TransZTF, fmt.Sprintf("ALeRCE lists no ZTF alert within %.0f arcsec. ZTF alerts only reach about magnitude 20.5, and ZTF may not have looked that week.", alertArcsec))
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
		if m.Catalog != "galaxy" {
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

// transientFacts gathers what the transient checks read.
func (s *Service) transientFacts(ctx context.Context, f surveys.Field, sum discoverysource.Summary, own *ownimage.Result, remote bool) transientFacts {
	ra, dec := sum.Source.RA, sum.Source.Dec
	fx := transientFacts{class: sum.Class, matches: matchesOf(sum.Best)}
	if own != nil {
		fx.flux = own.Flux
	}
	seen := map[string]bool{}
	for _, m := range sum.Members {
		k := m.Night.UTC().Format(time.DateOnly)
		if !seen[k] {
			seen[k] = true
			fx.flagged = append(fx.flagged, m.Night.UTC())
		}
	}
	if s.Gaia != nil {
		stars, err := s.Gaia.Cone(ctx, ra, dec, counterpartArcsec/3600, nil)
		fx.gaiaErr = err
		for _, g := range stars {
			gra, gdec := g.At(epochYear(s.now()))
			if math.IsNaN(float64(g.G)) {
				continue
			}
			if sep := arcsec(ra, dec, gra, gdec); sep <= counterpartArcsec {
				fx.gaia = append(fx.gaia, counterpart{g: float64(g.G), sep: sep})
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
	}
	if s.Alerts != nil {
		fx.ztfAsked = true
		fx.ztf, fx.ztfErr = s.Alerts(ctx, ra, dec, alertArcsec)
	}
	return fx
}

// transientSuggest turns the transient checks into a suggestion, after
// the image checks and catalogs have had their say.
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
	switch {
	case st(check.TransATLAS) == check.Fail:
		return Suggestion{State: app.VetArtifact, Say: "Artifact: ATLAS saw nothing on the same date", Confidence: Medium, Why: ev(check.TransATLAS)}
	case st(check.TransFlux) == check.Fail:
		return Suggestion{State: app.VetArtifact, Say: "Artifact: no new light in your own frames", Confidence: Medium, Why: ev(check.TransFlux)}
	case st(check.TransNights) == check.Fail:
		return Suggestion{State: app.VetUnsure, Say: "Not sure: the light was there before", Confidence: Low, Why: ev(check.TransNights)}
	case st(check.TransBefore) == check.Fail:
		return Suggestion{State: app.VetUnsure, Say: "Not sure: a catalogued source sits there at that brightness", Confidence: Low, Why: ev(check.TransBefore)}
	case st(check.TransZTF) == check.Fail:
		return Suggestion{State: app.VetUnsure, Say: "Not sure: ZTF saw this spot brighten before", Confidence: Low, Why: ev(check.TransZTF)}
	}
	if st(check.TransATLAS) == check.Pass || st(check.TransZTF) == check.Pass {
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
	if st(check.TransFlux) == check.Pass && st(check.TransNights) == check.Pass && st(check.TransBefore) == check.Pass {
		return Suggestion{State: app.VetReal, Say: "Looks like " + what + " in your own frames", Confidence: Medium,
			Why: joinSentences(ev(check.TransFlux), ev(check.TransNights), ev(check.TransBefore), "No survey has confirmed it yet.")}
	}
	var missing []string
	for _, id := range []string{check.TransFlux, check.TransNights, check.TransBefore, check.TransATLAS, check.TransZTF} {
		if st(id) != check.Pass {
			missing = append(missing, lowerFirst(check.Names()[id]))
		}
	}
	why := "Nothing found against it, and nothing that confirms it."
	if len(missing) > 0 {
		why = fmt.Sprintf("Nothing found against it, but these did not settle it: %s.", joinWords(missing))
	}
	if fx.atlas.State == surveys.Pending {
		why += " ATLAS data still being fetched may settle it."
	}
	return Suggestion{State: app.VetUnsure, Say: sayNotSure, Confidence: Low, Why: why}
}

// transientChart is the spot's own magnitude in each frame of its best
// night.
func transientChart(fc *ownimage.FluxCurve) Chart {
	ch := Chart{ID: "night", Title: "Your night", XUnit: unitHours, YUnit: unitMag, YLabel: labelMag, Sub: "The new light at the spot in each of your frames.", Empty: notMeasured}
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
	return []check.Check{fluxCheck(fx.flux), nightsCheckTransient(fx.flux), beforeCheck(fx), atlasCheckTransient(fx), ztfCheckTransient(fx), hostCheck(fx)}
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
	case flags.RefContaminated:
		return &Suggestion{State: app.VetArtifact, Say: "Artifact: reference image contaminated", Confidence: Medium, Why: evidence(groups, check.RefClean)}
	case flags.HotPixel:
		return &Suggestion{State: app.VetArtifact, Say: "Artifact: hot pixel", Confidence: Medium, Why: evidence(groups, check.HotPixel)}
	case flags.Trail:
		return &Suggestion{State: app.VetArtifact, Say: "Artifact: satellite trail", Confidence: Medium, Why: evidence(groups, check.Trail)}
	case flags.Edge:
		return &Suggestion{State: app.VetArtifact, Say: "Artifact: saturated star or image edge", Confidence: Medium, Why: evidence(groups, check.Edge)}
	case moving.Status == check.Fail:
		return &Suggestion{State: app.VetKnown, Say: "Known asteroid or comet", Confidence: High, Why: moving.Evidence}
	case known.Listed != "":
		return &Suggestion{State: app.VetKnown, Say: "Already known", Confidence: High, Why: sentence(known.Listed)}
	case flags.NotStar:
		return &Suggestion{State: app.VetArtifact, Say: sayNotStar, Confidence: Medium, Why: evidence(groups, check.PSF)}
	}
	return nil
}
