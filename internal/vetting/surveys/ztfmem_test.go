package surveys

import (
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/parquet-go/parquet-go"
)

// writeZTFFile writes a light curve file of n objects spread over a
// square degree around ra, dec, each seen in all exps exposures.
func writeZTFFile(t *testing.T, path string, ra, dec float64, n, exps int) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := parquet.NewGenericWriter[ztfRow](f)
	rng := rand.New(rand.NewPCG(1, 2))
	times := make([]float64, exps)
	for i := range times {
		times[i] = 58300 + float64(i)*1.7
	}
	rows := make([]ztfRow, 0, 64)
	flush := func() {
		if _, err := w.Write(rows); err != nil {
			t.Fatal(err)
		}
		rows = rows[:0]
	}
	for i := range n {
		var r ztfRow
		r.ObjectID = int64(540101100000000 + i)
		r.ObjRA = float32(ra - 0.5 + rng.Float64())
		r.ObjDec = float32(dec - 0.5 + rng.Float64())
		for _, tm := range times {
			r.HMJD.List = append(r.HMJD.List, struct {
				Item float64 `parquet:"item"`
			}{tm})
			r.Mag.List = append(r.Mag.List, struct {
				Item float32 `parquet:"item"`
			}{float32(14 + 4*rng.Float64())})
			r.MagErr.List = append(r.MagErr.List, struct {
				Item float32 `parquet:"item"`
			}{float32(0.01 + 0.1*rng.Float64())})
			r.CatFlags.List = append(r.CatFlags.List, struct {
				Item int32 `parquet:"item"`
			}{0})
		}
		rows = append(rows, r)
		if len(rows) == cap(rows) {
			flush()
		}
	}
	flush()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestZTFPullMemory pulls a light curve file whose footprint holds 6
// million epochs, about 42 MB packed and twice that as it grows, and
// checks the pull holds a tile at a time and not all of it; the parquet
// reader's own buffers take about 60 MB.
func TestZTFPullMemory(t *testing.T) { //nolint:paralleltest
	if testing.Short() {
		t.Skip("writes a 6 million epoch file")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "ztf_000540_zr_c01_q2_dr24.parquet")
	const ra, dec, objects, exps = 294.7, 10.5, 6000, 1000
	writeZTFFile(t, path, ra, dec, objects, exps)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.ServeFile(w, r, path) }))
	defer srv.Close()
	st := newMemStore()
	f := &Fetcher{Store: st, Prefix: "vetting/cache/", HTTP: srv.Client(), WorkDir: dir}
	if _, err := f.helio(); err != nil {
		t.Fatal(err)
	}
	defer debug.SetGCPercent(debug.SetGCPercent(10))
	for range 3 {
		runtime.GC()
	}
	var base runtime.MemStats
	runtime.ReadMemStats(&base)
	var peak atomic.Uint64
	stop := make(chan struct{})
	sampled := make(chan struct{})
	go func() {
		defer close(sampled)
		for {
			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)
			if ms.HeapAlloc > peak.Load() {
				peak.Store(ms.HeapAlloc)
			}
			select {
			case <-stop:
				return
			case <-time.After(2 * time.Millisecond):
			}
		}
	}()
	e, err := f.pullZTFFile(t.Context(), Field{Slug: "mem", RA: ra, Dec: dec, Radius: 2}, "dr24", srv.URL+"/"+filepath.Base(path),
		ZTFQuadrant{Field: 540, CCD: 1, Q: 2, Filter: "zr"})
	close(stop)
	<-sampled
	if err != nil {
		t.Fatal(err)
	}
	if e.Objects != objects || e.Epochs != objects*exps || len(e.Tiles) < 10 {
		t.Fatalf("entry %+v", e)
	}
	if !slices.IsSorted(e.Tiles) {
		t.Errorf("tiles %v not in order", e.Tiles)
	}
	grew := (peak.Load() - min(peak.Load(), base.HeapAlloc)) >> 20
	t.Logf("%d tiles, heap grew %d MiB at most", len(e.Tiles), grew)
	if grew > 120 {
		t.Errorf("the pull held %d MiB; it should hold one tile, not the file's whole footprint", grew)
	}
}
