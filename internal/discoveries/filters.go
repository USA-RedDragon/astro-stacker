package discoveries

import (
	"fmt"
	"log/slog"
	"math"
	"slices"
)

// filterDiff is one filter's own comparison of the night with its
// reference, beside the filters' combined one, and the changes only it
// found.
type filterDiff struct {
	fs    *filterStacks
	res   *Result
	diff  *Plane
	cands []cand
}

// filterMatchPx is how near a filter's change must be to one already found
// to be the same.
const filterMatchPx = 2.0

// signAgreeSigma is how strongly, in sigma over its subs, another filter
// may measure a filter's change with the opposite sign.
const signAgreeSigma = 2.0

// filterChanges finds, in each filter's own difference, the changes the
// filters combined missed: the noise-weighted sum dilutes a source of one
// colour. AT 2026aeob, of R 17.6 in TNS, on Pelican Nebula Panel 2's night
// of 2026-10-08, stood at 4.8 sigma in R, G and B combined, and its forced
// photometry at 6.2 sigma in R, 2.7 in G and 3.4 in B. A filter needs
// minSubs subs on the night, and the night two filters; only allows the
// filters it lists, when not nil.
func (in *Input) filterChanges(stacks []*filterStacks, have []cand, valid, edge, bmask []bool, base *Result, threads, minSubs int, only map[string]float64) []filterDiff {
	if len(stacks) < 2 {
		return nil
	}
	taken := make([][2]float64, 0, len(have))
	for _, c := range have {
		taken = append(taken, [2]float64{c.X, c.Y})
	}
	var out []filterDiff
	for _, fs := range stacks {
		if fs.night == nil || fs.ref == nil || len(fs.f.Night) < minSubs {
			continue
		}
		if only != nil && !(only[fs.f.Filter] > 0) {
			continue
		}
		r := &Result{Funnel: map[string]Funnel{}, PixelScale: base.PixelScale, NightSubs: len(fs.f.Night), ZeroPoint: fs.zp}
		in.stage(fmt.Sprintf("matching %s star profiles", fs.f.Filter))
		D, err := difference(fs.night, fs.ref, slices.Clone(valid), fs.f.Anchors, threads, r)
		if err != nil {
			slog.Info("Filter comparison skipped", "filter", fs.f.Filter, "reason", err)
			continue
		}
		mesh := Background(D, 64, nil)
		mesh.Subtract(D)
		r.ApSigma = emptyApertureSigma(D, edge, apertureR, emptyApertures)
		r.NightApSigma = emptyApertureSigma(fs.night, sourceMask(fs.ref, edge), apertureR, emptyApertures)
		if !(r.ApSigma > 0) || !(r.NightApSigma > 0) {
			continue
		}
		in.stage(fmt.Sprintf("finding changes in %s", fs.f.Filter))
		var keep []cand
		for _, c := range in.findChanges(D, mesh.RMSPlane(), fs.ref, edge, bmask, r) {
			if slices.ContainsFunc(taken, func(p [2]float64) bool { return math.Hypot(p[0]-c.X, p[1]-c.Y) <= filterMatchPx }) {
				continue
			}
			c.Filter = fs.f.Filter
			keep = append(keep, c)
			taken = append(taken, [2]float64{c.X, c.Y})
		}
		if len(keep) > 0 {
			out = append(out, filterDiff{fs: fs, res: r, diff: D, cands: keep})
		}
	}
	return out
}

// only is the candidate measured in its filter's subs alone.
func (c *cand) only(filter string) *cand {
	o := &cand{Candidate: c.Candidate, wide: c.wide, refNights: c.refNights, refLight: c.refLight}
	for k, pt := range c.Light {
		if pt.Filter != filter || k >= len(c.fluxes) {
			continue
		}
		o.fluxes, o.errs = append(o.fluxes, c.fluxes[k]), append(o.errs, c.errs[k])
		o.Light = append(o.Light, pt)
		if k < len(c.sharp) {
			o.sharp = append(o.sharp, c.sharp[k])
		}
		if k < len(c.sensor) {
			o.sensor = append(o.sensor, c.sensor[k])
		}
		if k < len(c.off) {
			o.off = append(o.off, c.off[k])
		}
	}
	return o
}

// against says which other filter measured a filter's change with the
// opposite sign, or "".
func (c *cand) against() string {
	type acc struct{ sum, varSum float64 }
	per := map[string]*acc{}
	var order []string
	for k, pt := range c.Light {
		if pt.Filter == c.Filter || k >= len(c.fluxes) {
			continue
		}
		a := per[pt.Filter]
		if a == nil {
			a = &acc{}
			per[pt.Filter] = a
			order = append(order, pt.Filter)
		}
		a.sum += c.fluxes[k]
		a.varSum += c.errs[k] * c.errs[k]
	}
	for _, f := range order {
		a := per[f]
		if !(a.varSum > 0) {
			continue
		}
		if z := a.sum / math.Sqrt(a.varSum); z < -signAgreeSigma {
			return fmt.Sprintf("%s in %s: %.1f sigma", signName(-c.Sign), f, -z)
		}
	}
	return ""
}

// withFilterChanges adds the filters' own changes to cands, and says the
// noise of each filter's own comparison.
func withFilterChanges(cands []cand, fds []filterDiff) ([]cand, map[string]float64) {
	own := map[string]float64{}
	for _, fd := range fds {
		cands = append(cands, fd.cands...)
		own[fd.fs.f.Filter] = fd.res.ApSigma
	}
	return cands, own
}

// splitByFilter parts candidates into those of the combined comparison
// and those of each filter's own, in the order of fds.
func splitByFilter(cands []cand, fds []filterDiff) ([]cand, [][]cand) {
	var combined []cand
	per := make([][]cand, len(fds))
	idx := map[string]int{}
	for i, fd := range fds {
		idx[fd.fs.f.Filter] = i
	}
	for _, c := range cands {
		if i, ok := idx[c.Filter]; ok && c.Filter != "" {
			per[i] = append(per[i], c)
		} else {
			combined = append(combined, c)
		}
	}
	return combined, per
}
