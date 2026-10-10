package goalmeasure

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/frameheader"
	"github.com/USA-RedDragon/astro-stacker/internal/goals"
	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

const (
	testW, testH   = 480, 360
	testExposure   = 300.0
	testObject     = "Synth"
	testFilter     = "H-a"
	testZP         = 6.0
	testPixelScale = 1.915
)

type memObjects struct {
	mu    sync.Mutex
	files map[string][]byte
	gets  int
}

func (m *memObjects) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gets++
	b, ok := m.files[key]
	if !ok {
		return nil, fmt.Errorf("no object %s", key)
	}
	return b, nil
}

func openDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	return db
}

func testWCS() goals.WCS {
	s := testPixelScale / 3600
	return goals.WCS{RA0: 83.8, Dec0: -5.4, PX0: float64(testW+1) / 2, PY0: float64(testH+1) / 2, CD: [2][2]float64{{-s, 0}, {0, s}}}
}

func wcsJSON(t *testing.T, g goals.WCS) string {
	t.Helper()
	f := func(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }
	cards := []frameheader.Card{
		{Name: "CTYPE1", Value: "RA---TAN", Quoted: true}, {Name: "CTYPE2", Value: "DEC--TAN", Quoted: true},
		{Name: "CRVAL1", Value: f(g.RA0)}, {Name: "CRVAL2", Value: f(g.Dec0)},
		{Name: "CRPIX1", Value: f(g.PX0)}, {Name: "CRPIX2", Value: f(g.PY0)},
		{Name: "CD1_1", Value: f(g.CD[0][0])}, {Name: "CD1_2", Value: f(g.CD[0][1])},
		{Name: "CD2_1", Value: f(g.CD[1][0])}, {Name: "CD2_2", Value: f(g.CD[1][1])},
	}
	b, err := json.Marshal(cards)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

type field struct {
	model []float32
	stars []goals.CatalogStar
}

func makeField(rng *rand.Rand) field {
	model := make([]float32, testW*testH)
	for y := range testH {
		for x := range testW {
			dx, dy := float64(x)-240, float64(y)-180
			u := (dx*dx + dy*dy) / (150 * 150)
			v := 0.05
			if u < 1 {
				v += 0.03 * (1 - u) * (1 - u)
			}
			model[y*testW+x] = float32(v)
		}
	}
	g := testWCS()
	var stars []goals.CatalogStar
	const sig = 1.5
	for gy := 12; gy < testH/goals.NoiseBin-11; gy += 12 {
		for gx := 12; gx < testW/goals.NoiseBin-11; gx += 12 {
			cx := float64(gx*goals.NoiseBin) + 4*rng.Float64()
			cy := float64(gy*goals.NoiseBin) + 4*rng.Float64()
			amp := 0.05 + 0.3*rng.Float64()
			for y := int(cy) - 8; y <= int(cy)+8; y++ {
				for x := int(cx) - 8; x <= int(cx)+8; x++ {
					dx, dy := float64(x)-cx, float64(y)-cy
					model[y*testW+x] += float32(amp * math.Exp(-(dx*dx+dy*dy)/(2*sig*sig)))
				}
			}
			flux := amp * 2 * math.Pi * sig * sig / testExposure
			ra, dec := g.ToSky(cx+1, float64(testH)-cy)
			stars = append(stars, goals.CatalogStar{RA: ra, Dec: dec, G: testZP - 2.5*math.Log10(flux)})
		}
	}
	return field{model: model, stars: stars}
}

func encodeSub(t *testing.T, rng *rand.Rand, model []float32) []byte {
	t.Helper()
	codes := make([]uint16, len(model))
	for i, v := range model {
		codes[i] = imagedata.Quantize16(v + float32(0.01*rng.NormFloat64()))
	}
	var buf bytes.Buffer
	if err := imagedata.WriteXISF16(&buf, testW, testH, codes, nil, imagedata.Pedestal16Properties()); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type env struct {
	mu        sync.Mutex
	db, sched *gorm.DB
	objects   *memObjects
	fetches   int
	field     field
}

func newEnv(t *testing.T) *env {
	t.Helper()
	db := openDB(t)
	if err := db.AutoMigrate(&app.Frame{}, &app.Stack{}, &app.StackFrame{}, &app.TargetReference{}, &app.GoalMeasurement{}, &app.GoalMask{}, &app.GaiaField{}, &app.FrameTarget{}, &app.XPField{}, &app.SkySample{}); err != nil {
		t.Fatal(err)
	}
	sched := openDB(t)
	for _, s := range []string{
		`CREATE TABLE project ("Id" INTEGER PRIMARY KEY, guid TEXT, name TEXT, "isMosaic" INTEGER, minimumaltitude REAL)`,
		`CREATE TABLE target ("Id" INTEGER PRIMARY KEY, name TEXT, guid TEXT, projectid INTEGER, active INTEGER, ra REAL, dec REAL, rotation REAL)`,
		`INSERT INTO project VALUES (1, 'p1', 'Project', 0, 30)`,
		`INSERT INTO target ("Id", name, guid, projectid) VALUES (1, 'Synth', 'g-synth', 1), (2, 'Twin', 'g-twin-a', 1), (3, 'Twin', 'g-twin-b', 1)`,
	} {
		if err := sched.Exec(s).Error; err != nil {
			t.Fatal(err)
		}
	}
	return &env{db: db, sched: sched, objects: &memObjects{files: map[string][]byte{}}, field: makeField(rand.New(rand.NewPCG(4, 2)))}
}

func (e *env) fetcher(context.Context, float64, float64, float64) ([]goals.CatalogStar, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.fetches++
	return e.field.stars, nil
}

func (e *env) addStack(t *testing.T, object, filter string, n int) app.Stack {
	t.Helper()
	rng := rand.New(rand.NewPCG(goals.Seed(object, filter), 9))
	st := app.Stack{Object: object, Filter: filter, Width: testW, Height: testH, Subs: n,
		ExposureSeconds: float64(n) * testExposure, EffectiveSeconds: float64(n) * testExposure, UpdatedAt: time.Now()}
	if err := e.db.Create(&st).Error; err != nil {
		t.Fatal(err)
	}
	exp := testExposure
	for i := range n {
		key := fmt.Sprintf("raw/%s/%s/%03d.xisf", object, filter, i)
		f := app.Frame{Key: key, ETag: "e", Size: 1, LastModified: time.Now(), Type: "LIGHT", Object: object, Filter: filter, Exposure: &exp}
		if err := e.db.Create(&f).Error; err != nil {
			t.Fatal(err)
		}
		reg := fmt.Sprintf("registered/%s/%s/%03d.xisf", object, filter, i)
		e.objects.files[reg] = encodeSub(t, rng, e.field.model)
		sf := app.StackFrame{FrameID: f.ID, StackID: &st.ID, Status: app.StackStatusAdded, Score: 1, Weight: testExposure,
			Exposure: testExposure, RegisteredKey: &reg}
		if err := e.db.Create(&sf).Error; err != nil {
			t.Fatal(err)
		}
	}
	return st
}

func (e *env) measurement(t *testing.T, object, filter string) app.GoalMeasurement {
	t.Helper()
	var m app.GoalMeasurement
	if err := e.db.Where(columnObject+" = ? AND filter = ?", object, filter).First(&m).Error; err != nil {
		t.Fatal(err)
	}
	return m
}

func TestPassMeasuresStacks(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t)
	st := e.addStack(t, testObject, testFilter, 16)
	e.addStack(t, "Twin", "L", 5)
	tr := app.TargetReference{Object: testObject, FrameID: 1, ObjectKey: "ref"}
	w := wcsJSON(t, testWCS())
	tr.WCS = &w
	if err := e.db.Create(&tr).Error; err != nil {
		t.Fatal(err)
	}
	r := New(e.db, e.sched, e.objects, e.fetcher, Options{MaxSubs: 200, Publish: goals.PublishOff})
	if err := r.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	m := e.measurement(t, testObject, testFilter)
	if m.Error != nil || m.Subs != 16 || m.SubsTotal != 16 || m.Levels != 2 || m.TargetGUID != "g-synth" ||
		m.MethodRevision != goals.MethodRevision || !(m.SNR > 2) || !(m.NoiseNow > 0) || m.Points == "" {
		t.Fatalf("measurement %+v", m)
	}
	if math.Abs(m.EffectiveHours-16*testExposure/3600) > 1e-9 {
		t.Errorf("effective hours %v", m.EffectiveHours)
	}
	if m.ZeroPoint == nil || math.Abs(*m.ZeroPoint-testZP) > 0.05 || m.Depth == nil || m.ZeroPointStars < goals.MinZeroPointStars {
		t.Errorf("zero point %v from %d stars, depth %v", m.ZeroPoint, m.ZeroPointStars, m.Depth)
	}
	if math.Abs(m.PixelScale-testPixelScale) > 1e-6 {
		t.Errorf("pixel scale %v", m.PixelScale)
	}
	twin := e.measurement(t, "Twin", "L")
	if twin.Error == nil || *twin.Error != goals.ErrInsufficientData.Error() || twin.SNR != 0 || twin.TargetGUID != "" {
		t.Errorf("insufficient stack %+v", twin)
	}
	e.checkMask(t, m)

	gets := e.objects.gets
	if err := r.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	if e.objects.gets != gets {
		t.Fatalf("measured again without growth: %d reads", e.objects.gets-gets)
	}
	if err := e.db.Model(&st).Update("effective_seconds", st.EffectiveSeconds*1.25).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	if e.objects.gets != gets+16 || e.fetches != 1 {
		t.Fatalf("growth: %d reads, %d catalogue fetches", e.objects.gets-gets, e.fetches)
	}
	var cache app.GaiaField
	if err := e.db.Where(columnObject+" = ?", testObject).First(&cache).Error; err != nil || cache.Stars == "" {
		t.Fatalf("catalogue cache %v", err)
	}
}

func (e *env) checkMask(t *testing.T, m app.GoalMeasurement) app.GoalMask {
	t.Helper()
	var masks int64
	e.db.Model(&app.GoalMask{}).Count(&masks)
	if masks != 1 {
		t.Fatalf("%d masks, want only the measured stack's", masks)
	}
	var gm app.GoalMask
	if err := e.db.Where(columnObject+" = ? AND filter = ?", m.Object, m.Filter).First(&gm).Error; err != nil {
		t.Fatal(err)
	}
	if gm.Width != testW/goals.NoiseBin || gm.Height != testH/goals.NoiseBin || gm.FrameWidth != testW || gm.FrameHeight != testH ||
		gm.Bin != goals.NoiseBin || gm.Subs != m.Subs || !gm.MeasuredAt.Equal(m.MeasuredAt) || gm.NoiseMask != m.NoiseMask {
		t.Fatalf("mask %+v", gm)
	}
	bits, err := goals.DecompressMask(gm.Data, gm.Width, gm.Height)
	if err != nil {
		t.Fatal(err)
	}
	if len(gm.Data) >= len(bits) {
		t.Errorf("mask is %d bytes for %d pixels", len(gm.Data), len(bits))
	}
	counts := map[uint8]int{}
	for _, v := range bits {
		for _, b := range []uint8{goals.MaskCovered, goals.MaskBand, goals.MaskStar, goals.MaskSky} {
			if v&b != 0 {
				counts[b]++
			}
		}
		if v&goals.MaskBand != 0 && (v&goals.MaskStar != 0 || v&goals.MaskCovered == 0) {
			t.Fatal("band pixel is a star or uncovered")
		}
	}
	if counts[goals.MaskCovered] != gm.Covered || counts[goals.MaskBand] != gm.Band || counts[goals.MaskStar] != gm.Stars ||
		counts[goals.MaskSky] != gm.SkyPixels || gm.Band == 0 || gm.Stars == 0 || gm.SkyPixels == 0 {
		t.Fatalf("counts %v, mask %+v", counts, gm)
	}
	if got := float64(gm.Band) / float64(gm.Covered); got != m.BandFraction {
		t.Errorf("mask band fraction %v, measurement %v", got, m.BandFraction)
	}
	return gm
}

func TestMastersWithoutMaskAreMeasuredOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t)
	e.addStack(t, testObject, testFilter, 16)
	r := New(e.db, nil, e.objects, nil, Options{})
	if err := r.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	if err := e.db.Where("1 = 1").Delete(&app.GoalMask{}).Error; err != nil {
		t.Fatal(err)
	}
	gets := e.objects.gets
	if err := r.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	if e.objects.gets != gets+16 {
		t.Fatalf("remeasure without a mask: %d reads", e.objects.gets-gets)
	}
	gm := e.checkMask(t, e.measurement(t, testObject, testFilter))
	if gm.Source != goals.NoiseMaskFaint || gm.BandLo == nil || gm.BandHi == nil || !(*gm.BandLo < *gm.BandHi) {
		t.Errorf("automatic band %+v", gm)
	}
	gets = e.objects.gets
	if err := r.Pass(ctx); err != nil || e.objects.gets != gets {
		t.Fatalf("measured again with a mask: %v, %d reads", err, e.objects.gets-gets)
	}
}

