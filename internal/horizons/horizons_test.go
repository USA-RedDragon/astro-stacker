package horizons_test

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/horizons"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/testdb"
	"gorm.io/gorm"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func result(t *testing.T, name string) string {
	t.Helper()
	var r struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal([]byte(fixture(t, name)), &r); err != nil {
		t.Fatal(err)
	}
	return r.Result
}

func near(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

func TestParseCometFixture(t *testing.T) {
	t.Parallel()
	tab, err := horizons.Parse(result(t, "comet-c2025r2-10m.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(tab.Rows) != 73 {
		t.Fatalf("%d rows, want 73", len(tab.Rows))
	}
	r := tab.Rows[1]
	if !r.At.Equal(time.Date(2026, 10, 13, 0, 10, 0, 0, time.UTC)) {
		t.Errorf("at %v", r.At)
	}
	if !near(r.RA, 76.194753110, 1e-9) || !near(r.Dec, 27.863643572, 1e-9) {
		t.Errorf("ICRF %v %v", r.RA, r.Dec)
	}
	if r.RAApp == nil || !near(*r.RAApp, 76.620159197, 1e-9) || r.DecApp == nil || !near(*r.DecApp, 27.900926101, 1e-9) {
		t.Errorf("apparent %v %v", r.RAApp, r.DecApp)
	}
	if !near(r.RARate, -13.3833, 1e-9) || !near(r.DecRate, 0.838615, 1e-9) {
		t.Errorf("rates %v %v", r.RARate, r.DecRate)
	}
	if r.Mag == nil || !near(*r.Mag, 24.298, 1e-9) {
		t.Errorf("mag %v", r.Mag)
	}
	if r.Delta == nil || !near(*r.Delta, 4.83612163692311, 1e-12) {
		t.Errorf("delta %v", r.Delta)
	}
	if r.UncRA == nil || !near(*r.UncRA, 2.864, 1e-9) || r.UncDec == nil || !near(*r.UncDec, 0.274, 1e-9) {
		t.Errorf("uncertainty %v %v", r.UncRA, r.UncDec)
	}
	if d, ok := tab.MinDelta(); !ok || d < 4.8 || d > 4.84 {
		t.Errorf("min delta %v %v", d, ok)
	}
}

func TestParseMoonFixtureMapsColumnsByName(t *testing.T) {
	t.Parallel()
	tab, err := horizons.Parse(result(t, "moon-10m.json"))
	if err != nil {
		t.Fatal(err)
	}
	r := tab.Rows[0]
	if !near(r.RA, 222.049164998, 1e-9) || !near(r.RARate, 1426.892, 1e-9) || !near(r.DecRate, -333.387, 1e-9) {
		t.Errorf("row %+v", r)
	}
	if r.Mag == nil || !near(*r.Mag, -6.552, 1e-9) {
		t.Errorf("APmag %v", r.Mag)
	}
	if r.UncRA != nil || r.UncDec != nil {
		t.Errorf("n.a. uncertainty parsed as %v %v", r.UncRA, r.UncDec)
	}
	if d, ok := tab.MinDelta(); !ok || d > 0.1 {
		t.Errorf("moon min delta %v %v", d, ok)
	}
}

func TestParseErrors(t *testing.T) {
	t.Parallel()
	if _, err := horizons.Parse(result(t, "nomatch.json")); !errors.Is(err, horizons.ErrUnknownBody) {
		t.Errorf("no match: %v", err)
	}
	if _, err := horizons.Parse(result(t, "ambiguous.json")); !errors.Is(err, horizons.ErrAmbiguous) {
		t.Errorf("ambiguous: %v", err)
	}
	if _, err := horizons.Parse("nothing here"); !errors.Is(err, horizons.ErrNoRows) {
		t.Errorf("empty: %v", err)
	}
}

const enckeSelector = "DES=2P;CAP;NOFRAG"

func TestCommand(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"C/2025 R2":    "DES=C/2025 R2;CAP;NOFRAG",
		" 12P ":        "DES=12P;CAP;NOFRAG",
		enckeSelector:  enckeSelector,
		"DES=1000351;": "DES=1000351;",
		"":             "",
	} {
		if got := horizons.Command(in); got != want {
			t.Errorf("Command(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCommandFor(t *testing.T) {
	t.Parallel()
	ast := horizons.KindAsteroid
	for in, want := range map[[2]string]string{
		{"1", ast}:             "1;",
		{" 0004 ", ast}:        "4;",
		{"2026 TC6", ast}:      "DES=2026 TC6;",
		{"2000  SG344", ast}:   "DES=2000 SG344;",
		{"DES=433;", ast}:      "DES=433;",
		{"0", ast}:             "DES=0;",
		{"C/2025 R2", "comet"}: "DES=C/2025 R2;CAP;NOFRAG",
		{"12P", ""}:            "DES=12P;CAP;NOFRAG",
		{"1", "comet"}:         "DES=1;CAP;NOFRAG",
		{enckeSelector, ast}:   enckeSelector,
		{"", ast}:              "",
	} {
		if got := horizons.CommandFor(in[0], in[1]); got != want {
			t.Errorf("CommandFor(%q, %q) = %q, want %q", in[0], in[1], got, want)
		}
	}
}

func TestParseAsteroidFixtures(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		file, name     string
		ra, dec, mag   float64
		delta, uncRA   float64
		rows           int
		nearEarthTable bool
	}{
		{"asteroid-ceres-10m.json", "1 Ceres (A801 AA)", 110.404856724, 23.422443551, 8.526, 2.45914598430954, 0, 73, false},
		{"asteroid-vesta-10m.json", "4 Vesta (A807 FA)", 22.591356055, -3.337871234, 6.391, 1.48242183208473, 0.023, 73, false},
		{"asteroid-2026tc6-10m.json", "(2026 TC6)", 328.783274073, -5.043807588, 20.019, 0.02561149448120, 8.621, 73, true},
	} {
		raw := result(t, c.file)
		if !strings.Contains(raw, "Target body name: "+c.name) {
			t.Errorf("%s is not of %s", c.file, c.name)
		}
		tab, err := horizons.Parse(raw)
		if err != nil {
			t.Fatalf("%s: %v", c.file, err)
		}
		if len(tab.Rows) != c.rows {
			t.Errorf("%s: %d rows", c.file, len(tab.Rows))
		}
		r := tab.Rows[0]
		if !r.At.Equal(time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)) || !near(r.RA, c.ra, 1e-9) || !near(r.Dec, c.dec, 1e-9) {
			t.Errorf("%s: row %+v", c.file, r)
		}
		if r.Mag == nil || !near(*r.Mag, c.mag, 1e-9) {
			t.Errorf("%s: APmag %v", c.file, r.Mag)
		}
		if r.Delta == nil || !near(*r.Delta, c.delta, 1e-12) {
			t.Errorf("%s: delta %v", c.file, r.Delta)
		}
		if r.UncRA == nil || !near(*r.UncRA, c.uncRA, 1e-9) {
			t.Errorf("%s: RA uncertainty %v", c.file, r.UncRA)
		}
		if d, ok := tab.MinDelta(); !ok || (d < 0.1) != c.nearEarthTable {
			t.Errorf("%s: min delta %v %v", c.file, d, ok)
		}
	}
}

func TestEphemerisSendsAsteroidCommands(t *testing.T) {
	t.Parallel()
	files := map[string]string{"'1;'": "asteroid-ceres-10m.json", "'4;'": "asteroid-vesta-10m.json", "'DES=2026 TC6;'": "asteroid-2026tc6-10m.json"}
	var mu sync.Mutex
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cmd := r.URL.Query().Get("COMMAND")
		mu.Lock()
		got = append(got, cmd)
		mu.Unlock()
		name, ok := files[cmd]
		if !ok {
			_, _ = w.Write([]byte(fixture(t, "nomatch.json")))
			return
		}
		_, _ = w.Write([]byte(fixture(t, name)))
	}))
	defer srv.Close()
	c := &horizons.Client{BaseURL: srv.URL, UserAgent: "test"}
	e := 200.0
	site := sky.Site{Latitude: 32, Longitude: -97, Elevation: &e}
	start := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	for _, des := range []string{"1", "4", "2026 TC6"} {
		tab, err := c.Ephemeris(context.Background(), horizons.Request{Command: horizons.CommandFor(des, horizons.KindAsteroid), Site: site, Start: start, Stop: start.Add(12 * time.Hour), Step: 10 * time.Minute})
		if err != nil || len(tab.Rows) != 73 {
			t.Errorf("%s: %d rows, %v", des, len(tab.Rows), err)
		}
	}
	if _, err := c.Ephemeris(context.Background(), horizons.Request{Command: horizons.Command("1"), Site: site, Start: start, Stop: start.Add(12 * time.Hour), Step: 10 * time.Minute}); !errors.Is(err, horizons.ErrUnknownBody) {
		t.Errorf("the comet form of an asteroid number: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(got, ",") != "'1;','4;','DES=2026 TC6;','DES=1;CAP;NOFRAG'" {
		t.Errorf("commands %v", got)
	}
}

func cacheDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := testdb.SQLite(t, "")
	if err := db.AutoMigrate(&app.HorizonsCache{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func site() sky.Site {
	e := 200.0
	return sky.Site{Latitude: 32, Longitude: -97, Elevation: &e}
}

func request() horizons.Request {
	return horizons.Request{Command: horizons.Command("C/2025 R2"), Site: site(),
		Start: time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC), Stop: time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC), Step: 10 * time.Minute}
}

func TestEphemerisCachedPerTargetNight(t *testing.T) {
	t.Parallel()
	body := fixture(t, "comet-c2025r2-10m.json")
	var hits atomic.Int32
	var agent, query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		agent, query = r.Header.Get("User-Agent"), r.URL.RawQuery
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	db := cacheDB(t)
	c := &horizons.Client{BaseURL: srv.URL, UserAgent: horizons.UserAgent("v1.2.3"), Cache: db}
	ctx := context.Background()
	first, err := c.Ephemeris(ctx, request())
	if err != nil {
		t.Fatal(err)
	}
	if first.Cached || len(first.Rows) != 73 {
		t.Fatalf("first fetch cached=%v rows=%d", first.Cached, len(first.Rows))
	}
	if agent != "astro-stacker/v1.2.3 (jacob@mcswain.dev)" {
		t.Errorf("User-Agent %q", agent)
	}
	for _, want := range []string{"CENTER=%27coord%40399%27", "QUANTITIES=%271%2C2%2C3%2C9%2C20%2C27%2C36%27", "ANG_FORMAT=%27DEG%27", "EXTRA_PREC=%27YES%27", "STEP_SIZE=%2710+m%27", "EPHEM_TYPE=%27OBSERVER%27"} {
		if !strings.Contains(query, want) {
			t.Errorf("query lacks %s: %s", want, query)
		}
	}
	again, err := c.Ephemeris(ctx, request())
	if err != nil || !again.Cached || len(again.Rows) != 73 {
		t.Fatalf("second fetch cached=%v rows=%d err=%v", again.Cached, len(again.Rows), err)
	}
	restarted := &horizons.Client{BaseURL: srv.URL, Cache: db}
	if tab, err := restarted.Ephemeris(ctx, request()); err != nil || !tab.Cached {
		t.Fatalf("after a restart cached=%v err=%v", tab.Cached, err)
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("%d requests for one target-night, want 1", n)
	}
	other := request()
	other.Start, other.Stop = other.Start.Add(24*time.Hour), other.Stop.Add(24*time.Hour)
	if _, err := c.Ephemeris(ctx, other); err != nil {
		t.Fatal(err)
	}
	if n := hits.Load(); n != 2 {
		t.Fatalf("%d requests after another night, want 2", n)
	}
	if n, err := c.Prune(ctx, time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)); err != nil || n != 1 {
		t.Fatalf("pruned %d, %v", n, err)
	}
}

