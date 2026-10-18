package period

import (
	"errors"
	"math"
	"slices"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
)

const (
	fieldP      = 2.3456
	fieldT0     = 2459000.3
	fieldDepth  = 0.4
	fieldOffset = 0.15
	keyZTFr     = lc.ZTF + " r"
)

func fieldModel(tt float64) float64 {
	return fieldDepth * shapeOnly(wrapPhase((tt-fieldT0)/fieldP), 0.04, 1.2)
}

// twoFields is a ZTF r light curve merged from two ZTF fields whose zero
// points differ by fieldOffset: 1100 nights in the first, and in the
// second 300 nights, or with inEclipse 90 points inside the dip and 30
// outside it.
func twoFields(depth float64, inEclipse bool, seed uint64) lc.Series {
	rng := lcg(seed)
	s := lc.Series{Survey: lc.ZTF, Band: "r", Dilution: 1}
	add := func(tt, off float64, src int64) {
		m := 15.6 + off + 0.02*rng.gauss()
		if depth > 0 {
			m += fieldModel(tt) * depth / fieldDepth
		}
		s.Points = append(s.Points, lc.Point{BJD: tt, Mag: m, Err: 0.02, Src: src})
	}
	for night := range 1100 {
		add(2458300+float64(night)*1.31+0.3*rng.next(), 0, 1)
	}
	if !inEclipse {
		for night := range 300 {
			add(2458310+float64(night)*4.7+0.3*rng.next(), fieldOffset, 2)
		}
	} else {
		in, out := 0, 0
		for c := 0; in < 90 || out < 30; c++ {
			tt := fieldT0 - 700*fieldP + float64(c)*fieldP*1.7 + fieldP*0.6*rng.next()
			dip := math.Abs(wrapPhase((tt-fieldT0)/fieldP)) < 0.02
			switch {
			case dip && in < 90:
				in++
			case !dip && out < 30 && c%5 == 0:
				out++
			default:
				continue
			}
			add(tt, fieldOffset, 2)
		}
	}
	slices.SortFunc(s.Points, func(a, b lc.Point) int { return cmpFloat(a.BJD, b.BJD) })
	return s
}

func cmpFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func withoutSrc(s lc.Series) lc.Series {
	s.Points = slices.Clone(s.Points)
	for i := range s.Points {
		s.Points[i].Src = 0
	}
	return s
}

func srcMedianOutside(s lc.Series, e *Ephemeris, src int64) float64 {
	var v []float64
	for _, p := range s.Points {
		if p.Src == src && e.Predict(p.BJD, keyZTFr) == 0 {
			v = append(v, p.Mag)
		}
	}
	return median(v)
}

// TestFieldOffsetEclipses merges two ZTF fields 0.15 mag apart: the
// period comes out, the dip's depth is not biased even when the second
// field mostly catches eclipses, and the false period tests still pass.
func TestFieldOffsetEclipses(t *testing.T) {
	t.Parallel()
	for _, inEclipse := range []bool{false, true} {
		s := twoFields(fieldDepth, inEclipse, 11)
		res, err := Analyze([]lc.Series{s}, Options{})
		if err != nil {
			t.Fatalf("in eclipse %v: %v", inEclipse, err)
		}
		e := res.Ephemeris
		if e.Kind != KindEclipsing || math.Abs(e.Period-fieldP) > 1e-4 {
			t.Fatalf("in eclipse %v: got %s at %.6f, want eclipsing at %.4f", inEclipse, e.Kind, e.Period, fieldP)
		}
		if d := e.Eclipses[0].Depth[keyZTFr]; math.Abs(d-fieldDepth) > 0.015 {
			t.Errorf("in eclipse %v: depth %.3f, want %.2f", inEclipse, d, fieldDepth)
		}
		if r := res.Robust; r == nil || !r.Real || !r.Stable {
			t.Errorf("in eclipse %v: the false period tests fail: %+v", inEclipse, r)
		}
		a := AlignSrc([]lc.Series{s}, e)[0]
		if d := srcMedianOutside(a, e, 2) - srcMedianOutside(a, e, 1); math.Abs(d) > 0.01 {
			t.Errorf("in eclipse %v: aligned fields differ by %.3f outside the dips", inEclipse, d)
		}
		if inEclipse {
			bad, err := AnalyzeAt([]lc.Series{withoutSrc(s)}, e, Options{})
			if err != nil {
				t.Fatal(err)
			}
			d := bad.Ephemeris.Eclipses[0].Depth[keyZTFr]
			t.Logf("one zero point for both fields: depth %.3f", d)
			if math.Abs(d-fieldDepth) < 0.05 {
				t.Error("on one zero point the depth is right too; the test shows nothing")
			}
		}
	}
}

