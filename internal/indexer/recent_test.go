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
// the prefixes listed.
type bucketServer struct {
	mu       sync.Mutex
	objs     map[string][]byte
	prefixes []string
}

func (b *bucketServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	key := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/astro"), "/")
	if key == "" && r.URL.Query().Has("list-type") {
		prefix := r.URL.Query().Get("prefix")
		b.prefixes = append(b.prefixes, prefix)
		res := listResult{Name: "astro", Prefix: prefix, MaxKeys: 1000}
		for k, v := range b.objs {
			if strings.HasPrefix(k, prefix) {
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
		imagedata.StringCard("IMAGETYP", "LIGHT", ""),
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
// Target Scheduler imaged lately, indexes what is new there, measures its
// starlight and wakes the loops waiting on new frames.
func TestIndexRecent(t *testing.T) {
	t.Parallel()
	old := fitsLight(t, "M31")
	bucket := &bucketServer{objs: map[string][]byte{
		"M31/LIGHT/old.fits":         old,
		"M31/LIGHT/new.fits":         fitsLight(t, "M31"),
		"M31/LIGHT/notes.txt":        []byte("x"),
		"NGC 7000/LIGHT/first.fits":  fitsLight(t, "NGC 7000"),
		"Stale/LIGHT/unindexed.fits": fitsLight(t, "Stale"),
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
	if n != 2 {
		t.Errorf("indexed %d, want 2: new.fits and first.fits", n)
	}
	select {
	case <-woke:
	default:
		t.Error("Wake not notified")
	}
	bucket.mu.Lock()
	listed := slices.Sorted(slices.Values(bucket.prefixes))
	bucket.mu.Unlock()
	if want := []string{"M31/", "NGC 7000/"}; !slices.Equal(listed, want) {
		t.Errorf("listed %q, want %q", listed, want)
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

func TestNinaFolder(t *testing.T) {
	cases := map[string]string{
		"161P/Hartley-IRAS": "161P-Hartley-IRAS",
		"C/2024 T5 (ATLAS)": "C-2024 T5 (ATLAS)",
		"M 31":              "M 31",
		`a:b*c?d"e<f>g|h\i`: "a-b-c-d-e-f-g-h-i",
	}
	for in, want := range cases {
		if got := ninaFolder(in); got != want {
			t.Errorf("ninaFolder(%q) = %q, want %q", in, got, want)
		}
	}
}
