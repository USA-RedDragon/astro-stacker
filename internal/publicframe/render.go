package publicframe

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"math"
	"math/rand/v2"
	"slices"

	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
	"github.com/USA-RedDragon/astro-stacker/internal/preview"
)

// Options shape the public frame.
type Options struct {
	Width, Height int
	Quality       int
	// Amplitude is the watermark's peak in units of the frame's sky noise,
	// measured in the stretched 8-bit frame; 0 leaves it out.
	Amplitude float64
	// StructureRatio is the least sky noise a frame is shown with, as a
	// multiple of the clutter its master's structure makes at the
	// letters' scale; frames smoother than that get fresh noise. 0 adds
	// none. See Render.
	StructureRatio float64
	// Seed seeds the added noise; 0 seeds it at random, as it must be in
	// use: the noise must differ in every frame, so a thief's stack
	// averages it away, and must not be predictable.
	Seed uint64
}

// DefaultOptions: the size wheresmyscope shows, lossy, a watermark at half
// the frame's sky noise (one frame doesn't show it; a stack of 25-100 of a
// noise-limited field does), and noise added to frames whose master's
// structure would hide the watermark from a stack (StructureRatio).
func DefaultOptions() Options {
	return Options{Width: 800, Height: 480, Quality: 60, Amplitude: 0.5, StructureRatio: defaultStructureRatio}
}

// defaultStructureRatio is the smallest at which "astro.garden" read by eye
// in a 100-frame stack of Orion H-a subs (H-a 120/600 s, 2025-12 to
// 2026-10): at 8 the text barely showed, at 10 it was borderline. Their noise
// is 1-11× their clutter (2.7 on average); Leo Triplet RGB 300 s subs,
// noise limited, are 15-28×, so the rule leaves them alone.
const defaultStructureRatio = 12

// Rect is a region of the reference grid, in pixels.
type Rect struct{ X, Y, W, H int }

// Master is a target's master for the sub's filter, binned: Scale
// reference pixels per pixel. Its linear preview is.
type Master struct {
	Plane []float32
	W, H  int
	Scale float64
}

// Input is one registered sub to render.
type Input struct {
	// Sub is the registered light: on its target's reference grid.
	Sub *imagedata.Image
	// Crop is the part of the reference grid the master's subs all fill
	// (the stack's crop), which keeps registration's empty borders out;
	// zero for the whole frame.
	Crop Rect
	// Pattern and Mask are the watermark and its sky weight on the
	// reference grid; a nil Mask weights it 1 everywhere.
	Pattern *Pattern
	Mask    *SkyMask
	// Master is the sub's master, for its structure; nil (a target's
	// first sub) adds no noise.
	Master *Master
}

// Result is a rendered frame.
type Result struct {
	JPEG []byte
	// Sigma is the sky noise of the stretched frame before quantizing, in
	// DN; Amplitude is the watermark's peak added, in DN.
	Sigma, Amplitude float64
	// Linear sky noise of the sub, the master's structure clutter at the
	// letters' scale in the same units, and the noise added (all on the
	// output grid, before the stretch).
	NoiseLinear, Clutter, Added float64
	// Region is the part of the reference grid shown.
	Region Rect
	// Weight is the watermark's shape on the output grid (pattern × sky
	// mask, 0-1) and Sky the mask alone, for checking it.
	Weight, Sky []float32
}

// The letters' scale on the output grid, for measuring structure: stars
// (a few pixels across) are taken out by an opening of radius starRadius,
// then a band-pass of box blurs this wide (radius, applied twice) keeps
// strokes and letters (about 15-80 px) and drops pixel noise and
// gradients. Stars don't hide the letters (the eye reads past them, and
// the sky mask leaves the bright ones out); extended nebulosity does.
const (
	starRadius = 3
	bandFine   = 4
	bandCoarse = 24
)