func TestRegionMaskIsThePolygon(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t)
	st := e.addStack(t, testObject, testFilter, 16)
	region := []goals.Point{{X: 0.1, Y: 0.1}, {X: 0.5, Y: 0.1}, {X: 0.5, Y: 0.6}, {X: 0.1, Y: 0.6}}
	r := New(e.db, nil, e.objects, nil, Options{})
	if err := r.MeasureStack(ctx, st, region, ""); err != nil {
		t.Fatal(err)
	}
	gm := e.checkMask(t, e.measurement(t, testObject, testFilter))
	if gm.Source != goals.NoiseMaskRegion || gm.BandLo != nil || gm.BandHi != nil {
		t.Fatalf("region mask %+v", gm)
	}
	bits, err := goals.DecompressMask(gm.Data, gm.Width, gm.Height)
	if err != nil {
		t.Fatal(err)
	}
	for y := range gm.Height {
		for x := range gm.Width {
			fx := (float64(x)*goals.NoiseBin + goals.NoiseBin/2.0) / testW
			fy := (float64(y)*goals.NoiseBin + goals.NoiseBin/2.0) / testH
			in := fx > 0.1 && fx < 0.5 && fy > 0.1 && fy < 0.6
			if v := bits[y*gm.Width+x]; v&goals.MaskBand != 0 && !in {
				t.Fatalf("band pixel %d,%d is outside the region", x, y)
			} else if in && v&goals.MaskCovered != 0 && v&goals.MaskStar == 0 && v&goals.MaskBand == 0 {
				t.Fatalf("clean region pixel %d,%d is not in the band", x, y)
			}
		}
	}
}

