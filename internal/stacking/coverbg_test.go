package stacking

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// gradPlane reads a testdata/covergrad channel: a linear preview of a real
// master, binned 4×4, its float32 mantissas cut to 10 bits.
func gradPlane(t *testing.T, name string) *linearImage {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "covergrad", name+".f32.zst"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := zstd.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	var size [2]uint32
	if err := binary.Read(zr, binary.LittleEndian, &size); err != nil {
		t.Fatal(err)
	}
	img := &linearImage{W: int(size[0]), H: int(size[1]), Data: make([]float32, int(size[0])*int(size[1]))}
	if err := binary.Read(zr, binary.LittleEndian, img.Data); err != nil {
		t.Fatal(err)
	}
	return img
}

func gradPlanes(t *testing.T, object string, filters ...string) (map[string]*linearImage, int, int) {
	t.Helper()
	planes := map[string]*linearImage{}
	w, h := math.MaxInt, math.MaxInt
	for _, f := range filters {
		planes[f] = gradPlane(t, object+"_"+f)
		w, h = min(w, planes[f].W), min(h, planes[f].H)
	}
	for f, p := range planes {
		planes[f] = &linearImage{W: w, H: h, Data: crop(p.Data, p.W, Rect{0, 0, w, h})}
	}
	return planes, w, h
}

// apertureFlux is the flux within r of (cx, cy) over the median of the
// annulus from 1.5r to 2r.
func apertureFlux(p []float32, w, h int, cx, cy, r float64) float64 {
	var sum float64
	n := 0
	var ring []float64
	for y := range h {
		for x := range w {
			d := math.Hypot(float64(x)-cx, float64(y)-cy)
			v := float64(p[y*w+x])
			switch {
			case d <= r:
				sum += v
				n++
			case d >= 1.5*r && d <= 2*r && v != 0:
				ring = append(ring, v)
			}
		}
	}
	slices.Sort(ring)
	return sum - float64(n)*ring[len(ring)/2]
}

// brightest is the centre of the brightest box×box mean of p.
func brightest(p []float32, w, h, box int) (float64, float64) {
	best, bx, by := math.Inf(-1), 0, 0
	for y := 0; y+box <= h; y += 2 {
		for x := 0; x+box <= w; x += 2 {
			var s float64
			for yy := y; yy < y+box; yy++ {
				for xx := x; xx < x+box; xx++ {
					s += float64(p[yy*w+xx])
				}
			}
			if s > best {
				best, bx, by = s, x, y
			}
		}
	}
	return float64(bx) + float64(box)/2, float64(by) + float64(box)/2
}

