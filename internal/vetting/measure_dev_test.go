//go:build vettingdev

package vetting

import (
	"os"
	"runtime"
	"runtime/pprof"
	"strconv"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/memwatch"
	"github.com/USA-RedDragon/astro-stacker/internal/starcat"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/ownimage"
	"github.com/minio/minio-go/v7"
	miniocreds "github.com/minio/minio-go/v7/pkg/credentials"
	pgdriver "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// heapPeaks writes a heap profile to path each time the heap in use
// grows by a twentieth, until the returned stop.
func heapPeaks(path string) func() {
	done := make(chan struct{})
	go func() {
		var best uint64
		for {
			select {
			case <-done:
				return
			case <-time.After(200 * time.Millisecond):
			}
			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)
			if ms.HeapInuse > best+best/20 {
				best = ms.HeapInuse
				if f, err := os.Create(path); err == nil {
					_ = pprof.Lookup("heap").WriteTo(f, 0)
					_ = f.Close()
				}
			}
		}
	}()
	return func() { close(done) }
}

// TestDevMeasure runs the own image checks of every source due on the
// target VET_DEV_OBJECT, reading the real database (VET_DEV_DSN) and
// bucket (S3_ACCESS_KEY, S3_SECRET_KEY) and writing nothing, and logs the
// peak memory; VET_DEV_PROF names a heap profile to write at the peak.
func TestDevMeasure(t *testing.T) { //nolint:paralleltest
	object, dsn := os.Getenv("VET_DEV_OBJECT"), os.Getenv("VET_DEV_DSN")
	if object == "" || dsn == "" {
		t.Skip("VET_DEV_OBJECT and VET_DEV_DSN not set")
	}
	threads, _ := strconv.Atoi(os.Getenv("VET_DEV_THREADS"))
	db, err := gorm.Open(pgdriver.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	mc, err := minio.New("s3.mcswain.dev", &minio.Options{Creds: miniocreds.NewStaticV4(os.Getenv("S3_ACCESS_KEY"), os.Getenv("S3_SECRET_KEY"), ""),
		Secure: true, Region: "us-east-1"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	w := memwatch.Start(100 * time.Millisecond)
	defer w.Stop()
	dues, err := Pick(ctx, db, time.Now().UTC(), []string{object}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := strconv.Atoi(os.Getenv("VET_DEV_SOURCES")); n > 0 && n < len(dues) {
		dues = dues[:n]
	}
	ids := make([]int, len(dues))
	for i, d := range dues {
		ids[i] = d.Summary.Source.ID
	}
	var cands []app.DiscoveryCandidate
	if err := db.WithContext(ctx).Where("source_id IN ?", ids).Find(&cands).Error; err != nil {
		t.Fatal(err)
	}
	byID := map[int][]app.DiscoveryCandidate{}
	runIDs := map[int]bool{}
	for _, c := range cands {
		byID[*c.SourceID] = append(byID[*c.SourceID], c)
		runIDs[c.RunID] = true
	}
	s := &Service{App: db}
	runs, err := s.runs(ctx, runIDs)
	if err != nil {
		t.Fatal(err)
	}
	spots := make([]ownimage.Spot, 0, len(dues))
	for _, d := range dues {
		spots = append(spots, spotOf(d.Summary, byID[d.Summary.Source.ID], runs, time.Now().UTC()))
	}
	cands = nil
	t.Logf("%s: %d sources due, setup peak %d MiB", object, len(dues), memwatch.MiB(w.Step().RSS))
	data := ownimage.DBData{App: db, Store: discoveries.MinioStore{Client: mc, Bucket: "astro-processed"},
		Gaia: starcat.NewGaia(starcat.MinioBucket{Client: mc, Name: "sky-survey"}, "catalogs/gaia-dr3/v1")}
	if p := os.Getenv("VET_DEV_PROF"); p != "" {
		defer heapPeaks(p)()
	}
	start := time.Now()
	res, err := ownimage.Measure(ctx, data, object, spots, ownimage.Options{Threads: max(1, threads), WorkDir: os.Getenv("VET_DEV_WORKDIR")})
	if err != nil {
		t.Fatal(err)
	}
	own := w.Step()
	t.Logf("%d results in %s, own image peak %d MiB resident, %d MiB live heap, process peak %d MiB", len(res), time.Since(start).Round(time.Second),
		memwatch.MiB(own.RSS), memwatch.MiB(own.Live), memwatch.MiB(w.Peak().RSS))
}
