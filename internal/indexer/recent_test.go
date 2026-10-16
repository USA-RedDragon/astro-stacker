package indexer

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
	"github.com/USA-RedDragon/astro-stacker/internal/measure"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/wake"
	"github.com/glebarez/sqlite"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"gorm.io/gorm"
)

type listedObject struct {
	Key          string
	ETag         string
	Size         int64
	LastModified string
}

type listResult struct {
	XMLName     xml.Name `xml:"ListBucketResult"`
	Name        string
	Prefix      string
	KeyCount    int
	MaxKeys     int
	IsTruncated bool
	Contents    []listedObject
}

// bucketServer is an S3 bucket that lists and serves objects, and records
// the prefixes listed and where each listing started.
type bucketServer struct {
	mu         sync.Mutex
	objs       map[string][]byte
	prefixes   []string
	startAfter map[string]string
}

func (b *bucketServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	key := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/astro"), "/")
	if key == "" && r.URL.Query().Has("list-type") {
		prefix, after := r.URL.Query().Get("prefix"), r.URL.Query().Get("start-after")
		b.prefixes = append(b.prefixes, prefix)
		if after != "" {
			if b.startAfter == nil {
				b.startAfter = map[string]string{}
			}
			b.startAfter[prefix] = after
		}
		res := listResult{Name: "astro", Prefix: prefix, MaxKeys: 1000}
		for k, v := range b.objs {
			if strings.HasPrefix(k, prefix) && k > after {
				res.Contents = append(res.Contents, listedObject{Key: k, ETag: etagOf(v), Size: int64(len(v)),
					LastModified: time.Now().UTC().Format(time.RFC3339)})
			}
		}
		res.KeyCount = len(res.Contents)
		w.Header().Set("Content-Type", "application/xml")
		_ = xml.NewEncoder(w).Encode(res)
		return
	}
	v, ok := b.objs[key]
	if !ok {
		http.Error(w, "no such key", http.StatusNotFound)
		return
	}
	w.Header().Set("ETag", etagOf(v))
	w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
	http.ServeContent(w, r, key, time.Now(), bytes.NewReader(v))
}

func etagOf(b []byte) string {
	return fmt.Sprintf(`"%x"`, len(b))
}

func fitsLight(t *testing.T, object string) []byte {
	t.Helper()
	return fitsFrame(t, "LIGHT", object)
}

