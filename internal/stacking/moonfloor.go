package stacking

import (
	"context"
	"fmt"
	"path"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/store/tables"
)

const migrationTable = "ts_template_migration"

// managedPeriod is when a plan imaged on a managed template: from its
// migration to its revert, or on.
type managedPeriod struct {
	from time.Time
	to   *time.Time
}

type planFile struct {
	target, file string
}

// floorPlans knows which lights a plan took on a managed template, so they
// keep the moon floor instead of the templates' rules.
type floorPlans struct {
	periods map[int][]managedPeriod
	managed map[int]bool
	byGUID  map[string]int
	byFile  map[planFile]int
}

// covers reports whether the light was taken on a managed template: inside
// one of its plan's migration periods, or on a plan whose current template
// is managed after its last period. A light with no plan link keeps the
// templates' rules.
func (fp *floorPlans) covers(f app.Frame) bool {
	if fp == nil || f.DateObs == nil {
		return false
	}
	id, ok := fp.plan(f)
	if !ok {
		return false
	}
	t := *f.DateObs
	var last time.Time
	for _, p := range fp.periods[id] {
		if !t.Before(p.from) && (p.to == nil || t.Before(*p.to)) {
			return true
		}
		end := p.from
		if p.to != nil {
			end = *p.to
		}
		if end.After(last) {
			last = end
		}
	}
	return fp.managed[id] && !t.Before(last)
}

func (fp *floorPlans) plan(f app.Frame) (int, bool) {
	if f.TSExposurePlan != nil {
		if id, ok := fp.byGUID[*f.TSExposurePlan]; ok {
			return id, true
		}
	}
	id, ok := fp.byFile[planFile{f.Object, path.Base(f.Key)}]
	return id, ok
}

// loadFloorPlans reads the plans that imaged on managed templates; when none
// ever did, nothing else is read.
func (p *Pipeline) loadFloorPlans(ctx context.Context) (*floorPlans, error) {
	fp := &floorPlans{periods: map[int][]managedPeriod{}, managed: map[int]bool{}, byGUID: map[string]int{}, byFile: map[planFile]int{}}
	if err := fp.loadPeriods(ctx, p); err != nil {
		return nil, err
	}
	if err := fp.loadManaged(ctx, p); err != nil {
		return nil, err
	}
	ids := make([]int, 0, len(fp.periods)+len(fp.managed))
	for id := range fp.periods {
		ids = append(ids, id)
	}
	for id := range fp.managed {
		if _, ok := fp.periods[id]; !ok {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return fp, nil
	}
	relevant := map[int]bool{}
	for _, id := range ids {
		relevant[id] = true
	}
	var plans []struct {
		ID   int     `gorm:"column:id"`
		GUID *string `gorm:"column:guid"`
	}
	if err := p.sched.WithContext(ctx).Table("exposureplan").Select(`"Id" AS id, guid`).Scan(&plans).Error; err != nil {
		return nil, fmt.Errorf("load exposure plans: %w", err)
	}
	for _, pl := range plans {
		if relevant[pl.ID] && pl.GUID != nil && *pl.GUID != "" {
			fp.byGUID[*pl.GUID] = pl.ID
		}
	}
	has, err := tables.Has(ctx, p.sched, "acquiredimage")
	if err != nil || !has {
		return fp, err
	}
	images, err := p.tsImages(ctx)
	if err != nil {
		return nil, err
	}
	for _, im := range images {
		k := planFile{im.Target, im.File}
		if _, seen := fp.byFile[k]; !seen && im.File != "" && relevant[im.PlanID] {
			fp.byFile[k] = im.PlanID
		}
	}
	return fp, nil
}

func (fp *floorPlans) loadPeriods(ctx context.Context, p *Pipeline) error {
	has, err := tables.Has(ctx, p.sched, migrationTable)
	if err != nil || !has {
		return err
	}
	var rows []struct {
		PlanID     int     `gorm:"column:plan_id"`
		MigratedAt string  `gorm:"column:migrated_at"`
		RevertedAt *string `gorm:"column:reverted_at"`
	}
	if err := p.sched.WithContext(ctx).Table(migrationTable).Select("plan_id, migrated_at, reverted_at").Scan(&rows).Error; err != nil {
		return fmt.Errorf("load template migrations: %w", err)
	}
	for _, r := range rows {
		from, err := parseChangedAt(r.MigratedAt)
		if err != nil {
			return fmt.Errorf("template migration time %q: %w", r.MigratedAt, err)
		}
		per := managedPeriod{from: from}
		if r.RevertedAt != nil && *r.RevertedAt != "" {
			to, err := parseChangedAt(*r.RevertedAt)
			if err != nil {
				return fmt.Errorf("template revert time %q: %w", *r.RevertedAt, err)
			}
			per.to = &to
		}
		fp.periods[r.PlanID] = append(fp.periods[r.PlanID], per)
	}
	return nil
}

func (fp *floorPlans) loadManaged(ctx context.Context, p *Pipeline) error {
	has, err := tables.Has(ctx, p.sched, roleTable)
	if err != nil || !has {
		return err
	}
	var templates []int
	if err := p.sched.WithContext(ctx).Table(roleTable).Where("role = ?", "managed").Pluck("template_id", &templates).Error; err != nil {
		return fmt.Errorf("load managed templates: %w", err)
	}
	if len(templates) == 0 {
		return nil
	}
	var ids []int
	if err := p.sched.WithContext(ctx).Table("exposureplan").Where(`"exposureTemplateId" IN ?`, templates).Select(`"Id"`).Scan(&ids).Error; err != nil {
		return fmt.Errorf("load plans on managed templates: %w", err)
	}
	for _, id := range ids {
		fp.managed[id] = true
	}
	return nil
}
