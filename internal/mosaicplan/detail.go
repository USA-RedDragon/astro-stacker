package mosaicplan

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/frameheader"
	"github.com/USA-RedDragon/astro-stacker/internal/goals"
	"github.com/USA-RedDragon/astro-stacker/internal/mosaics"
	"github.com/USA-RedDragon/astro-stacker/internal/stacking"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

const (
	RulePanelDeficit     = "Panel Deficit"
	RuleMosaicCompletion = "Mosaic Completion"
	SourceGoal           = "goal"
	SourceTS             = "ts"
	SourceNone           = "none"
	DoneGoal             = "goal"
	DonePlateau          = "plateau"
	DonePlan             = "plan"
	noiseWarn            = 1.5
	gapWarn              = 0.02
)

type PanelFilter struct {
	Filter         string  `json:"filter"`
	Object         string  `json:"object,omitempty"`
	Source         string  `json:"source"`
	Kind           string  `json:"kind,omitempty"`
	Goal           float64 `json:"goal,omitempty"`
	Achieved       float64 `json:"achieved,omitempty"`
	SNR            float64 `json:"snr,omitempty"`
	Progress       float64 `json:"progress"`
	EffectiveHours float64 `json:"effectiveHours"`
	HoursNeeded    float64 `json:"hoursNeeded"`
	PlannedHours   float64 `json:"plannedHours,omitempty"`
	Desired        int     `json:"desired,omitempty"`
	Accepted       int     `json:"accepted,omitempty"`
	Plateau        bool    `json:"plateau,omitempty"`
	Done           bool    `json:"done,omitempty"`
	DoneReason     string  `json:"doneReason,omitempty"`
	LowConfidence  bool    `json:"lowConfidence,omitempty"`
}

type Panel struct {
	Number     int                `json:"number"`
	TargetID   int                `json:"targetId"`
	TargetGUID string             `json:"targetGuid"`
	Target     string             `json:"target"`
	Objects    []string           `json:"objects"`
	Row        int                `json:"row"`
	Col        int                `json:"col"`
	RA         float64            `json:"ra"`
	Dec        float64            `json:"dec"`
	Rotation   float64            `json:"rotation"`
	Footprint  *mosaics.Footprint `json:"footprint"`
	Filters    []PanelFilter      `json:"filters"`
	Progress   float64            `json:"progress"`
	Weakest    string             `json:"weakest"`
	PastGoal   bool               `json:"pastGoal"`
}

type Balancing struct {
	PanelDeficit     float64 `json:"panelDeficit"`
	PanelDeficitSet  bool    `json:"panelDeficitSet"`
	MosaicCompletion float64 `json:"mosaicCompletion"`
	On               bool    `json:"on"`
	OnWeight         float64 `json:"onWeight"`
}

type Project struct {
	ID          int     `json:"id"`
	Priority    int     `json:"priority"`
	State       int     `json:"state"`
	MinimumTime int     `json:"minimumTime"`
	MinAltitude float64 `json:"minimumAltitude"`
}

type Detail struct {
	Project        string                  `json:"project"`
	ProjectGUID    string                  `json:"projectGuid"`
	TS             Project                 `json:"ts"`
	Adopted        bool                    `json:"adopted"`
	Rows           int                     `json:"rows"`
	Cols           int                     `json:"cols"`
	Rotation       float64                 `json:"rotation"`
	Layout         string                  `json:"layout"`
	Filters        []string                `json:"filters"`
	Panels         []Panel                 `json:"panels"`
	Complete       float64                 `json:"complete"`
	Average        float64                 `json:"average"`
	WeakestPanel   int                     `json:"weakestPanel"`
	WeakestFilter  string                  `json:"weakestFilter"`
	EffectiveHours float64                 `json:"effectiveHours"`
	HoursLeft      float64                 `json:"hoursLeft"`
	HoursUnknown   bool                    `json:"hoursUnknown"`
	Balancing      Balancing               `json:"balancing"`
	Seams          []app.MosaicSeam        `json:"seams"`
	Health         []app.MosaicPanelHealth `json:"health"`
	Needs          []string                `json:"needs"`
	Mosaics        []app.Mosaic            `json:"mosaics"`
	Noise          []MosaicNoise           `json:"noise"`
	SeamStatus     []SeamStatus            `json:"seamStatus"`
}

