package stacking

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/binary"
	"errors"
	"fmt"
	"maps"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// bgeScript runs GraXpert's background extraction model with onnxruntime.
//
//go:embed graxpert_bge.py
var bgeScript []byte

// BackgroundModel runs GraXpert's AI background extraction model
// (graxpert_bge.py, onnxruntime) on a cover's linear planes.
type BackgroundModel struct {
	// Python runs the embedded runner; Model is GraXpert's ONNX model.
	Python, Model string
	// Dir holds the runner's scratch files; empty for the system's.
	Dir string
}

// CoverBackground is the background model covers use, as the pod
// mounts it.
func CoverBackground() BackgroundModel {
	return BackgroundModel{Python: "python3", Model: "/usr/share/astro-stacker/graxpert-bge-1.0.1.onnx"}
}

// Ready reports why the model can't run, or nil.
func (m BackgroundModel) Ready() error {
	if _, err := exec.LookPath(m.Python); err != nil {
		return fmt.Errorf("background model: %w", err)
	}
	if _, err := os.Stat(m.Model); err != nil {
		return fmt.Errorf("background model: %w", err)
	}
	return nil
}

// backgroundFit models the smooth sky background of planes, w×h, fitted
// together (one, or three as a colour image), and returns it per plane.
// Planes have no empty pixels. With tiles, the model runs on a tiles×tiles
// grid of overlapping tiles a quarter of the frame across, so it follows
// structure four times finer.
type backgroundFit func(ctx context.Context, planes [][]float32, w, h, tiles int) ([][]float32, error)

// flatBackground is a background of each plane's median: nothing taken
// off, for pictures made where the model isn't installed.
func flatBackground(_ context.Context, planes [][]float32, _, _, _ int) ([][]float32, error) {
	out := make([][]float32, len(planes))
	for k, p := range planes {
		med, _ := statsNonZero(p)
		out[k] = make([]float32, len(p))
		for i := range out[k] {
			out[k][i] = float32(med)
		}
	}
	return out, nil
}

// Fit runs the model on planes together, as one colour image when there
// are three.
func (m BackgroundModel) Fit(ctx context.Context, planes [][]float32, w, h, tiles int) ([][]float32, error) {
	dir, err := os.MkdirTemp(m.Dir, "cover-bg-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	script, in, out := filepath.Join(dir, "graxpert_bge.py"), filepath.Join(dir, "in.npy"), filepath.Join(dir, "out.npy")
	if err := os.WriteFile(script, bgeScript, 0o600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(in, encodeNpy(planes, w, h), 0o600); err != nil {
		return nil, err
	}
	args := []string{"-I", script, m.Model, in, out}
	if tiles > 0 {
		args = append(args, strconv.Itoa(tiles))
	}
	cmd := exec.CommandContext(ctx, m.Python, args...)
	var log bytes.Buffer
	cmd.Stdout, cmd.Stderr = &log, &log
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("background model: %w: %s", err, strings.TrimSpace(log.String()))
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		return nil, err
	}
	return decodeNpy(raw, len(planes), w, h)
}

// encodeNpy writes planes as a float32 .npy array of h×w×len(planes),
// bottom row first as GraXpert reads a FITS file.
func encodeNpy(planes [][]float32, w, h int) []byte {
	c := len(planes)
	header := fmt.Sprintf("{'descr': '<f4', 'fortran_order': False, 'shape': (%d, %d, %d), }", h, w, c)
	header += strings.Repeat(" ", (64-(10+len(header)+1)%64)%64) + "\n"
	var b bytes.Buffer
	b.WriteString("\x93NUMPY\x01\x00")
	_ = binary.Write(&b, binary.LittleEndian, uint16(len(header)))
	b.WriteString(header)
	row := make([]byte, 4*w*c)
	for y := h - 1; y >= 0; y-- {
		for x := range w {
			for k, p := range planes {
				binary.LittleEndian.PutUint32(row[4*(x*c+k):], math.Float32bits(p[y*w+x]))
			}
		}
		b.Write(row)
	}
	return b.Bytes()
}

var npyShape = regexp.MustCompile(`'shape':\s*\((\d+),\s*(\d+),\s*(\d+)\)`)

// decodeNpy reads an h×w×c float32 .npy array, bottom row first, into c
// planes.
func decodeNpy(raw []byte, c, w, h int) ([][]float32, error) {
	if len(raw) < 10 || string(raw[:6]) != "\x93NUMPY" || raw[6] != 1 {
		return nil, errors.New("background model: not a version 1 .npy file")
	}
	n := int(binary.LittleEndian.Uint16(raw[8:10]))
	if len(raw) < 10+n {
		return nil, errors.New("background model: truncated .npy header")
	}
	header := string(raw[10 : 10+n])
	m := npyShape.FindStringSubmatch(header)
	if !strings.Contains(header, "'<f4'") || strings.Contains(header, "'fortran_order': True") || m == nil {
		return nil, fmt.Errorf("background model: unexpected array %q", strings.TrimSpace(header))
	}
	hh, _ := strconv.Atoi(m[1])
	ww, _ := strconv.Atoi(m[2])
	cc, _ := strconv.Atoi(m[3])
	data := raw[10+n:]
	if hh != h || ww != w || cc != c || len(data) < 4*w*h*c {
		return nil, fmt.Errorf("background model: got %d×%d×%d, want %d×%d×%d", hh, ww, cc, h, w, c)
	}
	out := make([][]float32, c)
	for k := range out {
		out[k] = make([]float32, w*h)
	}
	i := 0
	for y := h - 1; y >= 0; y-- {
		for x := range w {
			for k := range c {
				v := math.Float32frombits(binary.LittleEndian.Uint32(data[4*i:]))
				if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
					return nil, errors.New("background model: the background is not finite")
				}
				out[k][y*w+x] = v
				i++
			}
		}
	}
	return out, nil
}

