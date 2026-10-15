package discoveryreport

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/tnsmirror"
)

const andromedaGalaxy = "Andromeda Galaxy"

func tnsIndex(t *testing.T) *tnsmirror.Index {
	t.Helper()
	f, err := os.Open("../tnsmirror/testdata/tns_public_objects.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	file, err := tnsmirror.Parse(f)
	if err != nil {
		t.Fatal(err)
	}
	return tnsmirror.NewIndex(file.Objects, "20261014", file.From)
}

func localAt(t *testing.T, c app.DiscoveryCandidate) *LocalSearch {
	t.Helper()
	ix := tnsIndex(t)
	hits, ok := discoveries.LocalTNS(func() *tnsmirror.Index { return ix })(c.RA, c.Dec, SearchRadius)
	if !ok {
		t.Fatal("index not loaded")
	}
	return &LocalSearch{AsOf: ix.AsOf, Hits: hits}
}

// earlySpots are Andromeda's nights at AT 2026acsi's place before its TNS
// discovery on 2026-09-22.660: Luminance limits of 19.6 on 2026-09-01 and
// 19.5 on 2026-09-15, and a detection at 18.9 on 2026-09-20.
func earlySpots() []discoveries.SpotNight {
	d := func(day int) time.Time { return time.Date(2026, 9, day, 0, 0, 0, 0, time.UTC) }
	return []discoveries.SpotNight{
		{Night: d(15), Filter: lum, Subs: 6, Limit: ptr(19.5), Mid: d(16).Add(6 * time.Hour), Exposure: 360},
		{Night: d(20), Filter: lum, Subs: 6, Detected: true, SNR: 8, Mag: ptr(18.9), MagErr: ptr(0.14), Mid: d(21).Add(6 * time.Hour), Exposure: 360},
		{Night: d(1), Filter: lum, Subs: 6, Limit: ptr(19.6), Mid: d(2).Add(6 * time.Hour), Exposure: 360},
	}
}

func m31Nova(t *testing.T) (app.DiscoveryCandidate, app.DiscoveryRun) {
	t.Helper()
	c, run := nova(t)
	c.ID, c.Class = 18554, discoveries.ClassNova
	c.RA, c.Dec = 10.557969552390773, 41.27669384597613
	return c, run
}

func assertNeutral(t *testing.T, texts ...string) {
	t.Helper()
	for _, s := range texts {
		for _, w := range []string{"credit", "first to", "before it was reported", "missed", "reported first", "scooped"} {
			if strings.Contains(strings.ToLower(s), w) {
				t.Errorf("%q says %q", s, w)
			}
		}
	}
}

// Candidate 18554 is AT 2026acsi: with the local TNS copy loaded the TNS
// check is automatic and names it, the discovery report is closed, and our
// July limits are too old to be early data.
func TestLocalTNSCopyNamesTheM31Nova(t *testing.T) {
	t.Parallel()
	c, run := m31Nova(t)
	in := Input{Cand: c, Run: run, Spot: spots(), Settings: Settings{Reporter: smith, TNSGroupID: "12", TNSInstrumentID: "34", TNSKey: true}, Local: localAt(t, c)}
	r := Build(in)
	ck := checkOf(r, "tns")
	if ck.State != CheckBad || !strings.Contains(ck.Detail, "Already in TNS: AT 2026acsi (nova candidate, XOSS, Sep 22)") || !strings.Contains(ck.Detail, "automatically") {
		t.Errorf("TNS check %+v", ck)
	}
	if r.Known == nil || r.Known.Text != "already known: AT 2026acsi (nova candidate, XOSS, Sep 22)" || r.Known.Name != acsi || r.Known.Link != "https://www.wis-tns.org/object/2026acsi" {
		t.Fatalf("known %+v", r.Known)
	}
	if r.Known.Early || r.TNSFollowup.Applies || r.TNSFollowup.CanSend {
		t.Errorf("July limits made early data: %+v %+v", r.Known, r.TNSFollowup)
	}
	if r.TNS.CanSend || !strings.HasPrefix(r.TNS.SendReason, "Already in TNS as AT 2026acsi") {
		t.Errorf("discovery report still open: %v %q", r.TNS.CanSend, r.TNS.SendReason)
	}
	far := c
	far.RA += 0.01
	ck = checkOf(Build(Input{Cand: far, Run: run, Local: localAt(t, far)}), "tns")
	if ck.State != CheckOK || !strings.Contains(ck.Detail, "local TNS copy of Oct 14 00:00 UT") {
		t.Errorf("empty spot check %+v", ck)
	}
	ck = checkOf(Build(Input{Cand: c, Run: run}), "tns")
	if ck.State != CheckTodo {
		t.Errorf("without the copy or a key the check is %+v", ck)
	}
}

// Detections and a deep limit from before AT 2026acsi's discovery make it
// early data and draft a photometry report for the existing object.
func TestEarlyDataDraftsAPhotometryReport(t *testing.T) {
	t.Parallel()
	c, run := m31Nova(t)
	in := Input{Cand: c, Run: run, Spot: earlySpots(), Settings: Settings{Reporter: smith, TNSGroupID: "12", TNSInstrumentID: "34", TNSKey: true},
		Local: localAt(t, c), Exposure: 8400}
	r := Build(in)
	if r.Known == nil || !r.Known.Early || r.Known.EarlyText != discoveries.EarlyText || len(r.Known.EarlyBasis) != 3 {
		t.Fatalf("known %+v", r.Known)
	}
	f := r.TNSFollowup
	if !f.Applies || !f.CanSend || f.Name != acsi || len(f.Points) != 2 || f.NonDetection != "night of 2026-09-15, Luminance, fainter than 19.50" {
		t.Fatalf("follow-up %+v", f)
	}
	assertNeutral(t, append([]string{r.Known.Text, r.Known.EarlyText, f.SendReason}, r.Known.EarlyBasis...)...)
	vals := Values{"at_types": {atPSN: "1", atPNV: "2"}, "filters": {"Clear-": "1"}, "units": {"ABMag": "1"}}
	rep, err := FollowupReport(in, vals)
	if err != nil {
		t.Fatal(err)
	}
	e := dig(t, rep, "at_report", "0")
	nd := dig(t, e, "non_detection")
	p0, p1 := dig(t, e, "photometry", "0"), dig(t, e, "photometry", "1")
	if _, extra := dig(t, e, "photometry")["2"]; extra {
		t.Errorf("limits sent as photometry: %+v", dig(t, e, "photometry"))
	}
	if e["discovery_datetime"] != "2026-09-21 06:00:00" || e["at_type"] != "2" || e["host_name"] != andromedaGalaxy || nd["limiting_flux"] != "19.50" ||
		nd["obsdate"] != "2026-09-16 06:00:00" {
		t.Errorf("report %+v", e)
	}
	if p0["flux"] != "18.90" || p0["flux_error"] != "0.14" || p0["limiting_flux"] != "" || p1["flux"] != "19.26" || p1["exptime"] != "8400" {
		t.Errorf("photometry %+v %+v", p0, p1)
	}
	remarks, _ := e["remarks"].(string)
	if remarks != "Photometry of AT 2026acsi from our images" {
		t.Errorf("remarks %q", remarks)
	}
	assertNeutral(t, remarks)
	in.Settings.TNSKey = false
	if _, err := FollowupReport(in, vals); !errors.Is(err, ErrInvalid) {
		t.Errorf("sent without a key: %v", err)
	}
}

// A candidate the comparison already matched to TNS as early data needs no
// local copy for the page.
func TestStoredTNSMatchDrivesThePage(t *testing.T) {
	t.Parallel()
	c, run := m31Nova(t)
	c.Class = discoveries.ClassKnownTransient
	seen := time.Date(2026, 9, 21, 4, 0, 0, 0, time.UTC)
	m := []discoveries.Match{{Catalog: "galaxy", Name: andromedaGalaxy, Type: "Local Group galaxy", SepArcsec: 344}, {Catalog: "TNS", Name: acsi, SepArcsec: 0.3,
		TNS: &discoveries.TNSEntry{Name: acsi, Group: "XOSS", Discovered: time.Date(2026, 9, 22, 15, 50, 24, 0, time.UTC), Mag: ptr(17.4), OurClass: discoveries.ClassNova,
			Seen: seen, EarlyData: true}}}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	c.Matches = string(b)
	r := Build(Input{Cand: c, Run: run, Settings: Settings{TNSKey: true}})
	if r.Known == nil || !r.Known.Early || r.Known.Text != "already known: AT 2026acsi (nova candidate, XOSS, Sep 22)" {
		t.Fatalf("known %+v", r.Known)
	}
	if !r.TNSFollowup.Applies || r.TNSFollowup.CanSend || !strings.Contains(r.TNSFollowup.SendReason, "reporter") {
		t.Errorf("follow-up %+v", r.TNSFollowup)
	}
}

type earlyMeasurer struct{}

func (earlyMeasurer) SpotNights(context.Context, string, float64, float64, discoveries.SpotOptions) ([]discoveries.SpotNight, error) {
	return earlySpots(), nil
}

func TestServiceSendsFollowupOnceWithoutSearching(t *testing.T) {
	t.Parallel()
	db := reportDB(t)
	id := seed(t, db)
	if err := db.Model(&app.DiscoveryCandidate{}).Where("id = ?", id).Updates(map[string]any{"class": discoveries.ClassNova}).Error; err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ft := &fakeTNS{key: "k"}
	srv := ft.server(t)
	ix := tnsIndex(t)
	searches := 0
	s := &Service{DB: db, Measurer: earlyMeasurer{}, TNS: TNS{Key: "k", BotID: "7", BotName: botName, BaseURL: srv.URL, ReplyWait: time.Millisecond},
		Settings: Settings{Reporter: smith, TNSGroupID: "12", TNSInstrumentID: "34"}, TNSIndex: func() *tnsmirror.Index { return ix },
		Search: func(context.Context, float64, float64, float64) ([]string, error) { searches++; return nil, nil }}
	if _, err := s.Measure(ctx, id, false); err != nil {
		t.Fatal(err)
	}
	measured(t, s)
	r, err := s.Report(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if searches != 0 || checkOf(r, "tns").State != CheckBad {
		t.Errorf("%d TNS API searches; check %+v", searches, checkOf(r, "tns"))
	}
	if !r.TNSFollowup.CanSend {
		t.Fatalf("can't send: %s", r.TNSFollowup.SendReason)
	}
	if _, err := s.SendTNS(ctx, id); !errors.Is(err, ErrInvalid) {
		t.Errorf("a discovery report of a known object went out: %v", err)
	}
	if _, err := s.SendFollowup(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SendFollowup(ctx, id); !errors.Is(err, ErrInvalid) {
		t.Errorf("second send: %v", err)
	}
	if ft.sends != 1 {
		t.Errorf("%d sends", ft.sends)
	}
	var saved app.DiscoveryReport
	db.First(&saved)
	if saved.FollowupSentAt == nil || saved.SentAt != nil || !strings.Contains(saved.FollowupReport, `"discovery_datetime":"2026-09-21 06:00:00"`) {
		t.Errorf("saved %+v", saved)
	}
	if r, _ = s.Report(ctx, id); r.TNSFollowup.SentAt == nil || r.TNSFollowup.CanSend {
		t.Errorf("after send %+v", r.TNSFollowup)
	}
}

// Our own reported object is never early data for us, and TNS's own SN
// prefix types a photometry report when our class does not.
func TestOwnReportIsNotEarlyAndTNSTypesTheReport(t *testing.T) {
	t.Parallel()
	c, run := m31Nova(t)
	sent := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	in := Input{Cand: c, Run: run, Spot: earlySpots(), Settings: Settings{TNSKey: true}, Local: localAt(t, c), Saved: &app.DiscoveryReport{SentAt: &sent}}
	if k := Build(in).Known; k == nil || k.Early || len(k.EarlyBasis) != 0 {
		t.Errorf("own report: %+v", k)
	}
	in.Saved = &app.DiscoveryReport{TNSName: " AT  2026acsi"}
	if k := Build(in).Known; k == nil || k.Early {
		t.Errorf("own named report: %+v", k)
	}
	for _, tc := range []struct {
		e    discoveries.TNSEntry
		want string
	}{
		{discoveries.TNSEntry{Name: "SN 2026xyz", Type: "SN Ia", OurClass: discoveries.ClassNewVariable}, atPSN},
		{discoveries.TNSEntry{Name: acsi, Type: "Nova"}, atPNV},
		{discoveries.TNSEntry{Name: acsi, OurClass: discoveries.ClassNova}, atPNV},
		{discoveries.TNSEntry{Name: "AT 2026q"}, "Other"},
	} {
		if got := atTypeOf(tc.e); got != tc.want {
			t.Errorf("%+v: %s, want %s", tc.e, got, tc.want)
		}
	}
	red := earlySpots()
	for i := range red {
		red[i].Filter = "Red"
	}
	r := Build(Input{Cand: c, Run: run, Spot: red, Settings: Settings{Reporter: smith, TNSGroupID: "12", TNSInstrumentID: "34", TNSKey: true}, Local: localAt(t, c)})
	if r.Known == nil || !r.Known.Early || r.TNSFollowup.CanSend || !strings.Contains(r.TNSFollowup.SendReason, "Luminance non-detection") {
		t.Errorf("Red-only early data: %+v %+v", r.Known, r.TNSFollowup)
	}
}
