package coverage_test

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/calmatch"
	"github.com/USA-RedDragon/astro-stacker/internal/coverage"
)

// Offset-240 lights have no raw bias or darks; WBPP's masters stand in.
func TestImportedCalibrateOffset240(t *testing.T) {
	t.Parallel()
	night := time.Date(2025, 2, 4, 0, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		gain, exposure, temp float64
		dark                 string
	}{
		{100, 600, -10, "600.00s"},
		{0, 300, -15, "300.00s"},
		{0, 120, -20, "300.00s"},
	} {
		r := calmatch.Choose(calmatch.Group{Night: night, Filter: "H-a", Exposure: c.exposure, Gain: c.gain,
			Offset: 240, SetTemp: c.temp, BinX: 1, Rotator: math.NaN()}, coverage.Imported)
		if r.Bias.Set == nil || r.Bias.Set.Master == "" {
			t.Errorf("gain %v: no bias", c.gain)
		}
		if r.Dark.Set == nil || !contains(r.Dark.Set.Master, c.dark) {
			t.Errorf("gain %v %vs: dark %+v, want the %s one", c.gain, c.exposure, r.Dark.Set, c.dark)
		}
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