func TestMeasureSubset(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t)
	st := e.addStack(t, testObject, testFilter, 20)
	r := New(e.db, nil, e.objects, nil, Options{MaxSubs: 10})
	if err := r.MeasureStack(ctx, st, nil, ""); err != nil {
		t.Fatal(err)
	}
	m := e.measurement(t, testObject, testFilter)
	if m.Subs != 10 || m.SubsTotal != 20 || e.objects.gets != 10 || m.Depth != nil || m.ZeroPoint != nil {
		t.Fatalf("subset measurement %+v, %d reads", m, e.objects.gets)
	}
}

func TestMeasureFailureIsRetriedLater(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t)
	e.addStack(t, testObject, testFilter, 8)
	for k := range e.objects.files {
		if strings.HasSuffix(k, "003.xisf") {
			delete(e.objects.files, k)
		}
	}
	r := New(e.db, nil, e.objects, nil, Options{})
	if err := r.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	var n int64
	e.db.Model(&app.GoalMeasurement{}).Count(&n)
	if n != 0 || len(r.failed) != 1 {
		t.Fatalf("rows %d, failed %v", n, r.failed)
	}
	gets := e.objects.gets
	if err := r.Pass(ctx); err != nil || e.objects.gets != gets {
		t.Fatalf("retried before the backoff: %v", err)
	}
}

