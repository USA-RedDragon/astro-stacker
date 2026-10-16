package coverage

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/calmatch"
	"github.com/USA-RedDragon/astro-stacker/internal/darkcheck"
)

func TestBacklogKeepsLightsOnAShortSetUntilAFullOneMatches(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 10, 7, 0, 0, 0, time.UTC)
	night := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	f := func(v float64) *float64 { return &v }
	clean := darkcheck.StateClean
	unchecked := darkcheck.StateUnchecked
	darkSet := func(temp float64, n, cleanN int, start time.Time) ([]darkFrame, calmatch.Set) {
		out := make([]darkFrame, 0, n)
		for i := range n {
			at := start.Add(time.Duration(i) * time.Minute)
			state := &clean
			if i >= cleanN {
				state = &unchecked
			}
			out = append(out, darkFrame{Key: fmt.Sprintf("DARK/%v_%d", temp, i), Exposure: f(300), Gain: f(0), Offset: f(50),
				SetTemp: f(temp), BinX: f(1), DateObs: &at, Night: &night, CalCheck: state})
		}
		s := calmatch.Set{Type: darkType, Night: night, Exposure: 300, Gain: 0, Offset: 50, SetTemp: temp, BinX: 1,
			Rotator: math.NaN(), Count: n, From: start, To: start.Add(time.Duration(n-1) * time.Minute)}
		return out, s
	}
	lights := []darkLightGroup{{Night: night, Exposure: f(300), Gain: f(0), Offset: f(50), SetTemp: f(-10), BinX: f(1), N: 7}}
	thin := func(darks []darkFrame, sets []calmatch.Set) int {
		b := darkBacklog(lights, darks, sets, now)
		n := 0
		for _, c := range b.Combos {
			n += c.LightsThin
		}
		return n
	}
	shortDarks, short := darkSet(-10, 9, 9, night)
	if got := thin(shortDarks, []calmatch.Set{short}); got != 7 {
		t.Errorf("only a short set: %d lights on a short set, want 7", got)
	}
	fullDarks, full := darkSet(-11, 10, 10, night.Add(48*time.Hour))
	if got := thin(append(shortDarks, fullDarks...), []calmatch.Set{short, full}); got != 0 {
		t.Errorf("a full set 1 °C off: %d lights still on the backlog, want 0", got)
	}
	keptDarks, kept := darkSet(-11, 25, 9, night.Add(48*time.Hour))
	if got := thin(append(shortDarks, keptDarks...), []calmatch.Set{short, kept}); got != 7 {
		t.Errorf("a set of 25 with 9 clean: %d lights on a short set, want 7", got)
	}
}

func TestBacklogCombosListTheirTargetsMostLightsFirst(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 10, 7, 0, 0, 0, time.UTC)
	night := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	f := func(v float64) *float64 { return &v }
	light := func(object string, temp float64, n int) darkLightGroup {
		return darkLightGroup{Night: night, Object: object, Exposure: f(300), Gain: f(0), Offset: f(50), SetTemp: f(temp), BinX: f(1), N: n}
	}
	lights := []darkLightGroup{
		light("M 31", -10, 4), light("IC 1396", -10, 6), light("M 31", -10.5, 3),
		light("NGC 7000", -10, 7), light("Abell 85", -10.5, 7), light("", -10, 2),
		light("M 33", -20, 5),
	}
	b := darkBacklog(lights, nil, nil, now)
	got := map[float64][]DarkTarget{}
	for _, c := range b.Combos {
		got[c.SetTemp] = c.Targets
	}
	want := map[float64][]DarkTarget{
		-10: {{Name: "Abell 85", Lights: 7}, {Name: "M 31", Lights: 7}, {Name: "NGC 7000", Lights: 7}, {Name: "IC 1396", Lights: 6}},
		-20: {{Name: "M 33", Lights: 5}},
	}
	if len(got) != len(want) {
		t.Fatalf("combos at %v, want set temps -10 and -20", got)
	}
	for temp, w := range want {
		g := got[temp]
		if fmt.Sprint(g) != fmt.Sprint(w) {
			t.Errorf("set temp %v: targets %v, want %v", temp, g, w)
		}
	}
}

func TestBacklogTenCleanDarksCompleteASetFrom120s(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 10, 7, 0, 0, 0, time.UTC)
	night := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	f := func(v float64) *float64 { return &v }
	clean := darkcheck.StateClean
	for _, c := range []struct {
		exposure  float64
		cleanN    int
		wantThin  int
		wantCombo int
	}{{119, 10, 7, 25}, {119, 24, 7, 25}, {119, 25, 0, 0}, {120, 9, 7, 10}, {120, 10, 0, 0}, {300, 10, 0, 0}} {
		darks := make([]darkFrame, 0, c.cleanN)
		for i := range c.cleanN {
			at := night.Add(time.Duration(i) * time.Minute)
			darks = append(darks, darkFrame{Key: fmt.Sprintf("DARK/%d", i), Exposure: f(c.exposure), Gain: f(0), Offset: f(50),
				SetTemp: f(-10), BinX: f(1), DateObs: &at, Night: &night, CalCheck: &clean})
		}
		set := calmatch.Set{Type: darkType, Night: night, Exposure: c.exposure, Gain: 0, Offset: 50, SetTemp: -10, BinX: 1,
			Rotator: math.NaN(), Count: c.cleanN, From: night, To: night.Add(time.Duration(c.cleanN-1) * time.Minute)}
		lights := []darkLightGroup{{Night: night, Exposure: f(c.exposure), Gain: f(0), Offset: f(50), SetTemp: f(-10), BinX: f(1), N: 7}}
		b := darkBacklog(lights, darks, []calmatch.Set{set}, now)
		thin, need := 0, 0
		for _, combo := range b.Combos {
			thin += combo.LightsThin
			need = combo.FramesNeeded
		}
		if thin != c.wantThin || need != c.wantCombo {
			t.Errorf("%v s with %d clean darks: %d lights on a short set needing %d, want %d needing %d", c.exposure, c.cleanN, thin, need, c.wantThin, c.wantCombo)
		}
		if b.FramesNeeded != 25 || b.FramesNeededLong != 10 || b.LongExposureS != 120 {
			t.Errorf("backlog rule fields %d %d %v", b.FramesNeeded, b.FramesNeededLong, b.LongExposureS)
		}
	}
}
