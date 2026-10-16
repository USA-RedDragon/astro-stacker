package ownimage

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
)

const (
	synthW     = 360
	synthH     = 280
	synthSky   = 0.05
	synthNoise = 0.0015
	synthObj   = "Synth"
	synthLum   = "Luminance"
	darkKey    = "calibration/dark/d1.fit"
	spotX      = 180.3
	spotY      = 140.6
	flaggedDay = "2025-06-03"
	newX       = 232.4
	newY       = 196.7
)

type synthStar struct {
	x, y, g, bprp float64
	variable      bool
}

type synthSub struct {
	Sub
	u, v       int
	sigma, tr  float64
	targetDMag float64
	hotSpot    float64
	sensorHot  float64
	streak     float64
	transient  float64
	seed       uint64
}

type synthField struct {
	w     *discoveries.WCS
	stars []synthStar
	subs  []synthSub
	blobs map[string][]byte
	hotSX int
	hotSY int
}

func synthWCS() *discoveries.WCS {
	scale := 1.9 / 3600
	w, err := discoveries.ParseWCS(map[string]string{
		"CTYPE1": "'RA---TAN'", "CTYPE2": "'DEC--TAN'", "CRVAL1": "150.0", "CRVAL2": "30.0",
		"CRPIX1": fmt.Sprint(synthW / 2), "CRPIX2": fmt.Sprint(synthH / 2),
		"CD1_1": fmt.Sprint(-scale), "CD1_2": "0", "CD2_1": "0", "CD2_2": fmt.Sprint(scale),
	}, synthH)
	if err != nil {
		panic(err)
	}
	return w
}

func newSynth() *synthField {
	f := &synthField{w: synthWCS(), blobs: map[string][]byte{}}
	f.stars = append(f.stars, synthStar{x: spotX, y: spotY, g: 15, bprp: 1})
	for _, p := range [][2]float64{{60, 60}, {300, 60}, {60, 220}, {300, 220}, {110, 140}, {250, 140}, {180, 50}, {180, 230}} {
		f.stars = append(f.stars, synthStar{x: p[0] + 0.3, y: p[1] + 0.6, g: 13, bprp: 0.8})
	}
	for k := range 12 {
		a := float64(k) * 2 * math.Pi / 12
		r := 60 + 10*float64(k%4)
		f.stars = append(f.stars, synthStar{x: spotX + r*math.Cos(a), y: spotY + r*math.Sin(a), g: 14.7 + 0.05*float64(k), bprp: 1.1})
	}
	f.stars = append(f.stars, synthStar{x: spotX + 40, y: spotY - 30, g: 15.1, bprp: 1, variable: true},
		synthStar{x: spotX + 7, y: spotY - 5, g: 18.5, bprp: 1.2})
	return f
}

// addNights gives every night n subs with dithers, seeing and
// transparency that change from sub to sub.
func (f *synthField) addNights(nights []time.Time, n int) {
	id := 1
	var seed uint64
	for _, night := range nights {
		for range n {
			seed++
			r := rand.New(rand.NewPCG(seed, 7))
			k := (id - 1) % n
			start := night.Add(26*time.Hour + time.Duration(k)*6*time.Minute)
			s := synthSub{Sub: Sub{FrameID: id, Filter: synthLum, Night: night, Start: start, Exposure: 300, Weight: 1 + r.Float64(),
				Key: fmt.Sprintf("registered/%s/%s/%d.xisf", synthObj, synthLum, id), AddedAt: start.Add(time.Hour)},
				u: 2 + r.IntN(12), v: 2 + r.IntN(12), sigma: 1.2 + 0.3*r.Float64(), tr: 0.8 + 0.2*r.Float64(), seed: seed}
			if s.u == 6 && s.v == 5 {
				s.u = 7
			}
			f.subs = append(f.subs, s)
			id++
		}
	}
}

func flux(g float64) float64 { return math.Pow(10, -0.4*(g-15)) }

