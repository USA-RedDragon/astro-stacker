package mosaics

import (
	"fmt"
	"math"
	"sort"
)

type Outline struct {
	Centre      Point   `json:"centre"`
	MajorArcmin float64 `json:"majorArcmin"`
	MinorArcmin float64 `json:"minorArcmin"`
	PADeg       float64 `json:"paDeg"`
}

type PlannedPanel struct {
	N           int       `json:"n"`
	Row         int       `json:"row"`
	Col         int       `json:"col"`
	Centre      Point     `json:"centre"`
	RotationDeg float64   `json:"rotationDeg"`
	Footprint   Footprint `json:"footprint"`
	Covers      float64   `json:"covers"`
}

type Layout struct {
	ID          string         `json:"id"`
	Kind        string         `json:"kind"`
	Name        string         `json:"name"`
	Detail      string         `json:"detail"`
	Rows        int            `json:"rows"`
	Cols        int            `json:"cols"`
	RotationDeg float64        `json:"rotationDeg"`
	OverlapPct  float64        `json:"overlapPct"`
	Coverage    float64        `json:"coverage"`
	Panels      []PlannedPanel `json:"panels"`
}

const (
	KindSingle = "single"
	KindGrid   = "grid"
	KindBrick  = "brick"
	KindStrip  = "strip"

	fullCoverage  = 0.98
	singleFits    = 0.95
	cheapCoverage = 0.85
	dropShare     = 0.02
	maxSide       = 6
	maxOptions    = 5
)

type shape struct {
	centre  Point
	a, b    float64
	pa      float64
	point   bool
	samples []vec2
}

func newShape(o Outline) shape {
	s := shape{centre: o.Centre, a: o.MajorArcmin / 120, b: o.MinorArcmin / 120, pa: o.PADeg}
	if s.a <= 0 {
		s.point = true
		s.samples = []vec2{{}}
		return s
	}
	if s.b <= 0 {
		s.b = s.a
	}
	if s.b > s.a {
		s.a, s.b, s.pa = s.b, s.a, s.pa+90
	}
	const n = 64
	for i := range n {
		for j := range n {
			p := vec2{s.a * (2*(float64(i)+0.5)/n - 1), s.a * (2*(float64(j)+0.5)/n - 1)}
			if s.contains(p) {
				s.samples = append(s.samples, p)
			}
		}
	}
	if len(s.samples) == 0 {
		s.samples = []vec2{{}}
	}
	return s
}

func (s shape) contains(p vec2) bool {
	if s.point {
		return p.x == 0 && p.y == 0
	}
	along := p.x*sinD(s.pa) + p.y*cosD(s.pa)
	across := p.x*cosD(s.pa) - p.y*sinD(s.pa)
	return (along*along)/(s.a*s.a)+(across*across)/(s.b*s.b) <= 1
}

func (s shape) elongated() bool {
	return !s.point && s.a >= 2*s.b
}

type spec struct {
	kind              string
	rows, cols        int
	rotation, overlap float64
	rig               Rig
}

func (sp spec) panels(centre Point) []PlannedPanel {
	stepX := sp.rig.WidthDeg * (1 - sp.overlap/100)
	stepY := sp.rig.HeightDeg * (1 - sp.overlap/100)
	out := make([]PlannedPanel, 0, sp.rows*sp.cols)
	for r := range sp.rows {
		for c := range sp.cols {
			dx := (float64(sp.cols-1)/2 - float64(c)) * stepX
			dy := (float64(sp.rows-1)/2 - float64(r)) * stepY
			if sp.kind == KindBrick && sp.rows > 1 {
				if r%2 == 0 {
					dx += stepX / 4
				} else {
					dx -= stepX / 4
				}
			}
			p := Offset(centre, sp.rotation, dx, dy)
			rot := localRotation(centre, p, sp.rotation, dx, dy)
			out = append(out, PlannedPanel{
				N: len(out) + 1, Row: r, Col: c, Centre: p, RotationDeg: rot,
				Footprint: PanelFootprint(p, rot, sp.rig),
			})
		}
	}
	return out
}

