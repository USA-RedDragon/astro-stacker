package quality

import (
	"encoding/csv"
	"math"
	"os"
	"strconv"
	"strings"
	"testing"
)

// realSkyBinSubs reads testdata/skybins.csv: four targets' subs in one filter
// each, as production scored them on 2026-10-24.
func realSkyBinSubs(t *testing.T) []binnedSub {
	t.Helper()
	f, err := os.Open("testdata/skybins.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	recs, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	num := func(s string) float64 {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	subs := make([]binnedSub, 0, len(recs)-1)
	for _, r := range recs[1:] {
		gs, err := strconv.Atoi(r[3])
		if err != nil {
			t.Fatal(err)
		}
		subs = append(subs, binnedSub{target: r[0], s: &SubScore{
			Filter: r[1], File: r[2], GradingStatus: gs, StackerRejected: r[4] == "true", Exposure: num(r[5]),
			Sky: num(r[6]), HFR: num(r[7]), Transparency: num(r[8]), Score: num(r[9]), PlainScore: num(r[10]),
		}})
	}
	best := map[string]float64{}
	for _, b := range subs {
		if b.s.GradingStatus != GradingRejected || b.s.StackerRejected {
			best[b.target+b.s.Filter] = math.Max(best[b.target+b.s.Filter], b.s.Score)
		}
	}
	for _, b := range subs {
		b.s.TargetBest = best[b.target+b.s.Filter]
	}
	return subs
}

// The 13 Luminance subs of Sagittarius Star Cloud Panel 2 taken under a 94%
// Moon on the night of 2026-10-23 scored 0.009 to 0.076 against a best of
// 0.39 set by two moonless subs, and all fell under the old cut. Against the
// subs in a similar sky the clear ones are kept. Two stay out for real
// reasons: 0005 (HFR 2.66 against 1.7, guiding 1.57″ against 0.8″) and 0019
// (starlight 0.72 of the field's best, HFR 1.81).
func TestSkyBinsKeepClearMoonlitSubs(t *testing.T) {
	t.Parallel()
	subs := realSkyBinSubs(t)
	applySkyBins(subs)
	stillOut := map[string]string{"0005": "", "0019": ""}
	var night int
	for _, b := range subs {
		s := b.s
		if b.target != "Sagittarius Star Cloud Panel 2" || !strings.HasPrefix(s.File, "2026-10-23_") {
			continue
		}
		night++
		if !(s.Score < 0.3*s.TargetBest) {
			t.Errorf("%s: score %.3f was not under the old cut %.3f", s.File, s.Score, 0.3*s.TargetBest)
		}
		low, why := s.BelowCut(0.3)
		seq := strings.TrimSuffix(s.File[len(s.File)-9:], ".xisf")
		if _, bad := stillOut[seq]; bad {
			if !low {
				t.Errorf("%s: kept, want it cut", s.File)
			}
			stillOut[seq] = why
			continue
		}
		if low {
			t.Errorf("%s: cut (%s), want it kept", s.File, why)
		}
		if s.SkyBin.Subs < SkyBinMinSubs || s.SkyBin.High == 0 || !(s.SkyRate > 10) {
			t.Errorf("%s: sky %.1f ADU/s judged against %+v, want a bin of the moonlit subs", s.File, s.SkyRate, s.SkyBin)
		}
	}
	if night != 13 {
		t.Errorf("%d subs on 2026-10-23, want 13", night)
	}
	if !strings.Contains(stillOut["0005"], "similar sky") || !strings.Contains(stillOut["0019"], "similar sky") {
		t.Errorf("reasons %q", stillOut)
	}
}

// Genuinely bad subs stay out: NGC 2264 Panel 3 0019 with HFR 3.73 and full
// starlight, and Garlic Nebula 0141 and M 15 0018 with their starlight down
// to 0.40 and 0.42 of the field's best (cloud).
func TestSkyBinsStillCutBadSubs(t *testing.T) {
	t.Parallel()
	subs := realSkyBinSubs(t)
	applySkyBins(subs)
	want := map[string]string{
		"2026-03-30_23-58-38_Blue_-13.00_300.00s_0019.xisf": "too soft",
		"2026-10-22_01-51-57_Red_-12.00_120.00s_0141.xisf":  "similar sky",
		"2026-10-23_01-41-43_H-a_-13.80_600.00s_0018.xisf":  "similar sky",
	}
	for _, b := range subs {
		w, ok := want[b.s.File]
		if !ok {
			continue
		}
		delete(want, b.s.File)
		if low, why := b.s.BelowCut(0.3); !low || !strings.Contains(why, w) {
			t.Errorf("%s: below cut %v (%s), want cut for %q", b.s.File, low, why, w)
		}
	}
	if len(want) != 0 {
		t.Errorf("not in the testdata: %v", want)
	}
}

// A bin with fewer than SkyBinMinSubs subs widens until it has them, and a
// target with fewer than that judges every sub against its best.
func TestSkyBinFallback(t *testing.T) {
	t.Parallel()
	mk := func(sky, score float64) binnedSub {
		return binnedSub{target: "T", s: &SubScore{Filter: "L", Exposure: 1, Sky: sky, HFR: 2, Transparency: 1, Score: score, PlainScore: score}}
	}
	subs := make([]binnedSub, 0, 10)
	subs = append(subs, mk(1, 1), mk(1.1, 0.9), mk(1.2, 0.95), mk(1.3, 0.9), mk(1.4, 0.8), mk(10, 0.1))
	applySkyBins(subs)
	moon := subs[5].s
	if moon.SkyBin.High != 0 || moon.SkyBin.Best != 1 || moon.SkyBin.Subs != 6 {
		t.Errorf("lone moonlit sub binned %+v, want all 6 subs and best 1", moon.SkyBin)
	}
	if low, _ := moon.BelowCut(0.3); !low {
		t.Error("lone moonlit sub kept, want the cut as before sky bins")
	}
	subs = append(subs, mk(9, 0.12), mk(11, 0.09), mk(12, 0.08), mk(8, 0.11))
	applySkyBins(subs)
	if moon.SkyBin.Subs != 5 || moon.SkyBin.Best != 0.12 {
		t.Errorf("moonlit sub binned %+v, want its 5 moonlit peers, best 0.12", moon.SkyBin)
	}
	if low, why := moon.BelowCut(0.3); low {
		t.Errorf("moonlit sub cut: %s", why)
	}
	if !(math.Abs(moon.Score*moon.Exposure-0.1) < 1e-12) {
		t.Errorf("weight %v moved: the bin changes the cut, not the weight", moon.Score)
	}
}