type MosaicNoise struct {
	Filter     string     `json:"filter"`
	Median     *float64   `json:"median"`
	P90        *float64   `json:"p90"`
	Max        *float64   `json:"max"`
	MaxPanel   *int       `json:"maxPanel"`
	Tiles      int        `json:"tiles"`
	MeasuredAt *time.Time `json:"measuredAt"`
}

type SeamStatus struct {
	Filter     string     `json:"filter"`
	Measured   bool       `json:"measured"`
	MeasuredAt *time.Time `json:"measuredAt"`
	Pairs      int        `json:"pairs"`
}

func NoiseAndStatus(ms []app.Mosaic, seams []app.MosaicSeam) ([]MosaicNoise, []SeamStatus) {
	noise := make([]MosaicNoise, 0, len(ms))
	status := make([]SeamStatus, 0, len(ms))
	for _, m := range ms {
		n := MosaicNoise{Filter: m.Filter, Median: m.NoiseMedian, P90: m.NoiseP90, Max: m.NoiseMax, Tiles: m.NoiseTiles, MeasuredAt: m.NoiseAt}
		if m.NoiseMaxPanel > 0 {
			p := m.NoiseMaxPanel
			n.MaxPanel = &p
		}
		noise = append(noise, n)
		st := SeamStatus{Filter: m.Filter}
		for _, sm := range seams {
			if sm.Filter != m.Filter {
				continue
			}
			st.Pairs++
			at := sm.MeasuredAt
			if st.MeasuredAt == nil || at.After(*st.MeasuredAt) {
				st.MeasuredAt = &at
			}
		}
		st.Measured = st.Pairs > 0 || m.SeamSignature != ""
		status = append(status, st)
	}
	return noise, status
}

type Summary struct {
	Project       string  `json:"project"`
	ProjectGUID   string  `json:"projectGuid"`
	Panels        int     `json:"panels"`
	Adopted       bool    `json:"adopted"`
	Complete      float64 `json:"complete"`
	Average       float64 `json:"average"`
	WeakestPanel  int     `json:"weakestPanel"`
	WeakestFilter string  `json:"weakestFilter"`
	SeamWarnings  int     `json:"seamWarnings"`
	Balancing     bool    `json:"balancing"`
}

type planRow struct {
	TargetGUID string
	Filter     string
	Exposure   float64
	Default    float64
	Desired    int
	Accepted   int
	Enabled    int
}

func (s *Service) groups(ctx context.Context) ([]stacking.MosaicGroup, error) {
	return stacking.MosaicGroups(ctx, s.App, s.Sched)
}

func (s *Service) List(ctx context.Context) ([]Summary, error) {
	groups, err := s.groups(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Summary, 0, len(groups))
	for _, g := range groups {
		d, err := s.build(ctx, g)
		if err != nil {
			return nil, err
		}
		sum := Summary{Project: d.Project, ProjectGUID: d.ProjectGUID, Panels: len(d.Panels), Adopted: d.Adopted, Complete: d.Complete,
			Average: d.Average, WeakestPanel: d.WeakestPanel, WeakestFilter: d.WeakestFilter, Balancing: d.Balancing.On}
		for _, sm := range d.Seams {
			if !sm.OK {
				sum.SeamWarnings++
			}
		}
		out = append(out, sum)
	}
	return out, nil
}

func (s *Service) Detail(ctx context.Context, key string) (Detail, error) {
	groups, err := s.groups(ctx)
	if err != nil {
		return Detail{}, err
	}
	for _, g := range groups {
		if g.ProjectGUID == key || g.Project == key {
			return s.build(ctx, g)
		}
	}
	return Detail{}, ErrNotFound
}

