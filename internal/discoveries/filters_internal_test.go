package discoveries

import (
	"context"
	"math"
	"strings"
	"testing"
)

// TestRunFindsAOneFilterSource replays AT 2026aeob on Pelican Nebula Panel
// 2's 2026-10-08: a source in R alone, over 5 sigma in R's own comparison
// and under it in R, G and B combined, is found by R's, said so, and kept
// to its own null test's bar.
func TestRunFindsAOneFilterSource(t *testing.T) {
	t.Parallel()
	if underRace {
		t.Skip("a synthetic night takes ten minutes under the race detector; TestRunFindsSyntheticChanges runs the same code there")
	}
	f := newField(t)
	f.red, f.RedFlux = true, 0.045
	f.RedX, f.RedY = f.emptySpot(t, 1400, 300)
	res, err := Run(context.Background(), f.filterInput(t, []string{filterRed, "Green", "Blue"}, nil))
	if err != nil {
		t.Fatal(err)
	}
	var red, newSrc *Candidate
	for i, c := range res.Candidates {
		t.Logf("candidate %+d at %.1f,%.1f snr %.1f filter %q present %d/%d low %v", c.Sign, c.X, c.Y, c.SNR, c.Filter, c.PresentIn, c.SubsTested, c.LowConf)
		if math.Hypot(c.X-f.RedX, c.Y-f.RedY) < 1.5 && c.Sign > 0 {
			red = &res.Candidates[i]
		}
		if math.Hypot(c.X-f.NewX, c.Y-f.NewY) < 1.5 && c.Sign > 0 {
			newSrc = &res.Candidates[i]
		}
	}
	if red == nil {
		for _, r := range res.Rejected {
			if math.Hypot(r.X-f.RedX, r.Y-f.RedY) < 3 {
				t.Logf("rejected %+v", r)
			}
		}
		t.Fatalf("the red source at %.1f,%.1f was not found", f.RedX, f.RedY)
	}
	if red.Filter != filterRed || red.LowConf || red.SubsTested != f.PerNight {
		t.Errorf("the red source: filter %q, low confidence %v, %d subs tested", red.Filter, red.LowConf, red.SubsTested)
	}
	if newSrc == nil || newSrc.Filter != "" {
		t.Errorf("the source in every filter was not found by the filters combined: %+v", newSrc)
	}
	ls, tracks, _, _ := Classify(context.Background(), res, Known{}, nil)
	t.Logf("%d tracks", len(tracks))
	for _, l := range ls {
		if l.Filter == "" {
			continue
		}
		said := false
		for _, m := range l.Matches {
			said = said || (m.Catalog == catalogFilter && m.Name == l.Filter)
		}
		if !said {
			t.Errorf("a change found in %s alone does not say so: %+v", l.Filter, l.Matches)
		}
	}
	if res.ChangeNull.Subs == 0 {
		t.Error("no null test")
	}
	t.Logf("null: %d combined (max %.1f), per filter %v (max %v)", res.ChangeNull.Found, res.ChangeNull.MaxSNR, res.ChangeNull.FilterFound, res.ChangeNull.FilterMaxSNR)
}

func TestAgainstOtherFilters(t *testing.T) {
	t.Parallel()
	c := cand{Candidate: Candidate{Sign: 1, Filter: filterRed}}
	add := func(f string, fl float64, n int) {
		for range n {
			c.Light = append(c.Light, LightPoint{Filter: f})
			c.fluxes, c.errs = append(c.fluxes, fl), append(c.errs, 1)
		}
	}
	add(filterRed, 3, 6)
	add("Green", 0.5, 6)
	if why := c.against(); why != "" {
		t.Errorf("agreeing filters: %s", why)
	}
	add("Blue", -1.5, 6)
	if why := c.against(); !strings.Contains(why, "Blue") {
		t.Errorf("Blue fainter at %.1f sigma: %q", -1.5*math.Sqrt(6), why)
	}
}