// TestFieldOffsetIsNoVariability merges two ZTF fields of a steady star
// 0.15 mag apart: it stays flat, where on one zero point the offset reads
// as variability.
func TestFieldOffsetIsNoVariability(t *testing.T) {
	t.Parallel()
	s := twoFields(0, false, 12)
	res, err := Analyze([]lc.Series{s}, Options{})
	if !errors.Is(err, ErrNoPeriod) || res.Ephemeris.Kind != KindFlat {
		t.Fatalf("got %s, %v; want flat", res.Ephemeris.Kind, err)
	}
	merged, err := Analyze([]lc.Series{withoutSrc(s)}, Options{})
	if err != nil && !errors.Is(err, ErrNoPeriod) {
		t.Fatal(err)
	}
	t.Logf("on one zero point: %s at %.4f days", merged.Ephemeris.Kind, merged.Ephemeris.Period)
	if merged.Ephemeris.Kind == KindFlat {
		t.Fatal("on one zero point the offset reads as flat too; the test shows nothing")
	}
	a := AlignSrc([]lc.Series{s}, nil)[0]
	var one, two []float64
	for _, p := range a.Points {
		if p.Src == 1 {
			one = append(one, p.Mag)
		} else {
			two = append(two, p.Mag)
		}
	}
	if d := median(two) - median(one); math.Abs(d) > 0.005 {
		t.Errorf("aligned fields differ by %.3f", d)
	}
}

// doctorSrc moves every ZTF light curve but the largest of each band by
// off; with fewOutside it also keeps only about fewOutside of their
// points outside e's dips, spread over the years, so they mostly catch
// eclipses.
func doctorSrc(series []lc.Series, e *Ephemeris, off float64, fewOutside int) []lc.Series {
	out := slices.Clone(series)
	for i, s := range out {
		runs := splitSrc(s.Points)
		if s.Survey != lc.ZTF || len(runs) < 2 {
			continue
		}
		var pts []lc.Point
		pts = append(pts, runs[0]...)
		key := BandKey(s.Survey, s.Band)
		for _, r := range runs[1:] {
			outside := 0
			for _, p := range r {
				if e.Predict(p.BJD, key) == 0 {
					outside++
				}
			}
			step := 1
			if fewOutside > 0 {
				step = max(1, outside/fewOutside)
			}
			k := 0
			for _, p := range r {
				if e.Predict(p.BJD, key) == 0 {
					k++
					if (k-1)%step != 0 {
						continue
					}
				}
				p.Mag += off
				pts = append(pts, p)
			}
		}
		slices.SortFunc(pts, func(a, b lc.Point) int { return cmpFloat(a.BJD, b.BJD) })
		out[i].Points = pts
	}
	return out
}

