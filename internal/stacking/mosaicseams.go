package stacking

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/events"
	"github.com/USA-RedDragon/astro-stacker/internal/mosaics"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
)

const (
	seamLevelWarn   = 0.2
	seamStepWarn    = 0.3
	seamNoiseWarn   = 1.5
	seamSamplesWarn = 2 * minOverlap
	seamStrips      = 8
	gapWarn         = 0.02
	regWarn         = 1.5
	colourWarn      = 0.1
	seamMethod      = 2
	colourProblem   = "star colour differs by "
)

type seamMeasure struct {
	I, J        int
	Fit         overlapFit
	NoiseA      float64
	NoiseB      float64
	Profile     []float64
	StarMatches int
	RegMedian   float64
	RegP90      float64
	FluxRatio   float64
}

func measureSeams(binned []binnedPanel, stars [][]seamStar, pairs []panelPair) []seamMeasure {
	out := make([]seamMeasure, 0, len(pairs))
	for _, p := range pairs {
		m := seamMeasure{I: p.I, J: p.J, Fit: p.Fit, RegMedian: math.NaN(), RegP90: math.NaN(), FluxRatio: math.NaN()}
		m.NoiseA, m.NoiseB, m.Profile = seamProfile(binned[p.I], binned[p.J])
		if p.I < len(stars) && p.J < len(stars) {
			m.StarMatches, m.RegMedian, m.RegP90, m.FluxRatio = starStats(matchSeamStars(binned[p.I], binned[p.J], stars[p.I], stars[p.J]))
		}
		out = append(out, m)
	}
	return out
}

func seamSignature(sig string) string {
	return fmt.Sprintf("%s/%d", sig, seamMethod)
}

func finite(v float64) *float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	return &v
}

func seamProfile(a, b binnedPanel) (noiseA, noiseB float64, profile []float64) {
	type sample struct{ x, y, d float64 }
	var s []sample
	var na, nb []float64
	for by := range min(a.H, b.H) {
		for bx := range min(a.W, b.W) {
			ia, ib := by*a.W+bx, by*b.W+bx
			va, vb := a.Data[ia], b.Data[ib]
			if math.IsNaN(float64(va)) || math.IsNaN(float64(vb)) {
				continue
			}
			x, y := a.coords(bx, by)
			s = append(s, sample{x, y, float64(va) - float64(vb)})
			if len(a.Noise) > ia && len(b.Noise) > ib && !math.IsNaN(float64(a.Noise[ia])) && !math.IsNaN(float64(b.Noise[ib])) {
				na, nb = append(na, float64(a.Noise[ia])), append(nb, float64(b.Noise[ib]))
			}
		}
	}
	if len(na) > 0 {
		noiseA, noiseB = median(na), median(nb)
	}
	if len(s) < seamStrips {
		return noiseA, noiseB, nil
	}
	var mx, my float64
	for _, p := range s {
		mx, my = mx+p.x, my+p.y
	}
	mx, my = mx/float64(len(s)), my/float64(len(s))
	var sxx, syy, sxy float64
	for _, p := range s {
		dx, dy := p.x-mx, p.y-my
		sxx, syy, sxy = sxx+dx*dx, syy+dy*dy, sxy+dx*dy
	}
	theta := 0.5 * math.Atan2(2*sxy, sxx-syy)
	ux, uy := math.Cos(theta), math.Sin(theta)
	across := make([]float64, len(s))
	lo, hi := math.Inf(1), math.Inf(-1)
	for i, p := range s {
		across[i] = -(p.x-mx)*uy + (p.y-my)*ux
		lo, hi = math.Min(lo, across[i]), math.Max(hi, across[i])
	}
	strips := make([][]float64, seamStrips)
	for i, p := range s {
		k := 0
		if hi > lo {
			k = min(seamStrips-1, int((across[i]-lo)/(hi-lo)*seamStrips))
		}
		strips[k] = append(strips[k], p.d)
	}
	for _, st := range strips {
		if len(st) == 0 {
			profile = append(profile, math.NaN())
			continue
		}
		profile = append(profile, median(st))
	}
	return noiseA, noiseB, profile
}

type seamPanel struct {
	Number int
	Target string
	Object string
}

