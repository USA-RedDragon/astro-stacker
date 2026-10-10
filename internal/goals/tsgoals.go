package goals

import (
	"context"
	"crypto/sha256"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/frameheader"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/tslink"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	GoalTable     = "ts_goal"
	ProgressTable = "ts_goal_progress"
)

const (
	PublishOff    = "off"
	PublishDryRun = "dry-run"
	PublishOn     = "on"
)

const (
	kindSNR   = 0
	kindDepth = 1
)

type goalRow struct {
	TargetGUID  string
	Filter      string
	Kind        int
	SNRGoal     *float64
	DepthGoal   *float64
	PlateauStop *int
	Region      *string
	UpdatedAt   rowTime
}

type rowTime struct {
	t *time.Time
}

func (s rowTime) Value() (driver.Value, error) {
	if s.t == nil {
		return "", nil
	}
	return *s.t, nil
}

func (s *rowTime) Scan(v any) error {
	s.t = nil
	var text string
	switch x := v.(type) {
	case nil:
		return nil
	case time.Time:
		s.t = &x
		return nil
	case string:
		text = x
	case []byte:
		text = string(x)
	default:
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999Z07:00", "2006-01-02 15:04:05.999999999", "2006-01-02T15:04:05.999999999"} {
		if t, err := time.Parse(layout, text); err == nil {
			s.t = &t
			return nil
		}
	}
	return nil
}

type targetMap struct {
	objects map[string][]string
	guids   map[string]string
}

func loadTargetMap(ctx context.Context, appDB, sched *gorm.DB) (targetMap, error) {
	targets, err := tslink.Targets(ctx, sched)
	if err != nil {
		return targetMap{}, err
	}
	guids, err := tslink.ObjectTargets(ctx, appDB, targets)
	if err != nil {
		return targetMap{}, err
	}
	return targetMap{objects: tslink.TargetObjects(guids), guids: guids}, nil
}

func ObjectGUIDs(ctx context.Context, appDB, sched *gorm.DB) (map[string]string, error) {
	m, err := loadTargetMap(ctx, appDB, sched)
	return m.guids, err
}

func loadGoalRows(ctx context.Context, sched *gorm.DB) ([]goalRow, bool, error) {
	if !sched.Migrator().HasTable(GoalTable) {
		return nil, false, nil
	}
	var rows []goalRow
	if err := sched.WithContext(ctx).Table(GoalTable).
		Select("target_guid, filter, kind, snr_goal, depth_goal, plateau_stop, region, updated_at").
		Order("target_guid, filter").Scan(&rows).Error; err != nil {
		return nil, true, fmt.Errorf("load goals: %w", err)
	}
	return rows, true, nil
}

func loadPlanFilters(ctx context.Context, sched *gorm.DB, tm targetMap) (map[Key]string, error) {
	out := map[Key]string{}
	m := sched.Migrator()
	if !m.HasTable("exposureplan") || !m.HasTable("exposuretemplate") {
		return out, nil
	}
	var rows []struct {
		GUID       string
		Filtername string
	}
	if err := sched.WithContext(ctx).Table("exposureplan e").
		Select("coalesce(t.guid, '') AS guid, et.filtername AS filtername").
		Joins(`JOIN target t ON t."Id" = e.targetid`).
		Joins(`JOIN exposuretemplate et ON et."Id" = e."exposureTemplateId"`).
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("load exposure plan filters: %w", err)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].GUID != rows[j].GUID {
			return rows[i].GUID < rows[j].GUID
		}
		return rows[i].Filtername < rows[j].Filtername
	})
	for _, r := range rows {
		for _, obj := range tm.objects[r.GUID] {
			k := Key{Object: obj, Filter: frameheader.NormalizeFilter(r.Filtername)}
			if _, ok := out[k]; !ok {
				out[k] = r.Filtername
			}
		}
	}
	return out, nil
}

func (r goalRow) goal(filter string) Goal {
	g := DefaultGoal(filter)
	g.TargetGUID = r.TargetGUID
	if r.Kind == kindDepth {
		g.Kind = KindDepth
	}
	if r.SNRGoal != nil && *r.SNRGoal > 0 {
		g.SNR = *r.SNRGoal
	}
	if r.DepthGoal != nil && *r.DepthGoal > 0 {
		g.Depth = *r.DepthGoal
	}
	if r.PlateauStop != nil {
		g.PlateauStop = *r.PlateauStop != 0
	}
	if r.Region != nil {
		if pts, err := ParseRegion(*r.Region); err == nil && len(pts) >= 3 {
			g.Region = pts
		} else if err != nil {
			slog.Warn("Ignoring a goal's region that is not JSON points", "target_guid", r.TargetGUID, "filter", r.Filter, "error", err)
		}
	}
	return g
}

func newer(a, b *time.Time) bool {
	switch {
	case a == nil:
		return false
	case b == nil:
		return true
	}
	return a.After(*b)
}

