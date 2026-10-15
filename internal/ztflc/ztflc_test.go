package ztflc_test

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/ztflc"
)

const (
	iiCom    = "ii_com"
	constant = "gaia_dr3_3932502365802434944"
)

func load(t *testing.T, name string) []ztflc.Point {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name+".csv"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	pts, err := ztflc.Parse(f)
	if err != nil {
		t.Fatal(err)
	}
	return pts
}

func TestParse(t *testing.T) {
	t.Parallel()
	pts := load(t, iiCom)
	if len(pts) != 54 {
		t.Fatalf("got %d points, want 54", len(pts))
	}
	bands := map[string]int{}
	for _, p := range pts {
		bands[p.Band]++
	}
	if bands["g"] != 24 || bands["r"] != 30 {
		t.Fatalf("bands %v, want 24 g and 30 r", bands)
	}
	p := pts[0]
	if p.OID != 1572108200003882 || p.Band != "g" || p.CatFlags != 0 || math.Abs(p.MJD-58482.5278472) > 1e-6 || math.Abs(p.Mag-16.2200089) > 1e-6 || math.Abs(p.MagErr-0.0152098397) > 1e-9 || math.Abs(p.RA-189.3088918) > 1e-7 || math.Abs(p.Dec-14.4421537) > 1e-7 {
		t.Fatalf("first point %+v", p)
	}
	if pts := load(t, "blank"); len(pts) != 0 {
		t.Fatalf("blank sky gave %d points", len(pts))
	}
}