func TestNeedsMeasurement(t *testing.T) {
	t.Parallel()
	s := app.Stack{EffectiveSeconds: 3600}
	m := &app.GoalMeasurement{MethodRevision: goals.MethodRevision, EffectiveHours: 1}
	cases := []struct {
		name string
		s    app.Stack
		m    *app.GoalMeasurement
		hash string
		mask bool
		want bool
	}{
		{"none", s, nil, "", false, true},
		{"current", s, m, "", true, false},
		{"grew 10%", app.Stack{EffectiveSeconds: 3960}, m, "", true, false},
		{"grew 20%", app.Stack{EffectiveSeconds: 4320}, m, "", true, true},
		{"region", s, m, "abc", true, true},
		{"revision", s, &app.GoalMeasurement{MethodRevision: goals.MethodRevision - 1, EffectiveHours: 1}, "", true, true},
		{"no mask", s, m, "", false, true},
		{"no mask, insufficient data", s, &app.GoalMeasurement{MethodRevision: goals.MethodRevision, EffectiveHours: 1, Error: new(string)}, "", false, false},
	}
	for _, c := range cases {
		if got := needsMeasurement(c.s, c.m, c.hash, c.mask); got != c.want {
			t.Errorf("%s: %v", c.name, got)
		}
	}
}