// Render makes the public frame of a registered sub: the largest centred
// region of the crop with the output's aspect, area-averaged down to the
// output size, auto-stretched the way the previews are, watermarked in the
// stretched 8-bit domain and encoded as a JPEG with no metadata.
//
// The watermark sits at Amplitude × the frame's sky noise, so one frame
// hides it; a thief's stack averages the noise down and shows it. Where the
// sky holds structure (nebulosity) that is coherent from frame to frame,
// the stack doesn't average that down, and a watermark fainter than it
// never reads. So the master's structure at the letters' scale is measured
// (the master binned to the output grid and scaled to the sub by a fit on
// the sky), and a sub whose own noise is under StructureRatio × that gets
// fresh Gaussian noise to make it up, in linear units before the stretch:
// the frame looks like a shorter exposure, the auto-stretch compresses the
// nebula's contrast with it, and the watermark, tied to the noise, grows
// with it. Noise-limited fields fall under the ratio and are untouched.
func Render(in Input, opts Options) (Result, error) {
	im := in.Sub
	if im == nil || im.W <= 0 || im.H <= 0 {
		return Result{}, fmt.Errorf("no image")
	}
	if opts.Width <= 0 || opts.Height <= 0 {
		return Result{}, fmt.Errorf("output is %dx%d", opts.Width, opts.Height)
	}
	ow, oh := opts.Width, opts.Height
	region := fitRegion(clampCrop(in.Crop, im.W, im.H), ow, oh)
	plane := resample(im.Plane(0), im.W, im.H, float64(region.X), float64(region.Y), float64(region.W), float64(region.H), ow, oh)

	// The watermark and sky weight at each output pixel's centre on the
	// reference grid.
	weight := make([]float32, len(plane))
	sky := make([]float32, len(plane))
	sx := float64(region.W) / float64(ow)
	sy := float64(region.H) / float64(oh)
	for v := range oh {
		y := float64(region.Y) + (float64(v)+0.5)*sy
		for u := range ow {
			x := float64(region.X) + (float64(u)+0.5)*sx
			m := in.Mask.At(x, y)
			sky[v*ow+u] = float32(m)
			if in.Pattern != nil {
				weight[v*ow+u] = float32(in.Pattern.At(x, y) * m)
			}
		}
	}

	res := Result{Region: region, Weight: weight, Sky: sky}
	res.NoiseLinear = skySigma(plane, sky, ow, oh, false)
	if in.Master != nil && opts.StructureRatio > 0 {
		ms := in.Master.Scale
		mp := resample(in.Master.Plane, in.Master.W, in.Master.H,
			float64(region.X)/ms, float64(region.Y)/ms, float64(region.W)/ms, float64(region.H)/ms, ow, oh)
		res.Clutter = clutter(plane, mp, sky, ow, oh)
		if need := opts.StructureRatio * res.Clutter; need > res.NoiseLinear {
			res.Added = math.Sqrt(need*need - res.NoiseLinear*res.NoiseLinear)
			seed := opts.Seed
			if seed == 0 {
				seed = rand.Uint64()
			}
			rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
			for i := range plane {
				plane[i] += float32(rng.NormFloat64() * res.Added)
			}
		}
	}

	c0, m := preview.StretchParams(plane)
	stretched := preview.StretchWith(plane, c0, m)
	dn := make([]float32, len(stretched))
	for i, v := range stretched {
		dn[i] = 255 * v
	}
	res.Sigma = skySigma(dn, sky, ow, oh, true)
	res.Amplitude = opts.Amplitude * res.Sigma

	gray := image.NewGray(image.Rect(0, 0, ow, oh))
	for i, v := range dn {
		gray.Pix[i] = uint8(math.Round(math.Max(0, math.Min(255, float64(v)+res.Amplitude*float64(weight[i])))))
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, gray, &jpeg.Options{Quality: opts.Quality}); err != nil {
		return Result{}, err
	}
	res.JPEG = buf.Bytes()
	return res, nil
}

// clutter is the master's structure at the letters' scale, in the sub's
// linear units: the robust spread over the sky of the master's band-pass,
// scaled by a least-squares fit of the sub's band-pass on it. The sub's
// noise doesn't bias the fit (the master is all but noiseless); a sub with
// no structure the master shares fits a scale near 0, and gets none.
func clutter(sub, master, sky []float32, w, h int) float64 {
	bs, bm := bandPass(sub, w, h), bandPass(master, w, h)
	var sxy, sxx float64
	vals := make([]float32, 0, len(bm))
	for i, s := range sky {
		if s < 0.9 {
			continue
		}
		sxy += float64(bs[i]) * float64(bm[i])
		sxx += float64(bm[i]) * float64(bm[i])
		vals = append(vals, bm[i])
	}
	if sxx == 0 || len(vals) < 100 {
		return 0
	}
	scale := sxy / sxx
	if scale <= 0 {
		return 0
	}
	return scale * robustSpread(vals)
}

