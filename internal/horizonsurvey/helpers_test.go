package horizonsurvey_test

import (
	"time"

	hs "github.com/USA-RedDragon/astro-stacker/internal/horizonsurvey"
	"github.com/USA-RedDragon/astro-stacker/internal/horizonsurvey/sim"
)

func testSite() hs.Site { return hs.Site{Lat: 35, Lon: -110} }

func duskTime() time.Time { return time.Date(2026, 10, 13, 1, 9, 0, 0, time.UTC) }

func nightTime() time.Time { return time.Date(2026, 10, 13, 3, 14, 0, 0, time.UTC) }

func deepNight() time.Time { return time.Date(2026, 10, 13, 5, 44, 0, 0, time.UTC) }

func smallCamera(c sim.Camera) sim.Camera {
	c.W, c.H, c.Scale = 240, 135, 55
	return c
}

func checkSkyline(sl *hs.Skyline, cam sim.Camera, sky sim.Skyline) (worst float64, edges int, worstAz float64) {
	for _, b := range sl.Bins {
		if !b.Measured || b.Kind != hs.BinEdge {
			continue
		}
		edges++
		if e := sky.Error(b.Alt, b.Az, cam.AzResolution()); e > worst {
			worst, worstAz = e, b.Az
		}
	}
	return worst, edges, worstAz
}