func TestDueMeasuresNewStacksFirst(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t)
	old := e.addStack(t, "Old", testFilter, 4)
	unmasked := e.addStack(t, "Unmasked", testFilter, 4)
	sky := e.addStack(t, "Sky", "Red", 4)
	fresh := e.addStack(t, "Fresh", testFilter, 4)
	fresher := e.addStack(t, "Fresher", "Red", 4)
	long := time.Now().Add(-48 * time.Hour)
	zp := 6.0
	for _, m := range []app.GoalMeasurement{
		{Object: old.Object, Filter: testFilter, MethodRevision: goals.MethodRevision - 1, EffectiveHours: old.EffectiveSeconds / 3600, MeasuredAt: time.Now(), SkyRev: SkyRevision},
		{Object: unmasked.Object, Filter: testFilter, MethodRevision: goals.MethodRevision, EffectiveHours: unmasked.EffectiveSeconds / 3600, MeasuredAt: long, SkyRev: SkyRevision},
		{Object: sky.Object, Filter: "Red", MethodRevision: goals.MethodRevision, EffectiveHours: sky.EffectiveSeconds / 3600, MeasuredAt: long.Add(-time.Hour),
			ZeroPoint: &zp, DepthSystem: goals.SystemGaiaG},
	} {
		if err := e.db.Create(&m).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := e.db.Create(&app.GoalMask{Object: sky.Object, Filter: "Red", Data: []byte{0}}).Error; err != nil {
		t.Fatal(err)
	}
	r := New(e.db, nil, e.objects, nil, Options{})
	todo, n, err := r.due(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(todo))
	for _, s := range todo {
		got = append(got, s.Object)
	}
	want := []string{fresh.Object, fresher.Object, old.Object, sky.Object, unmasked.Object}
	if n != 2 || strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("order %v, want %v, never measured %d", got, want, n)
	}
}

func TestPassWaitsWhileStacking(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.addStack(t, testObject, testFilter, 8)
	e.addStack(t, "Second", testFilter, 8)
	var stacking sync.Mutex
	busy := true
	isBusy := func() bool {
		stacking.Lock()
		defer stacking.Unlock()
		return busy
	}
	r := New(e.db, nil, e.objects, nil, Options{Workers: 2, Busy: isBusy})
	r.poll = 5 * time.Millisecond
	done := make(chan error, 1)
	go func() { done <- r.Pass(context.Background()) }()
	time.Sleep(50 * time.Millisecond)
	e.objects.mu.Lock()
	gets := e.objects.gets
	e.objects.mu.Unlock()
	if gets != 0 || r.Live().State != goals.BackfillPaused || r.Live().Queued != 2 || r.Live().QueuedNew != 2 {
		t.Fatalf("measured while stacking: %d reads, %+v", gets, r.Live())
	}
	stacking.Lock()
	busy = false
	stacking.Unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("pass did not resume after stacking")
	}
	live := r.Live()
	if live.State != goals.BackfillIdle || live.DoneInPass != 2 || live.LastHour != 2 || live.PerHour <= 0 {
		t.Fatalf("live %+v", live)
	}
	var n int64
	e.db.Model(&app.GoalMeasurement{}).Count(&n)
	b, err := goals.CountBackfill(context.Background(), e.db, live)
	if err != nil || n != 2 || b.Total != 2 || b.Measured != 2 || b.Current != 2 {
		t.Fatalf("backfill %+v, rows %d, %v", b, n, err)
	}
}

func TestDrainStopsRun(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	r := New(e.db, nil, e.objects, nil, Options{Interval: time.Hour})
	done := make(chan struct{})
	go func() {
		r.Run(context.Background())
		close(done)
	}()
	r.Drain()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not stop on drain")
	}
}

func TestParseVizierTSV(t *testing.T) {
	t.Parallel()
	const body = "#\n#Title: Gaia DR3\n\nRA_ICRS\tDE_ICRS\tGmag\ndeg\t(deg)\tmag\n" +
		"---------------\t---------------\t---------\n010.60874916685\t+41.09671184994\t 9.483673\n010.7\t-01.5\t\n"
	stars, err := ParseVizierTSV(strings.NewReader(body))
	if err != nil || len(stars) != 1 || stars[0].RA != 10.60874916685 || stars[0].Dec != 41.09671184994 || stars[0].G != 9.483673 {
		t.Fatalf("%+v %v", stars, err)
	}
	if _, err := ParseVizierTSV(strings.NewReader("#nothing\n")); err == nil {
		t.Fatal("parsed an empty response")
	}
}

