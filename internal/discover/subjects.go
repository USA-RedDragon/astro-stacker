package discover

import (
	"context"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/catalog"
	"github.com/USA-RedDragon/astro-stacker/internal/goals"
)

const (
	SubjectProject = "project"
	SubjectObject  = "object"

	StateNone = -1
)

func StateName(state int) string {
	switch state {
	case 0:
		return "Draft"
	case 1:
		return "Active"
	case 2:
		return "Inactive"
	case 3:
		return "Closed"
	}
	return ""
}

type Subject struct {
	Key       string             `json:"key"`
	Name      string             `json:"name"`
	Kind      string             `json:"kind"`
	ProjectID int                `json:"projectId,omitempty"`
	State     int                `json:"state"`
	StateName string             `json:"stateName,omitempty"`
	Mosaic    bool               `json:"mosaic"`
	Targets   []string           `json:"targets"`
	RA        float64            `json:"ra"`
	Dec       float64            `json:"dec"`
	HasPos    bool               `json:"hasPosition"`
	Radius    float64            `json:"radius"`
	Hours     map[string]float64 `json:"hours"`
	Subs      int                `json:"subs"`
	Done      bool               `json:"done"`
	Measured  bool               `json:"measured"`
	LastNight *time.Time         `json:"lastNight,omitempty"`
}

func (s Subject) TotalHours() float64 {
	t := 0.0
	for _, h := range s.Hours {
		t += h
	}
	return t
}

func (s Subject) Scheduled() bool {
	return s.Kind == SubjectProject && s.State >= 0 && s.State <= 2
}

type tsRow struct {
	ID     int
	Name   string
	State  *int
	Mosaic *int
	Target *string
	RA     *float64
	Dec    *float64
}

type stackRow struct {
	Object           string
	Filter           string
	EffectiveSeconds float64
	Subs             int
}

type frameRow struct {
	Object   string
	RA       *float64
	RAWrap   *float64
	RAMin    *float64
	RAMax    *float64
	Dec      *float64
	LastSeen *string
}

func asTime(v any) *time.Time {
	switch t := v.(type) {
	case time.Time:
		return &t
	case *time.Time:
		return t
	case string:
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05.999999999", "2006-01-02 15:04:05", time.DateOnly} {
			if p, err := time.Parse(layout, t); err == nil {
				return &p
			}
		}
	case []byte:
		return asTime(string(t))
	}
	return nil
}

type point struct{ ra, dec float64 }

func centre(points []point) (float64, float64) {
	var x, y, z float64
	for _, p := range points {
		r, d := p.ra*math.Pi/180, p.dec*math.Pi/180
		x += math.Cos(d) * math.Cos(r)
		y += math.Cos(d) * math.Sin(r)
		z += math.Sin(d)
	}
	ra := math.Atan2(y, x) * 180 / math.Pi
	if ra < 0 {
		ra += 360
	}
	return ra, math.Atan2(z, math.Hypot(x, y)) * 180 / math.Pi
}

func (s *Service) loadSubjects(ctx context.Context) ([]Subject, error) {
	var ts []tsRow
	if s.SchedDB != nil {
		if err := s.SchedDB.WithContext(ctx).Table("project").
			Select(`project."Id" AS id, project.name AS name, project.state AS state, project."isMosaic" AS mosaic, target.name AS target, target.ra AS ra, target.dec AS dec`).
			Joins(`LEFT JOIN target ON target.projectid = project."Id"`).
			Order(`project."Id"`).Scan(&ts).Error; err != nil {
			return nil, fmt.Errorf("load scheduler projects: %w", err)
		}
	}
	var stacks []stackRow
	if err := s.AppDB.WithContext(ctx).Table("stacks").Select("object, filter, effective_seconds, subs").Scan(&stacks).Error; err != nil {
		return nil, fmt.Errorf("load stacks: %w", err)
	}
	var frames []frameRow
	if err := s.AppDB.WithContext(ctx).Table("frames").
		Select("object, AVG(mount_ra) AS ra, AVG(CASE WHEN mount_ra < 180 THEN mount_ra + 360 ELSE mount_ra END) AS ra_wrap, "+
			"MIN(mount_ra) AS ra_min, MAX(mount_ra) AS ra_max, AVG(mount_dec) AS dec, MAX(date_obs) AS last_seen").
		Where("type = ? AND object <> '' AND index_error IS NULL", "LIGHT").
		Group("object").Scan(&frames).Error; err != nil {
		return nil, fmt.Errorf("load light positions: %w", err)
	}
	progress, err := s.goalProgress(ctx, stacks)
	if err != nil {
		return nil, err
	}
	return buildSubjects(ts, stacks, frames, progress), nil
}

func (s *Service) goalProgress(ctx context.Context, stacks []stackRow) (map[goals.Key]goals.Progress, error) {
	keys := make([]goals.Key, 0, len(stacks))
	for _, st := range stacks {
		keys = append(keys, goals.Key{Object: st.Object, Filter: st.Filter})
	}
	if !s.AppDB.Migrator().HasTable("goal_measurements") {
		return map[goals.Key]goals.Progress{}, nil
	}
	return goals.Lookup(ctx, s.AppDB, nil, keys)
}

type objectData struct {
	hours    map[string]float64
	subs     int
	measured int
	done     int
	filters  int
	pos      *point
	last     *time.Time
}

