package ownimage

import (
	"bytes"
	"context"
	"math"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
)

const (
	masterKey   = "stacks/Synth/Luminance/master.fit"
	trackSubs   = 10
	trackX0     = 120.0
	trackY0     = 100.0
	trackVX     = 10.0
	trackVY     = 5.0
	synthScale  = 1.9
	moverFlux18 = 0.063
)

// trackField is a synthetic target with a master to subtract.
type trackField struct {
	*synthField
	master synthSub
	// more are the masters of other filters, by filter.
	more map[string]synthSub
}

func filterMasterKey(filter string) string { return "stacks/Synth/" + filter + "/master.fit" }

func (t *trackField) Masters(context.Context, string) (map[string]string, error) {
	out := map[string]string{synthLum: masterKey}
	for f := range t.more {
		out[f] = filterMasterKey(f)
	}
	return out, nil
}

func (t *trackField) Load(ctx context.Context, key string) (*discoveries.Plane, error) {
	if key == masterKey {
		return t.render(t.master), nil
	}
	for f, m := range t.more {
		if key == filterMasterKey(f) {
			return t.render(m), nil
		}
	}
	return t.synthField.Load(ctx, key)
}

func newTrackField() *trackField {
	f := newSynth()
	f.addNights([]time.Time{day(flaggedDay)}, trackSubs)
	return &trackField{synthField: f, master: synthSub{sigma: 1.4, tr: 1, seed: 999, quiet: true}}
}

func (t *trackField) hours(k int) float64 {
	return midTime(t.subs[k].Sub).Sub(midTime(t.subs[0].Sub)).Hours()
}

func (t *trackField) spot(pos func(k int) (float64, float64), snr float64) TrackSpot {
	s := TrackSpot{SourceID: 9, Night: day(flaggedDay), Group: discoveries.GroupBroadband}
	for k, sub := range t.subs {
		x, y := pos(k)
		s.Points = append(s.Points, TrackPoint{FrameID: sub.FrameID, Time: midTime(sub.Sub), X: x, Y: y, SNR: snr})
	}
	return s
}

func runTrack(t *testing.T, f *trackField, s TrackSpot, rate, angle float64) ([]check.Check, TrackFlags, TrackResult) {
	t.Helper()
	res, err := MeasureTracks(context.Background(), f, synthObj, []TrackSpot{s}, Options{Threads: 4})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 {
		t.Fatalf("got %d results", len(res))
	}
	snrs := make([]float64, len(s.Points))
	for i, p := range s.Points {
		snrs[i] = p.SNR
	}
	cs, fl := TrackChecks(&res[0], len(s.Points), snrs, rate, synthScale, angle)
	for _, c := range cs {
		t.Logf("%-13s %-4s %s", c.ID, c.Status, c.Evidence)
	}
	return cs, fl, res[0]
}

func status(t *testing.T, cs []check.Check, id string) check.Status {
	t.Helper()
	for _, c := range cs {
		if c.ID == id {
			return c.Status
		}
	}
	t.Fatalf("no %s check", id)
	return ""
}

func moverRate() (rate, angle float64) {
	return math.Hypot(trackVX, trackVY) * synthScale / 60, math.Atan2(trackVY, trackVX) * 180 / math.Pi
}

func TestLinearMoverPassesTrackChecks(t *testing.T) {
	t.Parallel()
	f := newTrackField()
	pos := func(k int) (float64, float64) {
		h := f.hours(k)
		return trackX0 + trackVX*h, trackY0 + trackVY*h
	}
	for k := range f.subs {
		x, y := pos(k)
		f.subs[k].extra = [][3]float64{{x, y, moverFlux18}}
	}
	rate, angle := moverRate()
	cs, fl, res := runTrack(t, f, f.spot(pos, 9), rate, angle)
	for _, id := range []string{check.MoverFrames, check.MoverSensor, check.MoverShape, check.MoverSteady} {
		if s := status(t, cs, id); s != check.Pass {
			t.Errorf("%s: %s, want pass", id, s)
		}
	}
	if fl != (TrackFlags{}) {
		t.Errorf("flags %+v", fl)
	}
	var sum, n float64
	for _, s := range res.Subs {
		if s.Measured {
			sum += s.Flux
			n++
		}
	}
	if n != trackSubs || math.Abs(sum/n-moverFlux18*0.9)/moverFlux18 > 0.2 {
		t.Errorf("measured %.0f subs, mean flux %.4f against about %.4f put in", n, sum/n, moverFlux18*0.9)
	}
}

