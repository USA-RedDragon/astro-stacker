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
	testW, testH = 480, 360
	testExposure = 300.0
	testObject   = "Synth"
	testFilter   = "H-a"
	testZP       = 6.0
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
	s := goals.DefaultPixelScale / 3600
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
	db, sched *gorm.DB
	objects   *memObjects
	fetches   int
	field     field
}

func newEnv(t *testing.T) *env {
	t.Helper()
	db := openDB(t)
	if err := db.AutoMigrate(&app.Frame{}, &app.Stack{}, &app.StackFrame{}, &app.TargetReference{}, &app.GoalMeasurement{}, &app.GaiaField{}, &app.FrameTarget{}); err != nil {
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
	if math.Abs(m.PixelScale-goals.DefaultPixelScale) > 1e-6 {
		t.Errorf("pixel scale %v", m.PixelScale)
	}
	twin := e.measurement(t, "Twin", "L")
	if twin.Error == nil || *twin.Error != goals.ErrInsufficientData.Error() || twin.SNR != 0 || twin.TargetGUID != "" {
		t.Errorf("insufficient stack %+v", twin)
	}

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
		want bool
	}{
		{"none", s, nil, "", true},
		{"current", s, m, "", false},
		{"grew 10%", app.Stack{EffectiveSeconds: 3960}, m, "", false},
		{"grew 20%", app.Stack{EffectiveSeconds: 4320}, m, "", true},
		{"region", s, m, "abc", true},
		{"revision", s, &app.GoalMeasurement{MethodRevision: goals.MethodRevision - 1, EffectiveHours: 1}, "", true},
	}
	for _, c := range cases {
		if got := needsMeasurement(c.s, c.m, c.hash); got != c.want {
			t.Errorf("%s: %v", c.name, got)
		}
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
