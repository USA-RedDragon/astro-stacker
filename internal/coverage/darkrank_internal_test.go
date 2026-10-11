package coverage

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/calmatch"
)

const oldCombo = "300|0|240|1|-15"

func rankLight(night time.Time, object string, exposure, offset, temp float64, n int) darkLightGroup {
	f := func(v float64) *float64 { return &v }
	return darkLightGroup{Night: night, Object: object, Exposure: f(exposure), Gain: f(0), Offset: f(offset), SetTemp: f(temp), BinX: f(1), N: n}
}

func dawn20261011() (time.Time, []darkLightGroup, []calmatch.Set) {
	now := time.Date(2026, 10, 11, 11, 11, 53, 0, time.UTC)
	tonight := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	old := time.Date(2025, 1, 31, 0, 0, 0, 0, time.UTC)
	lights := []darkLightGroup{
		rankLight(old, "Rosette Nebula", 300, 240, -15, 213),
		rankLight(tonight, "Caldwell 14", 300, 50, -4, 40),
		rankLight(tonight, "Eagle Nebula Panel 2", 600, 50, 0, 6),
	}
	setAt := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	sets := []calmatch.Set{
		{Type: darkType, Night: setAt, Exposure: 300, Gain: 0, Offset: 50, SetTemp: -10, BinX: 1, Rotator: math.NaN(), Count: 25, From: setAt, To: setAt},
		{Type: darkType, Night: setAt, Exposure: 600, Gain: 0, Offset: 50, SetTemp: -5, BinX: 1, Rotator: math.NaN(), Count: 25, From: setAt, To: setAt},
	}
	return now, lights, sets
}

func TestRecentScaledLightsOutrankOldLightsWithNoDark(t *testing.T) {
	t.Parallel()
	now, lights, sets := dawn20261011()
	b := darkBacklog(lights, nil, sets, now)
	if len(b.Combos) != 3 {
		t.Fatalf("got %d combos, want 3", len(b.Combos))
	}
	want := []string{"300|0|50|1|-4", "600|0|50|1|0", oldCombo}
	for i, k := range want {
		if b.Combos[i].ComboKey != k || b.Combos[i].Priority != i+1 {
			t.Errorf("priority %d is %s (%d), want %s", i+1, b.Combos[i].ComboKey, b.Combos[i].Priority, k)
		}
	}
	top, old := b.Combos[0], b.Combos[2]
	if top.LightsScaled != 40 || top.LightsRecent != 40 || top.LightsActive != 40 || top.Score != 40 || top.ScoreParts.Scaled != 40 {
		t.Errorf("tonight's combo: %+v", top)
	}
	if old.LightsBlocked != 213 || old.LightsRecent != 0 || old.LightsActive != 0 || !(old.Score < 0.01) {
		t.Errorf("the 2025 combo: blocked %d recent %d active %d score %v", old.LightsBlocked, old.LightsRecent, old.LightsActive, old.Score)
	}
	if !strings.Contains(top.Basis, "Score 40 = 0 from lights with no dark + 40 from lights on a scaled dark + 0 from lights on a short set") ||
		!strings.Contains(top.Basis, "40 of its lights are from the last 30 days, 40 belong to active targets, and the latest night is 2026-10-10") ||
		!strings.Contains(top.Basis, "40 of its lights are from the last imaging night, 2026-10-10, so it ranks ahead of every combo without any") {
		t.Errorf("basis does not explain the score: %s", top.Basis)
	}
	if top.LightsLastNight != 40 || old.LightsLastNight != 0 || b.LastNight != "2026-10-10" || strings.Contains(old.Basis, "last imaging night") {
		t.Errorf("last night: top %d old %d backlog %q", top.LightsLastNight, old.LightsLastNight, b.LastNight)
	}
	if b.RankingBasis != RankingBasis() || !strings.Contains(b.RankingBasis, "halved for every 30 days") ||
		!strings.Contains(b.RankingBasis, "lights from the last imaging night") || strings.Contains(b.RankingBasis, "Target Scheduler") {
		t.Errorf("ranking basis: %q", b.RankingBasis)
	}
}

func TestNoDarkAtAllStillOutranksAScaledDarkOfTheSameAge(t *testing.T) {
	t.Parallel()
	now, _, sets := dawn20261011()
	night := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	lights := []darkLightGroup{
		rankLight(night, "A", 300, 50, -4, 30),
		rankLight(night, "B", 300, 240, -4, 20),
	}
	b := darkBacklog(lights, nil, sets, now)
	if b.Combos[0].ComboKey != "300|0|240|1|-4" {
		t.Fatalf("first combo %s, want the one with no dark", b.Combos[0].ComboKey)
	}
	if b.Combos[0].LightsBlocked != 20 || b.Combos[1].LightsScaled != 30 {
		t.Errorf("blocked %d scaled %d", b.Combos[0].LightsBlocked, b.Combos[1].LightsScaled)
	}
}

