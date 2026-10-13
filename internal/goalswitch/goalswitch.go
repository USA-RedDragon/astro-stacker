package goalswitch

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discover"
	"github.com/USA-RedDragon/astro-stacker/internal/frameheader"
	"github.com/USA-RedDragon/astro-stacker/internal/goals"
	"github.com/USA-RedDragon/astro-stacker/internal/planning"
	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"github.com/USA-RedDragon/astro-stacker/internal/skybright"
	"github.com/USA-RedDragon/astro-stacker/internal/trigger"
	"gorm.io/gorm"
)

const (
	Author   = "stacker: zero point"
	ModeOff  = "off"
	ModeDry  = "dry-run"
	ModeOn   = "on"
	minDepth = 15
	maxDepth = 32
	stateOn  = "Active"
)

type Change struct {
	Project string  `json:"project"`
	Target  string  `json:"target"`
	Filter  string  `json:"filter"`
	SNR     float64 `json:"snr"`
	Depth   float64 `json:"depth"`
	Now     float64 `json:"now"`
	Basis   string  `json:"basis"`
}

func key(guid, filter string) string {
	return guid + "|" + strings.ToLower(frameheader.NormalizeFilter(filter))
}

func DepthGoal(m *planning.Measurement, filter string) (float64, string, bool) {
	if m == nil || m.Depth == nil || m.DepthApprox || m.EffectiveHours <= 0 {
		return 0, "", false
	}
	now := *m.Depth
	if h := m.PlateauAt.Hours; h != nil && *h > 0 && !math.IsInf(*h, 0) {
		d := math.Round((now+1.25*math.Log10(*h/m.EffectiveHours))*10) / 10
		return d, fmt.Sprintf("the depth at its plateau point, %.1f h, from %.2f now after %.1f h", *h, now, m.EffectiveHours), d > now && d >= minDepth && d <= maxDepth
	}
	d := goals.DefaultDepth(frameheader.NormalizeFilter(filter))
	return d, fmt.Sprintf("the default depth; the plateau is not measured yet (%s)", m.PlateauAt.Unmeasured), d > now
}

func Plan(s *planning.Snapshot, classes map[int]string, skip map[string]bool) ([]schedcmd.GoalEditPayload, []Change) {
	var out []schedcmd.GoalEditPayload
	var changes []Change
	settings := s.GoalSettings()
	for _, p := range s.Projects {
		if p.State != stateOn || !discover.DepthClass(classes[p.ID]) {
			continue
		}
		edit := schedcmd.GoalEditPayload{ProjectID: int64(p.ID), ProjectName: p.Name}
		for _, t := range p.Targets {
			if !t.Active || t.GUID == "" {
				continue
			}
			for _, g := range t.Goals {
				row, ok := settings.For(t.GUID, g.Filter)
				if !ok || row.IsDepth() || !skybright.Broadband(g.Filter) || skip[key(t.GUID, g.Filter)] {
					continue
				}
				depth, basis, ok := DepthGoal(g.Measurement, g.Filter)
				if !ok {
					continue
				}
				before := settingOf(row)
				after := before
				after.Kind, after.DepthGoal = schedcmd.GoalKindDepth, &depth
				change := schedcmd.GoalChange{TargetID: int64(t.ID), TargetGUID: t.GUID, TargetName: t.Name, Filter: row.Filter, Before: &before, After: &after}
				if err := valid(edit, change); err != nil {
					slog.Warn("Leaving a goal on SNR: its stored row does not make a valid goal edit", "project", p.Name, "target", t.Name, "filter", row.Filter, "error", err)
					continue
				}
				edit.Goals = append(edit.Goals, change)
				changes = append(changes, Change{Project: p.Name, Target: t.Name, Filter: row.Filter, SNR: before.SNRGoal, Depth: depth, Now: *g.Measurement.Depth, Basis: basis})
			}
		}
		if len(edit.Goals) > 0 {
			out = append(out, edit)
		}
	}
	return out, changes
}

func valid(edit schedcmd.GoalEditPayload, c schedcmd.GoalChange) error {
	edit.Goals = []schedcmd.GoalChange{c}
	raw, err := json.Marshal(edit)
	if err != nil {
		return err
	}
	spec, err := schedcmd.Default().Get(schedcmd.KindGoalEdit)
	if err != nil {
		return err
	}
	return spec.Validate(raw)
}

