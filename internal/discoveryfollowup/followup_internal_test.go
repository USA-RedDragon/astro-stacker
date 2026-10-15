package discoveryfollowup

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/planning"
	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/testdb"
	"gorm.io/gorm"
)

// SchedDB is a scheduler database with one project, Field, whose target
// Field has a Luminance and an H-a plan.
func SchedDB(t *testing.T) *gorm.DB {
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
		`insert into target values (12,'Field',1,0.70386,41.2767,12.5,5,'g-field')`,
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

func appDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := testdb.SQLite(t, "app")
	if err := db.AutoMigrate(&app.Frame{}, &app.GoalMeasurement{}, &app.DiscoveryRun{}, &app.DiscoveryCandidate{}, &app.DiscoveryAlert{},
		&app.DiscoveryFollowup{}); err != nil {
		t.Fatal(err)
	}
	return db
}

type fakeCommands struct {
	mu     sync.Mutex
	sent   map[string]schedcmd.Record
	order  []string
	status schedcmd.Status
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
	f.sent[id] = r
	f.order = append(f.order, id)
	return r, nil
}

func (f *fakeCommands) Status(_ context.Context, id string) (schedcmd.Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.sent[id]
	if f.status != "" {
		r.Status, r.Message = f.status, "web editing is off"
	}
	return r, nil
}

const fieldName = "Field"

func nightN(d int) time.Time { return time.Date(2026, 10, 11+d, 0, 0, 0, 0, time.UTC) }

func service(t *testing.T, db, sched *gorm.DB, cmds Commands, now *time.Time) *Service {
	t.Helper()
	return &Service{DB: db, Commands: cmds, Loc: time.UTC, Now: func() time.Time { return *now },
		Snapshot: func(ctx context.Context) (*planning.Snapshot, error) {
			return planning.Load(ctx, sched, db, planning.Inputs{})
		}}
}

func TestDesired(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		exp  float64
		want int
	}{{300, 6}, {180, 10}, {60, 30}, {0, 6}, {1, maxDesired}} {
		if got := Desired(tc.exp); got != tc.want {
			t.Errorf("Desired(%g) = %d, want %d", tc.exp, got, tc.want)
		}
	}
}

func TestTonightAndWindowEnd(t *testing.T) {
	t.Parallel()
	loc, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Fatal(err)
	}
	if got := Tonight(time.Date(2026, 10, 15, 8, 0, 0, 0, time.UTC), loc); !got.Equal(nightN(3)) {
		t.Errorf("3 AM CDT is the night of %v", got)
	}
	if got := Tonight(time.Date(2026, 10, 15, 0, 16, 0, 0, time.UTC), loc); !got.Equal(nightN(3)) {
		t.Errorf("7 PM CDT is the night of %v", got)
	}
	if got := WindowEnd(nightN(5), loc); !got.Equal(time.Date(2026, 10, 17, 17, 0, 0, 0, time.UTC)) {
		t.Errorf("window end %v", got)
	}
}

func TestPredict(t *testing.T) {
	t.Parallel()
	ra, dec, moved := Predict(10, 40, 1, 90, time.Hour)
	if math.Abs(moved-60) > 1e-9 || math.Abs(dec-40) > 1e-9 || math.Abs((ra-10)*math.Cos(40*math.Pi/180)*3600-60) > 1e-6 {
		t.Errorf("east 60″: %v %v %v", ra, dec, moved)
	}
	_, dec, _ = Predict(10, 40, 2, 0, 30*time.Minute)
	if math.Abs((dec-40)*3600-60) > 1e-6 {
		t.Errorf("north 60″: %v", dec)
	}
}

