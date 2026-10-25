package vetting

import (
	"encoding/json"
	"sort"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/discoverysource"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/ownimage"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/period"
)

// minTimed is the fewest frames of one night and filter a dip is timed
// from.
const minTimed = 5

// ownSet is one night's frames of the star in one filter, measured
// against the comparison stars or, with diff, on the detection's
// difference images.
type ownSet struct {
	night, filter string
	diff          bool
	points        []lc.Point
}

// ownSets splits the user's own frames of the star into nights and
// filters, in date order: the comparison star curve's on every night,
// and the difference image photometry the detection kept for each
// flagged night. Blended neighbours and nebulosity add scatter to the
// comparison star curve that the difference images leave out.
func ownSets(curve *ownimage.Curve, sum discoverysource.Summary) []ownSet {
	type key struct {
		night, filter string
		diff          bool
	}
	idx := map[key]int{}
	seen := map[key]map[int]bool{}
	var out []ownSet
	add := func(k key, frame int, p lc.Point) {
		i, ok := idx[k]
		if !ok {
			i = len(out)
			idx[k] = i
			seen[k] = map[int]bool{}
			out = append(out, ownSet{night: k.night, filter: k.filter, diff: k.diff})
		}
		if k.diff {
			if seen[k][frame] {
				return
			}
			seen[k][frame] = true
		}
		out[i].points = append(out[i].points, p)
	}
	if curve != nil {
		for _, p := range curve.Points {
			add(key{p.Night, p.Filter, false}, p.FrameID, lc.Point{BJD: p.BJD, Mag: p.DMag, Err: p.Err})
		}
	}
	for _, m := range sum.Members {
		var light []discoveries.LightPoint
		if m.Light == "" || json.Unmarshal([]byte(m.Light), &light) != nil {
			continue
		}
		night := m.Night.UTC().Format(time.DateOnly)
		for _, l := range light {
			if l.DeltaMag == nil || l.MagErr == nil {
				continue
			}
			add(key{night, l.Filter, true}, l.FrameID, lc.Point{BJD: lc.BJD(l.Time, sum.Source.RA, sum.Source.Dec), Mag: *l.DeltaMag, Err: *l.MagErr})
		}
	}
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].night != out[b].night {
			return out[a].night < out[b].night
		}
		if out[a].filter != out[b].filter {
			return out[a].filter < out[b].filter
		}
		return !out[a].diff && out[b].diff
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
		t.Night, t.Filter, t.Diff = s.night, s.filter, s.diff
		out = append(out, t)
	}
	return out
}

// ownTimingOf is the timing the own_timing check reports: the flagged
// night's when its frames time a dip, and otherwise the best timed of
// the field's other nights, from whichever photometry times it best.
// 76400 (Garlic Nebula), whose flagged night
// of 2026-10-07 caught only an egress, said its frames covered no dip,
// though its O-III frames of 2026-02-19 cover a whole primary.
func ownTimingOf(pr *period.Result, curve *ownimage.Curve, sum discoverysource.Summary) *period.Timing {
	if pr == nil || pr.Ephemeris == nil {
		return nil
	}
	flagged := sum.Best.Night.UTC().Format(time.DateOnly)
	var best *period.Timing
	for _, t := range ownTimings(pr.Ephemeris, ownSets(curve, sum)) {
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