func settingOf(r goals.Setting) schedcmd.GoalSetting {
	g := schedcmd.GoalSetting{Kind: r.Kind, PlateauStop: r.PlateauStop == nil || *r.PlateauStop != 0, DepthGoal: r.DepthGoal}
	if r.SNRGoal != nil {
		g.SNRGoal = *r.SNRGoal
	}
	if r.Region != nil {
		g.Region = *r.Region
	}
	return g
}

func Skips(ctx context.Context, log schedcmd.Log) (map[string]bool, error) {
	skip := map[string]bool{}
	mark := func(rs []schedcmd.Record) {
		for _, r := range rs {
			if r.Kind != schedcmd.KindGoalEdit {
				continue
			}
			var p schedcmd.GoalEditPayload
			if json.Unmarshal(r.Payload, &p) != nil {
				continue
			}
			for _, g := range p.Goals {
				skip[key(g.TargetGUID, g.Filter)] = true
			}
		}
	}
	mine, err := log.List(ctx, schedcmd.Filter{Author: Author, Limit: schedcmd.MaxListLimit})
	if err != nil {
		return nil, fmt.Errorf("list automatic goal edits: %w", err)
	}
	mark(mine)
	waiting, err := log.Waiting(ctx)
	if err != nil {
		return nil, fmt.Errorf("list waiting commands: %w", err)
	}
	mark(waiting)
	return skip, nil
}

type Switcher struct {
	App      *gorm.DB
	Sched    *gorm.DB
	Commands *schedcmd.Service
	Classes  func(ctx context.Context) (map[int]string, error)
	Mode     string

	kickOnce sync.Once
	kick     *trigger.Debouncer
}

// Kicks are debounced by KickQuiet and KickMaxWait and runs kept
// KickMinGap apart; the first run waits StartAfter for the project classes
// to load after a restart.
const (
	KickQuiet   = time.Minute
	KickMaxWait = 10 * time.Minute
	KickMinGap  = 10 * time.Minute
	StartAfter  = 5 * time.Minute
)

func (w *Switcher) debouncer() *trigger.Debouncer {
	w.kickOnce.Do(func() {
		w.kick = &trigger.Debouncer{Quiet: KickQuiet, MaxWait: KickMaxWait, MinGap: KickMinGap, StartAfter: StartAfter}
	})
	return w.kick
}

// Kick asks for a check soon, as when a master gets a zero point or goals
// change.
func (w *Switcher) Kick() { w.debouncer().Kick() }

func (w *Switcher) Once(ctx context.Context) ([]Change, error) {
	if w.Mode == ModeOff || w.Mode == "" {
		return nil, nil
	}
	classes, err := w.Classes(ctx)
	if err != nil {
		return nil, fmt.Errorf("object classes: %w", err)
	}
	in, err := planning.AppInputs(ctx, w.App, w.Sched)
	if err != nil {
		return nil, err
	}
	snap, err := planning.Load(ctx, w.Sched, w.App, in)
	if err != nil {
		return nil, err
	}
	if !snap.GoalsSetUp() {
		return nil, nil
	}
	skip, err := Skips(ctx, w.Commands.Log)
	if err != nil {
		return nil, err
	}
	edits, changes := Plan(snap, classes, skip)
	for _, c := range changes {
		slog.Info("Filter has a zero point; moving its goal from SNR to depth", "mode", w.Mode, "project", c.Project, "target", c.Target,
			"filter", c.Filter, "snr_goal", c.SNR, "depth_goal", c.Depth, "depth_now", c.Now, "basis", c.Basis)
	}
	if w.Mode != ModeOn {
		return changes, nil
	}
	for _, e := range edits {
		raw, err := json.Marshal(e)
		if err != nil {
			return changes, err
		}
		if _, err := w.Commands.Submit(ctx, schedcmd.KindGoalEdit, raw, Author); err != nil {
			slog.Warn("Could not submit the automatic depth goal edit", "project", e.ProjectName, "error", err)
		}
	}
	return changes, nil
}

// Run checks StartAfter after it starts, soon after each Kick and every
// backstop, until ctx ends.
func (w *Switcher) Run(ctx context.Context, backstop time.Duration) {
	w.debouncer().Run(ctx, backstop, func(ctx context.Context, _ []string) {
		if _, err := w.Once(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("Depth goal switch failed", "error", err)
		}
	})
}
