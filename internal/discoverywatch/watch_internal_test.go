package discoverywatch

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/discoverysource"
	"github.com/USA-RedDragon/astro-stacker/internal/planning"
	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/testdb"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/period"
	"gorm.io/gorm"
)

var start = time.Date(2026, 10, 19, 22, 0, 0, 0, time.UTC)

// barnardsE is source 3635 as it stood when Watching was approved: a new
// variable seen on one own night, real at low confidence from three ZTF
// fades with nearby stars steady, nothing pending.
func barnardsE() discoverysource.Summary {
	return discoverysource.Summary{
		Source:  app.DiscoverySource{ID: 3635, Object: "Barnard's E", Kind: app.DiscoveryKindChange},
		Class:   discoveries.ClassNewVariable,
		Best:    app.DiscoveryCandidate{Group: discoveries.GroupBroadband},
		Members: []app.DiscoveryCandidate{{Night: time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)}},
		Seen:    1,
		Vet:     &app.DiscoveryVet{SourceID: 3635, State: vetting.StateDone, Suggested: app.VetReal, Confidence: "low"},
	}
}

var ztfDates = []string{"2019-06-01", "2020-07-02", "2021-08-03", "2022-09-04", "2023-10-05", "2024-11-06"}

// ztf is a report with steady ZTF fade nights, Fades counting them only
// from two up as the period package does.
func ztf(steady int) *vetting.Report {
	rep := &vetting.Report{FadesRead: true, FadeNightsRead: true, ZTFFadeNights: ztfDates[:steady]}
	if steady >= 2 {
		rep.Fades = &period.Fades{Nights: steady, Steady: steady, Stars: 30}
	}
	return rep
}

// own adds an own-frame candidate on the night of day in October 2026.
func own(sum discoverysource.Summary, day int) discoverysource.Summary {
	sum.Members = append(slices.Clone(sum.Members), app.DiscoveryCandidate{Night: time.Date(2026, 10, day, 0, 0, 0, 0, time.UTC)})
	sum.Seen = len(sum.Members)
	return sum
}

func nightsOf(sum discoverysource.Summary, steady int) []string {
	n, _ := FadeNights(sum, ztf(steady))
	return n
}

func watched(t *testing.T, sum discoverysource.Summary) discoverysource.Summary {
	t.Helper()
	fades, known := FadeNights(sum, ztf(3))
	out, ok := Advance(sum, nil, fades, known, start)
	if !ok || !out.Started {
		t.Fatalf("%d did not start watching: %+v", sum.Source.ID, out)
	}
	w := out.Watch
	sum.Watch = &w
	return sum
}

func TestRealLowWithNothingPendingIsWatched(t *testing.T) {
	t.Parallel()
	sum := barnardsE()
	if !sum.Watching() || sum.Look() || !sum.In(discoverysource.ViewWatching) || sum.In(discoverysource.ViewLook) {
		t.Fatalf("3635 should be in Watching, not Needs a look: watching %v look %v", sum.Watching(), sum.Look())
	}
	sum = watched(t, sum)
	w := sum.Watch
	if w.FadesAtStart == nil || *w.FadesAtStart != 4 || *Needed(w) != 6 || !w.StartedAt.Equal(start) || w.Group != discoveries.GroupBroadband {
		t.Errorf("watch %+v, want 4 fade nights at the start and 6 needed", w)
	}
	if !sum.Watching() || !Asking(w) {
		t.Error("a new watch should stay in Watching and ask for imaging")
	}
}

func TestPendingSurveyStaysInNeedsALook(t *testing.T) {
	t.Parallel()
	sum := barnardsE()
	sum.Vet.Pending = "ATLAS"
	if sum.Watching() || !sum.Look() {
		t.Fatalf("an ATLAS answer still pending keeps it in Needs a look: watching %v look %v", sum.Watching(), sum.Look())
	}
	if _, ok := Advance(sum, nil, nightsOf(sum, 3), true, start); ok {
		t.Error("no watch should start while a survey is pending")
	}
	sum.Vet.Pending = ""
	sum = watched(t, sum)
	sum.Vet.Pending = "ATLAS"
	if !sum.Watching() {
		t.Error("a watched find's weekly refresh waiting on ATLAS should not leave Watching")
	}
}

