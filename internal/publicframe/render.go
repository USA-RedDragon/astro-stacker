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
	// NoiseFloor is the least sky noise a frame is shown with, in DN:
	// fresh noise is added to frames smoother than that. See Render.
	NoiseFloor float64
	// Seed seeds the added noise; 0 seeds it at random, as it must be in
	// use (a thief who could predict it could subtract it).
	Seed uint64
}

// DefaultOptions: the size wheresmyscope shows, lossy, and a watermark at
// half the frame's sky noise before JPEG (JPEG smooths the noise, so it is
// about 0.6σ of the frame as served). In a field where the noise dominates
// (Leo Triplet's 300 s subs) a stack of 25-100 frames shows it and one
// frame doesn't; 0.3σ didn't read by 100. No added noise by default.
var DefaultOptions = Options{Width: 800, Height: 480, Quality: 60, Amplitude: 0.5}

// Rect is a region of the reference grid, in pixels.
type Rect struct{ X, Y, W, H int }

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
}

// Result is a rendered frame.
type Result struct {
	JPEG []byte
	// Sigma is the sky noise of the stretched frame before quantizing, in
	// DN; Amplitude is the watermark's peak added, in DN.
	Sigma, Amplitude float64
	// Region is the part of the reference grid shown.
	Region Rect
	// Weight is the watermark's shape on the output grid (pattern × sky
	// mask, 0-1) and Sky the mask alone, for checking it.
	Weight, Sky []float32
}

// Render makes the public frame of a registered sub: the largest centred
// region of the crop with the output's aspect, area-averaged down to the
// output size, auto-stretched the way the previews are, watermarked in the
// stretched 8-bit domain and encoded as a JPEG with no metadata.
func Render(in Input, opts Options) (Result, error) {
	im := in.Sub
	if im == nil || im.W <= 0 || im.H <= 0 {
		return Result{}, fmt.Errorf("no image")
	}
	if opts.Width <= 0 || opts.Height <= 0 {
		return Result{}, fmt.Errorf("output is %dx%d", opts.Width, opts.Height)
	}
	region := fitRegion(clampCrop(in.Crop, im.W, im.H), opts.Width, opts.Height)
	plane := resample(im.Plane(0), im.W, region, opts.Width, opts.Height)
	stretched := preview.Stretch(plane)
	dn := make([]float32, len(stretched))
	for i, v := range stretched {
		dn[i] = 255 * v
	}

	// Each output pixel takes the watermark and sky weight at its centre on
	// the reference grid.
	weight := make([]float32, len(dn))
	sky := make([]float32, len(dn))
	sx := float64(region.W) / float64(opts.Width)
	sy := float64(region.H) / float64(opts.Height)
	for v := range opts.Height {
		y := float64(region.Y) + (float64(v)+0.5)*sy
		for u := range opts.Width {
			x := float64(region.X) + (float64(u)+0.5)*sx
			m := in.Mask.At(x, y)
			sky[v*opts.Width+u] = float32(m)
			if in.Pattern != nil {
				weight[v*opts.Width+u] = float32(in.Pattern.At(x, y) * m)
			}
		}
	}
	sigma := skySigma(dn, sky, opts.Width, opts.Height)
	// A long sub of a bright field, binned this far, is limited by its
	// structure, not its noise: a stack of many is hardly better than one,
	// and a watermark faint enough to hide in one frame never stands out
	// of the stack either. Topping its noise up to the floor, fresh in
	// every frame, gives the watermark noise to hide in that a stack
	// averages away (and leaves a thief less from each frame).
	if add := opts.NoiseFloor*opts.NoiseFloor - sigma*sigma; add > 0 {
		seed := opts.Seed
		if seed == 0 {
			seed = rand.Uint64()
		}
		rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
		sd := math.Sqrt(add)
		for i := range dn {
			dn[i] += float32(rng.NormFloat64() * sd)
		}
		sigma = opts.NoiseFloor
	}
	amp := opts.Amplitude * sigma

	gray := image.NewGray(image.Rect(0, 0, opts.Width, opts.Height))
	for i, v := range dn {
		gray.Pix[i] = uint8(math.Round(math.Max(0, math.Min(255, float64(v)+amp*float64(weight[i])))))
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, gray, &jpeg.Options{Quality: opts.Quality}); err != nil {
		return Result{}, err
	}
	return Result{JPEG: buf.Bytes(), Sigma: sigma, Amplitude: amp, Region: region, Weight: weight, Sky: sky}, nil
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

// areaWeights maps n source pixels from off onto m output pixels, each the
// mean of the source pixels it covers, partial ones weighted by overlap.
func areaWeights(off, n, m int) [][]span {
	out := make([][]span, m)
	scale := float64(n) / float64(m)
	for j := range m {
		a, b := float64(j)*scale, float64(j+1)*scale
		for i := int(a); i < int(math.Ceil(b)) && i < n; i++ {
			lo, hi := math.Max(a, float64(i)), math.Min(b, float64(i+1))
			if hi > lo {
				out[j] = append(out[j], span{off + i, float32((hi - lo) / scale)})
			}
		}
	}
	return out
}

// resample area-averages region r of a plane w pixels wide to ow×oh.
func resample(p []float32, w int, r Rect, ow, oh int) []float32 {
	cols := areaWeights(r.X, r.W, ow)
	rows := areaWeights(r.Y, r.H, oh)
	tmp := make([]float32, ow*r.H)
	for y := range r.H {
		src := p[(r.Y+y)*w:]
		for u, ws := range cols {
			var s float32
			for _, c := range ws {
				s += c.w * src[c.i]
			}
			tmp[y*ow+u] = s
		}
	}
	out := make([]float32, ow*oh)
	for v, ws := range rows {
		for _, c := range ws {
			row := tmp[(c.i-r.Y)*ow:]
			for u := range ow {
				out[v*ow+u] += c.w * row[u]
			}
		}
	}
	return out
}

// skySigma is the noise of the sky in DN: the MAD of horizontal neighbour
// differences, which nebula and gradients barely change, over pixels the
// sky mask leaves open and the stretch didn't clip to black.
func skySigma(dn, sky []float32, w, h int) float64 {
	var d []float32
	for y := range h {
		for x := range w - 1 {
			i := y*w + x
			if dn[i] <= 0 || dn[i+1] <= 0 || sky[i] < 0.9 || sky[i+1] < 0.9 {
				continue
			}
			d = append(d, float32(math.Abs(float64(dn[i+1]-dn[i]))))
		}
	}
	if len(d) == 0 {
		return 0
	}
	slices.Sort(d)
	return 1.4826 * float64(d[len(d)/2]) / math.Sqrt2
}
