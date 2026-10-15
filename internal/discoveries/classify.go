package discoveries

import (
	"context"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/ztflc"
)

// Classes of what changed.
const (
	ClassKnownVariable  = "known_variable"
	ClassKnownAsteroid  = "known_asteroid"
	ClassKnownTransient = "known_transient"
	ClassNova           = "nova_candidate"
	ClassSupernova      = "supernova_candidate"
	ClassNewVariable    = "new_variable_candidate"
	ClassUnknownMover   = "unknown_mover"
	ClassLowConfidence  = "low_confidence"
)

// Classes lists every class.
func Classes() []string {
	return []string{ClassKnownVariable, ClassKnownAsteroid, ClassKnownTransient, ClassNova, ClassSupernova, ClassNewVariable, ClassUnknownMover, ClassLowConfidence}
}

// IsNew says whether a class passed every check and matched nothing known.
func IsNew(class string) bool {
	return class == ClassNova || class == ClassSupernova || class == ClassNewVariable || class == ClassUnknownMover
}

const (
	vsxMatchArcsec = 5.0
	// simbadMatchArcsec is tighter: SIMBAD lists thousands of survey
	// variables in M 31's and M 33's discs, one within 5" of a twentieth of
	// their area.
	simbadMatchArcsec  = 2.0
	bodyMatchArcsec    = 20.0
	staticBodyArcsec   = 10.0
	alertMatchArcsec   = 3.0
	galaxyMatchFactor  = 1.2
	maxAlertCandidates = 60
	// maxZTFCandidates bounds the ZTF light curve queries of one run about
	// changes held for want of a confirmation, the strongest first: IRSA
	// takes 4 to 24 s a query. A new variable is always asked.
	maxZTFCandidates = 60
)

// Match is a catalogue entry a candidate or track matched.
type Match struct {
	Catalog   string   `json:"catalog"`
	Name      string   `json:"name"`
	Type      string   `json:"type,omitempty"`
	SepArcsec float64  `json:"sep_arcsec"`
	Mag       *float64 `json:"mag,omitempty"`
	Note      string   `json:"note,omitempty"`
	// TNS is set on a match from the local TNS copy.
	TNS *TNSEntry `json:"tns,omitempty"`
}

// Galaxy is a catalogued galaxy's ellipse.
type Galaxy struct {
	Name           string
	RA, Dec        float64
	Major, Minor   float64
	PA             float64
	HasOrientation bool
	// LocalGroup marks a galaxy of the Local Group, such as M 31 or M 33:
	// a new star in it is far more likely a nova than a supernova.
	LocalGroup bool
}

// localGroupType is the type a Local Group galaxy's match carries.
const localGroupType = "Local Group galaxy"

// Inside reports whether a position is inside the galaxy's ellipse grown by
// factor, and its distance from the centre in arcseconds.
func (g Galaxy) Inside(ra, dec, factor float64) (bool, float64) {
	sep := Separation(g.RA, g.Dec, ra, dec) * 3600
	a := g.Major * 60 / 2 * factor
	b := g.Minor * 60 / 2 * factor
	if b <= 0 || !g.HasOrientation {
		return sep <= a, sep
	}
	dx := wrapDeg(ra-g.RA) * math.Cos(g.Dec*deg) * 3600
	dy := (dec - g.Dec) * 3600
	pa := g.PA * deg
	u := dx*math.Sin(pa) + dy*math.Cos(pa)
	v := dx*math.Cos(pa) - dy*math.Sin(pa)
	return (u*u)/(a*a)+(v*v)/(b*b) <= 1, sep
}

// Known is what the catalogues say about a field on a night.
type Known struct {
	Variables []Variable
	Bodies    []Body
	Epoch     time.Time
	Galaxies  []Galaxy
	// Prior are the changes found on the target's other nights, and in its
	// other filter groups on this night.
	Prior []PriorChange
	// ZTF, if set, judges a new variable's change against the star's ZTF
	// light curve.
	ZTF ZTFLookup
	// Checked names the catalogues consulted and Failed those that could not
	// be reached.
	Checked []string
	Failed  []string
}

// ZTFLookup judges a claimed change of amplitude magnitudes at a position
// against ZTF's light curve there.
type ZTFLookup func(ctx context.Context, ra, dec, amplitude float64) (ztflc.Verdict, error)

