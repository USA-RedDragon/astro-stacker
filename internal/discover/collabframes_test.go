package discover_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discover"
	"github.com/USA-RedDragon/astro-stacker/internal/starfront"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

const (
	ownObject = "M31"
	panelOne  = "M31 COLLAB panel 1"
)

func TestCollabFramesAreOursTaggedAndNotExcluded(t *testing.T) {
	t.Parallel()
	s := newService(t)
	project, panel1, panel2 := "d07d82eb861e", 1, 2
	n1, n2 := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	exp300, exp600 := 300.0, 600.0
	ra, dec := 10.68, 41.27
	frames := []app.Frame{
		{Key: "c1", Object: panelOne, Filter: hydrogen, Exposure: &exp300, Night: &n1, CollabProject: &project, CollabPanel: &panel1},
		{Key: "c2", Object: panelOne, Filter: hydrogen, Exposure: &exp300, Night: &n2, CollabProject: &project, CollabPanel: &panel1},
		{Key: "c3", Object: "M31 COLLAB panel 2", Filter: oxygen, Exposure: &exp600, Night: &n2, CollabProject: &project, CollabPanel: &panel2},
		{Key: "o1", Object: ownObject, Filter: hydrogen, Exposure: &exp300, Night: &n2},
	}
	for _, f := range frames {
		f.ETag, f.Size, f.LastModified, f.Type, f.MountRA, f.MountDec = "e", 1, n2, lightType, &ra, &dec
		if err := s.AppDB.Create(&f).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := s.AppDB.AutoMigrate(&app.StackFrame{}); err != nil {
		t.Fatal(err)
	}
	stack := app.Stack{Object: panelOne, Filter: hydrogen, Subs: 3, EffectiveSeconds: 900}
	if err := s.AppDB.Create(&stack).Error; err != nil {
		t.Fatal(err)
	}
	ids := map[string]int{}
	var all []app.Frame
	s.AppDB.Find(&all)
	for _, f := range all {
		ids[f.Key] = f.ID
	}
	for i, sf := range []app.StackFrame{{FrameID: ids["c1"], Status: app.StackStatusAdded}, {FrameID: ids["c2"], Status: "rejected"}, {FrameID: ids["o1"], Status: app.StackStatusAdded}} {
		sf.StackID = &stack.ID
		sf.ID = 1000 + i
		if err := s.AppDB.Create(&sf).Error; err != nil {
			t.Fatal(err)
		}
	}
	src := fakeCollabs{starfront.State{Enabled: true, Projects: []starfront.Project{{ID: project, Name: "M31 NINA TEST COLLAB", Status: starfront.StatusOpen,
		Payload: starfront.Payload{Kind: single, Region: starfront.Region{RA: ra, Dec: dec, Width: 3, Height: 2}}}}}}
	v, err := s.Collabs(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if v.FramesError != "" || len(v.Frames) != 1 || v.SharedPrefix != starfront.SharedPrefix {
		t.Fatalf("frames %+v %q", v.Frames, v.FramesError)
	}
	f := v.Frames[0]
	if f.Project != project || f.Name != "M31 NINA TEST COLLAB" || f.Frames != 3 || f.Nights != 2 || f.FirstNight != "2026-10-12" || f.LastNight != "2026-10-13" || f.Hours != 0.33 {
		t.Errorf("summary %+v", f)
	}
	if !slices.Equal(f.Filters, []discover.CollabFilter{{Filter: hydrogen, Exposure: 300, Frames: 2}, {Filter: oxygen, Exposure: 600, Frames: 1}}) {
		t.Errorf("filters %+v", f.Filters)
	}
	if len(f.Targets) != 2 || f.Targets[0].Object != panelOne || *f.Targets[0].Panel != 1 || f.Targets[0].Frames != 2 ||
		!slices.Equal(f.Targets[0].Stacked, []discover.CollabStacked{{Filter: hydrogen, Frames: 1}}) || len(f.Targets[1].Stacked) != 0 || f.Targets[1].Stacked == nil {
		t.Errorf("targets %+v", f.Targets)
	}
	checkCollabSubjects(t, s)
}

func checkCollabSubjects(t *testing.T, s *discover.Service) {
	t.Helper()
	subjects, err := s.Subjects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, x := range subjects {
		names[x.Name] = true
		for _, t := range x.Targets {
			names[t] = true
		}
	}
	for _, want := range []string{"M31 COLLAB", panelOne, "M31 COLLAB panel 2", ownObject} {
		if !names[want] {
			t.Errorf("subject %q left out", want)
		}
	}
}

func TestNoCollabFramesIsAnEmptyList(t *testing.T) {
	t.Parallel()
	s := newService(t)
	v, err := s.Collabs(context.Background(), nil)
	if err != nil || v.Frames == nil || len(v.Frames) != 0 || v.FramesError != "" {
		t.Errorf("%+v %v", v.Frames, err)
	}
}
