package ownimage

import (
	"context"
	"errors"
	"math"
	"testing"
)

func TestLiveRecoversTheDipSubBySub(t *testing.T) {
	t.Parallel()
	f := newSynth()
	f.addNights(synthNights()[:1], 12)
	for i := range f.subs {
		k := float64(i)
		f.subs[i].targetDMag = 0.4 * math.Exp(-(k-6)*(k-6)/6)
	}
	f.subs[3].tr = 0.5
	ctx := context.Background()
	l, err := NewLive(ctx, f, synthObj, f.spot(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range f.subs {
		if err := l.Add(ctx, f, s.Sub); err != nil {
			t.Fatal(err)
		}
		if err := l.Add(ctx, f, s.Sub); err != nil {
			t.Fatal(err)
		}
	}
	pts, kept, picked := l.Points()
	if len(pts) != len(f.subs) || kept < minComps || picked < kept {
		t.Fatalf("%d points of %d subs, %d of %d comparison stars", len(pts), len(f.subs), kept, picked)
	}
	off := make([]float64, len(pts))
	for i, p := range pts {
		off[i] = p.Mag - f.subs[i].targetDMag
	}
	zero := median(off)
	trs := make([]float64, len(f.subs))
	for i, s := range f.subs {
		trs[i] = s.tr
	}
	mt := median(trs)
	for i, p := range pts {
		s := f.subs[i]
		if got := p.Mag - zero; math.Abs(got-s.targetDMag) > math.Max(0.02, 3*p.Err) {
			t.Errorf("sub %d: %.3f mag, truth %.3f", i, got, s.targetDMag)
		}
		if want := -2.5 * math.Log10(s.tr/mt); math.Abs(p.Comp-want) > 0.02 {
			t.Errorf("sub %d: comparison stars %.3f mag, truth %.3f", i, p.Comp, want)
		}
		if !(p.Err > 0 && p.Err < 0.05) {
			t.Errorf("sub %d: error %.4f", i, p.Err)
		}
	}
	if pts[3].Comp < 0.6 {
		t.Errorf("the cloudy sub's comparison stars read %.3f mag fainter, want about 0.75", pts[3].Comp)
	}
}

func TestLiveNeedsAStar(t *testing.T) {
	t.Parallel()
	f := newSynth()
	f.addNights(synthNights()[:1], 2)
	s := f.spot(0, 0)
	s.RA, s.Dec = f.w.SkyOf(20, 20)
	if _, err := NewLive(context.Background(), f, synthObj, s); !errors.Is(err, ErrNoStar) {
		t.Fatalf("got %v, want ErrNoStar", err)
	}
}