func (s *Service) build(ctx context.Context, g stacking.MosaicGroup) (Detail, error) {
	d := Detail{Project: g.Project, ProjectGUID: g.ProjectGUID, Seams: []app.MosaicSeam{}, Health: []app.MosaicPanelHealth{}, Needs: []string{},
		Noise: []MosaicNoise{}, SeamStatus: []SeamStatus{}, Balancing: Balancing{OnWeight: mosaics.PanelDeficitOnWeight}}
	var adopted int64
	if err := s.App.WithContext(ctx).Model(&app.MosaicPanel{}).Where("project_guid = ?", g.ProjectGUID).Count(&adopted).Error; err != nil {
		return d, err
	}
	d.Adopted = adopted > 0
	if err := s.loadTS(ctx, &d); err != nil {
		return d, err
	}
	var objects []string
	for _, pn := range g.Panels {
		objects = append(objects, pn.Objects...)
	}
	var stacks []app.Stack
	if err := s.App.WithContext(ctx).Select("object", "filter", "effective_seconds", "subs").
		Where("object IN ? AND subs > 0", objects).Find(&stacks).Error; err != nil {
		return d, err
	}
	plans, err := s.plans(ctx, g)
	if err != nil {
		return d, err
	}
	filterSet := map[string]bool{}
	for _, st := range stacks {
		filterSet[st.Filter] = true
	}
	for _, p := range plans {
		filterSet[p.Filter] = true
	}
	for f := range filterSet {
		d.Filters = append(d.Filters, f)
	}
	slices.SortFunc(d.Filters, compareFilters)

	best, keys := bestStacks(g, stacks)
	gs, _, err := goals.LoadGoals(ctx, s.App, s.Sched)
	if err != nil {
		gs = nil
	}
	progress, err := goals.Lookup(ctx, s.App, gs, keys)
	if err != nil {
		return d, err
	}
	byTarget := map[string][]planRow{}
	for _, p := range plans {
		byTarget[p.TargetGUID] = append(byTarget[p.TargetGUID], p)
	}

	centres := make([]mosaics.Point, len(g.Panels))
	for i, pn := range g.Panels {
		centres[i] = mosaics.Point{RA: pn.RA, Dec: pn.Dec}
	}
	if len(g.Panels) > 0 {
		d.Rotation = g.Panels[0].Rotation
	}
	rows, cols, footprints := s.panelGeometry(ctx, g, centres, d.Rotation)
	ids, err := s.targetIDs(ctx, g)
	if err != nil {
		return d, err
	}
	for i, pn := range g.Panels {
		p := Panel{Number: pn.Number, TargetID: ids[pn.TargetGUID], TargetGUID: pn.TargetGUID, Target: pn.Object, Objects: pn.Objects, Row: rows[i], Col: cols[i],
			RA: pn.RA, Dec: pn.Dec, Rotation: pn.Rotation, Progress: 1}
		p.Footprint = footprints[i]
		d.Rows, d.Cols = max(d.Rows, rows[i]+1), max(d.Cols, cols[i]+1)
		for _, f := range d.Filters {
			st, have := best[fmt.Sprintf("%d\x00%s", i, f)]
			gp, hasGoal := progress[goals.Key{Object: st.Object, Filter: f}]
			pf, ok := panelFilter(f, st, have, byTarget[pn.TargetGUID], gp, hasGoal && have)
			if !ok {
				continue
			}
			p.Filters = append(p.Filters, pf)
			if c := math.Min(1, pf.Progress); p.Weakest == "" || c < p.Progress {
				p.Progress, p.Weakest = c, f
			}
			d.EffectiveHours += pf.EffectiveHours
			if pf.HoursNeeded >= 0 {
				d.HoursLeft += pf.HoursNeeded
			} else if !pf.Done {
				d.HoursUnknown = true
			}
		}
		if len(p.Filters) == 0 {
			p.Progress = 0
		}
		p.PastGoal = len(p.Filters) > 0 && p.Progress >= 1
		d.Panels = append(d.Panels, p)
	}
	d.Complete, d.WeakestPanel, d.WeakestFilter, d.Average = Completion(d.Panels)
	d.Layout = layoutText(d)

	db := s.App.WithContext(ctx)
	if err := db.Where("project = ?", g.Project).Order("filter, panel_a, panel_b").Find(&d.Seams).Error; err != nil {
		return d, err
	}
	if err := db.Where("project = ?", g.Project).Order("filter, panel").Find(&d.Health).Error; err != nil {
		return d, err
	}
	if err := db.Where("project = ?", g.Project).Order("filter").Find(&d.Mosaics).Error; err != nil {
		return d, err
	}
	d.Noise, d.SeamStatus = NoiseAndStatus(d.Mosaics, d.Seams)
	d.Needs = Needs(d)
	return d, nil
}