func seamRecord(project, guid, filter string, a, b seamPanel, m seamMeasure) app.MosaicSeam {
	bg := (m.NoiseA + m.NoiseB) / 2
	r := app.MosaicSeam{Project: project, ProjectGUID: guid, Filter: filter, PanelA: a.Number, PanelB: b.Number, TargetA: a.Target, TargetB: b.Target,
		Difference: m.Fit.Mean, SlopeX: m.Fit.Plane.Bx, SlopeY: m.Fit.Plane.By, NoiseA: m.NoiseA, NoiseB: m.NoiseB, Samples: m.Fit.Samples}
	if bg > 0 {
		r.Level = math.Abs(m.Fit.Mean) / bg
		lo, hi := math.Inf(1), math.Inf(-1)
		for _, v := range m.Profile {
			if !math.IsNaN(v) {
				lo, hi = math.Min(lo, v), math.Max(hi, v)
			}
		}
		if hi >= lo {
			r.Step = (hi - lo) / bg
		}
	}
	if m.NoiseA > 0 && m.NoiseB > 0 {
		r.NoiseRatio = math.Max(m.NoiseA, m.NoiseB) / math.Min(m.NoiseA, m.NoiseB)
	}
	prof := make([]*float64, len(m.Profile))
	for i, v := range m.Profile {
		if !math.IsNaN(v) && bg > 0 {
			x := v / bg
			prof[i] = &x
		}
	}
	if b, err := json.Marshal(prof); err == nil {
		r.Profile = string(b)
	}
	var problems []string
	if r.Level > seamLevelWarn {
		problems = append(problems, fmt.Sprintf("level differs by %.2fσ", r.Level))
	}
	if r.Step > seamStepWarn {
		problems = append(problems, fmt.Sprintf("gradient step of %.2fσ across the seam", r.Step))
	}
	if r.NoiseRatio > seamNoiseWarn {
		noisier := a.Number
		if m.NoiseB > m.NoiseA {
			noisier = b.Number
		}
		problems = append(problems, fmt.Sprintf("panel %d is %.1f× noisier", noisier, r.NoiseRatio))
	}
	if r.Samples < seamSamplesWarn {
		problems = append(problems, fmt.Sprintf("overlap too small: %d blocks", r.Samples))
	}
	r.StarMatches = m.StarMatches
	if m.StarMatches >= minSeamStars {
		r.RegMedian, r.RegP90, r.FluxRatio = finite(m.RegMedian), finite(m.RegP90), finite(m.FluxRatio)
		if m.RegP90 > regWarn {
			problems = append(problems, fmt.Sprintf("stars misregistered by %.1f px (p90) across the seam", m.RegP90))
		}
	}
	r.OK = len(problems) == 0
	r.Problems = strings.Join(problems, "; ")
	return r
}

func plannedFootprint(pn panel, solved mosaics.Footprint) mosaics.Footprint {
	if pn.Planned != nil {
		return *pn.Planned
	}
	return mosaics.PanelFootprint(mosaics.Point{RA: pn.RA, Dec: pn.Dec}, pn.Rotation, mosaics.FootprintRig(solved))
}

func actualFootprint(g wcs) mosaics.Footprint {
	var fp mosaics.Footprint
	for i, c := range g.corners() {
		fp[i] = mosaics.Point{RA: c[0], Dec: c[1]}
	}
	return fp
}

func (p *Pipeline) solvedFootprints(dir string, n int) []*mosaics.Footprint {
	out := make([]*mosaics.Footprint, n)
	for i := range n {
		kw, err := readKeywords(filepath.Join(dir, fmt.Sprintf("pan_%05d.fit", i+1)))
		if err != nil {
			continue
		}
		g, err := wcsFromHeader(kw, int(kw.Float("NAXIS1")), int(kw.Float("NAXIS2")))
		if err != nil {
			continue
		}
		fp := actualFootprint(g)
		out[i] = &fp
	}
	return out
}

type seamResult struct {
	seams  []seamMeasure
	actual []*mosaics.Footprint
	noise  mosaicNoise
}

type seamNoise struct {
	Median, P90, Max float64
	MaxPanel, Tiles  int
}

func seamPanels(g mosaicGroup, masters []app.Stack) []seamPanel {
	byObject := map[string]panel{}
	for _, pn := range g.Panels {
		byObject[pn.Object] = pn
	}
	out := make([]seamPanel, len(masters))
	for i, m := range masters {
		pn := byObject[m.Object]
		out[i] = seamPanel{Number: pn.Number, Target: pn.TargetGUID, Object: m.Object}
	}
	return out
}