// recordedFit is the background model for tests: GraXpert's model run
// live when BGE_PYTHON and BGE_MODEL name a Python with onnxruntime and
// the model, and its answers recorded to testdata/coverbg with
// BGE_RECORD=1; otherwise the recorded answers. A run is found by the
// planes' size, the tiles, and each plane's median and mean to a few
// figures, so the last bits of float arithmetic, which differ between
// machines, don't matter.
func recordedFit(t *testing.T, name string) backgroundFit {
	t.Helper()
	live := BackgroundModel{Python: os.Getenv("BGE_PYTHON"), Model: os.Getenv("BGE_MODEL"), Dir: t.TempDir()}
	record := os.Getenv("BGE_RECORD") == "1"
	if live.Python != "" && live.Ready() != nil {
		t.Fatalf("BGE_PYTHON and BGE_MODEL set but not usable: %v", live.Ready())
	}
	var mu sync.Mutex
	return func(ctx context.Context, planes [][]float32, w, h, tiles int) ([][]float32, error) {
		prefix := fmt.Sprintf("%s-%dx%dx%d-t%d", name, w, h, len(planes), tiles)
		sig := make([]float64, 0, 2*len(planes))
		key := prefix
		for _, p := range planes {
			med, _ := statsNonZero(p)
			var sum float64
			for _, v := range p {
				sum += float64(v)
			}
			sig = append(sig, med, sum/float64(len(p)))
			key += fmt.Sprintf("_%.5g_%.4g", med, sum/float64(len(p)))
		}
		file := filepath.Join("testdata", "coverbg", key+".f32.zst")
		if live.Python == "" {
			file = nearestRecording(prefix, sig, file)
			raw, err := os.ReadFile(file)
			if err != nil {
				return nil, fmt.Errorf("no recorded background %s: %w", file, err)
			}
			zr, err := zstd.NewReader(bytes.NewReader(raw))
			if err != nil {
				return nil, err
			}
			defer zr.Close()
			out := make([][]float32, len(planes))
			for k := range out {
				out[k] = make([]float32, w*h)
				if err := binary.Read(zr, binary.LittleEndian, out[k]); err != nil {
					return nil, err
				}
			}
			return out, nil
		}
		out, err := live.Fit(ctx, planes, w, h, tiles)
		if err != nil || !record {
			return out, err
		}
		var raw bytes.Buffer
		zw, err := zstd.NewWriter(&raw, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
		if err != nil {
			return nil, err
		}
		for _, p := range out {
			for i, v := range p {
				p[i] = math.Float32frombits(math.Float32bits(v) &^ (1<<11 - 1))
			}
			if err := binary.Write(zw, binary.LittleEndian, p); err != nil {
				return nil, err
			}
		}
		if err := zw.Close(); err != nil {
			return nil, err
		}
		mu.Lock()
		defer mu.Unlock()
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			return nil, err
		}
		return out, os.WriteFile(file, raw.Bytes(), 0o600)
	}
}

// nearestRecording is the recording of prefix whose medians and means are
// within 0.1% of sig, nearest first, or file when none is.
func nearestRecording(prefix string, sig []float64, file string) string {
	names, _ := filepath.Glob(filepath.Join("testdata", "coverbg", prefix+"_*.f32.zst"))
	best, bestD := file, 1e-3
	for _, n := range names {
		parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(filepath.Base(n), prefix+"_"), ".f32.zst"), "_")
		if len(parts) != len(sig) {
			continue
		}
		d := 0.0
		for i, p := range parts {
			v, err := strconv.ParseFloat(p, 64)
			if err != nil {
				d = math.Inf(1)
				break
			}
			d = max(d, math.Abs(v-sig[i])/max(math.Abs(sig[i]), 1e-12))
		}
		if d < bestD {
			best, bestD = n, d
		}
	}
	return best
}

func TestNpyRoundTrip(t *testing.T) {
	t.Parallel()
	const w, h = 5, 3
	planes := make([][]float32, 3)
	for k := range planes {
		planes[k] = make([]float32, w*h)
		for i := range planes[k] {
			planes[k][i] = float32(k*100 + i)
		}
	}
	raw := encodeNpy(planes, w, h)
	n := int(binary.LittleEndian.Uint16(raw[8:10]))
	if (10+n)%64 != 0 || raw[10+n-1] != '\n' {
		t.Fatalf("header of %d bytes is not padded to 64", n)
	}
	first := math.Float32frombits(binary.LittleEndian.Uint32(raw[10+n:]))
	if first != planes[0][(h-1)*w] {
		t.Errorf("first sample %v, want the bottom row's first, %v", first, planes[0][(h-1)*w])
	}
	got, err := decodeNpy(raw, 3, w, h)
	if err != nil {
		t.Fatal(err)
	}
	for k := range planes {
		if !slices.Equal(got[k], planes[k]) {
			t.Errorf("plane %d: got %v, want %v", k, got[k], planes[k])
		}
	}
	if _, err := decodeNpy(raw, 1, w, h); err == nil {
		t.Error("decoded three channels as one")
	}
	planes[1][4] = float32(math.NaN())
	if _, err := decodeNpy(encodeNpy(planes, w, h), 3, w, h); err == nil {
		t.Error("decoded a background that is not finite")
	}
}

