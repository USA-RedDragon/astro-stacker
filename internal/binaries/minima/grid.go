package minima

// gridRoom is the most frame times a shapeGrid keeps rows for; rows past
// it are worked out again each time, so a night of many frames does not
// hold too much memory.
const gridRoom = 1000

// shapeGrid is a dip's shape at each of steps+1 middles lo to hi, h
// apart, for each frame time asked for, kept so the many fits made on
// the same frames at the same width work it out once.
type shapeGrid struct {
	d         Dip
	lo, hi, h float64
	steps     int
	rows      map[float64][]float64
}

func newShapeGrid(d Dip, lo, hi float64, steps int) *shapeGrid {
	return &shapeGrid{d: d, lo: lo, hi: hi, h: (hi - lo) / float64(steps), steps: steps, rows: map[float64][]float64{}}
}

// widthGrids is a shapeGrid over ±span about d.Mid in gridSteps for d at
// each width in widthScales.
func widthGrids(d Dip, span float64) []*shapeGrid {
	ws := WidthScales()
	gs := make([]*shapeGrid, len(ws))
	for i, s := range ws {
		gs[i] = newShapeGrid(scaled(d, s), -span, span, gridSteps)
	}
	return gs
}

func (g *shapeGrid) span() float64 { return (g.hi - g.lo) / 2 }

// row is the dip's shape at a frame at time t for each middle in g.
func (g *shapeGrid) row(t float64) []float64 {
	if r, ok := g.rows[t]; ok {
		return r
	}
	r := make([]float64, g.steps+1)
	for j := range r {
		r[j] = g.d.Shape(t - g.d.Mid - (g.lo + float64(j)*g.h))
	}
	if len(g.rows) < gridRoom {
		g.rows[t] = r
	}
	return r
}

// steady is the part of the filters' normal equations for a set of
// points that does not depend on the dip's middle, with each point's
// weight and time from the middle, so each step of the middle adds only
// the dip's terms. The sums are taken in the points' order, as addAll
// takes them.
type steady struct {
	sums []normal
	w, x []float64
}

func newSteady(pts []Point, mid float64, ng int, slope bool) steady {
	st := steady{sums: make([]normal, ng), w: make([]float64, len(pts)), x: make([]float64, len(pts))}
	for i, p := range pts {
		w := 1 / (p.E * p.E)
		x := p.T - mid
		st.w[i], st.x[i] = w, x
		s := &st.sums[p.G]
		s.b[0] += w * p.Y
		s.a[0][0] += w
		if slope {
			s.b[2] += w * x * p.Y
			s.a[0][2] += w * x
			s.a[2][2] += w * x * x
		}
		s.syy += w * p.Y * p.Y
	}
	return st
}

// dipTerms is one filter's sums in the normal equations that hold the
// dip's shape.
type dipTerms struct{ b1, a01, a11, a12 float64 }

// addDip sets terms to each filter's dip sums with the dip's middle at
// step j of rows, the dip's shape at each point, w and x each point's
// weight and time from the middle. A point outside the dip adds nothing
// and is passed over.
func addDip(pts []Point, rows [][]float64, j int, w, x []float64, terms []dipTerms) {
	clear(terms)
	for i, p := range pts {
		c := rows[i][j]
		if c == 0 {
			continue
		}
		wc := w[i] * c
		t := &terms[p.G]
		t.b1 += wc * p.Y
		t.a01 += wc
		t.a11 += wc * c
		t.a12 += wc * x[i]
	}
}

// with sets sums to the normal equations of st with the dip's terms.
func (st steady) with(terms []dipTerms, sums []normal, slope bool) {
	copy(sums, st.sums)
	for g, t := range terms {
		s := &sums[g]
		s.b[1] = t.b1
		s.a[0][1] = t.a01
		s.a[1][1] = t.a11
		if slope {
			s.a[1][2] = t.a12
		}
	}
}