func (s shape) evaluate(panels []PlannedPanel) (coverage float64, shares []float64) {
	polys := make([]polygon, len(panels))
	counts := make([]int, len(panels))
	for i, p := range panels {
		polys[i] = projectFootprint(s.centre, p.Footprint)
	}
	hit := 0
	for _, v := range s.samples {
		in := false
		for i, q := range polys {
			if q.contains(v) {
				counts[i]++
				in = true
			}
		}
		if in {
			hit++
		}
	}
	shares = make([]float64, len(panels))
	for i := range panels {
		shares[i] = float64(counts[i]) / float64(len(s.samples))
		panels[i].Covers = s.panelCovers(polys[i], counts[i] > 0)
	}
	return float64(hit) / float64(len(s.samples)), shares
}

func (s shape) panelCovers(p polygon, hit bool) float64 {
	if s.point {
		if hit {
			return 1
		}
		return 0
	}
	const nu, nv = 24, 16
	in := 0
	for i := range nu {
		for j := range nv {
			if s.contains(bilinear(p, (float64(i)+0.5)/nu, (float64(j)+0.5)/nv)) {
				in++
			}
		}
	}
	return float64(in) / (nu * nv)
}

func (s shape) layout(sp spec, prune bool) Layout {
	sp.rows, sp.cols = max(sp.rows, 1), max(sp.cols, 1)
	if sp.rows == 1 && sp.cols == 1 {
		sp.kind = KindSingle
	}
	panels := sp.panels(s.centre)
	coverage, shares := s.evaluate(panels)
	dropped := 0
	if prune {
		kept := make([]PlannedPanel, 0, len(panels))
		for i, p := range panels {
			if shares[i] >= dropShare {
				kept = append(kept, p)
			}
		}
		if len(kept) > 0 && len(kept) < len(panels) {
			dropped = len(panels) - len(kept)
			for i := range kept {
				kept[i].N = i + 1
			}
			panels = kept
			coverage, _ = s.evaluate(panels)
		}
	}
	l := Layout{
		Kind: sp.kind, Rows: sp.rows, Cols: sp.cols, RotationDeg: sp.rotation, OverlapPct: sp.overlap,
		Coverage: coverage, Panels: panels,
	}
	l.ID, l.Name = naming(sp.kind, sp.rows, sp.cols, len(panels))
	l.Detail = detail(coverage, dropped)
	return l
}

func naming(kind string, rows, cols, n int) (id, name string) {
	switch kind {
	case KindSingle:
		return "single", "One frame"
	case KindBrick:
		return fmt.Sprintf("brick-%dx%d", rows, cols), fmt.Sprintf("Brick, %d panels", n)
	case KindStrip:
		return fmt.Sprintf("strip-%dx%d", rows, cols), fmt.Sprintf("Strip, %d panels", n)
	default:
		return fmt.Sprintf("grid-%dx%d", rows, cols), fmt.Sprintf("Grid %d × %d", cols, rows)
	}
}

func detail(coverage float64, dropped int) string {
	d := fmt.Sprintf("Covers %d%% of the outline", int(math.Floor(coverage*100+1e-9)))
	switch {
	case dropped == 1:
		d += ", with 1 panel that misses the object dropped"
	case dropped > 1:
		d += fmt.Sprintf(", with %d panels that miss the object dropped", dropped)
	}
	return d
}

func Grid(o Outline, rows, cols int, rotationDeg, overlapPct float64, rig Rig) Layout {
	return newShape(o).layout(spec{kind: KindGrid, rows: rows, cols: cols, rotation: rotationDeg, overlap: overlapPct, rig: rig}, false)
}

func Brick(o Outline, rows, cols int, rotationDeg, overlapPct float64, rig Rig) Layout {
	return newShape(o).layout(spec{kind: KindBrick, rows: rows, cols: cols, rotation: rotationDeg, overlap: overlapPct, rig: rig}, false)
}

func score(l Layout) float64 {
	return float64(len(l.Panels)) + 4*(1-l.Coverage)
}

func smallest(ls []Layout) (best Layout, reached bool) {
	for _, l := range ls {
		if l.Coverage < fullCoverage {
			continue
		}
		if !reached || len(l.Panels) < len(best.Panels) ||
			(len(l.Panels) == len(best.Panels) && l.Coverage > best.Coverage) {
			best, reached = l, true
		}
	}
	if reached {
		return best, true
	}
	for i, l := range ls {
		if i == 0 || l.Coverage > best.Coverage {
			best = l
		}
	}
	return best, false
}