func TestMediumOrHighStaysInNeedsALook(t *testing.T) {
	t.Parallel()
	for _, c := range []string{"medium", "high"} {
		sum := barnardsE()
		sum.Vet.Confidence = c
		if sum.Watching() || !sum.Look() {
			t.Errorf("real at %s confidence belongs in Needs a look", c)
		}
	}
}

func TestRisingToMediumReturnsToNeedsALook(t *testing.T) {
	t.Parallel()
	sum := watched(t, barnardsE())
	sum.Vet.Confidence = "medium"
	out, ok := Advance(sum, sum.Watch, nightsOf(sum, 3), true, start.Add(7*24*time.Hour))
	if !ok || out.Watch.EndedAt == nil || out.Watch.EndReason != app.WatchEndRose || !out.Cancel {
		t.Fatalf("rising to medium should end the watch and withdraw imaging: %+v", out)
	}
	sum.Watch = &out.Watch
	if sum.Watching() || !sum.Look() {
		t.Error("it should be back in Needs a look")
	}
}

func TestArtifactGoesToLikelyArtifacts(t *testing.T) {
	t.Parallel()
	sum := watched(t, barnardsE())
	sum.Vet.Suggested, sum.Vet.Confidence = app.VetArtifact, "medium"
	sum.LikelyArtifact = true
	out, _ := Advance(sum, sum.Watch, nightsOf(sum, 3), true, start.Add(7*24*time.Hour))
	if out.Watch.EndReason != app.WatchEndArtifact || !out.Cancel {
		t.Fatalf("an artifact verdict should end the watch: %+v", out.Watch)
	}
	sum.Watch = &out.Watch
	if sum.Watching() || sum.Look() || !sum.In(discoverysource.ViewArtifacts) {
		t.Error("it should be in Likely artifacts only")
	}
}

func TestTwoMoreFadeNightsPromote(t *testing.T) {
	t.Parallel()
	sum := watched(t, barnardsE())
	later := start.Add(10 * 24 * time.Hour)
	sum = own(sum, 25)
	out, _ := Advance(sum, sum.Watch, nightsOf(sum, 3), true, later)
	if out.Watch.EndedAt != nil || *out.Watch.Fades != 5 || !out.Watch.LastFadeAt.Equal(later) {
		t.Fatalf("one more fade night should keep watching and count it: %+v", out.Watch)
	}
	sum.Watch = &out.Watch
	if !sum.Watching() {
		t.Error("5 of 6 should stay in Watching")
	}
	sum = own(sum, 28)
	fades, known := FadeNights(sum, ztf(3))
	out, _ = Advance(sum, sum.Watch, fades, known, later.Add(24*time.Hour))
	if len(fades) != 6 || out.Watch.EndReason != app.WatchEndFades || !out.Cancel {
		t.Fatalf("6 of 6 fade nights should move it to Needs a look: fades %v %+v", fades, out.Watch)
	}
	sum.Watch = &out.Watch
	if sum.Watching() || !sum.Look() {
		t.Error("it should be in Needs a look")
	}
	if out, _ := Advance(sum, sum.Watch, fades, known, later.Add(48*time.Hour)); out.Changed || out.Started {
		t.Error("a promoted find must not start watching again")
	}
}

// TestOneNewZTFNightCountsOnce is the known answer for the fade count: a
// find with one old backed ZTF night has no Fades, so counting Fades
// alone made one new ZTF night add two. Counting nights by date, the old
// night is counted from the start and the new one adds exactly one.
func TestOneNewZTFNightCountsOnce(t *testing.T) {
	t.Parallel()
	sum := barnardsE()
	first, known := FadeNights(sum, ztf(1))
	out, ok := Advance(sum, nil, first, known, start)
	if !ok || len(first) != 2 || *out.Watch.FadesAtStart != 2 || *Needed(&out.Watch) != 4 {
		t.Fatalf("start %v %+v, want the own night and the lone ZTF night", first, out.Watch)
	}
	sum.Watch = &out.Watch
	if r := ztf(2); !r.Fades.Confirms() || r.Fades.Steady != 2 {
		t.Fatalf("the second backed night makes Fades count both: %+v", r.Fades)
	}
	next, _ := FadeNights(sum, ztf(2))
	out, _ = Advance(sum, sum.Watch, next, true, start.Add(7*24*time.Hour))
	if *out.Watch.Fades != 3 || out.Watch.EndedAt != nil {
		t.Fatalf("one new ZTF night: %d fade nights, ended %v; want 2+1 and still watching", *out.Watch.Fades, out.Watch.EndReason)
	}
	sum.Watch = &out.Watch
	both := own(sum, 25)
	same, _ := FadeNights(both, &vetting.Report{FadesRead: true, FadeNightsRead: true, ZTFFadeNights: append([]string{"2026-10-25"}, ztfDates[:2]...)})
	if len(same) != 4 {
		t.Errorf("a night seen in own frames and ZTF both counts once: %v", same)
	}
	out, _ = Advance(both, sum.Watch, same, true, start.Add(8*24*time.Hour))
	if out.Watch.EndReason != app.WatchEndFades {
		t.Errorf("two nights not at the start should move it on: %+v", out.Watch)
	}
}

