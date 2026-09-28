package calmatch_test

import (
	"math"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/calmatch"
)

func night(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

var lights = calmatch.Group{
	Night: night("2025-11-15"), Filter: "Red", Exposure: 600, Gain: 0, Offset: 50,
	SetTemp: -10, BinX: 1, Rotator: 142.4,
}

func TestFlatPrefersSameNightThenNearest(t *testing.T) {
	t.Parallel()
	sets := []calmatch.Set{
		{Type: "FLAT", Night: night("2025-05-13"), Filter: "Red", Gain: 0, Offset: 50, BinX: 1, Rotator: 142.4},
		{Type: "FLAT", Night: night("2025-11-15"), Filter: "Red", Gain: 0, Offset: 50, BinX: 1, Rotator: 142.5},
		{Type: "FLAT", Night: night("2025-11-15"), Filter: "Green", Gain: 0, Offset: 50, BinX: 1, Rotator: 142.4},
	}
	m := calmatch.Choose(lights, sets).Flat
	if m.Quality != calmatch.Exact || m.Set.Filter != "Red" || m.AgeDays != 0 {
		t.Fatalf("got %+v", m)
	}

	m = calmatch.Choose(lights, sets[:1]).Flat
	if m.Quality != calmatch.Fallback || m.AgeDays != 186 {
		t.Fatalf("fallback got %+v (age %d)", m.Quality, m.AgeDays)
	}
}

func TestFlatRejectsGainAndFilterMismatch(t *testing.T) {
	t.Parallel()
	sets := []calmatch.Set{
		{Type: "FLAT", Night: night("2025-11-15"), Filter: "Red", Gain: 100, Offset: 50, BinX: 1, Rotator: 142.4},
		{Type: "FLAT", Night: night("2025-11-15"), Filter: "Blue", Gain: 0, Offset: 50, BinX: 1, Rotator: 142.4},
	}
	if m := calmatch.Choose(lights, sets).Flat; m.Quality != calmatch.Missing {
		t.Fatalf("got %+v", m)
	}
}

func TestFlatAtOtherRotationIsFallbackAndLosesTies(t *testing.T) {
	t.Parallel()
	rotated := calmatch.Set{Type: "FLAT", Night: night("2025-11-15"), Filter: "Red", Gain: 0, Offset: 50, BinX: 1, Rotator: 150}
	m := calmatch.Choose(lights, []calmatch.Set{rotated}).Flat
	if m.Quality != calmatch.Fallback || !m.RotationMismatch || m.AgeDays != 0 {
		t.Fatalf("got %+v", m)
	}
	aligned := rotated
	aligned.Rotator = 142.4
	m = calmatch.Choose(lights, []calmatch.Set{rotated, aligned}).Flat
	if m.Quality != calmatch.Exact || m.RotationMismatch {
		t.Fatalf("aligned flat should win the tie, got %+v", m)
	}
}

func TestFlatRotationWrapsAndUnknownRotatorIsCompatible(t *testing.T) {
	t.Parallel()
	g := lights
	g.Rotator = 359.6
	sets := []calmatch.Set{{Type: "FLAT", Night: g.Night, Filter: "Red", Gain: 0, Offset: 50, BinX: 1, Rotator: 0.2}}
	if m := calmatch.Choose(g, sets).Flat; m.Quality != calmatch.Exact {
		t.Fatalf("wrapped rotation: %+v", m)
	}
	sets[0].Rotator = math.NaN()
	if m := calmatch.Choose(lights, sets).Flat; m.Quality != calmatch.Exact {
		t.Fatalf("unknown rotator: %+v", m)
	}
}

func TestDarkPrefersClosestSetpointThenSameExposureThenLongest(t *testing.T) {
	t.Parallel()
	sets := []calmatch.Set{
		{Type: "DARK", Night: night("2025-02-06"), Exposure: 600, Gain: 0, Offset: 50, BinX: 1, SetTemp: 0},
		{Type: "DARK", Night: night("2025-12-01"), Exposure: 600, Gain: 0, Offset: 50, BinX: 1, SetTemp: -10},
		{Type: "DARK", Night: night("2025-03-01"), Exposure: 600, Gain: 0, Offset: 50, BinX: 1, SetTemp: -10},
		{Type: "DARK", Night: night("2025-11-15"), Exposure: 300, Gain: 0, Offset: 50, BinX: 1, SetTemp: -10},
		{Type: "DARK", Night: night("2025-11-15"), Exposure: 600, Gain: 100, Offset: 50, BinX: 1, SetTemp: -10},
	}
	m := calmatch.Choose(lights, sets).Dark
	if m.Quality != calmatch.Exact || m.Scaled || !m.Set.Night.Equal(night("2025-12-01")) {
		t.Fatalf("same setpoint and exposure, newest: got %+v", m)
	}

	// 10 °C away is still usable with scaling.
	m = calmatch.Choose(lights, sets[:1]).Dark
	if m.Quality != calmatch.Fallback || !m.Scaled || m.TempOff != 10 {
		t.Fatalf("10 °C off should be scaled fallback, got %+v", m)
	}

	// A 300 s light with only 600 s and 120 s darks at its setpoint takes the
	// longer one and scales it.
	g := lights
	g.Exposure = 300
	short := calmatch.Set{Type: "DARK", Night: night("2025-12-01"), Exposure: 120, Gain: 0, Offset: 50, BinX: 1, SetTemp: -10}
	m = calmatch.Choose(g, []calmatch.Set{short, sets[1]}).Dark
	if m.Quality != calmatch.Fallback || !m.Scaled || m.Set.Exposure != 600 {
		t.Fatalf("got %+v", m)
	}
}

func TestDarkBeyondScaleRangeOrOtherGainIsMissing(t *testing.T) {
	t.Parallel()
	g := lights
	g.SetTemp = -25
	sets := []calmatch.Set{
		{Type: "DARK", Night: night("2025-02-06"), Exposure: 600, Gain: 0, Offset: 50, BinX: 1, SetTemp: 0},
		{Type: "DARK", Night: night("2025-02-06"), Exposure: 600, Gain: 100, Offset: 50, BinX: 1, SetTemp: -25},
	}
	if m := calmatch.Choose(g, sets).Dark; m.Quality != calmatch.Missing {
		t.Fatalf("got %+v", m)
	}
}

func TestBiasIgnoresTemperatureAndExposure(t *testing.T) {
	t.Parallel()
	sets := []calmatch.Set{
		{Type: "BIAS", Night: night("2025-02-05"), Gain: 0, Offset: 50, BinX: 1, SetTemp: 0, Exposure: 0.000032},
		{Type: "BIAS", Night: night("2025-02-05"), Gain: 100, Offset: 50, BinX: 1, SetTemp: 0},
	}
	m := calmatch.Choose(lights, sets).Bias
	if m.Quality != calmatch.Fallback || m.Set.Gain != 0 {
		t.Fatalf("got %+v", m)
	}
}