func TestActiveMeansLightsInTheLastThirtyDays(t *testing.T) {
	t.Parallel()
	now, lights, sets := dawn20261011()
	recent := rankLight(time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC), "Rosette Nebula", 600, 50, -20, 1)
	b := darkBacklog(append(lights, recent), nil, sets, now)
	var old DarkCombo
	for _, c := range b.Combos {
		if c.ComboKey == oldCombo {
			old = c
		}
	}
	if old.LightsActive != 213 || old.Score != 42.6 || old.ScoreParts.Blocked != 42.6 {
		t.Errorf("a target with lights in the last %d days keeps the floor: active %d score %v parts %+v", DarkRecentDays, old.LightsActive, old.Score, old.ScoreParts)
	}
	stale := rankLight(time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC), "Rosette Nebula", 600, 50, -20, 1)
	b = darkBacklog(append(lights, stale), nil, sets, now)
	for _, c := range b.Combos {
		if c.ComboKey == oldCombo && (c.LightsActive != 0 || !(c.Score < 0.01)) {
			t.Errorf("a target whose latest lights are %d days old is not active: %+v", 31, c)
		}
	}
}

func TestLastNightsCombosRankAboveHigherScores(t *testing.T) {
	t.Parallel()
	now, lights, sets := dawn20261011()
	recent := rankLight(time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC), "Rosette Nebula", 600, 50, -20, 1)
	b := darkBacklog(append(lights, recent), nil, sets, now)
	want := []string{"300|0|50|1|-4", "600|0|50|1|0", oldCombo, "600|0|50|1|-20"}
	if len(b.Combos) != len(want) {
		t.Fatalf("got %d combos, want %d", len(b.Combos), len(want))
	}
	for i, k := range want {
		if b.Combos[i].ComboKey != k || b.Combos[i].Priority != i+1 {
			t.Errorf("priority %d is %s score %v, want %s", i+1, b.Combos[i].ComboKey, b.Combos[i].Score, k)
		}
	}
	if b.Combos[1].Score >= b.Combos[2].Score {
		t.Fatalf("the test needs last night's combo to score below the older one: %v vs %v", b.Combos[1].Score, b.Combos[2].Score)
	}
}

func TestAgeHalvesEveryHalfLife(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 11, 11, 0, 0, 0, time.UTC)
	r := newDarkRank(nil, now)
	for _, tc := range []struct {
		night time.Time
		want  float64
	}{
		{time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC), 1},
		{time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC), 1},
		{time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC), 0.5},
		{time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC), 0.25},
	} {
		if got := r.weight(darkLightGroup{Night: tc.night, Object: "x"}); math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("night %s: weight %v, want %v", tc.night.Format(time.DateOnly), got, tc.want)
		}
	}
}

func TestPickSetTempsCentresOnTheHigherScore(t *testing.T) {
	t.Parallel()
	tally := func(blocked, scaled int, score ScoreParts) *tempTally {
		return &tempTally{blocked: blocked, scaled: scaled, score: score, nights: map[string]bool{}, readout: map[string]int{}}
	}
	temps := map[float64]*tempTally{
		-4: tally(0, 50, ScoreParts{Scaled: 50}),
		-3: tally(10, 0, ScoreParts{Blocked: 0.01}),
		-2: tally(10, 0, ScoreParts{Blocked: 0.01}),
		-1: tally(10, 0, ScoreParts{Blocked: 0.01}),
	}
	got := pickSetTemps(temps, "")
	if len(got) != 2 || got[0].SetTemp != -3 || got[0].Score != 50.02 || len(got[0].SetTempsCovered) != 3 || got[1].SetTemp != -1 {
		for _, c := range got {
			t.Logf("%v covers %v score %v", c.SetTemp, c.SetTempsCovered, c.Score)
		}
		t.Fatal("the first set temperature should cover tonight's -4 °C lights, not the most lights with no dark")
	}
}

func TestPickSetTempsCentresOnLastNightsLights(t *testing.T) {
	t.Parallel()
	tally := func(blocked, last int, score ScoreParts) *tempTally {
		return &tempTally{blocked: blocked, lastNight: last, score: score, nights: map[string]bool{}, readout: map[string]int{}}
	}
	temps := map[float64]*tempTally{
		-4: tally(5, 5, ScoreParts{Blocked: 10}),
		-2: tally(100, 0, ScoreParts{Blocked: 20}),
		-1: tally(100, 0, ScoreParts{Blocked: 20}),
		0:  tally(100, 0, ScoreParts{Blocked: 20}),
	}
	got := pickSetTemps(temps, "2026-10-10")
	if len(got) != 2 || got[0].SetTemp != -4 || got[0].LightsLastNight != 5 || got[1].SetTemp != -1 || !strings.Contains(got[0].Basis, "from the last imaging night, 2026-10-10") {
		for _, c := range got {
			t.Logf("%v covers %v score %v last night %d", c.SetTemp, c.SetTempsCovered, c.Score, c.LightsLastNight)
		}
		t.Fatal("the first set temperature should cover last night's -4 °C lights")
	}
}
