package stacking

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

// A master can mix subs taken at different camera gains: Orion H-a has 64
// subs of 120 s at gain 0 and 40 of 600 s at gain 100. A higher gain records
// more ADU per electron, and the stack normalizes subs by exposure alone, so
// per second the gain-100 subs read 3.05× the gain-0 ones (median over 2386
// unsaturated stars, IQR 2.84-3.10; nebula 3.08). Where the 600 s subs
// saturate only the 120 s ones fill in, and against a gain-0-only stack the
// master's H-a core read 1.00 where the nebula around it read 2.42: the core
// was 2.4× too dim.
//
// Each gain's subs are stacked flat and their starlight measured against
// the gain holding most of the weight; a sub then counts as if exposed that
// much longer or shorter. Orion's gain 0 measures 0.323, so a 120 s sub
// there records the ADU of a 39 s one at gain 100, and the core reads 3.10
// against 3.01 around it, within 3%. Its weight, from its score and real
// exposure, stays: the gain scales signal and noise alike, unlike haze,
// which dims only the signal and so costs weight (see fold).

// gainMethod is how masters mixing gains are put on one gain. 1: each gain
// is scaled by its starlight against the gain with most weight.
const gainMethod = 1

// Bounds on a plausible gain scale. Wider than haze's: a gain step can be
// far bigger than any transparency change.
const (
	minGainScale = 1.0 / 100
	maxGainScale = 100
)

// gainTable is a master's scale per camera gain, the ADU per second its
// subs record against those of the reference gain.
type gainTable map[float64]float64

