package tnsmirror_test

import (
	"archive/zip"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/starcat"
	"github.com/USA-RedDragon/astro-stacker/internal/tnsmirror"
)

const (
	testPrefix = "catalogs/tns"
	testMarker = `tns_marker{"tns_id":42,"type":"bot","name":"testbot"}`
	testKey    = "test-key"
)

func zipOf(t *testing.T, name string, data []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	w, err := z.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fakeTNS serves the fixture files as TNS does, only to a POST with the
// bot's marker and key.
type fakeTNS struct {
	mu    sync.Mutex
	files map[string][]byte
	hits  map[string]int
	limit int
}

func (f *fakeTNS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := strings.TrimPrefix(r.URL.Path, "/")
	if f.hits == nil {
		f.hits = map[string]int{}
	}
	f.hits[name]++
	if r.Method != http.MethodPost || r.Header.Get("User-Agent") != testMarker || r.PostFormValue("api_key") != testKey {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if f.limit > 0 {
		f.limit--
		w.Header().Set("x-rate-limit-reset", "0")
		w.WriteHeader(http.StatusTooManyRequests)
		return
	}
	b, ok := f.files[name]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("x-rate-limit-remaining", "10")
	_, _ = w.Write(b)
}

func (f *fakeTNS) count(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[name]
}

func newFake(t *testing.T) (*fakeTNS, *tnsmirror.Client) {
	t.Helper()
	f := &fakeTNS{files: map[string][]byte{
		tnsmirror.FullFile:     zipOf(t, "tns_public_objects.csv", fixture(t, "tns_public_objects.csv")),
		tnsmirror.HourFile(5):  zipOf(t, "tns_public_objects_05.csv", fixture(t, "tns_public_objects_05.csv")),
		tnsmirror.HourFile(1):  zipOf(t, "tns_public_objects_01.csv", []byte("\"2026-10-14 00:00:00 - 01:00:00\"\n")),
		tnsmirror.HourFile(2):  zipOf(t, "tns_public_objects_02.csv", []byte("\"2026-10-13 01:00:00 - 02:00:00\"\n")),
		tnsmirror.HourFile(23): zipOf(t, "tns_public_objects_23.csv", []byte("\"2026-10-13 22:00:00 - 23:00:00\"\n")),
	}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, &tnsmirror.Client{Bot: tnsmirror.Bot{ID: "42", Name: "testbot", Key: testKey}, BaseURL: srv.URL, Gap: time.Millisecond}
}

func TestParseFixture(t *testing.T) {
	t.Parallel()
	f, err := tnsmirror.Parse(bytes.NewReader(fixture(t, "tns_public_objects.csv")))
	if err != nil {
		t.Fatal(err)
	}
	if !f.From.Equal(time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)) || len(f.Objects) != 3 {
		t.Fatalf("stamp %v, %d objects", f.From, len(f.Objects))
	}
	o := f.Objects[0]
	if o.FullName() != "AT 2026acsi" || o.Group() != "XOSS" || o.DiscoveryBand != "Clear-" || o.DiscoveryMag == nil || *o.DiscoveryMag != 17.4 {
		t.Errorf("AT 2026acsi read as %+v", o)
	}
	if want := time.Date(2026, 9, 22, 15, 50, 24, 0, time.UTC); !o.Discovered.Equal(want) {
		t.Errorf("discovered %v, want %v", o.Discovered, want)
	}
	if sn := f.Objects[1]; sn.Type != "SN Ia" || sn.Redshift == nil || *sn.Redshift != 0.0016 || sn.FullName() != "SN 2026xyz" {
		t.Errorf("SN 2026xyz read as %+v", sn)
	}
	if sx := f.Objects[2]; starcat.Separation(sx.RA, sx.Dec, 83.633083, 22.0145) > 0.1/3600 || sx.Group() != "C. Someone" {
		t.Errorf("sexagesimal row read as %.6f %+.6f, group %q", sx.RA, sx.Dec, sx.Group())
	}
	d, err := tnsmirror.Parse(bytes.NewReader(fixture(t, "tns_public_objects_05.csv")))
	if err != nil {
		t.Fatal(err)
	}
	if !d.From.Equal(time.Date(2026, 10, 14, 4, 0, 0, 0, time.UTC)) || !d.To.Equal(time.Date(2026, 10, 14, 5, 0, 0, 0, time.UTC)) {
		t.Errorf("hourly span %v to %v", d.From, d.To)
	}
	mid, err := tnsmirror.Parse(strings.NewReader("\"2025-01-17 23:00:00 - 00:00:00\"\n"))
	if err != nil || !mid.To.Equal(time.Date(2025, 1, 18, 0, 0, 0, 0, time.UTC)) || len(mid.Objects) != 0 {
		t.Errorf("midnight hourly file: %v, %v", mid.To, err)
	}
}

func TestParseByHeaderName(t *testing.T) {
	t.Parallel()
	in := "\"2026-10-14 00:00:00\"\n\"lastmodified\",\"declination\",\"ra\",\"name\",\"name_prefix\",\"objid\",\"discoverydate\"\n\"x\",\"-10.5\",\"200.25\",\"2026b\",\"AT\",\"7\",\"2026-09-22.660\"\n"
	f, err := tnsmirror.Parse(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	o := f.Objects[0]
	if o.RA != 200.25 || o.Dec != -10.5 || o.FullName() != "AT 2026b" || !o.Discovered.Equal(time.Date(2026, 9, 22, 15, 50, 24, 0, time.UTC)) {
		t.Errorf("reordered columns read as %+v", o)
	}
	if _, err := tnsmirror.Parse(strings.NewReader("\"2026-10-14 00:00:00\"\n\"objid\",\"name\"\n\"1\",\"x\"\n")); err == nil {
		t.Error("a file without ra and declination parsed")
	}
	bad := "\"2026-10-14 00:00:00\"\n\"objid\",\"name_prefix\",\"name\",\"ra\",\"declination\",\"discoverydate\"\n" +
		"\"1\",\"AT\",\"2026a\",\"10\",\"41\",\"2026-09-22 15:50:24\"\n\"2\",\"AT\",\"2026b\",\"x\",\"41\",\"\"\n"
	if f, err := tnsmirror.Parse(strings.NewReader(bad)); err != nil || len(f.Objects) != 1 || f.Skipped != 1 {
		t.Errorf("one bad row: %d objects, %d skipped, %v", len(f.Objects), f.Skipped, err)
	}
	if _, err := tnsmirror.Parse(strings.NewReader("<html>login</html>\n")); err == nil {
		t.Error("an HTML page parsed as a TNS file")
	}
}

func TestMarker(t *testing.T) {
	t.Parallel()
	if got := (tnsmirror.Bot{ID: "42", Name: "testbot"}).Marker(); got != testMarker {
		t.Errorf("marker %s", got)
	}
}

func TestMirrorFullHourlyAndSwitch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fake, cl := newFake(t)
	b := &starcat.MemBucket{}
	now := time.Date(2026, 10, 14, 1, 20, 0, 0, time.UTC)
	m := &tnsmirror.Mirror{Bucket: b, Prefix: testPrefix, Client: cl, Now: func() time.Time { return now }}
	if err := m.Load(ctx); err != nil || m.Status().Live {
		t.Fatalf("empty bucket: live %v, %v", m.Status().Live, err)
	}
	switched, err := m.Full(ctx)
	if err != nil || !switched {
		t.Fatalf("full: %v %v", switched, err)
	}
	if hits := m.Index().Near(10.557966104327647, 41.27670411346332, 3); len(hits) != 1 || hits[0].FullName() != "AT 2026acsi" || hits[0].SepArcsec > 1 {
		t.Fatalf("M 31 search: %+v", hits)
	}
	if again, err := m.Full(ctx); err != nil || again {
		t.Errorf("the same day's list switched again: %v %v", again, err)
	}
	if ok, err := m.Hour(ctx, time.Date(2026, 10, 14, 2, 0, 0, 0, time.UTC)); err != nil || ok {
		t.Errorf("a day-old hourly file was applied: %v %v", ok, err)
	}
	if ok, err := m.Hour(ctx, time.Date(2026, 10, 14, 5, 0, 0, 0, time.UTC)); err != nil || !ok {
		t.Fatalf("hourly: %v %v", ok, err)
	}
	if hits := m.Index().Near(10.557991667, 41.276797222, 3); len(hits) != 1 || hits[0].Type != "Nova" {
		t.Errorf("the hourly file did not replace AT 2026acsi: %+v", hits)
	}
	if hits := m.Index().Near(150.1, 2.2, 3); len(hits) != 1 {
		t.Errorf("the hourly file's new object is missing: %+v", hits)
	}
	if st := m.Status(); st.Objects != 4 || st.Version != "20261014" || !st.AsOf.Equal(time.Date(2026, 10, 14, 5, 0, 0, 0, time.UTC)) {
		t.Errorf("status %+v", st)
	}
	keys, err := b.List(ctx, "catalogs/tns/")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"catalogs/tns/current.json.zst", "catalogs/tns/20261014/objects.csv.zst", "catalogs/tns/20261014/manifest.json.zst",
		"catalogs/tns/20261014/deltas/2026101405.csv.zst"} {
		if !containsKey(keys, want) {
			t.Errorf("%s not stored; have %v", want, keys)
		}
	}
	raw, err := b.Get(ctx, "catalogs/tns/20261014/objects.csv.zst")
	if err != nil || !bytes.HasPrefix(raw, []byte{0x28, 0xb5, 0x2f, 0xfd}) {
		t.Errorf("the list is not stored zstd-compressed: %v", err)
	}
	fresh := &tnsmirror.Mirror{Bucket: b, Prefix: testPrefix, Client: cl}
	if err := fresh.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if st := fresh.Status(); st.Objects != 4 || !st.AsOf.Equal(m.Status().AsOf) {
		t.Errorf("reloaded status %+v", st)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.hits[tnsmirror.FullFile] != 2 {
		t.Errorf("full list fetched %d times", fake.hits[tnsmirror.FullFile])
	}
}

func containsKey(keys []string, k string) bool {
	for _, x := range keys {
		if x == k {
			return true
		}
	}
	return false
}

func TestMirrorKeepsThirtyDays(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, cl := newFake(t)
	b := &starcat.MemBucket{}
	for _, k := range []string{"catalogs/tns/20260901/objects.csv.zst", "catalogs/tns/20260901/manifest.json.zst", "catalogs/tns/20260920/objects.csv.zst", "catalogs/tns/other/x"} {
		if err := b.Put(ctx, k, []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	m := &tnsmirror.Mirror{Bucket: b, Prefix: testPrefix, Client: cl, Now: func() time.Time { return time.Date(2026, 10, 14, 1, 20, 0, 0, time.UTC) }}
	if _, err := m.Full(ctx); err != nil {
		t.Fatal(err)
	}
	keys, _ := b.List(ctx, "catalogs/tns/")
	if containsKey(keys, "catalogs/tns/20260901/objects.csv.zst") || containsKey(keys, "catalogs/tns/20260901/manifest.json.zst") {
		t.Errorf("a 43-day-old version was kept: %v", keys)
	}
	if !containsKey(keys, "catalogs/tns/20260920/objects.csv.zst") || !containsKey(keys, "catalogs/tns/other/x") {
		t.Errorf("a recent version or another file was deleted: %v", keys)
	}
}

func TestClientWaitsOutRateLimit(t *testing.T) {
	t.Parallel()
	fake, cl := newFake(t)
	fake.limit = 1
	if _, err := cl.Fetch(context.Background(), tnsmirror.FullFile); err != nil {
		t.Fatal(err)
	}
	fake.limit = 5
	if _, err := cl.Fetch(context.Background(), tnsmirror.FullFile); err == nil {
		t.Error("a lasting 429 was not reported")
	}
	if _, err := (&tnsmirror.Client{BaseURL: "http://127.0.0.1:1"}).Fetch(context.Background(), tnsmirror.FullFile); err == nil {
		t.Error("a fetch without credentials ran")
	}
}

func TestBadCredentialsAreRefused(t *testing.T) {
	t.Parallel()
	_, cl := newFake(t)
	cl.Bot.Key = "wrong"
	_, err := cl.Fetch(context.Background(), tnsmirror.FullFile)
	if err == nil || strings.Contains(err.Error(), "wrong") {
		t.Errorf("error %v", err)
	}
}

func TestMirrorSchedule(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fake, cl := newFake(t)
	now := time.Date(2026, 10, 14, 1, 20, 0, 0, time.UTC)
	m := &tnsmirror.Mirror{Bucket: &starcat.MemBucket{}, Prefix: testPrefix, Client: cl, Now: func() time.Time { return now }}
	m.Step(ctx)
	if !m.Status().Live {
		t.Fatal("the first step did not fetch the full list")
	}
	m.Step(ctx)
	if st := m.Status(); !st.AsOf.Equal(time.Date(2026, 10, 14, 1, 0, 0, 0, time.UTC)) {
		t.Fatalf("the empty 01 h file did not advance the mirror: %+v", st)
	}
	next := m.Step(ctx)
	if want := time.Date(2026, 10, 14, 2, 7, 0, 0, time.UTC); !next.Equal(want) {
		t.Errorf("next look at %v, want %v", next, want)
	}
	now = time.Date(2026, 10, 14, 2, 8, 0, 0, time.UTC)
	m.Step(ctx)
	if st := m.Status(); !st.AsOf.Equal(time.Date(2026, 10, 14, 1, 0, 0, 0, time.UTC)) {
		t.Errorf("a stale 02 h file advanced the mirror: %+v", st)
	}
	if next := m.Step(ctx); !next.Equal(time.Date(2026, 10, 14, 2, 28, 0, 0, time.UTC)) {
		t.Errorf("stale file retry at %v", next)
	}
	fake.mu.Lock()
	full := fake.hits[tnsmirror.FullFile]
	fake.mu.Unlock()
	if full != 1 {
		t.Errorf("full list fetched %d times", full)
	}
}

func TestMirrorStopsWhenTNSRefusesTheKey(t *testing.T) {
	t.Parallel()
	fake, cl := newFake(t)
	cl.Bot.Key = "revoked"
	m := &tnsmirror.Mirror{Bucket: &starcat.MemBucket{}, Prefix: testPrefix, Client: cl, Now: func() time.Time { return time.Date(2026, 10, 14, 1, 20, 0, 0, time.UTC) }}
	if next := m.Step(context.Background()); !next.IsZero() {
		t.Errorf("a refused key is tried again at %v", next)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.hits[tnsmirror.FullFile] != 1 {
		t.Errorf("%d requests", fake.hits[tnsmirror.FullFile])
	}
}

func TestStaleFullListBacksOff(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fake, cl := newFake(t)
	now := time.Date(2026, 10, 15, 1, 20, 0, 0, time.UTC)
	m := &tnsmirror.Mirror{Bucket: &starcat.MemBucket{}, Prefix: testPrefix, Client: cl, Now: func() time.Time { return now }}
	m.Step(ctx)
	if !m.Status().Live {
		t.Fatal("no first list")
	}
	var at []time.Time
	for len(at) < 6 && now.Before(time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)) {
		before := fake.count(tnsmirror.FullFile)
		next := m.Step(ctx)
		if next.IsZero() {
			t.Fatal("stopped")
		}
		if fake.count(tnsmirror.FullFile) > before {
			at = append(at, now)
		}
		if next.After(now) {
			now = next
		}
	}
	var gaps []time.Duration
	for i := 1; i < len(at); i++ {
		gaps = append(gaps, at[i].Sub(at[i-1]))
	}
	t.Logf("waits between full-list tries: %v", gaps)
	if len(gaps) < 4 || gaps[0] < 30*time.Minute || gaps[len(gaps)-1] < 4*time.Hour {
		t.Errorf("waits between tries at a stale list: %v", gaps)
	}
}