// catalogZTF names the ZTF light curve check in matches and the checks.
const catalogZTF = "ZTF"

// ztfCheck asks ZTF about a change on a catalogued star that is new, or
// held only for want of a confirmation. A star ZTF shows varying by the
// change's size confirms it: the star varies, and no catalogue lists it, so
// it is a new variable. One ZTF shows flat well below it holds no change of
// ours, and is held at low confidence.
func ztfCheck(ctx context.Context, l *Labelled, lookup ZTFLookup) (checked, failed bool) {
	c := l.Candidate
	if lookup == nil || c.GaiaG == nil || c.DeltaMag == nil || (l.Class != ClassNewVariable && !heldForConfirmation(*l)) {
		return false, false
	}
	v, err := lookup(ctx, c.RA, c.Dec, math.Abs(*c.DeltaMag))
	if err != nil {
		return false, true
	}
	switch v.Kind {
	case ztflc.Variable:
		l.Matches = append(l.Matches, Match{Catalog: catalogZTF, Name: "ZTF light curve", Type: "variable", Note: fmt.Sprintf("varies by %.2f mag in ZTF %s", v.Amplitude, v.Band)})
		l.Class = ClassNewVariable
	case ztflc.Flat:
		l.Matches = append(l.Matches, Match{Catalog: catalogZTF, Name: "ZTF light curve", Type: "flat", Note: fmt.Sprintf("flat to %.2f mag in ZTF %s", v.Amplitude, v.Band)})
		l.Class = ClassLowConfidence
	case ztflc.Unknown:
	}
	return true, false
}

// confirmByZTF holds a new or held change on a star at low confidence when
// the night's null test found as strong a change, and otherwise asks ZTF
// about it while the run's queries last.
func confirmByZTF(ctx context.Context, l *Labelled, res *Result, lookup ZTFLookup, asked *int, checked, failed []string) (outChecked, outFailed []string) {
	if l.Class != ClassNewVariable && !heldForConfirmation(*l) {
		return checked, failed
	}
	if l.SNR <= res.ChangeNull.MaxSNR {
		l.Class = ClassLowConfidence
		return checked, failed
	}
	held := l.Class != ClassNewVariable
	if held && *asked >= maxZTFCandidates {
		return checked, failed
	}
	ch, fl := ztfCheck(ctx, l, lookup)
	if held && (ch || fl) {
		*asked++
	}
	if ch {
		checked = union(checked, []string{catalogZTF})
	} else if fl {
		failed = union(failed, []string{catalogZTF})
	}
	return checked, failed
}

// minHeldSubs is the fewest subs a change held for few subs must be
// measured in for a confirmation to promote it: one night's five broadband
// subs caught the eclipse of 2MASS J21043883+4319143.
const minHeldSubs = minFilterSubs

// mayBeHeld says whether a change could be held for a confirmation: a
// fading of a catalogued star, strong, in most of its subs and in enough of
// them. A one-night brightening is not held: it is what a reference holding
// the star in eclipse makes on the star's other nights, and ZTF cannot tell
// when the star was faint.
func mayBeHeld(c Candidate) bool {
	return c.Sign < 0 && c.GaiaG != nil && c.SNR >= PromoteSigma && c.SubsTested >= minHeldSubs &&
		float64(c.PresentIn) >= minHeldPresent*float64(c.SubsTested)
}

// minHeldPresent is the least share of its subs a held change must be seen
// in for a confirmation to promote it. The three eclipses vetted in October
// 2026 were in every sub; 56 of the 77 changes on stars the method-4
// backfill called new, on one night each, were in under 80%.
const minHeldPresent = 0.8

// heldForConfirmation says whether a fading of a catalogued star is held at
// low confidence only because nothing confirmed it: no other night or
// filter group saw it, or it was strong but in under minPromoteSubs subs.
func heldForConfirmation(l Labelled) bool {
	if l.Class != ClassLowConfidence || !mayBeHeld(l.Candidate) {
		return false
	}
	t := l
	t.LowConf = false
	t.Matches = append(slices.Clone(l.Matches), Match{Catalog: catalogNight})
	return classOf(t) == ClassNewVariable
}