func (s *Service) panelGeometry(ctx context.Context, g stacking.MosaicGroup, centres []mosaics.Point, rotation float64) (rows, cols []int, fps []*mosaics.Footprint) {
	rig, _, rigErr := s.rig(ctx)
	rows, cols, fps = make([]int, len(centres)), make([]int, len(centres)), make([]*mosaics.Footprint, len(centres))
	if rigErr == nil {
		rows, cols = mosaics.GridCells(centres, rotation, rig)
	}
	for i, pn := range g.Panels {
		switch {
		case pn.Planned != nil:
			fp := *pn.Planned
			fps[i] = &fp
		case rigErr == nil:
			fp := mosaics.PanelFootprint(centres[i], pn.Rotation, rig)
			fps[i] = &fp
		}
	}
	return rows, cols, fps
}

func bestStacks(g stacking.MosaicGroup, stacks []app.Stack) (map[string]app.Stack, []goals.Key) {
	best := map[string]app.Stack{}
	panelOf := map[string]int{}
	for i, pn := range g.Panels {
		for _, o := range pn.Objects {
			if _, ok := panelOf[o]; !ok {
				panelOf[o] = i
			}
		}
	}
	for _, st := range stacks {
		i, ok := panelOf[st.Object]
		if !ok {
			continue
		}
		k := fmt.Sprintf("%d\x00%s", i, st.Filter)
		if cur, ok := best[k]; !ok || st.EffectiveSeconds > cur.EffectiveSeconds {
			best[k] = st
		}
	}
	keys := make([]goals.Key, 0, len(best))
	for _, st := range best {
		keys = append(keys, goals.Key{Object: st.Object, Filter: st.Filter})
	}
	return best, keys
}

func panelFilter(f string, st app.Stack, have bool, plans []planRow, gp goals.Progress, hasGoal bool) (PanelFilter, bool) {
	pf := PanelFilter{Filter: f, Source: SourceNone, HoursNeeded: -1}
	if have {
		pf.Object, pf.EffectiveHours = st.Object, st.EffectiveSeconds/3600
	}
	for _, pl := range plans {
		if pl.Filter != f || pl.Enabled == 0 {
			continue
		}
		exp := pl.Exposure
		if exp <= 0 {
			exp = pl.Default
		}
		pf.Desired += pl.Desired
		pf.Accepted += pl.Accepted
		pf.PlannedHours += float64(pl.Desired) * exp / 3600
	}
	switch {
	case hasGoal:
		pf.Source, pf.Kind, pf.Goal, pf.Achieved, pf.SNR = SourceGoal, string(gp.Kind), gp.Goal, gp.Achieved, gp.SNR
		pf.Progress, pf.Plateau, pf.Done, pf.LowConfidence = gp.Progress, gp.Plateau, gp.Done, gp.LowConfidence
		switch {
		case gp.Progress >= 1:
			pf.DoneReason = DoneGoal
		case gp.Done:
			pf.DoneReason = DonePlateau
		}
		if !math.IsInf(gp.HoursNeeded, 0) && !math.IsNaN(gp.HoursNeeded) {
			pf.HoursNeeded = gp.HoursNeeded
		}
		if gp.EffectiveHours > 0 {
			pf.EffectiveHours = gp.EffectiveHours
		}
	case pf.Desired > 0:
		pf.Source = SourceTS
		pf.Progress = float64(pf.Accepted) / float64(pf.Desired)
		pf.Done = pf.Accepted >= pf.Desired
		if pf.Done {
			pf.DoneReason = DonePlan
		}
		pf.HoursNeeded = math.Max(0, pf.PlannedHours*(1-math.Min(1, pf.Progress)))
	case !have:
		return pf, false
	}
	return pf, true
}

