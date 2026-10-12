package bodies_test

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

	"github.com/USA-RedDragon/astro-stacker/internal/bodies"
	"github.com/USA-RedDragon/astro-stacker/internal/horizons"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

const (
	tc6    = "2026 TC6"
	tc6Cmd = "DES=" + tc6 + ";"
)

func horizonsFixture(t *testing.T, name string) horizons.Table {
	t.Helper()
	b, err := os.ReadFile("../horizons/testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var r struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	tab, err := horizons.Parse(r.Result)
	if err != nil {
		t.Fatal(err)
	}
	return tab
}

func asteroidByDes(t *testing.T, els []bodies.AsteroidElements, des string) bodies.AsteroidElements {
	t.Helper()
	for _, el := range els {
		if el.Designation == des {
			return el
		}
	}
	t.Fatalf("no %s in the sample", des)
	return bodies.AsteroidElements{}
}

func TestParseAsteroids(t *testing.T) {
	t.Parallel()
	els, bad, err := bodies.ParseAsteroids(readFile(t, "sbdb-asteroids-sample.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(els) != 10 || bad != 1 {
		t.Fatalf("%d asteroids, %d unreadable", len(els), bad)
	}
	ceres := asteroidByDes(t, els, "1")
	if ceres.Name != "1 Ceres" || ceres.Number == nil || *ceres.Number != 1 || ceres.FullName != "1 Ceres (A801 AA)" || ceres.NEO {
		t.Errorf("Ceres %+v", ceres)
	}
	if ceres.HV != 3.34 || ceres.GV == nil || *ceres.GV != 0.12 || math.Abs(ceres.Q-2.545159361382861) > 1e-12 {
		t.Errorf("Ceres H %v G %v q %v", ceres.HV, ceres.GV, ceres.Q)
	}
	if eros := asteroidByDes(t, els, "433"); !eros.NEO || eros.Name != "433 Eros" {
		t.Errorf("Eros %+v", eros)
	}
	if astraea := asteroidByDes(t, els, "5"); astraea.GV != nil {
		t.Errorf("a null G read as %v", *astraea.GV)
	}
	if old := asteroidByDes(t, els, "1927 LA"); old.Number != nil || old.Name != "1927 LA" {
		t.Errorf("unnumbered %+v", old)
	}
	if _, _, err := bodies.ParseAsteroids(`{"fields":["pdes"],"data":[]}`); err == nil {
		t.Error("a query without the element fields was read")
	}
}

func TestAsteroidTwoBodyMatchesHorizons(t *testing.T) {
	t.Parallel()
	els, _, err := bodies.ParseAsteroids(readFile(t, "sbdb-asteroids-sample.json"))
	if err != nil {
		t.Fatal(err)
	}
	for des, file := range map[string]string{"1": "asteroid-ceres-10m.json", "4": "asteroid-vesta-10m.json"} {
		el := asteroidByDes(t, els, des)
		rows := horizonsFixture(t, file).Rows
		for _, row := range []horizons.Row{rows[0], rows[36], rows[len(rows)-1]} {
			s := el.Seen(row.At)
			dra := math.Remainder(s.RA-row.RA, 360) * math.Cos(row.Dec*math.Pi/180)
			sep := math.Hypot(dra, s.Dec-row.Dec) * 3600
			if sep > 30 {
				t.Errorf("%s at %s: two-body is %.1f arcsec from Horizons", des, row.At, sep)
			}
			if math.Abs(s.Delta-*row.Delta) > 5e-4 {
				t.Errorf("%s delta %v, Horizons %v", des, s.Delta, *row.Delta)
			}
			m, phase, ok := el.V(s, row.At)
			if !ok || math.Abs(m-*row.Mag) > 0.05 || phase <= 0 || phase > 30 {
				t.Errorf("%s V %.3f, Horizons APmag %.3f", des, m, *row.Mag)
			}
		}
	}
}

type fakeJPL struct {
	srv       *httptest.Server
	mu        sync.Mutex
	commands  []string
	sbdbHits  atomic.Int32
	cadQuery  atomic.Value
	slow      time.Duration
	failSBDB  atomic.Bool
	horizonsN atomic.Int32
}

func newFakeJPL(t *testing.T) *fakeJPL {
	t.Helper()
	f := &fakeJPL{}
	sbdb, cad := readFile(t, "sbdb-asteroids-sample.json"), readFile(t, "cad-sample.json")
	files := map[string]string{"'1;'": "asteroid-ceres-10m.json", "'4;'": "asteroid-vesta-10m.json", "'" + tc6Cmd + "'": "asteroid-2026tc6-10m.json"}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Error("no User-Agent")
		}
		switch r.URL.Path {
		case "/sbdb":
			f.sbdbHits.Add(1)
			if f.failSBDB.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write([]byte(sbdb))
		case "/cad":
			f.cadQuery.Store(r.URL.RawQuery)
			_, _ = w.Write([]byte(cad))
		case "/horizons":
			f.horizonsN.Add(1)
			cmd := r.URL.Query().Get("COMMAND")
			f.mu.Lock()
			f.commands = append(f.commands, cmd)
			f.mu.Unlock()
			if f.slow > 0 {
				select {
				case <-time.After(f.slow):
				case <-r.Context().Done():
					return
				}
			}
			name, ok := files[cmd]
			if !ok {
				_, _ = w.Write([]byte(`{"result":"No matches found."}`))
				return
			}
			b, err := os.ReadFile("../horizons/testdata/" + name)
			if err != nil {
				t.Error(err)
			}
			_, _ = w.Write(b)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeJPL) checkCommands(t *testing.T) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.commands {
		if strings.Contains(c, "CAP") || c == "'4;'" || c == "'1;'" {
			t.Errorf("Horizons asked for %s", c)
		}
	}
}

func checkListed(t *testing.T, v bodies.AsteroidView, limit float64) {
	t.Helper()
	for i, a := range v.Asteroids {
		if a.Magnitude == nil || *a.Magnitude > limit || a.Window.Hours == 0 {
			t.Errorf("listed %s V %v up %v h", a.Name, a.Magnitude, a.Window.Hours)
			continue
		}
		if i > 0 && *v.Asteroids[i-1].Magnitude > *a.Magnitude {
			t.Errorf("not brightest first at %s", a.Name)
		}
	}
}

func checkEros(t *testing.T, v bodies.AsteroidView) {
	t.Helper()
	eros := findAsteroid(v.Asteroids, "433")
	if eros == nil || eros.PositionSource != bodies.SourceTwoBody || !eros.NEO || eros.Window.Hours != 0 {
		t.Errorf("Eros, which Horizons does not know here, should keep its two-body values: %+v", eros)
	}
}

func asteroidList(t *testing.T, f *fakeJPL, now time.Time) *bodies.AsteroidList {
	t.Helper()
	return asteroidListAt(t, f, func() time.Time { return now })
}

func asteroidListAt(t *testing.T, f *fakeJPL, now func() time.Time) *bodies.AsteroidList {
	t.Helper()
	db := open(t)
	if err := db.AutoMigrate(&app.CometElements{}, &app.HorizonsCache{}); err != nil {
		t.Fatal(err)
	}
	comets := &bodies.CometList{DB: db, Now: now,
		Site: func(context.Context) (sky.Site, error) { return testSite(), nil }}
	return &bodies.AsteroidList{Comets: comets, URL: f.srv.URL + "/sbdb", CADURL: f.srv.URL + "/cad",
		Horizons: &horizons.Client{BaseURL: f.srv.URL + "/horizons", UserAgent: "test", Cache: db, Now: now}}
}

func findAsteroid(list []bodies.Asteroid, des string) *bodies.Asteroid {
	for i := range list {
		if list[i].Designation == des {
			return &list[i]
		}
	}
	return nil
}

func TestAsteroidListTonight(t *testing.T) {
	t.Parallel()
	f := newFakeJPL(t)
	now := time.Date(2026, 10, 12, 22, 0, 0, 0, time.UTC)
	l := asteroidList(t, f, now)
	ctx := context.Background()
	v, err := l.List(ctx, bodies.DefaultAsteroidMaxMag, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if v.MaxMag != 12 || v.MinAltitude != bodies.DefaultMinAlt || v.Elements.Asteroids != 10 || v.Elements.FetchedAt == nil || v.Basis == "" {
		t.Fatalf("view %+v", v)
	}
	if v.CloseApproaches.Asteroids != 2 || v.CloseApproaches.Error != "" {
		t.Errorf("close approaches %+v", v.CloseApproaches)
	}
	if q, _ := f.cadQuery.Load().(string); q != "date-max=2026-10-22&date-min=2026-10-02&dist-max=0.2&fullname=true" {
		t.Errorf("CAD query %q", q)
	}
	vesta := findAsteroid(v.Asteroids, "4")
	if vesta == nil || vesta.Name != "4 Vesta" || vesta.Kind != bodies.KindAsteroid || vesta.Command != "4;" || vesta.NEO {
		t.Fatalf("Vesta %+v", vesta)
	}
	if vesta.Magnitude == nil || math.Abs(*vesta.Magnitude-6.4) > 0.1 || vesta.MagSource != bodies.SourceTwoBody || vesta.PositionSource != bodies.SourceTwoBody {
		t.Errorf("Vesta V %v from %s", vesta.Magnitude, vesta.MagSource)
	}
	if vesta.Window.Hours <= 0 || vesta.Window.PeakAlt < 30 || vesta.MotionRate < 0.5 || vesta.MotionRate > 0.7 {
		t.Errorf("Vesta window %+v rate %v", vesta.Window, vesta.MotionRate)
	}
	checkListed(t, v, 12)
	if findAsteroid(v.Asteroids, tc6) != nil || findAsteroid(v.Asteroids, "2026 TP35") != nil {
		t.Error("faint close approaches listed at V 12")
	}
}

func TestAsteroidListChecksNearEarthWithHorizons(t *testing.T) {
	t.Parallel()
	f := newFakeJPL(t)
	l := asteroidList(t, f, time.Date(2026, 10, 12, 22, 0, 0, 0, time.UTC))
	ctx := context.Background()
	faint, err := l.List(ctx, 21, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	got := findAsteroid(faint.Asteroids, tc6)
	if got == nil {
		t.Fatalf("2026 TC6 missing at V 21: %+v", faint.Horizons)
	}
	if !got.NEO || got.PositionSource != bodies.SourceHorizons || got.MagSource != bodies.SourceHorizons || got.Command != tc6Cmd || got.Number != nil {
		t.Errorf("2026 TC6 %+v", got)
	}
	if got.Magnitude == nil || math.Abs(*got.Magnitude-20.0) > 0.1 || got.MotionRate < 30 || got.Delta < 0.025 || got.Delta > 0.03 {
		t.Errorf("2026 TC6 V %v rate %v delta %v", got.Magnitude, got.MotionRate, got.Delta)
	}
	if got.R < 0.95 || got.R > 1.05 || got.Phase <= 0 {
		t.Errorf("2026 TC6 r %v phase %v", got.R, got.Phase)
	}
	if findAsteroid(faint.Asteroids, "433") != nil {
		t.Error("Eros is listed though it stays low tonight")
	}
	every, err := l.List(ctx, 21, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	checkEros(t, every)
	if faint.Horizons.Failed == 0 || faint.Horizons.Checked == 0 || len(faint.Horizons.Errors) == 0 {
		t.Errorf("checks %+v", faint.Horizons)
	}
	f.checkCommands(t)
	n := f.horizonsN.Load()
	if _, err := l.List(ctx, 21, nil, false); err != nil {
		t.Fatal(err)
	}
	if f.horizonsN.Load() != n {
		t.Errorf("tables or failures were fetched again: %d then %d requests", n, f.horizonsN.Load())
	}
	if f.sbdbHits.Load() != 1 {
		t.Errorf("%d SBDB fetches in one day", f.sbdbHits.Load())
	}
}

func TestAsteroidListAllAndAltitude(t *testing.T) {
	t.Parallel()
	f := newFakeJPL(t)
	l := asteroidList(t, f, time.Date(2026, 10, 12, 22, 0, 0, 0, time.UTC))
	ctx := context.Background()
	all, err := l.List(ctx, 99, alt(30), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Asteroids) != 9 {
		t.Errorf("all: %d asteroids", len(all.Asteroids))
	}
	high, err := l.List(ctx, 99, alt(89.9), false)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range high.Asteroids {
		if a.Window.PeakAlt < 89.9 {
			t.Errorf("%s listed with peak %v above 89.9", a.Name, a.Window.PeakAlt)
		}
	}
	if _, err := l.List(ctx, math.NaN(), nil, false); !errors.Is(err, bodies.ErrBadQuery) {
		t.Errorf("NaN limit: %v", err)
	}
	raw, err := json.Marshal(all)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"asteroids":[`, `"close_approaches":`, `"horizons":{"checked":`, `"magnitude_source":`, `"position_source":`, `"rate_arcsec_per_min":`, `"peak_alt":`, `"number":1,`, `"number":null`} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("JSON has no %s", key)
		}
	}
}

func TestAsteroidListBudgetKeepsTwoBodyValues(t *testing.T) {
	t.Parallel()
	f := newFakeJPL(t)
	f.slow = 5 * time.Second
	l := asteroidList(t, f, time.Date(2026, 10, 12, 22, 0, 0, 0, time.UTC))
	l.Budget = 200 * time.Millisecond
	start := time.Now()
	v, err := l.List(context.Background(), 21, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("the list waited %s on Horizons", time.Since(start))
	}
	if v.Horizons.Pending == 0 || v.Horizons.Checked != 0 {
		t.Errorf("checks %+v", v.Horizons)
	}
	if findAsteroid(v.Asteroids, tc6) != nil {
		t.Error("a close approach with no elements was listed without Horizons")
	}
	if eros := findAsteroid(v.Asteroids, "433"); eros == nil || eros.PositionSource != bodies.SourceTwoBody {
		t.Errorf("Eros %+v", eros)
	}
}

func TestAsteroidListWithoutElements(t *testing.T) {
	t.Parallel()
	f := newFakeJPL(t)
	f.failSBDB.Store(true)
	l := asteroidList(t, f, time.Date(2026, 10, 12, 22, 0, 0, 0, time.UTC))
	if _, err := l.List(context.Background(), 12, nil, false); !errors.Is(err, bodies.ErrNoAsteroidElements) {
		t.Errorf("no elements: %v", err)
	}
	f.failSBDB.Store(false)
	if _, err := l.List(context.Background(), 12, nil, false); err != nil {
		t.Errorf("after SBDB came back: %v", err)
	}
	noSite := &bodies.AsteroidList{Comets: &bodies.CometList{}}
	if _, err := noSite.List(context.Background(), 12, nil, false); !errors.Is(err, bodies.ErrNoSite) {
		t.Errorf("no site: %v", err)
	}
	if _, err := (&bodies.AsteroidList{}).List(context.Background(), 12, nil, false); !errors.Is(err, bodies.ErrNoSite) {
		t.Errorf("no comet list: %v", err)
	}
}

func TestParseCloseApproaches(t *testing.T) {
	t.Parallel()
	cas, err := bodies.ParseCloseApproaches(readFile(t, "cad-sample.json"))
	if err != nil || len(cas) != 2 {
		t.Fatalf("%d approaches, %v", len(cas), err)
	}
	if cas[1].Designation != tc6 || math.Abs(cas[1].H-26.102) > 1e-9 || cas[1].DistMin <= 0 || cas[1].At.Year() != 2026 {
		t.Errorf("%+v", cas[1])
	}
	if cas, err := bodies.ParseCloseApproaches(`{"signature":{},"count":0}`); err != nil || len(cas) != 0 {
		t.Errorf("none: %v %v", cas, err)
	}
	if _, err := bodies.ParseCloseApproaches(`{"error":"x"}`); err == nil {
		t.Error("an answer without a count was read")
	}
}

func TestHGMagnitude(t *testing.T) {
	t.Parallel()
	if m, ok := bodies.HGMagnitude(3.34, 0.12, 1, 1, 0); !ok || math.Abs(m-3.34) > 1e-9 {
		t.Errorf("at 1 au and no phase: %v", m)
	}
	m0, _ := bodies.HGMagnitude(10, 0.15, 2, 1, 0)
	m20, _ := bodies.HGMagnitude(10, 0.15, 2, 1, 20)
	if math.Abs(m0-(10+5*math.Log10(2))) > 1e-9 || m20-m0 < 0.5 || m20-m0 > 1.2 {
		t.Errorf("phase 0 %v, phase 20 %v", m0, m20)
	}
	if _, ok := bodies.HGMagnitude(10, 0.15, 0, 1, 0); ok {
		t.Error("r of 0 gave a magnitude")
	}
	if p := bodies.PhaseAngle(2, 1, 1); math.Abs(p) > 1e-6 {
		t.Errorf("opposition phase %v", p)
	}
	if p := bodies.PhaseAngle(1, 1, math.Sqrt2); math.Abs(p-90) > 1e-6 {
		t.Errorf("quadrature phase %v", p)
	}
}

func TestAsteroidListLeavesOutStaleElements(t *testing.T) {
	t.Parallel()
	f := newFakeJPL(t)
	l := asteroidList(t, f, time.Date(2026, 10, 12, 22, 0, 0, 0, time.UTC))
	v, err := l.List(context.Background(), 99, alt(-90), true)
	if err != nil {
		t.Fatal(err)
	}
	if v.Elements.Stale != 2 || findAsteroid(v.Asteroids, "1927 LA") != nil || findAsteroid(v.Asteroids, "1939 RR") != nil {
		t.Errorf("stale %d, listed %d", v.Elements.Stale, len(v.Asteroids))
	}
	if findAsteroid(v.Asteroids, "4") == nil {
		t.Error("current elements were left out")
	}
}

func TestAsteroidListRemembersUnknownBodiesAndRotatesCloseApproaches(t *testing.T) {
	t.Parallel()
	f := newFakeJPL(t)
	var mu sync.Mutex
	now := time.Date(2026, 10, 12, 22, 0, 0, 0, time.UTC)
	clock := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}
	advance := func(d time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		now = now.Add(d)
	}
	l := asteroidListAt(t, f, clock)
	ctx := context.Background()
	comet := app.CometElements{Source: bodies.CometElementsURL, Body: "x", FetchedAt: clock()}
	if err := l.Comets.DB.Create(&comet).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := l.List(ctx, 21, nil, false); err != nil {
		t.Fatal(err)
	}
	asked := func(cmd string) int {
		f.mu.Lock()
		defer f.mu.Unlock()
		n := 0
		for _, c := range f.commands {
			if c == cmd {
				n++
			}
		}
		return n
	}
	if asked("'433;'") != 1 {
		t.Fatalf("Eros asked %d times", asked("'433;'"))
	}
	advance(2 * time.Hour)
	v, err := l.List(ctx, 21, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if asked("'433;'") != 1 || v.Horizons.Failed == 0 {
		t.Errorf("Eros asked %d times within a day of Horizons not knowing it, checks %+v", asked("'433;'"), v.Horizons)
	}
	advance(24 * time.Hour)
	if _, err := l.List(ctx, 21, nil, false); err != nil {
		t.Fatal(err)
	}
	if asked("'433;'") != 2 {
		t.Errorf("Eros asked %d times after a day", asked("'433;'"))
	}
	var rows []app.CometElements
	if err := l.Comets.DB.Order("source").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	var cad, comets int
	for _, r := range rows {
		switch {
		case strings.HasPrefix(r.Source, f.srv.URL+"/cad?"):
			cad++
			if !strings.Contains(r.Source, "date-min=2026-10-03") {
				t.Errorf("kept %s", r.Source)
			}
		case r.Source == bodies.CometElementsURL:
			comets++
		}
	}
	if cad != 1 || comets != 1 {
		t.Errorf("%d close-approach lists and %d comet rows kept", cad, comets)
	}
}