// A cover frame places points only on a cover of the palette it was
// worked out from; the luminance doesn't change the frame.
func TestCoverFrameShows(t *testing.T) {
	t.Parallel()
	f := CoverFrame{Filters: colourFilters(palette{R: filterRed, G: filterGreen, B: filterBlue, HaRed: true, Lum: true}.filters())}
	for name, want := range map[string]bool{"RGB+Ha": true, "LRGB+Ha": true, "RGB": false, "HOO": false, "": false} {
		if got := f.Shows(name); got != want {
			t.Errorf("%q: %v, want %v", name, got, want)
		}
	}
}

// bandedField is sky 1000 with pixel noise 10 and a galaxy at (120, 110)
// in every channel, and moonlit bands of 3σ that differ from channel to
// channel; back holds each channel's sky and bands.
type bandedField struct {
	w, h         int
	planes       map[string]*linearImage
	truth, back  map[string][]float32
	galaxy, band func(x, y, c int) float64
}

func newBandedField(bandsIn []string) bandedField {
	const w, h, sky, noise = 360, 240, 1000.0, 10.0
	f := bandedField{w: w, h: h, planes: map[string]*linearImage{}, truth: map[string][]float32{}, back: map[string][]float32{}}
	f.galaxy = func(x, y, _ int) float64 {
		dx, dy := (float64(x)-120)/8, (float64(y)-110)/24
		return 400 * math.Exp(-(dx*dx+dy*dy)/2)
	}
	f.band = func(x, y, c int) float64 {
		return 3 * noise * math.Sin(2*math.Pi*(float64(x)+2*float64(y))/90+float64(c)*1.7)
	}
	rng := rand.New(rand.NewPCG(1, 2))
	for c, name := range []string{filterRed, filterGreen, filterBlue, filterLuminance} {
		p, tr, bk := make([]float32, w*h), make([]float32, w*h), make([]float32, w*h)
		for y := range h {
			for x := range w {
				band := 0.0
				if slices.Contains(bandsIn, name) {
					band = f.band(x, y, c)
				}
				g := f.galaxy(x, y, c)
				tr[y*w+x] = float32(sky + g)
				bk[y*w+x] = float32(sky + band)
				p[y*w+x] = float32((sky + g + band + noise*rng.NormFloat64()) / 65535)
			}
		}
		for i := range tr {
			tr[i] /= 65535
			bk[i] /= 65535
		}
		f.planes[name], f.truth[name], f.back[name] = &linearImage{W: w, H: h, Data: p}, tr, bk
	}
	return f
}

// The coarse model's runs: the three colour channels fitted together and
// the luminance on its own, with no empty pixel handed to it; empty pixels
// stay empty, the sky keeps its level and the galaxy its flux.
func TestFlattenPlanesFitsGroupsAndKeepsLevel(t *testing.T) {
	t.Parallel()
	f := newBandedField(nil)
	f.planes[filterRed].Data[0] = 0
	var mu sync.Mutex
	var runs []int
	fit := func(_ context.Context, planes [][]float32, w, _, tiles int) ([][]float32, error) {
		mu.Lock()
		runs = append(runs, len(planes)*10+tiles)
		mu.Unlock()
		out := make([][]float32, len(planes))
		for k, p := range planes {
			if slices.Contains(p, 0) {
				t.Errorf("an empty pixel went to the model")
			}
			med, _ := statsNonZero(p)
			out[k] = make([]float32, len(p))
			for i := range p {
				out[k][i] = float32(med) + float32(i%w)*1e-6
			}
		}
		return out, nil
	}
	flat, reps, err := flattenPlanes(t.Context(), fit, f.planes, [][]string{{filterRed, filterGreen, filterBlue}}, f.w, f.h)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(runs)
	if !slices.Equal(runs, []int{10, 30}) {
		t.Errorf("runs %v, want one of three planes and one of one", runs)
	}
	if flat[filterRed].Data[0] != 0 {
		t.Error("an empty pixel was filled")
	}
	for name, p := range flat {
		before, _ := statsNonZero(f.planes[name].Data)
		after, _ := statsNonZero(p.Data)
		if math.Abs(after/before-1) > 0.001 {
			t.Errorf("%s: sky %.6g after, %.6g before", name, after, before)
		}
		want := apertureFlux(f.truth[name], f.w, f.h, 120, 110, 50)
		got := apertureFlux(p.Data, f.w, f.h, 120, 110, 50)
		if math.Abs(got/want-1) > 0.02 {
			t.Errorf("%s: galaxy flux %.4g, want %.4g", name, got, want)
		}
		if reps[name].Fine {
			t.Errorf("%s: finer pass on a field without bands", name)
		}
	}
}

