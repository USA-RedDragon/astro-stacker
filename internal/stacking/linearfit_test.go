package stacking

import (
	"context"
	"errors"
	"math"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// The fit recovers the relation between two masters despite noise, and to
// within a few percent despite a patch of structure only the target filter
// sees.
func TestFitLinearRecoversScaleAndOffset(t *testing.T) {
	t.Parallel()
	const w, h = 400, 300
	r := rand.New(rand.NewPCG(3, 3))
	ref := make([]float32, w*h)
	target := make([]float32, w*h)
	for i := range ref {
		sky := 0.05 + 0.1*float64(i%w)/w + 0.05*r.Float64() // gradient plus faint signal
		ref[i] = float32(sky + 0.002*r.NormFloat64())
		target[i] = float32((sky-0.02)/2.5 + 0.001*r.NormFloat64()) // ref = 0.02 + 2.5×target
	}
	for y := 50; y < 100; y++ { // emission only the target filter sees
		for x := 50; x < 150; x++ {
			target[y*w+x] += 0.2
		}
	}
	target[0], ref[1] = 0, 0.99 // no data; saturated
	offset, scale, err := fitLinear(target, ref, w, Rect{W: w, H: h})
	if err != nil || math.Abs(scale-2.5) > 0.2 || math.Abs(offset-0.02) > 0.01 {
		t.Errorf("offset %v scale %v err %v, want 0.02 2.5", offset, scale, err)
	}
	applyLinear(target, offset, scale)
	if target[0] != 0 {
		t.Errorf("empty pixel = %v, want 0", target[0])
	}
}

// faintPair makes two masters of the same sky, mostly background whose
// pixel noise swamps the nebula and stars both filters share, as in a
// short or moonlit narrowband master: ref = 0.004 + 3×(target - 0.013),
// with noise in proportion.
func faintPair(seed uint64, w, h int) (target, ref []float32) {
	r := rand.New(rand.NewPCG(seed, seed))
	target = make([]float32, w*h)
	ref = make([]float32, w*h)
	for y := range h {
		for x := range w {
			// A faint nebula over part of the frame, and a few stars.
			sig := 0.0004 * math.Exp(-(math.Pow(float64(x-w/3), 2)+math.Pow(float64(y-h/2), 2))/(2*60*60))
			if r.IntN(400) == 0 {
				sig += 0.05 * r.Float64()
			}
			i := y*w + x
			target[i] = float32(0.013 + sig + 0.0003*r.NormFloat64())
			ref[i] = float32(0.004 + 3*sig + 0.0009*r.NormFloat64())
		}
	}
	return target, ref
}

// A faint master, whose sky noise is most of every pixel, is fitted at the
// scale its stars and nebula show, not pulled towards 0 by the noise, and
// fitting the pair the other way round gives the inverse.
func TestFitLinearFaintMaster(t *testing.T) {
	t.Parallel()
	const w, h = 400, 300
	target, ref := faintPair(5, w, h)
	offset, scale, err := fitLinear(target, ref, w, Rect{W: w, H: h})
	if err != nil || math.Abs(scale-3) > 0.3 || math.Abs(offset-(0.004-3*0.013)) > 0.004 {
		t.Errorf("offset %v scale %v err %v, want %v 3", offset, scale, err, 0.004-3*0.013)
	}
	_, back, err := fitLinear(ref, target, w, Rect{W: w, H: h})
	if err != nil || math.Abs(back*scale-1) > 1e-3 {
		t.Errorf("reverse scale %v × %v = %v, want 1", back, scale, back*scale)
	}
}

// Two masters with nothing in common can't be fitted, and say so as a
// failure that more tries won't fix.
func TestFitLinearUnrelated(t *testing.T) {
	t.Parallel()
	const w, h = 200, 200
	r := rand.New(rand.NewPCG(7, 7))
	target := make([]float32, w*h)
	ref := make([]float32, w*h)
	for i := range target {
		target[i] = float32(0.01 + 0.001*r.NormFloat64())
		ref[i] = float32(0.02 - 0.5*(float64(target[i])-0.01) + 0.0001*r.NormFloat64()) // anticorrelated
	}
	_, _, err := fitLinear(target, ref, w, Rect{W: w, H: h})
	var ff *fitError
	if !errors.As(err, &ff) {
		t.Errorf("err %v, want a fitError", err)
	}
	_, _, err = fitLinear(target, ref, w, Rect{W: 5, H: 5})
	if !errors.As(err, &ff) {
		t.Errorf("tiny overlap: err %v, want a fitError", err)
	}
	_, _, err = fitMasters(&imagedata.Image{W: 2, H: 1, C: 1, Data: []float32{1, 1}},
		&imagedata.Image{W: 1, H: 2, C: 1, Data: []float32{1, 1}}, Rect{W: 2, H: 1}, Rect{W: 1, H: 2})
	if !errors.As(err, &ff) {
		t.Errorf("mismatched sizes: err %v, want a fitError", err)
	}
}

// The reference is the same filter on every panel of a mosaic, whichever
// has the most effective exposure.
func TestFitReference(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		masters []app.Stack
		want    string
	}{
		{[]app.Stack{{Filter: filterHa, EffectiveSeconds: 834}, {Filter: filterOIII, EffectiveSeconds: 151}, {Filter: filterSII, EffectiveSeconds: 1043}}, filterHa},
		{[]app.Stack{{Filter: filterOIII, EffectiveSeconds: 9}, {Filter: filterSII, EffectiveSeconds: 1}}, filterOIII},
		{[]app.Stack{{Filter: filterBlue, EffectiveSeconds: 9}, {Filter: filterGreen}, {Filter: filterRed, EffectiveSeconds: 1}}, filterRed},
		{[]app.Stack{{Filter: filterBlue, EffectiveSeconds: 9}, {Filter: filterGreen}}, filterGreen},
	} {
		if got := c.masters[fitReference(c.masters)].Filter; got != c.want {
			t.Errorf("fitReference(%v) = %s, want %s", c.masters, got, c.want)
		}
	}
}