// flatWork is about how many pixels across the sky's structure is
// measured on: each plane is binned to it first, so the noise and the
// thresholds mean the same whatever the preview's size.
const flatWork = 400

// flatGrid is how many blocks span the binned plane's longer side when
// the sky's structure is measured.
const flatGrid = 64

// flatObjectSigma is how far above the sky, in the sky's pixel noise, a
// block must sit in two thirds of the broadband channels, or in any
// narrowband one, to count as the target rather than sky.
const flatObjectSigma = 6.0

// flatGrowSigma is how far from the sky, in pixel noise, a block next to
// the target must sit, in the same channels, for the mask to grow into
// it: a galaxy's halo and a nebula's faint outskirts.
const flatGrowSigma = 1.5

// flatEdge is how many blocks from the frame's edge the mask may not grow
// into.
const flatEdge = 2

// FlatStructureMax is the most large-scale structure, in the sky's pixel
// noise, a channel's sky may keep after its background is taken off and
// still go into a cover. Measured on real masters once the model's
// background was off: clean skies 0.15 to 0.45, star clouds and patchy
// cloud bands up to 0.8, nebulae filling the frame up to 2; a red master
// shot through moonlit cloud with sharp-edged bands 7.6.
const FlatStructureMax = 3.0

// fineTiles is how many tiles across and down the finer pass of the
// model runs on.
const fineTiles = 7

// fineMin and fineRatio pick the channels the finer pass is tried on:
// structure left past fineMin and past fineRatio times the median of the
// subject's other channels. Bands from cloud that crossed one filter's
// subs are that channel's own, while a star cloud or a nebula shows in all.
const (
	fineMin   = 0.5
	fineRatio = 1.6
)

// fineObjectChange is the most the finer pass may change the target's
// flux, as a share of it, to be kept: it follows the bands, but on a
// galaxy's halo or a nebula that fills its tiles it takes off the target
// too.
const fineObjectChange = 0.05

// flatReport is what taking one channel's background off measured.
type flatReport struct {
	// Before and After are the sky's large-scale structure, in its pixel
	// noise, as the channel came and once the model's background is off.
	Before, After float64
	// Object is the share of the frame masked as the target.
	Object float64
	// Fine is set when the finer pass was kept; FineChange is how much it
	// changed the target's flux, as a share of it, when it was tried.
	Fine       bool
	FineChange float64
}