func TestCatalogCache(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t)
	calls := 0
	fetch := func(context.Context, float64, float64, float64) ([]goals.CatalogStar, error) {
		calls++
		return []goals.CatalogStar{{RA: 1, Dec: 2, G: 10}}, nil
	}
	for range 2 {
		if s, err := catalogStars(ctx, e.db, fetch, "M42", 83.8, -5.4, 1); err != nil || len(s) != 1 {
			t.Fatalf("%v %v", s, err)
		}
	}
	if _, err := catalogStars(ctx, e.db, fetch, "M42", 85, -5.4, 1); err != nil || calls != 2 {
		t.Fatalf("moved field: %d calls, %v", calls, err)
	}
	failing := func(context.Context, float64, float64, float64) ([]goals.CatalogStar, error) {
		return nil, errors.New("offline")
	}
	if _, err := catalogStars(ctx, e.db, failing, "M1", 83, 22, 1); err == nil {
		t.Fatal("no error from a failing fetch")
	}
}

func flatXP(s goals.CatalogStar) goals.XPStar {
	fnu := math.Pow(10, -(s.G+56.10)/2.5)
	at := func(nm float64) float64 {
		l := nm * 1e-9
		return fnu * 299792458.0 / (l * l) / 1e9
	}
	return goals.XPStar{RA: s.RA, Dec: s.Dec, FB: at(438), FV: at(545), FR: at(641), FI: at(798)}
}

func (e *env) xpFetcher(context.Context, float64, float64, float64) ([]goals.XPStar, error) {
	out := make([]goals.XPStar, 0, len(e.field.stars))
	for _, s := range e.field.stars {
		out = append(out, flatXP(s))
	}
	return out, nil
}

func addReference(t *testing.T, e *env) {
	t.Helper()
	tr := app.TargetReference{Object: testObject, FrameID: 1, ObjectKey: "ref"}
	w := wcsJSON(t, testWCS())
	tr.WCS = &w
	if err := e.db.Create(&tr).Error; err != nil {
		t.Fatal(err)
	}
}

func TestNarrowbandDepthFallsBackToGaiaG(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	st := e.addStack(t, testObject, testFilter, 8)
	addReference(t, e)
	r := New(e.db, e.sched, e.objects, e.fetcher, Options{MaxSubs: 200, Publish: goals.PublishOff})
	if err := r.MeasureStack(context.Background(), st, nil, ""); err != nil {
		t.Fatal(err)
	}
	m := e.measurement(t, testObject, testFilter)
	if m.DepthSystem != goals.SystemGaiaG || !m.DepthApprox || m.DepthBand != goals.BandGaiaG {
		t.Errorf("narrowband on Gaia G without XP: %q approx %v", m.DepthSystem, m.DepthApprox)
	}
}

func TestNarrowbandDepthUsesGaiaXP(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	st := e.addStack(t, testObject, testFilter, 8)
	addReference(t, e)
	r := New(e.db, e.sched, e.objects, e.fetcher, Options{MaxSubs: 200, Publish: goals.PublishOff})
	r.XP = e.xpFetcher
	if err := r.MeasureStack(context.Background(), st, nil, ""); err != nil {
		t.Fatal(err)
	}
	m := e.measurement(t, testObject, testFilter)
	if m.ZeroPoint == nil || math.Abs(*m.ZeroPoint-testZP) > 0.05 || m.DepthSystem != goals.SystemXPAB || m.DepthApprox || !strings.Contains(m.DepthBand, "656") {
		t.Fatalf("zero point %v system %q band %q approx %v", m.ZeroPoint, m.DepthSystem, m.DepthBand, m.DepthApprox)
	}
	var cached app.XPField
	if err := e.db.Where(columnObject+" = ?", testObject).First(&cached).Error; err != nil || cached.Stars == "" {
		t.Fatalf("xp cache %v", err)
	}
	if e.fetches != 0 {
		t.Errorf("fetched Gaia G for a narrowband zero point")
	}
	p := goals.Evaluate(m, goals.Goal{Kind: goals.KindDepth, Depth: 20})
	if p.DepthSystem != goals.SystemXPAB || p.DepthApprox || p.DepthBand != m.DepthBand {
		t.Errorf("progress %+v", p)
	}
}

