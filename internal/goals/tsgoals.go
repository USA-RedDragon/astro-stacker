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
	known   map[string]bool
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
	known := make(map[string]bool, len(targets))
	for _, t := range targets {
		if t.GUID != "" {
			known[t.GUID] = true
		}
	}
	return targetMap{objects: tslink.TargetObjects(guids), guids: guids, known: known}, nil
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
	NightQuality   *float64
	NightQualityAt *time.Time
	SeasonBoost    *float64
	State          *string
	Reason         *string
	StackSubs      *int
	MinSubs        *int
	SubLimit       *int
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

func sameInt(a, b *int) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

func sameText(a, b *string) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

func (r *progressRow) setReadiness(rd Readiness) {
	r.State, r.StackSubs, r.MinSubs, r.SubLimit = &rd.State, &rd.StackSubs, &rd.MinSubs, &rd.SubLimit
	r.Reason = nil
	if rd.Reason != "" {
		r.Reason = &rd.Reason
	}
}

func (r progressRow) sameReadiness(o progressRow) bool {
	return sameText(r.State, o.State) && sameText(r.Reason, o.Reason) && sameInt(r.StackSubs, o.StackSubs) &&
		sameInt(r.MinSubs, o.MinSubs) && sameInt(r.SubLimit, o.SubLimit)
}

func (r progressRow) same(o progressRow, signals, readiness bool) bool {
	if readiness && !r.sameReadiness(o) {
		return false
	}
	if signals && (!sameFloat(r.NightQuality, o.NightQuality) || !sameTime(r.NightQualityAt, o.NightQualityAt) || !sameFloat(r.SeasonBoost, o.SeasonBoost)) {
		return false
	}
	return r.Kind == o.Kind && r.Plateau == o.Plateau && r.LowConfidence == o.LowConfidence && r.Done == o.Done &&
		sameFloat(r.GoalValue, o.GoalValue) && sameFloat(r.AchievedValue, o.AchievedValue) && sameFloat(r.Progress, o.Progress) &&
		sameFloat(r.SNR, o.SNR) && sameFloat(r.Depth, o.Depth) && sameFloat(r.EffectiveHours, o.EffectiveHours) &&
		sameFloat(r.HoursNeeded, o.HoursNeeded) && sameFloat(r.GainPerHourPct, o.GainPerHourPct)
}

type Publisher struct {
	App          *gorm.DB
	Sched        *gorm.DB
	Mode         string
	SeasonBoosts SeasonBoostSource
	Now          func() time.Time
	Log          *slog.Logger

	mu      sync.Mutex
	outcome string
}

func (p *Publisher) logger() *slog.Logger {
	if p.Log != nil {
		return p.Log
	}
	return slog.Default()
}

func (p *Publisher) note(outcome string, warn bool, msg string, args ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.outcome == outcome {
		return
	}
	p.outcome = outcome
	if warn {
		p.logger().Warn(msg, args...)
	} else {
		p.logger().Info(msg, args...)
	}
}

type PublishSummary struct {
	Rows    int
	Changed int
	Skipped string
}

const (
	SkipOff           = "off"
	SkipNoScheduler   = "no scheduler database"
	SkipNoGoalTable   = "no " + GoalTable + " table"
	SkipNoProgress    = "no " + ProgressTable + " table"
	SkipNoGoals       = "no goal names a known target"
	SkipNoProgressYet = "no progress for any goal target"
	SkipUnchanged     = "unchanged"
)

func (p *Publisher) skip(sum PublishSummary, reason string, warn bool, msg string, args ...any) PublishSummary {
	sum.Skipped = reason
	p.note(reason, warn, msg, append([]any{"reason", reason, "mode", p.Mode}, args...)...)
	return sum
}