func (f *synthField) render(s synthSub) *discoveries.Plane {
	r := rand.New(rand.NewPCG(s.seed, 99))
	p := discoveries.NewPlane(synthW, synthH)
	for i := range p.Data {
		p.Data[i] = float32(synthSky + synthNoise*r.NormFloat64())
	}
	for k, st := range f.stars {
		fl := flux(st.g) * s.tr
		if k == 0 {
			fl *= math.Pow(10, -0.4*s.targetDMag)
		}
		gaussian(p, st.x, st.y, s.sigma, fl)
	}
	gaussian(p, newX, newY, s.sigma, s.transient*s.tr)
	cx, cy := int(math.Round(spotX)), int(math.Round(spotY))
	p.Data[cy*synthW+cx] += float32(s.hotSpot)
	if s.sensorHot > 0 {
		x, y := f.hotSX+s.u, f.hotSY+s.v
		if x >= 0 && y >= 0 && x < synthW && y < synthH {
			p.Data[y*synthW+x] += float32(s.sensorHot)
		}
	}
	if s.streak > 0 {
		for x := cx - 15; x <= cx+15; x++ {
			for dy := -1; dy <= 1; dy++ {
				y := cy + (x-cx)/2 + dy
				p.Data[y*synthW+x] += float32(s.streak * math.Exp(-float64(dy*dy)))
			}
		}
	}
	for y := range synthH {
		for x := range synthW {
			if sx, sy := x-s.u, y-s.v; sx < 0 || sy < 0 || sx >= synthW || sy >= synthH {
				p.Data[y*synthW+x] = float32(math.NaN())
			}
		}
	}
	return p
}

func gaussian(p *discoveries.Plane, x, y, sigma, fl float64) {
	n := fl / (2 * math.Pi * sigma * sigma)
	for yy := int(y - 6*sigma); yy <= int(y+6*sigma)+1; yy++ {
		for xx := int(x - 6*sigma); xx <= int(x+6*sigma)+1; xx++ {
			if xx < 0 || yy < 0 || xx >= p.W || yy >= p.H {
				continue
			}
			dx, dy := float64(xx)-x, float64(yy)-y
			p.Data[yy*p.W+xx] += float32(n * math.Exp(-(dx*dx+dy*dy)/(2*sigma*sigma)))
		}
	}
}

func (f *synthField) Subs(_ context.Context, object string) ([]Sub, error) {
	if object != synthObj {
		return nil, fmt.Errorf("no target %s", object)
	}
	out := make([]Sub, len(f.subs))
	for i, s := range f.subs {
		out[i] = s.Sub
	}
	return out, nil
}

func (f *synthField) Load(_ context.Context, key string) (*discoveries.Plane, error) {
	for _, s := range f.subs {
		if s.Key == key {
			return f.render(s), nil
		}
	}
	return nil, fmt.Errorf("no sub %s", key)
}

func (f *synthField) Get(_ context.Context, key string) ([]byte, error) {
	if b, ok := f.blobs[key]; ok {
		return b, nil
	}
	return nil, fmt.Errorf("no object %s", key)
}

func (f *synthField) Frame(context.Context, string) (*discoveries.WCS, int, int, error) {
	return f.w, synthW, synthH, nil
}

func (f *synthField) Stars(_ context.Context, ra, dec, radius float64) ([]Star, error) {
	var out []Star
	for i, s := range f.stars {
		sra, sdec := f.w.SkyOf(s.x, s.y)
		if discoveries.Separation(ra, dec, sra, sdec) <= radius {
			out = append(out, Star{SourceID: int64(i + 1), RA: sra, Dec: sdec, G: s.g, BPRP: s.bprp, Variable: s.variable})
		}
	}
	return out, nil
}

func day(s string) time.Time {
	t, err := time.Parse(dateLayout, s)
	if err != nil {
		panic(err)
	}
	return t
}

func synthNights() []time.Time {
	days := []string{"2025-01-02", "2025-01-05", "2025-03-01", "2025-03-04", "2025-06-01", "2025-06-02", flaggedDay, "2025-06-04"}
	out := make([]time.Time, 0, len(days))
	for _, s := range days {
		out = append(out, day(s))
	}
	return out
}

func (f *synthField) spot(sign int, dm float64) Spot {
	ra, dec := f.w.SkyOf(spotX, spotY)
	g := 15.0
	return Spot{SourceID: 7, Object: synthObj, Kind: app.DiscoveryKindChange, RA: ra, Dec: dec, GaiaG: &g, Nights: []Night{{
		Night: day(flaggedDay), Group: discoveries.GroupBroadband, X: spotX, Y: spotY, Sign: sign, DeltaMag: &dm,
		RunAt: day("2025-06-05"),
	}}}
}