// Bands in red alone are red's own, so the finer pass is tried on red and
// kept when it leaves the galaxy alone; when it takes the galaxy's flux
// too, it is not kept.
func TestFinerPassOnlyWhereItSparesTheTarget(t *testing.T) {
	t.Parallel()
	for _, eats := range []bool{false, true} {
		t.Run(fmt.Sprintf("eats %v", eats), func(t *testing.T) {
			t.Parallel()
			f := newBandedField([]string{filterRed})
			fit := func(_ context.Context, planes [][]float32, w, _, tiles int) ([][]float32, error) {
				out := make([][]float32, len(planes))
				for k, p := range planes {
					med, _ := statsNonZero(p)
					out[k] = make([]float32, len(p))
					for i := range p {
						out[k][i] = float32(med)
						if tiles == 0 {
							continue
						}
						x, y := i%w, i/w
						out[k][i] += float32(f.band(x, y, 0) / 65535)
						if eats {
							out[k][i] += float32(0.3 * f.galaxy(x, y, 0) / 65535)
						}
					}
				}
				return out, nil
			}
			flat, reps, err := flattenPlanes(t.Context(), fit, f.planes, nil, f.w, f.h)
			if err != nil {
				t.Fatal(err)
			}
			r := reps[filterRed]
			if r.Before < 1 {
				t.Fatalf("red structure %.3f before; the field has no bands", r.Before)
			}
			if r.Fine == eats {
				t.Fatalf("fine %v with change %.3f: want %v", r.Fine, r.FineChange, !eats)
			}
			for _, g := range []string{filterGreen, filterBlue, filterLuminance} {
				if reps[g].Fine || reps[g].FineChange != 0 {
					t.Errorf("%s: finer pass tried on a channel without bands of its own", g)
				}
			}
			want := apertureFlux(f.truth[filterRed], f.w, f.h, 120, 110, 50)
			if eats {
				want = apertureFlux(f.planes[filterRed].Data, f.w, f.h, 120, 110, 50)
			}
			got := apertureFlux(flat[filterRed].Data, f.w, f.h, 120, 110, 50)
			if math.Abs(got/want-1) > 0.03 {
				t.Errorf("galaxy flux %.4g, want %.4g", got, want)
			}
			if !eats && r.After > 0.5*r.Before {
				t.Errorf("structure %.3f after the finer pass, %.3f before", r.After, r.Before)
			}
		})
	}
}