func TestOldWatchTakesItsNextLookAsTheStart(t *testing.T) {
	t.Parallel()
	sum := barnardsE()
	four, five := 4, 5
	sum.Watch = &app.DiscoveryWatch{SourceID: 3635, StartedAt: start, LastFadeAt: start, FadesAtStart: &four, Fades: &five}
	if _, known := FadeNights(sum, &vetting.Report{FadesRead: true, Fades: &period.Fades{Steady: 3}}); known {
		t.Fatal("a report from before the ZTF nights were listed must not count")
	}
	out, _ := Advance(sum, sum.Watch, nightsOf(sum, 3), true, start.Add(time.Hour))
	if !out.Changed || out.Watch.StartNights == "" || *out.Watch.FadesAtStart != 4 || *out.Watch.Fades != 4 || out.Watch.EndedAt != nil {
		t.Errorf("an old watch should record its nights now as the start: %+v", out.Watch)
	}
}

func TestFieldFadesDoNotCount(t *testing.T) {
	t.Parallel()
	sum := barnardsE()
	rep := &vetting.Report{FadesRead: true, FadeNightsRead: true, ZTFFadeNights: ztfDates[:1], Fades: &period.Fades{Nights: 5, Steady: 1, Field: 4, Stars: 30}}
	if n, ok := FadeNights(sum, rep); !ok || len(n) != 2 {
		t.Errorf("fade nights %v %v, want the own night plus the one ZTF night with nearby stars steady", n, ok)
	}
	if _, ok := FadeNights(sum, &vetting.Report{}); ok {
		t.Error("a report that did not compare the fades must not count them")
	}
}

func TestUnknownFadesFilledInLater(t *testing.T) {
	t.Parallel()
	sum := barnardsE()
	out, _ := Advance(sum, nil, nil, false, start)
	if out.Watch.FadesAtStart != nil || Needed(&out.Watch) != nil {
		t.Fatalf("fade nights should be unknown: %+v", out.Watch)
	}
	sum.Watch = &out.Watch
	out, _ = Advance(sum, sum.Watch, nightsOf(sum, 3), true, start.Add(time.Hour))
	if out.Watch.FadesAtStart == nil || *out.Watch.FadesAtStart != 4 || out.Watch.EndedAt != nil {
		t.Errorf("the first count should become the start: %+v", out.Watch)
	}
}

func TestQuietNinetyDaysStopsImaging(t *testing.T) {
	t.Parallel()
	sum := watched(t, barnardsE())
	out, _ := Advance(sum, sum.Watch, nightsOf(sum, 3), true, start.Add(89*24*time.Hour))
	if out.Watch.ImagingStoppedAt != nil || out.Cancel {
		t.Fatalf("89 quiet days should keep asking: %+v", out.Watch)
	}
	out, _ = Advance(sum, sum.Watch, nightsOf(sum, 3), true, start.Add(QuietStop))
	if out.Watch.ImagingStoppedAt == nil || out.Watch.StopReason != app.WatchStopQuiet || !out.Cancel || out.Watch.EndedAt != nil {
		t.Fatalf("90 quiet days should stop imaging but keep watching: %+v", out.Watch)
	}
	sum.Watch = &out.Watch
	if !sum.Watching() || Asking(sum.Watch) {
		t.Error("it should stay in Watching without asking")
	}
	sum = watched(t, barnardsE())
	out, _ = Advance(sum, sum.Watch, nightsOf(sum, 4), true, start.Add(60*24*time.Hour))
	sum.Watch = &out.Watch
	out, _ = Advance(sum, sum.Watch, nightsOf(sum, 4), true, start.Add(QuietStop))
	if out.Watch.ImagingStoppedAt != nil {
		t.Error("a new fade on day 60 should restart the 90 days")
	}
}

