package mosaicplan

import (
	"context"
	"fmt"
	"math"
	"slices"
	"sync"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/mosaics"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

const (
	PaceLast      = "last"
	PaceGood      = "good"
	PacePoor      = "poor"
	goodSeason    = 60.0
	poorSeason    = 20.0
	minPace       = 20.0
	maxSeasons    = 8
	usableMonthly = 2.0
)

type SiteSource func(ctx context.Context) (mosaics.Site, bool)

type SeasonPace struct {
	Hours  float64    `json:"hours"`
	Nights int        `json:"nights"`
	From   *time.Time `json:"from,omitempty"`
	To     *time.Time `json:"to,omitempty"`
	Start  time.Time  `json:"seasonStart"`
	End    time.Time  `json:"seasonEnd"`
}

type Month struct {
	Month int     `json:"month"`
	Name  string  `json:"name"`
	Hours float64 `json:"hours"`
}

type SeasonRow struct {
	Index    int     `json:"index"`
	Name     string  `json:"name"`
	Weakest  float64 `json:"weakest"`
	Average  float64 `json:"average"`
	Done     bool    `json:"done"`
	Strategy string  `json:"strategy"`
}

type SeasonPlan struct {
	Project         string                `json:"project"`
	Strategy        string                `json:"strategy"`
	Pace            string                `json:"pace"`
	HoursPerSeason  float64               `json:"hoursPerSeason"`
	Last            *SeasonPace           `json:"lastSeason,omitempty"`
	Current         *SeasonPace           `json:"currentSeason,omitempty"`
	InSeason        bool                  `json:"inSeason"`
	NightsLeft      int                   `json:"nightsLeft"`
	Rows            []SeasonRow           `json:"rows"`
	FinishSeason    int                   `json:"finishSeason"`
	Compare         map[string]int        `json:"compare"`
	Months          []Month               `json:"months"`
	SiteKnown       bool                  `json:"siteKnown"`
	Items           []mosaics.Item        `json:"items"`
	GoalHoursSource string                `json:"goalHoursSource"`
	PanelPriority   []PanelSeasonPriority `json:"panelPriority"`
}

type PanelSeasonPriority struct {
	Panel      int     `json:"panel"`
	HoursLeft  float64 `json:"hoursLeft"`
	MonthsLeft int     `json:"monthsLeft"`
	Priority   float64 `json:"priority"`
	LastUsable string  `json:"lastUsableMonth,omitempty"`
	ThisSeason bool    `json:"thisSeason"`
}

type siteCache struct {
	mu   sync.Mutex
	site *mosaics.Site
}

func CachedSite(f SiteSource) SiteSource {
	c := &siteCache{}
	return func(ctx context.Context) (mosaics.Site, bool) {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.site != nil {
			return *c.site, true
		}
		s, ok := f(ctx)
		if ok {
			c.site = &s
		}
		return s, ok
	}
}

func seasonName(start time.Time) string {
	end := start.AddDate(1, 0, 0)
	return fmt.Sprintf("%d–%02d", start.Year(), end.Year()%100)
}

func (s *Service) Seasons(ctx context.Context, key, strategy, pace string, now time.Time) (SeasonPlan, error) {
	d, err := s.Detail(ctx, key)
	if err != nil {
		return SeasonPlan{}, err
	}
	switch mosaics.Strategy(strategy) {
	case mosaics.StrategyWeakest, mosaics.StrategyEven, mosaics.StrategyOff:
	default:
		strategy = string(mosaics.StrategyWeakest)
	}
	plan := SeasonPlan{Project: d.Project, Strategy: strategy, Pace: pace, Compare: map[string]int{}, Months: []Month{}, Rows: []SeasonRow{}}
	plan.Items, plan.GoalHoursSource = seasonItems(d)
	centres := make([]mosaics.Point, len(d.Panels))
	var meanRA float64
	var sx, sy float64
	for i, p := range d.Panels {
		centres[i] = mosaics.Point{RA: p.RA, Dec: p.Dec}
		sx, sy = sx+math.Cos(p.RA*math.Pi/180), sy+math.Sin(p.RA*math.Pi/180)
	}
	meanRA = math.Mod(math.Atan2(sy, sx)*180/math.Pi+360, 360)
	start, end := mosaics.Season(now, meanRA)
	seasons, err := s.seasonPaces(ctx, d, meanRA)
	if err != nil {
		return plan, err
	}
	for i := range seasons {
		sp := seasons[i]
		if !now.Before(sp.Start) && now.Before(sp.End) {
			plan.Current = &sp
		} else if sp.End.Before(now) || sp.End.Equal(now) {
			if plan.Last == nil || sp.Start.After(plan.Last.Start) {
				plan.Last = &sp
			}
		}
	}
	if plan.Current == nil {
		plan.Current = &SeasonPace{Start: start, End: end}
	}
	switch pace {
	case PaceGood:
		plan.HoursPerSeason = goodSeason
	case PacePoor:
		plan.HoursPerSeason = poorSeason
	default:
		plan.Pace = PaceLast
		plan.HoursPerSeason = minPace
		if plan.Last != nil {
			plan.HoursPerSeason = math.Max(minPace, plan.Last.Hours)
		} else if plan.Current.Hours > 0 {
			plan.HoursPerSeason = math.Max(minPace, plan.Current.Hours)
		}
	}
	var months [12]float64
	if s.Site != nil {
		if site, ok := s.Site(ctx); ok {
			plan.SiteKnown = true
			months = mosaics.MonthlyDarkHours(now.Year(), site, centres, d.TS.MinAltitude)
			for m, h := range months {
				plan.Months = append(plan.Months, Month{Month: m + 1, Name: time.Month(m + 1).String()[:3], Hours: math.Round(h*10) / 10})
			}
			plan.InSeason = months[int(now.Month())-1] >= usableMonthly
			plan.NightsLeft = nightsLeft(now, months)
		}
	}
	rows, finish := mosaics.Simulate(plan.Items, mosaics.Strategy(strategy), plan.HoursPerSeason, maxSeasons)
	for _, r := range rows {
		plan.Rows = append(plan.Rows, SeasonRow{Index: r.Index, Name: seasonName(start.AddDate(r.Index-1, 0, 0)), Weakest: r.WeakestProgress,
			Average: r.AverageProgress, Done: r.Done, Strategy: strategy})
	}
	plan.FinishSeason = finish
	for _, st := range []mosaics.Strategy{mosaics.StrategyWeakest, mosaics.StrategyEven, mosaics.StrategyOff} {
		_, f := mosaics.Simulate(plan.Items, st, plan.HoursPerSeason, maxSeasons)
		plan.Compare[string(st)] = f
	}
	plan.PanelPriority = s.panelPriorities(ctx, d, now)
	return plan, nil
}

func nightsLeft(now time.Time, months [12]float64) int {
	n := 0
	for i := range 12 {
		t := now.AddDate(0, i, 0)
		if months[int(t.Month())-1] < usableMonthly {
			break
		}
		days := 30
		if i == 0 {
			days = 31 - now.Day()
		}
		n += days
	}
	return n
}

func seasonItems(d Detail) ([]mosaics.Item, string) {
	src := SourceGoal
	var items []mosaics.Item
	for _, p := range d.Panels {
		for _, f := range p.Filters {
			it := mosaics.Item{Panel: p.Number, Filter: f.Filter, DoneHours: f.EffectiveHours}
			switch {
			case f.Done:
				it.GoalHours = f.EffectiveHours
			case f.HoursNeeded >= 0:
				it.GoalHours = f.EffectiveHours + f.HoursNeeded
				if f.Source != SourceGoal {
					src = SourceTS
				}
			case f.PlannedHours > 0:
				it.GoalHours = math.Max(f.PlannedHours, f.EffectiveHours)
				src = SourceTS
			default:
				continue
			}
			if it.GoalHours <= 0 {
				it.GoalHours = it.DoneHours
			}
			items = append(items, it)
		}
	}
	if items == nil {
		items = []mosaics.Item{}
	}
	return items, src
}

func (s *Service) seasonPaces(ctx context.Context, d Detail, ra float64) ([]SeasonPace, error) {
	var objects []string
	for _, p := range d.Panels {
		objects = append(objects, p.Objects...)
	}
	if len(objects) == 0 {
		return nil, nil
	}
	var nights []struct {
		Night     time.Time
		Effective float64
	}
	if err := s.App.WithContext(ctx).Table("stack_frames sf").
		Select("f.night AS night, COALESCE(SUM(sf.score*sf.exposure),0) AS effective").
		Joins("JOIN frames f ON f.id = sf.frame_id").
		Where("sf.status = ? AND f.object IN ? AND f.night IS NOT NULL", app.StackStatusAdded, objects).
		Group("f.night").Order("f.night").Scan(&nights).Error; err != nil {
		return nil, fmt.Errorf("load nights: %w", err)
	}
	var out []SeasonPace
	for _, n := range nights {
		start, end := mosaics.Season(n.Night, ra)
		k := slices.IndexFunc(out, func(sp SeasonPace) bool { return !n.Night.Before(sp.Start) && n.Night.Before(sp.End) })
		if k < 0 {
			out = append(out, SeasonPace{Start: start, End: end})
			k = len(out) - 1
		}
		sp := &out[k]
		sp.Hours += n.Effective / 3600
		sp.Nights++
		night := n.Night
		if sp.From == nil {
			sp.From = &night
		}
		sp.To = &night
	}
	return out, nil
}

func (s *Service) panelPriorities(ctx context.Context, d Detail, now time.Time) []PanelSeasonPriority {
	out := make([]PanelSeasonPriority, 0, len(d.Panels))
	var site mosaics.Site
	known := false
	if s.Site != nil {
		site, known = s.Site(ctx)
	}
	for _, p := range d.Panels {
		pp := PanelSeasonPriority{Panel: p.Number}
		for _, f := range p.Filters {
			if f.HoursNeeded > 0 {
				pp.HoursLeft += f.HoursNeeded
			}
		}
		if known {
			months := mosaics.MonthlyDarkHours(now.Year(), site, []mosaics.Point{{RA: p.RA, Dec: p.Dec}}, d.TS.MinAltitude)
			for i := range 12 {
				t := now.AddDate(0, i, 0)
				if months[int(t.Month())-1] < usableMonthly {
					break
				}
				pp.MonthsLeft++
				pp.LastUsable = t.Month().String()[:3]
			}
		}
		pp.ThisSeason = pp.HoursLeft > 0 && pp.MonthsLeft > 0
		if pp.ThisSeason {
			pp.Priority = (1 - p.Progress) / float64(pp.MonthsLeft)
		}
		out = append(out, pp)
	}
	slices.SortFunc(out, func(a, b PanelSeasonPriority) int {
		switch {
		case a.Priority > b.Priority:
			return -1
		case a.Priority < b.Priority:
			return 1
		}
		return a.Panel - b.Panel
	})
	return out
}
