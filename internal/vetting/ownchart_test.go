package vetting

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoverysource"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/ownimage"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/period"
)

// TestNightChart3635 is the own light curve of source 3635 (Gaia DR3
// 4315093031714485376) on 2026-10-13, built from the vet's own fits of
// its subs. The chart showed one Blue frame 0.215 mag faint with a 0.013
// mag error, though the comparison stars' fits wander by 0.1 mag from
// frame to frame and the detection's difference flux for that frame put
// it near normal. It must chart the night's five Luminance frames, the
// filter with the most frames, each with an error no smaller than the
// single frame scatter.
func TestNightChart3635(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("testdata/own3635_curve.json.zst")
	if err != nil {
		t.Fatal(err)
	}
	text, err := unzstd(raw)
	if err != nil {
		t.Fatal(err)
	}
	var c ownimage.Curve
	if err := json.Unmarshal(text, &c); err != nil {
		t.Fatal(err)
	}
	dep := map[string]float64{"ZTF r": 0.196}
	e := &period.Ephemeris{Period: 7.839490, T0: 2458321.28364, Kind: period.KindEclipsing, Eclipses: []period.Eclipse{
		{Phase: 0, Width: 0.01585, Shape: 1.394, Depth: dep}, {Phase: 0.5, Width: 0.01585, Shape: 1.394, Depth: dep}}}
	var sum discoverysource.Summary
	sum.Best.Night = time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	ch := nightChart(sum, &c, e, nil)
	if len(ch.Points) != 5 {
		t.Fatalf("charted %d frames (%s), want the 5 Luminance frames", len(ch.Points), ch.Sub)
	}
	for _, p := range ch.Points {
		if p.Err < c.Scatter-1e-4 {
			t.Errorf("frame at %.2f h has error %.3f, under the %.3f mag single frame scatter", p.X, p.Err, c.Scatter)
		}
	}
	if x0, x1 := ch.Points[0].X, ch.Points[4].X; x0 > -2.5 || x1 < -0.5 {
		t.Errorf("frames span %.2f to %.2f h from Min II, want about -2.6 to -0.4", x0, x1)
	}
}