// PriorChange is a change found at a spot on another night, or in another
// filter group on the same night, of the same target.
type PriorChange struct {
	RA, Dec float64
	Sign    int
	SNR     float64
	Night   time.Time
	Group   string
}

// confirmArcsec is how near a change on another night or in another filter
// group must be to confirm one, about 2 px.
const confirmArcsec = 4.0

// catalogNight is the match of a change confirmed by the same change on
// another night or in another filter group.
const catalogNight = "night"

// Labelled is a candidate with its class and matches.
type Labelled struct {
	Candidate
	Class   string
	Matches []Match
}

// LabelledTrack is a moving object with its class and nearest known body.
type LabelledTrack struct {
	Track
	Class      string
	Match      *Match
	OffsetArc  *float64
	BodyV      *float64
	BodyName   string
	BodyNumber string
}

// AlertLookup asks the alert brokers about one position.
type AlertLookup func(ctx context.Context, ra, dec, radius float64) ([]Alert, []string, []string)

// Classify labels each candidate and track.
func Classify(ctx context.Context, res *Result, k Known, alerts AlertLookup) ([]Labelled, []LabelledTrack, []string, []string) {
	checked, failed := append([]string(nil), k.Checked...), append([]string(nil), k.Failed...)
	tracks := make([]LabelledTrack, len(res.Tracks))
	for i, t := range res.Tracks {
		lt := LabelledTrack{Track: t, Class: ClassUnknownMover}
		bestSep := math.Inf(1)
		var best Body
		for _, b := range k.Bodies {
			mid := t.Points[len(t.Points)/2]
			bra, bdec := b.At(mid.Time.Sub(k.Epoch))
			if sep := Separation(mid.RA, mid.Dec, bra, bdec) * 3600; sep < bestSep {
				bestSep, best = sep, b
			}
		}
		if bestSep <= bodyMatchArcsec {
			v := best.V
			lt.Class = ClassKnownAsteroid
			lt.Match = &Match{Catalog: "SkyBoT", Name: bodyName(best), Type: best.Class, SepArcsec: bestSep, Mag: &v}
			lt.OffsetArc, lt.BodyV, lt.BodyName, lt.BodyNumber = &bestSep, &v, best.Name, best.Number
		}
		tracks[i] = lt
	}
	out := make([]Labelled, len(res.Candidates))
	looked, ztfAsked := 0, 0
	order := make([]int, len(res.Candidates))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return res.Candidates[order[a]].SNR > res.Candidates[order[b]].SNR })
	for _, i := range order {
		c := res.Candidates[i]
		l := Labelled{Candidate: c}
		if ti, ok := onTrack(c, tracks, res.Start, res.End); ok {
			tr := tracks[ti]
			m := Match{Catalog: "track", Name: "moving object", Note: "on the path of a moving object linked in the subs"}
			if tr.Match != nil {
				m.Name, m.Type, m.Mag = tr.Match.Name, tr.Match.Type, tr.Match.Mag
			}
			l.Matches = append(l.Matches, m)
			l.Class = ClassUnknownMover
			if tr.Class == ClassKnownAsteroid {
				l.Class = ClassKnownAsteroid
			}
			out[i] = l
			continue
		}
		for _, v := range k.Variables {
			if sep := Separation(c.RA, c.Dec, v.RA, v.Dec) * 3600; sep <= vsxMatchArcsec && (!tightMatch(v.Catalog) || sep <= simbadMatchArcsec) {
				m := Match{Catalog: or(v.Catalog, catalogVSX), Name: v.Name, Type: v.Type, SepArcsec: sep}
				if v.Max != 0 {
					mx := v.Max
					m.Mag = &mx
					switch {
					case v.Amplitude && v.Min != 0:
						m.Note = fmt.Sprintf("%.2f %s, amplitude %.2f", v.Max, strings.TrimSpace(v.Band), v.Min)
					case v.Min != 0:
						m.Note = fmt.Sprintf("%.2f to %.2f %s", v.Max, v.Min, strings.TrimSpace(v.Band))
					default:
						m.Note = fmt.Sprintf("%.2f %s", v.Max, strings.TrimSpace(v.Band))
					}
				}
				l.Matches = append(l.Matches, m)
			}
		}
		for _, b := range k.Bodies {
			ra, dec := b.At(res.MidTime.Sub(k.Epoch))
			if sep := Separation(c.RA, c.Dec, ra, dec) * 3600; sep <= staticBodyArcsec {
				v := b.V
				l.Matches = append(l.Matches, Match{Catalog: "SkyBoT", Name: bodyName(b), Type: b.Class, SepArcsec: sep, Mag: &v})
			}
		}
		if alerts != nil && looked < maxAlertCandidates && !explained(l.Matches) && (!c.LowConf || mayBeHeld(c)) {
			looked++
			as, ch, fl := alerts(ctx, c.RA, c.Dec, alertMatchArcsec)
			checked, failed = union(checked, ch), union(failed, fl)
			for _, a := range as {
				l.Matches = append(l.Matches, Match{Catalog: a.Source, Name: a.ID, Type: a.Class, SepArcsec: a.SepArc, TNS: a.TNS})
			}
		}
		l.Matches = append(l.Matches, placeMatches(c, k)...)
		l.Class = classOf(l)
		checked, failed = confirmByZTF(ctx, &l, res, k.ZTF, &ztfAsked, checked, failed)
		annotateTNS(&l, res)
		out[i] = l
	}
	return out, tracks, checked, failed
}

