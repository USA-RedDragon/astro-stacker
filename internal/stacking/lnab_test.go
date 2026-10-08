package stacking

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
	"github.com/USA-RedDragon/astro-stacker/internal/preview"
)

// TestLocalNormAB stacks real registered subs (s000.fit… in LN_AB_DIR, with
// manifest.txt lines "key|exposure|weight") with and without local
// normalization, and writes both masters, previews and their difference.
func TestLocalNormAB(t *testing.T) {
	dir := os.Getenv("LN_AB_DIR")
	if dir == "" {
		t.Skip("LN_AB_DIR not set")
	}
	f, err := os.Open(filepath.Join(dir, "manifest.txt"))
	if err != nil {
		t.Fatal(err)
	}
	var subs []storedSub
	longest := 0.0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		p := strings.Split(sc.Text(), "|")
		e, _ := strconv.ParseFloat(p[1], 64)
		w, _ := strconv.ParseFloat(p[2], 64)
		subs = append(subs, storedSub{key: fmt.Sprintf("s%03d.fit", len(subs)), exposure: e, weight: w})
		longest = max(longest, e)
	}
	f.Close()
	load := func(_ int, s storedSub) ([]float32, int, int, error) { return readSub(filepath.Join(dir, s.key)) }
	passes := 2
	if len(subs) < 40 {
		passes = 3
	}
	masters := map[string][]float32{}
	var w, h int
	for _, ln := range []bool{false, true} {
		opts := DefaultOptions()
		opts.LocalNorm = ln
		acc, err := streamStack(subs, passes, opts, load)
		if err != nil {
			t.Fatal(err)
		}
		w, h = acc.W, acc.H
		name := map[bool]string{false: "flat", true: "local"}[ln]
		used := 0.0
		for _, c := range acc.Count {
			used += float64(c)
		}
		m := acc.Master(longest)
		masters[name] = m
		t.Logf("%-5s samples kept %.0f, background noise %.3g", name, used, bgNoise(m))
		if err := writeFITSFile(filepath.Join(dir, name+".fit"), w, h, 1, m, nil); err != nil {
			t.Fatal(err)
		}
		jpg, err := preview.Render(&imagedata.Image{W: w, H: h, C: 1, Data: m}, preview.DefaultOptions())
		if err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(dir, name+".jpg"), jpg, 0o644)
	}
	// Each sub's fitted transparency against the flat stack.
	opts := DefaultOptions()
	opts.LocalNorm = false
	ref, err := streamStack(subs, 1, opts, load)
	if err != nil {
		t.Fatal(err)
	}
	var scales []string
	for i, s := range subs {
		sub, _, _, err := load(i, s)
		if err != nil {
			t.Fatal(err)
		}
		sky := fitSky(func(i int) float32 { return sub[i] }, w, h, s.exposure, ref, 0.9, background(sub, 0.9)/s.exposure)
		scales = append(scales, fmt.Sprintf("%.2f", sky.scale))
	}
	t.Logf("transparency per sub: %s", strings.Join(scales, " "))
	diff := make([]float32, w*h)
	for i := range diff {
		diff[i] = masters["local"][i] - masters["flat"][i] + 0.5
	}
	jpg, err := preview.Render(&imagedata.Image{W: w, H: h, C: 1, Data: diff}, preview.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "diff.jpg"), jpg, 0o644)
}

// bgNoise is the robust σ of pixel-to-pixel differences in a master's
// background: each pixel minus its right neighbour, over pixels near the
// median, divided by √2.
func bgNoise(m []float32) float64 {
	s := make([]float32, 0, len(m)/64)
	for i := 0; i < len(m); i += 64 {
		if m[i] > 0 {
			s = append(s, m[i])
		}
	}
	slices.Sort(s)
	lo, hi := s[len(s)/10], s[len(s)/2]
	var d []float64
	for i := 0; i+1 < len(m); i += 7 {
		if m[i] > lo && m[i] < hi && m[i+1] > lo && m[i+1] < hi {
			d = append(d, math.Abs(float64(m[i]-m[i+1])))
		}
	}
	slices.Sort(d)
	return madToSigma * d[len(d)/2] / math.Sqrt2
}
