package planning

import (
	"context"

	"github.com/USA-RedDragon/astro-stacker/internal/goals"
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
}

func AppInputs(ctx context.Context, appDB, sched *gorm.DB) (Inputs, error) {
	in := Inputs{Goals: map[goals.Key]goals.Goal{}, ObjectsByGUID: map[string][]string{}}
	if appDB == nil {
		return in, nil
	}
	gs, _, err := goals.LoadGoals(ctx, appDB, sched)
	if err != nil {
		return in, err
	}
	in.Goals = gs
	if byObject, err := goals.ObjectGUIDs(ctx, appDB, sched); err == nil {
		for obj, guid := range byObject {
			in.ObjectsByGUID[guid] = append(in.ObjectsByGUID[guid], obj)
		}
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
			j := Judgement{Project: p.Name, Target: t.Name, Percent: t.Percent, GoalDriven: t.Driven}
			enabled := 0
			for _, pl := range t.Plans {
				if pl.Enabled {
					enabled++
				}
			}
			j.Done = enabled > 0 && t.Percent >= 1
			for _, g := range t.Goals {
				j.Filters = append(j.Filters, FilterJudgement{Filter: g.Filter, Percent: g.Percent, Basis: g.Basis, Status: g.Status})
				if t.Driven && g.Status == StatusNotMeasured {
					j.Unmeasured++
				}
			}
			byTarget[t.Name] = j
		}
		out[p.ID] = byTarget
	}
	return out, nil
}
