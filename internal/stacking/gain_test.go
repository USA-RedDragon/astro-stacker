package stacking

import (
	"context"
	"math"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// Orion H-a's mix in small: short subs at a low gain and long ones at a gain
// recording 3× the ADU per electron, whose exposures saturate a bright core
// that only the short subs fill in. Scaled by exposure alone the core is 3×
// too dim against the nebula around it; with the measured gain it matches.
func TestMixedGains(t *testing.T) {
	t.Parallel()
	const w, h = 1024, 768 // 8×6 cells
	const hiGain = 3.0
	r := rand.New(rand.NewPCG(3, 7))
	truth := lnScene(w, h, r) // per second at gain 0
	for y := range h {
		for x := range w {
			dx, dy := float64(x-w/2), float64(y-h/2)
			truth[y*w+x] += 2e-3 * math.Exp(-(dx*dx+dy*dy)/(2*20*20))
		}
	}
	type sub struct {
		px             []float32
		gain, exposure float64
	}
	var subs []sub
	add := func(n int, gain, k, exposure float64) {
		for range n {
			px := make([]float32, w*h)
			for i, v := range truth {
				px[i] = float32(k*(3e-6+v)*exposure + 2e-4*r.NormFloat64())
			}
			subs = append(subs, sub{px, gain, exposure})
		}
	}
	add(8, 0, 1, 120)
	add(6, 100, hiGain, 600)

	opts := DefaultOptions()
	opts.MinSamples = math.MaxFloat32
	opts.LocalNorm = false
	groups := map[float64]*Accumulator{}
	for _, s := range subs {
		if groups[s.gain] == nil {
			groups[s.gain] = NewAccumulator(w, h)
		}
		if _, err := groups[s.gain].Add(s.px, s.exposure, s.exposure, opts); err != nil {
			t.Fatal(err)
		}
	}
	scales := measureGains(groups)
	// Gain 100 has the weight (3600 s against 960 s), so it's the reference.
	if scales[100] != 1 || math.Abs(scales[0]*hiGain-1) > 0.02 {
		t.Fatalf("scales %v, want gain 0 at %.4f", scales, 1/hiGain)
	}
	if got := decodeGainTable(scales.encode()); got[0] != scales[0] || got[100] != 1 {
		t.Errorf("round trip %v, want %v", got, scales)
	}

	// The core, saturated in every long sub, against the nebula around it.
	var core, ring []int
	for i, v := range truth {
		switch {
		case v > 1e-3:
			core = append(core, i)
		case v > 1e-4 && v < 4e-4:
			ring = append(ring, i)
		}
	}
	level := func(acc *Accumulator, px []int) float64 {
		q := make([]float64, 0, len(px))
		for _, i := range px {
			q = append(q, float64(acc.Mean[i])/(hiGain*truth[i]))
		}
		slices.Sort(q)
		return q[len(q)/2]
	}
	stack := func(gained bool) float64 {
		stored := make([]storedSub, len(subs))
		for i, s := range subs {
			stored[i] = storedSub{exposure: s.exposure, weight: s.exposure}
			if gained {
				stored[i].exposure *= scales.scale(&s.gain)
			}
		}
		acc, err := streamStack(stored, 2, DefaultOptions(), func(i int, _ storedSub) ([]float32, int, int, error) {
			return slices.Clone(subs[i].px), w, h, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		c, n := level(acc, core), level(acc, ring)
		t.Logf("gain scaled %v: core %.3f, nebula %.3f of gain 100's flux", gained, c, n)
		return c / n
	}
	if step := stack(false); step > 0.5 {
		t.Errorf("without gains the core is %.2f of the nebula's level; the test should show the step", step)
	}
	if step := stack(true); math.Abs(step-1) > 0.02 {
		t.Errorf("with gains the core is %.3f of the nebula's level, want 1", step)
	}
}

// Masters holding more than one gain are marked for restacking until
// rebuilt with the current gainMethod; one gain needs nothing. A batch
// adds incrementally only with every gain measured.
func TestMixedGainMasters(t *testing.T) {
	t.Parallel()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&app.Frame{}, &app.StackFrame{}, &app.Stack{}); err != nil {
		t.Fatal(err)
	}
	state := "state.fit"
	mixed := app.Stack{Object: objectOrion, Filter: filterHa, StateKey: &state}
	single := app.Stack{Object: objectM31, Filter: filterHa, StateKey: &state}
	for _, s := range []*app.Stack{&mixed, &single} {
		if err := db.Create(s).Error; err != nil {
			t.Fatal(err)
		}
	}
	g := func(v float64) *float64 { return &v }
	for i, f := range []struct {
		stack *app.Stack
		gain  *float64
	}{{&mixed, g(0)}, {&mixed, g(100)}, {&mixed, nil}, {&single, g(100)}, {&single, g(100)}, {&single, nil}} {
		fr := app.Frame{Key: string(rune('a' + i)), Type: frameTypeLight, LastModified: time.Now(), Gain: f.gain}
		if err := db.Create(&fr).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&app.StackFrame{FrameID: fr.ID, StackID: &f.stack.ID, Status: app.StackStatusAdded}).Error; err != nil {
			t.Fatal(err)
		}
	}
	p := &Pipeline{db: db}
	ctx := context.Background()
	marked := func() []int {
		t.Helper()
		var ids []int
		if err := db.Model(&app.Stack{}).Where("needs_rebuild").Order("id").Pluck("id", &ids).Error; err != nil {
			t.Fatal(err)
		}
		return ids
	}
	p.markMixedGains(ctx)
	if got := marked(); !slices.Equal(got, []int{mixed.ID}) {
		t.Errorf("marked %v, want only the mixed master %d", got, mixed.ID)
	}

	// Not measured: rebuild.
	if _, ok, err := p.batchGains(ctx, &mixed, []*float64{g(0)}); err != nil || ok {
		t.Errorf("unmeasured mixed master: ok %v, err %v", ok, err)
	}
	// One gain, even with a light of unknown gain: add as it is.
	if tab, ok, err := p.batchGains(ctx, &single, []*float64{g(100), nil}); err != nil || !ok || tab != nil {
		t.Errorf("single gain: %v %v %v", tab, ok, err)
	}
	// A new gain in a master of one: rebuild.
	if _, ok, _ := p.batchGains(ctx, &single, []*float64{g(0)}); ok {
		t.Error("a second gain added incrementally")
	}

	mixed.GainMethod, mixed.GainScales = gainMethod, gainTable{0: 0.328, 100: 1}.encode()
	mixed.NeedsRebuild = false
	if err := db.Save(&mixed).Error; err != nil {
		t.Fatal(err)
	}
	tab, ok, err := p.batchGains(ctx, &mixed, []*float64{g(0), g(100)})
	if err != nil || !ok || tab.scale(g(0)) != 0.328 || tab.scale(g(100)) != 1 || tab.scale(nil) != 1 {
		t.Errorf("measured master: %v %v %v", tab, ok, err)
	}
	if _, ok, _ := p.batchGains(ctx, &mixed, []*float64{g(200)}); ok {
		t.Error("an unmeasured gain added incrementally")
	}
	p.markMixedGains(ctx)
	if got := marked(); len(got) != 0 {
		t.Errorf("marked %v after rebuilding with gainMethod", got)
	}
}