// A master that couldn't be fitted isn't tried again until one of its
// group's masters changes; one whose fit never got recorded (a failed
// download or upload) is.
func TestFailedFitWaitsForNewMasters(t *testing.T) {
	t.Parallel()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&app.Stack{}); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	for _, f := range []string{filterHa, filterOIII, filterSII} {
		key := "stacks/T/" + f + "/master.fit"
		if err := db.Create(&app.Stack{Object: "T", Filter: f, Subs: 1, MasterKey: &key, UpdatedAt: old}).Error; err != nil {
			t.Fatal(err)
		}
	}
	load := func() []app.Stack {
		var s []app.Stack
		if err := db.Order("filter").Find(&s).Error; err != nil {
			t.Fatal(err)
		}
		return s
	}
	masters := load()
	sig, due := fitDue(masters, 10*time.Minute)
	if !due {
		t.Fatal("new masters not due")
	}
	if _, due := fitDue(masters, 2*time.Hour); due {
		t.Error("due before the masters were left alone")
	}
	// The pass fitted H-a and S-II, then failed O-III.
	p := &Pipeline{db: db}
	for i, m := range masters {
		if m.Filter == filterOIII {
			if err := p.failFit(context.Background(), &masters[i], sig, "fitting O-III to H-a: no positive relation"); err != nil {
				t.Fatal(err)
			}
			continue
		}
		db.Model(&masters[i]).UpdateColumns(map[string]any{"fit_signature": sig})
	}
	masters = load()
	if _, due := fitDue(masters, 10*time.Minute); due {
		t.Error("failed fit due again with the same masters")
	}
	if e := masters[1].FitError; e == nil || *e == "" || masters[1].FittedKey != nil {
		t.Errorf("O-III fit error %v fitted %v", e, masters[1].FittedKey)
	}
	// A new O-III master makes it due.
	db.Model(&masters[1]).Update("updated_at", old.Add(time.Minute))
	if _, due := fitDue(load(), 10*time.Minute); !due {
		t.Error("not due after a master changed")
	}
}

func TestSexagesimal(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		v      float64
		signed bool
		want   string
	}{
		{0.7123, false, "00 42 44.28"},
		{41.2692, true, "+41 16 09.1"},
		{-5.391, true, "-05 23 27.6"},
		{23.99999999, false, "00 00 00.00"},
	} {
		if got := sexagesimal(c.v, c.signed); got != c.want {
			t.Errorf("sexagesimal(%v) = %q, want %q", c.v, got, c.want)
		}
	}
}

func TestFitGroup(t *testing.T) {
	t.Parallel()
	for filter, want := range map[string]string{
		filterRed: "Red/Green/Blue", filterBlue: "Red/Green/Blue",
		filterHa: "H-a/O-III/S-II", filterSII: "H-a/O-III/S-II",
		filterLuminance: "", "Clear": "",
	} {
		if got := fitGroup(filter); got != want {
			t.Errorf("fitGroup(%q) = %q, want %q", filter, got, want)
		}
	}
}
