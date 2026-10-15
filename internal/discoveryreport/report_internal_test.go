package discoveryreport

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/testdb"
	"gorm.io/gorm"
)

const (
	m31     = "Andromeda"
	lum     = "Luminance"
	acsi    = "AT 2026acsi"
	smith   = "J. Smith"
	gaia3   = "Gaia3"
	botName = "bot"
)

func ptr(v float64) *float64 { return &v }

func findNight() time.Time { return time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC) }

func nova(t *testing.T) (app.DiscoveryCandidate, app.DiscoveryRun) {
	t.Helper()
	light, err := json.Marshal([]discoveries.LightPoint{
		{FrameID: 2, Filter: lum, Time: time.Date(2026, 10, 12, 3, 10, 0, 0, time.UTC), Flux: 2, FluxErr: 1},
		{FrameID: 1, Filter: lum, Time: time.Date(2026, 10, 12, 3, 5, 0, 0, time.UTC), Flux: 5, FluxErr: 1, Mag: ptr(18.1), MagErr: ptr(0.2)},
	})
	if err != nil {
		t.Fatal(err)
	}
	c := app.DiscoveryCandidate{ID: 13267, RunID: 9, Object: m31, Night: findNight(), Group: discoveries.GroupBroadband, Kind: app.DiscoveryKindChange,
		Class: discoveries.ClassSupernova, RA: 10.557966104327647, Dec: 41.27670411346332, Sign: 1, SNR: 10.8, NightMag: ptr(18.056), RefMag: ptr(18.492),
		DeltaMag: ptr(-0.348), PresentIn: 28, SubsTested: 28, Matches: `[{"catalog":"galaxy","name":"Andromeda Galaxy","sep_arcsec":344.2}]`, Light: string(light)}
	run := app.DiscoveryRun{ID: 9, Object: m31, Night: findNight(), Group: discoveries.GroupBroadband, Filters: lum, State: app.DiscoveryDone,
		Checked: "VSX,SkyBoT,galaxies,ALeRCE,Fink", NightSubs: 28, AstroRMS: ptr(0.25), PixelScale: 0.62, HFRNight: 2.1}
	return c, run
}

func spots() []discoveries.SpotNight {
	n := func(d int) time.Time { return findNight().AddDate(0, 0, -d) }
	return []discoveries.SpotNight{
		{Night: n(1), Filter: "Red", Subs: 4, Limit: ptr(16.76), Mid: n(1).Add(30 * time.Hour), Exposure: 240},
		{Night: n(83), Filter: "Green", Subs: 6, Limit: ptr(18.60), Mid: n(83).Add(30 * time.Hour), Exposure: 360},
		{Night: n(90), Filter: lum, Subs: 6, Limit: ptr(19.4), Mid: n(90).Add(30 * time.Hour), Exposure: 360},
		{Night: n(95), Filter: lum, Subs: 6, Detected: true, SNR: 5, Mag: ptr(19.0), MagErr: ptr(0.2), Mid: n(95).Add(30 * time.Hour)},
	}
}

func fieldOf(r Report, key string) Field {
	for _, f := range r.TNS.Fields {
		if f.Key == key {
			return f
		}
	}
	return Field{}
}

func checkOf(r Report, key string) Check {
	for _, c := range r.Checks {
		if c.Key == key {
			return c
		}
	}
	return Check{}
}

