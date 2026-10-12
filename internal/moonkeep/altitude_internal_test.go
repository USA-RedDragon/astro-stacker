package moonkeep

import (
	"fmt"
	"testing"
	"time"
)

type altBin struct {
	alt          float64
	subs, kept   int
	nights       int
	narrowband   bool
	moonAltitude float64
}

func altSamples(bins ...altBin) []Sample {
	var out []Sample
	start := time.Date(2026, 1, 1, 4, 0, 0, 0, time.UTC)
	for _, b := range bins {
		for i := range b.subs {
			night := i % b.nights
			out = append(out, Sample{Night: fmt.Sprintf("2026-01-%02d", night+1), Narrowband: b.narrowband, Kept: i < b.kept,
				Exposure: 300, Start: start.AddDate(0, 0, night), Altitude: b.alt, Moon: State{Altitude: b.moonAltitude - 1}})
		}
	}
	return out
}

func high(nb bool) altBin { return altBin{alt: 60, subs: 400, kept: 340, nights: 20, narrowband: nb} }

func floorFor(t *testing.T, fl []AltitudeFloor, nb bool) AltitudeFloor {
	t.Helper()
	f := AltitudeFloorFor(fl, nb)
	if len(f.Rows) != len(AltitudeEdges())-1 {
		t.Fatalf("%d rows", len(f.Rows))
	}
	return f
}

func TestAltitudeFloorStopsAtALowKeepingBin(t *testing.T) {
	t.Parallel()
	fl := FitAltitude(altSamples(high(false),
		altBin{alt: 37, subs: 200, kept: 160, nights: 20}, altBin{alt: 32, subs: 200, kept: 150, nights: 20},
		altBin{alt: 27, subs: 200, kept: 140, nights: 20}, altBin{alt: 22, subs: 200, kept: 110, nights: 20},
		altBin{alt: 17, subs: 200, kept: 40, nights: 20}, altBin{alt: 12, subs: 200, kept: 160, nights: 20}))
	f := floorFor(t, fl, false)
	if f.Floor != 20 || f.Basis != FloorKeptRate {
		t.Fatalf("broadband floor %g (%s): %s", f.Floor, f.Basis, f.Reason)
	}
	if !f.Rows[3].Below || f.Rows[4].Below {
		t.Errorf("15-20 is below the floor and 20-25 is not: %+v %+v", f.Rows[3], f.Rows[4])
	}
	if f.Reference != 0.85 {
		t.Errorf("reference %v", f.Reference)
	}
}

func TestAltitudeFloorFallsBackWhereHistoryIsThin(t *testing.T) {
	t.Parallel()
	fl := FitAltitude(altSamples(high(true),
		altBin{alt: 37, subs: 60, kept: 50, nights: 10, narrowband: true}, altBin{alt: 32, subs: 60, kept: 48, nights: 10, narrowband: true},
		altBin{alt: 27, subs: 60, kept: 45, nights: 10, narrowband: true}, altBin{alt: 22, subs: 60, kept: 40, nights: 10, narrowband: true},
		altBin{alt: 17, subs: 60, kept: 38, nights: 10, narrowband: true}, altBin{alt: 12, subs: 60, kept: 36, nights: 10, narrowband: true},
		altBin{alt: 7, subs: 10, kept: 0, nights: 2, narrowband: true}))
	if f := floorFor(t, fl, true); f.Floor != 10 || f.Basis != FloorThin {
		t.Errorf("kept enough down to 10°, thin below: %g (%s)", f.Floor, f.Basis)
	}
	fl = FitAltitude(altSamples(high(true), altBin{alt: 37, subs: 60, kept: 50, nights: 10, narrowband: true}, altBin{alt: 32, subs: 5, kept: 5, nights: 2, narrowband: true}))
	if f := floorFor(t, fl, true); f.Floor != AltitudeFallback || f.Basis != FloorThin {
		t.Errorf("thin below 35° falls back to %g: %g (%s)", AltitudeFallback, f.Floor, f.Basis)
	}
	if f := floorFor(t, fl, false); f.Floor != AltitudeFallback || f.Basis != FloorNoData {
		t.Errorf("no broadband subs: %g (%s)", f.Floor, f.Basis)
	}
}

func TestAltitudeFloorUsesMoonDownSubsOnly(t *testing.T) {
	t.Parallel()
	moonUp := altBin{alt: 17, subs: 300, kept: 0, nights: 20, moonAltitude: 30}
	fl := FitAltitude(altSamples(high(false), altBin{alt: 37, subs: 200, kept: 160, nights: 20}, altBin{alt: 32, subs: 200, kept: 160, nights: 20},
		altBin{alt: 27, subs: 200, kept: 160, nights: 20}, altBin{alt: 22, subs: 200, kept: 160, nights: 20}, moonUp))
	if f := floorFor(t, fl, false); f.Rows[3].Subs != 0 || f.Floor != AltitudeFallback {
		t.Errorf("Moon-up subs count: %+v, floor %g", f.Rows[3], f.Floor)
	}
}

func TestAltitudeReplayCountsSubsUnderEachFloor(t *testing.T) {
	t.Parallel()
	s := altSamples(high(false), altBin{alt: 37, subs: 200, kept: 160, nights: 20}, altBin{alt: 32, subs: 200, kept: 150, nights: 20},
		altBin{alt: 27, subs: 200, kept: 140, nights: 20}, altBin{alt: 22, subs: 200, kept: 110, nights: 20},
		altBin{alt: 17, subs: 200, kept: 40, nights: 20}, altBin{alt: 12, subs: 4, kept: 1, nights: 2})
	rows := AltitudeReplay(s, []float64{0.1, 0.5})
	if len(rows) != 2 || rows[0].BroadbandFloor != AltitudeFallback || rows[1].BroadbandFloor != 20 {
		t.Fatalf("%+v", rows)
	}
	if rows[1].SubsBelow != 204 || rows[1].KeptBelow != 41 || rows[1].HoursBelow != 17 {
		t.Errorf("under 20°: %+v", rows[1])
	}
	if rows[0].SubsBelow != 4 || rows[0].KeptBelow != 1 {
		t.Errorf("under 15°: %+v", rows[0])
	}
}

func TestSamplesCarryTheTargetAltitude(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 3, 1, 6, 0, 0, 0, time.UTC)
	s := Samples([]Input{{Object: "Polaris", Filter: "L", Exposure: 300, Start: at, Status: "added", RA: 37.95, Dec: 89.26, HasPos: true}}, 35.0, -110.0)
	if len(s) != 1 || s[0].Altitude < 30.7 || s[0].Altitude > 32.3 {
		t.Fatalf("Polaris stands at the latitude: %+v", s)
	}
}

func TestAltitudeFloorWithNothingKeptHighIsNoData(t *testing.T) {
	t.Parallel()
	fl := FitAltitude(altSamples(altBin{alt: 60, subs: 100, kept: 0, nights: 10}, altBin{alt: 17, subs: 100, kept: 0, nights: 10}))
	if f := AltitudeFloorFor(fl, false); f.Floor != AltitudeFallback || f.Basis != FloorNoData {
		t.Errorf("%g %s", f.Floor, f.Basis)
	}
}
