package planning

import (
	"context"
	"strings"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"gorm.io/gorm"
)

const (
	filterS2  = "S-II"
	filterLum = "Luminance"
)

const goalTable = `create table ts_goal (target_guid text not null, filter text not null, kind integer not null default 0, snr_goal real, depth_goal real, plateau_stop integer not null default 1, region text, updated_at timestamp, primary key (target_guid, filter))`

func goalsByTarget(d ApplySetDraft) map[string]map[string]schedcmd.GoalChange {
	out := map[string]map[string]schedcmd.GoalChange{}
	for _, ge := range d.Goals {
		for _, g := range ge.Goals {
			if out[g.TargetGUID] == nil {
				out[g.TargetGUID] = map[string]schedcmd.GoalChange{}
			}
			out[g.TargetGUID][g.Filter] = g
		}
	}
	return out
}

func TestDraftApplySetGivesNewFiltersAGoal(t *testing.T) {
	t.Parallel()
	sched, appDB := testDBs(t)
	for _, q := range []string{
		goalTable,
		`insert into ts_goal values ('g20','H-a',0,12,null,1,null,null),('g20','O-III',0,12,null,1,null,null)`,
		`insert into ts_goal values ('g13','Red',1,10,26.1,1,null,null),('g13','Green',1,10,26.3,1,null,null),('g13','H-a',0,4,null,0,null,null)`,
		`insert into exposuretemplate values (4,'p','S-II','S-II',100,10,1,2,1,45,7,85,600,0,0,1,'t4')`,
		`insert into project values (9,'p','Pacman',null,1,1,30,20,0,0,1,'pg9'),(10,'p','Bodes',null,1,1,30,20,0,0,1,'pg10')`,
		`insert into target values (30,'Pacman',1,0.88,56.6,0,9,'g30'),(31,'Bodes',1,9.9,69,0,10,'g31')`,
		`insert into exposureplan values (60,'p',600,40,0,0,30,4,1,'e60'),(61,'p',600,40,0,0,30,1,1,'e61'),(62,'p',300,150,0,0,31,3,1,'e62')`,
	} {
		if err := sched.Exec(q).Error; err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	s, err := Load(context.Background(), sched, appDB, Inputs{})
	if err != nil {
		t.Fatal(err)
	}
	sho := setNamed(t, s, "S-II, H-a").ID
	d, err := s.DraftApplySet(sho, "add", []int{20, 12}, 0, seq())
	if err != nil {
		t.Fatal(err)
	}
	got := goalsByTarget(d)
	s2 := got["g20"][filterS2]
	if s2.Before != nil || s2.After == nil || s2.After.Kind != schedcmd.GoalKindSNR || s2.After.SNRGoal != 12 || !s2.After.PlateauStop {
		t.Fatalf("S-II on Garlic should copy its narrowband goals: %+v", s2.After)
	}
	if _, ok := got["g20"]["H-a"]; ok {
		t.Fatal("Garlic already has an H-a goal")
	}
	if !strings.Contains(d.Effects[0].Effect, "sets the S-II goal SNR 12 (like the target's other narrowband goals)") {
		t.Fatalf("effect should name the goal: %q", d.Effects[0].Effect)
	}
	for _, f := range []string{filterS2, filterHa} {
		g := got["g12"][f]
		if g.After == nil || g.After.SNRGoal != 10 || !g.After.PlateauStop || g.After.Kind != schedcmd.GoalKindSNR {
			t.Fatalf("a target with no goals gets the default goal for %s: %+v", f, g.After)
		}
	}
	if len(d.Goals) != 2 || d.Goals[0].ProjectID != 7 || d.Goals[1].ProjectID != 5 {
		t.Fatalf("one goal edit per project, in target order: %+v", d.Goals)
	}
	if len(d.Payload.Targets) != 2 {
		t.Fatalf("%+v", d.Payload)
	}
	checkLuminanceGoals(t, sched, appDB, s)
}

func checkLuminanceGoals(t *testing.T, sched, appDB *gorm.DB, s *Snapshot) {
	t.Helper()
	lum := setNamed(t, s, filterLum).ID
	d, err := s.DraftApplySet(lum, "add", []int{13}, 0, seq())
	if err != nil {
		t.Fatal(err)
	}
	l := goalsByTarget(d)["g13"][filterLum]
	if l.After == nil || l.After.Kind != schedcmd.GoalKindDepth || l.After.DepthGoal == nil || *l.After.DepthGoal != 25.8 || !l.After.PlateauStop {
		t.Fatalf("Luminance on a depth target should be a default depth goal: %+v", l.After)
	}
	if !strings.Contains(d.Effects[0].Effect, "like the target's other broadband goals") {
		t.Fatalf("%q", d.Effects[0].Effect)
	}

	if err := sched.Exec(`insert into ts_goal values ('g31','Luminance',0,10,null,1,null,null)`).Error; err != nil {
		t.Fatal(err)
	}
	s, _ = Load(context.Background(), sched, appDB, Inputs{})
	d, err = s.DraftApplySet(lum, "add", []int{31}, 0, seq())
	if err != nil {
		t.Fatal(err)
	}
	if d.Payload != nil || len(d.Goals) != 0 || d.Effects[0].Changes {
		t.Fatalf("a target with the set and its goals changes nothing: %+v", d)
	}
}

func TestDraftApplySetAddsAMissingGoalWithoutPlanChanges(t *testing.T) {
	t.Parallel()
	sched, appDB := testDBs(t)
	if err := sched.Exec(goalTable).Error; err != nil {
		t.Fatal(err)
	}
	s, _ := Load(context.Background(), sched, appDB, Inputs{})
	d, err := s.DraftApplySet(setNamed(t, s, setHOO).ID, "add", []int{20}, 0, seq())
	if err != nil {
		t.Fatal(err)
	}
	if d.Payload != nil || len(d.Goals) != 1 || len(d.Goals[0].Goals) != 2 || !d.Effects[0].Changes || !strings.HasPrefix(d.Effects[0].Effect, "sets the H-a goal SNR 10") {
		t.Fatalf("%+v", d)
	}
}

func TestGoalForNewFilterMajority(t *testing.T) {
	t.Parallel()
	sched, appDB := testDBs(t)
	for _, q := range []string{
		goalTable,
		`insert into ts_goal values ('g12','Red',0,20,null,0,null,null),('g12','Green',0,20,null,0,null,null),('g12','Blue',1,10,26,1,null,null)`,
	} {
		if err := sched.Exec(q).Error; err != nil {
			t.Fatal(err)
		}
	}
	s, _ := Load(context.Background(), sched, appDB, Inputs{})
	g := s.GoalForNewFilter("g12", filterLum)
	if g.Setting.Kind != schedcmd.GoalKindSNR || g.Setting.SNRGoal != 20 || g.Setting.PlateauStop {
		t.Fatalf("%+v", g)
	}
	g = s.GoalForNewFilter("g12", filterS2)
	if g.Setting.SNRGoal != 20 || g.Basis != "like the target's other goals" {
		t.Fatalf("with no narrowband goals it falls back to all goals: %+v", g)
	}
	if GoalText(g.Setting) != "SNR 20, no plateau stop" {
		t.Fatal(GoalText(g.Setting))
	}
}

func TestGoalRowsMatchTheWayThePluginDoes(t *testing.T) {
	t.Parallel()
	sched, appDB := testDBs(t)
	for _, q := range []string{
		goalTable,
		`insert into ts_goal values ('g20','Ha',0,12,null,1,null,null),('g20','o-iii',0,12,null,1,null,null),('g12','H-a',0,10,null,1,null,null),('g12','O-III',0,10,null,1,null,null)`,
		`update target set guid = null where "Id" = 13`,
	} {
		if err := sched.Exec(q).Error; err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	s, _ := Load(context.Background(), sched, appDB, Inputs{})
	d, err := s.DraftApplySet(setNamed(t, s, setHOO).ID, "add", []int{20, 12, 13}, 0, seq())
	if err != nil {
		t.Fatal(err)
	}
	got := goalsByTarget(d)
	if len(got) != 1 || len(got["g20"]) != 1 || got["g20"][filterHa].After == nil {
		t.Fatalf("only Garlic H-a lacks a row the plugin reads (Ha is not H-a; o-iii matches O-III); a target with no guid is skipped: %+v", got)
	}
	p, _ := s.Project(7)
	if c := p.Targets[0].Least.CountBased; len(c) != 1 || c[0] != filterHa {
		t.Fatalf("count-based follows the target's own rows: %v", c)
	}
	p, _ = s.Project(5)
	if c := p.Targets[0].Least.CountBased; len(c) != 0 {
		t.Fatalf("panel 1 has both rows: %v", c)
	}
}