func (p *Publisher) Publish(ctx context.Context) (PublishSummary, error) {
	var sum PublishSummary
	if p.Mode != PublishOn && p.Mode != PublishDryRun {
		return p.skip(sum, SkipOff, false, "Goal progress is not published"), nil
	}
	if p.Sched == nil {
		return p.skip(sum, SkipNoScheduler, true, "Goal progress is not published"), nil
	}
	rows, ok, err := loadGoalRows(ctx, p.Sched)
	if err != nil {
		return sum, err
	}
	if !ok {
		return p.skip(sum, SkipNoGoalTable, true, "Scheduler database table is missing; goal progress is not published", "table", GoalTable), nil
	}
	if !p.Sched.Migrator().HasTable(ProgressTable) {
		return p.skip(sum, SkipNoProgress, true, "Scheduler database table is missing; goal progress is not published", "table", ProgressTable), nil
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
		return p.skip(sum, SkipNoGoals, len(rows) > 0, "No goal progress to publish",
			"goal_rows", len(rows), "known_targets", len(tm.known), "linked_objects", len(tm.guids)), nil
	}
	withReadiness := HasReadinessColumns(p.Sched)
	want, err := p.progressRows(ctx, byGUID, tm, filters, withReadiness)
	if err != nil {
		return sum, err
	}
	if len(want) == 0 {
		return p.skip(sum, SkipNoProgressYet, false, "No goal progress to publish",
			"goal_rows", len(rows), "goal_targets", len(byGUID), "readiness_columns", withReadiness), nil
	}
	have, err := p.existing(ctx)
	if err != nil {
		return sum, err
	}
	withSignals := HasSignalColumns(p.Sched)
	var sig Signals
	if withSignals {
		if sig, err = p.signals(ctx); err != nil {
			return sum, err
		}
	}
	for _, w := range want {
		sum.Rows++
		if withSignals {
			sig.apply(&w.row)
		}
		if old, ok := have[progressKey{w.row.TargetGUID, w.row.Filter}]; ok && old.same(w.row, withSignals, withReadiness) {
			continue
		}
		sum.Changed++
		if p.Mode == PublishDryRun {
			slog.Info("Would publish goal progress (dry run)", "object", w.prog.Object, "filter", w.row.Filter, "target_guid", w.row.TargetGUID,
				"kind", w.prog.Kind, "goal", w.prog.Goal, "achieved", w.prog.Achieved, "progress", w.prog.Progress,
				"hours_needed", w.prog.HoursNeeded, "gain_per_hour_pct", w.prog.GainPerHourPct, "done", w.prog.Done)
			continue
		}
		if err := p.upsert(ctx, w.row, withSignals, withReadiness); err != nil {
			return sum, err
		}
	}
	if sum.Changed == 0 {
		return p.skip(sum, SkipUnchanged, false, "Goal progress unchanged; nothing to publish", "rows", sum.Rows), nil
	}
	p.mu.Lock()
	p.outcome = ""
	p.mu.Unlock()
	if p.Mode == PublishOn {
		p.logger().Info("Published goal progress", "rows", sum.Rows, "changed", sum.Changed, "readiness_columns", withReadiness, "signal_columns", withSignals)
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
		if len(tm.objects[r.TargetGUID]) == 0 && !tm.known[r.TargetGUID] {
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

func (p *Publisher) progressRows(ctx context.Context, byGUID map[string]map[string]goalRow, tm targetMap, filters map[Key]string, readiness bool) ([]wantedProgress, error) {
	guids := make([]string, 0, len(byGUID))
	var objects []string
	for g := range byGUID {
		guids = append(guids, g)
		objects = append(objects, tm.objects[g]...)
	}
	sort.Strings(guids)
	var ms []app.GoalMeasurement
	if len(objects) > 0 {
		if err := p.App.WithContext(ctx).Where("object IN ?", objects).Order("object, filter").Find(&ms).Error; err != nil {
			return nil, fmt.Errorf("load goal measurements: %w", err)
		}
	}
	subs := map[Key]int{}
	if readiness {
		var err error
		if subs, err = MeasurableSubs(ctx, p.App); err != nil {
			return nil, err
		}
	}
	var out []wantedProgress
	for _, guid := range guids {
		objs := tm.objects[guid]
		measured, failed := map[string]app.GoalMeasurement{}, map[string]app.GoalMeasurement{}
		for _, m := range ms {
			if !slices.Contains(objs, m.Object) {
				continue
			}
			if m.Error != nil {
				if cur, ok := failed[m.Filter]; !ok || m.Subs > cur.Subs {
					failed[m.Filter] = m
				}
				continue
			}
			if cur, ok := measured[m.Filter]; !ok || m.EffectiveHours > cur.EffectiveHours {
				measured[m.Filter] = m
			}
		}
		for _, f := range sortedKeys(measured) {
			m := measured[f]
			g := DefaultGoal(f)
			g.TargetGUID = guid
			tsFilter := f
			if r, ok := byGUID[guid][f]; ok {
				g = r.goal(f)
				tsFilter = r.Filter
			} else if tf, ok := filters[Key{Object: m.Object, Filter: f}]; ok {
				tsFilter = tf
			}
			m.TargetGUID = guid
			prog := Evaluate(m, g)
			row := toRow(guid, tsFilter, prog, m.Depth)
			if readiness {
				row.setReadiness(Ready(&m, &prog, stackSubs(subs, objs, f)))
			}
			out = append(out, wantedProgress{row: row, prog: prog})
		}
		if !readiness {
			continue
		}
		for _, f := range sortedKeys(byGUID[guid]) {
			if _, ok := measured[f]; ok {
				continue
			}
			r := byGUID[guid][f]
			var mp *app.GoalMeasurement
			if fm, ok := failed[f]; ok {
				mp = &fm
			}
			row, prog := waitingRow(guid, r, f, mp)
			row.setReadiness(Ready(mp, nil, stackSubs(subs, objs, f)))
			out = append(out, wantedProgress{row: row, prog: prog})
		}
	}
	return out, nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func stackSubs(subs map[Key]int, objects []string, filter string) int {
	n := 0
	for _, o := range objects {
		n = max(n, subs[Key{Object: o, Filter: filter}])
	}
	return n
}

func waitingRow(guid string, r goalRow, filter string, m *app.GoalMeasurement) (progressRow, Progress) {
	g := r.goal(filter)
	prog := Progress{Filter: filter, TargetGUID: guid, Kind: g.Kind, Goal: g.SNR}
	kind := kindSNR
	if g.Kind == KindDepth {
		kind, prog.Goal = kindDepth, g.Depth
	}
	row := progressRow{TargetGUID: guid, Filter: r.Filter, Kind: kind, GoalValue: ptr(prog.Goal)}
	if m != nil {
		prog.Object = m.Object
		at := m.MeasuredAt.UTC()
		row.MeasuredAt = &at
		prog.MeasuredAt = at
	}
	return row, prog
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

func (p *Publisher) upsert(ctx context.Context, r progressRow, signals, readiness bool) error {
	values := map[string]any{
		"kind": r.Kind, "goal_value": r.GoalValue, "achieved_value": r.AchievedValue, "progress": r.Progress,
		"snr": r.SNR, "depth": r.Depth, "effective_hours": r.EffectiveHours, "hours_needed": r.HoursNeeded,
		"gain_per_hour_pct": r.GainPerHourPct, "plateau": r.Plateau, "low_confidence": r.LowConfidence,
		"done": r.Done, "measured_at": r.MeasuredAt,
	}
	if signals {
		values[ColumnNightQuality] = r.NightQuality
		values[ColumnNightQualityAt] = r.NightQualityAt
		values[ColumnSeasonBoost] = r.SeasonBoost
	}
	if readiness {
		values[ColumnState] = r.State
		values[ColumnReason] = r.Reason
		values[ColumnStackSubs] = r.StackSubs
		values[ColumnMinSubs] = r.MinSubs
		values[ColumnSubLimit] = r.SubLimit
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
