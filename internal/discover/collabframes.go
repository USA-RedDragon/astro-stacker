package discover

import (
	"cmp"
	"context"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/starfront"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/store/tables"
)

type CollabFilter struct {
	Filter   string  `json:"filter"`
	Exposure float64 `json:"exposure"`
	Frames   int     `json:"frames"`
}

type CollabStacked struct {
	Filter string `json:"filter"`
	Frames int    `json:"frames"`
}

type CollabTarget struct {
	Object  string          `json:"object"`
	Panel   *int            `json:"panel"`
	Frames  int             `json:"frames"`
	Stacked []CollabStacked `json:"stacked"`
}

type targetKey struct {
	object string
	panel  int
}

func keyOf(object string, panel *int) targetKey {
	k := targetKey{object: object}
	if panel != nil {
		k.panel = *panel
	}
	return k
}

type CollabFrames struct {
	Project    string         `json:"project"`
	Name       string         `json:"name,omitempty"`
	Nights     int            `json:"nights"`
	FirstNight string         `json:"firstNight"`
	LastNight  string         `json:"lastNight"`
	Frames     int            `json:"frames"`
	Hours      float64        `json:"hours"`
	Filters    []CollabFilter `json:"filters"`
	Targets    []CollabTarget `json:"targets"`
}

type collabFrameRow struct {
	CollabProject string
	CollabPanel   *int
	Object        string
	Filter        string
	Exposure      *float64
	Night         *time.Time
}

func (s *Service) collabFrames(ctx context.Context, projects []starfront.Project) ([]CollabFrames, error) {
	out := []CollabFrames{}
	if s.AppDB == nil {
		return out, nil
	}
	db := s.AppDB.WithContext(ctx)
	if has, err := tables.Has(ctx, db, &app.Frame{}); err != nil || !has {
		return out, err
	}
	var rows []collabFrameRow
	if err := db.Model(&app.Frame{}).Select("collab_project, collab_panel, object, filter, exposure, night").
		Where("type = ? AND index_error IS NULL AND collab_project IS NOT NULL AND collab_project <> ''", "LIGHT").
		Scan(&rows).Error; err != nil {
		return out, err
	}
	if len(rows) == 0 {
		return out, nil
	}
	names := map[string]string{}
	for _, p := range projects {
		names[p.ID] = p.Name
	}
	type acc struct {
		f       CollabFrames
		nights  map[string]bool
		filters map[CollabFilter]int
		targets map[targetKey]*CollabTarget
		seconds float64
	}
	by := map[string]*acc{}
	for _, r := range rows {
		a := by[r.CollabProject]
		if a == nil {
			a = &acc{f: CollabFrames{Project: r.CollabProject, Name: names[r.CollabProject]}, nights: map[string]bool{}, filters: map[CollabFilter]int{}, targets: map[targetKey]*CollabTarget{}}
			by[r.CollabProject] = a
		}
		a.f.Frames++
		exp := 0.0
		if r.Exposure != nil {
			exp = *r.Exposure
			a.seconds += exp
		}
		a.filters[CollabFilter{Filter: r.Filter, Exposure: exp}]++
		if r.Night != nil {
			a.nights[r.Night.Format(time.DateOnly)] = true
		}
		k := keyOf(r.Object, r.CollabPanel)
		t := a.targets[k]
		if t == nil {
			t = &CollabTarget{Object: r.Object, Panel: r.CollabPanel, Stacked: []CollabStacked{}}
			a.targets[k] = t
		}
		t.Frames++
	}
	stacked, err := s.collabStacked(ctx)
	if err != nil {
		return out, err
	}
	for _, a := range by {
		nights := keys(a.nights)
		a.f.Nights = len(nights)
		if len(nights) > 0 {
			a.f.FirstNight, a.f.LastNight = nights[0], nights[len(nights)-1]
		}
		a.f.Hours = math.Round(a.seconds/36) / 100
		for k, n := range a.filters {
			k.Frames = n
			a.f.Filters = append(a.f.Filters, k)
		}
		slices.SortFunc(a.f.Filters, func(x, y CollabFilter) int {
			if c := strings.Compare(x.Filter, y.Filter); c != 0 {
				return c
			}
			return cmp.Compare(x.Exposure, y.Exposure)
		})
		for k, t := range a.targets {
			if f := stacked[collabKey{project: a.f.Project, target: k}]; f != nil {
				t.Stacked = f
			}
			a.f.Targets = append(a.f.Targets, *t)
		}
		slices.SortFunc(a.f.Targets, func(x, y CollabTarget) int {
			if c := strings.Compare(x.Object, y.Object); c != 0 {
				return c
			}
			return cmp.Compare(panelOf(x.Panel), panelOf(y.Panel))
		})
		out = append(out, a.f)
	}
	slices.SortFunc(out, func(x, y CollabFrames) int {
		if c := strings.Compare(y.LastNight, x.LastNight); c != 0 {
			return c
		}
		return strings.Compare(x.Project, y.Project)
	})
	return out, nil
}

type collabKey struct {
	project string
	target  targetKey
}

func panelOf(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func (s *Service) collabStacked(ctx context.Context) (map[collabKey][]CollabStacked, error) {
	out := map[collabKey][]CollabStacked{}
	db := s.AppDB.WithContext(ctx)
	for _, m := range []any{&app.Stack{}, &app.StackFrame{}} {
		if has, err := tables.Has(ctx, db, m); err != nil || !has {
			return out, err
		}
	}
	var rows []struct {
		CollabProject string
		CollabPanel   *int
		Object        string
		Filter        string
		Frames        int
	}
	if err := db.Table("stack_frames sf").
		Select("f.collab_project, f.collab_panel, f.object, s.filter, COUNT(*) AS frames").
		Joins("JOIN frames f ON f.id = sf.frame_id").
		Joins("JOIN stacks s ON s.id = sf.stack_id").
		Where("f.collab_project IS NOT NULL AND f.collab_project <> '' AND sf.status = ?", app.StackStatusAdded).
		Group("f.collab_project, f.collab_panel, f.object, s.filter").Order("s.filter").Scan(&rows).Error; err != nil {
		return out, err
	}
	for _, r := range rows {
		k := collabKey{project: r.CollabProject, target: keyOf(r.Object, r.CollabPanel)}
		out[k] = append(out[k], CollabStacked{Filter: r.Filter, Frames: r.Frames})
	}
	return out, nil
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
