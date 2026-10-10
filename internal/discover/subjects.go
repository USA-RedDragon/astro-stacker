package discover

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/catalog"
	"github.com/USA-RedDragon/astro-stacker/internal/goals"
	"github.com/USA-RedDragon/astro-stacker/internal/planning"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
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
	Key         string             `json:"key"`
	Name        string             `json:"name"`
	Kind        string             `json:"kind"`
	ProjectID   int                `json:"projectId,omitempty"`
	State       int                `json:"state"`
	StateName   string             `json:"stateName,omitempty"`
	Mosaic      bool               `json:"mosaic"`
	Targets     []string           `json:"targets"`
	RA          float64            `json:"ra"`
	Dec         float64            `json:"dec"`
	HasPos      bool               `json:"hasPosition"`
	Radius      float64            `json:"radius"`
	Hours       map[string]float64 `json:"hours"`
	Subs        int                `json:"subs"`
	Done        bool               `json:"done"`
	Completion  string             `json:"completion"`
	Basis       string             `json:"completionBasis"`
	Measured    bool               `json:"measured"`
	Tally       Tally              `json:"tally"`
	MinAltitude *float64           `json:"minAltitude,omitempty"`
	LastNight   *time.Time         `json:"lastNight,omitempty"`
	Footprint   string             `json:"footprint"`

	NotCatalogue string `json:"notCatalogue,omitempty"`

	imaged  []field
	planned []field
	judged  map[string]planning.Judgement
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
	ID       int
	Name     string
	State    *int
	Mosaic   *int
	Target   *string
	RA       *float64
	Dec      *float64
	Rotation *float64
	MinAlt   *float64
}

type stackRow struct {
	Object           string
	Filter           string
	EffectiveSeconds float64
	Subs             int
	Width            int
	Height           int
	CropX            *int
	CropY            *int
	CropW            *int
	CropH            *int
}

