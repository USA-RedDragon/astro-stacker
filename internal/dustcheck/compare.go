package dustcheck

import (
	"math"
	"sort"
	"time"
)

// What a difference blob between two flats is.
const (
	ClassNew      = "new"      // a shadow that wasn't there
	ClassGone     = "gone"     // a shadow that left
	ClassMoved    = "moved"    // a shadow gone next to a new one of its size
	ClassDiffuse  = "diffuse"  // a soft illumination change, not dust
	ClassEdge     = "edge"     // touches the border, where flats differ most
	ClassHalo     = "halo"     // the bright ring beside a much deeper new shadow
	ClassRotShift = "rotshift" // the rim of a known shadow, with the angle changed
)

// Where a change is, from which filters show it.
const (
	ScopeWindow  = "window"  // the sensor window or sensor: every filter
	ScopeFilter  = "filter"  // one filter only
	ScopeUnknown = "unknown" // only one filter was checked
)

// Change reports whether a class is a real dust change worth telling.
func Change(class string) bool {
	return class == ClassNew || class == ClassGone || class == ClassMoved
}

// Flat is one flat set's dust map with what it was taken with.
type Flat struct {
	ID      int
	Filter  string
	Rotator float64 // NaN without a rotator
	TakenAt time.Time
	Map     Smoothed
}

// Event is one blob in the difference of two flats of a filter.
type Event struct {
	Blob
	Class    string
	RotMatch bool
	// Partner is the other half of a moved shadow.
	PartnerX, PartnerY int
	DiffSigma          float64
}

// AngleDiff is the difference between two rotator angles in degrees, 0 when
// either is unknown.
func AngleDiff(a, b float64) float64 {
	if math.IsNaN(a) || math.IsNaN(b) {
		return 0
	}
	d := math.Mod(math.Abs(a-b), 360)
	return math.Min(d, 360-d)
}

// Previous picks the flat to compare cur with: the latest earlier one of
// its filter at the same angle, or the latest of its filter at any angle
// when there is none. history must be in time order.
func Previous(cur Flat, history []Flat) (Flat, bool) {
	var latest, same *Flat
	for i := range history {
		f := &history[i]
		if f.Filter != cur.Filter || !f.TakenAt.Before(cur.TakenAt) || f.ID == cur.ID {
			continue
		}
		latest = f
		if AngleDiff(f.Rotator, cur.Rotator) <= RotationTolerance {
			same = f
		}
	}
	switch {
	case same != nil:
		return *same, true
	case latest != nil:
		return *latest, true
	}
	return Flat{}, false
}

