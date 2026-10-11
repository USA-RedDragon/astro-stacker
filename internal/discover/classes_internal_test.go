package discover

import (
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/catalog"
)

func TestProjectClassesTakeTheFirstLinkedObject(t *testing.T) {
	t.Parallel()
	link := func(subject, typ, status string) Link {
		return Link{Subject: subject, Object: catalog.Object{Type: typ}, Status: status}
	}
	snap := &snapshot{
		subjects: []Subject{
			{Key: "project:Horsehead", Kind: SubjectProject, ProjectID: 9},
			{Key: "project:M 34", Kind: SubjectProject, ProjectID: 67},
			{Key: "project:Orion", Kind: SubjectProject, ProjectID: 3},
			{Key: "project:Odd", Kind: SubjectProject, ProjectID: 80},
			{Key: "object:M 31", Kind: SubjectObject},
		},
		bySubj: map[string][]Link{
			"project:Horsehead": {link("project:Horsehead", catalog.TypeDark, StatusImaged), link("project:Horsehead", catalog.TypeEmission, StatusImaged)},
			"project:M 34":      {link("project:M 34", catalog.TypeEmission, StatusRejected), link("project:M 34", catalog.TypeOpenCluster, StatusPlanned)},
			"project:Orion":     {link("project:Orion", catalog.TypeClusterNebula, StatusConfirmed)},
			"project:Odd":       {link("project:Odd", catalog.TypeStar, StatusAuto)},
			"object:M 31":       {link("object:M 31", catalog.TypeGalaxy, StatusAuto)},
		},
	}
	got := projectClasses(snap)
	want := map[int]string{9: ClassDark, 67: ClassCluster, 3: ClassEmission}
	if len(got) != len(want) {
		t.Fatalf("%v", got)
	}
	for id, c := range want {
		if got[id] != c {
			t.Errorf("project %d: %q want %q", id, got[id], c)
		}
	}
}
