package stacking

import (
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/USA-RedDragon/astro-stacker/internal/frameheader"
	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
)

// plane is a + bx·x + by·y over a canvas, x and y running -0.5 to 0.5
// across it, rows from the top.
type plane struct{ A, Bx, By float64 }

func (p plane) at(x, y float64) float64 { return p.A + p.Bx*x + p.By*y }

// matchBin is the block size registered panels are compared in: fine
// enough to follow a gradient, coarse enough to average the noise out.
const matchBin = 8

// binnedPanel is a registered panel averaged over matchBin blocks; a block
// that isn't wholly covered by unsaturated data, or has a neighbour that
// isn't (the registration border), is NaN.
type binnedPanel struct {
	W, H         int // blocks
	fullW, fullH int // canvas pixels
	Data         []float32
	Noise        []float32
}

// placement is where a registered panel lies on the canvas they share: its
// top-left pixel's column and row, and the canvas's size.
type placement struct{ X, Y, CW, CH int }

// placements puts registered panels on one canvas from their plate
// solutions. Siril's -framing=max writes each panel cropped to its own
// data, on one projection shared by all, with the reference pixel moved to
// match, so where a panel's pixels go is its reference pixel's offset.
func placements(headers []frameheader.Keywords, sizes [][2]int) ([]placement, error) {
	if len(headers) == 0 {
		return nil, nil
	}
	ref := headers[0]
	for i, kw := range headers {
		for _, k := range []string{"CRVAL1", "CRVAL2", "CRPIX1", "CRPIX2"} {
			if math.IsNaN(kw.Float(k)) {
				return nil, fmt.Errorf("panel %d has no %s", i+1, k)
			}
		}
		for _, k := range []string{"CRVAL1", "CRVAL2", "CDELT1", "CDELT2", "CD1_1", "CD1_2", "CD2_1", "CD2_2", "PC1_1", "PC1_2", "PC2_1", "PC2_2"} {
			a, b := kw.Float(k), ref.Float(k)
			if math.IsNaN(a) != math.IsNaN(b) || math.Abs(a-b) > 1e-9*math.Max(1, math.Abs(b)) {
				return nil, fmt.Errorf("panel %d isn't on panel 1's projection: %s %v, %v", i+1, k, a, b)
			}
		}
	}
	// FITS pixel (X, Y), rows from the bottom, is at X − CRPIX1, Y − CRPIX2
	// on the shared projection; row r from the top is Y = H − r.
	left, top := math.Inf(1), math.Inf(-1)
	for i, kw := range headers {
		left = math.Min(left, 1-kw.Float("CRPIX1"))
		top = math.Max(top, float64(sizes[i][1])-kw.Float("CRPIX2"))
	}
	out := make([]placement, len(headers))
	var cw, ch int
	for i, kw := range headers {
		out[i].X = int(math.Round(1 - kw.Float("CRPIX1") - left))
		out[i].Y = int(math.Round(top - (float64(sizes[i][1]) - kw.Float("CRPIX2"))))
		cw, ch = max(cw, out[i].X+sizes[i][0]), max(ch, out[i].Y+sizes[i][1])
	}
	for i := range out {
		out[i].CW, out[i].CH = cw, ch
	}
	return out, nil
}

// binRegistered bins a registered panel, w×h top row first, on its canvas.
func binRegistered(data []float32, w, h int, at placement, sat float32) binnedPanel {
	b := binnedPanel{W: at.CW / matchBin, H: at.CH / matchBin, fullW: at.CW, fullH: at.CH}
	raw := make([]float32, b.W*b.H)
	b.Noise = make([]float32, b.W*b.H)
	for by := range b.H {
		for bx := range b.W {
			var s, d float32
			x0, y0 := bx*matchBin-at.X, by*matchBin-at.Y
			ok := x0 >= 0 && y0 >= 0 && x0+matchBin <= w && y0+matchBin <= h
			for y := y0; y < y0+matchBin && ok; y++ {
				row := data[y*w+x0 : y*w+x0+matchBin]
				for i, v := range row {
					if v == 0 || v >= sat {
						ok = false
						break
					}
					s += v
					if i > 0 {
						d += float32(math.Abs(float64(v - row[i-1])))
					}
				}
			}
			raw[by*b.W+bx] = float32(math.NaN())
			b.Noise[by*b.W+bx] = float32(math.NaN())
			if ok {
				raw[by*b.W+bx] = s / (matchBin * matchBin)
				b.Noise[by*b.W+bx] = d / (matchBin * (matchBin - 1)) * float32(math.Sqrt(math.Pi)/2)
			}
		}
	}
	b.Data = make([]float32, len(raw))
	nan := float32(math.NaN())
	for by := range b.H {
		for bx := range b.W {
			v := raw[by*b.W+bx]
			for _, d := range [4][2]int{{-1, 0}, {1, 0}, {0, -1}, {0, 1}} {
				x, y := bx+d[0], by+d[1]
				if x < 0 || y < 0 || x >= b.W || y >= b.H || math.IsNaN(float64(raw[y*b.W+x])) {
					v = nan
					break
				}
			}
			b.Data[by*b.W+bx] = v
		}
	}
	return b
}