// TestAuditFieldOffsets moves the second ZTF field of two audit binaries
// with two fields, 6014 and 23081, by 0.15 mag, and in a second run also
// cuts that field down to the points in the dips and a few outside: the
// depths and the false period tests do not change.
func TestAuditFieldOffsets(t *testing.T) {
	t.Parallel()
	byKey := map[string]AuditCase{}
	for _, c := range auditCases(t) {
		byKey[c.Key] = c
	}
	for _, key := range []string{"c6014", "c23081"} {
		c := byKey[key]
		series, comps := auditSeries(t, key)
		opts := Options{Comparisons: func() [][]lc.Series { return comps }}
		base, err := AnalyzeAt(series, c.Ephemeris, opts)
		if err != nil {
			t.Fatal(err)
		}
		e := base.Ephemeris
		for _, few := range []int{0, 10} {
			doc := doctorSrc(series, e, fieldOffset, few)
			res, err := AnalyzeAt(doc, c.Ephemeris, opts)
			if err != nil {
				t.Fatal(err)
			}
			if !holds(res.Robust) {
				t.Errorf("%s few %d: the false period tests fail: %+v", key, few, *res.Robust)
			}
			for k := range e.Eclipses {
				for band, d0 := range e.Eclipses[k].Depth {
					if band == lc.ZTF+" i" {
						continue
					}
					d := res.Ephemeris.Eclipses[k].Depth[band]
					t.Logf("%s few %d dip %d %s: depth %.3f, %.3f before", key, few, k, band, d, d0)
					if math.Abs(d-d0) > 0.003 {
						t.Errorf("%s few %d dip %d %s: depth %.3f, want %.3f", key, few, k, band, d, d0)
					}
				}
			}
			for _, s := range AlignSrc(doc, e) {
				runs := splitSrc(s.Points)
				if s.Survey != lc.ZTF || len(runs) < 2 {
					continue
				}
				ref := srcMedianOutside(lc.Series{Points: runs[0]}, e, runs[0][0].Src)
				for _, r := range runs[1:] {
					m := srcMedianOutside(lc.Series{Points: r}, e, r[0].Src)
					if math.Abs(m-ref) > 0.02 {
						t.Errorf("%s few %d ZTF %s: aligned field %d sits %.3f off the largest", key, few, s.Band, r[0].Src, m-ref)
					}
				}
			}
		}
	}
}

// TestAlignSrcInsideDips aligns a field whose every point is inside a
// dip, by its points less the model, both unblended and with a neighbour
// giving half the light.
func TestAlignSrcInsideDips(t *testing.T) {
	t.Parallel()
	e := &Ephemeris{Period: fieldP, T0: fieldT0, Kind: KindEclipsing,
		Eclipses: []Eclipse{{Phase: 0, Width: 0.04, Shape: 1.2, Depth: map[string]float64{keyZTFr: fieldDepth}}}}
	for _, dil := range []float64{1, 0.5} {
		rng := lcg(13)
		s := lc.Series{Survey: lc.ZTF, Band: "r", Dilution: dil}
		for night := range 400 {
			tt := 2458300 + float64(night)*1.31
			s.Points = append(s.Points, lc.Point{BJD: tt, Mag: 15.6 + dilute(fieldModel(tt), dil) + 0.01*rng.gauss(), Err: 0.01, Src: 1})
		}
		k0 := math.Ceil((2458300 - fieldT0) / fieldP)
		for c := range 30 {
			tt := fieldT0 + (k0+float64(c*7))*fieldP + 0.03*fieldP*(rng.next()-0.5)
			s.Points = append(s.Points, lc.Point{BJD: tt, Mag: 15.6 + fieldOffset + dilute(fieldModel(tt), dil) + 0.01*rng.gauss(), Err: 0.01, Src: 2})
		}
		var moved []float64
		for i, p := range AlignSrc([]lc.Series{s}, e)[0].Points {
			if p.Src == 2 {
				moved = append(moved, p.Mag-s.Points[i].Mag)
			}
		}
		if d := median(moved); math.Abs(d+fieldOffset) > 0.01 {
			t.Errorf("dilution %.1f: the field in the dips moved by %.3f, want %.2f", dil, d, -fieldOffset)
		}
	}
}