func TestPruneEarlierDropsOnlySupersededTablesOfTheSameCommandAndStep(t *testing.T) {
	t.Parallel()
	body := fixture(t, "comet-c2025r2-10m.json")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
	defer srv.Close()
	db := cacheDB(t)
	c := &horizons.Client{BaseURL: srv.URL, Cache: db}
	ctx := context.Background()
	day := func(r horizons.Request, d int) horizons.Request {
		r.Start, r.Stop = r.Start.AddDate(0, 0, d), r.Stop.AddDate(0, 0, d)
		return r
	}
	other := request()
	other.Command = horizons.Command("2P")
	coarse := request()
	coarse.Step = time.Hour
	for _, r := range []horizons.Request{request(), day(request(), 1), day(request(), 2), day(other, -1), day(coarse, -1)} {
		if _, err := c.Ephemeris(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := c.PruneEarlier(ctx, day(request(), 2)); err != nil || n != 2 {
		t.Fatalf("pruned %d, %v", n, err)
	}
	var left int64
	if err := db.Model(&app.HorizonsCache{}).Count(&left).Error; err != nil || left != 3 {
		t.Fatalf("%d tables left, %v", left, err)
	}
	if n, err := (&horizons.Client{}).PruneEarlier(ctx, request()); err != nil || n != 0 {
		t.Fatalf("no cache: %d, %v", n, err)
	}
}

func TestBacksOffOn503AndSerializes(t *testing.T) {
	t.Parallel()
	body := fixture(t, "comet-c2025r2-10m.json")
	var calls, inFlight, maxInFlight atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := inFlight.Add(1)
		defer inFlight.Add(-1)
		for {
			m := maxInFlight.Load()
			if n <= m || maxInFlight.CompareAndSwap(m, n) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		if calls.Add(1)%3 != 0 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	c := &horizons.Client{BaseURL: srv.URL, Backoff: time.Millisecond}
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Go(func() {
			r := request()
			r.Start = r.Start.Add(time.Duration(i) * 24 * time.Hour)
			r.Stop = r.Stop.Add(time.Duration(i) * 24 * time.Hour)
			_, errs[i] = c.Ephemeris(context.Background(), r)
		})
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("request %d: %v", i, err)
		}
	}
	if maxInFlight.Load() != 1 {
		t.Errorf("%d requests in flight at once", maxInFlight.Load())
	}
	if calls.Load() != 12 || c.Requests() != 12 {
		t.Errorf("%d calls, %d counted, want 12 (two 503s before each answer)", calls.Load(), c.Requests())
	}
}

func TestGivesUpAfterAttempts(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	c := &horizons.Client{BaseURL: srv.URL, Backoff: time.Millisecond, Attempts: 3}
	if _, err := c.Ephemeris(context.Background(), request()); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("err %v", err)
	}
	if c.Requests() != 3 {
		t.Errorf("%d requests, want 3", c.Requests())
	}
}

