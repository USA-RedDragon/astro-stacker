package ownimage

import (
	"context"
	"fmt"
	"log/slog"
	"math"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
)

const (
	darkSample = 97
	noStar     = "Not measured. There is no catalogued star at this spot, so there is no steady level to measure it against."
)

func measure(ctx context.Context, d Data, object string, spots []Spot, opts Options) ([]Result, error) {
	out := make([]Result, len(spots))
	for i, s := range spots {
		out[i] = Result{SourceID: s.SourceID}
	}
	if len(spots) == 0 {
		return out, nil
	}
	w, width, height, err := d.Frame(ctx, object)
	if err != nil {
		return nil, fmt.Errorf("the target's frame: %w", err)
	}
	subs, err := d.Subs(ctx, object)
	if err != nil {
		return nil, fmt.Errorf("the target's subs: %w", err)
	}
	refSubs := opts.RefSubs
	if refSubs <= 0 {
		refSubs = defaultRefSubs
	}
	st, err := newStamps(opts.WorkDir)
	if err != nil {
		return nil, fmt.Errorf("the stamp file: %w", err)
	}
	defer st.close()
	plans := make([]*spotPlan, len(spots))
	for i, s := range spots {
		plans[i] = newSpotPlan(w, width, height, subs, s, refSubs)
		plans[i].stamps = st
	}
	f, err := newField(ctx, d, w, width, height, plans)
	if err != nil {
		return nil, fmt.Errorf("the target's Gaia stars: %w", err)
	}
	for _, sp := range plans {
		if sp.inFrame {
			f.pickComps(sp)
		}
	}
	f.shapes(plans)
	if err := stream(ctx, d, subs, plans, opts.Threads, opts.Progress); err != nil {
		return nil, err
	}
	evs := make([]*spotEval, len(plans))
	need := map[darkPos]bool{}
	for i, sp := range plans {
		evs[i] = prepare(ctx, subs, sp, need)
	}
	levels := darkLevels(ctx, d, need)
	for i, ev := range evs {
		out[i] = finish(ctx, d, subs, ev, levels)
	}
	return out, nil
}

func skippedAll(why string) []check.Check {
	ids := []string{check.HotPixel, check.RefClean, check.Trail, check.PSF, check.Edge, check.OwnLC, check.OwnChange, check.OwnQuiet}
	out := make([]check.Check, len(ids))
	for i, id := range ids {
		out[i] = check.Skipped(id, why)
	}
	return out
}

// spotEval is a spot's ensemble and reference work, kept until the master
// darks are read.
type spotEval struct {
	sp    *spotPlan
	e     *ensemble
	ref   check.Check
	ncs   []*nightChange
	darks []darkPos
	curve *Curve
}

func prepare(ctx context.Context, subs []Sub, sp *spotPlan, need map[darkPos]bool) *spotEval {
	ev := &spotEval{sp: sp}
	if sp.spot.Kind == app.DiscoveryKindMover || !sp.inFrame {
		return ev
	}
	ev.e = buildEnsemble(subs, sp)
	if sp.star != nil && len(ev.e.kept) >= minPresent {
		ev.curve = buildCurve(subs, sp, ev.e)
		summarize(ev.curve, sp)
	}
	var evals []refEval
	ev.ref, evals = refClean(ctx, subs, sp, ev.e, sp.star != nil && !ownChanged(sp, ev.curve))
	ev.ncs = nightChanges(sp, ev.e, evals)
	ev.darks = darkNeeds(subs, ev.ncs, need)
	return ev
}

// ownChanged says whether the star's own light curve shows the change
// clear of the noise, in the direction reported.
func ownChanged(sp *spotPlan, c *Curve) bool {
	if c == nil || c.ChangeNight == "" {
		return false
	}
	if sp.fadeSign == 0 {
		return math.Abs(c.ChangeSigma) >= changeSigma
	}
	return -float64(sp.fadeSign)*c.ChangeSigma >= changeSigma
}

