package coverage

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/calmatch"
)

func biasSet(gain, offset float64, night time.Time, count int) calmatch.Set {
	return calmatch.Set{Type: biasType, Night: night, Gain: gain, Offset: offset, BinX: 1, Exposure: 0, SetTemp: math.NaN(),
		Rotator: math.NaN(), Count: count, From: night, To: night}
}

func TestBiasBacklogMissingAndThin(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 10, 7, 0, 0, 0, time.UTC)
	d := func(s string) time.Time { v, _ := time.Parse(time.DateOnly, s); return v }
	f := func(v float64) *float64 { return &v }
	mode := "High Gain"
	lights := []biasLightGroup{
		{Night: d("2026-10-01"), Gain: f(0), Offset: f(50), BinX: f(1), N: 40},
		{Night: d("2026-09-20"), Gain: f(0), Offset: f(50), BinX: f(1), N: 10},
		{Night: d("2025-02-20"), Gain: f(0), Offset: f(50), BinX: f(1), N: 7},
		{Night: d("2025-04-01"), Gain: f(0), Offset: f(50), BinX: f(1), N: 9},
		{Night: d("2026-10-09"), Gain: f(100), Offset: f(50), BinX: f(1), N: 5, ReadoutMode: &mode},
		{Night: d("2026-09-01"), Gain: f(100), Offset: f(50), BinX: f(1), N: 6},
		{Night: d("2026-10-02"), Gain: f(0), Offset: f(90), BinX: f(1), N: 3},
		{Night: d("2026-10-02"), Gain: f(0), Offset: f(240), BinX: f(1), N: 8},
		{Night: d("2025-01-25"), Gain: f(0), Offset: f(240), BinX: f(1), N: 11},
		{Night: d("2026-10-02"), Offset: f(50), BinX: f(1), N: 4},
		{Night: d("2026-10-02"), Gain: f(0), Offset: f(50), N: 2},
	}
	imported := biasSet(0, 240, d("2025-01-19"), 0)
	imported.Master = "offset240/masters/masterBias_BIN-1_6248x4176.xisf"
	sets := []calmatch.Set{biasSet(0, 50, d("2025-02-05"), 30), biasSet(100, 50, d("2026-10-08"), 10), imported}
	recent := now.Add(-2 * time.Hour)
	older := now.Add(-50 * time.Hour)
	frames := []biasFrame{
		{Gain: f(100), Offset: f(50), BinX: f(1), DateObs: &older},
		{Gain: f(100), Offset: f(50), BinX: f(1), DateObs: &recent},
		{Gain: f(100), Offset: f(50), BinX: f(1), DateObs: &recent},
	}
	b := biasBacklog(lights, frames, sets, now)
	checkBiasCombos(t, b, mode, recent)
	checkBiasBacklogFields(t, b)
}

func TestBiasBacklogThinClearsAfterAFullSet(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 20, 7, 0, 0, 0, time.UTC)
	d := func(s string) time.Time { v, _ := time.Parse(time.DateOnly, s); return v }
	f := func(v float64) *float64 { return &v }
	lights := []biasLightGroup{{Night: d("2026-10-07"), Gain: f(100), Offset: f(50), BinX: f(1), N: 5}}
	short := biasSet(100, 50, d("2026-10-08"), 10)
	if b := biasBacklog(lights, nil, []calmatch.Set{short}, now); len(b.Combos) != 1 || b.Combos[0].LightsThin != 5 {
		t.Fatalf("a short set with no full set after it is thin: %+v", b.Combos)
	}
	full := biasSet(100, 50, d("2026-10-12"), BiasFramesTarget)
	if b := biasBacklog(lights, nil, []calmatch.Set{short, full}, now); len(b.Combos) != 0 {
		t.Fatalf("a full set taken after the short one ends the need: %+v", b.Combos)
	}
}

func checkBiasCombos(t *testing.T, b BiasBacklog, mode string, recent time.Time) {
	t.Helper()
	if len(b.Combos) != 2 {
		t.Fatalf("an old full set or imported master is never wanted again, so only missing and thin remain: %+v", b.Combos)
	}
	missing, thin := b.Combos[0], b.Combos[1]
	if missing.ComboKey != "bias|0|90|1" || missing.LightsMissing != 3 || missing.Priority != 1 || missing.CurrentBiasNight != nil {
		t.Errorf("missing %+v", missing)
	}
	if thin.Priority != 2 || thin.ComboKey != "bias|100|50|1" || thin.LightsThin != 11 || thin.FramesHave != 2 || thin.FramesNeeded != BiasFramesTarget ||
		thin.ReadoutMode == nil || *thin.ReadoutMode != mode || thin.NewestBiasAt == nil || !thin.NewestBiasAt.Equal(recent) {
		t.Errorf("thin %+v", thin)
	}
}

func checkBiasBacklogFields(t *testing.T, b BiasBacklog) {
	t.Helper()
	for _, c := range b.Combos {
		if c.Requests == nil || c.Basis == "" {
			t.Errorf("combo %s: requests %v basis %q", c.ComboKey, c.Requests, c.Basis)
		}
	}
	if len(b.Unschedulable) != 2 || b.Unschedulable[0].Lights != 4 || !strings.Contains(b.Unschedulable[0].Reason, "GAIN or OFFSET") ||
		!strings.Contains(b.Unschedulable[1].Reason, "XBINNING") {
		t.Errorf("unschedulable %+v", b.Unschedulable)
	}
	if b.FramesNeeded != 30 || b.SessionGapHours != 36 || b.FramesNeededBasis == "" || !strings.Contains(b.AgeBasis, "nearest bias set") {
		t.Errorf("backlog %+v", b)
	}
}

func TestBiasBacklogNeverWantsAFullSetForItsAge(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 10, 7, 0, 0, 0, time.UTC)
	night := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	f := func(v float64) *float64 { return &v }
	lights := []biasLightGroup{
		{Night: night, Gain: f(0), Offset: f(50), BinX: f(1), N: 20},
		{Night: night.AddDate(0, -6, 0), Gain: f(0), Offset: f(50), BinX: f(1), N: 20},
	}
	old := biasSet(0, 50, time.Date(2025, 2, 5, 0, 0, 0, 0, time.UTC), BiasFramesTarget)
	if b := biasBacklog(lights, nil, []calmatch.Set{old}, now); len(b.Combos) != 0 {
		t.Errorf("a full set 611 days before the lights is still enough: %+v", b.Combos)
	}
	oldShort := biasSet(0, 50, time.Date(2025, 2, 5, 0, 0, 0, 0, time.UTC), BiasFramesTarget-1)
	b := biasBacklog(lights, nil, []calmatch.Set{oldShort}, now)
	if len(b.Combos) != 1 || b.Combos[0].LightsThin != 40 || b.Combos[0].LightsMissing != 0 ||
		b.Combos[0].CurrentBiasAgeDays == nil || *b.Combos[0].CurrentBiasAgeDays != 612 || strings.Contains(b.Combos[0].Basis, "days") {
		t.Fatalf("an old short set is wanted for its frame count only: %+v", b.Combos)
	}
	fresh := biasSet(0, 50, night, BiasFramesTarget)
	if b := biasBacklog(lights, nil, []calmatch.Set{oldShort, fresh}, now); len(b.Combos) != 0 {
		t.Errorf("a new full set at the newest night leaves %+v", b.Combos)
	}
}
