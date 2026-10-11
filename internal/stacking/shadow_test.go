package stacking

import (
	"math"
	"math/rand/v2"
	"slices"
	"testing"
)

const (
	shadowTestW, shadowTestH = 1024, 768
	shadowTestSky            = 0.002
	shadowTestX, shadowTestY = 700, 400
	shadowTestR              = 150
)

type shadowTestSub struct {
	seed         uint64
	shadow       float64
	gradient     float64
	galaxy       float64
	nebula       float64
	glow         float64
	transparency float64
	w, h         int
}

func (s shadowTestSub) size() (int, int) {
	if s.w == 0 {
		return shadowTestW, shadowTestH
	}
	return s.w, s.h
}

func (s shadowTestSub) pixels() []float32 {
	r := rand.New(rand.NewPCG(s.seed, 7))
	w, h := s.size()
	px := make([]float32, w*h)
	t := s.transparency
	if t == 0 {
		t = 1
	}
	for y := range h {
		for x := range w {
			v := shadowTestSky * (1 + s.gradient*(float64(x)/float64(w)-0.5))
			if s.galaxy > 0 {
				d2 := math.Pow(float64(x-w/2), 2) + math.Pow(float64(y-h/2), 2)
				v += t * s.galaxy * math.Exp(-d2/(2*200*200))
			}
			if s.nebula > 0 {
				n := math.Sin(float64(x)/170) * math.Cos(float64(y)/230) * math.Sin(float64(x+y)/410)
				v += t * s.nebula * (1 + n)
			}
			if s.glow > 0 {
				d2 := math.Pow(float64(x-shadowTestX), 2) + math.Pow(float64(y-shadowTestY), 2)
				v += s.glow * shadowTestSky * math.Exp(-d2/(2*shadowTestR*shadowTestR))
			}
			if s.shadow > 0 && math.Hypot(float64(x-shadowTestX), float64(y-shadowTestY)) < shadowTestR {
				v *= 1 - s.shadow
			}
			v += r.NormFloat64() * 0.1 * shadowTestSky
			px[y*w+x] = float32(v)
		}
	}
	return px
}

// shadowDip is how much darker the master is in the disc than in a ring
// around it, as a share of the sky.
func shadowDip(m []float32) float64 {
	var in, ring float64
	var nIn, nRing int
	for y := range shadowTestH {
		for x := range shadowTestW {
			d := math.Hypot(float64(x-shadowTestX), float64(y-shadowTestY))
			switch {
			case d < shadowTestR*0.8:
				in += float64(m[y*shadowTestW+x])
				nIn++
			case d > shadowTestR*1.6 && d < shadowTestR*2.2:
				ring += float64(m[y*shadowTestW+x])
				nRing++
			}
		}
	}
	return (ring/float64(nRing) - in/float64(nIn)) / shadowTestSky
}

func memSubs(subs []shadowTestSub) []memSub {
	all := make([]memSub, len(subs))
	for i, s := range subs {
		all[i] = toMemSub(s.pixels(), 300, 300, 0.9)
	}
	return all
}

func TestMedianAnchoredFillsShadow(t *testing.T) {
	t.Parallel()
	subs := []shadowTestSub{{seed: 1}, {seed: 2}, {seed: 3, shadow: 0.04}, {seed: 4}, {seed: 5}}
	off := DefaultOptions()
	off.FillShadows = false
	before := shadowDip(medianAnchored(memSubs(subs), shadowTestW, shadowTestH, off).Master(300))
	if before < 0.006 {
		t.Fatalf("without filling, the master's dip is %.4f, want the shadowed sub's share of 0.04", before)
	}
	all := memSubs(subs)
	after := shadowDip(medianAnchored(all, shadowTestW, shadowTestH, DefaultOptions()).Master(300))
	if math.Abs(after) > 0.0015 {
		t.Errorf("filled master dips %.4f of the sky in the shadow, want under 0.0015 (was %.4f)", after, before)
	}
	for i, l := range all {
		if (l.fill != nil) != (subs[i].shadow > 0) {
			t.Errorf("sub %d filled %v, shadowed %v", i, l.fill != nil, subs[i].shadow > 0)
		}
	}
}