func TestUserVettingEndsTheWatch(t *testing.T) {
	t.Parallel()
	sum := watched(t, barnardsE())
	sum.Source.Vetting = app.VetReal
	out, _ := Advance(sum, sum.Watch, nightsOf(sum, 3), true, start.Add(time.Hour))
	if out.Watch.EndReason != app.WatchEndUser || !out.Cancel {
		t.Errorf("marking it should end the watch: %+v", out.Watch)
	}
}

func TestPlanNightsSpreadOverThreeWeeks(t *testing.T) {
	t.Parallel()
	nights := PlanNights(start, time.UTC)
	if len(nights) != NightsAsked || NightsAsked != 5 {
		t.Fatalf("%d nights, want 5", len(nights))
	}
	want := []string{"2026-10-19", "2026-10-24", "2026-10-29", "2026-11-03", "2026-11-08"}
	for i, n := range nights {
		if n.Format(time.DateOnly) != want[i] {
			t.Errorf("night %d %s, want %s", i, n.Format(time.DateOnly), want[i])
		}
	}
	if span := nights[4].Sub(nights[0]); span >= SpanDays*24*time.Hour {
		t.Errorf("span %v is not inside 3 weeks", span)
	}
	morning := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	if n := PlanNights(morning, time.UTC)[0]; n.Format(time.DateOnly) != "2026-10-20" {
		t.Errorf("after a night ends, the first night is the coming one, got %s", n.Format(time.DateOnly))
	}
	s, e := NightWindow(nights[0], time.UTC)
	if !s.Equal(time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)) || !e.Equal(time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("window %v to %v", s, e)
	}
}