func TestBroadbandSubsGiveSkyBrightness(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	st := e.addStack(t, testObject, "Red", 8)
	addReference(t, e)
	night := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	if err := e.db.Model(&app.Frame{}).Where("filter = ?", "Red").Updates(map[string]any{"night": night, "date_obs": night.Add(30 * time.Hour)}).Error; err != nil {
		t.Fatal(err)
	}
	r := New(e.db, e.sched, e.objects, e.fetcher, Options{MaxSubs: 200, Publish: goals.PublishOff})
	if err := r.MeasureStack(context.Background(), st, nil, ""); err != nil {
		t.Fatal(err)
	}
	m := e.measurement(t, testObject, "Red")
	if m.ZeroPoint == nil || m.DepthSystem != goals.SystemGaiaG || m.DepthApprox || m.SkyRev != SkyRevision {
		t.Fatalf("luminance zero point %+v", m)
	}
	var samples []app.SkySample
	if err := e.db.Find(&samples).Error; err != nil {
		t.Fatal(err)
	}
	if len(samples) != 8 {
		t.Fatalf("%d sky samples", len(samples))
	}
	for _, s := range samples {
		want := *m.ZeroPoint - 2.5*math.Log10(s.SkyRate/(m.PixelScale*m.PixelScale))
		if s.Night == nil || !s.Night.Equal(night) || math.Abs(s.SkyMag-want) > 1e-9 || !(s.SkyRate > 0) {
			t.Errorf("sample %+v", s)
		}
	}
	if err := r.MeasureStack(context.Background(), st, nil, ""); err != nil {
		t.Fatal(err)
	}
	var n int64
	if err := e.db.Model(&app.SkySample{}).Count(&n).Error; err != nil || n != 8 {
		t.Errorf("remeasuring duplicated samples: %d %v", n, err)
	}
}

func TestParseXPTSV(t *testing.T) {
	t.Parallel()
	body := "#comment\nRA_ICRS\tDE_ICRS\tFB\tFV\tFR\tFI\ndeg\t\t\t\t\t\n--------\t--\t--\t--\t--\t--\n" +
		" 83.81683953130\t -5.43085496771\t \t \t 2.32219e-17\t 2.66724e-17\n"
	stars, err := ParseXPTSV(strings.NewReader(body))
	if err != nil || len(stars) != 1 || stars[0].FR != 2.32219e-17 || !math.IsNaN(stars[0].FB) {
		t.Fatalf("%+v %v", stars, err)
	}
	enc, err := encodeXP(stars)
	if err != nil {
		t.Fatal(err)
	}
	back, err := decodeXP(enc)
	if err != nil || len(back) != 1 || back[0].FI != stars[0].FI || !math.IsNaN(back[0].FV) {
		t.Fatalf("round trip %+v %v", back, err)
	}
	if _, err := ParseXPTSV(strings.NewReader("RA_ICRS\tDE_ICRS\n")); err == nil {
		t.Error("missing flux columns accepted")
	}
}

func TestOldBroadbandMeasurementsAreRemeasuredForSky(t *testing.T) {
	t.Parallel()
	zp := 6.0
	st := app.Stack{Filter: "Green", EffectiveSeconds: 3600}
	m := &app.GoalMeasurement{MethodRevision: goals.MethodRevision, EffectiveHours: 1, ZeroPoint: &zp, DepthSystem: goals.SystemGaiaG}
	if !needsMeasurement(st, m, "", true) {
		t.Error("a broadband master without sky samples is not remeasured")
	}
	m.SkyRev = SkyRevision
	if needsMeasurement(st, m, "", true) {
		t.Error("remeasured after its sky samples")
	}
	m.SkyRev = 0
	st.Filter = testFilter
	if needsMeasurement(st, m, "", true) {
		t.Error("a narrowband master was remeasured for sky")
	}
	st.Filter, m.ZeroPoint = "Green", nil
	if needsMeasurement(st, m, "", true) {
		t.Error("remeasured without a zero point")
	}
}

func TestPassRefitsStoredMeasurementsWithoutANoiseFloor(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t)
	pts := `[{"n":4,"t":0.2783315944633578,"sigma":7.73107505937816e-8},{"n":4,"t":0.2795662755549598,"sigma":7.753653950533588e-8},{"n":4,"t":0.2862299851603613,"sigma":7.755059968964953e-8}]`
	rows := []app.GoalMeasurement{
		{Object: "Pelican", Filter: "Blue", Subs: 9, Levels: 1, EffectiveHours: 0.6364448280832384, Signal: 4.150851964368485e-7,
			NoiseA: 2.591412588243134e-9, NoiseB: 7.731075059249109e-8, NoiseNow: 7.737896104594509e-8, SNR: 5.36, GainPerHourPct: 0.05, Points: pts},
		{Object: "Deep", Filter: "S-II", Subs: 40, Levels: 3, EffectiveHours: 3, Signal: 1, NoiseA: 1, NoiseB: 0.5, NoiseNow: 0.76, SNR: 1.3, GainPerHourPct: 1, Points: pts},
	}
	if err := e.db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	r := New(e.db, e.sched, e.objects, e.fetcher, Options{Publish: goals.PublishOff})
	if err := r.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	m := e.measurement(t, "Pelican", "Blue")
	if m.NoiseB != 0 || m.GainPerHourPct < 30 || math.Abs(m.SNR-8.06) > 0.05 || !m.LowConfidence ||
		m.LowReason != "noise floor not measurable from 1 draw levels" || goals.Evaluate(m, goals.DefaultGoal("Blue")).Done {
		t.Fatalf("refit %+v", m)
	}
	if deep := e.measurement(t, "Deep", "S-II"); deep.NoiseB != 0.5 || deep.GainPerHourPct != 1 || deep.LowConfidence {
		t.Fatalf("three-level row changed %+v", deep)
	}
}

