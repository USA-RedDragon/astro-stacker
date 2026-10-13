package horizonsurvey_test

import (
	"math"
	"testing"
	"time"

	hs "github.com/USA-RedDragon/astro-stacker/internal/horizonsurvey"
)

func TestAimKeepsTheHourAngleOffTheMeridian(t *testing.T) {
	t.Parallel()
	site := testSite()
	rows := testPlanConfig().Rows(0)
	for _, az := range []float64{0, 180} {
		if got := site.ColumnPiers(az, rows); len(got) != 2 {
			t.Fatalf("column az %v on the meridian should allow both pier sides, got %v", az, got)
		}
		for _, pier := range []string{hs.PierWest, hs.PierEast} {
			for _, alt := range rows {
				aAz, aAlt, ha, _, ok := site.Aim(az, alt, pier)
				if !ok {
					t.Fatalf("az %v alt %v cannot be aimed on %s", az, alt, pier)
				}
				if hs.NaturalPier(ha) != pier || math.Abs(ha) < hs.PierGuard-1e-9 || math.Abs(ha) > 180-hs.PierGuard+1e-9 {
					t.Fatalf("az %v alt %v on %s aims at HA %.3f", az, alt, pier, ha)
				}
				if d := hs.Separation(hs.HorVec(az, alt), hs.HorVec(aAz, aAlt)); d > hs.PierGuard {
					t.Fatalf("aiming moved az %v alt %v by %.2f deg", az, alt, d)
				}
				if math.Abs(aAlt-alt) > 1e-6 {
					t.Fatalf("aiming changed the altitude of az %v alt %v to %v", az, alt, aAlt)
				}
			}
		}
	}
	if got := site.ColumnPiers(90, rows); len(got) != 1 || got[0] != hs.PierWest {
		t.Fatalf("an eastern column must be on pierWest only, got %v", got)
	}
	if got := site.ColumnPiers(270, rows); len(got) != 1 || got[0] != hs.PierEast {
		t.Fatalf("a western column must be on pierEast only, got %v", got)
	}
	if _, _, _, _, ok := site.Aim(90, 10, hs.PierEast); ok {
		t.Fatal("an eastern target was aimed on pierEast")
	}
	if got := hs.ChoosePier([]string{hs.PierWest, hs.PierEast}, hs.PierEast); got != hs.PierEast {
		t.Fatalf("a column open to both sides must stay on the current side, got %s", got)
	}
}

func TestPlanFlipsOnceBetweenColumns(t *testing.T) {
	t.Parallel()
	pc := testPlanConfig()
	pc.AzStep = 15
	p, err := hs.MakePlan(testSite(), pc, hs.DefaultSafety(), deepNight(), 15*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if p.Flips != 1 {
		t.Fatalf("got %d pier flips, want 1; order %v", p.Flips, p.Columns)
	}
	side := map[int]string{}
	flips := 0
	prev := ""
	for i, pp := range p.Positions {
		if pp.Err != nil {
			t.Fatalf("deep-night position az %v alt %v skipped: %v", pp.Az, pp.Alt, pp.Err)
		}
		if s, ok := side[pp.Column]; ok && s != pp.Pier {
			t.Fatalf("column %d changes pier side within the column", pp.Column)
		}
		side[pp.Column] = pp.Pier
		if prev != "" && pp.Pier != prev {
			flips++
			if !pp.Flip || p.Positions[i-1].Column == pp.Column {
				t.Fatalf("pier changes at column %d row %d without being marked as the column's flip", pp.Column, pp.Row)
			}
		}
		prev = pp.Pier
	}
	if flips != 1 {
		t.Fatalf("the slew sequence changes pier side %d times", flips)
	}
}

func TestOrderFromEastSideFlipsAtMostOnce(t *testing.T) {
	t.Parallel()
	pc := testPlanConfig()
	rows := pc.Rows(0)
	start := hs.Mech{Phi: -30, Psi: -50}
	o := hs.ChooseOrder(testSite(), hs.DefaultSafety(), pc.Columns(), rows, time.Date(2026, 10, 13, 2, 40, 0, 0, time.UTC), start, 45*time.Second)
	if o.Flips > 1 {
		t.Fatalf("order %v flips %d times", o.Columns, o.Flips)
	}
	if first := o.Piers[0]; first != hs.PierEast {
		t.Fatalf("starting on pierEast, the first column is on %s", first)
	}
}

func TestNextRowFollowsTheEdge(t *testing.T) {
	t.Parallel()
	const n = 10
	sky, edge, obst := hs.ClassSky, hs.ClassEdge, hs.ClassObst
	cases := []struct {
		name   string
		seen   map[int]string
		coarse bool
		next   int
		done   bool
	}{
		{"first column descends in big steps through sky", map[int]string{9: sky}, true, 6, false},
		{"later columns descend two rows at a time", map[int]string{5: sky}, false, 3, false},
		{"sky down to the lowest row ends the column", map[int]string{1: sky, 0: sky}, false, 0, true},
		{"after a jump into the obstruction, fill upward", map[int]string{9: sky, 6: obst}, true, 7, false},
		{"obstructed start walks up", map[int]string{4: obst}, false, 5, false},
		{"obstruction under sky ends the column", map[int]string{5: obst, 6: sky}, false, 0, true},
		{"edge frame takes one frame below", map[int]string{5: edge, 6: sky}, false, 4, false},
		{"edge then obstruction ends the column", map[int]string{5: edge, 6: sky, 4: obst}, false, 0, true},
		{"edge then edge takes a second frame below", map[int]string{5: edge, 6: sky, 4: edge}, false, 3, false},
		{"two frames below the edge is enough", map[int]string{5: edge, 6: sky, 4: edge, 3: edge}, false, 0, true},
		{"obstructed to the top row ends the column", map[int]string{9: obst}, false, 0, true},
	}
	for _, c := range cases {
		next, done := hs.NextRow(c.seen, n, c.coarse)
		if done != c.done || (!done && next != c.next) {
			t.Fatalf("%s: got row %d done %v, want row %d done %v", c.name, next, done, c.next, c.done)
		}
	}
}
