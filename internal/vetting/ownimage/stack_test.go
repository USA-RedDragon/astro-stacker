package ownimage

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
)

// stackSpot is a track found in the subs det, each detection given the
// aperture light of a source of light fl.
func (t *trackField) stackSpot(pos func(k int) (float64, float64), det []int, fl float64) TrackSpot {
	s := t.spot(pos, 9)
	s.Points = s.Points[:0]
	for _, k := range det {
		sub := t.subs[k]
		x, y := pos(k)
		s.Points = append(s.Points, TrackPoint{FrameID: sub.FrameID, Filter: sub.Filter, Time: midTime(sub.Sub), X: x, Y: y, SNR: 6, Flux: fl * apertureShare(1.35, lineTrail())})
	}
	return s
}

func runStack(t *testing.T, f *trackField, s TrackSpot) TrackStack {
	t.Helper()
	res, err := StackTracks(context.Background(), f, synthObj, []TrackSpot{s}, Options{Threads: 4})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 {
		t.Fatalf("got %d results", len(res))
	}
	t.Logf("%+v", res[0])
	return res[0]
}

// lineTrail is how far the line's mover moves over one exposure.
func lineTrail() [2]float64 { return [2]float64{3 * trackVX / 12, 3 * trackVY / 12} }

// trailed is a source of half the light of magnitude 18 moving along
// lineTrail over the exposure, about (x, y).
func trailed(x, y float64) [][3]float64 {
	const fl = moverFlux18 / 2
	pts := trailPoints(lineTrail())
	out := make([][3]float64, 0, len(pts))
	for _, q := range pts {
		out = append(out, [3]float64{x + q[0], y + q[1], fl * q[2]})
	}
	return out
}

func (t *trackField) line(k int) (float64, float64) {
	h := t.hours(k)
	return trackX0 + 3*trackVX*h, trackY0 + 3*trackVY*h
}

func TestRealMoverStacksLikeItsFake(t *testing.T) {
	t.Parallel()
	f := newTrackField()
	const fl = moverFlux18 / 2
	for k := range f.subs {
		x, y := f.line(k)
		f.subs[k].extra = trailed(x, y)
	}
	ts := runStack(t, f, f.stackSpot(f.line, []int{1, 2, 3, 4, 5, 6}, fl))
	c, v := StackCheck(&ts)
	t.Logf("%s %s", c.Status, c.Evidence)
	if v != StackConfirmed || c.Status != check.Pass {
		t.Errorf("a real mover was not confirmed: %s", v)
	}
	if ts.SNR < 0.75*ts.Injected || ts.SNR > 1.3*ts.Injected {
		t.Errorf("stack %.1f against its fake %.1f", ts.SNR, ts.Injected)
	}
	if ts.Unused != 4 || ts.UnusedSNR < 0.6*ts.UnusedInjected {
		t.Errorf("unused frames %d at %.1f against %.1f", ts.Unused, ts.UnusedSNR, ts.UnusedInjected)
	}
}

func TestNoiseTrackIsRefutedByItsStack(t *testing.T) {
	t.Parallel()
	f := newTrackField()
	ts := runStack(t, f, f.stackSpot(f.line, []int{1, 2, 3, 4, 5, 6}, moverFlux18/2))
	c, v := StackCheck(&ts)
	t.Logf("%s %s", c.Status, c.Evidence)
	if v != StackRefuted || c.Status != check.Fail {
		t.Errorf("a track of nothing was not refuted: %s", v)
	}
}