func TestRejectsOversizedSpan(t *testing.T) {
	t.Parallel()
	r := request()
	r.Step = time.Minute
	r.Stop = r.Start.Add(70 * 24 * time.Hour)
	if _, err := (&horizons.Client{BaseURL: "http://127.0.0.1:1"}).Ephemeris(context.Background(), r); err == nil || !strings.Contains(err.Error(), "row limit") {
		t.Fatalf("err %v", err)
	}
}

func TestHonorsRetryAfterSeconds(t *testing.T) {
	t.Parallel()
	body := fixture(t, "comet-c2025r2-10m.json")
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	c := &horizons.Client{BaseURL: srv.URL, Backoff: time.Millisecond}
	start := time.Now()
	if _, err := c.Ephemeris(context.Background(), request()); err != nil {
		t.Fatal(err)
	}
	if waited := time.Since(start); waited < time.Second {
		t.Errorf("retried after %v, Retry-After said 1 s", waited)
	}
}

func TestRetriesBrokenConnections(t *testing.T) {
	t.Parallel()
	body := fixture(t, "comet-c2025r2-10m.json")
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			if hj, ok := w.(http.Hijacker); ok {
				if conn, _, err := hj.Hijack(); err == nil {
					_ = conn.Close()
				}
			}
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	c := &horizons.Client{BaseURL: srv.URL, Backoff: time.Millisecond}
	if _, err := c.Ephemeris(context.Background(), request()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Errorf("%d calls, want a retry after the dropped connection", calls.Load())
	}
}