func LoadGoals(ctx context.Context, appDB, sched *gorm.DB) (map[Key]Goal, map[Key]string, error) {
	tm, err := loadTargetMap(ctx, appDB, sched)
	if err != nil {
		return nil, nil, err
	}
	filters, err := loadPlanFilters(ctx, sched, tm)
	if err != nil {
		return nil, nil, err
	}
	rows, _, err := loadGoalRows(ctx, sched)
	if err != nil {
		return nil, nil, err
	}
	goals := map[Key]Goal{}
	stamp := map[Key]*time.Time{}
	for _, r := range rows {
		for _, obj := range tm.objects[r.TargetGUID] {
			k := Key{Object: obj, Filter: frameheader.NormalizeFilter(r.Filter)}
			if _, seen := goals[k]; seen && !newer(r.UpdatedAt.t, stamp[k]) {
				continue
			}
			goals[k] = r.goal(k.Filter)
			stamp[k] = r.UpdatedAt.t
			if _, ok := filters[k]; !ok {
				filters[k] = r.Filter
			}
		}
	}
	return goals, filters, nil
}

func RegionHash(region []Point) string {
	if len(region) < 3 {
		return ""
	}
	b, err := json.Marshal(region)
	if err != nil {
		return ""
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:8])
}

type progressRow struct {
	TargetGUID     string
	Filter         string
	Kind           int
	GoalValue      *float64
	AchievedValue  *float64
	Progress       *float64
	SNR            *float64
	Depth          *float64
	EffectiveHours *float64
	HoursNeeded    *float64
	GainPerHourPct *float64
	Plateau        int
	LowConfidence  int
	Done           int
	MeasuredAt     *time.Time
}

func ptr(v float64) *float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	return &v
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func toRow(guid, filter string, p Progress, depth *float64) progressRow {
	kind := kindSNR
	if p.Kind == KindDepth {
		kind = kindDepth
	}
	at := p.MeasuredAt.UTC()
	r := progressRow{
		TargetGUID: guid, Filter: filter, Kind: kind,
		GoalValue: ptr(p.Goal), AchievedValue: ptr(p.Achieved), Progress: ptr(p.Progress), SNR: ptr(p.SNR),
		EffectiveHours: ptr(p.EffectiveHours), HoursNeeded: ptr(p.HoursNeeded), GainPerHourPct: ptr(p.GainPerHourPct),
		Plateau: boolInt(p.Plateau), LowConfidence: boolInt(p.LowConfidence), Done: boolInt(p.Done), MeasuredAt: &at,
	}
	if depth != nil {
		r.Depth = ptr(*depth)
	}
	return r
}

func sameFloat(a, b *float64) bool {
	switch {
	case a == nil || b == nil:
		return a == nil && b == nil
	case *a == *b:
		return true
	}
	return math.Abs(*a-*b) <= 1e-9*math.Max(1, math.Max(math.Abs(*a), math.Abs(*b)))
}

func (r progressRow) same(o progressRow) bool {
	return r.Kind == o.Kind && r.Plateau == o.Plateau && r.LowConfidence == o.LowConfidence && r.Done == o.Done &&
		sameFloat(r.GoalValue, o.GoalValue) && sameFloat(r.AchievedValue, o.AchievedValue) && sameFloat(r.Progress, o.Progress) &&
		sameFloat(r.SNR, o.SNR) && sameFloat(r.Depth, o.Depth) && sameFloat(r.EffectiveHours, o.EffectiveHours) &&
		sameFloat(r.HoursNeeded, o.HoursNeeded) && sameFloat(r.GainPerHourPct, o.GainPerHourPct)
}

type Publisher struct {
	App   *gorm.DB
	Sched *gorm.DB
	Mode  string

	mu     sync.Mutex
	warned map[string]bool
}

func (p *Publisher) warnOnce(table string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.warned == nil {
		p.warned = map[string]bool{}
	}
	if !p.warned[table] {
		p.warned[table] = true
		slog.Warn("Scheduler database table is missing; goal progress is not published", "table", table)
	}
}

type PublishSummary struct {
	Rows    int
	Changed int
}