// coords is a block's centre in canvas coordinates.
func (b binnedPanel) coords(bx, by int) (x, y float64) {
	return (float64(bx)+0.5)*matchBin/float64(b.fullW) - 0.5, (float64(by)+0.5)*matchBin/float64(b.fullH) - 0.5
}

// overlapFit is the plane of one panel's sky less another's where both
// cover the canvas.
type overlapFit struct {
	Plane   plane
	Mean    float64 // the difference at the overlap's centre
	Samples int     // blocks kept after clipping
	Sigma   float64 // scatter of the kept blocks about the plane
}

// minOverlap is how many blocks two panels must share to be matched.
const minOverlap = 200

// fitDifference fits a plane to a − b over the blocks both cover. Stars,
// which never line up to the block, and anything else more than 2.5σ off
// the plane are left out, over and again. Across an overlap much narrower
// than it is long the slope is barely measured, and it would be carried
// across whole panels, so there only the offset and the slope along the
// overlap are fitted.
func fitDifference(a, b binnedPanel) (overlapFit, bool) {
	type sample struct{ x, y, v float64 }
	var s []sample
	for by := range min(a.H, b.H) {
		for bx := range min(a.W, b.W) {
			va, vb := a.Data[by*a.W+bx], b.Data[by*b.W+bx]
			if math.IsNaN(float64(va)) || math.IsNaN(float64(vb)) {
				continue
			}
			x, y := a.coords(bx, by)
			s = append(s, sample{x, y, float64(va) - float64(vb)})
		}
	}
	if len(s) < minOverlap {
		return overlapFit{}, false
	}
	// The overlap's axes.
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
	ux, uy := math.Cos(theta), math.Sin(theta) // along the overlap
	var along, across float64
	for _, p := range s {
		t, q := (p.x-mx)*ux+(p.y-my)*uy, -(p.x-mx)*uy+(p.y-my)*ux
		along, across = along+t*t, across+q*q
	}
	strip := across < 0.0625*along // narrower than a quarter of its length
	basis := func(p sample) []float64 {
		dx, dy := p.x-mx, p.y-my
		if strip {
			return []float64{1, dx*ux + dy*uy}
		}
		return []float64{1, dx, dy}
	}
	var coef []float64
	var sigma float64
	for range 8 {
		if len(s) < minOverlap {
			return overlapFit{}, false
		}
		rows := make([][]float64, len(s))
		vals := make([]float64, len(s))
		for i, p := range s {
			rows[i], vals[i] = basis(p), p.v
		}
		var ok bool
		if coef, ok = leastSquares(rows, vals); !ok {
			return overlapFit{}, false
		}
		res := make([]float64, len(s))
		for i := range s {
			var m float64
			for k, r := range rows[i] {
				m += r * coef[k]
			}
			res[i] = math.Abs(vals[i] - m)
		}
		sigma = madToSigma * median(slices.Clone(res))
		kept := s[:0:0]
		for i, p := range s {
			if res[i] <= 2.5*sigma {
				kept = append(kept, p)
			}
		}
		if len(kept) == len(s) {
			break
		}
		s = kept
	}
	var pl plane
	if strip {
		pl = plane{A: coef[0] - coef[1]*(mx*ux+my*uy), Bx: coef[1] * ux, By: coef[1] * uy}
	} else {
		pl = plane{A: coef[0] - coef[1]*mx - coef[2]*my, Bx: coef[1], By: coef[2]}
	}
	return overlapFit{Plane: pl, Mean: coef[0], Samples: len(s), Sigma: sigma}, true
}

