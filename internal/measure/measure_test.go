package measure

import (
	"bufio"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
)

// TestAgainstNINA compares measurements with NINA's for real subs in
// HFR_DIR, listed in nina.txt as "file|hfr|adu median|stars|filter".
func TestAgainstNINA(t *testing.T) {
	dir := os.Getenv("HFR_DIR")
	if dir == "" {
		t.Skip("HFR_DIR not set")
	}
	f, err := os.Open(filepath.Join(dir, "nina.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		p := strings.Split(sc.Text(), "|")
		b, err := os.ReadFile(filepath.Join(dir, p[0]))
		if err != nil {
			continue
		}
		im, err := imagedata.Decode(b)
		if err != nil {
			t.Fatal(err)
		}
		r := Sub(im)
		hfr, _ := strconv.ParseFloat(p[1], 64)
		sky, _ := strconv.ParseFloat(p[2], 64)
		t.Logf("%-10s NINA hfr %5.2f sky %6.0f stars %5s | ours hfr %5.2f sky %6.0f peaks %6d | hfr ratio %.2f",
			p[4], hfr, sky, p[3], r.HFR, r.SkyADU, r.Stars, r.HFR/hfr)
	}
}

// Gaussian stars of width σ have a half-flux radius of σ·√(π/2).
func TestGaussianStars(t *testing.T) {
	t.Parallel()
	const w, h, sigma = 800, 600, 1.4
	r := rand.New(rand.NewPCG(4, 4))
	im := &imagedata.Image{W: w, H: h, C: 1, Data: make([]float32, w*h)}
	for i := range im.Data {
		im.Data[i] = float32(0.01 + 0.0005*r.NormFloat64())
	}
	for range 150 {
		sx, sy, peak := 30+r.Float64()*(w-60), 30+r.Float64()*(h-60), 0.05+0.3*r.Float64()
		for y := int(sy) - 12; y <= int(sy)+12; y++ {
			for x := int(sx) - 12; x <= int(sx)+12; x++ {
				dx, dy := float64(x)-sx, float64(y)-sy
				im.Data[y*w+x] += float32(peak * math.Exp(-(dx*dx+dy*dy)/(2*sigma*sigma)))
			}
		}
	}
	got := Sub(im)
	if want := sigma * math.Sqrt(math.Pi/2); math.Abs(got.HFR-want) > 0.1*want {
		t.Errorf("HFR = %.2f, want %.2f", got.HFR, want)
	}
	if math.Abs(got.SkyADU-0.01*65535) > 5 {
		t.Errorf("sky = %.0f ADU, want 655", got.SkyADU)
	}
}
