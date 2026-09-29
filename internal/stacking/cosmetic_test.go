package stacking

import (
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
)

// A sky with a gradient, nebulosity, noise and bright stars, and a hot and
// a cold column: only the two columns are listed for cosme.
func TestBadColumns(t *testing.T) {
	t.Parallel()
	const w, h = 400, 300
	rng := rand.New(rand.NewPCG(1, 2))
	data := make([]float32, w*h)
	for y := range h {
		for x := range w {
			v := 0.05 + 0.02*float64(x)/w + 0.01*float64(y)/h + rng.NormFloat64()*0.002
			// A nebula as bright as 5σ, 20 pixels across.
			dx, dy := float64(x-200)/20, float64(y-150)/40
			v += 0.01 * math.Exp(-(dx*dx+dy*dy)/2)
			data[y*w+x] = float32(v)
		}
	}
	for _, s := range [][2]int{{50, 60}, {120, 200}, {300, 100}} {
		for dy := -3; dy <= 3; dy++ {
			for dx := -3; dx <= 3; dx++ {
				data[(s[1]+dy)*w+s[0]+dx] = 0.95 // saturated
			}
		}
	}
	for y := range h {
		data[y*w+321] += 0.03  // hot, 15σ
		data[y*w+77] -= 0.0015 // cold, under 1σ
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "sub.fit")
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := imagedata.WriteFITS(f, w, h, 1, data, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	list, err := writeCosmeList(file, filepath.Join(dir, "sub.lst"), 0.9)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(list)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); got != "C 77 0\nC 321 0\n" {
		t.Errorf("cosme list %q, want columns 77 and 321", got)
	}

	// A clean sub needs no list.
	for y := range h {
		data[y*w+321] -= 0.03
		data[y*w+77] += 0.0015
	}
	if cols := badColumns(&imagedata.Image{W: w, H: h, C: 1, Data: data}, 0.9); len(cols) != 0 {
		t.Errorf("bad columns %v in a clean sub", cols)
	}
}
