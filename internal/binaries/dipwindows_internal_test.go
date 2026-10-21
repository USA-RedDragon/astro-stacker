package binaries

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/planning"
	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/testdb"
	"gorm.io/gorm"
)

// cycle15523 is the dip cycle worked out for find 15523 from its 24
// firm survey dips.
func cycle15523() app.DipperEphemeris {
	return app.DipperEphemeris{SourceID: 15523, Period: 9.3564, EpochMJD: 61297.08, WindowHours: 20, DepthLow: 0.15, DepthHigh: 0.6,
		DurLowHours: 2, DurHighHours: 18, ActiveShare: 0.25}
}

// dipSite is a made-up site for the tests.
func dipSite() sky.Site { return sky.Site{Latitude: 35, Longitude: -90} }

// dipField is 15523's field.
const dipField = "Cygnis Loop Panel 1"

func TestDipWindowTimes15523(t *testing.T) {
	t.Parallel()
	e := cycle15523()
	for _, c := range []struct {
		n   int
		mid string
	}{
		{4, "2026-10-21 12:08"},
		{5, "2026-10-30 20:41"},
		{6, "2026-11-09 05:14"},
		{7, "2026-11-18 13:47"},
		{8, "2026-11-27 22:20"},
	} {
		w := windowOf(e, c.n)
		if got := w.mid.Format("2006-01-02 15:04"); got != c.mid {
			t.Errorf("window %d middle %s, want %s", c.n, got, c.mid)
		}
		if w.end.Sub(w.start) != 20*time.Hour || w.mid.Sub(w.start) != 10*time.Hour {
			t.Errorf("window %d runs %s to %s, want 10 h either side of the middle", c.n, w.start, w.end)
		}
		if cycleNear(e, w.mid.Add(4*24*time.Hour)) != c.n || cycleNear(e, w.mid.Add(-4*24*time.Hour)) != c.n {
			t.Errorf("window %d is not the nearest 4 days either side", c.n)
		}
	}
	now := time.Date(2026, 10, 20, 18, 0, 0, 0, time.UTC)
	if n := cycleFrom(e, now); n != 4 {
		t.Errorf("first window ending after %s is %d, want 4", now, n)
	}
	if n := cycleFrom(e, windowOf(e, 4).end.Add(-time.Minute)); n != 4 {
		t.Errorf("a window still open is skipped: %d", n)
	}
	if n := cycleFrom(e, windowOf(e, 4).end); n != 5 {
		t.Errorf("a window that has ended is kept: %d", n)
	}
}

// TestDarkSpansKnownAnswer checks the dark part of a window against the
// sky worked out by hand: on 2026-10-21 at 35° N, 90° W the Sun is 12°
// down from 00:14 UT, and a star at RA 312.86°, Dec +31.99° sinks under
// 30° at 05:48 UT.
func TestDarkSpansKnownAnswer(t *testing.T) {
	t.Parallel()
	from := time.Date(2026, 10, 20, 22, 0, 0, 0, time.UTC)
	to := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	spans := DarkSpans(dipSite(), 312.86, 31.99, from, to)
	if len(spans) != 1 {
		t.Fatalf("spans %v, want one", spans)
	}
	near := func(got time.Time, want string) {
		w, _ := time.Parse("2006-01-02 15:04", want)
		if d := got.Sub(w); d < -4*time.Minute || d > 4*time.Minute {
			t.Errorf("edge %s, want %s give or take 4 min", got.Format("15:04"), want)
		}
	}
	near(spans[0].Start, "2026-10-21 00:14")
	near(spans[0].End, "2026-10-21 05:48")
}

