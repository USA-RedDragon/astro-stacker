package binaries

import (
	"math"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/eclfacts"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
)

// TestFactsPutSecondDipAtItsMeasuredPhase has the vet's ephemeris put the
// second dip at mid-cycle where the surveys see it 0.004 of a cycle
// later: the facts' ephemeris takes the measured phase, so predictions
// are placed where the error they quote was measured.
func TestFactsPutSecondDipAtItsMeasuredPhase(t *testing.T) {
	t.Parallel()
	_, truth := star27822(0)
	truth.Eclipses[1].Depth = map[string]float64{keyZTFr: 0.12}
	truth.Eclipses[1].Phase = 0.513
	rng := rand.New(rand.NewPCG(27822, 22))
	s := lc.Series{Survey: lc.ZTF, Band: "r", Dilution: 1}
	for i := range 3000 {
		b := 2458300 + float64(i)*0.61 + 0.2*rng.Float64()
		s.Points = append(s.Points, lc.Point{BJD: b, Mag: 15.5 + truth.Predict(b, keyZTFr) + 0.02*rng.NormFloat64(), Err: 0.02})
	}
	_, vet := star27822(0)
	vet.Eclipses[1].Depth = map[string]float64{keyZTFr: 0.12}
	r := &vetting.Report{Ephemeris: vet, Survey: &eclfacts.Summary{Period: vet.Period, Twin: &eclfacts.Twin{}}}
	f := Work(r, vetting.StarData{Series: []lc.Series{s}}, nil, time.Time{})
	tw := f.Survey.Twin
	if !(tw.PhaseErr > 0) || f.Ephemeris.Eclipses[1].Phase != tw.Phase2 {
		t.Fatalf("second dip at %.5f, measured %.5f ± %.5f", f.Ephemeris.Eclipses[1].Phase, tw.Phase2, tw.PhaseErr)
	}
	if d := tw.Phase2 - 0.513; math.Abs(d) > 3*tw.PhaseErr {
		t.Errorf("measured %.5f ± %.5f, the dip is at 0.513", tw.Phase2, tw.PhaseErr)
	}
	if vet.Eclipses[1].Phase != 0.5090730285476748 {
		t.Error("the report's ephemeris was changed")
	}
}

// TestDeeperSecondDipTakesThePhaseError has the dip at the ephemeris's
// second phase as the deeper one: its primaries take the measured phase's
// error, and the secondaries at the epoch do not.
func TestDeeperSecondDipTakesThePhaseError(t *testing.T) {
	t.Parallel()
	st, e := star27822(0.00025279098867542205)
	st.Facts.Deeper = 1
	from := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	var prim, sec int
	for _, p := range Predict(e, 1, st.Facts.Gaia.RA, st.Facts.Gaia.Dec, from, from.Add(10*24*time.Hour)) {
		q := st.withPhaseError(p, e)
		switch p.Kind {
		case KindPrimary:
			prim++
			want := math.Hypot(p.Err.Minutes(), 0.00025279098867542205*e.Period*1440)
			if math.Abs(q.Err.Minutes()-want) > 0.01 {
				t.Errorf("primary error %.2f min, want %.2f with the phase's", q.Err.Minutes(), want)
			}
		case KindSecondary:
			sec++
			if q != p {
				t.Errorf("secondary at the epoch moved to %s ± %s", q.Mid, q.Err)
			}
		}
	}
	if prim == 0 || sec == 0 {
		t.Fatalf("%d primaries and %d secondaries predicted", prim, sec)
	}
}
