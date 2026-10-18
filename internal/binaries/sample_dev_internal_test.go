//go:build binariesdev

package binaries

import (
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
)

// TestWriteSamples writes a share picture, a finding chart and a phased
// plot of made-up data to BIN_SAMPLES, to look at by eye.
func TestWriteSamples(t *testing.T) {
	t.Parallel()
	dir := os.Getenv("BIN_SAMPLES")
	if dir == "" {
		t.Skip("set BIN_SAMPLES to a directory")
	}
	img := noisy(1600, 1060, 3)
	share, err := SharePicture(img, Mark{X: 560, Y: 520}, "the new variable star", "Gaia DR3 123456789012345678")
	if err != nil {
		t.Fatal(err)
	}
	chart, _, err := FindingChart(img, ChartGeometry{X: 560, Y: 520, Scale: 2.4, North: [2]float64{0.2, -1}, East: [2]float64{-1, -0.2}}, "Gaia DR3 123456789012345678")
	if err != nil {
		t.Fatal(err)
	}
	e := twoDips(0.43, 0.27)
	rng := rand.New(rand.NewPCG(1, 1))
	ss := make([]lc.Series, 0, 2)
	for _, band := range []string{"g", "r"} {
		s := lc.Series{Survey: lc.ZTF, Band: band}
		for i := range 1500 {
			tt := 2458300 + float64(i)*1.37 + rng.Float64()
			s.Points = append(s.Points, lc.Point{BJD: tt, Mag: 15.3 + e.Predict(tt, "ZTF r") + 0.02*rng.NormFloat64(), Err: 0.02})
		}
		ss = append(ss, s)
	}
	phased, _, err := PhasedPlot(ss, e, 0, "Gaia DR3 123456789012345678", [2]int{2018, 2025})
	if err != nil {
		t.Fatal(err)
	}
	for n, b := range map[string][]byte{"share.jpg": share, "chart.png": chart, "phased.png": phased} {
		if err := os.WriteFile(filepath.Join(dir, n), b, 0o600); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s %d bytes", n, len(b))
	}
}