func TestBuildFillsOnlyMeasuredValues(t *testing.T) {
	t.Parallel()
	c, run := nova(t)
	r := Build(Input{Cand: c, Run: run, Spot: spots(), Instrument: "Refractor (405 mm), ZWO ASI2600MM Pro"})
	for key, want := range map[string]string{
		"ra": "00:42:13.912", "dec": "+41:16:36.13", "discovery_time": "2026-10-12 03:05:00", "mag": "19.26 ± 0.10",
		"filter": "Clear (Luminance)", "non_detection": "night of 2026-07-13, fainter than 19.40", "host": "Andromeda Galaxy", "type": "Supernova (PSN)",
	} {
		if got := fieldOf(r, key); got.Value != want || got.Missing {
			t.Errorf("%s = %+v, want %q", key, got, want)
		}
	}
	if src := fieldOf(r, "mag").Source; !strings.Contains(src, "Light added since the reference") || !strings.Contains(src, "18.06, 18.49 before") {
		t.Errorf("the discovery magnitude is the new light, with the spot's totals as context: %q", src)
	}
	for _, f := range append(r.Facts, fieldOf(r, "host")) {
		if strings.Contains(f.Value+f.Source, "centre") || strings.Contains(f.Value+f.Source, "catalogue") {
			t.Errorf("US spelling: %+v", f)
		}
	}
	if f := fieldOf(r, "reporter"); !f.Missing || f.Value != "Not set" {
		t.Errorf("reporter %+v", f)
	}
	if !strings.Contains(fieldOf(r, "ra").Source, "±0.3″") {
		t.Errorf("uncertainty: %q", fieldOf(r, "ra").Source)
	}
	if r.TNS.CanSend || !strings.Contains(r.TNS.SendReason, "TNS bot key") {
		t.Errorf("send without a key: %v %q", r.TNS.CanSend, r.TNS.SendReason)
	}
	if len(r.TNS.Earlier) != 5 || !r.TNS.Earlier[0].Hit || r.TNS.Earlier[0].Result != "19.26 ± 0.10" || r.TNS.Earlier[4].Result != "seen, 19.00 ± 0.20" {
		t.Errorf("earlier %+v", r.TNS.Earlier)
	}
	if !strings.Contains(r.TNS.EarlierNote, "4 earlier nights") || !strings.Contains(r.TNS.EarlierNote, "seen on 1") {
		t.Errorf("note %q", r.TNS.EarlierNote)
	}
	for key, state := range map[string]string{"asteroid": CheckOK, "variable": CheckOK, "alerts": CheckOK, "tns": CheckTodo, "second_night": CheckTodo, "vetted": CheckTodo} {
		if got := checkOf(r, key); got.State != state {
			t.Errorf("check %s = %+v", key, got)
		}
	}
	if r.MPC.Applies || !r.VSX.Applies || r.VSX.Range != "18.10 to 19.00" {
		t.Errorf("mpc %+v vsx %+v", r.MPC, r.VSX)
	}

	c.NightMag, run.AstroRMS, c.Light = nil, nil, ""
	r = Build(Input{Cand: c, Run: run})
	for _, key := range []string{"discovery_time", "mag", "non_detection", "instrument"} {
		if f := fieldOf(r, key); !f.Missing || f.Value != notMeasured {
			t.Errorf("%s without data: %+v", key, f)
		}
	}
	if !strings.Contains(fieldOf(r, "ra").Source, "not measured") {
		t.Errorf("ra source %q", fieldOf(r, "ra").Source)
	}
}

func TestChecksFollowTheCatalogues(t *testing.T) {
	t.Parallel()
	c, run := nova(t)
	run.Failed = "Fink"
	f := &app.DiscoveryFollowup{State: app.FollowupConfirmed, Note: "seen again on the night of Oct 12"}
	c.Vetting = app.VetReal
	r := Build(Input{Cand: c, Run: run, Followup: f, TNSFound: []string{acsi}, Settings: Settings{TNSKey: true}})
	if a := checkOf(r, "alerts"); a.State != CheckTodo || !strings.Contains(a.Detail, "Fink could not be reached") {
		t.Errorf("alerts %+v", a)
	}
	if x := checkOf(r, "tns"); x.State != CheckBad || !strings.Contains(x.Detail, acsi) || x.LinkText == "" {
		t.Errorf("tns %+v", x)
	}
	if x := checkOf(r, "second_night"); x.State != CheckOK {
		t.Errorf("second night %+v", x)
	}
	if x := checkOf(r, "vetted"); x.State != CheckOK {
		t.Errorf("vetted %+v", x)
	}
	c.Matches = `[{"catalog":"VSX","name":"V1 And","sep_arcsec":1.2}]`
	if x := checkOf(Build(Input{Cand: c, Run: run}), "variable"); x.State != CheckBad || !strings.Contains(x.Detail, "V1 And") {
		t.Errorf("variable %+v", x)
	}
}