func TestDipRowOneHourRule(t *testing.T) {
	t.Parallel()
	ra, dec := 312.86, 31.99
	site := dipSite()
	night := DarkSpans(dipSite(), ra, dec, time.Date(2026, 10, 20, 22, 0, 0, 0, time.UTC), time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC))[0]
	loc, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Fatal(err)
	}
	d := dipper{st: Star{Source: app.DiscoverySource{ID: 7, Object: "Field", RA: ra, Dec: dec}}}
	for _, c := range []struct {
		dark time.Duration
		ok   bool
	}{{40 * time.Minute, false}, {59 * time.Minute, false}, {61 * time.Minute, true}, {3 * time.Hour, true}} {
		end := night.Start.Add(c.dark)
		start := end.Add(-4 * time.Hour)
		d.e = app.DipperEphemeris{Period: 9, EpochMJD: timeMJD(start.Add(2 * time.Hour)), WindowHours: 4, ActiveShare: 0.25, DepthLow: 0.1, DepthHigh: 0.5}
		in := dipInputs{site: &site, loc: loc, now: start.Add(-24 * time.Hour)}
		r := d.row(windowOf(d.e, 0), in)
		if r.Observable != c.ok || math.Abs(r.DarkMinutes-c.dark.Minutes()) > 1 {
			t.Errorf("%s of dark: observable %v with %.0f min, want %v", c.dark, r.Observable, r.DarkMinutes, c.ok)
		}
		if !c.ok && r.ObservableNote != "Not requested, under the 1 hour of usable dark a request needs" {
			t.Errorf("note %q", r.ObservableNote)
		}
		if r.Result.Label != "Not imaged yet" || r.Request != nil {
			t.Errorf("row %+v", r)
		}
	}
	noon := dipper{st: d.st, e: app.DipperEphemeris{Period: 9, EpochMJD: timeMJD(time.Date(2026, 10, 21, 18, 0, 0, 0, time.UTC)), WindowHours: 4}}
	r := noon.row(windowOf(noon.e, 0), dipInputs{site: &site, loc: loc, now: time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)})
	if r.Observable || r.DarkNote != "no dark time inside it" || r.DarkText != "" {
		t.Errorf("daytime window %+v", r)
	}
}

func TestDipTexts(t *testing.T) {
	t.Parallel()
	e := cycle15523()
	s := summaryOf(e)
	if s.WindowsText != "every 9.356 days, 20 hours wide" || s.DipsText != "0.15 to 0.6 mag, 2 to 18 hours, in about 1 window in 4" {
		t.Errorf("summary %+v", s)
	}
	if w := whyOf(e); w != "Dims in about 1 window in 4, by 0.15 to 0.6 mag for 2 to 18 hours. A dip here would back the 9.356-day cycle." {
		t.Errorf("why %q", w)
	}
	if p := pastText(e); p != "Past windows: not measured yet." {
		t.Errorf("past %q", p)
	}
	e.PastSurveys, e.PastDips, e.PastYears, e.PastActive, e.PastCovered = "ZTF, ATLAS and TESS", 25, 10, 24, 181
	if p := pastText(e); p != "Past windows from ZTF, ATLAS and TESS: 25 dips over 10 years, in 24 of about 181 covered windows." {
		t.Errorf("past %q", p)
	}
}

// appliedCommands applies every command at once and keeps them.
type appliedCommands struct {
	sent  map[string]schedcmd.Record
	order []string
}

func (f *appliedCommands) SubmitOnce(_ context.Context, id string, kind schedcmd.Kind, payload json.RawMessage, _ string) (schedcmd.Record, error) {
	if r, ok := f.sent[id]; ok {
		return r, nil
	}
	r := schedcmd.Record{ID: id, Kind: kind, Payload: payload, Status: schedcmd.StatusApplied}
	f.sent[id] = r
	f.order = append(f.order, id)
	return r, nil
}

func (f *appliedCommands) Status(_ context.Context, id string) (schedcmd.Record, error) {
	r, ok := f.sent[id]
	if !ok {
		return r, schedcmd.ErrNotFound
	}
	return r, nil
}

func (f *appliedCommands) Undo(_ context.Context, id, _ string) (schedcmd.Record, error) {
	r := f.sent[id]
	u := schedcmd.Record{ID: "undo-" + id, Status: schedcmd.StatusApplied, UndoOf: id}
	f.sent[u.ID] = u
	r.UndoneBy = u.ID
	f.sent[id] = r
	return u, nil
}

func (f *appliedCommands) creates() []schedcmd.Record {
	var out []schedcmd.Record
	for _, id := range f.order {
		if r := f.sent[id]; r.Kind == schedcmd.KindProjectCreate {
			out = append(out, r)
		}
	}
	return out
}

func f64(v float64) *float64 { return &v }

