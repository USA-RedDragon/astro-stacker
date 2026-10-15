package discoveries

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sort"
)

// ChangeNull is the night's change null test: the night's subs split into
// two interleaved halves, one half compared with the other by the same
// method. Nothing truly changes between them, so every change it finds is
// a false alarm of the method on this field and night. Found counts them,
// MaxSNR is the strongest in the full comparison's noise, and Subs the subs
// split.
type ChangeNull struct {
	Subs    int
	Found   int
	MaxSNR  float64
	Changes []NullChange
}

// NullChange is one change the null test found, its SNR in the full
// comparison's noise.
type NullChange struct {
	RA, Dec, SNR float64
}

// DropKnown leaves out of the null test the changes on known variable
// stars, which can truly change within a night (an eclipse, an RR Lyrae's
// rise): in Barnard's E's field, rich in them, most of the null test's
// changes were catalogued variables.
func (n *ChangeNull) DropKnown(vars []Variable) {
	n.Found, n.MaxSNR = 0, 0
	for _, c := range n.Changes {
		known := false
		for _, v := range vars {
			lim := vsxMatchArcsec
			if v.Catalog == catalogSIMBAD {
				lim = simbadMatchArcsec
			}
			if Separation(c.RA, c.Dec, v.RA, v.Dec)*3600 <= lim {
				known = true
				break
			}
		}
		if !known {
			n.Found++
			n.MaxSNR = math.Max(n.MaxSNR, c.SNR)
		}
	}
}

// nullMinSubs is the fewest subs a filter needs on the night to be split.
const nullMinSubs = 4

// nullChanges runs the change null test. Halves too poor to match star
// profiles give no test, not a failed comparison.
func (in *Input) nullChanges(ctx context.Context, stacks []*filterStacks, edge, bmask []bool, res *Result, threads int) (ChangeNull, error) {
	var out ChangeNull
	var halves []*filterStacks
	for _, fs := range stacks {
		var subs []Sub
		for _, s := range fs.f.Night {
			if !fs.cache.m[s.FrameID].bad {
				subs = append(subs, s)
			}
		}
		if len(subs) < nullMinSubs {
			continue
		}
		sort.Slice(subs, func(a, b int) bool { return subs[a].Start.Before(subs[b].Start) })
		var a, b []Sub
		for i, s := range subs {
			if i%2 == 0 {
				a = append(a, s)
			} else {
				b = append(b, s)
			}
		}
		h := &filterStacks{f: FilterInput{Filter: fs.f.Filter, Anchors: fs.f.Anchors, Night: a, Ref: b}, cache: fs.cache, load: fs.load, zp: fs.zp, zpWide: fs.zpWide}
		in.stage(fmt.Sprintf("null test: stacking %s halves (%d and %d subs)", fs.f.Filter, len(a), len(b)))
		var err error
		if h.night, err = ClippedMean(ctx, in.W, in.H, len(a), stackLoader(h.load, a), threads); err != nil {
			return out, err
		}
		if h.ref, err = ClippedMean(ctx, in.W, in.H, len(b), stackLoader(h.load, b), threads); err != nil {
			return out, err
		}
		h.noise = emptyApertureSigma(h.night, nil, apertureR, 4000)
		halves = append(halves, h)
		out.Subs += len(subs)
	}
	if len(halves) == 0 {
		return out, nil
	}
	nightA, refB, valid := combine(halves, in.W, in.H)
	r0 := &Result{Funnel: map[string]Funnel{}, PixelScale: res.PixelScale, NightApSigma: res.NightApSigma}
	for _, h := range halves {
		r0.NightSubs += len(h.f.Night)
	}
	in.stage("null test: matching star profiles")
	D, err := difference(nightA, refB, valid, halves[0].f.Anchors, threads, r0)
	if err != nil {
		slog.Info("Change null test skipped", "reason", err)
		return ChangeNull{}, nil
	}
	dmesh := Background(D, 64, nil)
	dmesh.Subtract(D)
	r0.ApSigma = emptyApertureSigma(D, edge, apertureR, emptyApertures)
	if !(r0.ApSigma > 0) || !(res.ApSigma > 0) {
		return out, nil
	}
	cands := in.findChanges(D, dmesh.RMSPlane(), refB, edge, bmask, r0)
	for _, h := range halves {
		for _, s := range h.f.Night {
			p, _, err := h.load(ctx, s)
			if err != nil {
				return out, err
			}
			for i, v := range p.Data {
				r := h.ref.Data[i]
				if isNaN32(v) || isNaN32(r) || !valid[i] {
					p.Data[i] = float32(math.NaN())
				} else {
					p.Data[i] = v - r
				}
			}
			Background(p, 64, nil).Subtract(p)
			sig := emptyApertureSigma(p, edge, apertureR, 4000)
			sigWide := emptyApertureSigma(p, edge, residualR, 4000)
			ns := nightSub{fs: h, sub: s}
			for ci := range cands {
				if cands[ci].wide {
					lightPoint(&cands[ci], p, ns, residualR, sigWide, h.zpWide)
				} else {
					lightPoint(&cands[ci], p, ns, apertureR, sig, h.zp)
				}
			}
		}
	}
	local := in.localResiduals(refB, D, edge, r0)
	for _, c := range in.cut(cands, refB, D, local, r0) {
		if c.LowConf {
			continue
		}
		snr := c.SNR * r0.ApSigma / res.ApSigma
		out.Changes = append(out.Changes, NullChange{RA: c.RA, Dec: c.Dec, SNR: snr})
		out.Found++
		out.MaxSNR = math.Max(out.MaxSNR, snr)
	}
	return out, nil
}
