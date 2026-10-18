package ownimage

import (
	"context"
	"testing"

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

// trailed is a source of light fl moving along lineTrail over the
// exposure, about (x, y).
func trailed(x, y, fl float64) [][3]float64 {
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
		f.subs[k].extra = trailed(x, y, fl)
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
		f.subs[k].extra = trailed(x, y, fl)
	}
	ts := runStack(t, f, f.stackSpot(f.line, []int{1, 2, 3, 4, 5, 6}, 2*fl))
	if ts.SNR > 0.6*ts.Injected || ts.SNR < 0.4*ts.Injected {
		t.Errorf("a track found at twice its light stacked to %.1f against its fake %.1f", ts.SNR, ts.Injected)
	}
}
