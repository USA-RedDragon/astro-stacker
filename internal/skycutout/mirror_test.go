package skycutout_test

import (
	"bytes"
	"context"
	imgjpeg "image/jpeg"
	"math"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/skycutout"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

func cdsStub(t *testing.T, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte{0xFF, 0xD8, 0xFF, 0xE0, 1, 2, 3})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestMirroredFieldsRenderLocallyOutsideTheLimit(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	srv := cdsStub(t, &hits)
	s := &skycutout.Service{Endpoint: srv.URL, PerMinute: 1, Local: &skycutout.Renderer{Store: dirStore{root: tileFixtures}, MaxOrder: 8}}
	for _, f := range goldenFields()[:4] {
		c, err := s.Get(context.Background(), f.params())
		if err != nil {
			t.Fatalf("%s: %v", f.name, err)
		}
		img, err := imgjpeg.Decode(bytes.NewReader(c.Data))
		if err != nil {
			t.Fatalf("%s: %v", f.name, err)
		}
		if b := img.Bounds(); b.Dx() != f.width || b.Dy() != f.height || c.ETag == "" {
			t.Errorf("%s: %dx%d etag %q", f.name, b.Dx(), b.Dy(), c.ETag)
		}
	}
	if hits.Load() != 0 || s.Counted() != 0 {
		t.Errorf("hips2fits hit %d times, %d counted against the limit", hits.Load(), s.Counted())
	}
}

func TestFinerFieldsAndMirrorMissesComeFromHiPS2FITS(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	srv := cdsStub(t, &hits)
	s := &skycutout.Service{Endpoint: srv.URL, Local: &skycutout.Renderer{Store: dirStore{root: tileFixtures}, MaxOrder: 8}}
	fine := skycutout.Params{RA: 202.47, Dec: 47.195, FOV: 0.05, Width: 300, Height: 300}
	if _, ok := s.Local.Order(fine); ok {
		t.Fatal("a 0.6\"/px field fits order 8")
	}
	missing := skycutout.Params{RA: 150, Dec: 20, FOV: 1, Width: 300, Height: 300}
	for _, p := range []skycutout.Params{fine, missing} {
		if _, err := s.Get(context.Background(), p); err != nil {
			t.Fatalf("%+v: %v", p, err)
		}
	}
	if hits.Load() != 2 || s.Counted() != 2 {
		t.Errorf("hips2fits hit %d times, %d counted", hits.Load(), s.Counted())
	}
}

func TestOrderIsTheCoarsestAtOrBelowOneOutputPixel(t *testing.T) {
	t.Parallel()
	r := &skycutout.Renderer{MaxOrder: 8}
	for _, c := range []struct {
		p     skycutout.Params
		order int
		ok    bool
	}{
		{skycutout.Params{FOV: 120, Width: 1024, Height: 1024}, 0, true},
		{skycutout.Params{FOV: 74, Width: 600, Height: 400}, 0, true},
		{skycutout.Params{FOV: 30, Width: 600, Height: 400}, 2, true},
		{skycutout.Params{FOV: 1, Width: 300, Height: 300}, 6, true},
		{skycutout.Params{FOV: 0.25, Width: 300, Height: 300}, 8, true},
		{skycutout.Params{FOV: 0.2, Width: 300, Height: 300}, 8, true},
		{skycutout.Params{FOV: 0.1, Width: 300, Height: 300}, 0, false},
	} {
		order, ok := r.Order(c.p)
		if order != c.order || ok != c.ok {
			t.Errorf("%+v: order %d %v, want %d %v", c.p, order, ok, c.order, c.ok)
			continue
		}
		if ok && (skycutout.PixelSize(order) > c.p.Step()*180/math.Pi || order > 0 && skycutout.PixelSize(order-1) <= c.p.Step()*180/math.Pi) {
			t.Errorf("%+v: order %d is not the coarsest fine enough", c.p, order)
		}
	}
}

type countingStore struct {
	dirStore
	fetches atomic.Int32
}

func (c *countingStore) Tile(ctx context.Context, order int, npix int64) ([]byte, error) {
	c.fetches.Add(1)
	return c.dirStore.Tile(ctx, order, npix)
}

func TestDecodedTilesStayCachedWithinTheMemoryLimit(t *testing.T) {
	t.Parallel()
	p := goldenFields()[0].params()
	for _, c := range []struct {
		name      string
		mem       int64
		refetches bool
	}{
		{"room", 100 << 20, false},
		{"one tile", 512 * 512 * 3, true},
	} {
		store := &countingStore{dirStore: dirStore{root: tileFixtures}}
		r := &skycutout.Renderer{Store: store, MaxOrder: 8, CacheBytes: c.mem}
		if _, err := r.Render(context.Background(), p); err != nil {
			t.Fatal(err)
		}
		first := store.fetches.Load()
		if _, err := r.Render(context.Background(), p); err != nil {
			t.Fatal(err)
		}
		if again := store.fetches.Load() - first; (again > 0) != c.refetches || first < 2 {
			t.Errorf("%s: %d fetches then %d more", c.name, first, again)
		}
	}
}

func TestTilePathFollowsTheHiPSLayout(t *testing.T) {
	t.Parallel()
	if got := skycutout.TilePath(8, 786431); got != "Norder8/Dir780000/Npix786431.jpg" {
		t.Error(got)
	}
	if got := skycutout.TilePath(3, 9999); got != "Norder3/Dir0/Npix9999.jpg" {
		t.Error(got)
	}
}

type stuckStore struct{ calls atomic.Int32 }

func (s *stuckStore) Tile(ctx context.Context, _ int, _ int64) ([]byte, error) {
	s.calls.Add(1)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestAStuckMirrorFallsBackToHiPS2FITSInTime(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	srv := cdsStub(t, &hits)
	store := &stuckStore{}
	s := &skycutout.Service{Endpoint: srv.URL, LocalWait: 200 * time.Millisecond, Local: &skycutout.Renderer{Store: store, MaxOrder: 8}}
	start := time.Now()
	c, err := s.Get(context.Background(), skycutout.Params{RA: 10, Dec: 20, FOV: 28.4, Width: 1024, Height: 1024})
	if err != nil || len(c.Data) == 0 || hits.Load() != 1 {
		t.Fatalf("%v, hips2fits hit %d times", err, hits.Load())
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("fallback took %v", d)
	}
}

func TestLocalRendersStayOutOfTheDatabaseCache(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	srv := cdsStub(t, &hits)
	db := openDB(t)
	s := &skycutout.Service{DB: db, Endpoint: srv.URL, Local: &skycutout.Renderer{Store: dirStore{root: tileFixtures}, MaxOrder: 8}}
	first, err := s.Get(context.Background(), goldenFields()[0].params())
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.Get(context.Background(), goldenFields()[0].params())
	if err != nil || again.ETag != first.ETag {
		t.Fatalf("re-render %v, etag %s then %s", err, first.ETag, again.ETag)
	}
	var n int64
	db.Model(&app.SkyCutout{}).Count(&n)
	if n != 0 || hits.Load() != 0 {
		t.Errorf("%d rows stored, hips2fits hit %d times", n, hits.Load())
	}
}
