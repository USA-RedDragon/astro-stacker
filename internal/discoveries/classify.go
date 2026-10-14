package discoveries

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// Classes of what changed.
const (
	ClassKnownVariable  = "known_variable"
	ClassKnownAsteroid  = "known_asteroid"
	ClassKnownTransient = "known_transient"
	ClassSupernova      = "supernova_candidate"
	ClassNewVariable    = "new_variable_candidate"
	ClassUnknownMover   = "unknown_mover"
	ClassLowConfidence  = "low_confidence"
)

// Classes lists every class.
func Classes() []string {
	return []string{ClassKnownVariable, ClassKnownAsteroid, ClassKnownTransient, ClassSupernova, ClassNewVariable, ClassUnknownMover, ClassLowConfidence}
}

// IsNew says whether a class passed every check and matched nothing known.
func IsNew(class string) bool {
	return class == ClassSupernova || class == ClassNewVariable || class == ClassUnknownMover
}

const (
	vsxMatchArcsec     = 5.0
	bodyMatchArcsec    = 20.0
	staticBodyArcsec   = 10.0
	alertMatchArcsec   = 3.0
	galaxyMatchFactor  = 1.2
	maxAlertCandidates = 60
)

// Match is a catalogue entry a candidate or track matched.
type Match struct {
	Catalog   string   `json:"catalog"`
	Name      string   `json:"name"`
	Type      string   `json:"type,omitempty"`
	SepArcsec float64  `json:"sep_arcsec"`
	Mag       *float64 `json:"mag,omitempty"`
	Note      string   `json:"note,omitempty"`
}

// Galaxy is a catalogued galaxy's ellipse.
type Galaxy struct {
	Name           string
	RA, Dec        float64
	Major, Minor   float64
	PA             float64
	HasOrientation bool
}

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
	// Checked names the catalogues consulted and Failed those that could not
	// be reached.
	Checked []string
	Failed  []string
}

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
	looked := 0
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
			if sep := Separation(c.RA, c.Dec, v.RA, v.Dec) * 3600; sep <= vsxMatchArcsec {
				m := Match{Catalog: "VSX", Name: v.Name, Type: v.Type, SepArcsec: sep}
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
		if alerts != nil && looked < maxAlertCandidates && len(l.Matches) == 0 && !c.LowConf {
			looked++
			as, ch, fl := alerts(ctx, c.RA, c.Dec, alertMatchArcsec)
			checked, failed = union(checked, ch), union(failed, fl)
			for _, a := range as {
				l.Matches = append(l.Matches, Match{Catalog: a.Source, Name: a.ID, Type: a.Class, SepArcsec: a.SepArc})
			}
		}
		for _, g := range k.Galaxies {
			if in, sep := g.Inside(c.RA, c.Dec, galaxyMatchFactor); in {
				l.Matches = append(l.Matches, Match{Catalog: "galaxy", Name: g.Name, SepArcsec: sep})
			}
		}
		l.Class = classOf(l)
		out[i] = l
	}
	return out, tracks, checked, failed
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
// reflection or trail in the reference, and is held at low confidence.
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
	case has("VSX"):
		return ClassKnownVariable
	case has("SkyBoT"):
		return ClassKnownAsteroid
	case has(sourceAlerce, sourceFink, sourceTNS):
		return ClassKnownTransient
	case l.LowConf, l.Sign < 0 && l.GaiaG == nil:
		return ClassLowConfidence
	case l.Sign > 0 && l.GaiaG == nil && has("galaxy"):
		return ClassSupernova
	}
	return ClassNewVariable
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

// AlertsFrom asks ALeRCE, Fink and (with a key) TNS about one position. A
// service that fails once is not asked again by the same lookup, so one
// that is down costs one timeout per run, not one per candidate.
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
		if c.TNSEnabled() {
			srcs = append(srcs, src{sourceTNS, c.TNS})
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