func (e *env) addGoalTables(t *testing.T) {
	t.Helper()
	for _, s := range []string{
		`CREATE TABLE ts_goal (target_guid TEXT NOT NULL, filter TEXT NOT NULL, kind INTEGER NOT NULL DEFAULT 0, snr_goal REAL,
			depth_goal REAL, plateau_stop INTEGER NOT NULL DEFAULT 1, region TEXT, updated_at TEXT, PRIMARY KEY (target_guid, filter))`,
		`CREATE TABLE ts_goal_progress (target_guid TEXT NOT NULL, filter TEXT NOT NULL, kind INTEGER, goal_value REAL, achieved_value REAL,
			progress REAL, snr REAL, depth REAL, effective_hours REAL, hours_needed REAL, gain_per_hour_pct REAL, plateau INTEGER,
			low_confidence INTEGER, done INTEGER, measured_at TIMESTAMP, state TEXT, reason TEXT, stack_subs INTEGER, min_subs INTEGER,
			sub_limit INTEGER, PRIMARY KEY (target_guid, filter))`,
	} {
		if err := e.sched.Exec(s).Error; err != nil {
			t.Fatal(err)
		}
	}
}

type readinessOut struct {
	TargetGUID string
	Filter     string
	State      *string
	StackSubs  *int
	MinSubs    *int
	Progress   *float64
}

func TestRunPublishesWhileMeasurementWaits(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.addGoalTables(t)
	e.addStack(t, testObject, "O-III", 3)
	if err := e.db.Create(&app.GoalMeasurement{Object: testObject, Filter: "Luminance", MethodRevision: goals.MethodRevision, Subs: 40,
		SNR: 12, EffectiveHours: 3, GainPerHourPct: 9, MeasuredAt: time.Now().UTC()}).Error; err != nil {
		t.Fatal(err)
	}
	r := New(e.db, e.sched, e.objects, nil, Options{Interval: time.Hour, Publish: goals.PublishOn, Busy: func() bool { return true }})
	r.poll, r.pubPoll, r.pubEvery = time.Millisecond, 5*time.Millisecond, 5*time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Run(ctx)

	deadline := time.Now().Add(5 * time.Second)
	for r.Live().State != goals.BackfillPaused {
		if time.Now().After(deadline) {
			t.Fatalf("pass never paused: %+v", r.Live())
		}
		time.Sleep(time.Millisecond)
	}
	if err := e.sched.Exec(`INSERT INTO ts_goal VALUES ('g-synth', 'Luminance', 0, 30, NULL, 1, NULL, '2026-10-10T06:06:24Z'),
		('g-synth', 'O-III', 0, 10, NULL, 1, NULL, '2026-10-10T06:06:24Z')`).Error; err != nil {
		t.Fatal(err)
	}
	var rows []readinessOut
	for len(rows) < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("no goal progress published while measurement waited: %+v", rows)
		}
		time.Sleep(5 * time.Millisecond)
		rows = nil
		if err := e.sched.Table(goals.ProgressTable).Order("filter").Scan(&rows).Error; err != nil {
			t.Fatal(err)
		}
	}
	if r.Live().State != goals.BackfillPaused || r.Live().DoneInPass != 0 {
		t.Fatalf("measured while stacking: %+v", r.Live())
	}
	l, o3 := rows[0], rows[1]
	if l.TargetGUID != "g-synth" || l.Filter != "Luminance" || l.State == nil || *l.State != goals.StateMeasured || l.Progress == nil || *l.Progress <= 0 {
		t.Errorf("Luminance row %+v", l)
	}
	if o3.Filter != "O-III" || o3.State == nil || *o3.State != goals.StateCollecting || o3.StackSubs == nil || *o3.StackSubs != 3 ||
		o3.MinSubs == nil || *o3.MinSubs != goals.MinSubs {
		t.Errorf("O-III row %+v", o3)
	}
}
