package goals

import (
	"context"
	"fmt"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
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

func HasReadinessColumns(sched *gorm.DB) bool {
	m := sched.Migrator()
	for _, c := range []string{ColumnState, ColumnReason, ColumnStackSubs, ColumnMinSubs, ColumnSubLimit} {
		if !m.HasColumn(ProgressTable, c) {
			return false
		}
	}
	return true
}

func MeasurableSubs(ctx context.Context, appDB *gorm.DB) (map[Key]int, error) {
	out := map[Key]int{}
	m := appDB.Migrator()
	if !m.HasTable(&app.Stack{}) || !m.HasTable(&app.StackFrame{}) {
		return out, nil
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
