package discover

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
)

const SubjectAdding = "adding"

func Affects(r schedcmd.Record) bool {
	return r.Category == schedcmd.CategoryMatching || r.Kind == schedcmd.KindProjectCreate || r.Kind == schedcmd.KindProjectDelete
}

type Adding struct {
	CommandID string     `json:"commandId"`
	Status    string     `json:"status"`
	AppliesAt *time.Time `json:"appliesAt,omitempty"`
	Message   string     `json:"message,omitempty"`
}

type createPayload struct {
	Project struct {
		GUID string `json:"guid"`
		Name string `json:"name"`
	} `json:"project"`
	Targets []struct {
		Name     string   `json:"name"`
		RAHours  *float64 `json:"ra_hours"`
		Dec      *float64 `json:"dec"`
		Rotation float64  `json:"rotation"`
	} `json:"targets"`
	Catalog string `json:"catalog"`
}

func (s *Service) addingSubjects(ctx context.Context, frame sky.Frame, existing map[string]bool) ([]Subject, error) {
	if !s.AppDB.Migrator().HasTable(&schedcmd.Record{}) {
		return nil, nil
	}
	var recs []schedcmd.Record
	if err := s.AppDB.WithContext(ctx).Where("kind IN ? AND status IN ?", []schedcmd.Kind{schedcmd.KindProjectCreate, schedcmd.KindProjectDelete},
		[]schedcmd.Status{schedcmd.StatusQueued, schedcmd.StatusPending, schedcmd.StatusApplied}).Order("created_at").Find(&recs).Error; err != nil {
		return nil, fmt.Errorf("load project creates: %w", err)
	}
	payloads := make([]createPayload, len(recs))
	deleted := map[string]time.Time{}
	for i, r := range recs {
		if json.Unmarshal(r.Payload, &payloads[i]) != nil {
			continue
		}
		if r.Kind == schedcmd.KindProjectDelete && r.Status == schedcmd.StatusApplied {
			deleted[payloads[i].Project.GUID] = r.CreatedAt
		}
	}
	var out []Subject
	for i, r := range recs {
		p := payloads[i]
		if r.Kind != schedcmd.KindProjectCreate || p.Project.Name == "" || r.UndoneBy != "" || existing[p.Project.GUID] {
			continue
		}
		if at, ok := deleted[p.Project.GUID]; ok && at.After(r.CreatedAt) {
			continue
		}
		if r.Status == schedcmd.StatusApplied && p.Project.GUID == "" {
			continue
		}
		if r.Status == schedcmd.StatusApplied {
			r.Message = "Target Scheduler has it; the scheduler database has not synced it yet."
		}
		subj := Subject{Key: SubjectAdding + ":" + r.ID, Name: p.Project.Name, Kind: SubjectAdding, State: StateNone, Hours: map[string]float64{},
			Adding: &Adding{CommandID: r.ID, Status: string(r.Status), AppliesAt: r.AppliesAt, Message: r.Message}}
		if p.Catalog != "" {
			subj.extraNames = []string{p.Catalog}
		}
		var pts []point
		for _, t := range p.Targets {
			subj.Targets = append(subj.Targets, t.Name)
			if t.RAHours == nil || t.Dec == nil {
				continue
			}
			pts = append(pts, point{*t.RAHours * 15, *t.Dec})
			if f, ok := planField(t.Name, *t.RAHours*15, *t.Dec, t.Rotation, frame); ok {
				subj.planned = append(subj.planned, f)
			}
		}
		subj.Mosaic = len(subj.Targets) > 1
		place(&subj, pts)
		subj.Completion, subj.Basis = CompletionAdding, addingText(subj.Adding)
		subj.Footprint = FieldPlan
		out = append(out, subj)
	}
	return out, nil
}

func addingText(a *Adding) string {
	out := "Being added to Target Scheduler; the command is " + a.Status + "."
	if a.Message != "" {
		out += " " + a.Message
	}
	return out
}