func TestMedianAnchoredLeavesHazeAndGradients(t *testing.T) {
	t.Parallel()
	big := func(s shadowTestSub) shadowTestSub { s.w, s.h = 2560, 1920; return s }
	for name, subs := range map[string][]shadowTestSub{
		"galaxy": {
			{seed: 1, galaxy: 0.0006},
			{seed: 2, galaxy: 0.0006, gradient: 0.1},
			{seed: 3, galaxy: 0.0006, transparency: 0.4},
			{seed: 4, galaxy: 0.0006, gradient: -0.05},
			{seed: 5, galaxy: 0.0006, transparency: 0.8, gradient: 0.05},
		},
		"nebula": {
			{seed: 1, nebula: 0.002},
			{seed: 2, nebula: 0.002, transparency: 0.7},
			{seed: 3, nebula: 0.002, gradient: 0.05},
			{seed: 4, nebula: 0.002, transparency: 0.85},
			{seed: 5, nebula: 0.002},
		},
	} {
		for i := range subs {
			subs[i] = big(subs[i])
		}
		w, h := subs[0].size()
		all := memSubs(subs)
		medianAnchored(all, w, h, DefaultOptions())
		for i, l := range all {
			if l.fill != nil {
				t.Errorf("%s: sub %d was filled in, but has no shadow", name, i)
			}
		}
	}
}

func TestStreamStackFillsShadow(t *testing.T) {
	t.Parallel()
	subs := make([]storedSub, 0, 12)
	gen := make([]shadowTestSub, 0, 12)
	for i := range 12 {
		s := shadowTestSub{seed: uint64(i + 1)}
		if i == 5 {
			s.shadow = 0.04
		}
		gen = append(gen, s)
		subs = append(subs, storedSub{exposure: float64(300 * (1 + i%2)), weight: 300})
	}
	px := make([][]float32, len(gen))
	for i, s := range gen {
		px[i] = s.pixels()
		for j := range px[i] {
			px[i][j] *= float32(subs[i].exposure / 300)
		}
	}
	stack := func(opts Options) float64 {
		acc, err := streamStack(subs, 3, opts, func(i int, _ storedSub) ([]float32, int, int, error) {
			return slices.Clone(px[i]), shadowTestW, shadowTestH, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return shadowDip(acc.Master(300))
	}
	off := DefaultOptions()
	off.FillShadows = false
	before, after := stack(off), stack(DefaultOptions())
	if before < 0.002 {
		t.Fatalf("without filling, the master's dip is %.4f, want the shadowed sub's share of 0.04", before)
	}
	if math.Abs(after) > 0.0008 {
		t.Errorf("filled master dips %.4f of the sky in the shadow, want under 0.0008 (was %.4f)", after, before)
	}
}

func TestFindShadowsAgainstOthersMedian(t *testing.T) {
	t.Parallel()
	gen := []shadowTestSub{{seed: 1, glow: 0.3}, {seed: 2, glow: 0.3}, {seed: 3, shadow: 0.04}, {seed: 4}, {seed: 5}, {seed: 6}}
	cells := make([][]float32, len(gen))
	skies := make([]float64, len(gen))
	for i, s := range gen {
		px := s.pixels()
		bg := background(px, 0.9)
		skies[i] = bg
		cells[i] = cellMedians(shadowTestW, shadowTestH, func(j int) (float32, bool) { return px[j] - float32(bg), px[j] != 0 })
	}
	for i, f := range findShadows(cells, skies, shadowTestW, shadowTestH) {
		if (f != nil) != (gen[i].shadow > 0) {
			t.Errorf("sub %d filled %v, shadowed %v", i, f != nil, gen[i].shadow > 0)
			continue
		}
		if f == nil {
			continue
		}
		if len(f.shadows) != 1 || math.Abs(f.shadows[0].depth-gen[i].shadow) > 0.01 ||
			math.Hypot(float64(f.shadows[0].x-shadowTestX), float64(f.shadows[0].y-shadowTestY)) > shadowTestR {
			t.Errorf("sub %d: shadows %+v, want one %.2f deep near %d,%d", i, f.shadows, gen[i].shadow, shadowTestX, shadowTestY)
		}
	}
}

func TestCometStacksDontFillShadows(t *testing.T) {
	t.Parallel()
	if cometOptions(DefaultOptions()).FillShadows {
		t.Error("comet stacks fill shadows; their masked stars leave holes the size of one")
	}
}
