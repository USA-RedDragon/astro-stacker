package schedauto_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/mosaics"
	"github.com/USA-RedDragon/astro-stacker/internal/observatory"
	"github.com/USA-RedDragon/astro-stacker/internal/schedauto"
	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"github.com/USA-RedDragon/astro-stacker/internal/schedglobal"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/testdb"
	"gorm.io/gorm"
)

type sentCommands struct{ sent []schedcmd.Envelope }

func (k *sentCommands) Name() string { return "kept" }

func (k *sentCommands) Send(_ context.Context, e schedcmd.Envelope) (schedcmd.Result, error) {
	k.sent = append(k.sent, e)
	return schedcmd.Result{ID: e.ID, Status: schedcmd.StatusQueued}, nil
}

func (k *sentCommands) Cancel(_ context.Context, id string) (schedcmd.Result, error) {
	return schedcmd.Result{ID: id, Status: schedcmd.StatusCancelled}, nil
}

func open(t *testing.T, name string) *gorm.DB {
	t.Helper()
	return testdb.SQLite(t, name)
}

const garlic = "Garlic Nebula"

func testNow() time.Time { return time.Date(2026, 10, 12, 21, 0, 0, 0, time.UTC) }

func fixture(t *testing.T, mode string) (*schedauto.Service, *sentCommands, *gorm.DB) {
	t.Helper()
	sched := open(t, "sched")
	for _, q := range []string{
		`create table project ("Id" integer primary key, "profileId" text, name text, description text, state integer, priority integer, minimumtime integer, minimumaltitude real, "maximumAltitude" real, "isMosaic" integer, enablegrader integer, guid text, ditherevery integer, filterswitchfrequency integer, "flatsHandling" integer, smartexposureorder integer, meridianwindow integer, usecustomhorizon integer, horizonoffset real)`,
		`create table target ("Id" integer primary key, name text, active integer, ra real, "dec" real, rotation real, projectid integer, guid text)`,
		`create table exposuretemplate ("Id" integer primary key, "profileId" text, name text, filtername text, gain integer, "offset" integer, bin integer, twilightlevel integer, moonavoidanceenabled integer, moonavoidanceseparation real, moonavoidancewidth integer, maximumhumidity real, defaultexposure real, moonrelaxscale real, moondownenabled integer, ditherevery integer, guid text)`,
		`create table exposureplan ("Id" integer primary key, "profileId" text, exposure real, desired integer, acquired integer, accepted integer, targetid integer, "exposureTemplateId" integer, enabled integer, guid text)`,
		`create table ruleweight ("Id" integer primary key, name text, weight real, projectid integer)`,
		`create table acquiredimage ("Id" integer primary key, "projectId" integer, "targetId" integer, acquireddate integer)`,
		`insert into project values (7,'p','Garlic Nebula',null,1,1,60,25,0,0,1,'pg7',1,1,1,0,0,0,0),(8,'p','Equal',null,1,1,25,15,0,0,1,'pg8',2,1,1,0,0,0,0),(9,'p','Closed one',null,3,1,60,25,0,0,1,'pg9',1,1,0,0,0,0,0)`,
		`insert into target values (20,'Garlic Nebula',1,23.987,62.44,0,7,'g20'),(21,'Equal',1,0.712,41.27,0,8,'g21'),(22,'Closed one',1,5,20,0,9,'g22')`,
		`insert into exposuretemplate values (1,'p','H-a','H-a',100,10,1,2,1,45,7,85,600,0,0,1,'t1')`,
		`insert into exposureplan values (50,'p',-1,100,64,64,20,1,1,'e50'),(51,'p',-1,100,64,64,21,1,1,'e51'),(52,'p',-1,100,64,64,22,1,1,'e52')`,
	} {
		if err := sched.Exec(q).Error; err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	appDB := open(t, "app")
	if err := appDB.AutoMigrate(&app.Stack{}, &app.GoalMeasurement{}, &app.FrameTarget{}, &app.Frame{}, &schedcmd.Record{},
		&app.GlobalSchedulingSetting{}, &app.SchedulingOriginal{}, &app.SkyEventChoice{}); err != nil {
		t.Fatal(err)
	}
	sent := &sentCommands{}
	cmds := &schedcmd.Service{Log: schedcmd.NewGormLog(appDB), AppDB: appDB, Transports: []schedcmd.Transport{sent}, Now: testNow}
	s := &schedauto.Service{App: appDB, Sched: sched, Commands: cmds, Mode: mode, Now: testNow,
		Site: func(context.Context) (sky.Site, error) { return site(), nil }}
	return s, sent, appDB
}

func TestOffComputesButNeverSends(t *testing.T) {
	t.Parallel()
	s, sent, appDB := fixture(t, schedauto.ModeOff)
	ctx := context.Background()
	if res, err := s.Once(ctx); res != nil || !errors.Is(err, schedauto.ErrOff) {
		t.Fatalf("off runs nothing: %v %v", res, err)
	}
	res, err := s.Current(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Projects) != 3 || res.Differing != 1 || res.Saved || res.Global != schedglobal.Defaults() || res.SwitchFloor != 25 {
		t.Fatalf("%+v", res)
	}
	if len(sent.sent) != 0 {
		t.Errorf("off sent %d commands", len(sent.sent))
	}
	var n int64
	appDB.Model(&app.SchedulingOriginal{}).Count(&n)
	if n != 0 {
		t.Errorf("off kept %d originals", n)
	}
	s.Mode = schedauto.ModeDryRun
	if _, err := s.Once(ctx); err != nil || len(sent.sent) != 0 {
		t.Errorf("dry-run sent %d: %v", len(sent.sent), err)
	}
	if r, _ := s.Current(ctx); r.LastRun == nil || r.LastRun.Projects != 1 || len(r.LastRun.CommandIDs) != 0 {
		t.Errorf("dry-run status %+v", r.LastRun)
	}
}

func TestOnSendsOneBatchKeepsOriginalsAndWaitsForTheSync(t *testing.T) {
	t.Parallel()
	s, sent, appDB := fixture(t, schedauto.ModeOn)
	ctx := context.Background()
	if _, err := s.Once(ctx); err != nil {
		t.Fatal(err)
	}
	if len(sent.sent) != 1 || sent.sent[0].Kind != schedcmd.KindProjectEdit {
		t.Fatalf("sent %+v", sent.sent)
	}
	var b schedcmd.EditPayload
	if err := json.Unmarshal(sent.sent[0].Payload, &b); err != nil || b.ID != 7 {
		t.Fatalf("edit %+v %v", b, err)
	}
	fields := map[string]string{}
	for _, c := range b.Changes {
		fields[c.Field] = string(c.Before) + ">" + string(c.After)
	}
	if fields[schedcmd.FieldDitherEvery] != ditherOneToTwo || fields[schedauto.FieldMinimumAltitude] != "25>15" {
		t.Errorf("changes %v", fields)
	}
	var orig []app.SchedulingOriginal
	if err := appDB.Find(&orig).Error; err != nil || len(orig) != 1 || orig[0].ProjectGUID != "pg7" {
		t.Fatalf("originals %+v %v", orig, err)
	}
	var v schedauto.Values
	if err := json.Unmarshal([]byte(orig[0].Values), &v); err != nil || v.DitherEvery == nil || *v.DitherEvery != 1 || *v.MinimumAltitude != 25 {
		t.Errorf("original values %s", orig[0].Values)
	}
	if _, err := s.Once(ctx); err != nil || len(sent.sent) != 1 {
		t.Errorf("a project with a waiting edit is left until the plugin and the sync catch up: %d sent, %v", len(sent.sent), err)
	}
	if r, _ := s.Current(ctx); r.LastRun == nil || len(r.LastRun.Left) != 1 || r.LastRun.Left[0] != "Garlic Nebula" {
		t.Errorf("status %+v", r.LastRun)
	}
}

type fakePreview struct{ reqs []observatory.PreviewRequest }

func (f *fakePreview) PreviewWith(_ context.Context, req observatory.PreviewRequest) (json.RawMessage, error) {
	f.reqs = append(f.reqs, req)
	return json.RawMessage(`{"blocks":[{"start":"2026-10-13T01:00:00Z","end":"2026-10-13T03:00:00Z","target_id":20},{"start":"2026-10-13T03:00:00Z","end":"2026-10-13T05:00:00Z","target_id":21}]}`), nil
}

func TestProjectionSimulatesBothWays(t *testing.T) {
	t.Parallel()
	s, _, _ := fixture(t, schedauto.ModeOff)
	pv := &fakePreview{}
	v, err := s.Projection(context.Background(), pv)
	if err != nil {
		t.Fatal(err)
	}
	if len(pv.reqs) != 2 || len(pv.reqs[0].Overrides) != 0 || len(pv.reqs[1].Overrides) == 0 || pv.reqs[0].Start == nil {
		t.Fatalf("requests %+v", pv.reqs)
	}
	for _, o := range pv.reqs[1].Overrides {
		if o.ID == 9 {
			t.Errorf("closed projects are not overridden: %+v", o)
		}
	}
	if v.Current.Switches != 1 || v.Automatic.Settings != schedauto.SettingsAutomatic || v.PerSwitchMinutes != schedauto.DefaultSwitchMinutes {
		t.Errorf("%+v", v)
	}
}

func TestSavedGlobalSetIsUsed(t *testing.T) {
	t.Parallel()
	s, _, appDB := fixture(t, schedauto.ModeOff)
	ctx := context.Background()
	g := schedglobal.Defaults()
	g.DitherEvery = 1
	if err := schedglobal.Apply(ctx, appDB, nil, &g, "web", testNow()); err != nil {
		t.Fatal(err)
	}
	res, err := s.Refresh(ctx)
	if err != nil || !res.Saved || res.Global.DitherEvery != 1 || res.SavedBy != "web" {
		t.Fatalf("%+v %v", res, err)
	}
	for _, p := range res.Projects {
		for _, c := range p.Changes {
			if c.Field == schedcmd.FieldDitherEvery && p.ID == 7 {
				t.Errorf("Garlic already dithers every 1: %+v", c)
			}
		}
	}
}

func TestAManualEditWaitsForTheSync(t *testing.T) {
	t.Parallel()
	s, sent, _ := fixture(t, schedauto.ModeOn)
	ctx := context.Background()
	raw, err := json.Marshal(schedcmd.EditPayload{Entity: entity, ID: 7, Name: "Garlic Nebula", Changes: []schedcmd.FieldChange{{Field: "priority", Before: json.RawMessage("1"), After: json.RawMessage("2")}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Commands.Submit(ctx, schedcmd.KindProjectEdit, raw, "web"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Once(ctx); err != nil || len(sent.sent) != 1 {
		t.Errorf("only the manual edit is sent while it syncs: %d, %v", len(sent.sent), err)
	}
}

func TestNewProjectTakesTheGlobalAndAutomaticValues(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	newPayload := func() *schedcmd.ProjectCreatePayload {
		one, zero := 1, 0
		return &schedcmd.ProjectCreatePayload{
			Project: schedcmd.NewProject{Name: "New", MinimumTime: 90, MinimumAltitude: 30, ProjectSettings: schedcmd.ProjectSettings{DitherEvery: &one, FlatsHandling: &zero}},
			Targets: []schedcmd.NewTarget{{Name: "M31", RAHours: 0.712, Dec: 41.27}},
		}
	}
	s, _, _ := fixture(t, schedauto.ModeOff)
	p := newPayload()
	if err := s.NewProject(ctx, "p", p); err != nil || p.Project.MinimumTime != 90 || *p.Project.DitherEvery != 1 {
		t.Fatalf("off changes nothing: %+v %v", p.Project, err)
	}
	s.Mode = schedauto.ModeOn
	if err := s.NewProject(ctx, "p", p); err != nil {
		t.Fatal(err)
	}
	g := schedglobal.Defaults()
	if *p.Project.DitherEvery != g.DitherEvery || *p.Project.FlatsHandling != g.FlatsHandling || *p.Project.EnableGrader != g.EnableGrader ||
		p.Project.MinimumAltitude != schedauto.HardMinAltitude || p.Project.MinimumTime != 25 || p.Project.UseCustomHorizon != nil {
		t.Errorf("global values, hard limit and switching floor; no horizon report keeps the draft's setting: %+v", p.Project)
	}
	wall := mosaics.Horizon{Step: 90, Altitudes: []float64{30, 30, 30, 30}}
	s.Horizon = func(context.Context) (mosaics.HorizonReport, bool) {
		return mosaics.HorizonReport{ProfileID: "p", Custom: true, Horizon: &wall}, true
	}
	p = newPayload()
	if err := s.NewProject(ctx, "p", p); err != nil || p.Project.MinimumAltitude != 0 || p.Project.UseCustomHorizon == nil || !*p.Project.UseCustomHorizon {
		t.Errorf("the profile's horizon: %+v %v", p.Project, err)
	}
	p = newPayload()
	if err := s.NewProject(ctx, "other", p); err != nil || p.Project.MinimumAltitude != schedauto.HardMinAltitude || p.Project.UseCustomHorizon != nil {
		t.Errorf("another profile's horizon is not used: %+v %v", p.Project, err)
	}
	if err := s.NewProject(ctx, "p", nil); err != nil {
		t.Error(err)
	}
}

func TestOwnAppliedEditHoldsOnlyItsOwnValues(t *testing.T) {
	t.Parallel()
	s, sent, appDB := fixture(t, schedauto.ModeOn)
	ctx := context.Background()
	if _, err := s.Once(ctx); err != nil || len(sent.sent) != 1 {
		t.Fatalf("sent %d: %v", len(sent.sent), err)
	}
	if err := appDB.Model(&schedcmd.Record{}).Where("id = ?", sent.sent[0].ID).Update("status", schedcmd.StatusApplied).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.Once(ctx); err != nil || len(sent.sent) != 1 {
		t.Fatalf("the same values are not sent again while the home copy catches up: %d sent, %v", len(sent.sent), err)
	}
	if r, _ := s.Current(ctx); r.LastRun == nil || len(r.LastRun.Left) != 0 {
		t.Errorf("an applied automatic edit holds nothing back: %+v", r.LastRun)
	}
	if err := s.Sched.Exec(`update project set filterswitchfrequency = 5 where "Id" = 7`).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.Once(ctx); err != nil || len(sent.sent) != 2 {
		t.Fatalf("a new difference is sent at once, not after the 30-minute hold: %d sent, %v", len(sent.sent), err)
	}
	var b schedcmd.EditPayload
	if err := json.Unmarshal(sent.sent[1].Payload, &b); err != nil || b.ID != 7 {
		t.Fatalf("edit %+v %v", b, err)
	}
	found := false
	for _, c := range b.Changes {
		found = found || c.Field == schedcmd.FieldFilterSwitchFrequency
	}
	if !found {
		t.Errorf("changes %+v", b.Changes)
	}
}

func TestOwnRefusedEditIsNotSentAgainAtOnce(t *testing.T) {
	t.Parallel()
	for _, st := range []schedcmd.Status{schedcmd.StatusConflict, schedcmd.StatusRejected, schedcmd.StatusFailed, schedcmd.StatusCancelled} {
		t.Run(string(st), func(t *testing.T) {
			t.Parallel()
			s, sent, appDB := fixture(t, schedauto.ModeOn)
			ctx := context.Background()
			if _, err := s.Once(ctx); err != nil || len(sent.sent) != 1 {
				t.Fatalf("sent %d: %v", len(sent.sent), err)
			}
			if err := appDB.Model(&schedcmd.Record{}).Where("id = ?", sent.sent[0].ID).Update("status", st).Error; err != nil {
				t.Fatal(err)
			}
			for range 3 {
				if _, err := s.Once(ctx); err != nil || len(sent.sent) != 1 {
					t.Fatalf("a refused edit is held for the sync, not sent again each run: %d sent, %v", len(sent.sent), err)
				}
			}
		})
	}
}

func TestAnAppliedManualEditStillWaitsForTheSync(t *testing.T) {
	t.Parallel()
	s, sent, appDB := fixture(t, schedauto.ModeOn)
	ctx := context.Background()
	raw, err := json.Marshal(schedcmd.EditPayload{Entity: entity, ID: 7, Name: garlic, Changes: []schedcmd.FieldChange{{Field: "priority", Before: json.RawMessage("1"), After: json.RawMessage("2")}}})
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.Commands.Submit(ctx, schedcmd.KindProjectEdit, raw, "web")
	if err != nil {
		t.Fatal(err)
	}
	if err := appDB.Model(&schedcmd.Record{}).Where("id = ?", r.ID).Update("status", schedcmd.StatusApplied).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.Once(ctx); err != nil || len(sent.sent) != 1 {
		t.Errorf("only the manual edit is sent while it syncs: %d, %v", len(sent.sent), err)
	}
}

func TestAffects(t *testing.T) {
	t.Parallel()
	for kind, want := range map[schedcmd.Kind]bool{
		schedcmd.KindProjectCreate: true, schedcmd.KindProjectEdit: true, schedcmd.KindProjectBatchEdit: true, schedcmd.KindProjectDelete: true,
		schedcmd.KindTargetEdit: true, schedcmd.KindPlanBatchEdit: true, schedcmd.KindSchedulingGlobal: true,
		schedcmd.KindSkip: false, schedcmd.KindAbortExposure: false, schedcmd.KindGoalEdit: false,
	} {
		if got := schedauto.Affects(schedcmd.Record{Kind: kind}); got != want {
			t.Errorf("%s: %v", kind, got)
		}
	}
}