func TestTrackAlongStarGlowIsNotConfirmed(t *testing.T) {
	t.Parallel()
	f := newTrackField()
	f.stars = append(f.stars, synthStar{x: 150.2, y: 110.4, g: 15.7, bprp: 1}, synthStar{x: 154.6, y: 111.3, g: 15.9, bprp: 1})
	for k := range f.subs {
		f.subs[k].halo = 0.12
		f.subs[k].sigma = 1.6
	}
	pos := func(k int) (float64, float64) {
		h := f.hours(k)
		return 146.5 + 20*h, 113.4 + 4*h
	}
	ts := runStack(t, f, f.stackSpot(pos, []int{0, 1, 2, 3, 4, 5, 6}, 0.036))
	c, v := StackCheck(&ts)
	t.Logf("%s %s", c.Status, c.Evidence)
	if v != StackRefuted {
		t.Errorf("a track along two stars' glow was not refuted: %s", v)
	}
}

func TestFakeFollowsTheLightPutIn(t *testing.T) {
	t.Parallel()
	f := newTrackField()
	const fl = moverFlux18 / 2
	for k := range f.subs {
		x, y := f.line(k)
		f.subs[k].extra = trailed(x, y)
	}
	ts := runStack(t, f, f.stackSpot(f.line, []int{1, 2, 3, 4, 5, 6}, 2*fl))
	if ts.SNR > 0.6*ts.Injected || ts.SNR < 0.4*ts.Injected {
		t.Errorf("a track found at twice its light stacked to %.1f against its fake %.1f", ts.SNR, ts.Injected)
	}
}

func TestTrackOnStarsIsStarGlow(t *testing.T) {
	t.Parallel()
	f := newTrackField()
	pos := func(k int) (float64, float64) {
		h := f.hours(k)
		return 150 + 2*h, 120 + 14*h
	}
	ax, ay := pos(2)
	bx, by := pos(5)
	f.stars = append(f.stars, synthStar{x: ax - 1.6, y: ay - 2.3, g: 14.7, bprp: 1}, synthStar{x: bx + 1.6, y: by + 2.1, g: 16.5, bprp: 1})
	ts := runStack(t, f, f.stackSpot(pos, []int{1, 2, 3, 4, 5, 6}, moverFlux18))
	c, v := StackCheck(&ts)
	t.Logf("%s %s", c.Status, c.Evidence)
	if !ts.Glow || v != StackRefuted || c.Status != check.Fail {
		t.Errorf("a track on two stars' glow was not refuted as star glow: glow %v, %s", ts.Glow, v)
	}
}

func TestRealMoverPastAStarStacksItsClearFrames(t *testing.T) {
	t.Parallel()
	f := newTrackField()
	const fl = moverFlux18 / 2
	for k := range f.subs {
		x, y := f.line(k)
		f.subs[k].extra = trailed(x, y)
	}
	x4, y4 := f.line(4)
	f.stars = append(f.stars, synthStar{x: x4 - 0.7, y: y4 + 0.9, g: 15.5, bprp: 1})
	ts := runStack(t, f, f.stackSpot(f.line, []int{1, 2, 3, 4, 5, 6}, fl))
	c, v := StackCheck(&ts)
	t.Logf("%s %s", c.Status, c.Evidence)
	if ts.Note != "" || ts.Glow {
		t.Fatalf("the stack gave up or blamed glow: %+v", ts)
	}
	if ts.Frames >= 6 || ts.OnStars == 0 {
		t.Errorf("the frames on the star were stacked: %d frames, %d on stars", ts.Frames, ts.OnStars)
	}
	if v != StackConfirmed || c.Status != check.Pass {
		t.Errorf("a real mover passing a star was not confirmed: %s", v)
	}
}

func TestRealMoverOnStarsMostOfItsFramesIsNotRefuted(t *testing.T) {
	t.Parallel()
	f := newTrackField()
	const fl = moverFlux18 / 2
	for k := range f.subs {
		x, y := f.line(k)
		f.subs[k].extra = trailed(x, y)
	}
	x3, y3 := f.line(3)
	x4, y4 := f.line(4)
	f.stars = append(f.stars, synthStar{x: x3 + 0.8, y: y3 - 0.6, g: 14.5, bprp: 1}, synthStar{x: x4 - 0.7, y: y4 + 0.9, g: 14.8, bprp: 1})
	ts := runStack(t, f, f.stackSpot(f.line, []int{1, 2, 3, 4, 5, 6}, fl))
	c, v := StackCheck(&ts)
	t.Logf("%s %s", c.Status, c.Evidence)
	if v == StackRefuted {
		t.Errorf("a real mover over two bright stars, clear in its unused frames, was refuted")
	}
}