func light(points ...[2]float64) string {
	ls := make([]discoveries.LightPoint, 0, len(points))
	for _, p := range points {
		ls = append(ls, discoveries.LightPoint{FrameID: int(p[0]), Flux: p[1], FluxErr: 1})
	}
	b, err := json.Marshal(ls)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func TestJudgeChange(t *testing.T) {
	t.Parallel()
	f := &app.DiscoveryFollowup{Kind: app.DiscoveryKindChange, Sign: 1, RA: 10.5, Dec: 41.2}
	run := app.DiscoveryRun{Night: nightN(1)}
	near := app.DiscoveryCandidate{ID: 7, Kind: app.DiscoveryKindChange, Sign: 1, RA: 10.5 + 1.0/3600, Dec: 41.2, Class: discoveries.ClassKnownTransient}
	if v := Judge(f, run, []app.DiscoveryCandidate{near}, nil, false, 0); !v.Decided || !v.Seen || *v.SeenID != 7 {
		t.Errorf("later night, same place: %+v", v)
	}
	fading := near
	fading.Sign = -1
	far := near
	far.RA = 10.5 + 5.0/3600
	if v := Judge(f, run, []app.DiscoveryCandidate{fading, far}, nil, false, 0); !v.Decided || v.Seen {
		t.Errorf("later night, nothing there: %+v", v)
	}
	ours := map[int]bool{11: true, 12: true, 13: true, 14: true}
	near.Light = light([2]float64{1, 9}, [2]float64{11, 5}, [2]float64{12, 4}, [2]float64{13, 6}, [2]float64{14, 1})
	if v := Judge(f, run, []app.DiscoveryCandidate{near}, ours, true, 0); !v.Seen || !strings.Contains(v.Note, "3 of 4") {
		t.Errorf("same night, shows in 3 of 4: %+v", v)
	}
	near.Light = light([2]float64{1, 9}, [2]float64{11, 5}, [2]float64{12, 1}, [2]float64{13, 1}, [2]float64{14, 1})
	if v := Judge(f, run, []app.DiscoveryCandidate{near}, ours, true, 0); !v.Decided || v.Seen {
		t.Errorf("same night, shows in 1 of 4: %+v", v)
	}
	near.Light = light([2]float64{1, 9})
	if v := Judge(f, run, []app.DiscoveryCandidate{near}, ours, true, 0); !v.Decided || v.Seen {
		t.Errorf("same night, not measured on the follow-up subs: %+v", v)
	}
}

func TestShallowLookLeavesItOpen(t *testing.T) {
	t.Parallel()
	mag, depth := 19.5, 18.7
	f := &app.DiscoveryFollowup{Kind: app.DiscoveryKindChange, Sign: 1, RA: 10.5, Dec: 41.2, Mag: &mag}
	if v := Judge(f, app.DiscoveryRun{Night: nightN(1), Depth: &depth}, nil, nil, false, 0); v.Decided || !strings.Contains(v.Note, "not deep enough") {
		t.Errorf("shallow night: %+v", v)
	}
	deep := 20.5
	if v := Judge(f, app.DiscoveryRun{Night: nightN(1), Depth: &deep}, nil, nil, false, 0); !v.Decided || v.Seen {
		t.Errorf("deep night: %+v", v)
	}
}

func TestFirstNight(t *testing.T) {
	t.Parallel()
	loc, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Fatal(err)
	}
	if got := FirstNight(time.Date(2026, 10, 12, 13, 0, 0, 0, time.UTC), loc); !got.Equal(nightN(1)) {
		t.Errorf("8 AM CDT after the night of Oct 11: %v", got)
	}
	if got := FirstNight(time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC), loc); !got.Equal(nightN(0)) {
		t.Errorf("3 AM CDT in the night of Oct 11: %v", got)
	}
	if got := FirstNight(time.Date(2026, 10, 12, 20, 0, 0, 0, time.UTC), loc); !got.Equal(nightN(1)) {
		t.Errorf("3 PM CDT on Oct 12: %v", got)
	}
}