func seamRecords(g mosaicGroup, filter string, masters []app.Stack, res seamResult, sig string, now time.Time) ([]app.MosaicSeam, []app.MosaicPanelHealth, seamNoise) {
	sp := seamPanels(g, masters)
	seams := make([]app.MosaicSeam, 0, len(res.seams))
	for _, m := range res.seams {
		a, b := sp[m.I], sp[m.J]
		if a.Number > b.Number {
			a, b = b, a
			m.NoiseA, m.NoiseB, m.Fit.Mean = m.NoiseB, m.NoiseA, -m.Fit.Mean
			m.Fit.Plane = plane{A: -m.Fit.Plane.A, Bx: -m.Fit.Plane.Bx, By: -m.Fit.Plane.By}
			m.FluxRatio = 1 / m.FluxRatio
			m.Profile = slices.Clone(m.Profile)
			for k := range m.Profile {
				m.Profile[k] = -m.Profile[k]
			}
		}
		r := seamRecord(g.Project, g.ProjectGUID, filter, a, b, m)
		r.Signature, r.MeasuredAt = sig, now
		seams = append(seams, r)
	}
	slices.SortFunc(seams, func(x, y app.MosaicSeam) int {
		if x.PanelA != y.PanelA {
			return x.PanelA - y.PanelA
		}
		return x.PanelB - y.PanelB
	})
	noise := map[int][]float64{}
	for _, m := range res.seams {
		noise[m.I] = append(noise[m.I], m.NoiseA)
		noise[m.J] = append(noise[m.J], m.NoiseB)
	}
	var actual []mosaics.Footprint
	for _, fp := range res.actual {
		if fp != nil {
			actual = append(actual, *fp)
		}
	}
	byObject := map[string]panel{}
	for _, pn := range g.Panels {
		byObject[pn.Object] = pn
	}
	health := make([]app.MosaicPanelHealth, 0, len(masters))
	for i, m := range masters {
		pn := byObject[m.Object]
		h := app.MosaicPanelHealth{Project: g.Project, ProjectGUID: g.ProjectGUID, Filter: filter, Panel: pn.Number, TargetGUID: pn.TargetGUID,
			Object: m.Object, Signature: sig, MeasuredAt: now}
		if len(noise[i]) > 0 {
			h.Noise = median(slices.Clone(noise[i]))
		}
		if i < len(res.noise.Scales) {
			h.FluxScale = finite(res.noise.Scales[i])
		}
		if i < len(res.actual) && res.actual[i] != nil && len(actual) > 0 {
			gap := mosaics.CoverageGap(plannedFootprint(pn, *res.actual[i]), actual)
			h.GapFraction, h.GapDeg2, h.GapWhere = gap.Fraction, gap.AreaDeg2, gap.Where
		}
		health = append(health, h)
	}
	n := seamNoise{Median: res.noise.Median, P90: res.noise.P90, Max: res.noise.Max, Tiles: res.noise.Tiles}
	if k := res.noise.MaxPanel; k >= 0 && k < len(sp) {
		n.MaxPanel = sp[k].Number
	}
	return seams, health, n
}

func (n seamNoise) apply(m *app.Mosaic, now time.Time) {
	m.NoiseMedian, m.NoiseP90, m.NoiseMax = finite(n.Median), finite(n.P90), finite(n.Max)
	m.NoiseMaxPanel, m.NoiseTiles = n.MaxPanel, n.Tiles
	if n.Tiles > 0 {
		m.NoiseAt = &now
	} else {
		m.NoiseAt = nil
	}
}

func (n seamNoise) columns(now time.Time) map[string]any {
	var m app.Mosaic
	n.apply(&m, now)
	return map[string]any{"noise_median": m.NoiseMedian, "noise_p90": m.NoiseP90, "noise_max": m.NoiseMax,
		"noise_max_panel": m.NoiseMaxPanel, "noise_tiles": m.NoiseTiles, "noise_at": m.NoiseAt}
}

func (p *Pipeline) saveSeams(ctx context.Context, g mosaicGroup, filter string, seams []app.MosaicSeam, health []app.MosaicPanelHealth) error {
	return p.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("project = ? AND filter = ?", g.Project, filter).Delete(&app.MosaicSeam{}).Error; err != nil {
			return err
		}
		if err := tx.Where("project = ? AND filter = ?", g.Project, filter).Delete(&app.MosaicPanelHealth{}).Error; err != nil {
			return err
		}
		if len(seams) > 0 {
			if err := tx.Create(&seams).Error; err != nil {
				return err
			}
		}
		if len(health) > 0 {
			if err := tx.Create(&health).Error; err != nil {
				return err
			}
		}
		return updateSeamColours(tx, g.Project)
	})
}