// placeMatches are the galaxies a change lies in and the same changes at
// its spot on the target's other nights or in its other filter groups.
func placeMatches(c Candidate, k Known) []Match {
	var out []Match
	for _, g := range k.Galaxies {
		if in, sep := g.Inside(c.RA, c.Dec, galaxyMatchFactor); in {
			m := Match{Catalog: "galaxy", Name: g.Name, SepArcsec: sep}
			if g.LocalGroup {
				m.Type = localGroupType
			}
			out = append(out, m)
		}
	}
	for _, p := range k.Prior {
		if sep := Separation(c.RA, c.Dec, p.RA, p.Dec) * 3600; p.Sign == c.Sign && p.SNR >= PromoteSigma && sep <= confirmArcsec {
			out = append(out, Match{Catalog: catalogNight, Name: p.Night.Format(time.DateOnly) + " " + p.Group, SepArcsec: sep,
				Note: "changed the same way there"})
		}
	}
	return out
}

const trackPathPx = 3.0

// onTrack finds a track whose path across the night passes within 3 px of
// a candidate: the trail a moving object leaves in the night's stack.
func onTrack(c Candidate, tracks []LabelledTrack, start, end time.Time) (int, bool) {
	for i, t := range tracks {
		ax, ay := t.Position(start)
		bx, by := t.Position(end)
		if segmentDistance(c.X, c.Y, ax, ay, bx, by) <= trackPathPx {
			return i, true
		}
	}
	return 0, false
}

func segmentDistance(px, py, ax, ay, bx, by float64) float64 {
	dx, dy := bx-ax, by-ay
	l2 := dx*dx + dy*dy
	t := 0.0
	if l2 > 0 {
		t = math.Max(0, math.Min(1, ((px-ax)*dx+(py-ay)*dy)/l2))
	}
	return math.Hypot(px-(ax+t*dx), py-(ay+t*dy))
}

// wrapDeg brings an angle difference into [-180, 180).
func wrapDeg(d float64) float64 {
	return math.Mod(math.Mod(d+180, 360)+360, 360) - 180
}

func bodyName(b Body) string {
	if b.Number != "" {
		return fmt.Sprintf("(%s) %s", b.Number, b.Name)
	}
	return b.Name
}

