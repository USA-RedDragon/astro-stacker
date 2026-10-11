package mosaicplan

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/frameheader"
	"github.com/USA-RedDragon/astro-stacker/internal/goals"
	"github.com/USA-RedDragon/astro-stacker/internal/mosaics"
	"github.com/USA-RedDragon/astro-stacker/internal/planning"
)

const (
	boostSampleNights = 7
	stateActive       = "Active"
)

type TargetSeasonBoost struct {
	Project   string  `json:"project"`
	Target    string  `json:"target"`
	GUID      string  `json:"guid"`
	Needed    float64 `json:"hoursNeeded"`
	Available float64 `json:"hoursAvailable"`
	Boost     float64 `json:"boost"`
}

func (s *Service) SeasonBoosts(ctx context.Context, now time.Time) (map[string]float64, error) {
	out := map[string]float64{}
	if s.Site == nil || s.Sched == nil {
		return out, nil
	}
	site, ok := s.Site(ctx)
	if !ok {
		return out, nil
	}
	in, err := planning.AppInputs(ctx, s.App, s.Sched)
	if err != nil {
		return nil, fmt.Errorf("load goals: %w", err)
	}
	in.Now = now
	snap, err := planning.Load(ctx, s.Sched, s.App, in)
	if err != nil {
		return nil, fmt.Errorf("load planning: %w", err)
	}
	hist, err := s.clearNights(ctx)
	if err != nil {
		return nil, err
	}
	ratio, err := s.effectivePerRaw(ctx, Detail{})
	if err != nil {
		return nil, err
	}
	perRaw := 1.0
	if ratio != nil && ratio.Value > 0 {
		perRaw = ratio.Value
	}
	for _, b := range SeasonBoostsFor(snap.Projects, site, ClearShare(hist.perMonth()), perRaw, now) {
		out[b.GUID] = b.Boost
	}
	return out, nil
}

func SeasonBoostsFor(projects []planning.Project, site mosaics.Site, share [12]float64, effectivePerRaw float64, now time.Time) []TargetSeasonBoost {
	var out []TargetSeasonBoost
	for _, p := range projects {
		if p.State != stateActive {
			continue
		}
		for _, t := range p.Targets {
			if !t.Active || t.GUID == "" || t.RAHours == nil || t.Dec == nil {
				continue
			}
			b := TargetSeasonBoost{Project: p.Name, Target: t.Name, GUID: t.GUID}
			avail, in := SeasonHours(t.Season, site, mosaics.Point{RA: *t.RAHours * 15, Dec: *t.Dec}, p.MinimumAltitude, p.MinimumTime, share, now)
			if in {
				b.Needed = TargetHoursNeeded(t, p.Grader, effectivePerRaw)
				b.Available = avail
				b.Boost = SeasonPriority(b.Needed, b.Available)
			}
			out = append(out, b)
		}
	}
	return out
}

func SeasonPriority(needed, available float64) float64 {
	if !(needed > 0) || math.IsInf(needed, 0) {
		return 0
	}
	return needed / (needed + math.Max(0, available))
}

func ClearShare(months []MonthNights) [12]float64 {
	var out [12]float64
	var sum float64
	known := 0
	for i := range 12 {
		out[i] = math.NaN()
		if i < len(months) && months[i].Nights != nil {
			days := float64(time.Date(2001, time.Month(i+2), 0, 0, 0, 0, 0, time.UTC).Day())
			out[i] = math.Max(0, math.Min(1, *months[i].Nights/days))
			sum += out[i]
			known++
		}
	}
	fill := 1.0
	if known > 0 {
		fill = sum / float64(known)
	}
	for i := range out {
		if math.IsNaN(out[i]) {
			out[i] = fill
		}
	}
	return out
}

func SeasonHours(season *planning.Season, site mosaics.Site, pt mosaics.Point, minAlt float64, minMinutes int, share [12]float64, now time.Time) (float64, bool) {
	if season == nil || season.OutOfSeason || season.NightsLeft <= 0 {
		return 0, false
	}
	local := now.Add(time.Duration(site.Lon / 15 * float64(time.Hour))).Add(-12 * time.Hour)
	first := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
	n := season.NightsLeft
	if end, err := time.Parse(time.DateOnly, season.SeasonEnd); err == nil {
		if d := int(end.Sub(first).Hours()/24) + 1; d > 0 {
			n = d
		}
	}
	var sum float64
	for i := 0; i < n; i += boostSampleNights {
		w := min(boostSampleNights, n-i)
		night := first.AddDate(0, 0, i+w/2)
		h := mosaics.DarkHours(night, site, []mosaics.Point{pt}, minAlt)
		if h*60 < float64(minMinutes) {
			continue
		}
		sum += float64(w) * h * share[int(night.Month())-1]
	}
	return sum, true
}

func TargetHoursNeeded(t planning.Target, grader bool, effectivePerRaw float64) float64 {
	if !(effectivePerRaw > 0) {
		effectivePerRaw = 1
	}
	byFilter := map[string][]planning.Plan{}
	var order []string
	for _, p := range t.Plans {
		if !p.Enabled {
			continue
		}
		k := frameheader.NormalizeFilter(p.Filter)
		if _, seen := byFilter[k]; !seen {
			order = append(order, k)
		}
		byFilter[k] = append(byFilter[k], p)
	}
	goalsBy := map[string]planning.FilterGoal{}
	for _, g := range t.Goals {
		goalsBy[frameheader.NormalizeFilter(g.Filter)] = g
	}
	var total float64
	for _, k := range order {
		plans := byFilter[k]
		g, ok := goalsBy[k]
		if !ok || g.Readiness == nil {
			total += countHours(plans, grader)
			continue
		}
		total += goalHours(g, plans, grader, effectivePerRaw)
	}
	return total
}

func goalHours(g planning.FilterGoal, plans []planning.Plan, grader bool, effectivePerRaw float64) float64 {
	if g.Progress != nil && g.Progress.Done {
		return 0
	}
	r := g.Readiness
	if r.State == goals.StateMeasured && g.Progress != nil && g.Progress.HoursNeeded >= 0 && !math.IsInf(g.Progress.HoursNeeded, 0) {
		return g.Progress.HoursNeeded / effectivePerRaw
	}
	exposure := 0.0
	for _, p := range plans {
		if p.Exposure > 0 {
			exposure = p.Exposure
			break
		}
	}
	subs := 1
	switch r.State {
	case goals.StateUnmeasurable:
		if r.SubLimit > 0 {
			return float64(max(1, r.SubLimit-r.StackSubs)) * exposure / 3600
		}
	case goals.StateCollecting:
		if r.MinSubs > 0 {
			subs = max(1, r.MinSubs-r.StackSubs)
		}
	}
	return math.Max(countHours(plans, grader), float64(subs)*exposure/3600)
}

func countHours(plans []planning.Plan, grader bool) float64 {
	var h float64
	for _, p := range plans {
		done := p.Acquired
		if grader {
			done = p.Accepted
		}
		if p.Complete || p.Exposure <= 0 {
			continue
		}
		h += float64(max(0, p.Desired-done)) * p.Exposure / 3600
	}
	return h
}