func (p *Publisher) Publish(ctx context.Context) (PublishSummary, error) {
	var sum PublishSummary
	if p.Mode != PublishOn && p.Mode != PublishDryRun {
		return sum, nil
	}
	rows, ok, err := loadGoalRows(ctx, p.Sched)
	if err != nil {
		return sum, err
	}
	if !ok {
		p.warnOnce(GoalTable)
		return sum, nil
	}
	if !p.Sched.Migrator().HasTable(ProgressTable) {
		p.warnOnce(ProgressTable)
		return sum, nil
	}
	tm, err := loadTargetMap(ctx, p.App, p.Sched)
	if err != nil {
		return sum, err
	}
	filters, err := loadPlanFilters(ctx, p.Sched, tm)
	if err != nil {
		return sum, err
	}
	byGUID := goalsByTarget(rows, tm)
	if len(byGUID) == 0 {
		return sum, nil
	}
	want, err := p.progressRows(ctx, byGUID, tm, filters)
	if err != nil {
		return sum, err
	}
	have, err := p.existing(ctx)
	if err != nil {
		return sum, err
	}
	for _, w := range want {
		sum.Rows++
		if old, ok := have[progressKey{w.row.TargetGUID, w.row.Filter}]; ok && old.same(w.row) {
			continue
		}
		sum.Changed++
		if p.Mode == PublishDryRun {
			slog.Info("Would publish goal progress (dry run)", "object", w.prog.Object, "filter", w.row.Filter, "target_guid", w.row.TargetGUID,
				"kind", w.prog.Kind, "goal", w.prog.Goal, "achieved", w.prog.Achieved, "progress", w.prog.Progress,
				"hours_needed", w.prog.HoursNeeded, "gain_per_hour_pct", w.prog.GainPerHourPct, "done", w.prog.Done)
			continue
		}
		if err := p.upsert(ctx, w.row); err != nil {
			return sum, err
		}
	}
	if sum.Changed > 0 && p.Mode == PublishOn {
		slog.Info("Published goal progress", "rows", sum.Rows, "changed", sum.Changed)
	}
	return sum, nil
}

type progressKey struct{ guid, filter string }

type wantedProgress struct {
	row  progressRow
	prog Progress
}

func goalsByTarget(rows []goalRow, tm targetMap) map[string]map[string]goalRow {
	out := map[string]map[string]goalRow{}
	for _, r := range rows {
		if len(tm.objects[r.TargetGUID]) == 0 {
			continue
		}
		if out[r.TargetGUID] == nil {
			out[r.TargetGUID] = map[string]goalRow{}
		}
		f := frameheader.NormalizeFilter(r.Filter)
		if cur, seen := out[r.TargetGUID][f]; !seen || newer(r.UpdatedAt.t, cur.UpdatedAt.t) {
			out[r.TargetGUID][f] = r
		}
	}
	return out
}

func (p *Publisher) progressRows(ctx context.Context, byGUID map[string]map[string]goalRow, tm targetMap, filters map[Key]string) ([]wantedProgress, error) {
	guids := make([]string, 0, len(byGUID))
	var objects []string
	for g := range byGUID {
		guids = append(guids, g)
		objects = append(objects, tm.objects[g]...)
	}
	sort.Strings(guids)
	var ms []app.GoalMeasurement
	if err := p.App.WithContext(ctx).Where("object IN ? AND error IS NULL", objects).Order("object, filter").Find(&ms).Error; err != nil {
		return nil, fmt.Errorf("load goal measurements: %w", err)
	}
	var out []wantedProgress
	for _, guid := range guids {
		for _, m := range ms {
			if !slices.Contains(tm.objects[guid], m.Object) {
				continue
			}
			g := DefaultGoal(m.Filter)
			g.TargetGUID = guid
			tsFilter := m.Filter
			if r, ok := byGUID[guid][m.Filter]; ok {
				g = r.goal(m.Filter)
				tsFilter = r.Filter
			} else if f, ok := filters[Key{Object: m.Object, Filter: m.Filter}]; ok {
				tsFilter = f
			}
			m.TargetGUID = guid
			prog := Evaluate(m, g)
			out = append(out, wantedProgress{row: toRow(guid, tsFilter, prog, m.Depth), prog: prog})
		}
	}
	return out, nil
}

func (p *Publisher) existing(ctx context.Context) (map[progressKey]progressRow, error) {
	var rows []progressRow
	if err := p.Sched.WithContext(ctx).Table(ProgressTable).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("load goal progress: %w", err)
	}
	out := make(map[progressKey]progressRow, len(rows))
	for _, r := range rows {
		out[progressKey{r.TargetGUID, r.Filter}] = r
	}
	return out, nil
}

func (p *Publisher) upsert(ctx context.Context, r progressRow) error {
	values := map[string]any{
		"kind": r.Kind, "goal_value": r.GoalValue, "achieved_value": r.AchievedValue, "progress": r.Progress,
		"snr": r.SNR, "depth": r.Depth, "effective_hours": r.EffectiveHours, "hours_needed": r.HoursNeeded,
		"gain_per_hour_pct": r.GainPerHourPct, "plateau": r.Plateau, "low_confidence": r.LowConfidence,
		"done": r.Done, "measured_at": r.MeasuredAt,
	}
	row := map[string]any{"target_guid": r.TargetGUID, "filter": r.Filter}
	for k, v := range values {
		row[k] = v
	}
	if err := p.Sched.WithContext(ctx).Table(ProgressTable).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "target_guid"}, {Name: "filter"}},
		DoUpdates: clause.Assignments(values),
	}).Create(row).Error; err != nil {
		return fmt.Errorf("publish goal progress for %s %s: %w", r.TargetGUID, r.Filter, err)
	}
	return nil
}