func TestCanSendNeedsEverything(t *testing.T) {
	t.Parallel()
	c, run := nova(t)
	st := Settings{TNSKey: true, Reporter: smith, TNSGroupID: "12", TNSInstrumentID: "34"}
	r := Build(Input{Cand: c, Run: run, Spot: spots(), Settings: st, Instrument: "x"})
	if !r.TNS.CanSend {
		t.Fatalf("can't send: %s", r.TNS.SendReason)
	}
	st.TNSInstrumentID = ""
	if r := Build(Input{Cand: c, Run: run, Spot: spots(), Settings: st, Instrument: "x"}); r.TNS.CanSend {
		t.Errorf("sends without an instrument id")
	}
	st.TNSInstrumentID = "34"
	if r := Build(Input{Cand: c, Run: run, Settings: st, Instrument: "x"}); r.TNS.CanSend || !strings.Contains(r.TNS.SendReason, "Last time not seen") {
		t.Errorf("sends without a non-detection: %q", r.TNS.SendReason)
	}
	sent := time.Now()
	if r := Build(Input{Cand: c, Run: run, Spot: spots(), Settings: st, Instrument: "x", Saved: &app.DiscoveryReport{SentAt: &sent}}); r.TNS.CanSend {
		t.Errorf("sends twice")
	}
	c.Class = discoveries.ClassNewVariable
	if r := Build(Input{Cand: c, Run: run, Spot: spots(), Settings: st, Instrument: "x"}); r.TNS.CanSend {
		t.Errorf("sends a variable star to TNS")
	}
}

func moverTrack(t *testing.T) app.DiscoveryCandidate {
	t.Helper()
	rec := discoveries.TrackRecord{TrkSub: "D9A00", RateArcmin: 0.5, PA: 90, ADES: []discoveries.ADES{
		{ObsTime: "2026-10-12T03:05:00.000Z", RA: 10.1, Dec: -2.5, RMSRA: 0.4, RMSDec: 0.4, AstCat: gaia3, Mag: ptr(18.2), RMSMag: ptr(0.1), Band: "G", PhotCat: gaia3, Mode: "CCD"},
		{ObsTime: "2026-10-12T03:11:00.000Z", RA: 10.101, Dec: -2.5, RMSRA: 0.4, RMSDec: 0.4, AstCat: gaia3, Band: "G", Mode: "CCD"},
	}}
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	return app.DiscoveryCandidate{Kind: app.DiscoveryKindMover, Class: discoveries.ClassUnknownMover, Track: string(b), Object: m31, Night: findNight()}
}

func TestADES(t *testing.T) {
	t.Parallel()
	c := moverTrack(t)
	d := mpcDraft(Input{Cand: c, Settings: Settings{Reporter: smith}, Instrument: "Refractor"})
	if !d.Applies || !strings.Contains(d.Reason, "observatory code") {
		t.Errorf("mpc %+v", d)
	}
	d = mpcDraft(Input{Cand: c, Settings: Settings{MPCCode: "Z99", Reporter: smith}})
	lines := strings.Split(strings.TrimSpace(d.PSV), "\n")
	last := lines[len(lines)-1]
	if !strings.Contains(d.PSV, "! mpcCode Z99") || !strings.HasPrefix(last, "D9A00   |CCD |Z99 |2026-10-12T03:11:00.000Z|10.101000  |-2.500000  |") {
		t.Errorf("psv:\n%s", d.PSV)
	}
	if !strings.Contains(lines[len(lines)-2], "|18.20|0.10  |G   |Gaia3") {
		t.Errorf("psv photometry: %q", lines[len(lines)-2])
	}
	var doc struct {
		Optical []struct {
			Stn string `xml:"stn"`
			Dec string `xml:"dec"`
			Mag string `xml:"mag"`
		} `xml:"obsBlock>obsData>optical"`
	}
	if err := xml.Unmarshal([]byte(d.XML), &doc); err != nil || len(doc.Optical) != 2 || doc.Optical[0].Stn != "Z99" || doc.Optical[0].Dec != "-2.500000" ||
		doc.Optical[0].Mag != "18.20" || doc.Optical[1].Mag != "" {
		t.Errorf("xml %v %+v\n%s", err, doc, d.XML)
	}
}

