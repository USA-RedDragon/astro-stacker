package mosaicplan

import "testing"

func TestSeasonBoosts(t *testing.T) {
	t.Parallel()
	d := Detail{Panels: []Panel{{Number: 1, TargetGUID: "g1"}, {Number: 2, TargetGUID: "g2"}, {Number: 3, TargetGUID: "g3"}, {Number: 4}}}
	got := seasonBoosts(d, []PanelSeasonPriority{
		{Panel: 1, Priority: 0.4, ThisSeason: true},
		{Panel: 2, Priority: 0.1, ThisSeason: true},
		{Panel: 3, Priority: 0.9},
		{Panel: 4, Priority: 0.5, ThisSeason: true},
	})
	want := map[string]float64{"g1": 0.8, "g2": 0.2, "g3": 0}
	if len(got) != len(want) {
		t.Fatalf("boosts %v", got)
	}
	for k, v := range want {
		if d := got[k] - v; d > 1e-9 || d < -1e-9 {
			t.Errorf("%s: %v want %v", k, got[k], v)
		}
	}
	none := seasonBoosts(d, []PanelSeasonPriority{{Panel: 1}, {Panel: 2}})
	if none["g1"] != 0 || none["g2"] != 0 || len(none) != 2 {
		t.Errorf("no season boosts %v", none)
	}
}
