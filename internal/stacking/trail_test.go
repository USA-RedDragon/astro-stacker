package stacking

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// TestSatelliteTrail runs the stacker's rejection on real registered subs,
// one of which has a satellite trail, and measures what the trail leaves in
// the master. It only runs when TRAIL_DIR points at the subs:
//
//	TRAIL_DIR=subs TRAIL_SUB=_0066 TRAIL_OUT=out [TRAIL_CROP=x,y,w,h] go test -run SatelliteTrail -v
func TestSatelliteTrail(t *testing.T) {
	dir := os.Getenv("TRAIL_DIR")
	if dir == "" {
		t.Skip("TRAIL_DIR not set")
	}
	out := os.Getenv("TRAIL_OUT")
	files, _ := filepath.Glob(filepath.Join(dir, "*.fit"))
	slices.Sort(files)
	var trail storedSub
	var clean []storedSub
	for _, f := range files {
		s := storedSub{key: f, exposure: 300, weight: 300}
		if strings.Contains(filepath.Base(f), os.Getenv("TRAIL_SUB")) {
			trail = s
		} else {
			clean = append(clean, s)
		}
	}
	if trail.key == "" {
		t.Fatal("trail sub not found")
	}
	load := func(_ int, s storedSub) ([]float32, int, int, error) { return readSub(s.key) }
	opts := DefaultOptions()
	t.Logf("trail sub %s, %d clean subs", filepath.Base(trail.key), len(clean))

	// The master without the trail sub is the truth to compare against.
	truth, err := streamStack(clean, 3, opts, load)
	if err != nil {
		t.Fatal(err)
	}
	w, h := truth.W, truth.H

	// Where the trail is: pixels of the trail sub far above the clean master.
	sub, _, _, err := readSub(trail.key)
	if err != nil {
		t.Fatal(err)
	}
	bg := float32(background(sub, opts.SaturationLevel) / trail.exposure)
	noise := float32(noiseLevel(sub, opts.SaturationLevel) / trail.exposure)
	mask := make([]bool, w*h)
	n := 0
	var excess float64
	for i, v := range sub {
		if v == 0 || truth.Weight[i] == 0 {
			continue
		}
		if r := v/float32(trail.exposure) - bg - truth.Mean[i]; r > 5*noise {
			mask[i] = true
			n++
			excess += float64(r)
		}
	}
	// Stars that differ with seeing also stand out; the trail is the line
	// most of the outliers fall on. Measure a band 3 px either side of it,
	// leaving out stars in the clean master.
	mask = trailBand(t, mask, truth, w, h)
	n = 0
	for _, m := range mask {
		if m {
			n++
		}
	}
	t.Logf("trail pixels: %d; the trail sub's noise per second %.3g", n, noise)

	// Background noise of a master, to express leftovers in σ.
	sigma := func(a *Accumulator) float64 {
		var d []float64
		for i := 0; i < len(a.Mean); i += 97 {
			if a.Weight[i] > 0 {
				d = append(d, float64(a.Mean[i]))
			}
		}
		slices.Sort(d)
		med := d[len(d)/2]
		for i := range d {
			d[i] = math.Abs(d[i] - med)
		}
		slices.Sort(d)
		return d[len(d)/2] * madToSigma
	}
	sig := sigma(truth)
	leftover := func(name string, a *Accumulator) {
		var sum float64
		var worst float64
		for i, m := range mask {
			if m {
				d := float64(a.Mean[i] - truth.Mean[i])
				sum += d
				worst = max(worst, d)
			}
		}
		t.Logf("%-44s mean leftover on the trail %+6.2fσ, worst pixel %+6.2fσ", name, sum/float64(n)/sig, worst/sig)
		if out != "" {
			writeCrop(t, filepath.Join(out, name+".png"), a, truth)
			writeDiff(t, filepath.Join(out, "diff-"+name+".png"), a, truth, sig)
		}
	}

	all := append(slices.Clone(clean), trail)

	plain, err := streamStack(all, 1, opts, load)
	if err != nil {
		t.Fatal(err)
	}
	leftover("1-plain-mean-no-rejection", plain)

	full, err := streamStack(all, 3, opts, load)
	if err != nil {
		t.Fatal(err)
	}
	leftover("2-full-rebuild", full)

	// The incremental path: the trail sub arrives after the others.
	inc, err := streamStack(clean, 3, opts, load)
	if err != nil {
		t.Fatal(err)
	}
	res, err := inc.Add(sub, trail.exposure, trail.weight, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("incremental add rejected %d pixels of the trail sub", res.Rejected)
	leftover("3-incremental-add", inc)

	// Warm-up: the trail sub is one of the first few.
	for _, k := range []int{2, 4, 9} {
		var mem []memSub
		for _, s := range append(slices.Clone(clean[:k]), trail) {
			p, _, _, err := readSub(s.key)
			if err != nil {
				t.Fatal(err)
			}
			mem = append(mem, toMemSub(p, s.exposure, s.weight, opts.SaturationLevel))
		}
		warm := medianAnchored(mem, w, h, opts)
		// Compare against the clean subs of this warm-up, not all of them.
		var cmem []memSub
		for _, s := range clean[:k] {
			p, _, _, _ := readSub(s.key)
			cmem = append(cmem, toMemSub(p, s.exposure, s.weight, opts.SaturationLevel))
		}
		wt := medianAnchored(cmem, w, h, opts)
		var sum, worst float64
		for i, m := range mask {
			if m {
				d := float64(warm.Mean[i] - wt.Mean[i])
				sum += d
				worst = max(worst, d)
			}
		}
		s := sigma(wt)
		name := fmt.Sprintf("4-warm-up-%d-subs", k+1)
		t.Logf("%-44s mean leftover on the trail %+6.2fσ, worst pixel %+6.2fσ", name, sum/float64(n)/s, worst/s)
		if out != "" {
			writeCrop(t, filepath.Join(out, name+".png"), warm, wt)
			writeDiff(t, filepath.Join(out, "diff-"+name+".png"), warm, wt, s)
		}
	}
	if out != "" {
		writeMask(t, filepath.Join(out, "0-trail-mask.png"), mask, w, h, 8)
		sa := NewAccumulator(w, h)
		if _, err := sa.Add(sub, trail.exposure, trail.weight, opts); err != nil {
			t.Fatal(err)
		}
		writeCrop(t, filepath.Join(out, "0-trail-sub.png"), sa, truth)
		writeCrop(t, filepath.Join(out, "0-master-without-trail-sub.png"), truth, truth)
	}
}

// trailBand fits a line to the outlier pixels by RANSAC and returns the
// pixels within 3 px of it that aren't stars in the clean master.
func trailBand(t *testing.T, mask []bool, truth *Accumulator, w, h int) []bool {
	var pts [][2]float64
	for i, m := range mask {
		if m {
			pts = append(pts, [2]float64{float64(i % w), float64(i / w)})
		}
	}
	best, bestN := [3]float64{}, 0
	seed := uint64(1)
	rnd := func() int {
		seed = seed*6364136223846793005 + 1442695040888963407
		return int(seed>>33) % len(pts)
	}
	for range 2000 {
		p, q := pts[rnd()], pts[rnd()]
		dx, dy := q[0]-p[0], q[1]-p[1]
		l := math.Hypot(dx, dy)
		if l < 500 {
			continue
		}
		// Line as a·x + b·y = c with (a, b) a unit normal.
		a, b := -dy/l, dx/l
		c := a*p[0] + b*p[1]
		n := 0
		for _, r := range pts {
			if math.Abs(a*r[0]+b*r[1]-c) < 3 {
				n++
			}
		}
		if n > bestN {
			best, bestN = [3]float64{a, b, c}, n
		}
	}
	t.Logf("trail line: %.4f·x + %.4f·y = %.1f, %d of %d outliers on it", best[0], best[1], best[2], bestN, len(pts))
	// Stars in the clean master: well above its background noise.
	var d []float64
	for i := 0; i < len(truth.Mean); i += 97 {
		d = append(d, float64(truth.Mean[i]))
	}
	slices.Sort(d)
	med := d[len(d)/2]
	for i := range d {
		d[i] = math.Abs(d[i] - med)
	}
	slices.Sort(d)
	star := float32(med + 10*d[len(d)/2]*madToSigma)
	out := make([]bool, len(mask))
	for y := range h {
		for x := range w {
			i := y*w + x
			if truth.Weight[i] > 0 && truth.Mean[i] < star && math.Abs(best[0]*float64(x)+best[1]*float64(y)-best[2]) < 3 {
				out[i] = true
			}
		}
	}
	return out
}

// writeCrop writes a crop of a master, stretched with the reference's
// statistics so every image is on the same scale.
func writeCrop(t *testing.T, name string, a, ref *Accumulator) {
	x0, y0, cw, ch := 0, 0, a.W, a.H
	bin := 8
	if c := os.Getenv("TRAIL_CROP"); c != "" {
		var v []int
		for _, s := range strings.Split(c, ",") {
			n, _ := strconv.Atoi(s)
			v = append(v, n)
		}
		x0, y0, cw, ch, bin = v[0], v[1], v[2], v[3], 1
	}
	var d []float64
	for i := 0; i < len(ref.Mean); i += 97 {
		if ref.Weight[i] > 0 {
			d = append(d, float64(ref.Mean[i]))
		}
	}
	slices.Sort(d)
	med := d[len(d)/2]
	for i := range d {
		d[i] = math.Abs(d[i] - med)
	}
	slices.Sort(d)
	sig := d[len(d)/2] * madToSigma
	lo, hi := med-2.8*sig, med+400*sig
	img := image.NewGray(image.Rect(0, 0, cw/bin, ch/bin))
	for y := 0; y < ch/bin; y++ {
		for x := 0; x < cw/bin; x++ {
			var s float64
			for dy := range bin {
				for dx := range bin {
					s += float64(a.Mean[(y0+y*bin+dy)*a.W+x0+x*bin+dx])
				}
			}
			v := (s/float64(bin*bin) - lo) / (hi - lo)
			img.SetGray(x, y, color.Gray{uint8(255 * MTF(0.02, min(max(v, 0), 1)))})
		}
	}
	f, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

// writeDiff writes a crop of a − ref, with ±3σ of the master's background
// noise spanning black to white.
func writeDiff(t *testing.T, name string, a, ref *Accumulator, sigma float64) {
	x0, y0, cw, ch := 0, 0, a.W, a.H
	if c := os.Getenv("TRAIL_CROP"); c != "" {
		var v []int
		for _, s := range strings.Split(c, ",") {
			n, _ := strconv.Atoi(s)
			v = append(v, n)
		}
		x0, y0, cw, ch = v[0], v[1], v[2], v[3]
	}
	img := image.NewGray(image.Rect(0, 0, cw, ch))
	for y := range ch {
		for x := range cw {
			i := (y0+y)*a.W + x0 + x
			v := float64(a.Mean[i]-ref.Mean[i])/sigma/6 + 0.5
			img.SetGray(x, y, color.Gray{uint8(255 * min(max(v, 0), 1))})
		}
	}
	f, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

func writeMask(t *testing.T, name string, mask []bool, w, h, bin int) {
	img := image.NewGray(image.Rect(0, 0, w/bin, h/bin))
	for i, m := range mask {
		if m && i%w/bin < w/bin && i/w/bin < h/bin {
			img.SetGray(i%w/bin, i/w/bin, color.Gray{255})
		}
	}
	f, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

// MTF is PixInsight's midtones transfer function.
func MTF(m, x float64) float64 {
	if x <= 0 || x >= 1 {
		return x
	}
	return (m - 1) * x / ((2*m-1)*x - m)
}