// Compare finds and classes the blobs in cur - prev; maps of different
// sizes have none.
func Compare(prev, cur Flat) []Event {
	if prev.Map.W != cur.Map.W || prev.Map.H != cur.Map.H {
		return nil
	}
	d := Diff(prev.Map, cur.Map)
	sg := Sigma(d)
	thr := math.Max(KSigma*sg, DiffMin)
	rotMatch := AngleDiff(prev.Rotator, cur.Rotator) <= RotationTolerance
	var known []Blob
	if !rotMatch {
		ps := Sigma(prev.Map)
		for _, k := range Blobs(prev.Map, math.Max(KSigma*ps, 0.003), -1, EdgeMin) {
			if k.Peak <= -0.8 {
				known = append(known, k)
			}
		}
	}
	var evs []Event
	for _, sign := range []int{-1, 1} {
		for _, b := range Blobs(d, thr, sign, EdgeMin) {
			e := Event{Blob: b, RotMatch: rotMatch, DiffSigma: sg}
			switch {
			case !b.Disc:
				e.Class = ClassDiffuse
			case sign < 0:
				e.Class = ClassNew
			default:
				e.Class = ClassGone
			}
			for _, k := range known {
				if dist(e.Blob, k) < 0.65*float64(k.Diameter)+0.5*float64(e.Diameter) {
					e.Class = ClassRotShift
				}
			}
			margin := float64(Border * BinFactor)
			w, h := float64(cur.Map.W*BinFactor), float64(cur.Map.H*BinFactor)
			if (e.Class == ClassNew || e.Class == ClassGone) &&
				math.Min(math.Min(float64(e.X), float64(e.Y)), math.Min(w-float64(e.X), h-float64(e.Y)))-float64(e.Diameter)/2 < margin {
				e.Class = ClassEdge
			}
			evs = append(evs, e)
		}
	}
	for i := range evs {
		e := &evs[i]
		if e.Class != ClassGone {
			continue
		}
		for _, f := range evs {
			if f.Class == ClassNew && math.Abs(f.Peak) >= 3*math.Abs(e.Peak) &&
				dist(e.Blob, f.Blob) < 0.75*float64(f.Diameter)+0.5*float64(e.Diameter) {
				e.Class = ClassHalo
			}
		}
	}
	for i := range evs {
		for j := range evs {
			e, f := &evs[i], &evs[j]
			if e.Peak >= 0 || f.Peak <= 0 || e.Class != ClassNew || f.Class != ClassGone {
				continue
			}
			ratio := float64(e.Diameter) / float64(f.Diameter)
			if dist(e.Blob, f.Blob) < 1.5*float64(max(e.Diameter, f.Diameter)) && ratio > 0.5 && ratio < 2 {
				e.Class, f.Class = ClassMoved, ClassMoved
				e.PartnerX, e.PartnerY = f.X, f.Y
				f.PartnerX, f.PartnerY = e.X, e.Y
			}
		}
	}
	return evs
}

func dist(a, b Blob) float64 {
	return math.Hypot(float64(a.X-b.X), float64(a.Y-b.Y))
}

// Catalog is a flat's dust discs at least 0.25 % deep.
func Catalog(s Smoothed) []Blob {
	var out []Blob
	for _, b := range Blobs(s, DiffMin, -1, 0.6) {
		if b.Disc {
			out = append(out, b)
		}
	}
	return out
}

// Near reports whether two blobs are the same spot.
func Near(a, b Blob) bool {
	return dist(a, b) < 0.3*float64(max(a.Diameter, b.Diameter))
}

// Located is a change found in one filter's flats.
type Located struct {
	Filter string
	Event
}

// Group is one change seen in one or more filters on one night.
type Group struct {
	Members []Located
	Scope   string
}

// Filters is the group's filters, sorted.
func (g Group) Filters() []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range g.Members {
		if !seen[m.Filter] {
			seen[m.Filter] = true
			out = append(out, m.Filter)
		}
	}
	sort.Strings(out)
	return out
}

// GroupChanges groups one night's changes across filters and says whether
// each is on the window or a filter. A change in two or more filters is on
// the window. A change in one filter is on that filter when another filter
// was checked the same night and didn't show it. With only its own filter
// checked, a new shadow that other filters' latest flats (others) already
// hold is on the window; anything else is unknown. checked is how many
// filters were compared that night.
func GroupChanges(changes []Located, checked int, others map[string][]Blob) []Group {
	var groups []Group
	for _, c := range changes {
		if c.Class == ClassMoved && c.Peak > 0 {
			continue
		}
		placed := false
		for gi := range groups {
			g := &groups[gi]
			if g.Members[0].Class == c.Class && Near(g.Members[0].Blob, c.Blob) {
				g.Members = append(g.Members, c)
				placed = true
				break
			}
		}
		if !placed {
			groups = append(groups, Group{Members: []Located{c}})
		}
	}
	for gi := range groups {
		g := &groups[gi]
		fs := g.Filters()
		switch {
		case len(fs) >= 2:
			g.Scope = ScopeWindow
		case checked >= 2:
			g.Scope = ScopeFilter
		default:
			g.Scope = ScopeUnknown
			m := g.Members[0]
			if m.Peak >= 0 {
				break
			}
			for f, cat := range others {
				if f == m.Filter {
					continue
				}
				for _, b := range cat {
					if Near(b, m.Blob) {
						g.Scope = ScopeWindow
					}
				}
			}
		}
	}
	return groups
}