// leastSquares solves rows·coef ≈ vals by the normal equations.
func leastSquares(rows [][]float64, vals []float64) ([]float64, bool) {
	if len(rows) == 0 {
		return nil, false
	}
	n := len(rows[0])
	m := make([][]float64, n)
	for i := range m {
		m[i] = make([]float64, n+1)
	}
	for r, row := range rows {
		for i := range n {
			for j := range n {
				m[i][j] += row[i] * row[j]
			}
			m[i][n] += row[i] * vals[r]
		}
	}
	return solveLinear(m)
}

// solveLinear solves an augmented n×(n+1) system by Gauss-Jordan
// elimination with partial pivoting.
func solveLinear(m [][]float64) ([]float64, bool) {
	n := len(m)
	for c := range n {
		p := c
		for r := c + 1; r < n; r++ {
			if math.Abs(m[r][c]) > math.Abs(m[p][c]) {
				p = r
			}
		}
		m[c], m[p] = m[p], m[c]
		if math.Abs(m[c][c]) < 1e-300 {
			return nil, false
		}
		for r := range n {
			if r == c {
				continue
			}
			f := m[r][c] / m[c][c]
			for k := c; k <= n; k++ {
				m[r][k] -= f * m[c][k]
			}
		}
	}
	out := make([]float64, n)
	for i := range n {
		out[i] = m[i][n] / m[i][i]
	}
	return out, true
}

// panelPair is two panels' measured difference: panel I less panel J.
type panelPair struct {
	I, J int
	Fit  overlapFit
}

// panelCorrections finds the plane to add to each of n panels so that
// every overlap matches, as nearly as the overlaps together allow: least
// squares over the pairs, each weighted by the blocks it was measured on.
// The corrections sum to nothing within each group of overlapping panels,
// so the mosaic keeps the panels' mean level.
func panelCorrections(n int, pairs []panelPair) []plane {
	out := make([]plane, n)
	if n == 0 {
		return out
	}
	var total float64
	for _, p := range pairs {
		total += float64(p.Fit.Samples)
	}
	get := [3]func(*plane) *float64{
		func(p *plane) *float64 { return &p.A },
		func(p *plane) *float64 { return &p.Bx },
		func(p *plane) *float64 { return &p.By },
	}
	for _, field := range get {
		// Correction j less correction i should be panel i less panel j:
		// a graph Laplacian, held off singular by a ridge too small to move
		// the answer, whose right-hand side sums to nothing.
		m := make([][]float64, n)
		for i := range m {
			m[i] = make([]float64, n+1)
			m[i][i] = 1e-9 * max(total, 1)
		}
		for _, p := range pairs {
			w, d := float64(p.Fit.Samples), *field(&p.Fit.Plane)
			m[p.I][p.I] += w
			m[p.J][p.J] += w
			m[p.I][p.J] -= w
			m[p.J][p.I] -= w
			m[p.J][n] += w * d
			m[p.I][n] -= w * d
		}
		x, ok := solveLinear(m)
		if !ok {
			continue
		}
		for k := range n {
			*field(&out[k]) = x[k]
		}
	}
	return out
}

// matchRegistered matches the sky of registered panels (Siril's r_ files,
// 0 where a panel has no data) on their overlaps, rewriting them with each
// one's correction added. Siril blends the panels unnormalized, so without
// it a panel brighter than its neighbour, or with a different gradient,
// shows as a feathered band. It returns the pairs as measured before and
// after.
func matchRegistered(files []string, sat float32) (before, after []panelPair, err error) {
	before, after, _, err = matchRegisteredSeams(files, sat, true)
	return before, after, err
}