func updateSeamColours(tx *gorm.DB, project string) error {
	var rows []app.MosaicSeam
	if err := tx.Where("project = ?", project).Find(&rows).Error; err != nil {
		return err
	}
	byPair := map[[2]int][]int{}
	for i, r := range rows {
		k := [2]int{r.PanelA, r.PanelB}
		byPair[k] = append(byPair[k], i)
	}
	for _, idx := range byPair {
		colours := seamColours(rows, idx)
		for _, i := range idx {
			r := rows[i]
			c := colours[i]
			var ref string
			if c != nil {
				ref = colourReference(rows, idx)
			}
			problems := withColourProblem(r.Problems, c)
			if equalPtr(r.Colour, c) && r.ColourRef == ref && r.Problems == problems {
				continue
			}
			if err := tx.Model(&app.MosaicSeam{}).Where("id = ?", r.ID).Updates(map[string]any{
				"colour": c, "colour_ref": ref, "problems": problems, "ok": problems == "",
			}).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

func seamColours(rows []app.MosaicSeam, idx []int) map[int]*float64 {
	out := map[int]*float64{}
	var logs []float64
	var with []int
	for _, i := range idx {
		r := rows[i]
		if r.StarMatches >= minSeamStars && r.FluxRatio != nil && *r.FluxRatio > 0 {
			logs = append(logs, math.Log(*r.FluxRatio))
			with = append(with, i)
		}
	}
	if len(with) < 2 {
		return out
	}
	var mean float64
	for _, l := range logs {
		mean += l
	}
	mean /= float64(len(logs))
	for k, i := range with {
		v := math.Exp(logs[k]-mean) - 1
		out[i] = &v
	}
	return out
}

func colourReference(rows []app.MosaicSeam, idx []int) string {
	var fs []string
	for _, i := range idx {
		if rows[i].StarMatches >= minSeamStars && rows[i].FluxRatio != nil {
			fs = append(fs, rows[i].Filter)
		}
	}
	slices.Sort(fs)
	return strings.Join(fs, ",")
}

func withColourProblem(problems string, c *float64) string {
	var keep []string
	for p := range strings.SplitSeq(problems, "; ") {
		if p != "" && !strings.HasPrefix(p, colourProblem) {
			keep = append(keep, p)
		}
	}
	if c != nil && math.Abs(*c) > colourWarn {
		keep = append(keep, fmt.Sprintf("%s%+.0f%% from the other filters", colourProblem, *c*100))
	}
	return strings.Join(keep, "; ")
}

func equalPtr(a, b *float64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return math.Abs(*a-*b) < 1e-12
}

func (p *Pipeline) seamsDue() bool {
	if !p.opts.MosaicSeams || p.stacking() {
		return false
	}
	if p.seamBudget > 0 {
		p.seamBudget--
		return true
	}
	return false
}

func (p *Pipeline) stacking() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.busy) > 0
}

func (p *Pipeline) measureSeamsOnly(ctx context.Context, g mosaicGroup, filter string, masters []app.Stack, mosaic *app.Mosaic, sig string) error {
	if len(masters) < 2 {
		return p.db.WithContext(ctx).Model(mosaic).Update("seam_signature", seamSignature(sig)).Error
	}
	name := "Mosaic seams: " + g.Project
	p.progress(name, filter, StageAssembling, 0, len(masters)+1)
	defer p.finished(name)
	dir, err := os.MkdirTemp(p.workDir, "seams-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if err := p.preparePanels(ctx, dir, g, filter, masters); err != nil {
		return p.db.WithContext(ctx).Model(mosaic).Update("seam_signature", seamSignature(sig)).Error
	}
	res, err := p.registerAndMeasure(ctx, dir, len(masters), false)
	if err != nil {
		return err
	}
	now := time.Now()
	seams, health, noise := seamRecords(g, filter, masters, res, sig, now)
	if err := p.saveSeams(ctx, g, filter, seams, health); err != nil {
		return err
	}
	cols := noise.columns(now)
	cols["seam_signature"] = seamSignature(sig)
	if err := p.db.WithContext(ctx).Model(mosaic).Updates(cols).Error; err != nil {
		return err
	}
	if p.Events != nil {
		p.Events.Publish(events.Event{Type: events.TypeMosaic, Object: g.Project, Filter: filter})
	}
	return nil
}

func (p *Pipeline) registerAndMeasure(ctx context.Context, dir string, n int, rewrite bool) (seamResult, error) {
	res := seamResult{actual: p.solvedFootprints(dir, n)}
	sat := p.opts.Stack.SaturationLevel
	for i := range n {
		if err := flattenPanel(filepath.Join(dir, fmt.Sprintf("pan_%05d.fit", i+1)), sat); err != nil {
			return res, fmt.Errorf("panel %d: flatten: %w", i+1, err)
		}
	}
	if n < 2 {
		return res, nil
	}
	if _, err := p.siril.Run(ctx, dir, p.sirilPreamble(true)+
		"seqplatesolve pan -nocache\n"+
		"seqapplyreg pan -framing=max\n"); err != nil {
		return res, err
	}
	registered, err := filepath.Glob(filepath.Join(dir, "r_pan_*.fit"))
	if err != nil {
		return res, err
	}
	slices.Sort(registered)
	_, _, rep, err := matchRegisteredSeams(registered, sat, rewrite)
	if err != nil {
		return res, fmt.Errorf("match panels: %w", err)
	}
	res.seams, res.noise = rep.seams, rep.noise
	return res, nil
}