func (s shape) candidates(kind string, rotation, overlap float64, rig Rig) []Layout {
	var out []Layout
	add := func(r, c int) {
		l := s.layout(spec{kind: kind, rows: r, cols: c, rotation: rotation, overlap: overlap, rig: rig}, true)
		if kind != KindBrick || isBrick(l) {
			out = append(out, l)
		}
	}
	switch kind {
	case KindStrip:
		for n := 2; n <= maxSide; n++ {
			add(1, n)
			add(n, 1)
		}
	case KindBrick:
		for r := 2; r <= maxSide; r++ {
			for c := 2; c <= maxSide; c++ {
				add(r, c)
			}
		}
	default:
		for r := 1; r <= maxSide; r++ {
			for c := 1; c <= maxSide; c++ {
				add(r, c)
			}
		}
	}
	return out
}

func isBrick(l Layout) bool {
	rows := map[int]bool{}
	for _, p := range l.Panels {
		rows[p.Row] = true
	}
	return len(rows) >= 2 && len(l.Panels) >= 3
}

func Alternatives(o Outline, rotationDeg, overlapPct float64, rig Rig) []Layout {
	s := newShape(o)
	grids := s.candidates(KindGrid, rotationDeg, overlapPct, rig)
	single := grids[0]
	if s.point {
		return []Layout{single}
	}
	var opts []Layout
	if single.Coverage >= singleFits {
		opts = append(opts, single)
	}
	all := append([]Layout(nil), grids...)
	if s.elongated() {
		strips := s.candidates(KindStrip, rotationDeg, overlapPct, rig)
		all = append(all, strips...)
		strip, _ := smallest(strips)
		opts = append(opts, strip)
	}
	grid, _ := smallest(grids)
	opts = append(opts, grid)
	bricks := s.candidates(KindBrick, rotationDeg, overlapPct, rig)
	all = append(all, bricks...)
	if brick, ok := smallest(bricks); ok && similar(len(brick.Panels), len(grid.Panels)) {
		opts = append(opts, brick)
	}
	if cheap, ok := cheaper(all, len(grid.Panels)); ok {
		opts = append(opts, cheap)
	}
	return rank(opts)
}

func similar(brick, grid int) bool {
	return grid >= 3 && 2*brick <= 3*grid
}

func cheaper(all []Layout, than int) (Layout, bool) {
	var best Layout
	found := false
	for _, l := range all {
		if len(l.Panels) >= than || l.Coverage < cheapCoverage {
			continue
		}
		if !found || score(l) < score(best) {
			best, found = l, true
		}
	}
	return best, found
}

func rank(opts []Layout) []Layout {
	sort.SliceStable(opts, func(i, j int) bool { return score(opts[i]) < score(opts[j]) })
	out := make([]Layout, 0, len(opts))
	for _, l := range opts {
		dup := false
		for _, k := range out {
			if k.ID == l.ID || (len(k.Panels) == len(l.Panels) && math.Abs(k.Coverage-l.Coverage) < 0.01) {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, l)
		}
	}
	if len(out) > maxOptions {
		out = out[:maxOptions]
	}
	return out
}

func SuggestRotation(o Outline, overlapPct float64, rig Rig) float64 {
	s := newShape(o)
	if s.point {
		return 0
	}
	best, bestCost := 0.0, math.Inf(1)
	for _, r := range []float64{0, norm180(o.PADeg - 90), norm180(o.PADeg)} {
		l, ok := smallest(s.candidates(KindGrid, r, overlapPct, rig))
		cost := float64(len(l.Panels))
		if !ok {
			cost = 1e6 - l.Coverage
		}
		if cost < bestCost-1e-9 {
			best, bestCost = r, cost
		}
	}
	return best
}

func norm180(a float64) float64 {
	a = math.Mod(a, 180)
	if a < 0 {
		a += 180
	}
	if a >= 180-1e-9 {
		a = 0
	}
	return a
}

func localRotation(centre, panel Point, rotationDeg, dx, dy float64) float64 {
	if dx == 0 && dy == 0 {
		return wrap360(rotationDeg)
	}
	ahead := Offset(centre, rotationDeg, dx, dy+0.01)
	xi, eta := Project(panel, ahead)
	return wrap360(math.Round(math.Atan2(xi, eta)/deg*1e6) / 1e6)
}