type refRow struct {
	Object string
	WCS    string
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

func (s *Service) loadSubjects(ctx context.Context, frame sky.Frame) ([]Subject, map[string]*objectData, error) {
	var ts []tsRow
	judged := map[int]map[string]planning.Judgement{}
	if s.SchedDB != nil {
		rotation, minAlt := "NULL", "NULL"
		if s.SchedDB.Migrator().HasColumn("target", "rotation") {
			rotation = "target.rotation"
		}
		if s.SchedDB.Migrator().HasColumn("project", "minimumaltitude") {
			minAlt = "project.minimumaltitude"
		}
		if err := s.SchedDB.WithContext(ctx).Table("project").
			Select(`project."Id" AS id, project.name AS name, project.state AS state, project."isMosaic" AS mosaic, target.name AS target, target.ra AS ra, target.dec AS dec, ` + rotation + ` AS rotation, ` + minAlt + ` AS min_alt`).
			Joins(`LEFT JOIN target ON target.projectid = project."Id"`).
			Order(`project."Id"`).Scan(&ts).Error; err != nil {
			return nil, nil, fmt.Errorf("load scheduler projects: %w", err)
		}
		var err error
		if judged, err = planning.Judge(ctx, s.SchedDB, s.AppDB); err != nil {
			slog.Warn("Could not judge catalogue completion the way Target Scheduler does", "error", err)
			judged = map[int]map[string]planning.Judgement{}
		}
	}
	var stacks []stackRow
	if err := s.AppDB.WithContext(ctx).Table("stacks").Select("object, filter, effective_seconds, subs, width, height, crop_x, crop_y, crop_w, crop_h").
		Scan(&stacks).Error; err != nil {
		return nil, nil, fmt.Errorf("load stacks: %w", err)
	}
	var frames []frameRow
	if err := s.AppDB.WithContext(ctx).Table("frames").
		Select("object, AVG(mount_ra) AS ra, AVG(CASE WHEN mount_ra < 180 THEN mount_ra + 360 ELSE mount_ra END) AS ra_wrap, "+
			"MIN(mount_ra) AS ra_min, MAX(mount_ra) AS ra_max, AVG(mount_dec) AS dec, MAX(date_obs) AS last_seen").
		Where("type = ? AND object <> '' AND index_error IS NULL", "LIGHT").
		Group("object").Scan(&frames).Error; err != nil {
		return nil, nil, fmt.Errorf("load light positions: %w", err)
	}
	var refs []refRow
	if s.AppDB.Migrator().HasTable("target_references") {
		if err := s.AppDB.WithContext(ctx).Table("target_references").Select("object, wcs").Where("wcs IS NOT NULL AND wcs <> ''").
			Scan(&refs).Error; err != nil {
			return nil, nil, fmt.Errorf("load plate solutions: %w", err)
		}
	}
	measured, short, err := s.measuredKeys(ctx)
	if err != nil {
		return nil, nil, err
	}
	objects := indexObjects(stacks, frames, refs, measured, short)
	return buildSubjects(ts, objects, judged, frame), objects, nil
}

func (s *Service) measuredKeys(ctx context.Context) (map[goals.Key]bool, map[goals.Key]bool, error) {
	measured, short := map[goals.Key]bool{}, map[goals.Key]bool{}
	if !s.AppDB.Migrator().HasTable("goal_measurements") {
		return measured, short, nil
	}
	var rows []struct {
		Object string
		Filter string
		Failed bool
	}
	if err := s.AppDB.WithContext(ctx).Table("goal_measurements").Select("object, filter, error IS NOT NULL AS failed").Scan(&rows).Error; err != nil {
		return nil, nil, fmt.Errorf("load goal measurements: %w", err)
	}
	for _, r := range rows {
		k := goals.Key{Object: r.Object, Filter: r.Filter}
		measured[k] = true
		short[k] = r.Failed
	}
	return measured, short, nil
}

type objectData struct {
	hours    map[string]float64
	subs     int
	measured int
	short    int
	filters  int
	pos      *point
	last     *time.Time
	fields   []field
}

func cropOf(st stackRow) *[4]int {
	if st.CropX == nil || st.CropY == nil || st.CropW == nil || st.CropH == nil || *st.CropW <= 0 || *st.CropH <= 0 {
		return nil
	}
	return &[4]int{*st.CropX, *st.CropY, *st.CropW, *st.CropH}
}

func unionCrop(a, b *[4]int) *[4]int {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	x0, y0 := min(a[0], b[0]), min(a[1], b[1])
	x1, y1 := max(a[0]+a[2], b[0]+b[2]), max(a[1]+a[3], b[1]+b[3])
	return &[4]int{x0, y0, x1 - x0, y1 - y0}
}

func indexObjects(stacks []stackRow, frames []frameRow, refs []refRow, measured, short map[goals.Key]bool) map[string]*objectData {
	out := map[string]*objectData{}
	get := func(name string) *objectData {
		d, ok := out[name]
		if !ok {
			d = &objectData{hours: map[string]float64{}}
			out[name] = d
		}
		return d
	}
	type geom struct {
		w, h int
		crop *[4]int
	}
	geoms := map[string]*geom{}
	for _, st := range stacks {
		d := get(st.Object)
		d.hours[st.Filter] += st.EffectiveSeconds / 3600
		d.subs += st.Subs
		if st.Width > 0 && st.Height > 0 {
			g := geoms[st.Object]
			if g == nil {
				g = &geom{w: st.Width, h: st.Height}
				geoms[st.Object] = g
			}
			g.crop = unionCrop(g.crop, cropOf(st))
		}
		if st.Subs <= 0 {
			continue
		}
		d.filters++
		k := goals.Key{Object: st.Object, Filter: st.Filter}
		if measured[k] {
			d.measured++
			if short[k] {
				d.short++
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
	for _, r := range refs {
		d, g := out[r.Object], geoms[r.Object]
		if d == nil || g == nil {
			continue
		}
		if fld, ok := framesField(r.Object, r.WCS, g.w, g.h, g.crop); ok {
			d.fields = append(d.fields, fld)
		}
	}
	for name, d := range out {
		if len(d.fields) == 0 && d.pos != nil && d.subs > 0 {
			d.fields = append(d.fields, pointingField(name, d.pos.ra, d.pos.dec))
		}
	}
	return out
}

func footprintKind(subj *Subject) {
	switch {
	case slices.ContainsFunc(subj.imaged, func(f field) bool { return f.kind == FieldFrames }):
		subj.Footprint = FieldFrames
	case slices.ContainsFunc(subj.imaged, func(f field) bool { return f.kind == FieldPointing }):
		subj.Footprint = FieldPointing
	case len(subj.imaged) > 0:
		subj.Footprint = "target"
	case len(subj.planned) > 0:
		subj.Footprint = FieldPlan
	default:
		subj.Footprint = "none"
	}
}

func buildSubjects(ts []tsRow, objects map[string]*objectData, judged map[int]map[string]planning.Judgement, frame sky.Frame) []Subject {
	byProject := map[int]*Subject{}
	points := map[int][]point{}
	plans := map[int][]field{}
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
				Mosaic: r.Mosaic != nil && *r.Mosaic != 0, Hours: map[string]float64{}, MinAltitude: r.MinAlt, judged: judged[r.ID]}
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
			rot := 0.0
			if r.Rotation != nil {
				rot = *r.Rotation
			}
			if f, ok := planField(*r.Target, *r.RA*15, *r.Dec, rot, frame); ok {
				plans[r.ID] = append(plans[r.ID], f)
			}
		}
	}
	out := make([]Subject, 0, len(order))
	for _, id := range order {
		subj := byProject[id]
		if len(subj.Targets) > 1 {
			subj.Mosaic = true
		}
		for _, t := range subj.Targets {
			d := objects[t]
			if d == nil {
				continue
			}
			addObject(subj, d)
			subj.imaged = append(subj.imaged, d.fields...)
		}
		for _, f := range plans[id] {
			switch d := objects[f.target]; {
			case d == nil || d.subs == 0:
				subj.planned = append(subj.planned, f)
			case len(d.fields) == 0:
				subj.imaged = append(subj.imaged, f)
			}
		}
		pts := points[id]
		if len(pts) == 0 {
			for _, t := range subj.Targets {
				if d := objects[t]; d != nil && d.pos != nil {
					pts = append(pts, *d.pos)
				}
			}
		}
		place(subj, pts)
		finish(subj)
		out = append(out, *subj)
	}
	return append(out, objectSubjects(objects, inTS)...)
}

func finish(subj *Subject) {
	subj.Measured = subj.Tally.Measured > 0
	subj.Completion, subj.Basis, _ = judge(subj.Tally, subj.judged, subj.Targets)
	subj.Done = subj.Completion == CompletionDone
	footprintKind(subj)
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
		subj.imaged = append(subj.imaged, d.fields...)
		if d.pos != nil {
			groupPts[g] = append(groupPts[g], *d.pos)
		}
	}
	for _, g := range gorder {
		subj := groups[g]
		subj.Mosaic = len(subj.Targets) > 1
		place(subj, groupPts[g])
		finish(subj)
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
	subj.Tally.add(d)
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
