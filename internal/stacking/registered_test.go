package stacking

import (
	"bytes"
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/frameheader"
	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
	"github.com/USA-RedDragon/astro-stacker/internal/siril"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type fakeObject struct {
	b    []byte
	time time.Time
}

type fakeStore struct {
	mu      sync.Mutex
	objs    map[string]fakeObject
	now     time.Time
	removed []string
	failPut bool
}

func newFakeStore(now time.Time) *fakeStore {
	return &fakeStore{objs: map[string]fakeObject{}, now: now}
}

func (f *fakeStore) set(key string, b []byte, at time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objs[key] = fakeObject{b: b, time: at}
}

func (f *fakeStore) has(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.objs[key]
	return ok
}

func (f *fakeStore) list(_ context.Context, prefix string) ([]registeredObject, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []registeredObject
	for k, o := range f.objs {
		if strings.HasPrefix(k, prefix) {
			out = append(out, registeredObject{Key: k, Size: int64(len(o.b)), LastModified: o.time})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (f *fakeStore) stat(_ context.Context, key string) (registeredObject, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.objs[key]
	if !ok {
		return registeredObject{}, fmt.Errorf("no %s", key)
	}
	return registeredObject{Key: key, Size: int64(len(o.b)), LastModified: o.time}, nil
}

func (f *fakeStore) get(_ context.Context, key string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.objs[key]
	if !ok {
		return nil, fmt.Errorf("no %s", key)
	}
	return o.b, nil
}

func (f *fakeStore) put(_ context.Context, key string, b []byte, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failPut {
		return fmt.Errorf("put refused")
	}
	f.objs[key] = fakeObject{b: b, time: f.now}
	return nil
}

func (f *fakeStore) remove(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.objs, key)
	f.removed = append(f.removed, key)
	return nil
}

func registeredFITS(t *testing.T, w, h int, seed uint64) ([]byte, []float32) {
	t.Helper()
	r := rand.New(rand.NewPCG(seed, seed))
	data := make([]float32, w*h)
	for i := range data {
		switch {
		case i%97 == 0:
			data[i] = 0
		case i%89 == 0:
			data[i] = float32(r.NormFloat64() * 1e-6)
		case i%83 == 0:
			data[i] = 1
		default:
			data[i] = float32(0.01 + 0.002*r.NormFloat64())
		}
	}
	var buf bytes.Buffer
	cards := []imagedata.Card{imagedata.StringCard("FILTER", "Ha", "filter"), imagedata.FloatCard("EXPTIME", 300, "")}
	if err := imagedata.WriteFITS(&buf, w, h, 1, data, cards); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), data
}

func TestRegisteredEncodingRoundTrip(t *testing.T) {
	t.Parallel()
	const w, h = 120, 80
	fit, data := registeredFITS(t, w, h, 1)
	enc, orig, err := encodeRegistered(fit)
	if err != nil {
		t.Fatal(err)
	}
	if len(enc) >= len(fit)/2 {
		t.Errorf("encoded %d bytes from %d", len(enc), len(fit))
	}
	dir := t.TempDir()
	oldFile, newFile := filepath.Join(dir, "old.fit"), filepath.Join(dir, "new.xisf")
	if err := os.WriteFile(oldFile, fit, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := encodeRegisteredFile(oldFile, newFile); err != nil {
		t.Fatal(err)
	}
	oldSub, ow, oh, err := readSub(oldFile)
	if err != nil {
		t.Fatal(err)
	}
	newSub, nw, nh, err := readSub(newFile)
	if err != nil {
		t.Fatal(err)
	}
	if ow != w || oh != h || nw != w || nh != h {
		t.Fatalf("sizes %dx%d and %dx%d", ow, oh, nw, nh)
	}
	for i, v := range data {
		if oldSub[i] != v {
			t.Fatalf("old format pixel %d = %v, want %v", i, oldSub[i], v)
		}
		if newSub[i] != dequantize(quantize(v)) {
			t.Fatalf("new format pixel %d = %v, want %v", i, newSub[i], dequantize(quantize(v)))
		}
		if v != 0 && newSub[i] == 0 {
			t.Fatalf("pixel %d = %v decoded as empty", i, v)
		}
	}
	dec, err := imagedata.Decode(enc)
	if err != nil {
		t.Fatal(err)
	}
	s, err := compareEncoded(orig, dec)
	if err != nil {
		t.Fatal(err)
	}
	if s.empty == 0 || s.clipped == 0 {
		t.Errorf("stats %+v: want empties and clipped pixels", s)
	}
	cards, err := frameheader.ParseCards(enc)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(cards))
	for _, c := range cards {
		names = append(names, c.Name)
	}
	if strings.Join(names, ",") != "FILTER,EXPTIME" {
		t.Errorf("keywords %v, want FILTER and EXPTIME only", names)
	}
}

func TestCompareEncodedCatchesDamage(t *testing.T) {
	t.Parallel()
	fit, _ := registeredFITS(t, 40, 30, 2)
	enc, orig, err := encodeRegistered(fit)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := imagedata.Decode(enc)
	if err != nil {
		t.Fatal(err)
	}
	dec.Data[5] += 0.01
	if _, err := compareEncoded(orig, dec); err == nil {
		t.Error("a moved pixel passed")
	}
	dec.Data[5] = 0
	if _, err := compareEncoded(orig, dec); err == nil {
		t.Error("a pixel turned empty passed")
	}
}

func TestGCDecide(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	grace := 24 * time.Hour
	old, recent := now.Add(-48*time.Hour), now.Add(-time.Hour)
	xisf, fit, xisfB := "registered/M31/Ha/a.xisf", "registered/M31/Ha/a.fit", "registered/M31/Ha/b.xisf"
	listed := map[string]registeredObject{
		xisf:  {Key: xisf, LastModified: old},
		xisfB: {Key: xisfB, LastModified: recent},
	}
	keyX, keyB, keyOther := xisf, xisfB, "registered/M33/Ha/a.xisf"
	longAgo, lately := now.Add(-30*time.Hour), now.Add(-2*time.Hour)
	cases := []struct {
		name       string
		obj        registeredObject
		rows       []subRow
		referenced bool
		seen       *time.Time
		want       gcVerdict
	}{
		{"referenced", registeredObject{Key: xisf, LastModified: old}, []subRow{{Status: app.StackStatusAdded, RegisteredKey: &keyX}}, true, nil, gcKeep},
		{"just uploaded", registeredObject{Key: fit, LastModified: recent}, nil, false, nil, gcYoung},
		{"first seen unreferenced", registeredObject{Key: fit, LastModified: old}, []subRow{{Status: app.StackStatusMoon}}, false, nil, gcNew},
		{"unreferenced within grace", registeredObject{Key: fit, LastModified: old}, []subRow{{Status: app.StackStatusLowScore}}, false, &lately, gcPending},
		{"unreferenced past grace", registeredObject{Key: fit, LastModified: old}, []subRow{{Status: app.StackStatusMoon}}, false, &longAgo, gcDelete},
		{"no row past grace", registeredObject{Key: fit, LastModified: old}, nil, false, &longAgo, gcDelete},
		{"converted long ago", registeredObject{Key: fit, LastModified: old}, []subRow{{Status: app.StackStatusAdded, RegisteredKey: &keyX}}, false, nil, gcDelete},
		{"converted within grace", registeredObject{Key: "registered/M31/Ha/b.fit", LastModified: old}, []subRow{{Status: app.StackStatusAdded, RegisteredKey: &keyB}}, false, nil, gcYoung},
		{"name shared with an added sub elsewhere", registeredObject{Key: fit, LastModified: old}, []subRow{{Status: app.StackStatusAdded, RegisteredKey: &keyOther}}, false, &longAgo, gcAmbiguous},
		{"added without key", registeredObject{Key: fit, LastModified: old}, []subRow{{Status: app.StackStatusAdded}}, false, &longAgo, gcAmbiguous},
	}
	for _, c := range cases {
		ref := map[string]bool{}
		if c.referenced {
			ref[c.obj.Key] = true
		}
		if got := gcDecide(c.obj, c.rows, ref, listed, c.seen, now, grace); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func registeredDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "db.sqlite")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&app.Frame{}, &app.Stack{}, &app.StackFrame{}, &app.RegisteredOrphan{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func addRegisteredRow(t *testing.T, db *gorm.DB, stackID int, frameKey, status string, key *string, at time.Time) app.StackFrame {
	t.Helper()
	f := app.Frame{Key: frameKey, ETag: frameKey, Type: "LIGHT", Object: objectM31, Filter: "Ha"}
	if err := db.Create(&f).Error; err != nil {
		t.Fatal(err)
	}
	sf := app.StackFrame{FrameID: f.ID, StackID: &stackID, Status: status, RegisteredKey: key, ProcessedAt: at}
	if err := db.Create(&sf).Error; err != nil {
		t.Fatal(err)
	}
	return sf
}

func TestCollectRegistered(t *testing.T) {
	t.Parallel()
	db := registeredDB(t)
	stack := app.Stack{Object: objectM31, Filter: "Ha"}
	if err := db.Create(&stack).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	old, recent := now.Add(-72*time.Hour), now.Add(-time.Hour)
	store := newFakeStore(now)
	kept := "registered/M31/Ha/kept.xisf"
	addRegisteredRow(t, db, stack.ID, "lights/M31/kept.fits", app.StackStatusAdded, &kept, old)
	addRegisteredRow(t, db, stack.ID, "lights/M31/moon.fits", app.StackStatusMoon, nil, old)
	store.set(kept, make([]byte, 10), old)
	store.set("registered/M31/Ha/moon.fit", make([]byte, 10), old)
	store.set("registered/M31/Ha/gone.fit", make([]byte, 10), old)
	store.set("registered/M31/Ha/new.xisf", make([]byte, 10), recent)
	p := NewPipeline(nil, "", "", db, nil, siril.Runner{}, "", DefaultPipelineOptions())
	p.opts.RegisteredDeletePause = 0
	ctx := context.Background()

	res, err := p.collectRegistered(ctx, store, now)
	if err != nil {
		t.Fatal(err)
	}
	if res.deleted != 2 || res.deletedBytes != 20 || res.referenced != 1 || res.young != 1 {
		t.Errorf("first sweep %+v: the orphans from before tracking go at once", res)
	}

	leaving := "registered/M31/Ha/leaving.xisf"
	sf := addRegisteredRow(t, db, stack.ID, "lights/M31/leaving.fits", app.StackStatusAdded, &leaving, old)
	store.set(leaving, make([]byte, 10), old)
	if err := db.Model(&sf).Updates(map[string]any{"status": app.StackStatusLowScore, "registered_key": nil}).Error; err != nil {
		t.Fatal(err)
	}
	if res, err = p.collectRegistered(ctx, store, now); err != nil || res.deleted != 0 || res.pending != 1 {
		t.Errorf("a sub that just left: %+v %v", res, err)
	}
	if res, err = p.collectRegistered(ctx, store, now.Add(time.Hour)); err != nil || res.deleted != 0 || res.pending != 1 {
		t.Errorf("an hour later: %+v %v", res, err)
	}
	p.hold(objectM31)
	if res, err = p.collectRegistered(ctx, store, now.Add(25*time.Hour)); err != nil || res.deleted != 0 || res.busy != 1 {
		t.Errorf("target busy: %+v %v", res, err)
	}
	p.release(objectM31)
	if res, err = p.collectRegistered(ctx, store, now.Add(26*time.Hour)); err != nil || res.deleted != 1 {
		t.Errorf("past the grace: %+v %v", res, err)
	}
	for k, want := range map[string]bool{kept: true, "registered/M31/Ha/moon.fit": false, "registered/M31/Ha/gone.fit": false,
		"registered/M31/Ha/new.xisf": true, leaving: false} {
		if store.has(k) != want {
			t.Errorf("%s kept = %v, want %v", k, !want, want)
		}
	}
	var left int64
	db.Model(&app.RegisteredOrphan{}).Where("key <> ''").Count(&left)
	if left != 1 {
		t.Errorf("%d sightings left, want new.xisf's", left)
	}
}

func TestConvertRegistered(t *testing.T) {
	t.Parallel()
	db := registeredDB(t)
	stack := app.Stack{Object: objectM31, Filter: "Ha"}
	if err := db.Create(&stack).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	store := newFakeStore(now)
	p := NewPipeline(nil, "", "", db, nil, siril.Runner{}, "", DefaultPipelineOptions())
	ctx := context.Background()
	rows := make([]app.StackFrame, 0, 4)
	for i := range 4 {
		key := fmt.Sprintf("registered/M31/Ha/s%d.fit", i)
		fit, _ := registeredFITS(t, 60, 40, uint64(i+10))
		store.set(key, fit, now.Add(-time.Hour))
		rows = append(rows, addRegisteredRow(t, db, stack.ID, fmt.Sprintf("lights/M31/s%d.fits", i), app.StackStatusAdded, &key, now))
	}
	row := func(i int) backfillRow {
		return backfillRow{ID: rows[i].ID, RegisteredKey: *rows[i].RegisteredKey, Object: objectM31}
	}
	keyOf := func(i int) string {
		var sf app.StackFrame
		if err := db.First(&sf, rows[i].ID).Error; err != nil {
			t.Fatal(err)
		}
		if sf.RegisteredKey == nil {
			return ""
		}
		return *sf.RegisteredKey
	}

	out, _, written, err := p.convertRegistered(ctx, store, row(0))
	if err != nil || out != backfillConverted {
		t.Fatalf("convert: %v %v", out, err)
	}
	if keyOf(0) != "registered/M31/Ha/s0.xisf" || store.has("registered/M31/Ha/s0.fit") || !store.has("registered/M31/Ha/s0.xisf") || written == 0 {
		t.Errorf("after converting: key %s, objects %v", keyOf(0), store.objs)
	}

	p.hold(objectM31)
	out, _, _, err = p.convertRegistered(ctx, store, row(1))
	p.release(objectM31)
	if err != nil || out != backfillBusy || keyOf(1) != "registered/M31/Ha/s1.fit" || store.has("registered/M31/Ha/s1.xisf") {
		t.Errorf("busy master: %v %v, key %s", out, err, keyOf(1))
	}

	if err := db.Model(&app.StackFrame{}).Where("id = ?", rows[2].ID).Updates(map[string]any{"status": app.StackStatusMoon, "registered_key": nil}).Error; err != nil {
		t.Fatal(err)
	}
	out, _, _, err = p.convertRegistered(ctx, store, row(2))
	if err != nil || out != backfillMoved || store.has("registered/M31/Ha/s2.xisf") || !store.has("registered/M31/Ha/s2.fit") {
		t.Errorf("sub left the master: %v %v", out, err)
	}

	store.failPut = true
	out, _, _, err = p.convertRegistered(ctx, store, row(3))
	store.failPut = false
	if err == nil || keyOf(3) != "registered/M31/Ha/s3.fit" || !store.has("registered/M31/Ha/s3.fit") {
		t.Errorf("failed upload: %v %v, key %s", out, err, keyOf(3))
	}

	again, _, _, err := p.convertRegistered(ctx, store, row(3))
	if err != nil || again != backfillConverted || keyOf(3) != "registered/M31/Ha/s3.xisf" {
		t.Errorf("retry: %v %v", again, err)
	}
}

func TestRegisteredBackfillRunsToTheEnd(t *testing.T) {
	t.Parallel()
	db := registeredDB(t)
	stack := app.Stack{Object: objectM31, Filter: "Ha"}
	if err := db.Create(&stack).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	store := newFakeStore(now)
	for i := range 5 {
		key := fmt.Sprintf("registered/M31/Ha/s%d.fit", i)
		fit, _ := registeredFITS(t, 30, 20, uint64(i+20))
		store.set(key, fit, now)
		addRegisteredRow(t, db, stack.ID, fmt.Sprintf("lights/M31/s%d.fits", i), app.StackStatusAdded, &key, now)
	}
	opts := DefaultPipelineOptions()
	opts.RegisteredBackfillRate = 1000
	p := NewPipeline(nil, "", "", db, nil, siril.Runner{}, "", opts)
	p.objects = store
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	p.runRegisteredBackfill(ctx)
	if ctx.Err() != nil {
		t.Fatal("backfill did not finish")
	}
	if n, _ := p.legacyRemaining(ctx); n != 0 {
		t.Errorf("%d left", n)
	}
	objs, _ := store.list(ctx, registeredPrefix)
	for _, o := range objs {
		if !strings.HasSuffix(o.Key, registeredExt) {
			t.Errorf("%s left behind", o.Key)
		}
	}
	if len(objs) != 5 {
		t.Errorf("%d objects, want 5", len(objs))
	}
}
