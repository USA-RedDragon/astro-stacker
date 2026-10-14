package calmatch_test

import (
	"math"
	"strings"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/calmatch"
)

func seededEpochs() calmatch.Epochs {
	return calmatch.Epochs{
		{Start: night("2026-05-28")},
		{Start: night("2026-04-16"), Fallback: calmatch.FallbackPrevious},
	}
}

func redFlat(n string, rot float64) calmatch.Set {
	return calmatch.Set{Type: typeFlat, Night: night(n), Filter: filterRed, Gain: 0, Offset: 50, BinX: 1, Rotator: rot}
}

// The flat sets of Red as they were on 2026-10-14.
func realRedFlats() []calmatch.Set {
	return []calmatch.Set{
		redFlat("2025-02-05", 133), redFlat("2025-05-13", 132),
		redFlat("2026-09-28", 43), redFlat("2026-09-28", 142), redFlat("2026-09-28", 141),
		redFlat("2026-10-05", 141), redFlat("2026-10-05", 142), redFlat("2026-10-06", 2), redFlat("2026-10-06", 142),
		redFlat("2026-10-07", 143), redFlat("2026-10-07", 43), redFlat("2026-10-07", 142), redFlat("2026-10-08", 43),
		redFlat("2026-10-09", 42), redFlat("2026-10-09", 142), redFlat("2026-10-10", 142), redFlat("2026-10-10", 141),
		redFlat("2026-10-11", 141), redFlat("2026-10-11", 142), redFlat("2026-10-12", 141), redFlat("2026-10-12", 136),
		redFlat("2026-10-13", 136), redFlat("2026-10-13", 142),
	}
}

func redLights(n string, rot float64) calmatch.Group {
	return calmatch.Group{Night: night(n), Filter: filterRed, Exposure: 300, Gain: 0, Offset: 50, SetTemp: -10, BinX: 1,
		Rotator: rot, Epochs: seededEpochs()}
}

func TestEpochOfCountsBoundariesOnOrBefore(t *testing.T) {
	t.Parallel()
	e := seededEpochs()
	for n, want := range map[string]int{"2025-11-15": 0, "2026-04-15": 0, "2026-04-16": 1, "2026-05-27": 1, "2026-05-28": 2, "2026-10-13": 2} {
		if got := e.Of(night(n)); got != want {
			t.Errorf("Of(%s) = %d, want %d", n, got, want)
		}
	}
	if got := calmatch.Epochs(nil).Of(night("2026-05-01")); got != 0 {
		t.Fatalf("no epochs: got %d", got)
	}
}

// Lights from before the dust came get the old flats, however far in the
// past, over a newer flat at their own angle from across the boundary.
func TestFlatNeverCrossesADustBoundary(t *testing.T) {
	t.Parallel()
	m := calmatch.Choose(redLights("2026-03-20", 141.6), realRedFlats()).Flat
	if m.Set == nil || m.Set.Night.Year() != 2025 || m.OtherEpoch {
		t.Fatalf("got %+v, want a 2025 flat from the lights' own epoch", m)
	}
	if m.Set.Rotator != 133 || !m.RotationMismatch || m.RotationOff < 8 {
		t.Fatalf("got rotator %v, want the nearest angle (133) flagged as a mismatch", m.Set.Rotator)
	}
	m = calmatch.Choose(redLights("2026-06-10", 141), realRedFlats()).Flat
	if m.Set == nil || m.Set.Night != night("2026-09-28") || m.RotationMismatch {
		t.Fatalf("got %+v, want the nearest 2026 flat within 1.5°", m.Set)
	}
}

