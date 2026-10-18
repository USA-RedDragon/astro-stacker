package discoveries

import (
	"context"
	"testing"
)

// TestNewSourceOnAStar replays the audit of October 2026: 60400 sat 2.0
// px from a G 15.68 star and 58090 2.35 px from a G 14.87 star, both
// steady, and moved the light there by 0.09 mag; AT 2026uti, a real nova
// 2.8 px from a G 15.77 star, moved it by 0.15.
func TestNewSourceOnAStar(t *testing.T) {
	t.Parallel()
	m31 := m31Galaxy()
	f := func(v float64) *float64 { return &v }
	for _, tc := range []struct {
		name      string
		g, px, dm float64
		want      string
	}{
		{"60400", 15.68, 2.0, -0.09, ClassLowConfidence},
		{"58090", 14.87, 2.35, -0.09, ClassLowConfidence},
		{"AT 2026uti", 15.77, 2.8, -0.15, ClassNova},
	} {
		c := Candidate{RA: 10.70, Dec: 41.30, Sign: 1, SNR: 7.4, LocalSNR: 7, PresentIn: 8, SubsTested: 8,
			NearStarG: f(tc.g), NearStarPx: tc.px, DeltaMag: f(tc.dm)}
		ls := classifyChanges(context.Background(), t, &Result{Candidates: []Candidate{c}, PixelScale: 1.9}, Known{Galaxies: []Galaxy{m31}})
		onStar := false
		for _, m := range ls[0].Matches {
			onStar = onStar || m.Catalog == catalogOnStar
		}
		if ls[0].Class != tc.want || onStar != (tc.want == ClassLowConfidence) {
			t.Errorf("%s: %s %+v", tc.name, ls[0].Class, ls[0].Matches)
		}
	}
}

func TestNearStarLooksPastTheGaiaMatch(t *testing.T) {
	t.Parallel()
	gs := []float64{15.68, 17.4, 14.0}
	pts := [][2]float64{{102, 100}, {100, 101.8}, {150, 150}}
	g := gaiaIndex{grid: NewGrid(pts, 8), from: pts, idx: []int{0, 1, 2}}
	bright := func(i int) bool { return gs[i] < onStarMaxG }
	if i, _, _ := g.match(100, 100); i >= 0 {
		t.Fatalf("matched star %d past gaiaMatchPx", i)
	}
	if i, d := g.near(100, 100, bright); i != 0 || d != 2 {
		t.Errorf("star %d at %.2f px, want the G 15.68 star at 2 px", i, d)
	}
	if i, _ := g.near(100, 100, func(int) bool { return true }); i != 1 {
		t.Errorf("star %d: the nearest of any brightness is the G 17.4 one", i)
	}
	if i, _ := g.near(140, 140, bright); i >= 0 {
		t.Errorf("star %d over 3 px away", i)
	}
}
