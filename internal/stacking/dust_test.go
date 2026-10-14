package stacking

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/calmatch"
	"github.com/USA-RedDragon/astro-stacker/internal/coverage"
	"github.com/USA-RedDragon/astro-stacker/internal/dustcheck"
	"github.com/USA-RedDragon/astro-stacker/internal/metrics"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// memS3 is a bucket held in memory.
func memS3(t *testing.T) (*minio.Client, func() map[string][]byte) {
	t.Helper()
	var mu sync.Mutex
	objs := map[string][]byte{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		k, _ := url.PathUnescape(strings.TrimPrefix(r.URL.EscapedPath(), "/out/"))
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case http.MethodPut:
			b, _ := io.ReadAll(r.Body)
			if strings.HasPrefix(r.Header.Get("X-Amz-Content-Sha256"), "STREAMING-") {
				b = unchunk(b)
			}
			objs[k] = b
			w.Header().Set("ETag", `"0"`)
		case http.MethodGet, http.MethodHead:
			b, ok := objs[k]
			if !ok {
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(http.StatusNotFound)
				_, _ = io.WriteString(w, `<Error><Code>NoSuchKey</Code></Error>`)
				return
			}
			w.Header().Set("Content-Length", fmt.Sprint(len(b)))
			w.Header().Set("ETag", `"0"`)
			w.Header().Set("Last-Modified", time.Unix(0, 0).UTC().Format(http.TimeFormat))
			if r.Method == http.MethodGet {
				_, _ = w.Write(b)
			}
		case http.MethodDelete:
			delete(objs, k)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "not implemented", http.StatusNotImplemented)
		}
	}))
	t.Cleanup(srv.Close)
	s3, err := minio.New(strings.TrimPrefix(srv.URL, "http://"), &minio.Options{
		Creds: credentials.NewStaticV4("k", "s", ""), Region: testRegion})
	if err != nil {
		t.Fatal(err)
	}
	return s3, func() map[string][]byte {
		mu.Lock()
		defer mu.Unlock()
		out := make(map[string][]byte, len(objs))
		for k, v := range objs {
			out[k] = v
		}
		return out
	}
}

// unchunk decodes an aws-chunked upload body.
func unchunk(b []byte) []byte {
	var out []byte
	for len(b) > 0 {
		line, rest, ok := bytes.Cut(b, []byte("\r\n"))
		if !ok {
			break
		}
		hexSize, _, _ := bytes.Cut(line, []byte(";"))
		n, err := strconv.ParseInt(string(hexSize), 16, 64)
		if err != nil || n == 0 || int(n) > len(rest) {
			break
		}
		out = append(out, rest[:n]...)
		b = bytes.TrimPrefix(rest[n:], []byte("\r\n"))
	}
	return out
}

const (
	dustW = 2400
	dustH = 1600
)

type dustDisc struct {
	x, y, d int
	depth   float64
}

func dustPlane(seed uint64, sky float64, vignette float64, noise float64, discs ...dustDisc) []float32 {
	rng := rand.New(rand.NewPCG(seed, 3))
	pix := make([]float32, dustW*dustH)
	for y := range dustH {
		for x := range dustW {
			dx, dy := float64(x-dustW/2)/dustW, float64(y-dustH/2)/dustW
			v := sky * (1 - vignette*(dx*dx+dy*dy))
			for _, d := range discs {
				if math.Hypot(float64(x-d.x), float64(y-d.y)) < float64(d.d)/2 {
					v *= 1 - d.depth
				}
			}
			pix[y*dustW+x] = float32(v * (1 + noise*rng.NormFloat64()))
		}
	}
	return pix
}

func dustDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&app.Frame{}, &app.DustFlat{}, &app.DustEvent{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func addFlatFrames(t *testing.T, db *gorm.DB, night time.Time, filter string, at time.Time) {
	t.Helper()
	for i := range 3 {
		n := night
		d := at.Add(time.Duration(i) * time.Minute)
		rot, exp, gain := 141.2, 1.0, 100.0
		f := app.Frame{Key: fmt.Sprintf("flat/%s/%s/%d.xisf", night.Format(time.DateOnly), filter, i), ETag: "e", Type: frameTypeFlat,
			Object: objectM31, Filter: filter, Night: &n, DateObs: &d, Rotator: &rot, Exposure: &exp, Gain: &gain,
			LastModified: at.Add(-24 * time.Hour)}
		if err := db.Create(&f).Error; err != nil {
			t.Fatal(err)
		}
	}
}

const dustBucket = "out"

func dustPipeline(t *testing.T) (*Pipeline, *gorm.DB, func() map[string][]byte) {
	t.Helper()
	s3, objects := memS3(t)
	db := dustDB(t)
	p := &Pipeline{s3: s3, dest: dustBucket, db: db, workDir: t.TempDir(),
		opts: PipelineOptions{CalibrationSettle: time.Hour, DustCheck: true}}
	return p, db, objects
}

func TestDustCheckFlats(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p, db, objects := dustPipeline(t)
	now := time.Now().UTC().Truncate(24 * time.Hour)
	night1, night2 := now.AddDate(0, 0, -20), now.AddDate(0, 0, -1)
	for _, f := range []string{filterRed, filterGreen} {
		addFlatFrames(t, db, night1, f, night1.Add(28*time.Hour))
		addFlatFrames(t, db, night2, f, night2.Add(28*time.Hour))
	}
	built := 0
	p.dustMasterFor = func(_ context.Context, s calmatch.Set, _ []calmatch.Set) (string, string, error) {
		built++
		var discs []dustDisc
		if s.Night.Equal(night2) {
			discs = append(discs, dustDisc{1200, 800, 400, 0.01})
		}
		key := s.Night.Format("20060102") + s.Filter
		file := filepath.Join(p.workDir, key+".fit")
		if err := writeFITSFile(file, dustW, dustH, 1, dustPlane(uint64(len(key)+built), 0.5, 0.08, 0.002, discs...), nil); err != nil { //nolint:gosec
			return "", "", err
		}
		return file, key, nil
	}
	if err := p.CheckFlats(ctx); err != nil {
		t.Fatal(err)
	}
	var flats []app.DustFlat
	db.Order("taken_at").Find(&flats)
	if len(flats) != 4 {
		t.Fatalf("%d flats checked, want 4", len(flats))
	}
	var evs []app.DustEvent
	db.Where("class = ?", dustcheck.ClassNew).Find(&evs)
	if len(evs) != 2 {
		t.Fatalf("new shadows %+v, want one in each filter", evs)
	}
	for _, e := range evs {
		if e.Scope != dustcheck.ScopeWindow || e.Filters != "Green,Red" || math.Hypot(float64(e.X-1200), float64(e.Y-800)) > 40 {
			t.Errorf("event %+v, want a window shadow at 1200,800 in Green and Red", e)
		}
		if e.CropKey == nil || len(objects()[*e.CropKey]) == 0 {
			t.Errorf("event %d has no crop", e.ID)
		}
	}
	if err := p.CheckFlats(ctx); err != nil {
		t.Fatal(err)
	}
	if built != 4 {
		t.Errorf("masters asked for %d times, want each set once", built)
	}
	p.dustMetrics(ctx, night2.Add(30*time.Hour))
	if got := testutil.ToFloat64(metrics.FlatDustEvents.WithLabelValues(filterRed, dustcheck.ClassNew, dustcheck.ScopeWindow)); got != 1 {
		t.Errorf("flat dust gauge %v, want 1", got)
	}
}
func TestMasterPixelsLeavesNoCopy(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p, db, _ := dustPipeline(t)
	if err := db.AutoMigrate(&app.CalibrationMaster{}); err != nil {
		t.Fatal(err)
	}
	night := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -30)
	addFlatFrames(t, db, night, filterRed, night.Add(28*time.Hour))
	sets, err := coverage.Sets(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	var set calmatch.Set
	for _, s := range sets {
		if s.Type == frameTypeFlat {
			set = s
		}
	}
	frames, err := coverage.SetFrames(ctx, db, set)
	if err != nil {
		t.Fatal(err)
	}
	key := setKey(frameTypeFlat, frames)
	src := filepath.Join(t.TempDir(), "m.fit")
	if err := writeFITSFile(src, 64, 32, 1, make([]float32, 64*32), nil); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	obj := "calibration/flat/" + key + ".fit"
	if err := p.putBytes(ctx, obj, b, minio.PutObjectOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&app.CalibrationMaster{SetKey: key, Type: frameTypeFlat, ObjectKey: obj, Frames: 3}).Error; err != nil {
		t.Fatal(err)
	}
	data, w, h, got, err := p.masterPixels(ctx, set, sets)
	if err != nil {
		t.Fatal(err)
	}
	if got != key || w != 64 || h != 32 || len(data) != 64*32 {
		t.Fatalf("read %s %dx%d", got, w, h)
	}
	left, _ := os.ReadDir(p.workDir)
	if len(left) != 0 {
		t.Errorf("left %d entries in the work directory", len(left))
	}
}
