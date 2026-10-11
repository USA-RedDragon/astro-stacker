package planning

import (
	"context"
	"testing"

	"gorm.io/gorm"
)

func ip(v int) *int { return &v }

func fp(v float64) *float64 { return &v }

func bp(v bool) *bool { return &v }

func TestProjectSettingsMajorityPerField(t *testing.T) {
	t.Parallel()
	var rows []projectRow
	add := func(id, n, dither, flats int) {
		for i := range n {
			rows = append(rows, projectRow{ID: id + i, ProfileID: "p", DitherEvery: ip(dither), FilterSwitchFrequency: ip(1), FlatsHandling: ip(flats), EnableGrader: 1})
		}
	}
	add(1, 40, 2, 1)
	add(100, 19, 1, 1)
	add(200, 4, 3, 0)
	add(300, 10, 0, 0)
	rows = append(rows, projectRow{ID: 999, ProfileID: "q2", DitherEvery: ip(7)})
	got := projectSettingsFor(rows, "p")
	want := ProjectSettings{DitherEvery: 2, FilterSwitchFrequency: 1, FlatsHandling: 1, EnableGrader: true}
	if got.ProjectSettings != want || got.Basis != SettingsBasisProfile || got.Projects != 73 || got.ProfileID != "p" {
		t.Fatalf("%+v", got)
	}
}

func TestProjectSettingsTieGoesToNewestProject(t *testing.T) {
	t.Parallel()
	rows := []projectRow{
		{ID: 1, ProfileID: "p", DitherEvery: ip(1), HorizonOffset: fp(5)},
		{ID: 2, ProfileID: "p", DitherEvery: ip(3), HorizonOffset: fp(0)},
		{ID: 9, ProfileID: "p", DitherEvery: ip(1), HorizonOffset: fp(0)},
		{ID: 7, ProfileID: "p", DitherEvery: ip(3), HorizonOffset: fp(5)},
		{ID: 8, ProfileID: "p", DitherEvery: ip(2), HorizonOffset: fp(2)},
	}
	got := projectSettingsFor(rows, "p")
	if got.DitherEvery != 1 || got.HorizonOffset != 0 {
		t.Fatalf("ties should go to the value of project 9: %+v", got)
	}
	rows[2].DitherEvery = ip(2)
	if got := projectSettingsFor(rows, "p"); got.DitherEvery != 2 {
		t.Fatalf("2 is used by projects 8 and 9: %+v", got)
	}
}

func TestProjectSettingsWithoutProjectsUseTargetSchedulerDefaults(t *testing.T) {
	t.Parallel()
	got := projectSettingsFor([]projectRow{{ID: 1, ProfileID: "q2", DitherEvery: ip(2)}}, "p")
	want := ProjectSettings{EnableGrader: true}
	if got.ProjectSettings != want || got.Basis != SettingsBasisTargetScheduler || got.Projects != 0 || got.ProfileID != "p" {
		t.Fatalf("%+v", got)
	}
	if got := projectSettingsFor(nil, ""); got.ProjectSettings != want || got.Basis != SettingsBasisTargetScheduler {
		t.Fatalf("%+v", got)
	}
}

func TestDefaultProfileOwnsMostProjects(t *testing.T) {
	t.Parallel()
	rows := []projectRow{{ID: 1, ProfileID: "a"}, {ID: 2, ProfileID: "b"}, {ID: 3, ProfileID: "b"}, {ID: 4, ProfileID: "a"}, {ID: 5, ProfileID: "a"}}
	if p := defaultProfile(rows); p != "a" {
		t.Fatalf("%q", p)
	}
	if p := defaultProfile(rows[:4]); p != "a" {
		t.Fatalf("tie should go to the profile of project 4: %q", p)
	}
	if p := defaultProfile(nil); p != "" {
		t.Fatalf("%q", p)
	}
}