func fieldSnapshot() *planning.Snapshot {
	return &planning.Snapshot{
		Projects: []planning.Project{{ID: 3, GUID: "field-project", Name: "Cygnis Loop", State: "Active", MinimumAltitude: 15,
			Targets: []planning.Target{{GUID: "field-target", Name: dipField, RAHours: f64(20.868), Dec: f64(31.537), Rotation: f64(0),
				Plans: []planning.Plan{{TemplateID: 32, Template: "Red (auto)", Filter: redFilter, Exposure: 180, Enabled: true}}}}}},
		Templates: []planning.Template{{ID: 32, Name: "Red (auto)", Filter: redFilter, DefaultExposure: 180}},
	}
}

// handWatches are the four dip watches of 15523 made by hand before the
// app planned dip windows, with their projects' create commands.
func handWatches() []handWatch {
	return []handWatch{
		{"8a8fc07c-0e9b-5138-8b26-a8d6e48b64a5", "8412f6d7-eb74-49c8-969c-71618b3f84b2", "2026-10-21T02:02:00Z", "2026-10-21T22:02:00Z"},
		{"791ad578-2ad1-561f-b945-dbdca6583f0c", "e6160526-7ed9-496f-bc63-451f61919217", "2026-10-30T10:35:00Z", "2026-10-31T06:35:00Z"},
		{"ada08362-c4eb-521d-8b0c-6d7adf1e38b0", "4b1744aa-a8cf-4494-a1e6-8b9ea6917bd2", "2026-11-08T19:09:00Z", "2026-11-09T15:09:00Z"},
		{"0b9f2004-3047-469d-9b84-46fe3daf48c7", "34257bbf-d7e0-4d3c-a8ad-e111945fffa9", "2026-11-27T12:16:00Z", "2026-11-28T08:16:00Z"},
	}
}

type handWatch struct {
	guid, cmd  string
	start, end string
}

