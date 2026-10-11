package planning

import (
	"context"

	"github.com/USA-RedDragon/astro-stacker/internal/goals"
	"github.com/USA-RedDragon/astro-stacker/internal/tslink"
	"gorm.io/gorm"
)

type FilterJudgement struct {
	Filter  string  `json:"filter"`
	Percent float64 `json:"percentComplete"`
	Basis   string  `json:"completionBasis"`
	Status  string  `json:"status"`
}

type Judgement struct {
	Project    string            `json:"project"`
	Target     string            `json:"target"`
	Percent    float64           `json:"percentComplete"`
	Done       bool              `json:"done"`
	GoalDriven bool              `json:"goalDriven"`
	Unmeasured int               `json:"unmeasured"`
	Filters    []FilterJudgement `json:"filters"`
	Least      Least             `json:"least"`
}

func AppInputs(ctx context.Context, appDB, sched *gorm.DB) (Inputs, error) {
	in := Inputs{Goals: map[goals.Key]goals.Goal{}, ObjectsByGUID: map[string][]string{}, Links: map[string]tslink.ObjectLink{}}
	if appDB == nil {
		return in, nil
	}
	gs, _, err := goals.LoadGoals(ctx, appDB, sched)
	if err != nil {
		return in, err
	}
	in.Goals = gs
	if byObject, err := goals.ObjectLinks(ctx, appDB, sched); err == nil {
		for obj, l := range byObject {
			in.ObjectsByGUID[l.TargetGUID] = append(in.ObjectsByGUID[l.TargetGUID], obj)
		}
		in.Links = byObject
	}
	return in, nil
}

func Judge(ctx context.Context, sched, appDB *gorm.DB) (map[int]map[string]Judgement, error) {
	in, err := AppInputs(ctx, appDB, sched)
	if err != nil {
		return nil, err
	}
	s, err := Load(ctx, sched, appDB, in)
	if err != nil {
		return nil, err
	}
	out := map[int]map[string]Judgement{}
	for _, p := range s.Projects {
		byTarget := map[string]Judgement{}
		for _, t := range p.Targets {
			j := Judgement{Project: p.Name, Target: t.Name, Percent: t.Percent, GoalDriven: t.Driven, Least: t.Least}
			enabled, complete := 0, true
			for _, pl := range t.Plans {
				if pl.Enabled {
					enabled++
					complete = complete && pl.Complete
				}
			}
			collecting := false
			for _, g := range t.Goals {
				j.Filters = append(j.Filters, FilterJudgement{Filter: g.Filter, Percent: g.Percent, Basis: g.Basis, Status: g.Status})
				waiting := g.Readiness != nil && g.Readiness.State == goals.StateCollecting
				collecting = collecting || waiting
				if t.Driven && (g.Status == StatusNotMeasured || waiting) {
					j.Unmeasured++
				}
			}
			j.Done = enabled > 0 && complete && !collecting
			byTarget[t.Name] = j
		}
		out[p.ID] = byTarget
	}
	return out, nil
}