func schedDB(t *testing.T) *gorm.DB {
	t.Helper()
	sched := testdb.SQLite(t, "sched")
	stmts := []string{
		`create table project ("Id" integer primary key, "profileId" text, name text, description text, state integer, priority integer, minimumtime integer, minimumaltitude real, "maximumAltitude" real, "isMosaic" integer, enablegrader integer, guid text, meridianwindow integer, usecustomhorizon integer, horizonoffset real)`,
		`create table target ("Id" integer primary key, name text, active integer, ra real, "dec" real, rotation real, projectid integer, guid text)`,
		`create table exposuretemplate ("Id" integer primary key, "profileId" text, name text, filtername text, gain integer, "offset" integer, bin integer, twilightlevel integer, moonavoidanceenabled integer, moonavoidanceseparation real, moonavoidancewidth integer, maximumhumidity real, defaultexposure real, moonrelaxscale real, moondownenabled integer, ditherevery integer, guid text)`,
		`create table exposureplan ("Id" integer primary key, "profileId" text, exposure real, desired integer, acquired integer, accepted integer, targetid integer, "exposureTemplateId" integer, enabled integer, guid text)`,
		`create table ruleweight ("Id" integer primary key, name text, weight real, projectid integer)`,
		`create table acquiredimage ("Id" integer primary key, "projectId" integer, "targetId" integer, acquireddate integer, filtername text, "gradingStatus" integer, metadata text)`,
		`insert into project values (5,'p','Field',null,1,1,30,25,0,0,1,'pg5',40,1,3)`,
		`insert into target values (12,'Barnard''s E',1,19.6,-7.9,12.5,5,'g-field')`,
		`insert into exposuretemplate values (1,'p','H-a','H-a',100,10,1,2,1,45,7,85,600,0,0,1,'t1'),(3,'p','Luminance','Luminance',0,50,1,0,1,30,7,85,180,0,0,1,'t3')`,
		`insert into exposureplan values (40,'p',-1,100,50,40,12,1,1,'e40'),(41,'p',-1,100,80,70,12,3,1,'e41')`,
	}
	for _, s := range stmts {
		if err := sched.Exec(s).Error; err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	return sched
}

func TestDraftAsksForGapsOnly(t *testing.T) {
	t.Parallel()
	db := appDB(t)
	snap, err := planning.Load(context.Background(), schedDB(t), db, planning.Inputs{})
	if err != nil {
		t.Fatal(err)
	}
	night := PlanNights(start, time.UTC)[1]
	s, e := NightWindow(night, time.UTC)
	n := &app.DiscoveryWatchNight{SourceID: 3635, Night: night, Start: s, End: e}
	p, why := Draft(snap, "g-field", "Barnard's E", n, discoveries.GroupBroadband)
	if p == nil {
		t.Fatalf("no draft: %s", why)
	}
	if p.Project.Priority != 1 || !strings.HasPrefix(p.Project.Name, ProjectPrefix) {
		t.Errorf("project %+v, want normal priority", p.Project)
	}
	tg := p.Targets[0]
	if tg.Name != "Barnard's E" || tg.Rotation != 12.5 {
		t.Errorf("target %+v, want the field's own name and framing", tg)
	}
	if tg.Window == nil || tg.Window.Priority != schedcmd.EventPriorityNormal || !tg.Window.Start.Equal(s) || !tg.Window.End.Equal(e) {
		t.Errorf("window %+v, want a normal window over the night", tg.Window)
	}
	if len(tg.Plans) != 1 || tg.Plans[0].Filter != "Luminance" || tg.Plans[0].Exposure != 180 || tg.Plans[0].Desired != 20 || n.Minutes != 60 {
		t.Errorf("plans %+v minutes %d, want 20 × 180 s Luminance for 60 minutes", tg.Plans, n.Minutes)
	}
	b, _ := json.Marshal(p)
	if err := schedcmd.ProjectCreateSpec().Validate(b); err != nil {
		t.Errorf("the draft does not validate: %v", err)
	}
	if p, why := Draft(snap, "g-field", "Barnard's E", n, "O-III"); p != nil || !strings.Contains(why, "no exposure plan") {
		t.Errorf("O-III draft: %v %q", p, why)
	}
}

// fakeCommands applies each command at once, or with pending holds new
// creates waiting at an observatory it cannot reach until land.
type fakeCommands struct {
	mu      sync.Mutex
	sent    map[string]schedcmd.Record
	undone  []string
	pending bool
}

func (f *fakeCommands) SubmitOnce(_ context.Context, id string, kind schedcmd.Kind, payload json.RawMessage, _ string) (schedcmd.Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sent == nil {
		f.sent = map[string]schedcmd.Record{}
	}
	if r, ok := f.sent[id]; ok {
		return r, nil
	}
	r := schedcmd.Record{ID: id, Kind: kind, Payload: payload, Status: schedcmd.StatusApplied}
	if f.pending && kind == schedcmd.KindProjectCreate {
		r.Status = schedcmd.StatusPending
	}
	f.sent[id] = r
	return r, nil
}

func (f *fakeCommands) Status(_ context.Context, id string) (schedcmd.Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.sent[id]
	if !ok {
		return r, schedcmd.ErrNotFound
	}
	return r, nil
}

// Undo cancels nothing while the observatory is unreachable, and takes a
// landed command back with an undo that applies at once.
func (f *fakeCommands) Undo(_ context.Context, id, _ string) (schedcmd.Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.undone = append(f.undone, id)
	r := f.sent[id]
	if r.Status.Waiting() {
		return r, schedcmd.ErrUnreachable
	}
	u := schedcmd.Record{ID: "undo-" + id, Status: schedcmd.StatusApplied, UndoOf: id}
	f.sent[u.ID] = u
	r.UndoneBy = u.ID
	f.sent[id] = r
	return u, nil
}

// land applies every waiting create.
func (f *fakeCommands) land() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, r := range f.sent {
		if r.Status.Waiting() {
			r.Status = schedcmd.StatusApplied
			f.sent[id] = r
		}
	}
}

func (f *fakeCommands) kinds(kind schedcmd.Kind) []schedcmd.Record {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []schedcmd.Record
	for _, r := range f.sent {
		if r.Kind == kind {
			out = append(out, r)
		}
	}
	return out
}

func appDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := testdb.SQLite(t, "app")
	if err := db.AutoMigrate(&app.Frame{}, &app.GoalMeasurement{}, &app.DiscoveryRun{}, &app.DiscoveryCandidate{}, &app.DiscoverySource{},
		&app.DiscoveryVet{}, &app.VetBuild{}, &app.DiscoveryFollowup{}, &app.DiscoveryWatch{}, &app.DiscoveryWatchNight{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func seed(t *testing.T, db *gorm.DB) {
	t.Helper()
	night := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	run := app.DiscoveryRun{Object: "Barnard's E", Group: discoveries.GroupBroadband, Night: night, State: app.DiscoveryDone}
	if err := db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	src := app.DiscoverySource{ID: 3635, Object: "Barnard's E", Kind: app.DiscoveryKindChange, Night: night}
	if err := db.Create(&src).Error; err != nil {
		t.Fatal(err)
	}
	sid := src.ID
	c := app.DiscoveryCandidate{RunID: run.ID, SourceID: &sid, Object: "Barnard's E", Group: discoveries.GroupBroadband, Night: night,
		Kind: app.DiscoveryKindChange, Class: discoveries.ClassNewVariable}
	if err := db.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	rep, _ := json.Marshal(ztf(3))
	fin := start.Add(-time.Hour)
	v := app.DiscoveryVet{SourceID: sid, State: vetting.StateDone, Version: vetting.Version, Suggested: app.VetReal, Confidence: "low",
		Report: string(rep), FinishedAt: &fin}
	if err := db.Create(&v).Error; err != nil {
		t.Fatal(err)
	}
}

func TestServiceAsksStopsAndWithdraws(t *testing.T) {
	t.Parallel()
	db, sched := appDB(t), schedDB(t)
	seed(t, db)
	now := start
	cmds := &fakeCommands{}
	svc := &Service{DB: db, Commands: cmds, Loc: time.UTC, Now: func() time.Time { return now },
		Snapshot: func(ctx context.Context) (*planning.Snapshot, error) {
			return planning.Load(ctx, sched, db, planning.Inputs{})
		}}
	ctx := context.Background()
	if err := svc.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	var nights []app.DiscoveryWatchNight
	db.Order("night").Find(&nights)
	if len(nights) != 5 {
		t.Fatalf("%d nights asked, want 5", len(nights))
	}
	for _, n := range nights {
		if n.State != app.EclipseSent || n.Minutes != 60 || n.CreateCmd == "" {
			t.Errorf("night %s: %+v", n.Night.Format(time.DateOnly), n)
		}
	}
	if err := svc.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(cmds.kinds(schedcmd.KindProjectCreate)) != 5 {
		t.Errorf("a second look sent %d commands, want the same 5", len(cmds.kinds(schedcmd.KindProjectCreate)))
	}
	list, counts, err := discoverysource.List(ctx, db, discoverysource.Filter{View: discoverysource.ViewWatching})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || counts.Watching != 1 || counts.Look != 0 {
		t.Fatalf("list %d, counts %+v; want 3635 in Watching only", len(list), counts)
	}
	views, err := Views(ctx, db, list)
	if err != nil {
		t.Fatal(err)
	}
	v := views[3635]
	if v == nil || *v.FadeNights != 4 || *v.FadeNightsNeeded != 6 || v.NightsPlanned != 5 || v.NightsImaged != 0 || v.Imaging != ImagingAsking ||
		v.NextSurveyCheck == nil || *v.NextSurveyCheck != "2026-10-26" || len(v.Nights) != 5 || v.Nights[0].GotMinutes != nil || v.Nights[0].Result != "" {
		t.Fatalf("view %+v", v)
	}
	exp := 180.0
	night0 := nights[0].Night
	frames := []app.Frame{{Key: "a", Type: "LIGHT", Object: "Barnard's E", Exposure: &exp, TSTarget: &nights[0].TargetGUID, Night: &night0},
		{Key: "b", Type: "LIGHT", Object: "Barnard's E", Exposure: &exp, TSTarget: &nights[0].TargetGUID, Night: &night0}}
	if err := db.Create(&frames).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&app.DiscoveryRun{Object: "Barnard's E", Group: discoveries.GroupBroadband, Night: night0, State: app.DiscoveryDone}).Error; err != nil {
		t.Fatal(err)
	}
	views, _ = Views(ctx, db, list)
	if n := views[3635].Nights[0]; n.GotMinutes == nil || *n.GotMinutes != 6 || n.Result != ResultSteady || views[3635].NightsImaged != 1 {
		t.Errorf("night 0 %+v, want 6 minutes got and steady", n)
	}
	now = now.Add(2 * time.Hour)
	if err := svc.Stop(ctx, 3635); err != nil {
		t.Fatal(err)
	}
	db.Order("night").Find(&nights)
	for _, n := range nights {
		if n.State != app.EclipseCancel {
			t.Errorf("night %s still %s after stop", n.Night.Format(time.DateOnly), n.State)
		}
	}
	if len(cmds.undone) != 4 || slices.Contains(cmds.undone, nights[0].CreateCmd) {
		t.Errorf("taken back %v; want the 4 nights to come, not the one under way, which is closed instead", cmds.undone)
	}
	list, counts, _ = discoverysource.List(ctx, db, discoverysource.Filter{View: discoverysource.ViewWatching})
	views, _ = Views(ctx, db, list)
	if counts.Watching != 1 || views[3635].Imaging != ImagingStoppedUser {
		t.Errorf("after stop: counts %+v view %+v; want it still in Watching, not asking", counts, views[3635])
	}
	if err := svc.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(cmds.kinds(schedcmd.KindProjectCreate)) != 5 {
		t.Errorf("a stopped watch asked again: %d commands", len(cmds.kinds(schedcmd.KindProjectCreate)))
	}
}

func TestComingBackKeepsTheWatch(t *testing.T) {
	t.Parallel()
	sum := watched(t, barnardsE())
	sum.Vet.Suggested, sum.Vet.Confidence = app.VetUnsure, ""
	sum.Unsure = true
	out, _ := Advance(sum, sum.Watch, nightsOf(sum, 3), true, start.Add(24*time.Hour))
	if out.Watch.EndReason != app.WatchEndOther {
		t.Fatalf("not sure should leave Watching: %+v", out.Watch)
	}
	sum.Watch = &out.Watch
	sum.Vet.Suggested, sum.Vet.Confidence = app.VetReal, "low"
	sum.Unsure = false
	sum.Seen = 2
	out, _ = Advance(sum, sum.Watch, nightsOf(sum, 4), true, start.Add(48*time.Hour))
	if out.Started || out.Watch.EndedAt != nil || *out.Watch.FadesAtStart != 4 || !out.Watch.StartedAt.Equal(start) || !out.Watch.LastFadeAt.Equal(start) {
		t.Errorf("coming back should take up the old watch: %+v", out.Watch)
	}
}

func TestNewSetOfNightsOnceTheLastIsOver(t *testing.T) {
	t.Parallel()
	db, sched := appDB(t), schedDB(t)
	seed(t, db)
	now := start
	cmds := &fakeCommands{}
	svc := &Service{DB: db, Commands: cmds, Loc: time.UTC, Now: func() time.Time { return now },
		Snapshot: func(ctx context.Context) (*planning.Snapshot, error) {
			return planning.Load(ctx, sched, db, planning.Inputs{})
		}}
	ctx := context.Background()
	if err := svc.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	now = start.Add(20 * 24 * time.Hour)
	if err := svc.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(cmds.kinds(schedcmd.KindProjectCreate)) != 5 {
		t.Fatalf("%d commands while the last night is still to come, want 5", len(cmds.kinds(schedcmd.KindProjectCreate)))
	}
	now = start.Add(22 * 24 * time.Hour)
	if err := svc.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if err := svc.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	var nights []app.DiscoveryWatchNight
	db.Order("night").Find(&nights)
	var w app.DiscoveryWatch
	db.First(&w)
	if len(nights) != 10 || w.Batch != 1 || nights[9].State != app.EclipseSent {
		t.Fatalf("%d nights, batch %d; want a second set of 5 sent", len(nights), w.Batch)
	}
	now = start.Add(QuietStop)
	if err := svc.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	db.First(&w)
	if w.ImagingStoppedAt == nil || w.StopReason != app.WatchStopQuiet {
		t.Errorf("90 quiet days should stop imaging: %+v", w)
	}
}

// TestStopBeforeTheCreatesLand stops a watch while its creates still wait
// on an observatory that cannot be reached, so neither the night under
// way nor those to come can be taken back. When the creates land, the
// next looks take the nights to come back and close the one under way,
// then stop owing anything.
func TestStopBeforeTheCreatesLand(t *testing.T) {
	t.Parallel()
	db, sched := appDB(t), schedDB(t)
	seed(t, db)
	now := start
	cmds := &fakeCommands{pending: true}
	var landed []planning.Project
	svc := &Service{DB: db, Commands: cmds, Loc: time.UTC, Now: func() time.Time { return now },
		Snapshot: func(ctx context.Context) (*planning.Snapshot, error) {
			snap, err := planning.Load(ctx, sched, db, planning.Inputs{})
			if err == nil {
				snap.Projects = append(snap.Projects, landed...)
			}
			return snap, err
		}}
	ctx := context.Background()
	if err := svc.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Hour)
	if err := svc.Stop(ctx, 3635); err != nil {
		t.Fatal(err)
	}
	var nights []app.DiscoveryWatchNight
	db.Order("night").Find(&nights)
	for _, n := range nights {
		if n.State != app.EclipseCancel || n.Settle != "withdraw" {
			t.Fatalf("night %s: %s owing %q, want cancelled and still owing its withdraw", n.Night.Format(time.DateOnly), n.State, n.Settle)
		}
	}
	if err := svc.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	cmds.land()
	for _, n := range nights {
		landed = append(landed, planning.Project{ID: 900 + n.ID, GUID: n.ProjectGUID, Name: "Watch", State: "Active"})
	}
	if err := svc.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	closes := cmds.kinds(schedcmd.KindProjectEdit)
	if len(closes) != 1 || !strings.Contains(string(closes[0].Payload), nights[0].ProjectGUID) {
		t.Fatalf("closes %+v, want the night under way closed once it landed", closes)
	}
	for _, n := range nights[1:] {
		if r, _ := cmds.Status(ctx, n.CreateCmd); r.UndoneBy == "" {
			t.Errorf("night %s landed after the stop and was not taken back", n.Night.Format(time.DateOnly))
		}
	}
	landed[0].State = "Closed"
	if err := svc.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	db.Order("night").Find(&nights)
	for _, n := range nights {
		if n.Settle != "" || n.State != app.EclipseCancel {
			t.Errorf("night %s: %s still owing %q", n.Night.Format(time.DateOnly), n.State, n.Settle)
		}
	}
	if nights[0].CloseCmd == "" {
		t.Error("the night under way should keep its close command")
	}
}

