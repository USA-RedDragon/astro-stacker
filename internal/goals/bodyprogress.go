package goals

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/bodies"
	"github.com/USA-RedDragon/astro-stacker/internal/frameheader"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/store/tables"
	"gorm.io/gorm"
)

type BodyInputs struct {
	Stored      map[string]bodies.Target
	Nights      map[Key][]app.BodyNight
	Tonight     time.Time
	TonightSubs map[Key]int
}

func LoadBodyInputs(ctx context.Context, appDB, sched *gorm.DB, now time.Time, longitude *float64) (BodyInputs, error) {
	in := BodyInputs{Nights: map[Key][]app.BodyNight{}, TonightSubs: map[Key]int{}, Tonight: NightOf(now, deref(longitude))}
	if sched != nil {
		stored, err := bodies.TargetsByName(ctx, sched)
		if err != nil {
			return in, err
		}
		in.Stored = stored
	}
	if appDB == nil {
		in.Tonight = NightOf(now, deref(longitude))
		return in, nil
	}
	if has, err := tables.Has(ctx, appDB, &app.BodyNight{}); err != nil {
		return in, err
	} else if has {
		var rows []app.BodyNight
		if err := appDB.WithContext(ctx).Order("object, filter, night").Find(&rows).Error; err != nil {
			return in, fmt.Errorf("load body nights: %w", err)
		}
		var latest time.Time
		for _, r := range rows {
			r.Night = r.Night.UTC()
			k := Key{Object: r.Object, Filter: r.Filter}
			in.Nights[k] = append(in.Nights[k], r)
			if longitude == nil && r.Longitude != nil && r.UpdatedAt.After(latest) {
				latest, longitude = r.UpdatedAt, r.Longitude
			}
		}
	}
	lon := 0.0
	if longitude != nil {
		lon = *longitude
	}
	in.Tonight = NightOf(now, lon)
	subs, err := NightSubs(ctx, appDB, in.Tonight)
	if err != nil {
		return in, err
	}
	in.TonightSubs = subs
	return in, nil
}

func NightSubs(ctx context.Context, appDB *gorm.DB, night time.Time) (map[Key]int, error) {
	out := map[Key]int{}
	for _, t := range []any{&app.Stack{}, &app.StackFrame{}, &app.Frame{}} {
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
		Joins("JOIN frames f ON f.id = sf.frame_id").
		Where("sf.status IN ? AND sf.registered_key IS NOT NULL AND sf.weight > 0 AND sf.exposure > 0", []string{app.StackStatusAdded, app.StackStatusNight}).
		Where("f.night >= ? AND f.night < ?", night, night.AddDate(0, 0, 1)).
		Group("s.object, s.filter").Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("count tonight's subs: %w", err)
	}
	for _, r := range rows {
		out[Key{Object: r.Object, Filter: r.Filter}] = r.N
	}
	return out, nil
}

func (in BodyInputs) KindOf(objects ...string) (string, bool) {
	for _, o := range objects {
		if b, ok := bodies.OwnBody(in.Stored, o); ok {
			return BodyKind(b.Kind), true
		}
	}
	return "", false
}

func (in BodyInputs) For(objects []string, filter string) ([]app.BodyNight, int) {
	byNight := map[string]app.BodyNight{}
	subs := 0
	for _, o := range objects {
		k := Key{Object: o, Filter: filter}
		subs = max(subs, in.TonightSubs[k])
		for _, n := range in.Nights[k] {
			d := n.Night.Format(NightLayout)
			if cur, ok := byNight[d]; !ok || n.Subs > cur.Subs {
				byNight[d] = n
			}
		}
	}
	out := make([]app.BodyNight, 0, len(byNight))
	for _, n := range byNight {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Night.Before(out[j].Night) })
	return out, subs
}

func (in BodyInputs) Filters(objects []string) []string {
	seen := map[string]bool{}
	var out []string
	for k := range in.Nights {
		for _, o := range objects {
			if k.Object == o && !seen[k.Filter] {
				seen[k.Filter] = true
				out = append(out, k.Filter)
			}
		}
	}
	sort.Strings(out)
	return out
}

func (p *Publisher) bodyRows(guid, kind string, objs []string, goalRows map[string]goalRow, filters map[Key]string, in BodyInputs, readiness bool) []wantedProgress {
	set := map[string]bool{}
	for f := range goalRows {
		set[f] = true
	}
	for _, f := range in.Filters(objs) {
		set[frameheader.NormalizeFilter(f)] = true
	}
	out := make([]wantedProgress, 0, len(set))
	for _, f := range sortedKeys(set) {
		g := DefaultBodyGoal(kind, f)
		g.TargetGUID = guid
		tsFilter := f
		if r, ok := goalRows[f]; ok {
			g = r.goal(f)
			tsFilter = r.Filter
		} else {
			for _, o := range objs {
				if tf, ok := filters[Key{Object: o, Filter: f}]; ok {
					tsFilter = tf
					break
				}
			}
		}
		if g.SNR <= 0 {
			g.SNR = DefaultBodyGoal(kind, f).SNR
		}
		nights, subs := in.For(objs, f)
		prog, rd, _ := EvaluateBody(kind, nights, g, in.Tonight, subs)
		var row progressRow
		var pr Progress
		if prog != nil {
			pr = *prog
			pr.TargetGUID = guid
			row = toRow(guid, tsFilter, pr, nil)
		} else {
			pr = Progress{Filter: f, TargetGUID: guid, Kind: KindSNR, Goal: g.SNR}
			row = progressRow{TargetGUID: guid, Filter: tsFilter, Kind: kindSNR, GoalValue: ptr(g.SNR)}
		}
		if readiness {
			row.setReadiness(rd)
		}
		out = append(out, wantedProgress{row: row, prog: pr})
	}
	return out
}

func deref(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}