func Completion(panels []Panel) (complete float64, weakestPanel int, weakestFilter string, average float64) {
	if len(panels) == 0 {
		return 0, 0, "", 0
	}
	complete = math.Inf(1)
	var sum float64
	for _, p := range panels {
		sum += p.Progress
		if p.Progress < complete {
			complete, weakestPanel, weakestFilter = p.Progress, p.Number, p.Weakest
		}
	}
	return complete, weakestPanel, weakestFilter, sum / float64(len(panels))
}

func layoutText(d Detail) string {
	if len(d.Panels) == 0 {
		return ""
	}
	full := d.Rows * d.Cols
	shape := fmt.Sprintf("%d × %d", d.Cols, d.Rows)
	if full != len(d.Panels) {
		shape = fmt.Sprintf("%d panels over %d × %d", len(d.Panels), d.Cols, d.Rows)
	}
	return fmt.Sprintf("%s at %.0f°", shape, d.Rotation)
}

func Needs(d Detail) []string {
	out := []string{}
	byNumber := map[int]Panel{}
	for _, p := range d.Panels {
		byNumber[p.Number] = p
	}
	hours := func(n int, filter string) float64 {
		for _, f := range byNumber[n].Filters {
			if f.Filter == filter {
				return f.EffectiveHours
			}
		}
		return 0
	}
	type need struct {
		panel  int
		filter string
		hours  float64
		ratio  float64
	}
	worst := map[string]need{}
	for _, sm := range d.Seams {
		if sm.NoiseRatio <= noiseWarn {
			continue
		}
		noisier := sm.PanelA
		if sm.NoiseB > sm.NoiseA {
			noisier = sm.PanelB
		}
		h := hours(noisier, sm.Filter) * (sm.NoiseRatio*sm.NoiseRatio - 1)
		k := fmt.Sprintf("%d\x00%s", noisier, sm.Filter)
		if cur, ok := worst[k]; !ok || h > cur.hours {
			worst[k] = need{noisier, sm.Filter, h, sm.NoiseRatio}
		}
	}
	needs := make([]need, 0, len(worst))
	for _, n := range worst {
		needs = append(needs, n)
	}
	slices.SortFunc(needs, func(a, b need) int {
		if a.panel != b.panel {
			return a.panel - b.panel
		}
		return strings.Compare(a.filter, b.filter)
	})
	for _, n := range needs {
		if n.hours > 0 {
			out = append(out, fmt.Sprintf("Panel %d needs about +%.1f h %s (noise ratio %.1f vs neighbours)", n.panel, n.hours, n.filter, n.ratio))
		} else {
			out = append(out, fmt.Sprintf("Panel %d is noisier in %s (noise ratio %.1f vs neighbours)", n.panel, n.filter, n.ratio))
		}
	}
	seen := map[int]bool{}
	for _, h := range d.Health {
		if h.GapFraction <= gapWarn || seen[h.Panel] {
			continue
		}
		seen[h.Panel] = true
		where := h.GapWhere
		if where == "" {
			where = "edge"
		}
		out = append(out, fmt.Sprintf("Panel %d gap of %.2f deg² (%.0f%%) at the %s: re-frame or shift", h.Panel, h.GapDeg2, h.GapFraction*100, where))
	}
	return out
}

func compareFilters(a, b string) int {
	order := map[string]int{"Luminance": 0, "L": 0, "Red": 1, "R": 1, "Green": 2, "G": 2, "Blue": 3, "B": 3, "H-a": 4, "O-III": 5, "S-II": 6}
	oa, okA := order[a]
	ob, okB := order[b]
	switch {
	case okA && okB && oa != ob:
		return oa - ob
	case okA != okB:
		if okA {
			return -1
		}
		return 1
	}
	return strings.Compare(a, b)
}

