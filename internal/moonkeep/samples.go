package moonkeep

import (
	"math"
	"sort"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/frameheader"
	"github.com/USA-RedDragon/astro-stacker/internal/goals"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

// Input is one light and the stacker's verdict on it.
type Input struct {
	Object   string
	Filter   string
	Exposure float64
	Start    time.Time
	Status   string
	Score    *float64
	RA, Dec  float64
	HasPos   bool
}

// Verdict is whether the stacker kept a light on its quality: stacked, or
// left out only by a moon avoidance rule after passing its score (the
// managed templates the plugin plans with have none). ok is false for a
// light with no quality verdict.
func Verdict(status string) (kept, ok bool) {
	switch status {
	case app.StackStatusAdded, app.StackStatusNight, app.StackStatusRecalibrate, app.StackStatusMoon:
		return true, true
	case app.StackStatusLowScore, app.StackStatusRejected:
		return false, true
	}
	return false, false
}

func scored(status string) bool {
	return status == app.StackStatusAdded || status == app.StackStatusNight || status == app.StackStatusLowScore
}

// NightOf is the local date the night of t started on, by mean solar time
// at east longitude lon.
func NightOf(t time.Time, lon float64) string {
	return t.UTC().Add(time.Duration(lon/15*float64(time.Hour)) - 12*time.Hour).Format(time.DateOnly)
}

const (
	qualityWindow   = 3 * time.Hour
	qualityMinSubs  = 3
	qualityStep     = 0.05
	nightQualityMax = 1.0
)

type scoreAt struct {
	at    time.Time
	score float64
}

// qualityBefore is the night quality the stacker would have published just
// before t: the median score of the subs in the 3 hours before it, as
// goals.NightQuality computes it.
func qualityBefore(all []scoreAt, t time.Time) *float64 {
	lo := sort.Search(len(all), func(i int) bool { return all[i].at.After(t.Add(-qualityWindow)) })
	hi := sort.Search(len(all), func(i int) bool { return !all[i].at.Before(t) })
	if hi-lo < qualityMinSubs {
		return nil
	}
	v := make([]float64, 0, hi-lo)
	for _, s := range all[lo:hi] {
		v = append(v, s.score)
	}
	sort.Float64s(v)
	mid := len(v) / 2
	q := v[mid]
	if len(v)%2 == 0 {
		q = (v[mid-1] + v[mid]) / 2
	}
	q = math.Round(q/qualityStep) * qualityStep
	return &q
}

// Samples turns lights into samples, seen from lat, lon: lights with a
// verdict, a time, a length and a position.
func Samples(in []Input, lat, lon float64) []Sample {
	var scores []scoreAt
	for _, r := range in {
		if scored(r.Status) && r.Score != nil && !math.IsNaN(*r.Score) && !math.IsInf(*r.Score, 0) {
			scores = append(scores, scoreAt{r.Start, math.Max(0, math.Min(nightQualityMax, *r.Score))})
		}
	}
	sort.Slice(scores, func(i, j int) bool { return scores[i].at.Before(scores[j].at) })
	out := make([]Sample, 0, len(in))
	names := map[string]string{}
	for _, r := range in {
		kept, ok := Verdict(r.Status)
		if !ok || !r.HasPos || !(r.Exposure > 0) || r.Start.IsZero() {
			continue
		}
		f := frameheader.NormalizeFilter(strings.TrimSpace(r.Filter))
		if first, ok := names[strings.ToUpper(f)]; ok {
			f = first
		} else {
			names[strings.ToUpper(f)] = f
		}
		mid := r.Start.Add(time.Duration(r.Exposure / 2 * float64(time.Second)))
		out = append(out, Sample{
			Night: NightOf(r.Start, lon), Object: r.Object, Filter: f, Narrowband: goals.IsNarrowband(f), Kept: kept,
			Exposure: r.Exposure, Start: r.Start, Moon: At(mid, r.RA, r.Dec, lat, lon), Quality: qualityBefore(scores, r.Start),
		})
	}
	return out
}