func runSynth(t *testing.T, f *synthField, s Spot) Result {
	t.Helper()
	res, err := Measure(context.Background(), f, synthObj, []Spot{s}, Options{Threads: 4})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 {
		t.Fatalf("got %d results", len(res))
	}
	for _, c := range res[0].Checks {
		t.Logf("%-10s %-4s %s", c.ID, c.Status, c.Evidence)
	}
	return res[0]
}

func checkOf(t *testing.T, r Result, id string) check.Check {
	t.Helper()
	for _, c := range r.Checks {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no %s check", id)
	return check.Check{}
}

func want(t *testing.T, r Result, id string, s check.Status, has ...string) {
	t.Helper()
	c := checkOf(t, r, id)
	if c.Status != s {
		t.Errorf("%s: got %s (%s), want %s", id, c.Status, c.Evidence, s)
	}
	for _, h := range has {
		if !strings.Contains(c.Evidence, h) {
			t.Errorf("%s: evidence %q lacks %q", id, c.Evidence, h)
		}
	}
}

func TestEclipseIsMeasuredAndPasses(t *testing.T) {
	t.Parallel()
	f := newSynth()
	f.addNights(synthNights(), 6)
	for i := range f.subs {
		if nightKey(f.subs[i].Night) == flaggedDay {
			k := float64(i % 6)
			f.subs[i].targetDMag = 0.3 * math.Exp(-(k-2.5)*(k-2.5)/8)
		}
	}
	r := runSynth(t, f, f.spot(-1, 0.25))
	for _, id := range []string{check.HotPixel, check.RefClean, check.Trail, check.PSF, check.Edge, check.OwnLC, check.OwnChange, check.OwnQuiet} {
		want(t, r, id, check.Pass)
	}
	want(t, r, check.OwnLC, check.Pass, "Measured against 12 steady nearby stars over 6 frames.", "The dip bottomed out")
	if r.Flags != (Flags{}) {
		t.Errorf("flags %+v, want none", r.Flags)
	}
	c := r.Curve
	if c == nil || c.ChangeNight != flaggedDay {
		t.Fatalf("curve %+v", c)
	}
	var truth float64
	for i := range 6 {
		truth += 0.3 * math.Exp(-(float64(i)-2.5)*(float64(i)-2.5)/8) / 6
	}
	if math.Abs(c.Change-truth) > 0.02 || c.ChangeSigma < 5 || math.Abs(c.Quiet) > 0.02 {
		t.Errorf("change %.3f (truth %.3f) sigma %.1f quiet %.3f", c.Change, truth, c.ChangeSigma, c.Quiet)
	}
	if len(c.Points) != len(f.subs) {
		t.Errorf("%d points for %d subs", len(c.Points), len(f.subs))
	}
	ss := c.Series()
	if len(ss) != 1 || ss[0].Band != synthLum || !ss[0].Relative || len(ss[0].Points) != len(f.subs) {
		t.Errorf("series %+v", ss)
	}
}

func TestContaminatedReferenceFails(t *testing.T) {
	t.Parallel()
	f := newSynth()
	f.addNights(synthNights(), 6)
	for i := range f.subs {
		if n := nightKey(f.subs[i].Night); n == "2025-01-02" || n == "2025-01-05" {
			f.subs[i].hotSpot = 0.6
		}
	}
	r := runSynth(t, f, f.spot(-1, 0.3))
	want(t, r, check.RefClean, check.Fail, "Hot pixels at this spot in Luminance subs from 2025-01-02 and 2025-01-05 went into the reference image.")
	want(t, r, check.OwnChange, check.Fail, "against 0.30 mag fainter reported")
	if !r.Flags.RefContaminated {
		t.Errorf("flags %+v", r.Flags)
	}
	if c := r.Curve; c == nil || math.Abs(c.Change) > 0.02 {
		t.Errorf("curve not flat: %+v", c)
	}
}

func TestClippedOutlierDoesNotFailARealChange(t *testing.T) {
	t.Parallel()
	f := newSynth()
	f.addNights(synthNights(), 6)
	for i := range f.subs {
		if nightKey(f.subs[i].Night) == flaggedDay {
			f.subs[i].targetDMag = 0.3
		}
	}
	f.subs[0].hotSpot = 0.6
	r := runSynth(t, f, f.spot(-1, 0.3))
	want(t, r, check.RefClean, check.Pass, "1 of the 36 reference subs had extra light at this spot")
	want(t, r, check.OwnChange, check.Pass, "The night was 0.30 mag fainter on average.")
}

func darkFITS(t *testing.T, x, y int) []byte {
	t.Helper()
	r := rand.New(rand.NewPCG(5, 5))
	d := make([]float32, synthW*synthH)
	for i := range d {
		d[i] = float32(0.01 + 0.0005*r.NormFloat64())
	}
	d[y*synthW+x] = 0.5
	var b bytes.Buffer
	if err := imagedata.WriteFITS(&b, synthW, synthH, 1, d, nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestHotPixelOnTheSensorFails(t *testing.T) {
	t.Parallel()
	f := newSynth()
	f.addNights(synthNights(), 6)
	f.hotSX, f.hotSY = int(math.Round(spotX))-6, int(math.Round(spotY))-5
	f.blobs[darkKey] = darkFITS(t, f.hotSX, f.hotSY)
	dk := darkKey
	moves := [][2]int{{6, 5}, {6, 5}, {6, 5}, {12, 3}, {3, 11}, {10, 10}}
	k := 0
	for i := range f.subs {
		f.subs[i].sensorHot = 0.3
		if nightKey(f.subs[i].Night) == flaggedDay {
			f.subs[i].u, f.subs[i].v = moves[k][0], moves[k][1]
			f.subs[i].DarkKey = &dk
			k++
		}
	}
	r := runSynth(t, f, f.spot(1, -0.3))
	want(t, r, check.HotPixel, check.Fail, "the change shows only in the 3 frames that put the spot on the same sensor pixel", "The master dark has a hot pixel under the spot")
	if !r.Flags.HotPixel {
		t.Errorf("flags %+v", r.Flags)
	}
}

func TestSatelliteTrailFails(t *testing.T) {
	t.Parallel()
	f := newSynth()
	f.addNights(synthNights(), 6)
	for i := range f.subs {
		if nightKey(f.subs[i].Night) == flaggedDay && i%6 == 2 {
			f.subs[i].streak = 0.05
		}
	}
	r := runSynth(t, f, f.spot(1, -0.05))
	want(t, r, check.Trail, check.Fail, "a streak")
	if !r.Flags.Trail {
		t.Errorf("flags %+v", r.Flags)
	}
}

func TestMoverAndOutsideAreNotMeasured(t *testing.T) {
	t.Parallel()
	f := newSynth()
	f.addNights(synthNights()[:3], 2)
	mover := f.spot(1, -0.3)
	mover.Kind = "mover"
	out := f.spot(1, -0.3)
	out.SourceID, out.RA = 8, out.RA+1
	res, err := Measure(context.Background(), f, synthObj, []Spot{mover, out}, Options{Threads: 2})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		if len(r.Checks) != 8 || r.Curve != nil {
			t.Fatalf("result %+v", r)
		}
		for _, c := range r.Checks {
			if c.Status != check.NotRun || !strings.HasPrefix(c.Evidence, "Not measured") {
				t.Errorf("%+v", c)
			}
		}
	}
}

func TestPickRefTakesFarNightsFirst(t *testing.T) {
	t.Parallel()
	night := day(flaggedDay)
	subs := []Sub{
		{Night: day("2025-06-01"), Weight: 5}, {Night: day("2025-01-02"), Weight: 1}, {Night: day("2025-01-02"), Weight: 3},
		{Night: day("2025-03-01"), Weight: 2},
	}
	by := map[time.Time][]int{}
	for i, s := range subs {
		by[s.Night] = append(by[s.Night], i)
	}
	got := pickRef(subs, by, night, 3)
	if fmt.Sprint(got) != "[2 3 1]" {
		t.Errorf("got %v", got)
	}
}

func TestInRefJudgesLateSubs(t *testing.T) {
	t.Parallel()
	run := day("2025-06-05")
	cases := []struct {
		s    Sub
		want bool
	}{
		{Sub{Start: run.Add(time.Hour)}, false},
		{Sub{Start: run.Add(-time.Hour), AddedAt: run.Add(time.Hour)}, false},
		{Sub{Start: run.Add(-time.Hour), AddedAt: run.Add(-time.Minute)}, true},
		{Sub{Start: run.Add(-30 * 24 * time.Hour), AddedAt: run.Add(10 * 24 * time.Hour)}, true},
	}
	for i, c := range cases {
		if got := inRef(c.s, run); got != c.want {
			t.Errorf("case %d: got %v", i, got)
		}
	}
}

func TestNewSourceAndEclipseShareOneRead(t *testing.T) {
	t.Parallel()
	f := newSynth()
	f.addNights(synthNights(), 6)
	for i := range f.subs {
		if nightKey(f.subs[i].Night) == flaggedDay {
			f.subs[i].targetDMag = 0.3
			f.subs[i].transient = 0.5
		}
	}
	eclipse := f.spot(-1, 0.3)
	alone, err := Measure(context.Background(), f, synthObj, []Spot{eclipse}, Options{Threads: 2})
	if err != nil {
		t.Fatal(err)
	}
	fresh := f.spot(1, -1)
	fresh.SourceID, fresh.GaiaG = 9, nil
	fresh.RA, fresh.Dec = f.w.SkyOf(newX, newY)
	both, err := Measure(context.Background(), f, synthObj, []Spot{eclipse, fresh}, Options{Threads: 2})
	if err != nil {
		t.Fatal(err)
	}
	for k, c := range alone[0].Checks {
		if both[0].Checks[k].Status != c.Status {
			t.Errorf("eclipse %s changed with a second spot: %q against %q", c.ID, both[0].Checks[k].Evidence, c.Evidence)
		}
	}
	if a, b := alone[0].Curve, both[0].Curve; a == nil || b == nil || math.Abs(a.Change-b.Change) > 0.005 {
		t.Errorf("eclipse change moved with a second spot: %+v against %+v", b, a)
	}
	r := both[1]
	if r.Curve != nil {
		t.Errorf("a curve for a spot with no star: %+v", r.Curve)
	}
	for _, id := range []string{check.OwnLC, check.OwnChange, check.OwnQuiet} {
		want(t, r, id, check.NotRun, "no catalogued star at this spot")
	}
	for _, id := range []string{check.HotPixel, check.RefClean, check.PSF, check.Edge} {
		want(t, r, id, check.Pass)
	}
}

func TestMixedSignsCountEachNightItsOwnWay(t *testing.T) {
	t.Parallel()
	f := newSynth()
	f.addNights(synthNights(), 6)
	for i := range f.subs {
		switch nightKey(f.subs[i].Night) {
		case flaggedDay:
			f.subs[i].targetDMag = 0.3
		case "2025-06-04":
			f.subs[i].targetDMag = -0.3
		}
	}
	s := f.spot(-1, 0.3)
	up := s.Nights[0]
	up.Night, up.Sign = day("2025-06-04"), 1
	dm := -0.3
	up.DeltaMag = &dm
	s.Nights = append(s.Nights, up)
	r := runSynth(t, f, s)
	want(t, r, check.OwnChange, check.Pass, "some reported fainter and some brighter, the star moved 0.30 mag the reported way")
	want(t, r, check.HotPixel, check.Pass)
}

func TestStampsOnDiskMatchInMemory(t *testing.T) {
	t.Parallel()
	f := newSynth()
	f.addNights(synthNights(), 6)
	for i := range f.subs {
		if nightKey(f.subs[i].Night) == flaggedDay && i%6 == 2 {
			f.subs[i].streak = 0.05
		}
	}
	spots := []Spot{f.spot(1, -0.05)}
	mem, err := Measure(context.Background(), f, synthObj, spots, Options{Threads: 4})
	if err != nil {
		t.Fatal(err)
	}
	disk, err := Measure(context.Background(), f, synthObj, spots, Options{Threads: 4, WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	show := func(r Result) string { return fmt.Sprintf("%+v %+v %+v", r.Checks, r.Flags, *r.Curve) }
	if a, b := show(mem[0]), show(disk[0]); a != b {
		t.Fatalf("stamps on disk changed the checks:\n%s\n%s", a, b)
	}
	if checkOf(t, disk[0], check.Trail).Status != check.Fail {
		t.Error("the trail check did not read its stamps back")
	}
}