// classOf picks a candidate's class. A source gone since the reference with
// no catalogued star there had nothing to fade, so it is most often a
// reflection or trail in the reference, and is held at low confidence. A
// change that matches nothing known is new only when it is confirmed: the
// same change on another night or in another filter group. The filters of
// one group are not independent: a change of the sky's color between the
// night and the reference moves a star alike in L, R, G and B. One night's
// change alone is held at low confidence, as the backfill of October 2026
// found over a hundred such changes on stars and none held up; the star's
// ZTF light curve can confirm one instead (see ztfCheck). A source where no
// star was before is not held: it may be a transient, and follow-up imaging
// is asked for on it. An alert broker's ZTF object at a catalogued star is
// that star's own variability in ZTF's differences, not a transient: only a
// TNS entry makes a change on a star a known transient.
func classOf(l Labelled) string {
	has := func(cat ...string) bool {
		for _, m := range l.Matches {
			for _, c := range cat {
				if m.Catalog == c {
					return true
				}
			}
		}
		return false
	}
	switch {
	case has(catalogVSX), knownSIMBAD(l.Matches), has(surveyCatalogNames()...):
		return ClassKnownVariable
	case has("SkyBoT"):
		return ClassKnownAsteroid
	case has(sourceTNS), l.GaiaG == nil && has(sourceAlerce, sourceFink):
		return ClassKnownTransient
	case l.LowConf, l.Sign < 0 && l.GaiaG == nil:
		return ClassLowConfidence
	case l.Sign > 0 && l.GaiaG == nil && inLocalGroup(l.Matches):
		return ClassNova
	case l.Sign > 0 && l.GaiaG == nil && has("galaxy"):
		return ClassSupernova
	case l.GaiaG != nil && !has(catalogNight):
		return ClassLowConfidence
	}
	return ClassNewVariable
}

func inLocalGroup(ms []Match) bool {
	for _, m := range ms {
		if m.Catalog == "galaxy" && m.Type == localGroupType {
			return true
		}
	}
	return false
}

// isLocalGroup says whether a catalogue galaxy, by its designation or
// aliases, is one of the Local Group's the built-in catalogue can hold, by
// PGC number: M 31, M 32, M 110, M 33, NGC 147, NGC 185, NGC 6822, IC 10,
// IC 1613, WLM, Leo A, Leo I, the Pegasus dwarf and the Magellanic Clouds.
func isLocalGroup(designation string, aliases []string) bool {
	for _, n := range append([]string{designation}, aliases...) {
		switch n {
		case "PGC 2557", "PGC 2555", "PGC 2429", "PGC 5818", "PGC 2004", "PGC 2329", "PGC 63616", "PGC 1305", "PGC 3844",
			"PGC 143", "PGC 28868", "PGC 29488", "PGC 71538", "PGC 17223", "PGC 3085":
			return true
		}
	}
	return false
}

// knownSIMBAD says whether a SIMBAD variable matched. A past nova does not
// make a brightening known: it may be erupting again.
func knownSIMBAD(ms []Match) bool {
	for _, m := range ms {
		if m.Catalog == catalogSIMBAD && !pastNova(m) {
			return true
		}
	}
	return false
}

func pastNova(m Match) bool {
	return m.Catalog == catalogSIMBAD && (m.Type == "No*" || m.Type == "No?")
}

// explained says whether a match other than a past nova already tells what
// a change is, so the alert brokers need not be asked.
func explained(ms []Match) bool {
	for _, m := range ms {
		if !pastNova(m) {
			return true
		}
	}
	return false
}

func union(a, b []string) []string {
	for _, x := range b {
		found := false
		for _, y := range a {
			if x == y {
				found = true
				break
			}
		}
		if !found {
			a = append(a, x)
		}
	}
	return a
}

// AlertsFrom asks ALeRCE, Fink and TNS (its local copy, or with a key its
// search API) about one position. A service that fails once is not asked
// again by the same lookup, so one that is down costs one timeout per run,
// not one per candidate.
func (c *Catalogs) AlertsFrom() AlertLookup {
	down := map[string]bool{}
	return func(ctx context.Context, ra, dec, radius float64) ([]Alert, []string, []string) {
		var out []Alert
		var checked, failed []string
		type src struct {
			name string
			fn   func(context.Context, float64, float64, float64) ([]Alert, error)
		}
		srcs := []src{{sourceAlerce, c.Alerce}, {sourceFink, c.Fink}}
		if c.tnsAvailable() {
			srcs = append(srcs, src{sourceTNS, c.tnsSearch})
		}
		for _, s := range srcs {
			if down[s.name] {
				failed = append(failed, s.name)
				continue
			}
			cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			as, err := s.fn(cctx, ra, dec, radius)
			cancel()
			if err != nil {
				down[s.name] = true
				failed = append(failed, s.name)
				continue
			}
			checked = append(checked, s.name)
			for _, a := range as {
				if a.SepArc <= radius || a.Source == sourceTNS {
					out = append(out, a)
				}
			}
		}
		return out, checked, failed
	}
}