// Usable reports whether the channel's sky came out flat enough to show.
func (r flatReport) Usable() bool { return r.After <= FlatStructureMax }

// narrowband reports whether a filter shuts out most moonlight and cloud
// glow, so its structure is the target's.
func narrowband(f string) bool { return f == filterHa || f == filterOIII || f == filterSII }

// flattenPlanes takes each plane's smooth sky background off with fit,
// as GraXpert does, and returns new planes and what each measured. The
// planes of each of groups, three distinct filters, are fitted together
// as one colour image; the others each on its own. Planes must share w×h;
// pixels empty (0) stay empty and are filled with the plane's median for
// the fit. The background less its median over the covered pixels is
// taken off, so the sky keeps its level.
func flattenPlanes(ctx context.Context, fit backgroundFit, planes map[string]*linearImage, groups [][]string, w, h int) (map[string]*linearImage, map[string]flatReport, error) {
	names := make([]string, 0, len(planes))
	for f := range planes {
		names = append(names, f)
	}
	slices.Sort(names)
	var runs [][]string
	done := map[string]bool{}
	for _, g := range groups {
		ok := len(g) == 3 && g[0] != g[1] && g[1] != g[2] && g[0] != g[2]
		for _, f := range g {
			if planes[f] == nil || done[f] {
				ok = false
			}
		}
		if ok {
			runs = append(runs, g)
			for _, f := range g {
				done[f] = true
			}
		}
	}
	for _, f := range names {
		if !done[f] {
			runs = append(runs, []string{f})
		}
	}
	out := map[string]*linearImage{}
	var mu sync.Mutex
	errs := make([]error, len(runs))
	var wg sync.WaitGroup
	for i, run := range runs {
		wg.Go(func() {
			in := make([][]float32, len(run))
			for k, f := range run {
				in[k] = filledPlane(planes[f].Data)
			}
			bg, err := fit(ctx, in, w, h, 0)
			if err != nil {
				errs[i] = err
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for k, f := range run {
				out[f] = &linearImage{W: w, H: h, Data: subtractBackground(planes[f].Data, bg[k])}
			}
		})
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return nil, nil, err
	}
	reps, sky := measureFlat(planes, out, names, w, h)
	for _, f := range names {
		var others []float64
		for _, g := range names {
			if g != f {
				others = append(others, reps[g].After)
			}
		}
		if len(others) < 2 {
			continue
		}
		slices.Sort(others)
		r := reps[f]
		if r.After <= fineMin || r.After <= fineRatio*others[len(others)/2] {
			continue
		}
		bg, err := fit(ctx, [][]float32{filledPlane(out[f].Data)}, w, h, fineTiles)
		if err != nil {
			return nil, nil, err
		}
		fine := &linearImage{W: w, H: h, Data: subtractBackground(out[f].Data, bg[0])}
		r.FineChange = sky.objectChange(out[f].Data, fine.Data)
		trial := maps.Clone(out)
		trial[f] = fine
		after, _ := measureFlat(planes, trial, names, w, h)
		if r.FineChange <= fineObjectChange && after[f].After < r.After {
			out[f] = fine
			r.After, r.Fine = after[f].After, true
		}
		reps[f] = r
	}
	return out, reps, nil
}

// filledPlane is p with its empty pixels set to its median.
func filledPlane(p []float32) []float32 {
	med, _ := statsNonZero(p)
	out := slices.Clone(p)
	for i, v := range out {
		if v == 0 || math.IsNaN(float64(v)) {
			out[i] = float32(med)
		}
	}
	return out
}

// subtractBackground is p less bg plus bg's median over p's covered
// pixels; empty pixels stay empty.
func subtractBackground(p, bg []float32) []float32 {
	var cov []float64
	stride := max(1, len(p)/200_000)
	for i := 0; i < len(p); i += stride {
		if p[i] != 0 {
			cov = append(cov, float64(bg[i]))
		}
	}
	level := float32(0)
	if len(cov) > 0 {
		slices.Sort(cov)
		level = float32(cov[len(cov)/2])
	}
	out := make([]float32, len(p))
	for i, v := range p {
		if v == 0 {
			continue
		}
		nv := v - bg[i] + level
		if nv == 0 {
			nv = math.SmallestNonzeroFloat32
		}
		out[i] = nv
	}
	return out
}

// coverSky is where measureFlat found the target: blocks of s×s pixels of
// planes binned k×k, gw×gh of them.
type coverSky struct {
	object       []bool
	k, s, gw, gh int
	w, h         int
}

// objectChange is how much b differs from a over the target, as a share of
// a's flux there above its sky.
func (sky coverSky) objectChange(a, b []float32) float64 {
	var skyVals []float64
	var diff, flux float64
	n := 0
	for y := range sky.h {
		for x := range sky.w {
			i := y*sky.w + x
			if a[i] == 0 {
				continue
			}
			bx, by := x/sky.k/sky.s, y/sky.k/sky.s
			if bx >= sky.gw || by >= sky.gh || !sky.object[by*sky.gw+bx] {
				if (x+y)%7 == 0 {
					skyVals = append(skyVals, float64(a[i]))
				}
				continue
			}
			diff += float64(a[i] - b[i])
			flux += float64(a[i])
			n++
		}
	}
	if n == 0 || len(skyVals) == 0 {
		return 0
	}
	slices.Sort(skyVals)
	flux -= float64(n) * skyVals[len(skyVals)/2]
	if flux <= 0 {
		return math.Inf(1)
	}
	return math.Abs(diff) / flux
}

// measureFlat measures each plane's sky structure as it came and once
// flattened, over the sky blocks: those not masked as the target in the
// flattened planes together (objectMask).
func measureFlat(before, after map[string]*linearImage, names []string, w, h int) (map[string]flatReport, coverSky) {
	k := max(1, int(math.Round(float64(max(w, h))/flatWork)))
	ww, wh := w/k, h/k
	s := max(2, int(math.Ceil(float64(max(ww, wh))/flatGrid)))
	type grid struct {
		blocks, quad []float64
		pix          float64
	}
	var gw, gh int
	measure := func(p []float32) grid {
		work := binMean(p, w, h, k)
		var g grid
		g.blocks, gw, gh = blockMedians(work, ww, wh, s)
		g.quad, _ = skySurface(g.blocks, gw, gh)
		g.pix = float64(pixelNoise(work, float32(math.Inf(1))))
		return g
	}
	pre, post := map[string]grid{}, map[string]grid{}
	for _, f := range names {
		pre[f], post[f] = measure(before[f].Data), measure(after[f].Data)
	}
	object := objectMask(names, func(f string) ([]float64, []float64, float64) {
		g := post[f]
		return g.blocks, g.quad, g.pix
	}, gw, gh)
	covered, masked := 0, 0
	for i, m := range object {
		if !math.IsNaN(post[names[0]].blocks[i]) {
			covered++
			if m {
				masked++
			}
		}
	}
	share := 0.0
	if covered > 0 {
		share = float64(masked) / float64(covered)
	}
	out := map[string]flatReport{}
	for _, f := range names {
		out[f] = flatReport{
			Before: structure(pre[f].blocks, pre[f].quad, object, gw, gh, pre[f].pix),
			After:  structure(post[f].blocks, post[f].quad, object, gw, gh, post[f].pix),
			Object: share,
		}
	}
	return out, coverSky{object: object, k: k, s: s, gw: gw, gh: gh, w: w, h: h}
}

// binMean bins p, w×h, by k×k, means of blocks with every pixel covered;
// others empty (0).
func binMean(p []float32, w, h, k int) []float32 {
	if k == 1 {
		return p
	}
	bw, bh := w/k, h/k
	out := make([]float32, bw*bh)
	for by := range bh {
		for bx := range bw {
			var sum float32
			ok := true
			for y := by * k; y < (by+1)*k && ok; y++ {
				for x := bx * k; x < (bx+1)*k; x++ {
					v := p[y*w+x]
					if v == 0 {
						ok = false
						break
					}
					sum += v
				}
			}
			if ok {
				out[by*bw+bx] = sum / float32(k*k)
			}
		}
	}
	return out
}