func bandPass(p []float32, w, h int) []float32 {
	p = localMax(localMin(p, w, h, starRadius), w, h, starRadius)
	fine := boxBlur(boxBlur(p, w, h, bandFine), w, h, bandFine)
	coarse := boxBlur(boxBlur(p, w, h, bandCoarse), w, h, bandCoarse)
	for i := range fine {
		fine[i] -= coarse[i]
	}
	return fine
}

// robustSpread is the MAD of v scaled to a Gaussian σ.
func robustSpread(v []float32) float64 {
	s := slices.Clone(v)
	slices.Sort(s)
	med := s[len(s)/2]
	for i := range s {
		s[i] = float32(math.Abs(float64(s[i] - med)))
	}
	slices.Sort(s)
	return 1.4826 * float64(s[len(s)/2])
}

func clampCrop(c Rect, w, h int) Rect {
	if c.W <= 0 || c.H <= 0 {
		return Rect{0, 0, w, h}
	}
	x0, y0 := max(0, c.X), max(0, c.Y)
	x1, y1 := min(w, c.X+c.W), min(h, c.Y+c.H)
	if x1 <= x0 || y1 <= y0 {
		return Rect{0, 0, w, h}
	}
	return Rect{x0, y0, x1 - x0, y1 - y0}
}

// fitRegion is the largest region of c with the aspect ow:oh, centred.
func fitRegion(c Rect, ow, oh int) Rect {
	w, h := c.W, c.H
	if w*oh > h*ow {
		w = h * ow / oh
	} else {
		h = w * oh / ow
	}
	return Rect{c.X + (c.W-w)/2, c.Y + (c.H-h)/2, w, h}
}

// span is one source pixel's share of an output pixel.
type span struct {
	i int
	w float32
}

// areaWeights maps the source span [off, off+length) of a line n pixels
// long onto m output pixels, each the mean of the source it covers,
// partial pixels weighted by overlap and the line's ends clamped.
func areaWeights(off, length float64, n, m int) [][]span {
	out := make([][]span, m)
	scale := length / float64(m)
	for j := range m {
		a, b := off+float64(j)*scale, off+float64(j+1)*scale
		var total float64
		for i := int(math.Floor(a)); float64(i) < b; i++ {
			lo, hi := math.Max(a, float64(i)), math.Min(b, float64(i+1))
			if hi <= lo {
				continue
			}
			out[j] = append(out[j], span{min(max(i, 0), n-1), float32(hi - lo)})
			total += hi - lo
		}
		for k := range out[j] {
			out[j][k].w /= float32(total)
		}
	}
	return out
}

// resample area-averages the region (x, y, rw, rh), in pixels of a plane
// w×h, to ow×oh.
func resample(p []float32, w, h int, x, y, rw, rh float64, ow, oh int) []float32 {
	cols := areaWeights(x, rw, w, ow)
	rows := areaWeights(y, rh, h, oh)
	y0, y1 := h, 0
	for _, ws := range rows {
		for _, c := range ws {
			y0, y1 = min(y0, c.i), max(y1, c.i)
		}
	}
	tmp := make([]float32, ow*(y1-y0+1))
	for yy := y0; yy <= y1; yy++ {
		src := p[yy*w:]
		for u, ws := range cols {
			var s float32
			for _, c := range ws {
				s += c.w * src[c.i]
			}
			tmp[(yy-y0)*ow+u] = s
		}
	}
	out := make([]float32, ow*oh)
	for v, ws := range rows {
		for _, c := range ws {
			row := tmp[(c.i-y0)*ow:]
			for u := range ow {
				out[v*ow+u] += c.w * row[u]
			}
		}
	}
	return out
}

// skySigma is the noise of the sky: the MAD of horizontal neighbour
// differences, which nebula and gradients barely change, over pixels the
// sky mask leaves open (and, stretched, that weren't clipped to black).
func skySigma(p, sky []float32, w, h int, stretched bool) float64 {
	var d []float32
	for y := range h {
		for x := range w - 1 {
			i := y*w + x
			if stretched && (p[i] <= 0 || p[i+1] <= 0) {
				continue
			}
			if sky[i] < 0.9 || sky[i+1] < 0.9 {
				continue
			}
			d = append(d, float32(math.Abs(float64(p[i+1]-p[i]))))
		}
	}
	if len(d) == 0 {
		return 0
	}
	slices.Sort(d)
	return 1.4826 * float64(d[len(d)/2]) / math.Sqrt2
}