func matchRegisteredSeams(files []string, sat float32, rewrite bool) (before, after []panelPair, seams []seamMeasure, err error) {
	headers := make([]frameheader.Keywords, len(files))
	sizes := make([][2]int, len(files))
	for i, f := range files {
		if headers[i], err = readKeywords(f); err != nil {
			return nil, nil, nil, fmt.Errorf("%s: %w", filepath.Base(f), err)
		}
		sizes[i] = [2]int{int(headers[i].Float("NAXIS1")), int(headers[i].Float("NAXIS2"))}
	}
	at, err := placements(headers, sizes)
	if err != nil {
		return nil, nil, nil, err
	}
	binned := make([]binnedPanel, len(files))
	for i, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, nil, nil, err
		}
		im, err := imagedata.Decode(b)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("%s: %w", filepath.Base(f), err)
		}
		if im.C != 1 || im.W != sizes[i][0] || im.H != sizes[i][1] {
			return nil, nil, nil, fmt.Errorf("%s: %dx%dx%d, header says %dx%d", filepath.Base(f), im.W, im.H, im.C, sizes[i][0], sizes[i][1])
		}
		binned[i] = binRegistered(im.Plane(0), im.W, im.H, at[i], sat)
	}
	measure := func() []panelPair {
		var pairs []panelPair
		for i := range binned {
			for j := i + 1; j < len(binned); j++ {
				if fit, ok := fitDifference(binned[i], binned[j]); ok {
					pairs = append(pairs, panelPair{I: i, J: j, Fit: fit})
				}
			}
		}
		return pairs
	}
	before = measure()
	if len(before) == 0 {
		return before, nil, nil, nil
	}
	corr := panelCorrections(len(files), before)
	for k, b := range binned {
		for by := range b.H {
			for bx := range b.W {
				x, y := b.coords(bx, by)
				b.Data[by*b.W+bx] += float32(corr[k].at(x, y))
			}
		}
	}
	after = measure()
	seams = measureSeams(binned, after)
	if !rewrite {
		return before, after, seams, nil
	}
	for k, f := range files {
		if err := addPlane(f, corr[k], at[k], sat); err != nil {
			return nil, nil, nil, fmt.Errorf("%s: %w", filepath.Base(f), err)
		}
	}
	return before, after, seams, nil
}

// readKeywords reads a FITS file's header.
func readKeywords(file string) (frameheader.Keywords, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b := make([]byte, 64*2880)
	n, _ := io.ReadFull(f, b)
	return frameheader.Parse(b[:n])
}

// addPlane adds a plane over the canvas to a registered panel placed on
// it, keeping its header, 0 (no data) and saturated pixels.
func addPlane(file string, pl plane, at placement, sat float32) error {
	b, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	im, err := imagedata.Decode(b)
	if err != nil {
		return err
	}
	cards, err := copiedCards(b)
	if err != nil {
		return err
	}
	data := im.Plane(0)
	for y := range im.H {
		fy := (float64(y+at.Y)+0.5)/float64(at.CH) - 0.5
		for x := range im.W {
			i := y*im.W + x
			if v := data[i]; v != 0 && v < sat {
				data[i] = v + float32(pl.at((float64(x+at.X)+0.5)/float64(at.CW)-0.5, fy))
				if data[i] <= 0 {
					data[i] = 1e-7 // 0 marks no data
				}
			}
		}
	}
	return writeFITSFile(file, im.W, im.H, 1, data, cards)
}

// copiedCards are a FITS file's keywords to carry into a rewrite of it:
// all but those describing the file's layout, which the rewrite sets.
func copiedCards(b []byte) ([]imagedata.Card, error) {
	all, err := frameheader.ParseCards(b)
	if err != nil {
		return nil, err
	}
	var cards []imagedata.Card
	for _, c := range all {
		switch {
		case c.Name == keywordSIMPLE, c.Name == keywordBITPIX, c.Name == keywordEXTEND, c.Name == keywordBZERO, c.Name == keywordBSCALE,
			c.Name == keywordROWORDER, strings.HasPrefix(c.Name, "NAXIS"):
		default:
			cards = append(cards, imageCard(c))
		}
	}
	return cards, nil
}

func logSeams(project, filter string, seams []seamMeasure) {
	for _, m := range seams {
		pl := m.Fit.Plane
		slog.Info("Mosaic overlap after matching", "project", project, "filter", filter, "panels", fmt.Sprintf("%d-%d", m.I+1, m.J+1),
			"blocks", m.Fit.Samples, "difference", fmt.Sprintf("%.3g", m.Fit.Mean), "slope_x", fmt.Sprintf("%.3g", pl.Bx),
			"slope_y", fmt.Sprintf("%.3g", pl.By), "scatter", fmt.Sprintf("%.3g", m.Fit.Sigma),
			"noise_a", fmt.Sprintf("%.3g", m.NoiseA), "noise_b", fmt.Sprintf("%.3g", m.NoiseB))
	}
}