// glowBeside is 67203 (Cygnus Loop, 2025-11-10): a slow track past two
// stars of about 7 times its light, 3.2 px from one or the other in every
// detection frame, with leftover light of its own brightness at the path
// in those frames and none in the frames the detection did not use.
func glowBeside(real bool) (*trackField, TrackSpot) {
	f := newTrackField()
	const fl = moverFlux18 / 2
	pos := func(k int) (float64, float64) {
		h := f.hours(k)
		return 150 + 4*h, 120 + 2*h
	}
	ax, ay := pos(2)
	bx, by := pos(5)
	f.stars = append(f.stars, synthStar{x: ax + 1.4, y: ay - 2.9, g: 16.6, bprp: 1}, synthStar{x: bx - 1.4, y: by + 2.9, g: 16.7, bprp: 1})
	det := []int{1, 2, 3, 4, 5, 6}
	for _, k := range det {
		x, y := pos(k)
		f.subs[k].extra = trailed(x, y)
	}
	if real {
		for k := range f.subs {
			x, y := pos(k)
			f.subs[k].extra = trailed(x, y)
		}
	}
	return f, f.stackSpot(pos, det, fl)
}

func TestTrackBesideStarsIsStarGlow(t *testing.T) {
	t.Parallel()
	f, s := glowBeside(false)
	ts := runStack(t, f, s)
	c, v := StackCheck(&ts)
	t.Logf("%s %s", c.Status, c.Evidence)
	if ts.Beside < 4 || v != StackRefuted || c.Status != check.Fail || !strings.Contains(c.Evidence, "glow of those stars") {
		t.Errorf("a track of leftover light beside two stars was not refuted as star glow: beside %d, %s", ts.Beside, v)
	}
}

func TestRealMoverBesideStarsIsNotRefuted(t *testing.T) {
	t.Parallel()
	f, s := glowBeside(true)
	ts := runStack(t, f, s)
	c, v := StackCheck(&ts)
	t.Logf("%s %s", c.Status, c.Evidence)
	if ts.Beside < 4 || v != StackConfirmed {
		t.Errorf("a real mover beside two stars, there in its unused frames, was not confirmed: beside %d, %s", ts.Beside, v)
	}
}

// TestLongTrackStacksTheFramesItCrossed is 23765 (Cygnus Loop, 2026-09-04):
// found in 6 of the 10 H-alpha frames over 378 minutes, and absent from the
// frames it crossed but was not found in, which lay more than 90 minutes
// from its detections' middle. Light only in the frames it was found in
// must not confirm it.
func TestLongTrackStacksTheFramesItCrossed(t *testing.T) {
	t.Parallel()
	f := newTrackField()
	base := f.subs[0].Start
	for k := range f.subs {
		f.subs[k].Start = base.Add(time.Duration(k) * 30 * time.Minute)
	}
	det := []int{0, 5, 6, 7, 8, 9}
	for _, k := range det {
		x, y := f.line(k)
		f.subs[k].extra = trailed(x, y)
	}
	ts := runStack(t, f, f.stackSpot(f.line, det, moverFlux18/2))
	c, v := StackCheck(&ts)
	t.Logf("%s %s", c.Status, c.Evidence)
	if ts.Unused != 4 {
		t.Errorf("stacked %d of the 4 frames it crossed but was not found in", ts.Unused)
	}
	if ts.UnusedSNR > 2 || ts.UnusedInjected < StackInjMin {
		t.Errorf("unused frames %.1f against %.1f", ts.UnusedSNR, ts.UnusedInjected)
	}
	if v == StackConfirmed {
		t.Errorf("light only in its detection frames was confirmed")
	}
}