func fitsFrame(t *testing.T, typ, object string) []byte {
	t.Helper()
	const w, h = 64, 64
	px := make([]float32, w*h)
	for i := range px {
		px[i] = 0.01
	}
	for s := range 20 {
		px[(s*373+97)%len(px)] = 0.6
	}
	var buf bytes.Buffer
	if err := imagedata.WriteFITS(&buf, w, h, 1, px, []imagedata.Card{
		imagedata.StringCard("IMAGETYP", typ, ""),
		imagedata.StringCard("OBJECT", object, ""),
		imagedata.StringCard("FILTER", "L", ""),
		imagedata.FloatCard("EXPTIME", 300, ""),
		imagedata.StringCard("DATE-OBS", time.Now().UTC().Add(-time.Hour).Format("2006-01-02T15:04:05"), ""),
	}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// IndexRecent lists only the folders of recent lights and of the targets
// Target Scheduler imaged lately, and the calibration keys dated within
// RecentWindow, indexes what is new there, measures its starlight and wakes
// the loops waiting on new frames.
func TestIndexRecent(t *testing.T) {
	t.Parallel()
	old := fitsLight(t, "M31")
	bucket := &bucketServer{objs: map[string][]byte{
		"M31/LIGHT/old.fits":         old,
		"M31/LIGHT/new.fits":         fitsLight(t, "M31"),
		"M31/LIGHT/notes.txt":        []byte("x"),
		"NGC 7000/LIGHT/first.fits":  fitsLight(t, "NGC 7000"),
		"Stale/LIGHT/unindexed.fits": fitsLight(t, "Stale"),
		"DARK/" + time.Now().UTC().Format("2006-01-02_15-04-05") + "_L_-10.00_300.00s_0000.fits": fitsFrame(t, "DARK", ""),
		"BIAS/" + time.Now().UTC().Format("2006-01-02_15-04-05") + "_L_-10.00_0.00s_0000.fits":   fitsFrame(t, "BIAS", ""),
		"DARK/2020-01-01_00-00-00_L_-10.00_300.00s_0000.fits":                                    fitsFrame(t, "DARK", ""),
	}}
	srv := httptest.NewServer(bucket)
	t.Cleanup(srv.Close)
	s3, err := minio.New(strings.TrimPrefix(srv.URL, "http://"), &minio.Options{
		Creds: credentials.NewStaticV4("k", "s", ""), Region: "us-east-1"})
	if err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&app.Frame{}, &app.StackFrame{}); err != nil {
		t.Fatal(err)
	}
	recent, long := time.Now().Add(-time.Hour), time.Now().Add(-30*24*time.Hour)
	for _, f := range []app.Frame{
		{Key: "M31/LIGHT/old.fits", ETag: strings.Trim(etagOf(old), `"`), DateObs: &recent},
		{Key: "Stale/LIGHT/indexed.fits", ETag: "x", DateObs: &long},
	} {
		f.Type, f.LastModified = lightFrame, time.Now()
		if err := db.Create(&f).Error; err != nil {
			t.Fatal(err)
		}
	}

	ix := New(s3, "astro", db, 2)
	ix.Wake = &wake.Signal{}
	ix.RecentTargets = func(context.Context) ([]string, error) { return []string{"NGC 7000", "M31"}, nil }
	woke := ix.Wake.C()
	n, err := ix.IndexRecent(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Errorf("indexed %d, want 4: new.fits, first.fits and the new dark and bias", n)
	}
	select {
	case <-woke:
	default:
		t.Error("Wake not notified")
	}
	bucket.mu.Lock()
	listed := slices.Sorted(slices.Values(bucket.prefixes))
	after := bucket.startAfter
	bucket.mu.Unlock()
	if want := []string{"BIAS/", "DARK/", "M31/", "NGC 7000/"}; !slices.Equal(listed, want) {
		t.Errorf("listed %q, want %q", listed, want)
	}
	from := time.Now().UTC().Add(-RecentWindow - 24*time.Hour).Format(time.DateOnly)
	if after["DARK/"] != "DARK/"+from || after["BIAS/"] != "BIAS/"+from || after["M31/"] != "" {
		t.Errorf("listings started after %q, want the calibration folders from %s and the light folders from the start", after, from)
	}
	var frames []app.Frame
	if err := db.Order("key").Find(&frames).Error; err != nil {
		t.Fatal(err)
	}
	got := map[string]app.Frame{}
	for _, f := range frames {
		got[f.Key] = f
	}
	for _, k := range []string{"M31/LIGHT/new.fits", "NGC 7000/LIGHT/first.fits"} {
		f, ok := got[k]
		switch {
		case !ok:
			t.Errorf("%s not indexed", k)
		case f.Type != lightFrame || f.IndexError != nil:
			t.Errorf("%s indexed as %q, error %v", k, f.Type, f.IndexError)
		case f.PhotometryRev == nil || *f.PhotometryRev != measure.PhotometryRevision:
			t.Errorf("%s starlight not measured: rev %v", k, f.PhotometryRev)
		}
	}
	n = 0
	for k, f := range got {
		if strings.HasPrefix(k, "DARK/") || strings.HasPrefix(k, "BIAS/") {
			n++
			if f.Type != strings.TrimSuffix(k[:5], "/") {
				t.Errorf("%s indexed as %q", k, f.Type)
			}
		}
	}
	if n != 2 {
		t.Errorf("%d calibration frames indexed, want the new dark and bias only", n)
	}
	if _, ok := got["Stale/LIGHT/unindexed.fits"]; ok {
		t.Error("a folder with no recent lights was listed")
	}
	if f := got["M31/LIGHT/old.fits"]; f.PhotometryRev != nil {
		t.Error("an unchanged frame was indexed again")
	}

	again, err := ix.IndexRecent(context.Background())
	if err != nil || again != 0 {
		t.Errorf("second pass indexed %d (error %v), want 0", again, err)
	}
}

// Targets imaged in the last RecentWindow are read whether Target
// Scheduler stored the date as Unix seconds or .NET ticks.
func TestScheduledTargets(t *testing.T) {
	t.Parallel()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ticks := func(at time.Time) int64 { return at.Unix()*ticksPerSecond + ticksUnixOffset }
	for _, q := range []string{
		`create table target ("Id" integer primary key, name text)`,
		`create table acquiredimage ("Id" integer primary key, "targetId" integer, acquireddate integer)`,
		`insert into target values (1, 'Seconds'), (2, 'Ticks'), (3, 'Old'), (4, 'Old ticks')`,
		fmt.Sprintf(`insert into acquiredimage values (1, 1, %d), (2, 1, %d), (3, 2, %d), (4, 3, %d), (5, 4, %d)`,
			now.Add(-time.Hour).Unix(), now.Add(-2*time.Hour).Unix(), ticks(now.Add(-time.Hour)),
			now.Add(-RecentWindow-time.Hour).Unix(), ticks(now.Add(-RecentWindow-time.Hour))),
	} {
		if err := db.Exec(q).Error; err != nil {
			t.Fatal(err)
		}
	}
	names, err := ScheduledTargets(db)(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(names)
	if want := []string{"Seconds", "Ticks"}; !slices.Equal(names, want) {
		t.Errorf("targets %q, want %q", names, want)
	}
}

// A scan that listed an object before IndexRecent indexed it leaves the
// new row alone rather than indexing it again.
func TestCurrent(t *testing.T) {
	t.Parallel()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&app.Frame{}); err != nil {
		t.Fatal(err)
	}
	const key = "M31/LIGHT/a.fits"
	if err := db.Create(&app.Frame{Key: key, ETag: "e1", Type: lightFrame, LastModified: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	ix := New(nil, "astro", db, 1)
	ctx := context.Background()
	if !ix.current(ctx, minio.ObjectInfo{Key: key, ETag: "e1"}) {
		t.Error("an indexed object at its ETag is not current")
	}
	if ix.current(ctx, minio.ObjectInfo{Key: key, ETag: "e2"}) {
		t.Error("a changed object is current")
	}
	if ix.current(ctx, minio.ObjectInfo{Key: "M31/LIGHT/b.fits", ETag: "e1"}) {
		t.Error("an unindexed object is current")
	}
}

func TestScheduledTargetsWithoutTables(t *testing.T) {
	t.Parallel()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	names, err := ScheduledTargets(db)(context.Background())
	if err != nil || names != nil {
		t.Errorf("got %q, %v; want nothing", names, err)
	}
}
