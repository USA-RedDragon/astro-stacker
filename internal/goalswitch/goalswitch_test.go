package goalswitch_test

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discover"
	"github.com/USA-RedDragon/astro-stacker/internal/goals"
	"github.com/USA-RedDragon/astro-stacker/internal/goalswitch"
	"github.com/USA-RedDragon/astro-stacker/internal/planning"
	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/testdb"
	"gorm.io/gorm"
)

const (
	lum   = "Luminance"
	red   = "Red"
	green = "Green"
	m81   = "M 81"
)

func open(t *testing.T, name string) *gorm.DB {
	t.Helper()
	return testdb.SQLite(t, name)
}

func fixture(t *testing.T) (sched, appDB *gorm.DB) {
	t.Helper()
	sched = open(t, "sched")
	for _, q := range []string{
		`create table project ("Id" integer primary key, "profileId" text, name text, description text, state integer, priority integer, minimumtime integer, minimumaltitude real, "maximumAltitude" real, "isMosaic" integer, enablegrader integer, guid text)`,
		`create table target ("Id" integer primary key, name text, active integer, ra real, "dec" real, rotation real, projectid integer, guid text)`,
		`create table exposuretemplate ("Id" integer primary key, "profileId" text, name text, filtername text, gain integer, "offset" integer, bin integer, twilightlevel integer, moonavoidanceenabled integer, moonavoidanceseparation real, moonavoidancewidth integer, maximumhumidity real, defaultexposure real, moonrelaxscale real, moondownenabled integer, ditherevery integer, guid text)`,
		`create table exposureplan ("Id" integer primary key, "profileId" text, exposure real, desired integer, acquired integer, accepted integer, targetid integer, "exposureTemplateId" integer, enabled integer, guid text)`,
		`create table ruleweight ("Id" integer primary key, name text, weight real, projectid integer)`,
		`create table acquiredimage ("Id" integer primary key, "projectId" integer, "targetId" integer, acquireddate integer, filtername text, "gradingStatus" integer, metadata text)`,
		`create table ts_goal (target_guid text not null, filter text not null, kind integer not null default 0, snr_goal real, depth_goal real, plateau_stop integer not null default 1, region text, updated_at timestamp, primary key (target_guid, filter))`,
		`insert into project values (1,'p','Bodes',null,1,1,60,20,0,0,1,'pg1'),(2,'p','North America',null,1,1,60,20,0,0,1,'pg2'),(3,'p','Old galaxy',null,3,1,60,20,0,0,1,'pg3')`,
		`insert into target values (10,'M 81',1,9.9,69,0,1,'g10'),(20,'NGC 7000',1,20.9,44,0,2,'g20'),(30,'M 51',1,13.5,47,0,3,'g30')`,
		`insert into exposuretemplate values (1,'p','Luminance','Luminance',0,50,1,0,1,30,7,85,300,0,0,1,'t1'),(2,'p','Red','Red',0,50,1,0,1,30,7,85,300,0,0,1,'t2'),(3,'p','Green','Green',0,50,1,0,1,30,7,85,300,0,0,1,'t3'),(4,'p','H-a','H-a',100,10,1,2,1,45,7,85,600,0,0,1,'t4')`,
		`insert into exposureplan values (1,'p',300,32,40,40,10,1,1,'e1'),(2,'p',300,32,40,40,10,2,1,'e2'),(3,'p',300,32,40,40,10,3,1,'e3'),(4,'p',600,32,40,40,10,4,1,'e4'),(5,'p',300,32,40,40,20,1,1,'e5'),(6,'p',300,32,40,40,30,1,1,'e6')`,
		`insert into ts_goal values ('g10','Luminance',0,10,null,1,null,null),('g10','Red',0,10,25.8,0,null,null),('g10','Green',1,10,26.1,1,null,null),('g10','H-a',0,10,null,1,null,null),('g20','Luminance',0,10,null,1,null,null),('g30','Luminance',0,10,null,1,null,null)`,
	} {
		if err := sched.Exec(q).Error; err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	appDB = open(t, "app")
	if err := appDB.AutoMigrate(&app.GoalMeasurement{}, &app.Frame{}, &app.FrameTarget{}, &schedcmd.Record{}); err != nil {
		t.Fatal(err)
	}
	d := func(v float64) *float64 { return &v }
	at := time.Date(2026, 10, 10, 6, 0, 0, 0, time.UTC)
	ms := []app.GoalMeasurement{
		{Object: m81, Filter: lum, Subs: 40, Levels: 4, EffectiveHours: 2, NoiseA: 1, NoiseB: 0.1, SNR: 6, Depth: d(25.0), ZeroPoint: d(22), PixelScale: 1.2, ZeroPointStars: 40, MeasuredAt: at},
		{Object: m81, Filter: red, Subs: 40, Levels: 1, EffectiveHours: 3, NoiseA: 1, SNR: 6, Depth: d(24.0), ZeroPoint: d(22), PixelScale: 1.2, ZeroPointStars: 40, MeasuredAt: at},
		{Object: m81, Filter: green, Subs: 40, Levels: 4, EffectiveHours: 3, NoiseA: 1, SNR: 6, Depth: d(24.0), ZeroPoint: d(22), MeasuredAt: at},
		{Object: m81, Filter: "H-a", Subs: 40, Levels: 4, EffectiveHours: 3, NoiseA: 1, SNR: 6, Depth: d(24.0), DepthApprox: true, MeasuredAt: at},
		{Object: "NGC 7000", Filter: lum, Subs: 40, Levels: 4, EffectiveHours: 3, NoiseA: 1, SNR: 6, Depth: d(24.0), ZeroPoint: d(22), MeasuredAt: at},
		{Object: "M 51", Filter: lum, Subs: 40, Levels: 4, EffectiveHours: 3, NoiseA: 1, SNR: 6, Depth: d(24.0), ZeroPoint: d(22), MeasuredAt: at},
	}
	if err := appDB.Create(&ms).Error; err != nil {
		t.Fatal(err)
	}
	return sched, appDB
}

func classes(context.Context) (map[int]string, error) {
	return map[int]string{1: discover.ClassGalaxy, 2: discover.ClassEmission, 3: discover.ClassGalaxy}, nil
}

func TestPlanMovesBroadbandFiltersWithAZeroPointOnDepthTargets(t *testing.T) {
	t.Parallel()
	sched, appDB := fixture(t)
	s, err := planning.Load(context.Background(), sched, appDB, planning.Inputs{})
	if err != nil {
		t.Fatal(err)
	}
	cls, _ := classes(context.Background())
	edits, changes := goalswitch.Plan(s, cls, nil)
	if len(edits) != 1 || edits[0].ProjectID != 1 || len(edits[0].Goals) != 2 || len(changes) != 2 {
		t.Fatalf("only Bodes L and R move: %+v", edits)
	}
	byFilter := map[string]schedcmd.GoalChange{}
	for _, g := range edits[0].Goals {
		byFilter[g.Filter] = g
	}
	l := byFilter[lum]
	plateau := goals.HoursToPlateau(1, 0.1, goals.PlateauGainPct)
	want := math.Round((25+1.25*math.Log10(plateau/2))*10) / 10
	if l.Before == nil || l.Before.Kind != schedcmd.GoalKindSNR || l.Before.DepthGoal != nil || !l.Before.PlateauStop ||
		l.After.Kind != schedcmd.GoalKindDepth || l.After.DepthGoal == nil || *l.After.DepthGoal != want || l.After.SNRGoal != 10 {
		t.Fatalf("L moves to its plateau depth %v: %+v -> %+v", want, l.Before, l.After)
	}
	r := byFilter[red]
	if r.Before.DepthGoal == nil || *r.Before.DepthGoal != 25.8 || r.Before.PlateauStop || *r.After.DepthGoal != 25.8 || r.After.PlateauStop {
		t.Fatalf("R keeps its row exactly and takes the default depth: %+v -> %+v", r.Before, r.After)
	}
	skip := map[string]bool{"g10|luminance": true}
	if edits, _ := goalswitch.Plan(s, cls, skip); len(edits) != 1 || len(edits[0].Goals) != 1 || edits[0].Goals[0].Filter != red {
		t.Fatalf("a filter switched before is left alone: %+v", edits)
	}
}

func TestDepthGoalNeedsAMeasuredZeroPointAndRoomToGrow(t *testing.T) {
	t.Parallel()
	d := func(v float64) *float64 { return &v }
	if _, _, ok := goalswitch.DepthGoal(nil, lum); ok {
		t.Error("no measurement")
	}
	if _, _, ok := goalswitch.DepthGoal(&planning.Measurement{EffectiveHours: 2}, lum); ok {
		t.Error("no zero point")
	}
	if _, _, ok := goalswitch.DepthGoal(&planning.Measurement{EffectiveHours: 2, Depth: d(25), DepthApprox: true}, "H-a"); ok {
		t.Error("an approximate narrowband depth")
	}
	if _, _, ok := goalswitch.DepthGoal(&planning.Measurement{EffectiveHours: 2, Depth: d(26)}, lum); ok {
		t.Error("already deeper than the default depth with no plateau measured")
	}
	if _, _, ok := goalswitch.DepthGoal(&planning.Measurement{EffectiveHours: 20, Depth: d(26), PlateauAt: goals.Plateau{Hours: d(10)}}, lum); ok {
		t.Error("past its plateau")
	}
	g, basis, ok := goalswitch.DepthGoal(&planning.Measurement{EffectiveHours: 1, Depth: d(24.3), PlateauAt: goals.Plateau{Hours: d(10)}}, lum)
	if !ok || g != 25.6 || basis != "the depth at its plateau point, 10.0 h, from 24.30 now after 1.0 h" {
		t.Errorf("%v %q %v", g, basis, ok)
	}
}

func TestSwitcherSubmitsOnceAndRespectsTheMode(t *testing.T) {
	t.Parallel()
	sched, appDB := fixture(t)
	cmds := &schedcmd.Service{Log: schedcmd.NewGormLog(appDB), AppDB: appDB}
	w := &goalswitch.Switcher{App: appDB, Sched: sched, Commands: cmds, Classes: classes, Mode: goalswitch.ModeDry}
	changes, err := w.Once(context.Background())
	if err != nil || len(changes) != 2 {
		t.Fatalf("%+v %v", changes, err)
	}
	if rs, _ := cmds.Log.List(context.Background(), schedcmd.Filter{}); len(rs) != 0 {
		t.Fatalf("dry run submitted %d commands", len(rs))
	}
	w.Mode = goalswitch.ModeOn
	if _, err := w.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	rs, _ := cmds.Log.List(context.Background(), schedcmd.Filter{})
	if len(rs) != 1 || rs[0].Kind != schedcmd.KindGoalEdit || rs[0].Author != goalswitch.Author || rs[0].Status != schedcmd.StatusQueued {
		t.Fatalf("%+v", rs)
	}
	var p schedcmd.GoalEditPayload
	if err := json.Unmarshal(rs[0].Payload, &p); err != nil || p.ProjectName != "Bodes" || len(p.Goals) != 2 {
		t.Fatalf("%+v %v", p, err)
	}
	if changes, err := w.Once(context.Background()); err != nil || len(changes) != 0 {
		t.Fatalf("the second run repeats nothing: %+v %v", changes, err)
	}
	w.Mode = goalswitch.ModeOff
	if changes, err := w.Once(context.Background()); err != nil || changes != nil {
		t.Fatalf("off does nothing: %+v %v", changes, err)
	}
}

func TestPlanSkipsARowThatIsNotAValidGoalEdit(t *testing.T) {
	t.Parallel()
	sched, appDB := fixture(t)
	if err := sched.Exec(`update ts_goal set region = '[]' where target_guid = 'g10' and filter = 'Red'`).Error; err != nil {
		t.Fatal(err)
	}
	s, err := planning.Load(context.Background(), sched, appDB, planning.Inputs{})
	if err != nil {
		t.Fatal(err)
	}
	cls, _ := classes(context.Background())
	edits, _ := goalswitch.Plan(s, cls, nil)
	if len(edits) != 1 || len(edits[0].Goals) != 1 || edits[0].Goals[0].Filter != lum {
		t.Fatalf("R with a broken region stays on SNR and does not block L: %+v", edits)
	}
}