// Markarian Chain Panel 9's Red subs of 2026-04-24 to 05-03 were calibrated
// with the 2026-09-28 flat and showed a bright disc A at +0.45 %. Their epoch
// has no flat, so they get the earlier epoch's.
func TestMarkarian9AprilRedNoLongerGetsTheSeptemberFlat(t *testing.T) {
	t.Parallel()
	for _, n := range []string{"2026-04-24", "2026-05-03"} {
		m := calmatch.Choose(redLights(n, 141.6), realRedFlats()).Flat
		if m.Set == nil || m.Set.Night.Equal(night("2026-09-28")) || m.Set.Night.Year() != 2025 {
			t.Fatalf("%s: got %+v", n, m.Set)
		}
		if !m.OtherEpoch || m.FallbackWhy != calmatch.WhyEpochPrevious || m.Quality != calmatch.Fallback {
			t.Fatalf("%s: got %+v, want a flagged fallback from the earlier epoch", n, m)
		}
		if note := m.FlatNote(); !strings.Contains(note, "other dust epoch") {
			t.Fatalf("%s: note %q", n, note)
		}
	}
	synth := redFlat("2026-04-30", math.NaN())
	synth.Master = "dustfix/offset50/masterFlat_BIN-1_6248x4176_FILTER-Red_mono.xisf"
	m := calmatch.Choose(redLights("2026-04-24", 141.6), append(realRedFlats(), synth)).Flat
	if m.Set == nil || m.Set.Master != synth.Master || m.OtherEpoch {
		t.Fatalf("got %+v, want the epoch's own synthesized flat", m.Set)
	}
}

func TestEpochFallbackPolicies(t *testing.T) {
	t.Parallel()
	sets := []calmatch.Set{redFlat("2026-01-10", 142), redFlat("2026-09-28", 142)}
	g := redLights("2026-05-20", 142)
	g.Epochs = calmatch.Epochs{{Start: night("2026-04-16"), Fallback: calmatch.FallbackNext}, {Start: night("2026-05-28")}}
	if m := calmatch.Choose(g, sets).Flat; m.Set.Night != night("2026-09-28") || m.FallbackWhy != calmatch.WhyEpochNext {
		t.Fatalf("next: got %+v", m)
	}
	g.Epochs[0].Fallback = ""
	if m := calmatch.Choose(g, sets).Flat; m.Set.Night != night("2026-01-10") || m.FallbackWhy != calmatch.WhyEpochNearest {
		t.Fatalf("nearest: got %+v", m)
	}
	g.Epochs[0].Fallback = calmatch.FallbackPrevious
	if m := calmatch.Choose(g, sets[1:]).Flat; m.Set == nil || m.FallbackWhy != calmatch.WhyEpochNearest {
		t.Fatalf("previous with no earlier flat falls back to the nearest: got %+v", m)
	}
}

// Within an epoch a flat within 1.5° wins over a nearer night at another
// angle, and among flats at other angles the nearest angle wins.
func TestFlatPrefersSameAngleThenNearestAngleWithinEpoch(t *testing.T) {
	t.Parallel()
	g := redLights("2026-10-12", 142.3)
	sets := []calmatch.Set{redFlat("2026-10-12", 136), redFlat("2026-09-28", 141), redFlat("2026-10-11", 43)}
	m := calmatch.Choose(g, sets).Flat
	if m.Set.Night != night("2026-09-28") || m.RotationMismatch {
		t.Fatalf("got %+v, want the 141° flat", m.Set)
	}
	m = calmatch.Choose(g, []calmatch.Set{redFlat("2026-10-12", 43), redFlat("2026-10-05", 136)}).Flat
	if m.Set.Rotator != 136 || !m.RotationMismatch {
		t.Fatalf("got %+v, want the nearest angle", m.Set)
	}
	if note := m.FlatNote(); !strings.Contains(note, "rotator 6.3° off") || !strings.Contains(note, "7 nights apart") {
		t.Fatalf("note %q", note)
	}
	g.Rotator = 142
	exact := calmatch.Choose(g, []calmatch.Set{redFlat("2026-10-12", 143)}).Flat
	if exact.Quality != calmatch.Exact || exact.FlatNote() != "exact" {
		t.Fatalf("1° off on the same night should be exact, got %+v", exact)
	}
}
