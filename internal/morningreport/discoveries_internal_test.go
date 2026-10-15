package morningreport

import (
	"context"
	"strings"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

const (
	objM31         = "M 31"
	groupBroadband = "broadband"
)

func TestDiscoveriesLine(t *testing.T) {
	t.Parallel()
	db := appDB(t)
	if err := db.AutoMigrate(&app.DiscoveryRun{}, &app.DiscoveryCandidate{}); err != nil {
		t.Fatal(err)
	}
	night := day(night1013)
	runs := []app.DiscoveryRun{
		{Object: objM31, Night: night, Group: groupBroadband, State: app.DiscoveryDone, NewCandidates: 2},
		{Object: objM31, Night: night, Group: "H-a", State: app.DiscoveryDone},
		{Object: "Leo Triplet", Night: night, Group: groupBroadband, State: app.DiscoveryDone, NewCandidates: 1},
		{Object: "M 33", Night: night, Group: groupBroadband, State: app.DiscoveryRunning, NewCandidates: 5},
		{Object: objM31, Night: night.AddDate(0, 0, -1), Group: groupBroadband, State: app.DiscoveryDone, NewCandidates: 4},
	}
	if err := db.Create(&runs).Error; err != nil {
		t.Fatal(err)
	}
	cands := []app.DiscoveryCandidate{
		{Object: objM31, Night: night, Group: groupBroadband, Kind: app.DiscoveryKindChange, Class: discoveries.ClassNova},
		{Object: objM31, Night: night, Group: groupBroadband, Kind: app.DiscoveryKindChange, Class: discoveries.ClassNova, Vetting: app.VetArtifact},
		{Object: objM31, Night: night.AddDate(0, 0, -1), Group: groupBroadband, Kind: app.DiscoveryKindChange, Class: discoveries.ClassNova},
		{Object: "Leo Triplet", Night: night, Group: groupBroadband, Kind: app.DiscoveryKindChange, Class: discoveries.ClassSupernova},
	}
	if err := db.Create(&cands).Error; err != nil {
		t.Fatal(err)
	}
	r := Report{Night: night}
	Collector{App: db}.discoveries(context.Background(), &r)
	f := formatter(t)
	f.WebURL = "https://astro.example/"
	got := f.discoveries(r)
	want := `<b>Discoveries</b> 2 new candidates on M 31 (1 possible nova), 1 on Leo Triplet: <a href="https://astro.example/discoveries?night=2026-10-13">astro-processing</a>`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if !strings.Contains(f.Text(sampleReportWith(r.Discoveries, r.Novae)), want) {
		t.Error("the line is not in the report")
	}
	if f.discoveries(Report{Night: night}) != "" {
		t.Error("a night with nothing new has no discoveries line")
	}
}

func sampleReportWith(d []Count, novae map[string]int) Report {
	r := sampleReport()
	r.Discoveries, r.Novae = d, novae
	return r
}