func (s *Service) loadTS(ctx context.Context, d *Detail) error {
	var rows []struct {
		ID              int
		Priority        *int
		State           *int
		MinimumTime     *int
		MinimumAltitude *float64
	}
	if err := s.Sched.WithContext(ctx).Table("project").
		Select(`"Id" AS id, priority, state, minimumtime AS minimum_time, minimumaltitude AS minimum_altitude`).
		Where("guid = ?", d.ProjectGUID).Scan(&rows).Error; err != nil {
		return fmt.Errorf("load project: %w", err)
	}
	if len(rows) == 0 {
		return nil
	}
	r := rows[0]
	d.TS = Project{ID: r.ID, Priority: deref(r.Priority), State: deref(r.State), MinimumTime: deref(r.MinimumTime), MinAltitude: deref(r.MinimumAltitude)}
	var weights []struct {
		Name   string
		Weight float64
	}
	if err := s.Sched.WithContext(ctx).Table("ruleweight").Select("name, weight").Where("projectid = ?", r.ID).Scan(&weights).Error; err != nil {
		return fmt.Errorf("load rule weights: %w", err)
	}
	for _, w := range weights {
		switch w.Name {
		case RulePanelDeficit:
			d.Balancing.PanelDeficit, d.Balancing.PanelDeficitSet = w.Weight, true
		case RuleMosaicCompletion:
			d.Balancing.MosaicCompletion = w.Weight
		}
	}
	d.Balancing.On = d.Balancing.PanelDeficit > 0
	return nil
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

func (s *Service) targetIDs(ctx context.Context, g stacking.MosaicGroup) (map[string]int, error) {
	var guids []string
	for _, pn := range g.Panels {
		if pn.TargetGUID != "" {
			guids = append(guids, pn.TargetGUID)
		}
	}
	out := map[string]int{}
	if len(guids) == 0 {
		return out, nil
	}
	var rows []struct {
		ID   int
		GUID string
	}
	if err := s.Sched.WithContext(ctx).Table("target").Select(`"Id" AS id, guid`).Where("guid IN ?", guids).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("load target ids: %w", err)
	}
	for _, r := range rows {
		out[r.GUID] = r.ID
	}
	return out, nil
}

func (s *Service) plans(ctx context.Context, g stacking.MosaicGroup) ([]planRow, error) {
	var guids []string
	for _, pn := range g.Panels {
		if pn.TargetGUID != "" {
			guids = append(guids, pn.TargetGUID)
		}
	}
	if len(guids) == 0 {
		return nil, nil
	}
	var rows []struct {
		TargetGUID string
		Filter     string
		Exposure   *float64
		Default    *float64
		Desired    *int
		Accepted   *int
		Enabled    *int
	}
	if err := s.Sched.WithContext(ctx).Table("exposureplan ep").
		Select(`t.guid AS target_guid, et.filtername AS filter, ep.exposure AS exposure, et.defaultexposure AS "default", `+
			`ep.desired AS desired, ep.accepted AS accepted, ep.enabled AS enabled`).
		Joins(`JOIN target t ON t."Id" = ep.targetid`).
		Joins(`JOIN exposuretemplate et ON et."Id" = ep."exposureTemplateId"`).
		Where("t.guid IN ?", guids).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("load exposure plans: %w", err)
	}
	out := make([]planRow, 0, len(rows))
	for _, r := range rows {
		enabled := 1
		if r.Enabled != nil {
			enabled = *r.Enabled
		}
		out = append(out, planRow{TargetGUID: r.TargetGUID, Filter: frameheader.NormalizeFilter(r.Filter), Exposure: deref(r.Exposure),
			Default: deref(r.Default), Desired: deref(r.Desired), Accepted: deref(r.Accepted), Enabled: enabled})
	}
	return out, nil
}
