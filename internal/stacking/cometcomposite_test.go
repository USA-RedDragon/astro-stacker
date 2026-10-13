package stacking

import (
	"math"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

// cometScene is n subs of a star field, registered on the stars, with a
// comet moving 2.5 px a sub from (cx, cy) whose nucleus peaks at nucleus.
type cometScene struct {
	w, h, n int
	cx, cy  float64
	stars   [][3]float64
	subs    [][]float32
	shifts  [][2]float64
	track   [][2]float64
	stored  []storedSub
}

func (c *cometScene) blob(img []float32, cx, cy, f, s float64) {
	r := int(math.Ceil(4 * s))
	for y := max(0, int(cy)-r); y <= min(c.h-1, int(cy)+r); y++ {
		for x := max(0, int(cx)-r); x <= min(c.w-1, int(cx)+r); x++ {
			dx, dy := float64(x)-cx, float64(y)-cy
			img[y*c.w+x] += float32(f * math.Exp(-(dx*dx+dy*dy)/(2*s*s)))
		}
	}
}

func newCometScene(w, h, n int, nucleus float64, seed uint64) *cometScene {
	return newDriftScene(w, h, n, nucleus, 2.5, seed, nil)
}

// newDriftScene is newCometScene with the comet moving drift px a sub, and
// extra stars on every sub.
func newDriftScene(w, h, n int, nucleus, drift float64, seed uint64, extra [][3]float64) *cometScene {
	r := rand.New(rand.NewPCG(seed, 7))
	c := &cometScene{w: w, h: h, n: n, cx: float64(w) / 4, cy: float64(h) / 2}
	for range w * h / 350 {
		c.stars = append(c.stars, [3]float64{10 + r.Float64()*float64(w-20), 10 + r.Float64()*float64(h-20), 0.002 + 0.3*math.Pow(r.Float64(), 4)})
	}
	c.stars = append(c.stars, extra...)
	for i := range n {
		img := make([]float32, w*h)
		for j := range img {
			img[j] = float32(0.01 + 0.0005*r.NormFloat64())
		}
		seeing := 1.3 + 0.4*r.Float64()
		for _, s := range c.stars {
			c.blob(img, s[0], s[1], s[2], seeing)
		}
		drift := drift * float64(i)
		c.blob(img, c.cx+drift, c.cy, nucleus, 2)
		c.blob(img, c.cx+drift, c.cy, 0.01, 9)
		for j, v := range img {
			img[j] = min(v, 1)
		}
		c.subs = append(c.subs, img)
		c.shifts = append(c.shifts, [2]float64{-drift, 0})
		c.track = append(c.track, [2]float64{c.cx + drift, c.cy})
		c.stored = append(c.stored, storedSub{exposure: 10, weight: 10})
	}
	return c
}

func (c *cometScene) separate(t *testing.T) cometLayers {
	t.Helper()
	read := func(i int) ([]float32, int, int, error) { return slices.Clone(c.subs[i]), c.w, c.h, nil }
	layers, err := separateComet(c.stored, c.shifts, c.track, nil, DefaultOptions(), read, func(int, int) {})
	if err != nil {
		t.Fatal(err)
	}
	if layers.field == nil {
		t.Fatal("the comet wasn't found for the field")
	}
	return layers
}

func TestCometCompositeSaturatedNucleus(t *testing.T) {
	t.Parallel()
	c := newCometScene(400, 200, 12, 2, 3)
	m := c.separate(t).composite(10)
	if v := m[int(c.cy)*c.w+int(c.cx)]; v != 1 {
		t.Errorf("saturated nucleus is %.4f, want full scale", v)
	}
}

func TestCometCompositeOnStarField(t *testing.T) {
	t.Parallel()
	c := newCometScene(800, 400, 30, 0.03, 4)
	w, h, cx, cy := c.w, c.h, c.cx, c.cy
	layers := c.separate(t)
	truth := make([]float32, w*h)
	for _, s := range c.stars {
		c.blob(truth, s[0], s[1], s[2], 1.5)
	}
	c.blob(truth, cx, cy, 0.03, 2)
	c.blob(truth, cx, cy, 0.01, 9)
	m := layers.composite(10)
	al := layers.aligned(10)
	sky := func(p []float32) float32 {
		v := slices.Clone(p)
		slices.Sort(v)
		return v[len(v)/2]
	}
	resid := func(p []float32) (worst float64) {
		bg := sky(p)
		for y := 15; y < h-15; y++ {
			for x := 15; x < w-15; x++ {
				if truth[y*w+x] > 0.002 {
					continue
				}
				worst = max(worst, math.Abs(float64(p[y*w+x]-bg-truth[y*w+x])))
			}
		}
		return worst
	}
	if got, was := resid(m), resid(al); got > 0.004 || got > was {
		t.Errorf("composite strays %.4f from the sky between the stars, the comet stack %.4f", got, was)
	}
	held := layers.comet.Master(10)
	peak, want := m[int(cy)*w+int(cx)]-sky(m), held[int(cy)*w+int(cx)]-sky(held)
	if math.Abs(float64(peak-want)) > 0.05*float64(want) {
		t.Errorf("comet peak %.4f, the comet stack's %.4f", peak, want)
	}
	for _, s := range c.stars {
		if s[2] > 0.05 && math.Hypot(s[0]-cx, s[1]-cy) > 20 {
			if v := m[int(math.Round(s[1]))*w+int(math.Round(s[0]))] - sky(m); v < 0.6*float32(s[2]) {
				t.Errorf("star at (%.0f,%.0f) is %.3f, want about %.3f", s[0], s[1], v, s[2])
			}
		}
	}
	_, box, _ := cometSignal(layers.comet.Mean, w, h, [2]float64{cx, cy})
	if box.W*box.H > w*h/2 {
		t.Errorf("the comet's region %+v covers more than half the frame", box)
	}
}

// Two nights see the comet in different star fields. The master keeps the
// stars of one night, and the other night's stars, which would trail
// through it, none at all.
func TestCometCompositeTwoNights(t *testing.T) {
	t.Parallel()
	const w, h = 800, 400
	n1, n2 := newCometScene(w, h, 16, 0.03, 11), newCometScene(w, h, 16, 0.03, 12)
	sky1, sky2 := frameWCS(100, 20, 0, w, h), frameWCS(101.5, 20.8, 35, w, h)
	l1, l2 := n1.separate(t), n2.separate(t)
	raC1, decC1 := sky1.toSky(n1.cx+1, float64(h)-n1.cy)
	raC2, decC2 := sky2.toSky(n2.cx+1, float64(h)-n2.cy)
	alone := func(l cometLayers) []float32 {
		c := make([]float32, len(l.signal))
		for i, v := range l.signal {
			c[i] = v * 10
		}
		return c
	}
	comets, _ := combineNights(sky1, w, h, raC1, decC1, []nightImage{
		{data: alone(l1), w: w, h: h, sky: sky1, raC: raC1, decC: decC1, weight: 1},
		{data: alone(l2), w: w, h: h, sky: sky2, raC: raC2, decC: decC2, weight: 1},
	})
	m := cometOnField(l1.field.Master(10), comets)

	truth := make([]float32, w*h)
	for _, s := range n1.stars {
		n1.blob(truth, s[0], s[1], s[2], 1.5)
	}
	n1.blob(truth, n1.cx, n1.cy, 0.03, 2)
	n1.blob(truth, n1.cx, n1.cy, 0.01, 9)
	bg := sorted(m)[len(m)/2]
	var worst float64
	for y := 15; y < h-15; y++ {
		for x := 15; x < w-15; x++ {
			if truth[y*w+x] > 0.002 {
				continue
			}
			worst = max(worst, math.Abs(float64(m[y*w+x]-bg-truth[y*w+x])))
		}
	}
	if worst > 0.004 {
		t.Errorf("the master strays %.4f from night 1's sky between its stars", worst)
	}
	for _, s := range n1.stars {
		if s[2] > 0.05 && math.Hypot(s[0]-n1.cx, s[1]-n1.cy) > 20 {
			if v := m[int(math.Round(s[1]))*w+int(math.Round(s[0]))] - bg; v < 0.6*float32(s[2]) {
				t.Errorf("night 1's star at (%.0f,%.0f) is %.3f, want about %.3f", s[0], s[1], v, s[2])
			}
		}
	}
	peak := m[int(n1.cy)*w+int(n1.cx)] - bg
	h1 := l1.comet.Master(10)
	want := h1[int(n1.cy)*w+int(n1.cx)] - sorted(h1)[len(h1)/2]
	if math.Abs(float64(peak-want)) > 0.1*float64(want) {
		t.Errorf("comet peak %.4f, night 1's %.4f", peak, want)
	}
}

func sorted(p []float32) []float32 {
	v := slices.Clone(p)
	slices.Sort(v)
	return v
}

func TestPickFieldNight(t *testing.T) {
	t.Parallel()
	day := func(d int) time.Time { return time.Date(2026, 10, d, 0, 0, 0, 0, time.UTC) }
	rows := []app.BodyNight{{Night: day(12), EffectiveHours: 1}, {Night: day(13), EffectiveHours: 0.5}, {Night: day(14), EffectiveHours: 1}}
	all := append(slices.Clone(rows), app.BodyNight{Filter: filterRed, Night: day(13), EffectiveHours: 1})
	if got := pickFieldNight(rows, all); !got.Night.Equal(day(13)) {
		t.Errorf("picked %s, want the night with the most hours over all filters", got.Night)
	}
	if got := pickFieldNight(rows, rows); !got.Night.Equal(day(14)) {
		t.Errorf("picked %s, want the later of equal nights", got.Night)
	}
}

// The comet's sky is taken round it: a stack whose sky is higher there than
// over the frame would otherwise lift the whole region.
func TestCometSignalLocalSky(t *testing.T) {
	t.Parallel()
	const w, h = 1200, 600
	r := rand.New(rand.NewPCG(5, 5))
	img := make([]float32, w*h)
	for y := range h {
		for x := range w {
			v := 0.01 + 0.0002*r.NormFloat64()
			if x > w/2 {
				v += 0.0003
			}
			dx, dy := float64(x-900), float64(y-300)
			v += 0.01 * math.Exp(-(dx*dx+dy*dy)/(2*15*15))
			img[y*w+x] = float32(v)
		}
	}
	signal, box, ok := cometSignal(img, w, h, [2]float64{900, 300})
	if !ok {
		t.Fatal("no comet")
	}
	var edge []float64
	for x := box.X; x < box.X+box.W; x++ {
		for _, y := range []int{box.Y + box.H/2 - 80, box.Y + box.H/2 + 80} {
			if math.Hypot(float64(x-900), float64(y-300)) > 70 {
				edge = append(edge, float64(signal[y*w+x]))
			}
		}
	}
	if m := median(edge); math.Abs(m) > 0.00005 {
		t.Errorf("signal off the comet is %.6f, want about 0 (the sky round it is 0.0003 above the frame's)", m)
	}
}

// A slow comet stays by a star near its path through most subs. Taking the
// comet out of the subs on the stars must not take the star.
func TestCometCompositeKeepsStarsByThePath(t *testing.T) {
	t.Parallel()
	star := [3]float64{240, 220, 0.2}
	c := newDriftScene(800, 400, 30, 0.03, 0.8, 4, [][3]float64{star})
	m := c.separate(t).composite(10)
	j := int(star[1])*c.w + int(star[0])
	if got := m[j] - sorted(m)[len(m)/2]; got < 0.8*float32(star[2]) {
		t.Errorf("the star by the path is %.3f, want about %.3f", got, star[2])
	}
}
