package discoveries

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/frameheader"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type memStore struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (s *memStore) Get(_ context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.m[key]
	if !ok {
		return nil, fmt.Errorf("no %s", key)
	}
	return b, nil
}

func (s *memStore) Put(_ context.Context, key string, data []byte, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[key] = data
	return nil
}

func (s *memStore) RemovePrefix(_ context.Context, prefix string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.m {
		if strings.HasPrefix(k, prefix) {
			delete(s.m, k)
		}
	}
	return nil
}

func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&app.Frame{}, &app.Stack{}, &app.StackFrame{}, &app.TargetReference{}, &app.DiscoveryRun{}, &app.DiscoveryCandidate{}); err != nil {
		t.Fatal(err)
	}
	return db
}

// catalogServer answers like VizieR, SkyBoT, ALeRCE and Fink for the
// synthetic field.
func catalogServer(t *testing.T, f *field, variable Variable, body func() Body) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/vizier", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("-source") {
		case "I/355/gaiadr3":
			fmt.Fprint(w, "#\nRA_ICRS\tDE_ICRS\tGmag\n-------\t-------\t----\n")
			for _, s := range f.gaia() {
				fmt.Fprintf(w, "%.7f\t%+.7f\t%.3f\n", s.RA, s.Dec, s.G)
			}
		case "B/vsx/vsx":
			fmt.Fprint(w, "Name\tType\tmax\tmin\tn_max\tPeriod\tRAJ2000\tDEJ2000\n----\t----\t---\t---\t---\t---\t---\t---\n")
			fmt.Fprintf(w, "%s\t%s\t15.1\t15.9\tV\t0.31\t%.5f\t%+.5f\n", variable.Name, variable.Type, variable.RA, variable.Dec)
		default:
			http.Error(w, "unknown", http.StatusBadRequest)
		}
	})
	mux.HandleFunc("/skybot", func(w http.ResponseWriter, _ *http.Request) {
		b := body()
		fmt.Fprint(w, "# Flag: 1\n# Ticket: 1\n# Num | Name | RA(h) | DE(deg) | Class | Mv | Err(arcsec) | d(arcsec) | dRA(arcsec/h) | dDEC(arcsec/h)\n")
		fmt.Fprintf(w, "%s | %s | %s | %s | %s | %.1f | 0.1 | 10 | %.4f | %.4f\n", b.Number, b.Name, hms(b.RA/15), dms(b.Dec), b.Class, b.V, b.DRA, b.DDec)
	})
	mux.HandleFunc("/simbad", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "main_id\tra\tdec\totype\n\"V* NN Tst\"\t1.0\t-80.0\t\"Or*\"\n")
	})
	mux.HandleFunc("/alerce/", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"items":[]}`) })
	mux.HandleFunc("/fink", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `[]`) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func hms(h float64) string {
	hh := int(h)
	m := (h - float64(hh)) * 60
	mm := int(m)
	return fmt.Sprintf("%02d %02d %07.4f", hh, mm, (m-float64(mm))*60)
}

func dms(d float64) string {
	sign := "+"
	if d < 0 {
		sign, d = "-", -d
	}
	dd := int(d)
	m := (d - float64(dd)) * 60
	mm := int(m)
	return fmt.Sprintf("%s%02d %02d %06.3f", sign, dd, mm, (m-float64(mm))*60)
}

type runnerFixture struct {
	f        *field
	db       *gorm.DB
	store    *memStore
	r        *Runner
	finished []app.DiscoveryRun
}

const synthObject = "Synthetic Field"

func seedTarget(t *testing.T, f *field, db *gorm.DB, store *memStore) {
	t.Helper()
	key := "stacks/" + synthObject + "/" + testFilter + "/master.fit"
	store.m[key] = f.masterFITS(t)
	stack := app.Stack{Object: synthObject, Filter: testFilter, Width: f.W, Height: f.H, Subs: 24, MasterKey: &key, UpdatedAt: time.Now()}
	if err := db.Create(&stack).Error; err != nil {
		t.Fatal(err)
	}
	cards := make([]frameheader.Card, 0, len(synthWCS()))
	for k, v := range synthWCS() {
		cards = append(cards, frameheader.Card{Name: k, Value: v})
	}
	wcsJSON, err := json.Marshal(cards)
	if err != nil {
		t.Fatal(err)
	}
	ws := string(wcsJSON)
	if err := db.Create(&app.TargetReference{Object: synthObject, FrameID: 1, ObjectKey: "x", WCS: &ws}).Error; err != nil {
		t.Fatal(err)
	}
	processed := time.Date(2026, 3, 4, 12, 0, 0, 0, time.UTC)
	for n := range f.Nights {
		for i := range f.PerNight {
			k := fmt.Sprintf("registered/%s/%s/%d_%d.xisf", synthObject, testFilter, n, i)
			store.m[k] = f.xisf(t, n, i)
			start := f.subTime(n, i)
			night := f.Nights[n]
			exp := float64(synthSubSeconds)
			fr := app.Frame{Key: fmt.Sprintf("%s/LIGHT/%d_%d.xisf", synthObject, n, i), Type: "LIGHT", Object: synthObject, Filter: testFilter, Exposure: &exp, DateObs: &start, Night: &night, LastModified: start}
			if err := db.Create(&fr).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&app.StackFrame{FrameID: fr.ID, StackID: &stack.ID, Status: app.StackStatusAdded, Weight: 1, Exposure: exp, RegisteredKey: &k, ProcessedAt: processed}).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
}

func newRunnerFixture(t *testing.T) *runnerFixture {
	t.Helper()
	fx := &runnerFixture{f: newField(t), db: testDB(t), store: &memStore{m: map[string][]byte{}}}
	f := fx.f
	seedTarget(t, f, fx.db, fx.store)
	nra, ndec := f.WCS.SkyOf(f.NewX, f.NewY)
	if err := fx.db.Create(&app.DiscoveryCandidate{RunID: 999, Object: synthObject, Night: f.Nights[0], Group: GroupBroadband, Kind: app.DiscoveryKindChange,
		Class: ClassLowConfidence, RA: nra, Dec: ndec, Sign: 1, SNR: 6, Matches: "[]"}).Error; err != nil {
		t.Fatal(err)
	}
	v := f.Stars[f.Var]
	vra, vdec := f.WCS.SkyOf(v.X, v.Y)
	first := f.subTime(len(f.Nights)-1, 0)
	var sum time.Duration
	for i := range f.PerNight {
		sum += f.subTime(len(f.Nights)-1, i).Add(synthSubSeconds * time.Second / 2).Sub(first)
	}
	mid := first.Add(sum / time.Duration(f.PerNight)).UTC()
	body := func() Body {
		dt := mid.Sub(first).Minutes()
		ra, dec := f.WCS.SkyOf(f.AstX+f.AstVX*dt, f.AstY+f.AstVY*dt)
		return Body{Number: "12345", Name: "2000 AA", Class: "MB>Inner", RA: ra, Dec: dec, V: 17.2, DRA: -f.AstVX * 2 * 60, DDec: -f.AstVY * 2 * 60}
	}
	srv := catalogServer(t, f, Variable{Name: "V1 Test", Type: "EW", RA: vra, Dec: vdec}, body)
	cat := &Catalogs{VizierURL: srv.URL + "/vizier", SkyBoTURL: srv.URL + "/skybot", AlerceURL: srv.URL + "/alerce/", FinkURL: srv.URL + "/fink", SIMBADURL: srv.URL + "/simbad", ZTFURL: srv.URL + "/ztf"}
	fx.r = New(fx.db, nil, fx.store, cat, Options{BackfillDays: 1, Threads: 2, OnRun: func(run app.DiscoveryRun) { fx.finished = append(fx.finished, run) }})
	fx.r.now = func() time.Time { return time.Date(2026, 3, 5, 12, 0, 0, 0, time.UTC) }
	return fx
}

func (fx *runnerFixture) checkRun(t *testing.T) {
	t.Helper()
	var runs []app.DiscoveryRun
	fx.db.Find(&runs)
	if len(runs) != 1 || runs[0].State != app.DiscoveryDone {
		t.Fatalf("runs %+v", runs)
	}
	run := runs[0]
	if run.NightSubs != 6 || run.RefSubs != 18 || run.RefNights != 3 || run.Depth == nil || run.Checked != "VSX,SIMBAD,SkyBoT,ALeRCE,Fink" {
		t.Errorf("run %+v", run)
	}
	if run.NewCandidates != 1 || run.KnownTracks != 1 || run.Tracks != 1 || run.BodiesInField != 1 {
		t.Errorf("run counts new %d tracks %d known %d bodies %d", run.NewCandidates, run.Tracks, run.KnownTracks, run.BodiesInField)
	}
	if len(fx.finished) != 1 {
		t.Errorf("OnRun heard %d runs", len(fx.finished))
	}
}

func (fx *runnerFixture) checkCandidates(t *testing.T) {
	t.Helper()
	var cands []app.DiscoveryCandidate
	fx.db.Where("run_id <> ?", 999).Order("id").Find(&cands)
	classes := map[string]int{}
	for _, c := range cands {
		classes[c.Class]++
		for _, n := range []string{"night.png", "ref.png", "diff.png", "stamp.fits"} {
			if _, err := fx.store.Get(context.Background(), c.CutoutKey+n); c.CutoutKey == "" || err != nil {
				t.Errorf("candidate %d cutout %s: %v", c.ID, n, err)
			}
		}
		if c.Kind == app.DiscoveryKindMover {
			checkTrack(t, c)
		}
	}
	delete(classes, ClassLowConfidence)
	want := map[string]int{ClassKnownVariable: 1, ClassNewVariable: 1, ClassKnownAsteroid: 1}
	if !maps.Equal(classes, want) {
		t.Errorf("classes %v, want %v", classes, want)
	}
}

func checkTrack(t *testing.T, c app.DiscoveryCandidate) {
	t.Helper()
	var tr TrackRecord
	if err := json.Unmarshal([]byte(c.Track), &tr); err != nil {
		t.Fatal(err)
	}
	if len(tr.ADES) < MoverMinHits || tr.ADES[0].AstCat != "Gaia3" || !strings.HasSuffix(tr.ADES[0].ObsTime, "Z") || tr.BodyNumber != "12345" {
		t.Errorf("track %+v", tr)
	}
}

func TestRunnerComparesSettledNight(t *testing.T) {
	t.Parallel()
	fx := newRunnerFixture(t)
	ctx := context.Background()
	if err := fx.r.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	fx.checkRun(t)
	fx.checkCandidates(t)

	fx.db.Model(&app.DiscoveryCandidate{}).Where("class = ?", ClassNewVariable).Updates(map[string]any{"vetting": app.VetReal, "vet_note": "checked"})
	if err := fx.r.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	if len(fx.finished) != 1 {
		t.Errorf("a second pass with the same subs ran again")
	}
	fx.db.Model(&app.DiscoveryRun{}).Where("1 = 1").Update("method", 3)
	if jobs, err := fx.r.due(ctx); err != nil || len(jobs) != 1 {
		t.Errorf("a night compared by method 3, before the hot pixel, proper motion, SIMBAD and nova fixes, is not due again: %v %v", jobs, err)
	}
	fx.db.Model(&app.DiscoveryRun{}).Where("1 = 1").Update("method", Method)
	fx.db.Model(&app.DiscoveryRun{}).Where("1 = 1").Update("method", 3)
	stale, err := fx.r.due(ctx)
	if err != nil || len(stale) != 1 {
		t.Fatalf("due %v %v", stale, err)
	}
	fx.r.now = func() time.Time { return time.Date(2026, 3, 5, 12, 5, 0, 0, time.UTC) }
	fx.db.Model(&app.DiscoveryRun{}).Where("1 = 1").Updates(map[string]any{"method": Method, "finished_at": fx.r.now()})
	fx.r.runOne(ctx, stale[0])
	if len(fx.finished) != 1 {
		t.Errorf("a night another runner finished since the pass began was compared again")
	}
	fx.r.now = func() time.Time { return time.Date(2026, 3, 5, 12, 0, 0, 0, time.UTC) }
	fx.r.Request(synthObject, fx.f.Nights[len(fx.f.Nights)-1], "")
	if err := fx.r.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	if len(fx.finished) != 2 {
		t.Errorf("a requested run did not run")
	}
	var vetted []app.DiscoveryCandidate
	fx.db.Where("vetting <> ''").Find(&vetted)
	if len(vetted) != 1 || vetted[0].VetNote != "checked" {
		t.Errorf("vetting not kept over a rerun: %+v", vetted)
	}
	n := 0
	for k := range fx.store.m {
		if strings.HasPrefix(k, keyPrefix+"cache/") {
			n++
		}
	}
	if n != 3 {
		t.Errorf("want the Gaia, VSX and SIMBAD caches, have %d cache objects", n)
	}
}

func TestPickRefTakesNightsInTurn(t *testing.T) {
	t.Parallel()
	d1, d2 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	nights := map[time.Time][]subRow{
		d1: {{FrameID: 1, Weight: 1}, {FrameID: 2, Weight: 3}, {FrameID: 3, Weight: 2}},
		d2: {{FrameID: 4, Weight: 1}},
	}
	got := pickRef(nights, d2.AddDate(0, 0, 1), 3)
	ids := make([]int, 0, len(got))
	for _, s := range got {
		ids = append(ids, s.FrameID)
	}
	if !slices.Equal(ids, []int{2, 4, 3}) {
		t.Errorf("picked %v", ids)
	}
}

// TestPickRefPrefersNightsFarInTime: a transient that lasts weeks, like
// the luminous red nova AT 2025abao in M 31, is in the nights near the one
// compared; nights a month or more away make the reference when they have
// subs enough, the farthest first.
func TestPickRefPrefersNightsFarInTime(t *testing.T) {
	t.Parallel()
	night := time.Date(2025, 10, 20, 0, 0, 0, 0, time.UTC)
	rows := func(id0, n int) []subRow {
		out := make([]subRow, n)
		for i := range out {
			out[i] = subRow{FrameID: id0 + i, Weight: float64(n - i)}
		}
		return out
	}
	nights := map[time.Time][]subRow{
		night.AddDate(0, 0, -3): rows(100, 10), night.AddDate(0, 0, 2): rows(200, 10),
		night.AddDate(0, 0, -280): rows(300, 4), night.AddDate(0, 0, 365): rows(400, 4),
	}
	got := pickRef(nights, night, 6)
	for _, s := range got {
		if s.FrameID < 300 {
			t.Errorf("picked sub %d from a night days away while nights months away had subs enough", s.FrameID)
		}
	}
	if len(got) != 6 || got[0].FrameID != 400 {
		t.Errorf("picked %v, want the farthest night's best first", got)
	}
	got = pickRef(nights, night, 12)
	near := 0
	for _, s := range got {
		if s.FrameID < 300 {
			near++
		}
	}
	if len(got) != 12 || near != 4 {
		t.Errorf("with too few far subs the nearer nights fill in: %d of %d near", near, len(got))
	}
}

func TestGroupOfAndHash(t *testing.T) {
	t.Parallel()
	if GroupOf("Red") != GroupBroadband || GroupOf("H-a") != "H-a" {
		t.Error("groups")
	}
	a := subsHash([]subRow{{FrameID: 2}, {FrameID: 1}})
	b := subsHash([]subRow{{FrameID: 1}, {FrameID: 2}})
	if a != b || a == subsHash([]subRow{{FrameID: 1}}) {
		t.Error("hash")
	}
}

func TestRunnerBacksOffAfterAFailure(t *testing.T) {
	t.Parallel()
	fx := newRunnerFixture(t)
	fx.db.Where("object = ?", synthObject).Delete(&app.TargetReference{})
	ctx := context.Background()
	for range 2 {
		if err := fx.r.Pass(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var runs []app.DiscoveryRun
	fx.db.Find(&runs)
	if len(runs) != 1 || runs[0].State != app.DiscoveryFailed || runs[0].Attempts != 1 || runs[0].SubsHash == "" || runs[0].NextTry == nil {
		t.Fatalf("runs %+v", runs)
	}
	fx.db.Model(&runs[0]).Update("state", app.DiscoveryRunning)
	jobs, err := fx.r.due(ctx)
	if err != nil || len(jobs) != 1 {
		t.Errorf("a run left running by a restart is not due again: %v %v", jobs, err)
	}
}

func TestWaitIdleGoesOnBesideALongBusyStacker(t *testing.T) {
	t.Parallel()
	room := false
	r := New(nil, nil, nil, nil, Options{Busy: func() bool { return true }, MaxWait: time.Millisecond, Headroom: func() bool { return room }})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := r.waitIdle(ctx); err == nil {
		t.Error("went on without memory to spare")
	}
	room = true
	if err := r.waitIdle(context.Background()); err != nil {
		t.Error(err)
	}
}

func TestStatValue(t *testing.T) {
	t.Parallel()
	if v, ok := statValue("anon 123\nfile 456\nanon_thp 7\n", "anon"); !ok || v != 123 {
		t.Errorf("anon %d %v", v, ok)
	}
	if _, ok := statValue("file 1\n", "anon"); ok {
		t.Error("missing key found")
	}
}