func TestATReport(t *testing.T) {
	t.Parallel()
	c, run := nova(t)
	st := Settings{Reporter: smith, TNSGroupID: "12", TNSInstrumentID: "34"}
	vals := Values{"at_types": {"PSN": "1", "PNV": "2"}, "filters": {"Clear-": "1"}, "units": {"ABMag": "1"}}
	rep, err := ATReport(c, run, spots(), st, vals, 8400)
	if err != nil {
		t.Fatal(err)
	}
	e := dig(t, rep, "at_report", "0")
	nd := dig(t, e, "non_detection")
	ph := dig(t, e, "photometry", "0")
	if e["discovery_datetime"] != "2026-10-12 03:05:00" || e["at_type"] != "1" || e["host_name"] != "Andromeda Galaxy" || e["reporting_groupid"] != "12" ||
		nd["limiting_flux"] != "19.40" || nd["filterid"] != "1" || ph["flux"] != "19.26" || ph["exptime"] != "8400" || ph["instrumentid"] != "34" {
		t.Errorf("report %+v", e)
	}
	if dig(t, e, "ra")["value"] != "00:42:13.912" {
		t.Errorf("ra %+v", e["ra"])
	}
	if _, err := ATReport(c, run, spots(), st, Values{}, 1); !errors.Is(err, ErrInvalid) {
		t.Errorf("no ids: %v", err)
	}
}

func dig(t *testing.T, m map[string]any, keys ...string) map[string]any {
	t.Helper()
	for _, k := range keys {
		next, ok := m[k].(map[string]any)
		if !ok {
			t.Fatalf("no %s in %v", k, m)
		}
		m = next
	}
	return m
}

func TestLastNonDetection(t *testing.T) {
	t.Parallel()
	s := spots()
	if got, ok := lastNonDetection(s, ptr(18.0)); !ok || got.Filter != "Green" {
		t.Errorf("most recent deep enough: %+v", got)
	}
	if got, ok := lastNonDetection(s, ptr(20.0)); !ok || got.Filter != lum || *got.Limit != 19.4 {
		t.Errorf("deepest when none is deep enough: %+v", got)
	}
	if _, ok := lastNonDetection(s[3:], ptr(18.0)); ok {
		t.Errorf("a detection counted as a non-detection")
	}
}

type fakeTNS struct {
	mu      sync.Mutex
	sends   int
	replies int
	key     string
}

