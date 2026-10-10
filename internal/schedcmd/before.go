package schedcmd

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"gorm.io/gorm"
)

type SkipState struct {
	Scope     string
	TargetID  int64
	ProjectID int64
	Until     *time.Time
}

type SchedulerState struct {
	State          string
	Paused         *bool
	PauseRequested *bool
	Skips          []SkipState
}

type PlanState struct {
	Template string
	Enabled  *bool
}

type Snapshot interface {
	SchedulerState() (SchedulerState, bool)
	TargetPlans(ctx context.Context, targetID int64) ([]PlanState, error)
}

func (s *Service) fillBefore(ctx context.Context, kind Kind, payload json.RawMessage, diffs []Diff) []Diff {
	if s.Snapshot == nil {
		return diffs
	}
	if kind == KindApplySet || kind == KindUnapplySet {
		for i := range diffs {
			if diffs[i].Object.Entity != entityTarget || diffs[i].Object.ID <= 0 {
				continue
			}
			if plans, err := s.Snapshot.TargetPlans(ctx, diffs[i].Object.ID); err == nil {
				diffs[i].Before = quote(plansText(plans))
			}
		}
		return diffs
	}
	if kind != KindSkip && kind != KindUnskip && kind != KindPause && kind != KindResume {
		return diffs
	}
	st, ok := s.Snapshot.SchedulerState()
	if !ok {
		return diffs
	}
	text := pauseState(st)
	if kind == KindSkip || kind == KindUnskip {
		v, err := decodeSkip(payload)
		if err != nil {
			return diffs
		}
		text = skipState(st, v)
	}
	for i := range diffs {
		diffs[i].Before = quote(text)
	}
	return diffs
}

func skipState(st SchedulerState, v SkipPayload) string {
	for _, k := range st.Skips {
		match := k.Scope == v.Scope && ((v.Scope == scopeTarget && k.TargetID == v.TargetID) || (v.Scope == scopeProject && k.ProjectID == v.ProjectID))
		if !match {
			continue
		}
		if k.Until != nil {
			return "Skipped until " + k.Until.UTC().Format("2006-01-02 15:04 UTC")
		}
		return fieldSkipped
	}
	return "No"
}

func pauseState(st SchedulerState) string {
	switch {
	case st.Paused != nil && *st.Paused:
		return "Paused"
	case st.PauseRequested != nil && *st.PauseRequested:
		return "Pause requested"
	case st.Paused == nil:
		if st.State != "" {
			return st.State
		}
		return "Not reported"
	case st.State != "":
		return "Not paused, " + st.State
	}
	return "Not paused"
}

func plansText(plans []PlanState) string {
	var on, off, unknown []string
	for _, p := range plans {
		switch {
		case p.Enabled == nil:
			unknown = append(unknown, p.Template)
		case *p.Enabled:
			on = append(on, p.Template)
		default:
			off = append(off, p.Template)
		}
	}
	parts := []string{}
	if len(on) > 0 {
		parts = append(parts, strings.Join(on, ", "))
	}
	if len(off) > 0 {
		parts = append(parts, "off: "+strings.Join(off, ", "))
	}
	if len(unknown) > 0 {
		parts = append(parts, "on/off not set: "+strings.Join(unknown, ", "))
	}
	if len(parts) == 0 {
		return "no plans"
	}
	return strings.Join(parts, "; ")
}

func TargetPlansFrom(ctx context.Context, sched *gorm.DB, targetID int64) ([]PlanState, error) {
	var rows []struct {
		Template *string
		Enabled  *int
	}
	if err := sched.WithContext(ctx).Table("exposureplan ep").
		Select(`et.name AS template, ep.enabled AS enabled`).
		Joins(`LEFT JOIN exposuretemplate et ON et."Id" = ep."exposureTemplateId"`).
		Where("ep.targetid = ?", targetID).Order(`ep."Id"`).Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]PlanState, 0, len(rows))
	for _, r := range rows {
		name := "unknown template"
		if r.Template != nil {
			name = *r.Template
		}
		var on *bool
		if r.Enabled != nil {
			v := *r.Enabled != 0
			on = &v
		}
		out = append(out, PlanState{Template: name, Enabled: on})
	}
	return out, nil
}
