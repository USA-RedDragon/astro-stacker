package planning

import (
	"context"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/goals"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

const (
	filterHa     = "H-a"
	filterO3     = "O-III"
	garlicNebula = "Garlic Nebula"
	setHOO       = "HOO"
	setIDHoo     = "hoo"
)

func testDBs(t *testing.T) (*gorm.DB, *gorm.DB) {
	t.Helper()
	sched, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	stmts := []string{
		`create table project ("Id" integer primary key, "profileId" text, name text, description text, state integer, priority integer, minimumtime integer, minimumaltitude real, "maximumAltitude" real, "isMosaic" integer, enablegrader integer, guid text)`,
		`create table target ("Id" integer primary key, name text, active integer, ra real, "dec" real, rotation real, projectid integer, guid text)`,
		`create table exposuretemplate ("Id" integer primary key, "profileId" text, name text, filtername text, gain integer, "offset" integer, bin integer, twilightlevel integer, moonavoidanceenabled integer, moonavoidanceseparation real, moonavoidancewidth integer, maximumhumidity real, defaultexposure real, moonrelaxscale real, moondownenabled integer, ditherevery integer, guid text)`,
		`create table exposureplan ("Id" integer primary key, "profileId" text, exposure real, desired integer, acquired integer, accepted integer, targetid integer, "exposureTemplateId" integer, enabled integer, guid text)`,
		`create table ruleweight ("Id" integer primary key, name text, weight real, projectid integer)`,
		`create table acquiredimage ("Id" integer primary key, "projectId" integer, "targetId" integer, acquireddate integer, filtername text, "gradingStatus" integer, metadata text)`,
		`create table ts_target_season (target_guid text primary key, nights_left integer, out_of_season integer, season_end text, computed_for text, computed_at text)`,
		`insert into project values (5,'p','Cygnis Loop',null,1,0,90,15,0,1,1,'pg5'),(7,'p','Garlic Nebula',null,1,1,60,20,0,0,1,'pg7')`,
		`insert into target values (12,'Cygnis Loop Panel 1',1,20.8,30.7,0,5,'g12'),(13,'Cygnis Loop Panel 2',1,20.9,31.2,0,5,'g13'),(20,'Garlic Nebula',1,23.98,62.44,0,7,'g20')`,
		`insert into exposuretemplate values (1,'p','H-a','H-a',100,10,1,2,1,45,7,85,600,0,0,1,'t1'),(2,'p','O-III','O-III',100,10,1,2,1,45,7,85,600,0,0,1,'t2'),(3,'p','Luminance','Luminance',0,50,1,0,1,30,7,85,300,0,0,1,'t3')`,
		`insert into exposureplan values (40,'p',-1,300,350,180,12,1,1,'e40'),(41,'p',-1,300,300,200,12,2,1,'e41'),(42,'p',-1,300,10,10,13,1,1,'e42'),(43,'p',-1,300,10,10,13,2,1,'e43'),(50,'p',-1,100,64,64,20,1,1,'e50'),(51,'p',-1,100,47,47,20,2,1,'e51'),(52,'p',300,1,1,1,20,3,0,'e52')`,
		`insert into ruleweight values (1,'Project Priority',100,5),(2,'Target Switch Penalty',67,5),(3,'Filter Steering',100,5)`,
		`insert into acquiredimage values (1,5,13,638950000000000000,'H-a',1,'{}')`,
		`insert into ts_target_season values ('g12',40,0,null,'2026-10-09',null),('g13',35,0,null,'2026-10-09',null),('g20',0,1,null,'2026-10-09',null)`,
	}
	for _, s := range stmts {
		if err := sched.Exec(s).Error; err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	appDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := appDB.AutoMigrate(&app.GoalMeasurement{}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ms := []app.GoalMeasurement{
		{Object: "Cygnis Loop Panel 1", Filter: filterHa, Subs: 200, SNR: 8.4, EffectiveHours: 33.7, GainPerHourPct: 1.4, MeasuredAt: now},
		{Object: "Cygnis Loop Panel 1", Filter: filterO3, Subs: 200, SNR: 5.3, EffectiveHours: 35.2, GainPerHourPct: 1.3, MeasuredAt: now},
		{Object: garlicNebula, Filter: filterHa, Subs: 64, SNR: 11, EffectiveHours: 10.7, GainPerHourPct: 4, MeasuredAt: now},
		{Object: garlicNebula, Filter: filterO3, Subs: 47, SNR: 4.4, EffectiveHours: 7.8, GainPerHourPct: 5.8, MeasuredAt: now},
	}
	if err := appDB.Create(&ms).Error; err != nil {
		t.Fatal(err)
	}
	return sched, appDB
}

func TestFilterSteeringIsAProjectSwitchNotARule(t *testing.T) {
	t.Parallel()
	sched, appDB := testDBs(t)
	s, err := Load(context.Background(), sched, appDB, Inputs{})
	if err != nil {
		t.Fatal(err)
	}
	cyg, _ := s.Project(5)
	if cyg.FilterSteering.Missing || cyg.FilterSteering.Weight != 100 || slices.ContainsFunc(cyg.RuleWeights, func(w RuleWeight) bool { return w.Name == SwitchFilterSteering }) {
		t.Fatalf("filter steering %+v in %+v", cyg.FilterSteering, cyg.RuleWeights)
	}
	garlic, _ := s.Project(7)
	if !garlic.FilterSteering.Missing || garlic.FilterSteering.Weight != 0 {
		t.Fatalf("garlic filter steering %+v", garlic.FilterSteering)
	}
	for _, r := range s.Rules {
		if (r.Name == RuleConditionMatch || r.Name == RuleSeasonalRunway || r.Name == RuleSeasonPriority) && (r.DefaultWeight != 0 || !r.New) {
			t.Errorf("rule %+v", r)
		}
	}
}

func TestLoadBuildsProjects(t *testing.T) {
	t.Parallel()
	sched, appDB := testDBs(t)
	g := map[goals.Key]goals.Goal{{Object: garlicNebula, Filter: filterO3}: {Kind: goals.KindSNR, SNR: 5, PlateauStop: false}}
	s, err := Load(context.Background(), sched, appDB, Inputs{Goals: g})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Projects) != 2 || len(s.Templates) != 3 {
		t.Fatalf("%d projects %d templates", len(s.Projects), len(s.Templates))
	}
	cyg, _ := s.Project(5)
	if !cyg.IsMosaic || cyg.Priority != "Low" || len(cyg.Targets) != 2 {
		t.Fatalf("%+v", cyg)
	}
	if cyg.WeakestTarget != "Cygnis Loop Panel 2" || cyg.Progress != 0 {
		t.Fatalf("weakest %q %v", cyg.WeakestTarget, cyg.Progress)
	}
	if cyg.Season == nil || cyg.Season.NightsLeft != 35 || math.Abs(cyg.Rarity-85.0/110) > 1e-9 {
		t.Fatalf("season %+v rarity %v", cyg.Season, cyg.Rarity)
	}
	if cyg.SetName != setHOO || cyg.LastSub == nil {
		t.Fatalf("set %q last %v", cyg.SetName, cyg.LastSub)
	}
	if cyg.RuleWeights[0].Weight != 100 || !cyg.RuleWeights[len(cyg.RuleWeights)-1].Missing {
		t.Fatalf("%+v", cyg.RuleWeights)
	}
	garlic, _ := s.Project(7)
	if garlic.Rarity != 0 || !garlic.Season.OutOfSeason {
		t.Fatalf("%+v", garlic.Season)
	}
	tg := garlic.Targets[0]
	if len(tg.Goals) != 2 || tg.SetName != setHOO || len(tg.Plans) != 3 {
		t.Fatalf("%+v", tg)
	}
	o3 := tg.Goals[1]
	if !o3.GoalSet || o3.Progress == nil || math.Abs(o3.Progress.Progress-(4.4/5)*(4.4/5)) > 1e-9 {
		t.Fatalf("%+v", o3)
	}
	if tg.Weakest.Filter != filterO3 || math.Abs(tg.Novelty-(1-0.7744)*0.8) > 1e-9 || !garlic.GoalDriven {
		t.Fatalf("novelty %v weakest %+v", tg.Novelty, tg.Weakest)
	}
	if tg.Plans[2].Exposure != 300 || tg.Plans[0].Exposure != 600 {
		t.Fatalf("%+v", tg.Plans)
	}
}

func TestSetName(t *testing.T) {
	t.Parallel()
	if SetName([]string{filterO3, "h-a"}) != setHOO || SetName([]string{"Luminance", "Red", "Green", "Blue"}) != "LRGB" {
		t.Fatal("preset")
	}
	if SetName([]string{filterHa}) != "Custom · H-a" || SetName(nil) != "No plans" {
		t.Fatal("custom")
	}
}

func TestNoveltyRarity(t *testing.T) {
	t.Parallel()
	if Novelty(0, 0) != 1 || Novelty(0.5, 2) != 0.4 || Novelty(1.2, 5) != 0 {
		t.Fatal("novelty")
	}
	if Rarity(&Season{NightsLeft: 10}) != 1 || Rarity(&Season{NightsLeft: 130}) != 0 || Rarity(&Season{OutOfSeason: true}) != 0 || Rarity(nil) != 0 {
		t.Fatal("rarity")
	}
}