func TestJudgeMover(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 12, 5, 0, 0, 0, time.UTC)
	rate, pa := 0.5, 90.0
	f := &app.DiscoveryFollowup{Kind: app.DiscoveryKindMover, RA: 10, Dec: 40, At: &at, RateArcmin: &rate, PA: &pa}
	later := at.Add(24 * time.Hour)
	ra, dec, _ := Predict(10, 40, rate, pa, 24*time.Hour)
	track := func(ra, dec float64) string {
		b, err := json.Marshal(discoveries.TrackRecord{ADES: []discoveries.ADES{{ObsTime: later.Format("2006-01-02T15:04:05.000Z"), RA: ra, Dec: dec, FrameID: 3}}})
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	on := app.DiscoveryCandidate{ID: 4, Kind: app.DiscoveryKindMover, Track: track(ra, dec+5.0/3600)}
	if v := Judge(f, app.DiscoveryRun{Night: nightN(2)}, []app.DiscoveryCandidate{on}, nil, false, 0); !v.Seen {
		t.Errorf("on the path: %+v", v)
	}
	off := app.DiscoveryCandidate{ID: 5, Kind: app.DiscoveryKindMover, Track: track(10, 40)}
	mid := later
	inField := app.DiscoveryRun{Night: nightN(2), MidTime: &mid, FieldRA: 10.1, FieldDec: 40}
	if v := Judge(f, inField, []app.DiscoveryCandidate{off}, nil, false, 0.5); !v.Decided || v.Seen {
		t.Errorf("off the path in the field: %+v", v)
	}
	gone := app.DiscoveryRun{Night: nightN(2), MidTime: &mid, FieldRA: 10, FieldDec: 40}
	if v := Judge(f, gone, []app.DiscoveryCandidate{off}, nil, false, 0.1); v.Decided || !strings.Contains(v.Note, "out of the imaged field") {
		t.Errorf("moved out of the field: %+v", v)
	}
	f.At = nil
	if v := Judge(f, inField, nil, nil, false, 0.5); v.Decided {
		t.Errorf("no track decided: %+v", v)
	}
}

