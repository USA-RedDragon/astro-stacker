package vetting

import (
	"sort"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoverysource"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/ownimage"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/period"
)

// minTimed is the fewest frames of one night and filter a dip is timed
// from.
const minTimed = 5

// ownSet is one night's frames of the star in one filter.
type ownSet struct {
	night, filter string
	points        []lc.Point
}

// ownSets splits the user's own frames of the star into nights and
// filters, in date order.
func ownSets(curve *ownimage.Curve) []ownSet {
	type key struct{ night, filter string }
	idx := map[key]int{}
	var out []ownSet
	if curve == nil {
		return nil
	}
	for _, p := range curve.Points {
		k := key{p.Night, p.Filter}
		i, ok := idx[k]
		if !ok {
			i = len(out)
			idx[k] = i
			out = append(out, ownSet{night: p.Night, filter: p.Filter})
		}
		out[i].points = append(out[i].points, lc.Point{BJD: p.BJD, Mag: p.DMag, Err: p.Err})
	}
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].night != out[b].night {
			return out[a].night < out[b].night
		}
		return out[a].filter < out[b].filter
	})
	for _, s := range out {
		sort.Slice(s.points, func(a, b int) bool { return s.points[a].BJD < s.points[b].BJD })
	}
	return out
}

// ownTimings times every night and filter of the user's own frames that
// covers a predicted dip's minimum, primary or secondary.
func ownTimings(e *period.Ephemeris, sets []ownSet) []period.Timing {
	var out []period.Timing
	for _, s := range sets {
		if len(s.points) < minTimed {
			continue
		}
		t, ok := period.TimeMinimum(e, s.points, bandOf(e, s.filter))
		if !ok {
			continue
		}
		t.Night, t.Filter = s.night, s.filter
		out = append(out, t)
	}
	return out
}

// ownTimingOf is the timing the own_timing check reports: the flagged
// night's when its frames time a dip, and otherwise the best timed of
// the field's other nights. 76400 (Garlic Nebula), whose flagged night
// of 2026-10-07 caught only an egress, said its frames covered no dip,
// though its O-III frames of 2026-02-19 cover a whole primary.
func ownTimingOf(pr *period.Result, curve *ownimage.Curve, sum discoverysource.Summary) *period.Timing {
	if pr == nil || pr.Ephemeris == nil {
		return nil
	}
	flagged := sum.Best.Night.UTC().Format(time.DateOnly)
	var best *period.Timing
	for _, t := range ownTimings(pr.Ephemeris, ownSets(curve)) {
		if best == nil || betterTiming(t, *best, flagged) {
			best = &t
		}
	}
	return best
}

func betterTiming(a, b period.Timing, flagged string) bool {
	if (a.Night == flagged) != (b.Night == flagged) {
		return a.Night == flagged
	}
	return a.OffsetErr < b.OffsetErr
}
