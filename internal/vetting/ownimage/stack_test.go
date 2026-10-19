package ownimage

import (
	"context"
	"fmt"
	"math"
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

// colourField is a field whose 10 subs are H-alpha, passing half the light
// of broadband, with 10 frames in Red, Green and Blue between them,
// passing 1.0, 0.9 and 0.7 of it, each filter with its master. A source
// of G-scale light fl moves along the line through all of them, its light
// in each frame 10^(-0.4 dmag[filter]) times that, none in a filter dmag
// lacks.
func colourField(fl float64, dmag map[string]float64) *trackField {
	const ha = "H-a"
	thru := map[string]float64{ha: 0.5, "Red": 1.0, "Green": 0.9, "Blue": 0.7}
	f := newTrackField()
	f.more = map[string]synthSub{}
	for filter, tr := range thru {
		f.more[filter] = synthSub{sigma: 1.4, tr: tr, seed: 990 + uint64(len(filter)), quiet: true}
	}
	n := len(f.subs)
	for k := range n {
		f.subs[k].Filter = ha
		f.subs[k].tr *= thru[ha]
		c := f.subs[k]
		c.FrameID = 100 + k
		c.Filter = []string{"Red", "Green", "Blue"}[k%3]
		c.Start = c.Start.Add(3 * time.Minute)
		c.Key = fmt.Sprintf("registered/%s/%s/%d.xisf", synthObj, c.Filter, c.FrameID)
		c.seed += 500
		c.tr = f.subs[k].tr / thru[ha] * thru[c.Filter]
		f.subs = append(f.subs, c)
	}
	for k := range f.subs {
		d, ok := dmag[f.subs[k].Filter]
		if !ok {
			continue
		}
		x, y := f.line(k)
		f.subs[k].extra = trailedFlux(x, y, fl*math.Pow(10, -0.4*d))
	}
	return f
}

// trailedFlux is a source of light fl moving along lineTrail over the
// exposure, about (x, y).
func trailedFlux(x, y, fl float64) [][3]float64 {
	pts := trailPoints(lineTrail())
	out := make([][3]float64, 0, len(pts))
	for _, q := range pts {
		out = append(out, [3]float64{x + q[0], y + q[1], fl * q[2]})
	}
	return out
}

// colourSpot is the line found in the H-alpha subs det at G-scale light
// fl, its detections given the light H-alpha passes and the magnitude of
// fl against the field's stars.
func colourSpot(f *trackField, det []int, fl float64) TrackSpot {
	s := f.stackSpot(f.line, det, fl*f.more["H-a"].tr)
	s.Group = "H-a"
	for i := range s.Points {
		s.Points[i].Mag = 15 - 2.5*math.Log10(fl)
	}
	return s
}

// TestRealMoverShowsInOtherFilters is a moving object found in H-alpha
// with an S-type asteroid's colours against the field's stars, 0.2
// magnitudes brighter in Red and 0.4 fainter in Blue: its frames in Red,
// Green and Blue, faked by each master's zero point, show it as their
// fakes do, and do not refute it.
func TestRealMoverShowsInOtherFilters(t *testing.T) {
	t.Parallel()
	f := colourField(moverFlux18, map[string]float64{"H-a": 0, "Red": -0.2, "Green": 0, "Blue": 0.4})
	ts := runStack(t, f, colourSpot(f, []int{1, 2, 3, 4, 5, 6}, moverFlux18))
	c, v := StackCheck(&ts)
	t.Logf("%s %s", c.Status, c.Evidence)
	if ts.Other != 10 || len(ts.OtherFilters) != 3 {
		t.Fatalf("stacked %d frames in %v of the 10 in Red, Green and Blue", ts.Other, ts.OtherFilters)
	}
	if ts.OtherSNR < 0.75*ts.OtherInjected || ts.OtherSNR > 1.25*ts.OtherInjected {
		t.Errorf("other filters %.1f against their fake %.1f", ts.OtherSNR, ts.OtherInjected)
	}
	if ts.OtherFaint < StackInjMin {
		t.Errorf("a fake 1.5 magnitudes fainter shows at only %.1f", ts.OtherFaint)
	}
	if v != StackConfirmed || c.Status != check.Pass {
		t.Errorf("a real mover with an asteroid's colours was not confirmed: %s", v)
	}
}

// TestLineEmitterIsRefutedByOtherFilters is a source moving along the line
// with light in every H-alpha frame and none in Red, Green or Blue: its own
// stack shows it, and the other filters refute it.
func TestLineEmitterIsRefutedByOtherFilters(t *testing.T) {
	t.Parallel()
	f := colourField(moverFlux18, map[string]float64{"H-a": 0})
	ts := runStack(t, f, colourSpot(f, []int{1, 2, 3, 4, 5, 6}, moverFlux18))
	c, v := StackCheck(&ts)
	t.Logf("%s %s", c.Status, c.Evidence)
	if ts.SNR < 0.6*ts.Injected {
		t.Errorf("its own stack %.1f against its fake %.1f", ts.SNR, ts.Injected)
	}
	if v != StackRefuted || c.Status != check.Fail || !strings.Contains(c.Evidence, "reflected sunlight") {
		t.Errorf("light only in H-alpha was not refuted by the other filters: %s", v)
	}
}

// TestBroadbandTrackIgnoresNarrowbandFrames is a moving object found in
// Luminance on a night with H-alpha frames too: it has no frames in other
// filters, and its stack is judged as before.
func TestBroadbandTrackIgnoresNarrowbandFrames(t *testing.T) {
	t.Parallel()
	judge := func(withHa bool) (TrackStack, StackVerdict) {
		f := newTrackField()
		const fl = moverFlux18 / 2
		for k := range f.subs {
			x, y := f.line(k)
			f.subs[k].extra = trailed(x, y)
		}
		if withHa {
			f.more = map[string]synthSub{"H-a": {sigma: 1.4, tr: 0.5, seed: 991, quiet: true}}
			for k := range trackSubs {
				c := f.subs[k]
				c.FrameID, c.Filter, c.Start = 100+k, "H-a", c.Start.Add(3*time.Minute)
				c.Key = fmt.Sprintf("registered/%s/H-a/%d.xisf", synthObj, c.FrameID)
				c.tr, c.seed, c.extra = c.tr*0.5, c.seed+500, nil
				f.subs = append(f.subs, c)
			}
		}
		s := f.stackSpot(f.line, []int{1, 2, 3, 4, 5, 6}, fl)
		for i := range s.Points {
			s.Points[i].Mag = 15 - 2.5*math.Log10(fl)
		}
		ts := runStack(t, f, s)
		_, v := StackCheck(&ts)
		return ts, v
	}
	plain, pv := judge(false)
	ha, hv := judge(true)
	if ha.Other != 0 || plain.Other != 0 {
		t.Errorf("a broadband track stacked %d and %d frames in other filters", plain.Other, ha.Other)
	}
	if pv != StackConfirmed || hv != pv || ha.SNR != plain.SNR || ha.Unused != plain.Unused {
		t.Errorf("H-alpha frames changed a broadband track's stack: %s %+v against %s %+v", hv, ha, pv, plain)
	}
}

func TestMoverSelfShare(t *testing.T) {
	t.Parallel()
	// 10 subs of one night weighted 1 to 2 beside 200 deep subs of 1.5:
	// a mover of 200 px an hour, 6 minutes apart, meets only its own sub's
	// share; one standing still meets the whole night's, and the fixture's
	// own, 10 px an hour, its neighbours' too.
	f := newTrackField()
	subs, err := f.Subs(context.Background(), synthObj)
	if err != nil {
		t.Fatal(err)
	}
	var total, night float64
	for _, s := range subs {
		total += s.Weight
		if nightKey(s.Night) == flaggedDay {
			night += s.Weight
		}
	}
	sp := &stackPlan{tp: newTrackPlan(subs, f.spot(func(k int) (float64, float64) { return trackX0 + trackVX*f.hours(k), trackY0 + trackVY*f.hours(k) }, 8), synthW, synthH),
		weight: map[string]float64{synthLum: total}}
	own := subs[3].Weight / total
	if got := sp.selfShare(subs, 3, 1.4); got < 3*own || got > night/total {
		t.Errorf("10 px an hour: share %.4f, the night's %.4f", got, night/total)
	}
	sp.tp.vx, sp.tp.vy = 200, 100
	if got := sp.selfShare(subs, 3, 1.4); math.Abs(got-own) > 0.002 {
		t.Errorf("fast mover: share %.4f, its own sub's %.4f", got, own)
	}
	sp.tp.vx, sp.tp.vy = 0, 0
	if got := sp.selfShare(subs, 3, 1.4); math.Abs(got-night/total) > 1e-9 {
		t.Errorf("still: share %.4f, the night's %.4f", got, night/total)
	}
}
