package discoveries

import (
	"context"
	"strings"
	"testing"
)

// m31Galaxy is M 31's catalogue circle.
func m31Galaxy() Galaxy {
	return Galaxy{Name: "M 31", RA: 10.6847, Dec: 41.2690, Major: 190, Minor: 60, LocalGroup: true}
}

func newSource(x, y float64) Candidate {
	return Candidate{X: x, Y: y, RA: 10.70 + x/1e5, Dec: 41.30 + y/1e5, Sign: 1, SNR: 9, LocalSNR: 8, PresentIn: 8, SubsTested: 8, SkyFixed: true}
}

func heldBy(l Labelled, name string) bool {
	m := l.Matches[len(l.Matches)-1]
	return l.Class == ClassLowConfidence && m.Catalog == catalogMany && strings.HasPrefix(m.Name, name)
}

// TestClusterOfNewSourcesIsHeld replays M 31's bulge of 2025-10-27, whose
// 21 sky-fixed residuals sat within 64 px of each other, against AT
// 2026acsi and AT 2026abeq, two real novae 7.6 px apart.
func TestClusterOfNewSourcesIsHeld(t *testing.T) {
	t.Parallel()
	m31 := Known{Galaxies: []Galaxy{m31Galaxy()}}
	ctx := context.Background()
	weak := newSource(3140, 1990)
	weak.SNR, weak.LowConf = 6, true
	res := &Result{Candidates: []Candidate{newSource(3100, 1950), newSource(3140, 1960), newSource(3120, 1985), weak, newSource(1000, 1000), newSource(1007.6, 1000)}}
	ls := classifyChanges(ctx, t, res, m31)
	for i, l := range ls[:3] {
		if !heldBy(l, "cluster: 4 new sources within 64 px") {
			t.Errorf("cluster member %d: %s %+v", i, l.Class, l.Matches)
		}
	}
	for _, l := range ls[4:] {
		if l.Class != ClassNova {
			t.Errorf("a pair of novae 7.6 px apart: %s %+v", l.Class, l.Matches)
		}
	}
	res.Candidates = res.Candidates[:3]
	for _, l := range classifyChanges(ctx, t, res, m31) {
		if l.Class != ClassNova {
			t.Errorf("three within 64 px: %s %+v", l.Class, l.Matches)
		}
	}
}

func TestSkyFixedNewSourcesAreHeldOnANightOfMany(t *testing.T) {
	t.Parallel()
	res := &Result{}
	for i := range maxNewSources + 1 {
		res.Candidates = append(res.Candidates, newSource(float64(i)*100, 500))
	}
	for _, l := range classifyChanges(context.Background(), t, res, Known{}) {
		if !heldBy(l, "night had 11 new sources") {
			t.Errorf("%s %+v", l.Class, l.Matches)
		}
	}
}

// TestNoisyNightHoldsItsNewSources replays IC 1396's 2025-08-03: 1,628
// brightenings where no star was, nearly all held for weakness, and 8 new
// sources sent to vetting.
func TestNoisyNightHoldsItsNewSources(t *testing.T) {
	t.Parallel()
	mk := func(n int) *Result {
		res := &Result{Candidates: []Candidate{newSource(0, 0)}}
		for i := 1; i < n; i++ {
			c := newSource(float64(i%40)*100, float64(i/40)*100+100)
			c.SNR, c.LowConf = 5.5, true
			res.Candidates = append(res.Candidates, c)
		}
		return res
	}
	ctx := context.Background()
	if l := classifyChanges(ctx, t, mk(maxNoStarChanges), Known{})[0]; l.Class != ClassNewVariable {
		t.Errorf("%d brightenings where no star was: %s %+v", maxNoStarChanges, l.Class, l.Matches)
	}
	if l := classifyChanges(ctx, t, mk(maxNoStarChanges+1), Known{})[0]; !heldBy(l, "night had 101 brightenings where no star was") {
		t.Errorf("%d brightenings where no star was: %s %+v", maxNoStarChanges+1, l.Class, l.Matches)
	}
}