func TestParseErrors(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"empty":      "",
		"no columns": "oid,mjd\n1,2\n",
		"bad mag":    "oid,mjd,mag,magerr,catflags,filtercode,ra,dec\n1,58000,x,0.01,0,zr,1,2\n",
		"html":       "<html><body>error</body></html>\n",
	} {
		if _, err := ztflc.Parse(strings.NewReader(body)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestJudgeRecorded(t *testing.T) {
	t.Parallel()
	cases := []struct {
		file     string
		amp      float64
		want     ztflc.Kind
		min, max float64
	}{
		{iiCom, 0.3, ztflc.Variable, 0.9, 0.95},
		{iiCom, -0.5, ztflc.Variable, 0.9, 0.95},
		{iiCom, 1.2, ztflc.Unknown, 0.9, 0.95},
		{"linear_4012679", 0.3, ztflc.Variable, 0.88, 0.92},
		{"css_j123950.4+135404", 0.3, ztflc.Variable, 0.43, 0.47},
		{"css_j123950.4+135404", 0.15, ztflc.Variable, 0.43, 0.47},
		{"ztf_j123725.55+140516.2", 0.3, ztflc.Flat, 0.06, 0.07},
		{constant, 0.3, ztflc.Flat, 0.03, 0.045},
		{constant, 1.0, ztflc.Flat, 0.03, 0.045},
		{constant, 0.1, ztflc.Unknown, 0, 0},
		{"gaia_dr3_3932523836343528448", 0.3, ztflc.Flat, 0.035, 0.045},
		{"gaia_dr3_3932584794814722048", 0.3, ztflc.Flat, 0.055, 0.065},
		{"haro_4-411", 0.3, ztflc.Unknown, 0, 0},
		{"blank", 0.3, ztflc.Unknown, 0, 0},
		{"gaia_dr3_2179309875368546432", 0.13, ztflc.Variable, 0.48, 0.49},
		{"2mass_j21043883+4319143", 0.19, ztflc.Variable, 0.20, 0.21},
		{"2mass_j21043883+4319143", 0.30, ztflc.Unknown, 0.20, 0.21},
		{"2mass_j00433343+4139446", 0.24, ztflc.Variable, 0.53, 0.54},
	}
	for _, c := range cases {
		v := ztflc.Judge(load(t, c.file), c.amp)
		if v.Kind != c.want || v.Amplitude < c.min || v.Amplitude > c.max {
			t.Errorf("%s at %.2f mag: got %s %.3f (%s), want %s in [%.3f, %.3f]", c.file, c.amp, v.Kind, v.Amplitude, v.Reason, c.want, c.min, c.max)
		}
	}
}

func flat(n int, mag, scatter float64) []ztflc.Point {
	pts := make([]ztflc.Point, n)
	for i := range pts {
		pts[i] = ztflc.Point{OID: 1, Band: "r", MJD: 58000 + float64(i), Mag: mag + scatter*math.Sin(float64(i)*2.399), MagErr: scatter / 1.2}
	}
	return pts
}

func TestJudgeSynthetic(t *testing.T) {
	t.Parallel()
	offset := flat(40, 16, 0.02)
	more := flat(40, 16.3, 0.02)
	for i := range more {
		more[i].OID = 2
	}
	offset = append(offset, more...)

	eclipsing := flat(200, 16, 0.02)
	for i := 0; i < 200; i += 25 {
		eclipsing[i].Mag += 0.6
	}

	detached := flat(600, 16, 0.02)
	for i := 0; i < 600; i += 90 {
		detached[i].Mag += 0.3
	}

	strays := flat(600, 16, 0.02)
	for i := 0; i < 600; i += 90 {
		strays[i].Mag += 0.3 * float64(1-2*(i/90%2))
	}

	fewDips := flat(200, 16, 0.02)
	for i := 0; i < 3; i++ {
		fewDips[i*60].Mag += 0.6
	}

	flagged := flat(30, 16, 0.02)
	for i := range 20 {
		flagged = append(flagged, ztflc.Point{OID: 1, Band: "r", MJD: 59000 + float64(i), Mag: 17 + float64(i%2), MagErr: 0.02, CatFlags: 4})
	}

	cases := []struct {
		name string
		pts  []ztflc.Point
		amp  float64
		want ztflc.Kind
	}{
		{"flat", flat(30, 16, 0.02), 0.3, ztflc.Flat},
		{"too few epochs", flat(19, 16, 0.02), 0.3, ztflc.Unknown},
		{"too faint", flat(100, 20.8, 0.02), 0.3, ztflc.Unknown},
		{"too bright", flat(100, 11.5, 0.02), 0.3, ztflc.Unknown},
		{"too noisy for the claim", flat(100, 16, 0.08), 0.2, ztflc.Unknown},
		{"field offsets are not variability", offset, 0.2, ztflc.Flat},
		{"eclipses the percentiles miss", eclipsing, 0.5, ztflc.Variable},
		{"one dip in a hundred, all fainter", detached, 0.3, ztflc.Variable},
		{"one stray in a hundred, either way", strays, 0.3, ztflc.Unknown},
		{"a few dips keep it from flat", fewDips, 0.5, ztflc.Unknown},
		{"flagged epochs ignored", flagged, 0.3, ztflc.Flat},
		{"no points", nil, 0.3, ztflc.Unknown},
	}
	for _, c := range cases {
		if v := ztflc.Judge(c.pts, c.amp); v.Kind != c.want {
			t.Errorf("%s: got %s (%s), want %s", c.name, v.Kind, v.Reason, c.want)
		}
	}
}

type memStore struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (s *memStore) Get(_ context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.m[key]
	if !ok {
		return nil, errors.New("not found")
	}
	return b, nil
}

func (s *memStore) Put(_ context.Context, key string, data []byte, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[key] = data
	return nil
}

func TestChecker(t *testing.T) {
	t.Parallel()
	body, err := os.ReadFile(filepath.Join("testdata", "ii_com.csv"))
	if err != nil {
		t.Fatal(err)
	}
	var hits atomic.Int32
	fail := atomic.Bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		q := r.URL.Query()
		if q.Get("POS") != "CIRCLE 189.308880 14.442170 0.0004167" || q.Get("BANDNAME") != "g,r" || q.Get("BAD_CATFLAGS_MASK") != "32768" || q.Get("FORMAT") != "csv" {
			t.Errorf("query %s", r.URL.RawQuery)
		}
		if fail.Load() {
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	now := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	store := &memStore{m: map[string][]byte{}}
	c := &ztflc.Checker{Client: &ztflc.Client{BaseURL: srv.URL}, Store: store, Now: func() time.Time { return now }}
	ctx := context.Background()

	v, err := c.Check(ctx, 189.30888, 14.44217, 0.4)
	if err != nil || v.Kind != ztflc.Variable {
		t.Fatalf("got %+v, %v", v, err)
	}
	if _, ok := store.m[ztflc.CacheKey(189.30888, 14.44217)]; !ok {
		t.Fatalf("not cached: %v", store.m)
	}
	if v, err := c.Check(ctx, 189.30888, 14.44217, 2); err != nil || v.Kind != ztflc.Unknown || hits.Load() != 1 {
		t.Fatalf("cached check: %+v, %v, %d queries", v, err, hits.Load())
	}

	now = now.Add(31 * 24 * time.Hour)
	fail.Store(true)
	if _, err := c.Check(ctx, 189.30888, 14.44217, 0.4); err == nil || hits.Load() != 2 {
		t.Fatalf("expired cache: %v, %d queries", err, hits.Load())
	}
	fail.Store(false)
	if _, err := c.Check(ctx, 189.30888, 14.44217, 0.4); !errors.Is(err, ztflc.ErrDown) || hits.Load() != 2 {
		t.Fatalf("after a failure: %v, %d queries", err, hits.Load())
	}
}

func TestCheckerTimeout(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	c := &ztflc.Checker{Client: &ztflc.Client{BaseURL: srv.URL}, Timeout: 50 * time.Millisecond}
	if _, err := c.Check(context.Background(), 10, 20, 0.5); err == nil || errors.Is(err, ztflc.ErrDown) {
		t.Fatalf("got %v, want a timeout", err)
	}
	if _, err := c.Check(context.Background(), 11, 20, 0.5); !errors.Is(err, ztflc.ErrDown) {
		t.Fatalf("got %v, want ErrDown", err)
	}
}

func TestCacheKey(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		ra, dec float64
		want    string
	}{
		{189.30888, 14.44217, "discoveries/cache/ztf/189.3089+14.4422.json.zst"},
		{84.19832, -5.60868, "discoveries/cache/ztf/84.1983-5.6087.json.zst"},
		{-0.5, 1, "discoveries/cache/ztf/359.5000+1.0000.json.zst"},
	} {
		if got := ztflc.CacheKey(c.ra, c.dec); got != c.want {
			t.Errorf("CacheKey(%v, %v) = %s, want %s", c.ra, c.dec, got, c.want)
		}
	}
}