// TestFieldInAGapKeepsTheChange gives a steady star a second ZTF field
// that only looked during a 60 day gap of the first, while the star was
// 0.5 mag fainter: the fade stays in the light curve and the ZTF row
// still sees the reported fade.
func TestFieldInAGapKeepsTheChange(t *testing.T) {
	t.Parallel()
	rng := lcg(21)
	s := lc.Series{Survey: lc.ZTF, Band: "r", Dilution: 1}
	for night := range 1000 {
		tt := 2458300 + float64(night)
		if tt >= 2458700 && tt < 2458760 {
			if night%4 == 0 {
				s.Points = append(s.Points, lc.Point{BJD: tt, Mag: 16.1 + 0.01*rng.gauss(), Err: 0.01, Src: 2})
			}
			continue
		}
		s.Points = append(s.Points, lc.Point{BJD: tt, Mag: 15.6 + 0.01*rng.gauss(), Err: 0.01, Src: 1})
	}
	for _, p := range AlignSrc([]lc.Series{s}, nil)[0].Points {
		if p.Src == 2 && p.Mag < 16 {
			t.Fatalf("the field in the gap moved to %.3f", p.Mag)
		}
	}
	res, err := Analyze([]lc.Series{s}, Options{ChangeMag: 0.5})
	if err != nil && !errors.Is(err, ErrNoPeriod) {
		t.Fatal(err)
	}
	for _, c := range res.Checks {
		if c.ID == check.ZTF && c.Status != check.Pass {
			t.Errorf("ZTF row %+v, want the fade seen", c)
		}
	}
}

// TestFieldOffsetUnderASlowFade aligns a second field that covers only
// the first year of a star fading by 0.3 mag over six: the offset is its
// zero point's 0.1 mag, not the fade.
func TestFieldOffsetUnderASlowFade(t *testing.T) {
	t.Parallel()
	rng := lcg(22)
	s := lc.Series{Survey: lc.ZTF, Band: "r", Dilution: 1}
	for night := range 2000 {
		tt := 2458300 + float64(night)*1.1
		fade := 0.3 * float64(night) / 2000
		s.Points = append(s.Points, lc.Point{BJD: tt, Mag: 15.6 + fade + 0.01*rng.gauss(), Err: 0.01, Src: 1})
		if night < 330 && night%3 == 0 {
			s.Points = append(s.Points, lc.Point{BJD: tt + 0.05, Mag: 15.7 + fade + 0.01*rng.gauss(), Err: 0.01, Src: 2})
		}
	}
	var moved []float64
	for i, p := range AlignSrc([]lc.Series{s}, nil)[0].Points {
		if p.Src == 2 {
			moved = append(moved, p.Mag-s.Points[i].Mag)
		}
	}
	if d := median(moved); math.Abs(d+0.1) > 0.005 {
		t.Errorf("moved by %.3f, want -0.100", d)
	}
}

func TestSplitSrc(t *testing.T) {
	t.Parallel()
	ps := make([]lc.Point, 0, 44)
	for i := range 40 {
		src := int64(7)
		switch {
		case i%4 == 1 && i < 36:
			src = 3
		case i%13 == 0:
			src = 9
		}
		ps = append(ps, lc.Point{BJD: float64(i), Src: src})
	}
	runs := splitSrc(ps)
	if len(runs) != 1 {
		t.Fatalf("got %d runs, want the 10 point one merged into the first (fewer than %d)", len(runs), minSrcPoints)
	}
	for i := range 4 {
		ps = append(ps, lc.Point{BJD: float64(40 + i), Src: 3})
	}
	runs = splitSrc(ps)
	if len(runs) != 2 || len(runs[0]) != 31 || len(runs[1]) != 13 {
		t.Fatalf("runs %v", runs)
	}
	for _, p := range runs[1] {
		if p.Src != 3 {
			t.Fatalf("second run holds %+v", p)
		}
	}
	if !slices.IsSortedFunc(runs[0], func(a, b lc.Point) int { return cmpFloat(a.BJD, b.BJD) }) {
		t.Error("first run out of time order")
	}
	if got := splitSrc(ps[:3]); len(got) != 1 || len(got[0]) != 3 {
		t.Errorf("one zero point split into %v", got)
	}
}
