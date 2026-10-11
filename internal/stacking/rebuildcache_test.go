package stacking

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
	"github.com/USA-RedDragon/astro-stacker/internal/siril"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type countingS3 struct {
	mu    sync.Mutex
	objs  map[string][]byte
	gets  map[string]int
	bytes int64
	rate  int64
}

func (c *countingS3) total() (int, int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, v := range c.gets {
		n += v
	}
	return n, c.bytes
}

func newCountingS3(t *testing.T) (*minio.Client, *countingS3) {
	t.Helper()
	c := &countingS3{objs: map[string][]byte{}, gets: map[string]int{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/out/")
		c.mu.Lock()
		b, ok := c.objs[key]
		if ok && r.Method == http.MethodGet {
			c.gets[key]++
			c.bytes += int64(len(b))
		}
		c.mu.Unlock()
		if !ok {
			http.Error(w, "missing", http.StatusNotFound)
			return
		}
		w.Header().Set("ETag", `"0"`)
		w.Header().Set("Last-Modified", time.Unix(0, 0).UTC().Format(http.TimeFormat))
		w.Header().Set("Content-Length", strconv.Itoa(len(b)))
		if r.Method != http.MethodGet {
			return
		}
		if c.rate <= 0 {
			_, _ = w.Write(b)
			return
		}
		start := time.Now()
		for off := 0; off < len(b); off += 1 << 20 {
			_, _ = w.Write(b[off:min(off+1<<20, len(b))])
			time.Sleep(time.Until(start.Add(time.Duration(int64(off+1<<20) * int64(time.Second) / c.rate))))
		}
	}))
	t.Cleanup(srv.Close)
	s3, err := minio.New(strings.TrimPrefix(srv.URL, "http://"), &minio.Options{
		Creds: credentials.NewStaticV4("k", "s", ""), Region: "us-east-1"})
	if err != nil {
		t.Fatal(err)
	}
	return s3, c
}

type rebuildCase struct {
	subs, w, h int
	gains      []float64
	rate       int64
}

func runRebuild(t *testing.T, rc rebuildCase, opts PipelineOptions) (*Accumulator, *countingS3, time.Duration) {
	t.Helper()
	s3, store := newCountingS3(t)
	store.rate = rc.rate
	db := offTargetDB(t)
	if err := db.AutoMigrate(&app.Stack{}); err != nil {
		t.Fatal(err)
	}
	stack := app.Stack{Object: "M31", Filter: "L"}
	if err := db.Create(&stack).Error; err != nil {
		t.Fatal(err)
	}
	r := rand.New(rand.NewPCG(1, 2))
	for i := range rc.subs {
		px := noisySub(r, rc.w*rc.h, 0.02, 0.002)
		for s := range 20 {
			px[(i*37+s*977)%len(px)] = 0.5
		}
		var buf bytes.Buffer
		if err := imagedata.WriteFITS(&buf, rc.w, rc.h, 1, px, nil); err != nil {
			t.Fatal(err)
		}
		key := fmt.Sprintf("registered/M31/L/s%04d.fit", i)
		store.objs[key] = buf.Bytes()
		gain := rc.gains[i%len(rc.gains)]
		f := app.Frame{Key: fmt.Sprintf("raw/%d.fit", i), Type: "LIGHT", Gain: &gain}
		if err := db.Create(&f).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&app.StackFrame{FrameID: f.ID, StackID: &stack.ID, Status: app.StackStatusAdded,
			Exposure: 300, Weight: 1, RegisteredKey: &key}).Error; err != nil {
			t.Fatal(err)
		}
	}
	p := NewPipeline(s3, "raw", "out", db, nil, siril.Runner{}, t.TempDir(), opts)
	start := time.Now()
	acc, err := p.rebuild(context.Background(), &stack)
	took := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if p.cacheHeld != 0 {
		t.Errorf("rebuild left %d bytes of the cache budget taken", p.cacheHeld)
	}
	return acc, store, took
}

func accDigest(a *Accumulator) string {
	h := sha256.New()
	for _, s := range [][]float32{a.Mean, a.M2, a.Weight} {
		_ = binary.Write(h, binary.LittleEndian, s)
	}
	_ = binary.Write(h, binary.LittleEndian, a.Count)
	return fmt.Sprintf("%x", h.Sum(nil))[:16]
}