func dipDB(t *testing.T, every, hand bool) *gorm.DB {
	t.Helper()
	db := testdb.SQLite(t, "dips")
	if err := db.AutoMigrate(&app.DiscoverySource{}, &app.DiscoveryVet{}, &app.BinaryFacts{}, &app.BinaryBook{}, &app.ShareImage{},
		&app.EclipseRequest{}, &app.EclipseTiming{}, &app.Frame{}, &app.DipWatch{}, &app.DipperEphemeris{}, &app.DipResult{}, &schedcmd.Record{}); err != nil {
		t.Fatal(err)
	}
	g := 16.89
	src := app.DiscoverySource{ID: 15523, Object: dipField, Kind: app.DiscoveryKindChange, RA: 312.86, Dec: 31.99, GaiaG: &g,
		Night: time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC), Vetting: app.VetReal}
	if err := db.Create(&src).Error; err != nil {
		t.Fatal(err)
	}
	e := cycle15523()
	e.EveryWindow = every
	if err := db.Create(&e).Error; err != nil {
		t.Fatal(err)
	}
	if !hand {
		return db
	}
	for _, h := range handWatches() {
		start, _ := time.Parse(time.RFC3339, h.start)
		end, _ := time.Parse(time.RFC3339, h.end)
		if err := db.Create(&app.DipWatch{SourceID: 15523, ProjectGUID: h.guid, Start: start, End: end, DepthLow: 0.15, DepthHigh: 0.6, ActiveShare: 0.25}).Error; err != nil {
			t.Fatal(err)
		}
		p := schedcmd.ProjectCreatePayload{Project: schedcmd.NewProject{GUID: h.guid, Name: "Dip watch: by hand"},
			Targets: []schedcmd.NewTarget{{GUID: "t-" + h.guid, Name: dipField, Plans: []schedcmd.NewPlan{{Filter: redFilter, Exposure: 180, Desired: 120}}}}}
		raw, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&schedcmd.Record{ID: h.cmd, Kind: schedcmd.KindProjectCreate, Payload: raw, Status: schedcmd.StatusApplied,
			Title: "x", Category: "x", Destination: "x", Author: "x", CreatedAt: start.AddDate(0, 0, -10)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func dipService(db *gorm.DB, cmds Commands, now *time.Time) *Service {
	return &Service{DB: db, Commands: cmds, Now: func() time.Time { return *now },
		Site:     func(context.Context) (sky.Site, error) { return dipSite(), nil },
		Snapshot: func(context.Context) (*planning.Snapshot, error) { return fieldSnapshot(), nil }}
}

func dipRequests(t *testing.T, db *gorm.DB) []app.EclipseRequest {
	t.Helper()
	var reqs []app.EclipseRequest
	if err := db.Where("kind = ?", KindDip).Order("id").Find(&reqs).Error; err != nil {
		t.Fatal(err)
	}
	return reqs
}

// TestEveryWindowAdoptsHandWatches turns on every window for 15523 with
// its four hand-made dip watches: the four become the requests of their
// windows with their own create commands, the one window between them
// with under an hour of dark is not asked for, and no project is made.
func TestEveryWindowAdoptsHandWatches(t *testing.T) {
	t.Parallel()
	db := dipDB(t, true, true)
	now := time.Date(2026, 10, 20, 18, 0, 0, 0, time.UTC)
	cmds := &appliedCommands{sent: map[string]schedcmd.Record{}}
	svc := dipService(db, cmds, &now)
	ctx := context.Background()
	for range 2 {
		if err := svc.dipWork(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if c := cmds.creates(); len(c) != 0 {
		t.Fatalf("made %d projects, want none: the hand-made ones cover every window the site sees", len(c))
	}
	reqs := dipRequests(t, db)
	if len(reqs) != 4 {
		t.Fatalf("%d requests, want the 4 adopted: %+v", len(reqs), reqs)
	}
	e := cycle15523()
	for i, r := range reqs {
		h := handWatches()[i]
		w := windowOf(e, []int{4, 5, 6, 8}[i])
		if r.ProjectGUID != h.guid || r.CreateCmd != h.cmd || r.State != app.EclipseSent || r.Auto || !r.Mid.Equal(w.mid) || r.Subs != 120 || r.Filter != redFilter {
			t.Errorf("request %d: %+v, want the hand watch %s of window %d", i, r, h.guid, w.n)
		}
	}
	var watches []app.DipWatch
	if err := db.Order("start").Find(&watches).Error; err != nil {
		t.Fatal(err)
	}
	if len(watches) != 4 {
		t.Fatalf("%d dip watches, want the same 4", len(watches))
	}
	if w := windowOf(e, 4); !watches[0].Start.Equal(w.start) || !watches[0].End.Equal(w.end) {
		t.Errorf("the first watch runs %s to %s, want the planned window %s to %s", watches[0].Start, watches[0].End, w.start, w.end)
	}
	v, err := svc.Dipper(ctx, 15523)
	if err != nil {
		t.Fatal(err)
	}
	asked := 0
	for _, w := range v.Windows {
		if w.Request != nil {
			asked++
		}
		if w.N == 7 && (w.Observable || w.Request != nil) {
			t.Errorf("window 7 %+v, want listed and not requested", w)
		}
	}
	if asked != 4 || len(v.Windows) != dipAhead+3 || !v.EveryWindow {
		t.Errorf("view: %d asked of %d windows, every %v", asked, len(v.Windows), v.EveryWindow)
	}
}

// TestEveryWindowMakesOneProjectPerWindow asks for 15523's windows with
// no hand-made watches: one project per window with an hour of dark, a
// second look adds none, and a cancelled window is not asked again.
func TestEveryWindowMakesOneProjectPerWindow(t *testing.T) {
	t.Parallel()
	db := dipDB(t, true, false)
	now := time.Date(2026, 10, 20, 18, 0, 0, 0, time.UTC)
	cmds := &appliedCommands{sent: map[string]schedcmd.Record{}}
	svc := dipService(db, cmds, &now)
	ctx := context.Background()
	if err := svc.dipWork(ctx); err != nil {
		t.Fatal(err)
	}
	if err := svc.dipWork(ctx); err != nil {
		t.Fatal(err)
	}
	if err := svc.prune(ctx, nil); err != nil {
		t.Fatal(err)
	}
	reqs := dipRequests(t, db)
	if len(reqs) != 4 || len(cmds.creates()) != 4 {
		t.Fatalf("%d requests and %d projects, want 4 each (windows 4, 5, 6 and 8)", len(reqs), len(cmds.creates()))
	}
	e := cycle15523()
	for i, r := range reqs {
		w := windowOf(e, []int{4, 5, 6, 8}[i])
		if !r.Auto || r.State != app.EclipseSent || !r.Mid.Equal(w.mid) || r.Start.Before(w.start) || r.End.After(w.end) || r.End.Sub(r.Start) < dipMinDark {
			t.Errorf("request %d %+v, want the app's request over the dark part of window %d", i, r, w.n)
		}
		var p schedcmd.ProjectCreatePayload
		if err := json.Unmarshal(cmds.sent["dip-request-"+r.ProjectGUID].Payload, &p); err != nil {
			t.Fatal(err)
		}
		tw := p.Targets[0].Window
		if tw == nil || tw.Priority != schedcmd.EventPriorityHigh || !tw.Start.Equal(r.Start) || !tw.End.Equal(r.End) || p.Targets[0].Plans[0].Exposure != 180 {
			t.Errorf("project %+v", p)
		}
		var wt app.DipWatch
		if err := db.Where("project_guid = ?", r.ProjectGUID).First(&wt).Error; err != nil || !wt.Start.Equal(w.start) || wt.DepthHigh != 0.6 {
			t.Errorf("watch of request %d: %+v %v", i, wt, err)
		}
	}
}

// TestCancelledDipWindowStaysCancelled cancels one of the app's window
// requests: its project is taken back, the window is not asked for again,
// and asking by hand for a window already asked for gives its request.
func TestCancelledDipWindowStaysCancelled(t *testing.T) {
	t.Parallel()
	db := dipDB(t, true, false)
	now := time.Date(2026, 10, 20, 18, 0, 0, 0, time.UTC)
	cmds := &appliedCommands{sent: map[string]schedcmd.Record{}}
	svc := dipService(db, cmds, &now)
	ctx := context.Background()
	if err := svc.dipWork(ctx); err != nil {
		t.Fatal(err)
	}
	reqs := dipRequests(t, db)
	if len(reqs) != 4 {
		t.Fatalf("%d requests, want 4", len(reqs))
	}
	e := cycle15523()
	if _, err := svc.CancelRequest(ctx, reqs[1].ID); err != nil {
		t.Fatal(err)
	}
	if r, _ := cmds.Status(ctx, reqs[1].CreateCmd); r.UndoneBy == "" {
		t.Error("the cancelled window's project was not taken back")
	}
	if err := svc.dipWork(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(dipRequests(t, db)); n != 4 || len(cmds.creates()) != 4 {
		t.Errorf("%d requests after the cancel, want the window left cancelled", n)
	}
	v, err := svc.Dipper(ctx, 15523)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range v.Windows {
		if w.N == 5 && w.Request != nil {
			t.Errorf("cancelled window shows %+v", w.Request)
		}
	}
	r, err := svc.RequestDipWindow(ctx, 15523, windowOf(e, 7).mid)
	if !errors.Is(err, ErrNoDark) || r.ID != 0 {
		t.Errorf("window 7 by hand: %+v %v, want refused for under an hour of dark", r, err)
	}
	r, err = svc.RequestDipWindow(ctx, 15523, windowOf(e, 4).mid.Add(time.Hour))
	if err != nil || r.ID != reqs[0].ID {
		t.Errorf("asking again for window 4: %+v %v, want its request", r, err)
	}
}

// TestNewCycleRedraftsMovedRequests moves the cycle a day later: the
// user's request whose window has not begun is withdrawn and asked for
// at its new time.
func TestNewCycleRedraftsMovedRequests(t *testing.T) {
	t.Parallel()
	db := dipDB(t, false, false)
	now := time.Date(2026, 10, 20, 18, 0, 0, 0, time.UTC)
	cmds := &appliedCommands{sent: map[string]schedcmd.Record{}}
	svc := dipService(db, cmds, &now)
	ctx := context.Background()
	e := cycle15523()
	first, err := svc.RequestDipWindow(ctx, 15523, windowOf(e, 5).mid)
	if err != nil || first.State != app.EclipseSent {
		t.Fatalf("%+v %v", first, err)
	}
	if err := svc.dipWork(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(dipRequests(t, db)); n != 1 {
		t.Fatalf("%d requests with the cycle unchanged, want 1", n)
	}
	e.EpochMJD++
	if err := db.Save(&e).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.dipWork(ctx); err != nil {
		t.Fatal(err)
	}
	reqs := dipRequests(t, db)
	if len(reqs) != 2 || reqs[0].State != app.EclipseMoved || reqs[0].Settle != "" && reqs[0].Settle != "withdraw" || reqs[1].State != app.EclipseSent || !reqs[1].Mid.Equal(windowOf(e, 5).mid) {
		t.Fatalf("requests %+v, want the first moved and the window asked again", reqs)
	}
	if r, _ := cmds.Status(ctx, first.CreateCmd); r.UndoneBy == "" {
		t.Error("the moved request's project was not taken back")
	}
}

func dipPoints(depth func(i int) float64, n int) []LivePoint {
	t0 := time.Date(2026, 10, 21, 3, 0, 0, 0, time.UTC)
	out := make([]LivePoint, n)
	for i := range out {
		noise := 0.01 * math.Sin(float64(i)*1.7)
		out[i] = LivePoint{FrameID: i, Time: t0.Add(time.Duration(i) * 195 * time.Second), Filter: redFilter, Mag: 15 + noise + depth(i), Err: 0.01, Comp: 0.002 * math.Cos(float64(i))}
	}
	return out
}

func TestJudgeResultStates(t *testing.T) {
	t.Parallel()
	loc := time.UTC
	flat := judgeResult(app.DipResult{}, dipPoints(func(int) float64 { return 0 }, 40), goalOf(0.15, 2), loc)
	if flat.State != app.DipNone || flat.Note != fmt.Sprintf("steady to %.2f mag", flat.Smallest) || flat.Smallest < dipFloor || flat.Measured != 40 {
		t.Errorf("flat %+v", flat)
	}
	dip := judgeResult(app.DipResult{}, dipPoints(func(i int) float64 {
		if i >= 20 && i < 32 {
			return 0.3
		}
		return 0
	}, 40), goalOf(0.15, 2), loc)
	if dip.State != app.DipSeen || math.Abs(dip.Depth-0.3) > 0.03 || dip.Note != fmt.Sprintf("faded %.2f mag from 4:05 AM", dip.Depth) {
		t.Errorf("dip %+v", dip)
	}
	few := judgeResult(app.DipResult{}, dipPoints(func(int) float64 { return 0 }, 3), goalOf(0.15, 2), loc)
	if few.State != app.DipUnsure || few.Note != "3 subs gave a measurement, 6 are needed" {
		t.Errorf("few %+v", few)
	}
	for _, c := range []struct {
		over, known bool
		subs        int
		state       string
		label       string
	}{
		{false, true, 0, DipWaiting, "Not imaged yet"},
		{false, true, 5, DipWaiting, "Being imaged, follow it on the Now page"},
		{true, true, 0, DipNotImaged, "Not imaged"},
		{true, true, 12, DipMeasuring, "Being measured"},
		{true, false, 0, DipMeasuring, "Not measured yet"},
	} {
		if r := resultOf(app.DipResult{}, c.over, c.known, c.subs); r.State != c.state || r.Label != c.label {
			t.Errorf("%+v: %+v", c, r)
		}
	}
	if r := resultOf(dip, true, true, 40); r.State != app.DipSeen || r.Label != "Dip seen" || r.Note != dip.Note {
		t.Errorf("stored dip %+v", r)
	}
	if r := resultOf(flat, true, true, 40); r.Label != "No dip" {
		t.Errorf("stored flat %+v", r)
	}
}

// TestNewCycleUnderWayKeepsOneProject moves the cycle 0.45 days earlier while a
// window is being imaged: the request under way stays the window's
// request and no second project is made.
func TestNewCycleUnderWayKeepsOneProject(t *testing.T) {
	t.Parallel()
	db := dipDB(t, true, false)
	now := time.Date(2026, 10, 20, 18, 0, 0, 0, time.UTC)
	cmds := &appliedCommands{sent: map[string]schedcmd.Record{}}
	svc := dipService(db, cmds, &now)
	ctx := context.Background()
	if err := svc.dipWork(ctx); err != nil {
		t.Fatal(err)
	}
	first := dipRequests(t, db)[0]
	now = first.Start.Add(time.Hour)
	e := cycle15523()
	e.EveryWindow = true
	e.EpochMJD -= 0.45
	if err := db.Save(&e).Error; err != nil {
		t.Fatal(err)
	}
	before := len(cmds.creates())
	if err := svc.dipWork(ctx); err != nil {
		t.Fatal(err)
	}
	for _, c := range cmds.creates()[before:] {
		var p schedcmd.ProjectCreatePayload
		if err := json.Unmarshal(c.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if w := p.Targets[0].Window; w.Start.Before(first.End) && first.Start.Before(w.End) {
			t.Errorf("made %s over the request under way", p.Project.Name)
		}
	}
}
