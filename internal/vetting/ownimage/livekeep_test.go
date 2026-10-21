package ownimage

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

// liveNight is the dip watch on find 15523 of 2026-10-21 UTC as the live
// card measured it: each sub's star and comparison stars' light.
type liveNight struct {
	Kept []int `json:"kept"`
	Subs []struct {
		T     time.Time `json:"t"`
		Flux  float64   `json:"flux"`
		Ferr  float64   `json:"ferr"`
		Comps []float64 `json:"comps"`
	} `json:"subs"`
}

func readLiveNight(t *testing.T) (liveNight, [][]float64, [][]float64) {
	t.Helper()
	raw, err := os.ReadFile("testdata/live15523-20261021.json.zst")
	if err != nil {
		t.Fatal(err)
	}
	dec, err := zstd.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()
	var n liveNight
	if err := json.NewDecoder(dec).Decode(&n); err != nil {
		t.Fatal(err)
	}
	flux := make([][]float64, 0, len(n.Subs))
	for _, s := range n.Subs {
		flux = append(flux, slices.Clone(s.Comps))
	}
	return n, flux, liveMedians(flux)
}

func liveMedians(flux [][]float64) [][]float64 {
	m := make([]float64, len(flux[0]))
	for j := range m {
		var v []float64
		for _, f := range flux {
			if f[j] > 0 {
				v = append(v, f[j])
			}
		}
		m[j] = median(v)
	}
	out := make([][]float64, len(flux))
	for i := range out {
		out[i] = m
	}
	return out
}

// liveMags is each sub's star against the kept stars, and the level.
func liveMags(t *testing.T, n liveNight, flux, med [][]float64, kept []int) ([]float64, []float64, []subLevel) {
	t.Helper()
	mags := make([]float64, 0, len(n.Subs))
	comps := make([]float64, 0, len(n.Subs))
	lvs := make([]subLevel, 0, len(n.Subs))
	for i, s := range n.Subs {
		lv, ok := liveLevel(flux[i], med[i], kept)
		if !ok {
			t.Fatalf("sub %d has no level", i)
		}
		mags = append(mags, -2.5*math.Log10(s.Flux/lv.t))
		comps = append(comps, -2.5*math.Log10(lv.t))
		lvs = append(lvs, lv)
	}
	return mags, comps, lvs
}

func TestLiveUniformDimmingCancels(t *testing.T) {
	t.Parallel()
	n, flux, med := readLiveNight(t)
	kept := liveKeep(flux, med, n.Kept)
	mags, comps, lvs := liveMags(t, n, flux, med, kept)
	k := len(mags)
	early, late := median(comps[:20]), median(comps[k-20:])
	if late-early < 0.2 {
		t.Fatalf("the nearby stars dimmed %.3f mag from the first to the last 20 subs, want the night's 0.25", late-early)
	}
	if d := median(mags[k-20:]) - median(mags[:20]); math.Abs(d) > 0.06 {
		t.Errorf("the star moved %.3f mag against the nearby stars while they dimmed %.3f together", d, late-early)
	}
	share := make([]float64, 0, liveRecent)
	for _, lv := range lvs[k-liveRecent:] {
		share = append(share, float64(lv.agree)/float64(lv.present))
	}
	if s := median(share); s < 0.8 {
		t.Errorf("only %.2f of the nearby stars agree in the last subs", s)
	}
	if len(kept) < len(n.Kept)-1 || !slices.Contains(kept, 4) || !slices.Contains(kept, 2) {
		t.Errorf("kept %v of %v", kept, n.Kept)
	}
}

func TestLiveDropsAFadingComparisonStar(t *testing.T) {
	t.Parallel()
	n, flux, med := readLiveNight(t)
	base, _, _ := liveMags(t, n, flux, med, liveKeep(flux, med, n.Kept))
	k := len(flux)
	for i := k - 8; i < k; i++ {
		flux[i][4] *= math.Pow(10, -0.4*0.3)
	}
	med = liveMedians(flux)
	kept := liveKeep(flux, med, n.Kept)
	if slices.Contains(kept, 4) {
		t.Fatalf("kept %v: the comparison star fading 0.3 mag over the last 8 subs is still in", kept)
	}
	mags, _, _ := liveMags(t, n, flux, med, kept)
	var d []float64
	for i := k - 8; i < k; i++ {
		d = append(d, mags[i]-base[i])
	}
	if m := median(d); math.Abs(m) > 0.03 {
		t.Errorf("the star reads %.3f mag off in the last 8 subs with the fading star dropped", m)
	}
	naive, _, _ := liveMags(t, n, flux, med, n.Kept)
	var nd []float64
	for i := k - 8; i < k; i++ {
		nd = append(nd, naive[i]-base[i])
	}
	if m := median(nd); math.Abs(m) < 0.025 {
		t.Errorf("kept, the fading star moves the star only %.3f mag; the test does not test the drop", m)
	}
}