func TestDitherFollowingHotPixelFailsTrackChecks(t *testing.T) {
	t.Parallel()
	f := newTrackField()
	f.hotSX, f.hotSY = 110, 90
	for k := range f.subs {
		f.subs[k].u, f.subs[k].v = 2+k, 3+k/2
		f.subs[k].sensorHot = 0.05
	}
	pos := func(k int) (float64, float64) {
		return float64(f.hotSX + f.subs[k].u), float64(f.hotSY + f.subs[k].v)
	}
	cs, fl, _ := runTrack(t, f, f.spot(pos, 12), 0.2, 27)
	if status(t, cs, check.MoverSensor) != check.Fail || !fl.HotPixel {
		t.Errorf("a hot pixel walked by the frames passed the sensor check: %+v", fl)
	}
	if status(t, cs, check.MoverShape) != check.Fail || !fl.NotStar {
		t.Errorf("a single hot pixel passed as star-shaped: %+v", fl)
	}
}

func TestSingleFrameGlintFailsTrackChecks(t *testing.T) {
	t.Parallel()
	f := newTrackField()
	pos := func(k int) (float64, float64) {
		h := f.hours(k)
		return trackX0 + trackVX*h, trackY0 + trackVY*h
	}
	x, y := pos(4)
	f.subs[4].extra = [][3]float64{{x, y, 0.5}}
	rate, angle := moverRate()
	cs, fl, _ := runTrack(t, f, f.spot(pos, 5), rate, angle)
	if status(t, cs, check.MoverSteady) != check.Fail || !fl.Flash {
		t.Errorf("a single-frame flash passed as steady: %+v", fl)
	}
	if status(t, cs, check.MoverFrames) != check.Fail || !fl.Faint {
		t.Errorf("a single-frame flash passed as seen in enough frames: %+v", fl)
	}
}

func TestHotDarkPixelsLinedUpFailTrackChecks(t *testing.T) {
	t.Parallel()
	f := newTrackField()
	pos := func(k int) (float64, float64) {
		h := f.hours(k)
		return math.Round(trackX0 + trackVX*h), math.Round(trackY0 + trackVY*h)
	}
	r := rand.New(rand.NewPCG(11, 3))
	d := make([]float32, synthW*synthH)
	for i := range d {
		d[i] = float32(0.01 + 0.0005*r.NormFloat64())
	}
	dk := darkKey
	for k := range f.subs {
		x, y := pos(k)
		f.subs[k].extra = [][3]float64{{x, y, moverFlux18}}
		f.subs[k].DarkKey = &dk
		d[(int(y)-f.subs[k].v)*synthW+int(x)-f.subs[k].u] = 0.5
	}
	var b bytes.Buffer
	if err := imagedata.WriteFITS(&b, synthW, synthH, 1, d, nil); err != nil {
		t.Fatal(err)
	}
	f.blobs[darkKey] = b.Bytes()
	rate, angle := moverRate()
	cs, fl, res := runTrack(t, f, f.spot(pos, 9), rate, angle)
	if status(t, cs, check.MoverSensor) != check.Fail || !fl.HotPixel || res.Dark != trackSubs || 2*res.Near >= res.NearN {
		t.Errorf("detections on a different hot dark pixel each frame passed: %+v, %d of %d hot, %d of %d near", fl, res.Dark, res.DarkN, res.Near, res.NearN)
	}
}
