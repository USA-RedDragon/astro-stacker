package goals

import (
	"context"
	"fmt"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/store/tables"
	"gorm.io/gorm"
)

const (
	StateMeasured     = "measured"
	StateCollecting   = "collecting"
	StateUnmeasurable = "unmeasurable"

	UnmeasurableSubLimit = 4 * MinSubs

	ColumnState     = "state"
	ColumnReason    = "reason"
	ColumnStackSubs = "stack_subs"
	ColumnMinSubs   = "min_subs"
	ColumnSubLimit  = "sub_limit"

	ReasonNoMaster    = "no master yet"
	ReasonNotMeasured = "not measured yet"
)

type Readiness struct {
	State     string `json:"state"`
	Reason    string `json:"reason,omitempty"`
	StackSubs int    `json:"stackSubs"`
	MinSubs   int    `json:"minSubs"`
	SubLimit  int    `json:"subLimit"`
	Open      bool   `json:"open"`
}

func Ready(m *app.GoalMeasurement, p *Progress, stackSubs int) Readiness {
	r := Readiness{StackSubs: stackSubs, MinSubs: MinSubs, SubLimit: UnmeasurableSubLimit}
	switch {
	case m == nil || p == nil:
		r.State = StateCollecting
		r.Reason = ReasonNotMeasured
		if stackSubs == 0 {
			r.Reason = ReasonNoMaster
		}
		if m != nil && m.Error != nil {
			r.Reason = *m.Error
		}
		r.Open = stackSubs < r.MinSubs
	case p.Done:
		r.State = StateMeasured
	case p.Unmeasured != "":
		r.State, r.Reason = StateUnmeasurable, p.Unmeasured
		r.Open = stackSubs < r.SubLimit
	default:
		r.State, r.Open = StateMeasured, true
	}
	return r
}

func HasReadinessColumns(ctx context.Context, sched *gorm.DB) (bool, error) {
	for _, c := range []string{ColumnState, ColumnReason, ColumnStackSubs, ColumnMinSubs, ColumnSubLimit} {
		if has, err := tables.HasColumn(ctx, sched, ProgressTable, c); err != nil || !has {
			return false, err
		}
	}
	return true, nil
}

func MeasurableSubs(ctx context.Context, appDB *gorm.DB) (map[Key]int, error) {
	out := map[Key]int{}
	for _, t := range []any{&app.Stack{}, &app.StackFrame{}} {
		if has, err := tables.Has(ctx, appDB, t); err != nil || !has {
			return out, err
		}
	}
	var rows []struct {
		Object string
		Filter string
		N      int
	}
	if err := appDB.WithContext(ctx).Table("stack_frames sf").
		Select("s.object AS object, s.filter AS filter, count(*) AS n").
		Joins("JOIN stacks s ON s.id = sf.stack_id").
		Where("sf.status = ? AND sf.registered_key IS NOT NULL AND sf.weight > 0 AND sf.exposure > 0", app.StackStatusAdded).
		Group("s.object, s.filter").Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("count measurable subs: %w", err)
	}
	for _, r := range rows {
		out[Key{Object: r.Object, Filter: r.Filter}] = r.N
	}
	return out, nil
}