func TestDraftCopiesTheTargetAndLimits(t *testing.T) {
	t.Parallel()
	db, sched := appDB(t), SchedDB(t)
	snap, err := planning.Load(context.Background(), sched, db, planning.Inputs{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 12, 1, 0, 0, 0, time.UTC)
	f := &app.DiscoveryFollowup{Object: fieldName, Group: discoveries.GroupBroadband, Night: nightN(0), Until: nightN(2), ProjectGUID: ProjectGUID(fieldName, discoveries.GroupBroadband, nightN(0))}
	p, pick, why := Draft(snap, f, "g-field", now, time.UTC)
	if p == nil {
		t.Fatalf("no draft: %s", why)
	}
	checkDraft(t, p, pick, now)
	if _, err := json.Marshal(p); err != nil {
		t.Fatal(err)
	}
	if err := schedcmd.ProjectCreateSpec().Validate(mustJSON(t, p)); err != nil {
		t.Errorf("the draft does not validate: %v", err)
	}
	f.Group = "O-III"
	if p, _, why := Draft(snap, f, "g-field", now, time.UTC); p != nil || !strings.Contains(why, "no exposure plan") {
		t.Errorf("O-III draft: %v %q", p, why)
	}
}

func checkDraft(t *testing.T, p *schedcmd.ProjectCreatePayload, pick Pick, now time.Time) {
	t.Helper()
	tg := p.Targets[0]
	if tg.Name != fieldName || math.Abs(tg.RAHours-0.70386) > 1e-9 || tg.Dec != 41.2767 || tg.Rotation != 12.5 {
		t.Errorf("target %+v", tg)
	}
	if tg.Window == nil || tg.Window.Priority != schedcmd.EventPriorityHigh || tg.Window.IgnoreMoon || !tg.Window.Start.Equal(now) ||
		!tg.Window.End.Equal(time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("window %+v", tg.Window)
	}
	if len(tg.Plans) != 1 || tg.Plans[0].TemplateID != 3 || tg.Plans[0].Exposure != 180 || tg.Plans[0].Desired != 10 || pick.Subs != 10 {
		t.Errorf("plans %+v pick %+v", tg.Plans, pick)
	}
	if p.Project.Priority != 2 || p.Project.State != 1 || p.Project.MinimumAltitude != 25 || !strings.HasPrefix(p.Project.Name, ProjectPrefix) {
		t.Errorf("project %+v", p.Project)
	}
	if s := p.Project.ProjectSettings; s.MeridianWindow == nil || *s.MeridianWindow != 40 || s.HorizonOffset == nil || *s.HorizonOffset != 3 ||
		s.UseCustomHorizon == nil || !*s.UseCustomHorizon {
		t.Errorf("limits not kept: %+v", s)
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func seedFind(t *testing.T, db *gorm.DB) app.DiscoveryAlert {
	t.Helper()
	exp := 180.0
	for i := range 3 {
		at := nightN(0).Add(time.Duration(4*60+i*4) * time.Minute)
		n := nightN(0)
		g := "g-field"
		if err := db.Create(&app.Frame{Key: fmt.Sprintf("f%d", i), Type: "LIGHT", Object: fieldName, Filter: "Luminance", Exposure: &exp, DateObs: &at, Night: &n, TSTarget: &g}).Error; err != nil {
			t.Fatal(err)
		}
	}
	run := app.DiscoveryRun{Object: fieldName, Night: nightN(0), Group: discoveries.GroupBroadband, State: app.DiscoveryDone}
	db.Create(&run)
	c := app.DiscoveryCandidate{RunID: run.ID, Object: fieldName, Night: nightN(0), Group: discoveries.GroupBroadband, Kind: app.DiscoveryKindChange, Class: discoveries.ClassSupernova,
		RA: 10.5, Dec: 41.2, Sign: 1}
	db.Create(&c)
	a := app.DiscoveryAlert{Night: c.Night, Class: c.Class, Kind: c.Kind, RA: c.RA, Dec: c.Dec, Object: c.Object, Group: c.Group, RunID: run.ID, CandidateID: c.ID}
	db.Create(&a)
	return a
}

func laterRun(t *testing.T, db *gorm.DB, night time.Time, finished time.Time, cands ...app.DiscoveryCandidate) {
	t.Helper()
	run := app.DiscoveryRun{Object: fieldName, Night: night, Group: discoveries.GroupBroadband, State: app.DiscoveryDone, FinishedAt: &finished}
	if err := db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	for _, c := range cands {
		c.RunID, c.Object, c.Night, c.Group = run.ID, fieldName, night, discoveries.GroupBroadband
		if err := db.Create(&c).Error; err != nil {
			t.Fatal(err)
		}
	}
}

func get(t *testing.T, db *gorm.DB) app.DiscoveryFollowup {
	t.Helper()
	var f app.DiscoveryFollowup
	if err := db.First(&f).Error; err != nil {
		t.Fatal(err)
	}
	return f
}

func TestFollowupSentConfirmedAndClosed(t *testing.T) {
	t.Parallel()
	db, sched := appDB(t), SchedDB(t)
	now := time.Date(2026, 10, 12, 1, 0, 0, 0, time.UTC)
	cmds := &fakeCommands{}
	s := service(t, db, sched, cmds, &now)
	a := seedFind(t, db)
	ctx := context.Background()
	if err := s.Request(ctx, []app.DiscoveryAlert{a}); err != nil {
		t.Fatal(err)
	}
	if err := s.Request(ctx, []app.DiscoveryAlert{a}); err != nil {
		t.Fatal(err)
	}
	if err := s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	f := get(t, db)
	if f.State != app.FollowupWaiting || f.CreateCmd == "" || f.TargetGUID != FollowTargetGUID(f.ProjectGUID) || f.Subs != 10 || !f.Until.Equal(nightN(2)) {
		t.Fatalf("after send: %+v", f)
	}
	if len(cmds.order) != 1 || cmds.sent[cmds.order[0]].Kind != schedcmd.KindProjectCreate {
		t.Fatalf("commands %v", cmds.order)
	}
	if err := s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(cmds.order) != 1 {
		t.Errorf("sent again: %v", cmds.order)
	}
	now = now.Add(30 * time.Hour)
	laterRun(t, db, nightN(1), now, app.DiscoveryCandidate{Kind: app.DiscoveryKindChange, Class: discoveries.ClassSupernova, Sign: 1, RA: 10.5 + 0.8/3600, Dec: 41.2})
	if err := sched.Exec(`insert into project values (6,'p',?,null,1,2,30,25,0,0,1,?,40,1,3)`, "Follow-up: Field", f.ProjectGUID).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	f = get(t, db)
	if f.State != app.FollowupConfirmed || f.FollowNight == nil || !f.FollowNight.Equal(nightN(1)) || f.SeenID == nil || f.CloseCmd == "" {
		t.Fatalf("after the later night: %+v", f)
	}
	closed := cmds.sent[f.CloseCmd]
	var edit schedcmd.EditPayload
	if err := json.Unmarshal(closed.Payload, &edit); err != nil || closed.Kind != schedcmd.KindProjectEdit || edit.ID != 6 ||
		string(edit.Changes[0].After) != "3" || string(edit.Changes[0].Before) != "1" {
		t.Errorf("close command %+v %+v", closed, edit)
	}
	got, err := ForCandidate(ctx, db, app.DiscoveryCandidate{Object: fieldName, Night: nightN(0), Class: discoveries.ClassSupernova, RA: 10.5, Dec: 41.2})
	if err != nil || got == nil || got.State != app.FollowupConfirmed {
		t.Errorf("ForCandidate %+v %v", got, err)
	}
}

func TestFollowupNotSeenAgain(t *testing.T) {
	t.Parallel()
	db, sched := appDB(t), SchedDB(t)
	now := time.Date(2026, 10, 12, 1, 0, 0, 0, time.UTC)
	s := service(t, db, sched, &fakeCommands{}, &now)
	ctx := context.Background()
	if err := s.Request(ctx, []app.DiscoveryAlert{seedFind(t, db)}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(30 * time.Hour)
	laterRun(t, db, nightN(1), now)
	if err := s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if f := get(t, db); f.State != app.FollowupNotConfirmed || !strings.Contains(f.Note, "not seen again") {
		t.Errorf("%+v", f)
	}
}

func TestFollowupExpires(t *testing.T) {
	t.Parallel()
	db, sched := appDB(t), SchedDB(t)
	now := time.Date(2026, 10, 12, 1, 0, 0, 0, time.UTC)
	s := service(t, db, sched, &fakeCommands{}, &now)
	ctx := context.Background()
	if err := s.Request(ctx, []app.DiscoveryAlert{seedFind(t, db)}); err != nil {
		t.Fatal(err)
	}
	now = time.Date(2026, 10, 14, 5, 0, 0, 0, time.UTC)
	if err := s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if f := get(t, db); f.State != app.FollowupWaiting {
		t.Fatalf("expired on its last night: %+v", f)
	}
	now = time.Date(2026, 10, 15, 13, 0, 0, 0, time.UTC)
	laterRun(t, db, nightN(4), now, app.DiscoveryCandidate{Kind: app.DiscoveryKindChange, Sign: 1, RA: 10.5, Dec: 41.2})
	if err := s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if f := get(t, db); f.State != app.FollowupExpired || f.ClosedAt == nil {
		t.Errorf("a run after the last night settled it: %+v", f)
	}
}

func TestRefusedFollowupFails(t *testing.T) {
	t.Parallel()
	db, sched := appDB(t), SchedDB(t)
	now := time.Date(2026, 10, 12, 1, 0, 0, 0, time.UTC)
	cmds := &fakeCommands{}
	s := service(t, db, sched, cmds, &now)
	ctx := context.Background()
	if err := s.Request(ctx, []app.DiscoveryAlert{seedFind(t, db)}); err != nil {
		t.Fatal(err)
	}
	if err := s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	cmds.status = schedcmd.StatusRejected
	if err := s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if f := get(t, db); f.State != app.FollowupFailed || !strings.Contains(f.Note, "web editing is off") {
		t.Errorf("%+v", f)
	}
}

func TestSameNightFollowupCountsOnlyItsOwnSubs(t *testing.T) {
	t.Parallel()
	db, sched := appDB(t), SchedDB(t)
	now := time.Date(2026, 10, 12, 1, 0, 0, 0, time.UTC)
	s := service(t, db, sched, &fakeCommands{}, &now)
	ctx := context.Background()
	if err := s.Request(ctx, []app.DiscoveryAlert{seedFind(t, db)}); err != nil {
		t.Fatal(err)
	}
	if err := s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	f := get(t, db)
	exp := 180.0
	ids := make([]int, 0, 4)
	for i := range 4 {
		at := now.Add(time.Duration(i*4) * time.Minute)
		n := nightN(0)
		g := f.TargetGUID
		fr := app.Frame{Key: fmt.Sprintf("u%d", i), Type: "LIGHT", Object: fieldName, Filter: "Luminance", Exposure: &exp, DateObs: &at, Night: &n, TSTarget: &g}
		db.Create(&fr)
		ids = append(ids, fr.ID)
	}
	now = now.Add(4 * time.Hour)
	lp := light([2]float64{1, 9}, [2]float64{float64(ids[0]), 6}, [2]float64{float64(ids[1]), 5}, [2]float64{float64(ids[2]), 7}, [2]float64{float64(ids[3]), 0})
	var run app.DiscoveryRun
	db.Where("night = ?", nightN(0)).First(&run)
	db.Model(&run).Update("finished_at", now)
	db.Create(&app.DiscoveryCandidate{RunID: run.ID, Object: fieldName, Night: nightN(0), Group: discoveries.GroupBroadband, Kind: app.DiscoveryKindChange,
		Class: discoveries.ClassSupernova, Sign: 1, RA: 10.5, Dec: 41.2, Light: lp})
	if err := s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if f := get(t, db); f.State != app.FollowupConfirmed || !strings.Contains(f.Note, "3 of 4") {
		t.Errorf("%+v", f)
	}
}

func TestCappedProjectCloses(t *testing.T) {
	t.Parallel()
	db, sched := appDB(t), SchedDB(t)
	now := time.Date(2026, 10, 12, 1, 0, 0, 0, time.UTC)
	cmds := &fakeCommands{}
	s := service(t, db, sched, cmds, &now)
	ctx := context.Background()
	if err := s.Request(ctx, []app.DiscoveryAlert{seedFind(t, db)}); err != nil {
		t.Fatal(err)
	}
	if err := s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	f := get(t, db)
	stmts := []string{
		`insert into project values (6,'p','Follow-up: Field',null,1,2,30,25,0,0,1,'` + f.ProjectGUID + `',40,1,3)`,
		`insert into target values (13,'Field',1,0.70386,41.2767,12.5,6,'` + f.TargetGUID + `')`,
		`insert into exposureplan values (60,'p',180,10,15,4,13,3,1,'e60')`,
	}
	for _, st := range stmts {
		if err := sched.Exec(st).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if f := get(t, db); f.CloseCmd != "" {
		t.Fatalf("closed at 15 of 10 subs: %q", f.CloseCmd)
	}
	if err := sched.Exec(`update exposureplan set acquired = 20 where "Id" = 60`).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	f = get(t, db)
	if f.CloseCmd == "" || f.State != app.FollowupWaiting {
		t.Errorf("not closed at twice the subs, or settled by it: %+v", f)
	}
}
