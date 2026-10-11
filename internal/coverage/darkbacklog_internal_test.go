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
	shortDarks, short := darkSet(-10, 14, 14, night)
	if got := thin(shortDarks, []calmatch.Set{short}); got != 7 {
		t.Errorf("only a short set: %d lights on a short set, want 7", got)
	}
	fullDarks, full := darkSet(-11, 25, 25, night.Add(48*time.Hour))
	if got := thin(append(shortDarks, fullDarks...), []calmatch.Set{short, full}); got != 0 {
		t.Errorf("a full set 1 °C off: %d lights still on the backlog, want 0", got)
	}
	keptDarks, kept := darkSet(-11, 25, 20, night.Add(48*time.Hour))
	if got := thin(append(shortDarks, keptDarks...), []calmatch.Set{short, kept}); got != 7 {
		t.Errorf("a set of 25 with 20 clean: %d lights on a short set, want 7", got)
	}
}
