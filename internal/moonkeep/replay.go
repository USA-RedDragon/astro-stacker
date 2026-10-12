package moonkeep

import (
	"math"
	"sort"
)

// Filter is one filter a target has a plan for.
type Filter struct {
	Name       string `json:"name"`
	Narrowband bool   `json:"narrowband"`
}

// ReplayNight is what the rule would have done on one past night.
type ReplayNight struct {
	Night        string  `json:"night"`
	Illumination float64 `json:"illumination"`
	Subs         int     `json:"subs"`
	Kept         int     `json:"kept"`
	SkippedSubs  int     `json:"skipped_subs"`
	SkippedHours float64 `json:"skipped_hours"`
	KeptLost     int     `json:"kept_lost"`
	IdleHours    float64 `json:"idle_hours"`
}

// Replay is the rule run over past nights, each night judged by a model
// fitted without it.
type Replay struct {
	Nights        int           `json:"nights"`
	MoonUpNights  int           `json:"moon_up_nights"`
	Subs          int           `json:"subs"`
	Kept          int           `json:"kept"`
	MoonUpSubs    int           `json:"moon_up_subs"`
	MoonUpHours   float64       `json:"moon_up_hours"`
	MoonUpKept    int           `json:"moon_up_kept"`
	SkippedSubs   int           `json:"skipped_subs"`
	SkippedHours  float64       `json:"skipped_hours"`
	SkippedNights int           `json:"skipped_nights"`
	KeptLost      int           `json:"kept_lost"`
	IdleSubs      int           `json:"idle_subs"`
	IdleHours     float64       `json:"idle_hours"`
	IdleKeptLost  int           `json:"idle_kept_lost"`
	WithoutGood   ReplayCounts  `json:"without_quality_check"`
	Worst         []ReplayNight `json:"worst"`
	ByNight       []ReplayNight `json:"by_night"`
}

// ReplayCounts is the rule without the night quality check, for
// comparison.
type ReplayCounts struct {
	SkippedSubs  int     `json:"skipped_subs"`
	SkippedHours float64 `json:"skipped_hours"`
	KeptLost     int     `json:"kept_lost"`
}

// Run replays the rule: each night with Moon-up subs is judged by a model
// fitted on every other night. filters gives each object's planned
// filters, for the hours the target would have been left idle (every one of
// its filters skipped); an object without any counts only its own filter.
func Run(samples []Sample, filters map[string][]Filter) Replay {
	return run(samples, filters, SkipBelow)
}

func run(samples []Sample, filters map[string][]Filter, skipBelow float64) Replay {
	var out Replay
	byNight := map[string][]int{}
	nights := map[string]bool{}
	for i, s := range samples {
		c := CellOf(s.Moon)
		if !c.Down && !c.Valid() {
			continue
		}
		nights[s.Night] = true
		out.Subs++
		if s.Kept {
			out.Kept++
		}
		if c.Down {
			continue
		}
		byNight[s.Night] = append(byNight[s.Night], i)
	}
	out.Nights = len(nights)
	out.MoonUpNights = len(byNight)
	names := make([]string, 0, len(byNight))
	for n := range byNight {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		rest := make([]Sample, 0, len(samples))
		for _, s := range samples {
			if s.Night != n {
				rest = append(rest, s)
			}
		}
		m := fit(rest, true, skipBelow)
		rn := ReplayNight{Night: n}
		var illum float64
		for _, i := range byNight[n] {
			s := samples[i]
			h := s.Exposure / 3600
			rn.Subs++
			out.MoonUpSubs++
			out.MoonUpHours += h
			illum = math.Max(illum, s.Moon.Illumination)
			if s.Kept {
				rn.Kept++
				out.MoonUpKept++
			}
			if m.Skips(s.Filter, s.Narrowband, s.Moon, nil) {
				out.WithoutGood.SkippedSubs++
				out.WithoutGood.SkippedHours += h
				if s.Kept {
					out.WithoutGood.KeptLost++
				}
			}
			if !m.Skips(s.Filter, s.Narrowband, s.Moon, s.Quality) {
				continue
			}
			rn.SkippedSubs++
			rn.SkippedHours += h
			if s.Kept {
				rn.KeptLost++
			}
			idle := true
			for _, f := range filters[s.Object] {
				if !m.Skips(f.Name, f.Narrowband, s.Moon, s.Quality) {
					idle = false
					break
				}
			}
			if idle {
				rn.IdleHours += h
				out.IdleSubs++
				out.IdleHours += h
				if s.Kept {
					out.IdleKeptLost++
				}
			}
		}
		rn.Illumination = round(illum)
		rn.SkippedHours = math.Round(rn.SkippedHours*100) / 100
		rn.IdleHours = math.Round(rn.IdleHours*100) / 100
		out.SkippedSubs += rn.SkippedSubs
		out.SkippedHours += rn.SkippedHours
		out.KeptLost += rn.KeptLost
		if rn.SkippedSubs > 0 {
			out.SkippedNights++
			out.ByNight = append(out.ByNight, rn)
		}
	}
	out.MoonUpHours = math.Round(out.MoonUpHours*10) / 10
	out.SkippedHours = math.Round(out.SkippedHours*10) / 10
	out.IdleHours = math.Round(out.IdleHours*10) / 10
	out.WithoutGood.SkippedHours = math.Round(out.WithoutGood.SkippedHours*10) / 10
	worst := append([]ReplayNight(nil), out.ByNight...)
	sort.SliceStable(worst, func(i, j int) bool { return worst[i].KeptLost > worst[j].KeptLost })
	for _, w := range worst {
		if len(out.Worst) == 5 || w.KeptLost == 0 {
			break
		}
		out.Worst = append(out.Worst, w)
	}
	return out
}