// A channel whose sky stays banded past FlatStructureMax is left out and
// the cover falls back to a single filter; with no usable channel at all
// the cover waits.
func TestComposeSubjectDropsBandedChannels(t *testing.T) {
	t.Parallel()
	f := newBandedField([]string{filterRed, filterGreen, filterBlue, filterLuminance})
	for c, name := range []string{filterRed, filterGreen, filterBlue, filterLuminance} {
		p := f.planes[name]
		for i := range p.Data {
			x, y := i%f.w, i/f.w
			p.Data[i] += float32(5 * f.band(x/3, y/3, c) / 65535)
		}
	}
	have := map[string]layer{}
	for name := range f.planes {
		have[name] = layer{Key: name, Effective: 3600}
	}
	read := func(k string) (*linearImage, error) {
		p := f.planes[k]
		return &linearImage{W: p.W, H: p.H, Data: slices.Clone(p.Data)}, nil
	}
	res, err := composeSubject(t.Context(), flatBackground, "Test Galaxy", have, read, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Palette != "" || res.Filter != "" || res.JPEG != nil {
		t.Fatalf("palette %q filter %q, %+v: want the cover to wait", res.Palette, res.Filter, res.Flat)
	}
	for name, r := range res.Flat {
		if r.Usable() {
			t.Errorf("%s: structure %.3f counted usable", name, r.After)
		}
	}
	delete(have, filterRed)
	clean := newBandedField(nil)
	f.planes[filterLuminance] = clean.planes[filterLuminance]
	res, err = composeSubject(t.Context(), flatBackground, "Test Galaxy", have, read, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Filter != filterLuminance || res.JPEG == nil {
		t.Errorf("palette %q filter %q: want a luminance cover", res.Palette, res.Filter)
	}
}

// starField adds n stars of Gaussian profile at random places to p, w×h,
// their fluxes a power law over 50 to 20000.
func starField(rng *rand.Rand, p []float64, w, h, n int) {
	const sigma = 1.3
	for range n {
		cx, cy := rng.Float64()*float64(w), rng.Float64()*float64(h)
		flux := min(20000, 50*math.Pow(rng.Float64()+1e-6, -1.5))
		peak := flux / (2 * math.Pi * sigma * sigma)
		for y := max(0, int(cy)-5); y < min(h, int(cy)+6); y++ {
			for x := max(0, int(cx)-5); x < min(w, int(cx)+6); x++ {
				dx, dy := float64(x)-cx, float64(y)-cy
				p[y*w+x] += peak * math.Exp(-(dx*dx+dy*dy)/(2*sigma*sigma))
			}
		}
	}
}

// contrast is how far the middle third of p stands above its corners.
func contrast(p []float32, w, h int) float64 {
	box := func(x0, y0 int) float64 {
		var v []float64
		for y := y0; y < y0+h/3; y++ {
			for x := x0; x < x0+w/3; x++ {
				v = append(v, float64(p[y*w+x]))
			}
		}
		slices.Sort(v)
		return v[len(v)/2]
	}
	corners := []float64{box(0, 0), box(w-w/3, 0), box(0, h-h/3), box(w-w/3, h-h/3)}
	slices.Sort(corners)
	return box(w/3, h/3) - (corners[1]+corners[2])/2
}

// removedBump is how much of the target in the middle third of after was
// taken off with the background, as a share of how far it stands above
// the frame's border: the removed surface (before less after) under the
// middle against a quadratic through it over the outer eighth of the
// frame.
func removedBump(before, after []float32, w, h int) float64 {
	const s = 8
	rem := make([]float32, len(before))
	for i := range rem {
		if before[i] != 0 {
			rem[i] = before[i] - after[i] + 1
		}
	}
	g, gw, gh := blockMedians(rem, w, h, s)
	border := make([]bool, len(g))
	for i := range g {
		x, y := i%gw, i/gw
		border[i] = x < gw/8 || x >= gw-gw/8 || y < gh/8 || y >= gh-gh/8
	}
	quad, _ := skySurfaceAmong(g, border, gw, gh)
	ag, _, _ := blockMedians(after, w, h, s)
	var mid, edge, out []float64
	for i := range g {
		x, y := i%gw, i/gw
		switch {
		case math.IsNaN(g[i]):
		case x >= gw/3 && x < 2*gw/3 && y >= gh/3 && y < 2*gh/3:
			mid = append(mid, g[i]-quad[i])
			out = append(out, ag[i])
		case border[i]:
			edge = append(edge, ag[i])
		}
	}
	median := func(v []float64) float64 {
		slices.Sort(v)
		return v[len(v)/2]
	}
	return median(mid) / (median(out) - median(edge))
}

// GraXpert's model on synthetic star fields, each channel fitted on its
// own as for a single-filter cover: a gradient and broad moonlit bands
// that differ from channel to channel come off a field with a galaxy,
// which keeps its flux; a patchy nebula filling most of the frame, over a
// gradient, keeps its flux and its patches.
func TestBackgroundModelSynthetic(t *testing.T) {
	t.Parallel()
	const w, h = 780, 520
	t.Run("bands", func(t *testing.T) {
		t.Parallel()
		const sky, noise = 1000.0, 10.0
		stars := make([]float64, w*h)
		starField(rand.New(rand.NewPCG(5, 6)), stars, w, h, 1500)
		galaxy := func(x, y int) float64 {
			dx, dy := float64(x)-260, float64(y)-300
			a, b := 0.8*dx+0.6*dy, -0.6*dx+0.8*dy
			r := math.Hypot(a/18, b/7)
			return 2000*math.Exp(-r) + 3000*math.Exp(-math.Hypot(a, b)/3)
		}
		rng := rand.New(rand.NewPCG(7, 8))
		planes, truth := map[string]*linearImage{}, map[string][]float32{}
		for c, name := range []string{filterRed, filterGreen, filterBlue, filterLuminance} {
			p, tr := make([]float32, w*h), make([]float32, w*h)
			ang := 0.7 * float64(c)
			for y := range h {
				for x := range w {
					grad := 0.06 * (math.Cos(ang)*float64(x) + math.Sin(ang)*float64(y))
					band := 4 * noise * math.Sin(2*math.Pi*(float64(x)+2*float64(y))/420+1.9*float64(c))
					obj := stars[y*w+x] + galaxy(x, y)
					tr[y*w+x] = float32((sky + obj) / 65535)
					p[y*w+x] = float32((sky + obj + grad + band + noise*rng.NormFloat64()) / 65535)
				}
			}
			planes[name], truth[name] = &linearImage{W: w, H: h, Data: p}, tr
		}
		flat, reps, err := flattenPlanes(t.Context(), recordedFit(t, "bands"), planes, nil, w, h)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%+v", reps)
		for name, r := range reps {
			if r.After > 0.3*r.Before || !r.Usable() {
				t.Errorf("%s: structure %.3f after, %.3f before: want under 30%% of it and usable", name, r.After, r.Before)
			}
			want := apertureFlux(truth[name], w, h, 260, 300, 40)
			got := apertureFlux(flat[name].Data, w, h, 260, 300, 40)
			if math.Abs(got/want-1) > 0.05 {
				t.Errorf("%s: galaxy flux %.4g, want %.4g", name, got, want)
			}
		}
	})
	t.Run("nebula", func(t *testing.T) {
		t.Parallel()
		const sky, noise = 500.0, 5.0
		rng := rand.New(rand.NewPCG(3, 4))
		field := make([]float32, w*h)
		for _, scale := range []float64{4, 12, 36} {
			white := make([]float32, w*h)
			for i := range white {
				white[i] = float32(rng.NormFloat64())
			}
			sm := gaussPlane(white, w, h, scale)
			_, sd := statsNonZero(sm)
			for i := range field {
				field[i] += sm[i] / float32(sd)
			}
		}
		neb := make([]float64, w*h)
		for y := range h {
			for x := range w {
				dx, dy := (float64(x)-390)/230, (float64(y)-260)/160
				neb[y*w+x] = 150 * math.Exp(-(dx*dx+dy*dy)/2) * math.Exp(0.6*float64(field[y*w+x]))
			}
		}
		stars := make([]float64, w*h)
		starField(rand.New(rand.NewPCG(9, 10)), stars, w, h, 2500)
		planes := map[string]*linearImage{}
		nebOnly := map[string][]float32{}
		for c, name := range []string{filterHa, filterOIII} {
			p, n := make([]float32, w*h), make([]float32, w*h)
			for y := range h {
				for x := range w {
					i := y*w + x
					grad := 0.04 * float64(x) * float64(1-2*c)
					n[i] = float32(neb[i] / 65535)
					p[i] = float32((sky + grad + neb[i]/float64(1+c) + stars[i] + noise*rng.NormFloat64()) / 65535)
				}
			}
			planes[name], nebOnly[name] = &linearImage{W: w, H: h, Data: p}, n
		}
		flat, reps, err := flattenPlanes(t.Context(), recordedFit(t, "nebula"), planes, nil, w, h)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%+v", reps)
		for c, name := range []string{filterHa, filterOIII} {
			r := reps[name]
			if !r.Usable() || r.Fine {
				t.Errorf("%s: usable %v, fine %v", name, r.Usable(), r.Fine)
			}
			want := contrast(nebOnly[name], w, h) / float64(1+c)
			got := contrast(flat[name].Data, w, h)
			if math.Abs(got/want-1) > 0.15 {
				t.Errorf("%s: nebula stands %.4g above the corners, want %.4g", name, got, want)
			}
		}
	})
}

// GraXpert's model on real masters, binned 4×4. M 15's red came from
// three subs on a night of patchy cloud and drew diagonal bands of its
// own: the finer pass takes them off and the cluster keeps its flux.
// Sculptor Galaxy's red, from one night under a bright Moon low in the
// sky, keeps sharp-edged bands and is left out, while H-a and luminance
// stay and the galaxy keeps its flux. The Rosette Nebula keeps most of its
// nebula: the model takes some of its middle with the sky, 24% in the
// binned luminance and 13% in the full-size preview's.
func TestBackgroundModelRealMasters(t *testing.T) {
	t.Parallel()
	all := []string{filterRed, filterGreen, filterBlue, filterLuminance, filterHa}
	rgb := [][]string{{filterRed, filterGreen, filterBlue}}
	t.Run("M 15", func(t *testing.T) {
		t.Parallel()
		planes, w, h := gradPlanes(t, "M_15", all...)
		flat, reps, err := flattenPlanes(t.Context(), recordedFit(t, "M_15"), planes, rgb, w, h)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%+v", reps)
		for _, f := range all {
			r := reps[f]
			if !r.Usable() {
				t.Errorf("%s: structure %.3f after, want usable", f, r.After)
			}
		}
		if r := reps[filterRed]; r.Before < 1 || r.After > 0.25*r.Before {
			t.Errorf("red: structure %.3f before, %.3f after: want banded before and a quarter of it after", r.Before, r.After)
		}
		cx, cy := brightest(planes[filterLuminance].Data, w, h, 9)
		for _, f := range all {
			before := apertureFlux(planes[f].Data, w, h, cx, cy, 12)
			after := apertureFlux(flat[f].Data, w, h, cx, cy, 12)
			if math.Abs(after/before-1) > 0.03 {
				t.Errorf("%s: cluster flux %.4g after, %.4g before", f, after, before)
			}
		}
	})
	t.Run("Sculptor Galaxy", func(t *testing.T) {
		t.Parallel()
		planes, w, h := gradPlanes(t, "Sculptor_Galaxy", all...)
		flat, reps, err := flattenPlanes(t.Context(), recordedFit(t, "Sculptor_Galaxy"), planes, rgb, w, h)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%+v", reps)
		if r := reps[filterRed]; r.Usable() || r.Fine {
			t.Errorf("red: structure %.3f after, fine %v: want over %v and no finer pass", r.After, r.Fine, FlatStructureMax)
		}
		cx, cy := brightest(planes[filterHa].Data, w, h, 9)
		for _, f := range []string{filterHa, filterLuminance} {
			if !reps[f].Usable() {
				t.Errorf("%s: structure %.3f after, want usable", f, reps[f].After)
			}
			before := apertureFlux(planes[f].Data, w, h, cx, cy, 25)
			after := apertureFlux(flat[f].Data, w, h, cx, cy, 25)
			if math.Abs(after/before-1) > 0.03 {
				t.Errorf("%s: galaxy flux %.4g after, %.4g before", f, after, before)
			}
		}
	})
	t.Run("Rosette Nebula", func(t *testing.T) {
		t.Parallel()
		planes, w, h := gradPlanes(t, "Rosette_Nebula", filterRed, filterGreen, filterBlue, filterLuminance)
		flat, reps, err := flattenPlanes(t.Context(), recordedFit(t, "Rosette_Nebula"), planes, rgb, w, h)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%+v", reps)
		for f, r := range reps {
			if !r.Usable() || r.Fine {
				t.Errorf("%s: usable %v, fine %v", f, r.Usable(), r.Fine)
			}
			if b := removedBump(planes[f].Data, flat[f].Data, w, h); math.Abs(b) > 0.25 {
				t.Errorf("%s: %.0f%% of the nebula went with the background", f, 100*b)
			}
		}
	})
}