func finish(ctx context.Context, d Data, subs []Sub, ev *spotEval, levels map[darkPos]float64) Result {
	sp := ev.sp
	res := Result{SourceID: sp.spot.SourceID}
	switch {
	case sp.spot.Kind == app.DiscoveryKindMover:
		res.Checks = skippedAll("Not measured. A moving object is checked along its track, not at one spot.")
		return res
	case !sp.inFrame:
		res.Checks = skippedAll("Not measured yet. The spot is outside the target's frame.")
		return res
	}
	z, zok := 0.0, false
	for _, q := range ev.darks {
		if v, ok := levels[q]; ok {
			z, zok = math.Max(z, v), true
		}
	}
	e, c := ev.e, ev.curve
	res.Curve = c
	res.Checks = append(res.Checks, hotPixel(ev.ncs, z, zok), ev.ref, trail(subs, sp, e, ev.ncs), psf(ctx, d, sp, e), edge(sp))
	if sp.star == nil {
		for _, id := range []string{check.OwnLC, check.OwnChange, check.OwnQuiet} {
			res.Checks = append(res.Checks, check.Skipped(id, noStar))
		}
	} else {
		res.Checks = append(res.Checks, ownLC(sp, c), ownChange(sp, c), ownQuiet(sp, c))
	}
	res.Flags = flagsOf(res.Checks)
	return res
}

func flagsOf(checks []check.Check) Flags {
	var f Flags
	for _, ch := range checks {
		if ch.Status != check.Fail {
			continue
		}
		switch ch.ID {
		case check.HotPixel:
			f.HotPixel = true
		case check.RefClean:
			f.RefContaminated = true
		case check.Trail:
			f.Trail = true
		case check.PSF:
			f.NotStar = true
		case check.Edge:
			f.Edge = true
		}
	}
	return f
}

type darkPos struct {
	key  string
	x, y int
}

// darkLevels reads each master dark under the flagged nights' subs once
// and returns each needed pixel's excess over the dark's typical level, in
// its spreads.
func darkLevels(ctx context.Context, d Data, need map[darkPos]bool) map[darkPos]float64 {
	byKey := map[string][]darkPos{}
	for p := range need {
		byKey[p.key] = append(byKey[p.key], p)
	}
	out := map[darkPos]float64{}
	for key, ps := range byKey {
		b, err := d.Get(ctx, key)
		if err != nil {
			slog.Warn("Could not read a master dark for vetting", "key", key, "error", err)
			continue
		}
		im, err := imagedata.Decode(b)
		if err != nil {
			continue
		}
		p := &discoveries.Plane{W: im.W, H: im.H, Data: im.Plane(0)}
		var sample []float64
		for i := 0; i < len(p.Data); i += darkSample {
			if v := p.Data[i]; !isNaN32(v) {
				sample = append(sample, float64(v))
			}
		}
		med, spread := median(sample), robustStd(sample)
		if !(spread > 0) {
			continue
		}
		for _, q := range ps {
			if v := p.At(q.x, q.y); !isNaN32(v) {
				out[q] = (float64(v) - med) / spread
			}
		}
	}
	return out
}

// darkNeeds are the master dark pixels under the spot in the subs where
// the change shows strongly.
func darkNeeds(subs []Sub, ncs []*nightChange, need map[darkPos]bool) []darkPos {
	var out []darkPos
	for _, nc := range ncs {
		for j, i := range nc.subs {
			if subs[i].DarkKey == nil || !nc.sensed[j] || !nc.isStrong(j) {
				continue
			}
			x, y := int(math.Round(nc.sx[j])), int(math.Round(nc.sy[j]))
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					q := darkPos{*subs[i].DarkKey, x + dx, y + dy}
					need[q] = true
					out = append(out, q)
				}
			}
		}
	}
	return out
}