func TestRebuildDownloadMeasurement(t *testing.T) {
	t.Parallel()
	spec := os.Getenv("REBUILD_MEASURE")
	if spec == "" {
		t.Skip("set REBUILD_MEASURE=subs,width,height,MB/s[,gains...]")
	}
	f := strings.Split(spec, ",")
	n := make([]int, 4)
	for i := range n {
		n[i], _ = strconv.Atoi(f[i])
	}
	rc := rebuildCase{subs: n[0], w: n[1], h: n[2], rate: int64(n[3]) << 20, gains: []float64{100}}
	for _, g := range f[4:] {
		v, _ := strconv.ParseFloat(g, 64)
		rc.gains = append(rc.gains, v)
	}
	for _, cache := range []int64{0, DefaultPipelineOptions().RebuildCache} {
		opts := DefaultPipelineOptions()
		opts.RebuildCache = cache
		acc, store, took := runRebuild(t, rc, opts)
		gets, b := store.total()
		var one int64
		for _, o := range store.objs {
			one += int64(len(o))
		}
		t.Logf("cache=%d subs=%d gains=%v downloads=%d bytes=%d (%.2fx the subs) wall=%s digest=%s",
			cache, rc.subs, rc.gains, gets, b, float64(b)/float64(one), took.Round(time.Millisecond), accDigest(acc))
	}
}

func perSub(t *testing.T, store *countingS3) (lo, hi int) {
	t.Helper()
	lo = 1 << 30
	for k := range store.objs {
		n := store.gets[k]
		lo, hi = min(lo, n), max(hi, n)
	}
	return lo, hi
}

func TestRebuildDownloadsEachSubOnce(t *testing.T) {
	t.Parallel()
	for _, rc := range []rebuildCase{
		{subs: 12, w: 96, h: 64, gains: []float64{100, 200}},
		{subs: 41, w: 96, h: 64, gains: []float64{100}},
		{subs: 6, w: 96, h: 64, gains: []float64{100, 200}},
	} {
		uncached := DefaultPipelineOptions()
		uncached.RebuildCache = 0
		want, _, _ := runRebuild(t, rc, uncached)
		opts := DefaultPipelineOptions()
		opts.RebuildCacheFree = 0
		acc, store, _ := runRebuild(t, rc, opts)
		if lo, hi := perSub(t, store); lo != 1 || hi != 1 {
			t.Errorf("%d subs: each downloaded %d to %d times, want once", rc.subs, lo, hi)
		}
		if accDigest(acc) != accDigest(want) {
			t.Errorf("%d subs: master differs from the uncached rebuild", rc.subs)
		}
	}
}

func TestRebuildCacheStaysInBudget(t *testing.T) {
	t.Parallel()
	rc := rebuildCase{subs: 12, w: 96, h: 64, gains: []float64{100}}
	var size int64
	for _, tc := range []struct {
		name       string
		cache      int64
		free       int64
		lo, hi     int
		downloaded int
	}{
		{name: "probe", cache: 0, lo: 3, hi: 3},
		{name: "five subs", cache: 5, lo: 1, hi: 3, downloaded: 12 + 2*7},
		{name: "full volume", cache: 100, free: 1 << 62, lo: 3, hi: 3},
	} {
		opts := DefaultPipelineOptions()
		opts.RebuildCache, opts.RebuildCacheFree = tc.cache*size, tc.free
		_, store, _ := runRebuild(t, rc, opts)
		if size == 0 {
			for _, b := range store.objs {
				size = int64(len(b))
			}
		}
		lo, hi := perSub(t, store)
		if lo != tc.lo || hi != tc.hi {
			t.Errorf("%s: each sub downloaded %d to %d times, want %d to %d", tc.name, lo, hi, tc.lo, tc.hi)
		}
		if n, _ := store.total(); tc.downloaded > 0 && n != tc.downloaded {
			t.Errorf("%s: %d downloads, want %d", tc.name, n, tc.downloaded)
		}
	}
}
