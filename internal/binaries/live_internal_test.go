package binaries

import (
	"math"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/ownimage"
)

func TestLiveViewLevelsAndDrawsTheEclipse(t *testing.T) {
	t.Parallel()
	m := liveModel()
	run := &liveRun{model: &m, from: liveMid.Add(-130 * time.Minute), to: liveMid.Add(70 * time.Minute),
		view: LiveCurve{Kind: LiveEclipse, Eclipse: &LiveEclipseView{Mid: m.mid, Depth: m.depth}}}
	var raw []ownimage.LivePoint
	for _, p := range synthRun(-130, 60, 3*time.Minute, 0.45, 0.02) {
		raw = append(raw, ownimage.LivePoint{FrameID: p.FrameID, Time: p.Time, Filter: "R", Mag: p.Mag, Err: p.Err, Comp: p.Comp})
	}
	raw = append(raw, ownimage.LivePoint{FrameID: 99, Time: liveMid, Filter: "G", Mag: 13})
	v := liveView(run, raw, 11, liveMid.Add(70*time.Minute))
	if v.State != LiveFollowing || v.Filter != "R" || v.OtherSubs != 1 || len(v.Points) != len(raw)-1 || v.Comps != 11 {
		t.Fatalf("state %s (%s) filter %s other %d points %d comps %d", v.State, v.Why, v.Filter, v.OtherSubs, len(v.Points), v.Comps)
	}
	e := v.Eclipse
	if e.ShiftMin == nil || math.Abs(*e.ShiftMin-3) > 3 || e.FitDepth == nil || math.Abs(*e.FitDepth-0.45) > 0.04 {
		t.Errorf("fit %+v", e)
	}
	if len(e.Curve) != curveSamples+1 || !e.Curve[0].T.Equal(liveMid.Add(-130*time.Minute)) {
		t.Fatalf("curve of %d from %s", len(e.Curve), e.Curve[0].T)
	}
	for _, c := range e.Curve {
		if c.Lo > c.V+1e-9 || c.Hi < c.V-1e-9 || c.V < 0 || c.V > m.depth+1e-9 {
			t.Fatalf("sample %+v", c)
		}
	}
	var out []float64
	for _, p := range v.Points {
		if m.at(p.Time, 3*time.Minute) == 0 {
			out = append(out, p.Mag)
		}
	}
	if len(out) < 3 || math.Abs(medianOf(out)) > 0.02 {
		t.Errorf("subs outside the dip sit at %.3f, want 0", medianOf(out))
	}
	if v.LastSub == nil || !v.LastSub.Equal(liveMid.Add(60*time.Minute)) {
		t.Errorf("last sub %v", v.LastSub)
	}
}

func TestLiveViewDipWatch(t *testing.T) {
	t.Parallel()
	w := app.DipWatch{Start: liveMid, End: liveMid.Add(2 * time.Hour), DepthLow: 0.15, DepthHigh: 0.6, ActiveShare: 0.25}
	run := &liveRun{dip: &w, view: LiveCurve{Kind: LiveDipWatch, Dip: &LiveDipView{Start: w.Start, End: w.End}}}
	var raw []ownimage.LivePoint
	for _, p := range synthWatch(30, 0.03, 0.3, 12, 25) {
		raw = append(raw, ownimage.LivePoint{FrameID: p.FrameID, Time: p.Time, Filter: p.Filter, Mag: p.Mag, Err: p.Err, Comp: p.Comp})
	}
	v := liveView(run, raw[:4], 9, liveMid)
	if v.State != LiveEarly || v.Dip.Latest != nil || v.Dip.Smallest != nil {
		t.Errorf("four subs: state %s latest %v", v.State, v.Dip.Latest)
	}
	v = liveView(run, raw, 9, liveMid)
	d := v.Dip
	if v.State != LiveDip || d.DipStart == nil || !d.DipStart.Equal(liveMid.Add(36*time.Minute)) || d.DipDepth == nil || math.Abs(*d.DipDepth-0.3) > 0.04 {
		t.Fatalf("state %s (%s) dip %+v", v.State, v.Why, d)
	}
	if d.Latest == nil || math.Abs(*d.Latest) > 0.05 || d.OverMin == nil || *d.OverMin != 12 {
		t.Errorf("latest %v over %v", d.Latest, d.OverMin)
	}
}

func TestDepthInPicksTheBand(t *testing.T) {
	t.Parallel()
	d := map[string]float64{"ZTF r": 0.4, "ZTF g": 0.5, "TESS": 0.3}
	for _, c := range []struct {
		filter, band string
		depth        float64
	}{{"Red", "r", 0.4}, {"G", "g", 0.5}, {"Ha", "", 0.4}} {
		if v, b := depthIn(d, c.filter); math.Abs(v-c.depth) > 1e-9 || b != c.band {
			t.Errorf("%s: %.2f in %q, want %.2f in %q", c.filter, v, b, c.depth, c.band)
		}
	}
	if s := surveysOf(d); s != "TESS and ZTF" {
		t.Errorf("surveys %q", s)
	}
}