func (t gainTable) encode() string {
	if len(t) == 0 {
		return ""
	}
	m := make(map[string]float64, len(t))
	for g, s := range t {
		m[strconv.FormatFloat(g, 'f', -1, 64)] = s
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func decodeGainTable(s string) gainTable {
	if s == "" {
		return nil
	}
	var m map[string]float64
	if json.Unmarshal([]byte(s), &m) != nil {
		return nil
	}
	t := make(gainTable, len(m))
	for k, v := range m {
		if g, err := strconv.ParseFloat(k, 64); err == nil && v > 0 {
			t[g] = v
		}
	}
	return t
}

// scale is a sub's scale for gain, 1 for an unknown gain or a table without
// it. A light whose header had no gain is taken to be at the reference.
func (t gainTable) scale(gain *float64) float64 {
	if gain == nil {
		return 1
	}
	if s, ok := t[*gain]; ok {
		return s
	}
	return 1
}

// measureGains measures each gain's scale from its subs stacked flat, one
// accumulator per gain. The gain with the most weight is the reference, 1.
// A gain whose starlight can't be measured keeps 1, as before.
func measureGains(groups map[float64]*Accumulator) gainTable {
	gains := make([]float64, 0, len(groups))
	for g := range groups {
		gains = append(gains, g)
	}
	slices.Sort(gains)
	ref := gains[0]
	for _, g := range gains[1:] {
		if groups[g].WeightSum > groups[ref].WeightSum {
			ref = g
		}
	}
	t := gainTable{ref: 1}
	r := groups[ref]
	for _, g := range gains {
		if g == ref {
			continue
		}
		a := groups[g]
		// Only where every sub of the gain holds the pixel: where some
		// saturate, the mean holds only the fainter ones.
		var full float32
		for _, c := range a.Count {
			full = max(full, c)
		}
		cells := skyCells(func(i int) float32 {
			if a.Count[i] < full {
				return 0
			}
			return a.Mean[i]
		}, a.W, a.H, 1, r, math.MaxFloat32)
		s, ok := gainRatio(cells)
		if !ok || s < minGainScale || s > maxGainScale {
			slog.Warn("Could not measure a gain's flux against the master's reference gain", "gain", g, "reference", ref, "cells", len(cells), "ratio", s)
			s = 1
		}
		t[g] = s
	}
	return t
}

// gainRatio is fluxRatio for a gain, which unlike haze can be far from 1:
// fluxRatio judges the noise from how far the faint cells' fluxes differ,
// and at 3× they differ by the signal itself. So the brighter half's median
// ratio comes first, and fluxRatio refines what is left of it.
func gainRatio(cells []skyCell) (float64, bool) {
	if len(cells) < 16 {
		return 0, false
	}
	byRef := slices.Clone(cells)
	slices.SortFunc(byRef, func(a, b skyCell) int { return cmpFloat(a.fref, b.fref) })
	var rough []float64
	for _, c := range byRef[len(byRef)/2:] {
		if c.fref > 0 {
			rough = append(rough, c.fsub/c.fref)
		}
	}
	if len(rough) < 8 {
		return 0, false
	}
	slices.Sort(rough)
	s0 := rough[len(rough)/2]
	if !(s0 > 0) {
		return 0, false
	}
	s, ok := fluxRatio(func(i int) (float64, float64) { return cells[i].fsub / s0, cells[i].fref }, len(cells))
	return s * s0, ok
}

// gainScales measures the gains of a master's subs (see measureGains) when
// they mix, reading every sub once and stacking each gain flat. It returns
// nil for a single gain.
func (p *Pipeline) gainScales(ctx context.Context, dir string, stack *app.Stack, subs []storedSub, gains []*float64) (gainTable, error) {
	distinct := map[float64]bool{}
	for _, g := range gains {
		if g != nil {
			distinct[*g] = true
		}
	}
	if len(distinct) < 2 {
		return nil, nil
	}
	opts := p.opts.Stack
	opts.MinSamples = math.MaxFloat32
	opts.LocalNorm = false
	groups := map[float64]*Accumulator{}
	for i, s := range subs {
		if gains[i] == nil {
			continue
		}
		p.progress(stack.Object, stack.Filter, StageRebuilding, i, len(subs))
		local := filepath.Join(dir, fmt.Sprintf("g%04d.fit", i))
		if err := p.download(ctx, p.dest, s.key, local); err != nil {
			return nil, err
		}
		sub, w, h, err := readSub(local)
		os.Remove(local)
		if err != nil {
			return nil, err
		}
		a := groups[*gains[i]]
		if a == nil {
			a = NewAccumulator(w, h)
			groups[*gains[i]] = a
		}
		if _, err := a.Add(sub, s.exposure, s.weight, opts); err != nil {
			return nil, err
		}
	}
	t := measureGains(groups)
	slog.Info("Measured the gains of a master mixing them", "object", stack.Object, "filter", stack.Filter, "scales", t.encode())
	return t, nil
}

// batchGains is the gain table for adding subs of gains to stack
// incrementally. ok is false when the master will mix gains that its last
// rebuild didn't measure, and must be rebuilt.
func (p *Pipeline) batchGains(ctx context.Context, stack *app.Stack, gains []*float64) (gainTable, bool, error) {
	var held []float64
	if err := p.db.WithContext(ctx).Table("stack_frames sf").
		Joins("JOIN frames f ON f.id = sf.frame_id").
		Where("sf.stack_id = ? AND sf.status = ? AND f.gain IS NOT NULL", stack.ID, app.StackStatusAdded).
		Distinct().Pluck("f.gain", &held).Error; err != nil {
		return nil, false, err
	}
	distinct := map[float64]bool{}
	for _, g := range held {
		distinct[g] = true
	}
	for _, g := range gains {
		if g != nil {
			distinct[*g] = true
		}
	}
	if len(distinct) < 2 {
		return nil, true, nil
	}
	t := decodeGainTable(stack.GainScales)
	if stack.GainMethod != gainMethod {
		return nil, false, nil
	}
	for g := range distinct {
		if _, ok := t[g]; !ok {
			return nil, false, nil
		}
	}
	return t, true, nil
}

// markMixedGains marks masters mixing gains that weren't rebuilt with the
// current gainMethod for the moon sweep to restack.
func (p *Pipeline) markMixedGains(ctx context.Context) {
	res := p.db.WithContext(ctx).Model(&app.Stack{}).
		Where("state_key IS NOT NULL AND (gain_method IS NULL OR gain_method < ?)", gainMethod).
		Where("(SELECT COUNT(DISTINCT f.gain) FROM stack_frames sf JOIN frames f ON f.id = sf.frame_id "+
			"WHERE sf.stack_id = stacks.id AND sf.status = ?) > 1", app.StackStatusAdded).
		UpdateColumn("needs_rebuild", true)
	if res.Error != nil {
		slog.Warn("Could not mark masters mixing gains for restacking", "error", res.Error)
	} else if res.RowsAffected > 0 {
		slog.Info("Marked masters mixing gains for restacking", "masters", res.RowsAffected)
	}
}
