package ownimage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/frameheader"
	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
	"github.com/USA-RedDragon/astro-stacker/internal/starcat"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/testdb"
)

type memStore struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (s *memStore) Get(_ context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.m[key]
	if !ok {
		return nil, fmt.Errorf("no %s", key)
	}
	return b, nil
}

func (s *memStore) Put(_ context.Context, key string, data []byte, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[key] = data
	return nil
}

func (s *memStore) RemovePrefix(context.Context, string) error { return nil }

func ptr[T any](v T) *T { return &v }

func seedDB(t *testing.T) DBData {
	t.Helper()
	db := testdb.SQLite(t, "ownimage")
	if err := db.AutoMigrate(&app.Frame{}, &app.Stack{}, &app.StackFrame{}, &app.TargetReference{}, &app.CalibrationMaster{}); err != nil {
		t.Fatal(err)
	}
	cards, err := json.Marshal([]frameheader.Card{
		{Name: "CTYPE1", Value: "RA---TAN"}, {Name: "CTYPE2", Value: "DEC--TAN"}, {Name: "CRVAL1", Value: "150"}, {Name: "CRVAL2", Value: "30"},
		{Name: "CRPIX1", Value: "180"}, {Name: "CRPIX2", Value: "140"}, {Name: "CD1_1", Value: "-0.000527"}, {Name: "CD2_2", Value: "0.000527"},
	})
	if err != nil {
		t.Fatal(err)
	}
	must := func(v any) {
		t.Helper()
		if err := db.Create(v).Error; err != nil {
			t.Fatal(err)
		}
	}
	must(&app.TargetReference{Object: synthObj, FrameID: 1, ObjectKey: "ref.xisf", WCS: ptr(string(cards))})
	st := &app.Stack{Object: synthObj, Filter: synthLum, Width: synthW, Height: synthH, MasterKey: ptr("stacks/m.fit")}
	must(st)
	must(&app.CalibrationMaster{SetKey: "darkset", Type: "Dark", ObjectKey: "calibration/dark/darkset.fit", Frames: 20})
	base := time.Date(2025, 6, 4, 3, 0, 0, 0, time.UTC)
	rows := []struct {
		status string
		key    *string
		dark   *string
		weight float64
	}{
		{app.StackStatusAdded, ptr("registered/a.xisf"), ptr("darkset"), 2},
		{app.StackStatusAdded, ptr("registered/b.xisf"), ptr("calibration/imported/dark.fit"), 1},
		{app.StackStatusAdded, ptr("registered/c.xisf"), nil, 1},
		{app.StackStatusLowScore, ptr("registered/d.xisf"), nil, 1},
		{app.StackStatusAdded, nil, nil, 1},
	}
	for i, r := range rows {
		night := time.Date(2025, 6, 3, 0, 0, 0, 0, time.UTC)
		obs := base.Add(time.Duration(len(rows)-i) * time.Minute)
		f := &app.Frame{Key: fmt.Sprintf("raw/%d.fits", i), ETag: "e", Size: 1, LastModified: base, Type: "LIGHT", Object: synthObj, Filter: synthLum,
			Exposure: ptr(300.0), DateObs: &obs, Night: &night}
		must(f)
		must(&app.StackFrame{FrameID: f.ID, StackID: &st.ID, Status: r.status, Weight: r.weight, RegisteredKey: r.key, DarkMaster: r.dark,
			ProcessedAt: obs.Add(time.Hour)})
	}
	return DBData{App: db, Store: &memStore{m: map[string][]byte{}}}
}

func TestDBDataSubs(t *testing.T) {
	t.Parallel()
	d := seedDB(t)
	subs, err := d.Subs(context.Background(), synthObj)
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 3 {
		t.Fatalf("got %d subs: %+v", len(subs), subs)
	}
	if subs[0].Key != "registered/c.xisf" || subs[2].Key != "registered/a.xisf" {
		t.Errorf("not oldest first: %s %s", subs[0].Key, subs[2].Key)
	}
	a, b, c := subs[2], subs[1], subs[0]
	if a.DarkKey == nil || *a.DarkKey != "calibration/dark/darkset.fit" {
		t.Errorf("dark of a: %v", a.DarkKey)
	}
	if b.DarkKey == nil || *b.DarkKey != "calibration/imported/dark.fit" {
		t.Errorf("dark of b: %v", b.DarkKey)
	}
	if c.DarkKey != nil {
		t.Errorf("dark of c: %v", *c.DarkKey)
	}
	if a.Weight != 2 || a.Exposure != 300 || a.Filter != synthLum || nightKey(a.Night) != flaggedDay || !a.AddedAt.Equal(a.Start.Add(time.Hour)) {
		t.Errorf("sub a %+v", a)
	}
}

func TestDBDataFrameLoadAndGet(t *testing.T) {
	t.Parallel()
	d := seedDB(t)
	ctx := context.Background()
	w, width, height, err := d.Frame(ctx, synthObj)
	if err != nil {
		t.Fatal(err)
	}
	if width != synthW || height != synthH || math.Abs(w.PixelScale()-1.8972) > 0.001 {
		t.Errorf("frame %d x %d at %.4f", width, height, w.PixelScale())
	}
	if _, _, _, err := d.Frame(ctx, "Nothing"); err == nil {
		t.Error("frame of an unknown target")
	}
	data := make([]float32, 4*3)
	data[5] = 0.25
	var b bytes.Buffer
	if err := imagedata.WriteFITS(&b, 4, 3, 1, data, nil); err != nil {
		t.Fatal(err)
	}
	if err := d.Store.Put(ctx, "registered/a.fit", b.Bytes(), ""); err != nil {
		t.Fatal(err)
	}
	p, err := d.Load(ctx, "registered/a.fit")
	if err != nil {
		t.Fatal(err)
	}
	if p.W != 4 || p.H != 3 || !isNaN32(p.Data[0]) || math.Abs(float64(p.Data[5])-0.25) > 1e-6 {
		t.Errorf("plane %+v", p)
	}
	if got, err := d.Get(ctx, "registered/a.fit"); err != nil || !bytes.Equal(got, b.Bytes()) {
		t.Errorf("get: %v", err)
	}
	if _, err := d.Load(ctx, "missing"); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Errorf("load of a missing key: %v", err)
	}
	if _, err := d.Stars(ctx, 150, 30, 0.1); !errors.Is(err, ErrNoGaia) {
		t.Errorf("stars without a mirror: %v", err)
	}
}

func TestStarsOfMovesByProperMotion(t *testing.T) {
	t.Parallel()
	nan := float32(math.NaN())
	rows := []starcat.GaiaStar{
		{SourceID: 1, RA: 10, Dec: 0, PMRA: 3600000, PMDec: 0, G: 15, BP: 15.5, RP: 14.5, Variable: starcat.VariableVariable},
		{SourceID: 2, RA: 20, Dec: 1, PMRA: math.NaN(), PMDec: math.NaN(), G: 16, BP: nan, RP: 15},
	}
	got := starsOf(rows, starcat.GaiaEpoch+1)
	if math.Abs(got[0].RA-11) > 1e-3 || got[0].BPRP != 1 || !got[0].Variable {
		t.Errorf("star 1 %+v", got[0])
	}
	if got[1].RA != 20 || !math.IsNaN(got[1].BPRP) || got[1].Variable {
		t.Errorf("star 2 %+v", got[1])
	}
}
