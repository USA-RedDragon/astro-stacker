package quality_test

import (
	"fmt"
	"math"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/quality"
)

const targetM31 = "M31"

func TestPerSecondScoresCompareSubsOfDifferentLengths(t *testing.T) {
	t.Parallel()
	db := emptyScheduler(t)
	measured := make([]quality.Measured, 0, 30)
	for i := range 10 {
		measured = append(measured, quality.Measured{File: "dark300_" + string(rune('a'+i)) + ".fits", Target: targetM31, Filter: filterRed,
			Exposure: 300, SkyADU: 506 + 120, Gain: 0, Offset: 50, HFR: 2, Stars: 500})
		measured = append(measured, quality.Measured{File: "dark120_" + string(rune('a'+i)) + ".fits", Target: targetM31, Filter: filterRed,
			Exposure: 120, SkyADU: 506 + 48, Gain: 0, Offset: 50, HFR: 2, Stars: 500})
		measured = append(measured, quality.Measured{File: "moon120_" + string(rune('a'+i)) + ".fits", Target: targetM31, Filter: filterRed,
			Exposure: 120, SkyADU: 506 + 240, Gain: 0, Offset: 50, HFR: 2, Stars: 500})
	}
	peds := quality.Pedestals{Configured: 506}

	var noDark120 []quality.Measured
	for _, m := range measured {
		if m.Exposure == 300 || m.SkyADU > 506+48 {
			noDark120 = append(noDark120, m)
		}
	}
	byLength, err := new(quality.Scorer).Load(t.Context(), db, peds, noDark120)
	if err != nil {
		t.Fatal(err)
	}
	if s := byLength["moon120_a.fits"]; !(s.Score > 0.99) {
		t.Errorf("by length: moonlit 120 s subs alone in their group scored %v, want 1 (the bias per-second scoring removes)", s.Score)
	}

	perSecond, err := (&quality.Scorer{PerSecond: true}).Load(t.Context(), db, peds, measured)
	if err != nil {
		t.Fatal(err)
	}
	d300, d120, m120 := perSecond["dark300_a.fits"], perSecond["dark120_a.fits"], perSecond["moon120_a.fits"]
	if math.Abs(d300.Score-d120.Score) > 1e-9 || !(d300.Score > 0.99) {
		t.Errorf("per second: dark 300 s %v and dark 120 s %v should both score 1 on the same sky", d300.Score, d120.Score)
	}
	if math.Abs(m120.Score-0.2) > 1e-9 {
		t.Errorf("per second: moonlit 120 s sub with five times the sky scored %v, want 0.2", m120.Score)
	}
	if d120.Exposure != 120 || d300.Exposure != 300 {
		t.Errorf("exposures %v and %v, want each sub's own", d120.Exposure, d300.Exposure)
	}
	if d120.ReferenceSubs != 30 {
		t.Errorf("per second: reference from %d subs, want all 30 of the filter and gain", d120.ReferenceSubs)
	}
}

func TestPerSecondKeepsSkyExcessTransparency(t *testing.T) {
	t.Parallel()
	db := emptyScheduler(t)
	if err := db.Exec(`INSERT INTO target VALUES (1, 'M31')`).Error; err != nil {
		t.Fatal(err)
	}
	id := 0
	add := func(name string, exposure, median, excess float64) {
		t.Helper()
		id++
		meta := fmt.Sprintf(`{"FileName":"%s","FilterName":"Red","ExposureDuration":%g,"Gain":0,"Offset":50,"HFR":2,"DetectedStars":500,"ADUMedian":%g,"ADUMean":%g,"RotatorPosition":0}`,
			name, exposure, median, median+excess)
		if err := db.Exec(`INSERT INTO acquiredimage VALUES (?, 1, 1, ?, NULL)`, id, meta).Error; err != nil {
			t.Fatal(err)
		}
	}
	for i := range 12 {
		add(fmt.Sprintf("long%d.xisf", i), 300, 506+120, 100)
		add(fmt.Sprintf("short%d.xisf", i), 120, 506+48, 40)
	}
	add("hazy.xisf", 300, 506+120, 33)
	scores, err := (&quality.Scorer{PerSecond: true}).Load(t.Context(), db, quality.Pedestals{Configured: 506}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if h := scores["hazy.xisf"]; h.TransparencySource != quality.TransparencySkyExcess || math.Abs(h.Transparency-0.33) > 0.01 {
		t.Errorf("hazy sub: transparency %v from %q (%s), want 0.33 from the sky excess", h.Transparency, h.TransparencySource, h.TransparencyMissing)
	}
	if s := scores["short0.xisf"]; !(s.Transparency > 0.99) || !(s.Score > 0.99) {
		t.Errorf("clear 120 s sub: transparency %v score %v, want 1 against the 300 s subs per second", s.Transparency, s.Score)
	}
}