func indexObjects(stacks []stackRow, frames []frameRow, progress map[goals.Key]goals.Progress) map[string]*objectData {
	out := map[string]*objectData{}
	get := func(name string) *objectData {
		d, ok := out[name]
		if !ok {
			d = &objectData{hours: map[string]float64{}}
			out[name] = d
		}
		return d
	}
	for _, st := range stacks {
		d := get(st.Object)
		d.hours[st.Filter] += st.EffectiveSeconds / 3600
		d.subs += st.Subs
		d.filters++
		if p, ok := progress[goals.Key{Object: st.Object, Filter: st.Filter}]; ok {
			d.measured++
			if p.Done {
				d.done++
			}
		}
	}
	for _, f := range frames {
		d := get(f.Object)
		if f.LastSeen != nil {
			d.last = asTime(*f.LastSeen)
		}
		if f.RA != nil && f.Dec != nil {
			ra := *f.RA
			if f.RAMin != nil && f.RAMax != nil && *f.RAMax-*f.RAMin > 180 && f.RAWrap != nil {
				ra = math.Mod(*f.RAWrap, 360)
			}
			d.pos = &point{ra, *f.Dec}
		}
	}
	return out
}

func buildSubjects(ts []tsRow, stacks []stackRow, frames []frameRow, progress map[goals.Key]goals.Progress) []Subject {
	objects := indexObjects(stacks, frames, progress)
	byProject := map[int]*Subject{}
	points := map[int][]point{}
	var order []int
	inTS := map[string]bool{}
	names := map[string]int{}
	for _, r := range ts {
		subj, ok := byProject[r.ID]
		if !ok {
			key := SubjectProject + ":" + r.Name
			names[key]++
			if names[key] > 1 {
				key = fmt.Sprintf("%s#%d", key, r.ID)
			}
			state := StateNone
			if r.State != nil {
				state = *r.State
			}
			subj = &Subject{Key: key, Name: r.Name, Kind: SubjectProject, ProjectID: r.ID, State: state, StateName: StateName(state),
				Mosaic: r.Mosaic != nil && *r.Mosaic != 0, Hours: map[string]float64{}}
			byProject[r.ID] = subj
			order = append(order, r.ID)
		}
		if r.Target == nil {
			continue
		}
		subj.Targets = append(subj.Targets, *r.Target)
		inTS[*r.Target] = true
		if r.RA != nil && r.Dec != nil {
			points[r.ID] = append(points[r.ID], point{*r.RA * 15, *r.Dec})
		}
	}
	out := make([]Subject, 0, len(order))
	for _, id := range order {
		subj := byProject[id]
		if len(subj.Targets) > 1 {
			subj.Mosaic = true
		}
		var pts []point
		measured, done, filters := 0, 0, 0
		for _, t := range subj.Targets {
			d := objects[t]
			if d == nil {
				continue
			}
			addObject(subj, d)
			measured += d.measured
			done += d.done
			filters += d.filters
		}
		pts = append(pts, points[id]...)
		if len(pts) == 0 {
			for _, t := range subj.Targets {
				if d := objects[t]; d != nil && d.pos != nil {
					pts = append(pts, *d.pos)
				}
			}
		}
		place(subj, pts)
		subj.Measured = measured > 0
		subj.Done = filters > 0 && measured == filters && done == measured
		out = append(out, *subj)
	}
	return append(out, objectSubjects(objects, inTS)...)
}

func objectSubjects(objects map[string]*objectData, inTS map[string]bool) []Subject {
	var out []Subject
	groups := map[string]*Subject{}
	groupPts := map[string][]point{}
	var gorder []string
	objNames := make([]string, 0, len(objects))
	for name := range objects {
		objNames = append(objNames, name)
	}
	slices.Sort(objNames)
	for _, name := range objNames {
		if inTS[name] {
			continue
		}
		d := objects[name]
		g := catalog.StripPanel(name)
		subj, ok := groups[g]
		if !ok {
			subj = &Subject{Key: SubjectObject + ":" + g, Name: g, Kind: SubjectObject, State: StateNone, Hours: map[string]float64{}}
			groups[g] = subj
			gorder = append(gorder, g)
		}
		subj.Targets = append(subj.Targets, name)
		addObject(subj, d)
		if d.pos != nil {
			groupPts[g] = append(groupPts[g], *d.pos)
		}
		if d.filters > 0 && d.measured == d.filters && d.done == d.measured {
			subj.Done = true
		}
		subj.Measured = subj.Measured || d.measured > 0
	}
	for _, g := range gorder {
		subj := groups[g]
		subj.Mosaic = len(subj.Targets) > 1
		place(subj, groupPts[g])
		if subj.TotalHours() == 0 && subj.Subs == 0 && !subj.HasPos {
			continue
		}
		out = append(out, *subj)
	}
	return out
}

func addObject(subj *Subject, d *objectData) {
	for f, h := range d.hours {
		subj.Hours[f] += h
	}
	subj.Subs += d.subs
	if d.last != nil && (subj.LastNight == nil || d.last.After(*subj.LastNight)) {
		l := *d.last
		subj.LastNight = &l
	}
}

func place(subj *Subject, pts []point) {
	if len(pts) == 0 {
		return
	}
	subj.RA, subj.Dec = centre(pts)
	subj.HasPos = true
	for _, p := range pts {
		subj.Radius = math.Max(subj.Radius, catalog.Separation(subj.RA, subj.Dec, p.ra, p.dec))
	}
}