func TestWaitersHonorContext(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	defer close(release)
	c := &horizons.Client{BaseURL: srv.URL, Attempts: 1}
	go func() { _, _ = c.Ephemeris(context.Background(), request()) }()
	for c.Requests() == 0 {
		time.Sleep(time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	other := request()
	other.Start, other.Stop = other.Start.Add(24*time.Hour), other.Stop.Add(24*time.Hour)
	start := time.Now()
	if _, err := c.Ephemeris(ctx, other); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err %v", err)
	}
	if time.Since(start) > time.Second || c.Requests() != 1 {
		t.Errorf("a waiter ignored its context: %v, %d requests", time.Since(start), c.Requests())
	}
}

func TestFailuresAreRememberedForAWhile(t *testing.T) {
	t.Parallel()
	body := fixture(t, "nomatch.json")
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	now := time.Date(2026, 10, 12, 20, 0, 0, 0, time.UTC)
	c := &horizons.Client{BaseURL: srv.URL, Cache: cacheDB(t), Now: func() time.Time { return now }}
	for range 3 {
		if _, err := c.Ephemeris(context.Background(), request()); !errors.Is(err, horizons.ErrUnknownBody) {
			t.Fatalf("err %v", err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("%d requests for a remembered failure", calls.Load())
	}
	now = now.Add(horizons.DefaultFailureTTL + time.Minute)
	if _, err := c.Ephemeris(context.Background(), request()); !errors.Is(err, horizons.ErrUnknownBody) || calls.Load() != 2 {
		t.Fatalf("after the TTL: %v, %d requests", err, calls.Load())
	}
}

func TestStaleTablesAreRefreshed(t *testing.T) {
	t.Parallel()
	body := fixture(t, "comet-c2025r2-10m.json")
	var calls atomic.Int32
	var fail atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		if fail.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	now := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	c := &horizons.Client{BaseURL: srv.URL, Cache: cacheDB(t), Attempts: 1, Now: func() time.Time { return now }}
	ctx := context.Background()
	if _, err := c.Ephemeris(ctx, request()); err != nil {
		t.Fatal(err)
	}
	now = now.Add(25 * time.Hour)
	fresh, err := c.Ephemeris(ctx, request())
	if err != nil || fresh.Cached || calls.Load() != 2 {
		t.Fatalf("a day-old table of a coming night was not fetched again: cached=%v %v %d", fresh.Cached, err, calls.Load())
	}
	now = now.Add(25 * time.Hour)
	fail.Store(true)
	stale, err := c.Ephemeris(ctx, request())
	if err != nil || !stale.Cached || len(stale.Rows) != 73 || calls.Load() != 3 {
		t.Fatalf("a failed refresh did not keep the cached table: %v %v %d", stale.Cached, err, calls.Load())
	}
	now = time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	if done, err := c.Ephemeris(ctx, request()); err != nil || !done.Cached || calls.Load() != 3 {
		t.Fatalf("a past night was fetched again: %v %d", err, calls.Load())
	}
}

func TestCacheKeyIncludesTheSite(t *testing.T) {
	t.Parallel()
	body := fixture(t, "comet-c2025r2-10m.json")
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	c := &horizons.Client{BaseURL: srv.URL, Cache: cacheDB(t)}
	other := request()
	other.Site.Latitude = 40
	for _, r := range []horizons.Request{request(), other, request(), other} {
		if _, err := c.Ephemeris(context.Background(), r); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 2 {
		t.Errorf("%d requests for two sites, want 2", calls.Load())
	}
}

func TestMultipleMajorBodiesIsAmbiguous(t *testing.T) {
	t.Parallel()
	if _, err := horizons.Parse(" Multiple major-bodies match string \"MAR*\"\n\n  ID#      Name\n  499      Mars\n"); !errors.Is(err, horizons.ErrAmbiguous) {
		t.Errorf("err %v", err)
	}
}

func TestParsePositionAngles(t *testing.T) {
	t.Parallel()
	tab, err := horizons.Parse(result(t, "comet-161p-10m-psang.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !tab.Angles || len(tab.Rows) != 13 {
		t.Fatalf("angles %v rows %d", tab.Angles, len(tab.Rows))
	}
	r := tab.Rows[0]
	if r.PsAng == nil || r.PsAMV == nil || !near(*r.PsAng, 84.059, 1e-9) || !near(*r.PsAMV, 141.693, 1e-9) {
		t.Fatalf("PsAng %v PsAMV %v", r.PsAng, r.PsAMV)
	}
	if !near(r.RA, 321.494951490, 1e-9) || r.UncRA == nil || !near(*r.UncRA, 0.220, 1e-9) {
		t.Fatalf("columns after PsAMV misread: ra %v unc %v", r.RA, r.UncRA)
	}
	old, err := horizons.Parse(result(t, "comet-c2025r2-10m.json"))
	if err != nil {
		t.Fatal(err)
	}
	if old.Angles || old.Rows[0].PsAng != nil || old.Rows[0].PsAMV != nil {
		t.Fatalf("a table without quantity 27 read as having angles")
	}
}

func TestEphemerisWithAnglesRefetchesAnOldCachedTableOnce(t *testing.T) {
	t.Parallel()
	old, fresh := fixture(t, "comet-c2025r2-10m.json"), fixture(t, "comet-161p-10m-psang.json")
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			_, _ = w.Write([]byte(old))
			return
		}
		_, _ = w.Write([]byte(fresh))
	}))
	defer srv.Close()
	now := time.Date(2026, 10, 12, 18, 0, 0, 0, time.UTC)
	c := &horizons.Client{BaseURL: srv.URL, Cache: cacheDB(t), Now: func() time.Time { return now }}
	ctx := context.Background()
	if tab, err := c.Ephemeris(ctx, request()); err != nil || tab.Angles {
		t.Fatalf("seed: %v %v", tab.Angles, err)
	}
	if tab, err := c.Ephemeris(ctx, request()); err != nil || !tab.Cached || calls.Load() != 1 {
		t.Fatalf("a young cached table without angles was fetched again for a caller that does not need them: %d %v", calls.Load(), err)
	}
	tab, err := c.EphemerisWithAngles(ctx, request())
	if err != nil || !tab.Angles || tab.Cached || calls.Load() != 2 {
		t.Fatalf("angles %v cached %v calls %d err %v", tab.Angles, tab.Cached, calls.Load(), err)
	}
	again, err := c.EphemerisWithAngles(ctx, request())
	if err != nil || !again.Angles || !again.Cached || calls.Load() != 2 {
		t.Fatalf("the refetched table was not cached under the same key: %v %v %d %v", again.Angles, again.Cached, calls.Load(), err)
	}
	if plain, err := c.Ephemeris(ctx, request()); err != nil || !plain.Angles || calls.Load() != 2 {
		t.Fatalf("plain lookup after the refetch: %v %d %v", plain.Angles, calls.Load(), err)
	}
}
