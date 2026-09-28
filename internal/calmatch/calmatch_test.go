package calmatch_test

import (
	"math"
	"testing"
	"time"

	"github.com/USA-RedDragon/pixinsight-worker/internal/calmatch"
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

func TestDarkPrefersClosestSetpointThenNewest(t *testing.T) {
	t.Parallel()
	sets := []calmatch.Set{
		{Type: "DARK", Night: night("2025-02-06"), Exposure: 600, Gain: 0, Offset: 50, BinX: 1, SetTemp: 0},
		{Type: "DARK", Night: night("2025-12-01"), Exposure: 600, Gain: 0, Offset: 50, BinX: 1, SetTemp: -10},
		{Type: "DARK", Night: night("2025-03-01"), Exposure: 600, Gain: 0, Offset: 50, BinX: 1, SetTemp: -10},
		{Type: "DARK", Night: night("2025-11-15"), Exposure: 300, Gain: 0, Offset: 50, BinX: 1, SetTemp: -10},
		{Type: "DARK", Night: night("2025-11-15"), Exposure: 600, Gain: 100, Offset: 50, BinX: 1, SetTemp: -10},
	}
	m := calmatch.Choose(lights, sets).Dark
	if m.Quality != calmatch.Exact || !m.Set.Night.Equal(night("2025-12-01")) || m.TempOff != 0 {
		t.Fatalf("got %+v", m)
	}

	// Only the 0 °C library is left: 10 °C away is too far to use.
	if m := calmatch.Choose(lights, sets[:1]).Dark; m.Quality != calmatch.Missing {
		t.Fatalf("10°C off should be missing, got %+v", m)
	}

	g := lights
	g.SetTemp = -3
	if m := calmatch.Choose(g, sets[:1]).Dark; m.Quality != calmatch.Fallback || m.TempOff != 3 {
		t.Fatalf("3°C off should be fallback, got %+v", m)
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
