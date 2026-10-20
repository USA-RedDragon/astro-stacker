package ownimage

import (
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
)

// night2217 is source 2217's Luminance night of 2026-10-12 as its star:19
// report charts it: hours from the predicted minimum and mag fainter.
var night2217 = [][2]float64{
	{-1.1164, 0.3075}, {-1.0273, 0.1298}, {-0.9382, 0.4885}, {-0.849, 0.2811}, {-0.7642, 0.3393}, {-0.6794, 0.4312},
	{-0.5898, 0.4158}, {-0.5013, 0.46}, {-0.4165, 0.6995}, {-0.3258, 0.5043}, {-0.241, 0.5771}, {-0.1514, 0.7678},
	{-0.0665, 0.5427}, {0.0237, 0.9791}, {0.1086, 0.842}, {0.1983, 0.2828}, {0.2831, 0.821}, {0.3732, 0.7593},
}

func case2217(change, sigma float64) (*spotPlan, *Curve) {
	d := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	rep := 0.12
	sp := &spotPlan{
		spot:     Spot{Nights: []Night{{Night: d, Group: discoveries.GroupOf("Luminance"), Sign: -1, DeltaMag: &rep}}},
		flagged:  map[string]bool{nightKey(d): true},
		fadeSign: -1,
	}
	c := &Curve{Comps: 12, Scatter: 0.192, ChangeNight: nightKey(d), Change: change, ChangeSigma: sigma, ChangeLevels: 1}
	for i, p := range night2217 {
		c.Points = append(c.Points, Point{FrameID: i, BJD: 2461326 + p[0]/24, Night: nightKey(d), Filter: "Luminance", DMag: p[1] + change - 0.53, Err: 0.1918})
	}
	return sp, c
}

// TestOwnWords2217 checks the own light curve and change rows agree on
// source 2217: its night shows the dip 0.82 mag deep against single
// frames scattering by 0.19 mag, and averages 0.53 +- 0.11 mag fainter,
// 4.8 times its error, against 0.12 reported. The light curve row said
// "no clear change", and the change row named only the reported 0.12 mag
// as if the two disagreed. The change row must still fail, now for being
// short of 5 times its error; a night far shallower than reported, or
// brighter where a fade was reported, must say that instead.
func TestOwnWords2217(t *testing.T) {
	t.Parallel()
	sp, c := case2217(0.53, 0.53/0.11)
	lc := ownLC(sp, c)
	if lc.Status != check.Pass || strings.Contains(lc.Evidence, "no clear change") ||
		!strings.Contains(lc.Evidence, "The dip bottomed out 0.82 mag deep") || !strings.Contains(lc.Evidence, "not clear of how much the star's level wanders") {
		t.Errorf("own light curve %s: %s", lc.Status, lc.Evidence)
	}
	ch := ownChange(sp, c)
	if ch.Status != check.Fail || !strings.Contains(ch.Evidence, "That is 4.8 times its error") || strings.Contains(ch.Evidence, "less than") {
		t.Errorf("own change %s: %s", ch.Status, ch.Evidence)
	}

	sp, c = case2217(0.02, 6)
	if ch := ownChange(sp, c); ch.Status != check.Fail || !strings.Contains(ch.Evidence, "less than 30% of the reported change") {
		t.Errorf("shallow night: own change %s: %s", ch.Status, ch.Evidence)
	}
	if lc := ownLC(sp, c); !strings.Contains(lc.Evidence, "No dip or peak within the night stands out") {
		t.Errorf("shallow night: own light curve %s", lc.Evidence)
	}

	sp, c = case2217(-0.53, -6)
	if ch := ownChange(sp, c); ch.Status != check.Fail || !strings.Contains(ch.Evidence, "the other way from the reported change") {
		t.Errorf("brighter night: own change %s: %s", ch.Status, ch.Evidence)
	}

	sp, c = case2217(0.53, 6)
	if ch := ownChange(sp, c); ch.Status != check.Pass {
		t.Errorf("clear night: own change %s: %s", ch.Status, ch.Evidence)
	}
	if lc := ownLC(sp, c); strings.Contains(lc.Evidence, "not clear") {
		t.Errorf("clear night: own light curve %s", lc.Evidence)
	}
}