// TestNightEndClosesALateCreate ends a night while its create still waits;
// the close is owed until the project shows up, then sent.
func TestNightEndClosesALateCreate(t *testing.T) {
	t.Parallel()
	db, sched := appDB(t), schedDB(t)
	seed(t, db)
	now := start
	cmds := &fakeCommands{pending: true}
	var landed []planning.Project
	svc := &Service{DB: db, Commands: cmds, Loc: time.UTC, Now: func() time.Time { return now },
		Snapshot: func(ctx context.Context) (*planning.Snapshot, error) {
			snap, err := planning.Load(ctx, sched, db, planning.Inputs{})
			if err == nil {
				snap.Projects = append(snap.Projects, landed...)
			}
			return snap, err
		}}
	ctx := context.Background()
	if err := svc.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	var first app.DiscoveryWatchNight
	db.Order("night").First(&first)
	now = first.End.Add(2 * time.Hour)
	if err := svc.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	db.First(&first, first.ID)
	if first.State != app.EclipseDone || first.Settle != "close" {
		t.Fatalf("passed night %s owing %q, want done and owing its close", first.State, first.Settle)
	}
	cmds.land()
	landed = append(landed, planning.Project{ID: 901, GUID: first.ProjectGUID, Name: "Watch", State: "Active"})
	if err := svc.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if closes := cmds.kinds(schedcmd.KindProjectEdit); len(closes) != 1 || !strings.Contains(string(closes[0].Payload), first.ProjectGUID) {
		t.Fatalf("closes %+v, want the late project closed", closes)
	}
	if err := svc.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	db.First(&first, first.ID)
	if first.Settle != "" || first.CloseCmd == "" {
		t.Errorf("night owing %q close %q, want settled by its applied close", first.Settle, first.CloseCmd)
	}
}

func TestOldReportIsVettedAgain(t *testing.T) {
	t.Parallel()
	db := appDB(t)
	seed(t, db)
	rep, _ := json.Marshal(&vetting.Report{FadesRead: true, Fades: &period.Fades{Nights: 3, Steady: 3, Stars: 30}})
	if err := db.Model(&app.DiscoveryVet{}).Where("source_id = ?", 3635).Update("report", string(rep)).Error; err != nil {
		t.Fatal(err)
	}
	svc := &Service{DB: db, Loc: time.UTC, Now: func() time.Time { return start }}
	if err := svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	var v app.DiscoveryVet
	db.First(&v, "source_id = ?", 3635)
	var w app.DiscoveryWatch
	db.First(&w)
	if v.State != vetting.StateQueued || w.FadesAtStart != nil {
		t.Errorf("vet %s, start %v; want the report from before the ZTF nights were listed vetted again before counting", v.State, w.FadesAtStart)
	}
}
