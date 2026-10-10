package skycutout_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/skycutout"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func jpeg() []byte { return []byte{0xFF, 0xD8, 0xFF, 0xE0, 1, 2, 3} }

func openDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&app.SkyCutout{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestParseValidatesAndQuantises(t *testing.T) {
	t.Parallel()
	p, err := skycutout.Parse("-10.0014", "41.26912", "2.5", "-30", "5000", "10")
	if err != nil {
		t.Fatal(err)
	}
	if p.RA != 349.999 || p.Dec != 41.269 || p.FOV != 2.5 || p.Rotation != 330 || p.Width != skycutout.MaxSize || p.Height != skycutout.MinSize {
		t.Errorf("params %+v", p)
	}
	q, _ := skycutout.Parse("349.9994", "41.2691", "2.5", "330", "1024", "64")
	if p.Key() != q.Key() {
		t.Errorf("keys differ: %s %s", p.Key(), q.Key())
	}
	d, _ := skycutout.Parse("10", "0", "1", "", "", "")
	if d.Width != skycutout.DefaultWidth || d.Height != skycutout.DefaultHeight || d.Rotation != 0 {
		t.Errorf("defaults %+v", d)
	}
	for _, bad := range [][3]string{{"", "1", "1"}, {"x", "1", "1"}, {"1", "91", "1"}, {"1", "1", "0"}, {"1", "1", "40"}, {"1", "1", "NaN"}} {
		if _, err := skycutout.Parse(bad[0], bad[1], bad[2], "", "", ""); !errors.Is(err, skycutout.ErrBadRequest) {
			t.Errorf("%v accepted: %v", bad, err)
		}
	}
}

func TestGetCachesRateLimitsAndFailsGracefully(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	var lastQuery atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		lastQuery.Store(r.URL.RawQuery)
		if r.UserAgent() != skycutout.UserAgent {
			http.Error(w, "no agent", http.StatusForbidden)
			return
		}
		if r.URL.Query().Get("dec") == "-45.000" {
			_, _ = w.Write([]byte("<html>oops</html>"))
			return
		}
		_, _ = w.Write(jpeg())
	}))
	defer srv.Close()
	now := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	s := &skycutout.Service{DB: openDB(t), Endpoint: srv.URL, PerMinute: 2, MaxEntries: 2, Now: func() time.Time { return now }}
	ctx := context.Background()
	p, _ := skycutout.Parse("83.82", "-5.39", "1.5", "30", "300", "200")
	c, err := s.Get(ctx, p)
	if err != nil || string(c.Data) != string(jpeg()) || c.ETag == "" || hits.Load() != 1 {
		t.Fatalf("first %v %v hits %d", c, err, hits.Load())
	}
	q, _ := lastQuery.Load().(string)
	if !strings.Contains(q, "rotation_angle=-30.0") || !strings.Contains(q, "hips=CDS%2FP%2FDSS2%2Fcolor") || !strings.Contains(q, "format=jpg") {
		t.Errorf("query %s", q)
	}
	again, err := s.Get(ctx, p)
	if err != nil || again.ETag != c.ETag || hits.Load() != 1 {
		t.Fatalf("cache missed: hits %d %v", hits.Load(), err)
	}
	bad, _ := skycutout.Parse("10", "-45", "1", "", "", "")
	if _, err := s.Get(ctx, bad); !errors.Is(err, skycutout.ErrUpstream) {
		t.Errorf("non-JPEG accepted: %v", err)
	}
	third, _ := skycutout.Parse("20", "10", "1", "", "", "")
	_, err = s.Get(ctx, third)
	var limited *skycutout.RateLimitError
	if !errors.As(err, &limited) || limited.RetryAfter <= 0 || hits.Load() != 2 {
		t.Fatalf("not rate limited: %v hits %d", err, hits.Load())
	}
	if _, err := s.Get(ctx, p); err != nil {
		t.Errorf("cached cutout limited: %v", err)
	}
	now = now.Add(2 * time.Minute)
	for i, ra := range []string{"20", "30", "40"} {
		x, _ := skycutout.Parse(ra, "10", "1", "", "", "")
		if _, err := s.Get(ctx, x); err != nil && i < 2 {
			t.Fatalf("after the window %s: %v", ra, err)
		}
		now = now.Add(time.Minute)
	}
	var n int64
	s.DB.Model(&app.SkyCutout{}).Count(&n)
	if n != 2 {
		t.Errorf("%d cutouts kept, want 2", n)
	}
	if _, err := (&skycutout.Service{Off: true}).Get(ctx, p); !errors.Is(err, skycutout.ErrOff) {
		t.Errorf("off %v", err)
	}
}