func (f *fakeTNS) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	ok := func(w http.ResponseWriter, data string) {
		fmt.Fprintf(w, `{"id_code":200,"id_message":"OK","data":%s}`, data)
	}
	mux.HandleFunc("/get/values", func(w http.ResponseWriter, _ *http.Request) {
		ok(w, `{"at_types":[{"id":1,"name":"PSN"},{"id":2,"name":"PNV"}],"filters":[{"id":"1","name":"Clear-"}],"units":[{"id":1,"name":"ABMag"}]}`)
	})
	mux.HandleFunc("/set/bulk-report", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.sends++
		f.mu.Unlock()
		if !strings.HasPrefix(r.Header.Get("User-Agent"), `tns_marker{"name":"bot","tns_id":"7","type":"bot"}`) || r.FormValue("api_key") != f.key {
			http.Error(w, `{"id_code":401,"id_message":"Unauthorized"}`, http.StatusUnauthorized)
			return
		}
		var body map[string]any
		if err := json.Unmarshal([]byte(r.FormValue("data")), &body); err != nil || body["at_report"] == nil {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		ok(w, `{"report_id":"4321"}`)
	})
	mux.HandleFunc("/get/bulk-report-reply", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.replies++
		n := f.replies
		f.mu.Unlock()
		if r.FormValue("report_id") != "4321" {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		if n == 1 {
			fmt.Fprint(w, `{"id_code":404,"id_message":"Not Found"}`)
			return
		}
		ok(w, `{"feedback":{"at_report":[{"100":{"message":"Transient object was inserted","objname":"2026zzz"}}]}}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func reportDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := testdb.SQLite(t, "app")
	if err := db.AutoMigrate(&app.Frame{}, &app.DiscoveryRun{}, &app.DiscoveryCandidate{}, &app.DiscoveryAlert{}, &app.DiscoveryFollowup{}, &app.DiscoveryReport{}); err != nil {
		t.Fatal(err)
	}
	return db
}

type fakeMeasurer struct {
	calls int
	err   error
}

func (f *fakeMeasurer) SpotNights(_ context.Context, _ string, _, _ float64, opts discoveries.SpotOptions) ([]discoveries.SpotNight, error) {
	f.calls++
	if !opts.Before.Equal(findNight()) || opts.Nights != SpotNights || opts.PerNight != SpotPerNight {
		return nil, fmt.Errorf("options %+v", opts)
	}
	return spots(), f.err
}

type cutouts map[string][]byte

func (c cutouts) Cutout(_ context.Context, key string) ([]byte, error) {
	if b, ok := c[key]; ok {
		return b, nil
	}
	return nil, errors.New("missing")
}

func seed(t *testing.T, db *gorm.DB) int {
	t.Helper()
	c, run := nova(t)
	c.CutoutKey = "discoveries/x/c019/"
	if err := db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	exp := 300.0
	n := findNight()
	for i := range 28 {
		at := n.Add(27*time.Hour + time.Duration(i*5)*time.Minute)
		if err := db.Create(&app.Frame{Key: fmt.Sprint(i), Type: "LIGHT", Object: m31, Filter: lum, Exposure: &exp, Night: &n, DateObs: &at}).Error; err != nil {
			t.Fatal(err)
		}
	}
	return c.ID
}

func TestServiceMeasuresNamesSendsOnceAndBundles(t *testing.T) {
	t.Parallel()
	db := reportDB(t)
	id := seed(t, db)
	ctx := context.Background()
	m := &fakeMeasurer{}
	ft := &fakeTNS{key: "k"}
	srv := ft.server(t)
	s := &Service{DB: db, Measurer: m, Cutouts: cutouts{"discoveries/x/c019/night.png": []byte("png"), "discoveries/x/c019/stamp.fits": []byte("fits")},
		TNS:        TNS{Key: "k", BotID: "7", BotName: botName, BaseURL: srv.URL, ReplyWait: time.Millisecond},
		Settings:   Settings{Reporter: smith, TNSGroupID: "12", TNSInstrumentID: "34"},
		Instrument: func(context.Context, app.DiscoveryCandidate) string { return "Refractor" }}
	r, err := s.Report(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if r.Spot.State != SpotNone || r.TNS.CanSend {
		t.Fatalf("before measuring: %+v can send %v", r.Spot, r.TNS.CanSend)
	}
	if _, err := s.Measure(ctx, id, false); err != nil {
		t.Fatal(err)
	}
	for s.MeasureNext(ctx) {
	}
	if _, err := s.Measure(ctx, id, false); err != nil {
		t.Fatal(err)
	}
	s.MeasureNext(ctx)
	if r, err = s.Report(ctx, id); err != nil || r.Spot.State != SpotDone || m.calls != 1 {
		t.Fatalf("after measuring: %+v %v, %d calls", r.Spot, err, m.calls)
	}
	if !r.TNS.CanSend {
		t.Fatalf("can't send: %s", r.TNS.SendReason)
	}
	reply, err := s.SendTNS(ctx, id)
	if err != nil || reply.ReportID != "4321" || reply.Name != "AT 2026zzz" {
		t.Fatalf("send: %+v %v", reply, err)
	}
	if _, err := s.SendTNS(ctx, id); !errors.Is(err, ErrInvalid) {
		t.Errorf("second send: %v", err)
	}
	if ft.sends != 1 {
		t.Errorf("%d sends", ft.sends)
	}
	if r, err = s.Report(ctx, id); err != nil || r.TNS.Name != "AT 2026zzz" || r.TNS.NameLink != "https://www.wis-tns.org/object/2026zzz" || r.TNS.SentAt == nil {
		t.Errorf("after send: %+v %v", r.TNS, err)
	}
	var saved app.DiscoveryReport
	db.First(&saved)
	var sent map[string]any
	if err := json.Unmarshal([]byte(saved.SentReport), &sent); err != nil {
		t.Fatal(err)
	}
	ph := dig(t, sent, "at_report", "0", "photometry", "0")
	if ph["exptime"] != "8400" {
		t.Errorf("exposure %v", ph["exptime"])
	}
	if err := s.SetName(ctx, id, "  AT  2026acsi "); err != nil {
		t.Fatal(err)
	}
	if r, _ = s.Report(ctx, id); r.TNS.Name != acsi {
		t.Errorf("name %q", r.TNS.Name)
	}
	checkBundle(ctx, t, s, id)
}

func TestSendAfterAnUncertainFailureIsNotRepeated(t *testing.T) {
	t.Parallel()
	db := reportDB(t)
	id := seed(t, db)
	ctx := context.Background()
	s := &Service{DB: db, Measurer: &fakeMeasurer{}, TNS: TNS{Key: "k", BotID: "7", BotName: botName, BaseURL: "http://127.0.0.1:1"},
		Settings: Settings{Reporter: smith, TNSGroupID: "12", TNSInstrumentID: "34"}, Instrument: func(context.Context, app.DiscoveryCandidate) string { return "x" }}
	if _, err := s.Measure(ctx, id, false); err != nil {
		t.Fatal(err)
	}
	for s.MeasureNext(ctx) {
	}
	if _, err := s.SendTNS(ctx, id); !errors.Is(err, ErrTNS) {
		t.Fatalf("values from a dead TNS: %v", err)
	}
	var saved app.DiscoveryReport
	db.First(&saved)
	if saved.SentAt != nil {
		t.Errorf("marked sent when nothing went out")
	}
}

func TestTNSSearchIsCached(t *testing.T) {
	t.Parallel()
	db := reportDB(t)
	id := seed(t, db)
	ctx := context.Background()
	now := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	n := 0
	s := &Service{DB: db, Now: func() time.Time { return now }, TNS: TNS{Key: "k", BotID: "7", BotName: botName},
		Search: func(_ context.Context, _, _, radius float64) ([]string, error) {
			n++
			if radius != SearchRadius {
				return nil, errors.New("radius")
			}
			return []string{acsi}, nil
		}}
	for range 2 {
		r, err := s.Report(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if x := checkOf(r, "tns"); x.State != CheckBad {
			t.Errorf("tns check %+v", x)
		}
	}
	now = now.Add(2 * time.Hour)
	if _, err := s.Report(ctx, id); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("%d searches", n)
	}
	if _, err := s.Report(ctx, 99999); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing: %v", err)
	}
}

func checkBundle(ctx context.Context, t *testing.T, s *Service, id int) {
	t.Helper()
	name, zipped, err := s.Bundle(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(zipped), int64(len(zipped)))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		files[f.Name] = string(b)
	}
	if name != "andromeda-2026-10-11-"+fmt.Sprint(id)+".zip" || files["night.png"] != "png" || files["stamp.fits"] != "fits" ||
		!strings.HasPrefix(files["light-curve.csv"], "time_utc,filter,flux") || strings.Count(files["earlier-nights.csv"], "\n") != 5 {
		t.Errorf("bundle %s %v", name, files)
	}
}
