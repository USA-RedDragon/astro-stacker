package vetting

import (
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoverysource"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/testdb"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/surveys"
)

const barnardsE = "Barnard's E"

func lineDue(id int, object, vetting, suggested, confidence, pending string) Due {
	d := Due{Summary: discoverysource.Summary{Source: app.DiscoverySource{ID: id, Object: object, Kind: app.DiscoveryKindChange, Vetting: vetting}}}
	if suggested != "" {
		d.Prior = &app.DiscoveryVet{SourceID: id, State: StateDone, Suggested: suggested, Confidence: confidence, Pending: pending}
	}
	return d
}

// The waiting rows of 2026-10-20, in Pick's order: Barnard's E 3635, real
// at low confidence and bound for Watching once ATLAS answers, sat behind
// Andromeda's 40 artifacts every run.
func lineDues() []Due {
	asked := time.Date(2026, 10, 20, 3, 0, 0, 0, time.UTC)
	ds := []Due{
		lineDue(1, "Andromeda", "", app.VetArtifact, Medium, lc.ATLAS),
		lineDue(2, "Andromeda", "", app.VetArtifact, High, lc.ATLAS),
		lineDue(3635, barnardsE, "", app.VetReal, Low, lc.ATLAS),
		lineDue(4, "Caldwell 14", "", app.VetUnsure, Low, lc.ATLAS),
		lineDue(5, "Caldwell 14", "", app.VetArtifact, Medium, lc.ATLAS),
		lineDue(6, "Horsehead Nebula", "", "", "", ""),
		lineDue(7, "M 15", "", app.VetKnown, High, lc.ATLAS),
		lineDue(8, "Orion", app.VetReal, app.VetReal, High, lc.ATLAS),
		lineDue(9, "Sombrero Galaxy", "", app.VetArtifact, Low, lc.ATLAS+","+lc.TESS),
		lineDue(10, "Sombrero Galaxy", "", app.VetArtifact, Medium, lc.ATLAS),
		lineDue(11, "Abell 85", "", app.VetArtifact, Medium, lc.ATLAS),
	}
	ds[10].AskedAt = &asked
	return ds
}

func TestATLASPlaces(t *testing.T) {
	t.Parallel()
	want := map[int]surveys.Priority{1: atlasSettled, 2: atlasSettled, 3635: atlasOpen, 4: atlasOpen, 5: atlasSettled, 6: atlasNew,
		7: atlasSettled, 8: atlasRefresh, 9: atlasOpen, 10: atlasSettled, 11: atlasAsked}
	for _, d := range lineDues() {
		if got := atlasPlace(d); got != want[d.Summary.Source.ID] {
			t.Errorf("source %d placed %d, want %d", d.Summary.Source.ID, got, want[d.Summary.Source.ID])
		}
	}
}

func TestBatchesPutATLASLineFirst(t *testing.T) {
	t.Parallel()
	got := batches(lineDues(), nil)
	want := []struct {
		object string
		ids    []int
	}{
		{"Abell 85", []int{11}},
		{barnardsE, []int{3635}},
		{"Caldwell 14", []int{4, 5}},
		{"Sombrero Galaxy", []int{9, 10}},
		{"Horsehead Nebula", []int{6}},
		{"Orion", []int{8}},
		{"Andromeda", []int{1, 2}},
		{"M 15", []int{7}},
	}
	if len(got) != len(want) {
		t.Fatalf("%d batches, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].object != w.object || len(got[i].dues) != len(w.ids) {
			t.Fatalf("batch %d is %s with %d sources, want %s %v", i, got[i].object, len(got[i].dues), w.object, w.ids)
		}
		for j, id := range w.ids {
			if got[i].dues[j].Summary.Source.ID != id {
				t.Errorf("batch %d source %d is %d, want %d", i, j, got[i].dues[j].Summary.Source.ID, id)
			}
		}
	}
}

func TestATLASLineAhead(t *testing.T) {
	t.Parallel()
	ds := lineDues()
	l := newATLASLine(ds)
	if n := l.ahead(atlasSettled); n != 6 {
		t.Fatalf("%d ahead of the settled, want 6", n)
	}
	if n := l.ahead(atlasOpen); n != 1 {
		t.Fatalf("%d ahead of the open, want 1", n)
	}
	for _, d := range ds {
		if d.Summary.Source.ID == 11 || d.Summary.Source.ID == 3635 {
			l.visit(d)
		}
	}
	if n := l.ahead(atlasNew); n != 2 {
		t.Fatalf("%d ahead of the new after two visits, want 2", n)
	}
	var none *atlasLine
	none.visit(ds[0])
	if none.ahead(atlasSettled) != 0 {
		t.Fatal("a nil line kept room")
	}
}

func TestATLASWaitingCount(t *testing.T) {
	t.Parallel()
	db := testdb.SQLite(t, "atlaswaiting")
	if err := db.AutoMigrate(&app.DiscoverySource{}, &app.DiscoveryVet{}); err != nil {
		t.Fatal(err)
	}
	srcs := []app.DiscoverySource{
		{Object: "A", Kind: app.DiscoveryKindChange, Night: time.Now()},
		{Object: "A", Kind: app.DiscoveryKindChange, Night: time.Now(), Vetting: app.VetReal},
		{Object: "A", Kind: app.DiscoveryKindChange, Night: time.Now(), Vetting: app.VetArtifact},
		{Object: "A", Kind: app.DiscoveryKindChange, Night: time.Now()},
		{Object: "A", Kind: app.DiscoveryKindChange, Night: time.Now()},
	}
	if err := db.Create(&srcs).Error; err != nil {
		t.Fatal(err)
	}
	pend := []string{lc.ATLAS, lc.TESS + "," + lc.ATLAS, lc.ATLAS, lc.TESS, ""}
	for i, s := range srcs {
		if err := db.Create(&app.DiscoveryVet{SourceID: s.ID, State: StateDone, Pending: pend[i]}).Error; err != nil {
			t.Fatal(err)
		}
	}
	n, err := ATLASWaiting(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("%d waiting, want 2", n)
	}
}