func withSettingsColumns(t *testing.T, sched *gorm.DB) {
	t.Helper()
	for _, q := range []string{
		`alter table project add column ditherevery integer`,
		`alter table project add column filterswitchfrequency integer`,
		`alter table project add column "flatsHandling" integer`,
		`alter table project add column smartexposureorder integer`,
		`alter table project add column meridianwindow integer`,
		`alter table project add column usecustomhorizon integer`,
		`alter table project add column horizonoffset real`,
		`update project set ditherevery = 2, filterswitchfrequency = 1, "flatsHandling" = 1, smartexposureorder = 0, meridianwindow = 0, usecustomhorizon = 0, horizonoffset = 0`,
		`update project set ditherevery = 3, "flatsHandling" = 100, smartexposureorder = 1, meridianwindow = 60, usecustomhorizon = 1, horizonoffset = 2.5, "maximumAltitude" = 75 where "Id" = 7`,
	} {
		if err := sched.Exec(q).Error; err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

func TestSnapshotCarriesProjectSettings(t *testing.T) {
	t.Parallel()
	sched, appDB := testDBs(t)
	withSettingsColumns(t, sched)
	if err := sched.Exec(`insert into project values (9,'p','Pacman',null,1,1,30,20,0,0,1,'pg9',2,1,1,0,0,0,0)`).Error; err != nil {
		t.Fatal(err)
	}
	s, err := Load(context.Background(), sched, appDB, Inputs{})
	if err != nil {
		t.Fatal(err)
	}
	g, _ := s.Project(7)
	if g.DitherEvery != 3 || g.FilterSwitchFrequency != 1 || g.FlatsHandling != 100 || !g.SmartExposureOrder || g.MeridianWindow != 60 ||
		!g.UseCustomHorizon || g.HorizonOffset != 2.5 || g.MaximumAltitude != 75 || !g.Grader {
		t.Fatalf("%+v", g)
	}
	d := s.Defaults.ProjectSettings
	want := ProjectSettings{DitherEvery: 2, FilterSwitchFrequency: 1, FlatsHandling: 1, EnableGrader: true}
	if d.ProjectSettings != want || d.Basis != SettingsBasisProfile || d.Projects != 3 || d.ProfileID != "p" {
		t.Fatalf("%+v", d)
	}
}

func TestSnapshotSettingsWithoutColumnsAreZero(t *testing.T) {
	t.Parallel()
	sched, appDB := testDBs(t)
	s, err := Load(context.Background(), sched, appDB, Inputs{})
	if err != nil {
		t.Fatal(err)
	}
	d := s.Defaults.ProjectSettings
	if d.DitherEvery != 0 || !d.EnableGrader || d.Projects != 2 {
		t.Fatalf("%+v", d)
	}
}

func settingsSnapshot(t *testing.T) (*Snapshot, ProjectDraft) {
	t.Helper()
	sched, appDB := testDBs(t)
	withSettingsColumns(t, sched)
	if err := sched.Exec(`insert into project values (9,'p','Pacman',null,1,1,30,20,0,0,1,'pg9',2,1,1,0,0,0,0)`).Error; err != nil {
		t.Fatal(err)
	}
	s, err := Load(context.Background(), sched, appDB, Inputs{})
	if err != nil {
		t.Fatal(err)
	}
	return s, ProjectDraft{Name: "Lagoon", MinimumTime: 30, MinimumAltitude: 30, Plans: []PlanDraft{{TemplateID: 1, Exposure: 300}}, Desired: 50, Panels: []PanelDraft{{RAHours: 18.3, Dec: -13.8}}}
}

func TestDraftProjectFillsEverySetting(t *testing.T) {
	t.Parallel()
	s, base := settingsSnapshot(t)
	p, err := s.DraftProject(base, seq())
	if err != nil {
		t.Fatal(err)
	}
	ps := p.Project.ProjectSettings
	if ps.DitherEvery == nil || ps.FilterSwitchFrequency == nil || ps.FlatsHandling == nil || ps.SmartExposureOrder == nil || ps.MeridianWindow == nil ||
		ps.UseCustomHorizon == nil || ps.HorizonOffset == nil || ps.MaximumAltitude == nil || ps.EnableGrader == nil {
		t.Fatalf("every setting should be filled: %+v", ps)
	}
	if *ps.DitherEvery != 2 || *ps.FilterSwitchFrequency != 1 || *ps.FlatsHandling != 1 || *ps.SmartExposureOrder || *ps.MeridianWindow != 0 ||
		*ps.UseCustomHorizon || *ps.HorizonOffset != 0 || *ps.MaximumAltitude != 0 || !*ps.EnableGrader {
		t.Fatalf("defaults should come from the profile: %+v", ps)
	}
	d := base
	d.DitherEvery, d.FlatsHandling, d.SmartExposureOrder, d.MaximumAltitude, d.EnableGrader = ip(5), ip(200), bp(true), fp(80), bp(false)
	p, err = s.DraftProject(d, seq())
	if err != nil {
		t.Fatal(err)
	}
	ps = p.Project.ProjectSettings
	if *ps.DitherEvery != 5 || *ps.FlatsHandling != 200 || !*ps.SmartExposureOrder || *ps.MaximumAltitude != 80 || *ps.EnableGrader || *ps.FilterSwitchFrequency != 1 {
		t.Fatalf("given values should win: %+v", ps)
	}
}

func TestDraftProjectRejectsBadSettings(t *testing.T) {
	t.Parallel()
	s, base := settingsSnapshot(t)
	for _, bad := range []func(*ProjectDraft){
		func(d *ProjectDraft) { d.FlatsHandling = ip(150) },
		func(d *ProjectDraft) { d.DitherEvery = ip(1000) },
		func(d *ProjectDraft) { d.MeridianWindow = ip(721) },
		func(d *ProjectDraft) { d.HorizonOffset = fp(91) },
		func(d *ProjectDraft) { d.MaximumAltitude = fp(30) },
	} {
		d := base
		bad(&d)
		if _, err := s.DraftProject(d, seq()); err == nil {
			t.Fatalf("accepted %+v", d)
		}
	}
}

func TestDraftProjectWithoutProjectsUsesTargetSchedulerDefaults(t *testing.T) {
	t.Parallel()
	sched, appDB := testDBs(t)
	withSettingsColumns(t, sched)
	if err := sched.Exec(`update exposuretemplate set "profileId" = 'q' where "Id" = 1`).Error; err != nil {
		t.Fatal(err)
	}
	s, err := Load(context.Background(), sched, appDB, Inputs{})
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.DraftProject(ProjectDraft{Name: "Lagoon", MinimumTime: 30, Plans: []PlanDraft{{TemplateID: 1, Exposure: 300}}, Desired: 50, Panels: []PanelDraft{{}}}, seq())
	if err != nil {
		t.Fatal(err)
	}
	ps := p.Project.ProjectSettings
	if *ps.DitherEvery != 0 || *ps.FilterSwitchFrequency != 0 || *ps.FlatsHandling != 0 || !*ps.EnableGrader {
		t.Fatalf("profile q has no projects: %+v", ps)
	}
}
