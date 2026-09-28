package stacking

import (
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
)

// TestDarkStats prints the light-leak measure of real darks in DARK_DIR.
func TestDarkStats(t *testing.T) {
	dir := os.Getenv("DARK_DIR")
	if dir == "" {
		t.Skip("DARK_DIR not set")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.xisf"))
	for _, f := range files {
		b, _ := os.ReadFile(f)
		im, err := imagedata.Decode(b)
		if err != nil {
			t.Fatal(err)
		}
		spread, noise := darkSpread(im.Data, im.W, im.H)
		t.Logf("%-60s noise %5.2f  spread %6.1f ADU  leaky %v", filepath.Base(f), noise, spread, leaky(spread, noise))
	}
}

func TestLightLeak(t *testing.T) {
	t.Parallel()
	const w, h = 640, 480
	r := rand.New(rand.NewPCG(5, 5))
	clean := make([]float32, w*h)
	leak := make([]float32, w*h)
	for y := range h {
		for x := range w {
			n := 500 + 5*r.NormFloat64()
			clean[y*w+x] = float32(n / 65535)
			leak[y*w+x] = float32((n + 150*float64(w-x+h-y)/float64(w+h)) / 65535) // brighter towards the top left
		}
	}
	if s, n := darkSpread(clean, w, h); leaky(s, n) {
		t.Errorf("clean dark: spread %v noise %v called leaky", s, n)
	}
	if s, n := darkSpread(leak, w, h); !leaky(s, n) {
		t.Errorf("leaky dark: spread %v noise %v called clean", s, n)
	}
}
