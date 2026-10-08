package stacking

import (
	"context"
	"log/slog"
	"path"
	"slices"

	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
	"github.com/USA-RedDragon/astro-stacker/internal/preview"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/minio/minio-go/v7"
)

// CropVersion changes with the crop rule, so masters cropped by an older
// rule get their crop and preview redone.
const CropVersion = 5

// Rect is a crop in full-resolution pixels.
type Rect struct{ X, Y, W, H int }

// cropBlock is the block size crops are computed on.
const cropBlock = 8

// coverageCrop crops to where (nearly) every sub of the master's framing
// overlaps, as PixInsight's WBPP autocrop does for a single framing: the
// largest rectangle whose pixels are all covered by at least 95% of the
// subs that cover most of the frame. That drops the empty borders
// registration leaves and the weak edges only some subs reach. Subs from
// another framing (a target re-centred on some nights) add depth where
// they overlap but don't shrink the crop: the count to reach is the one
// most of the frame has, not the peak where both framings overlap.
//
// Coverage is each sub's footprint, not the per-pixel count: counts drop
// where stars, trails and hot pixels were rejected, which would read as
// holes. A frame's edges are straight and rejections small, so a closing
// (local maximum, then local minimum, over rejectionFill pixels) fills them
// while leaving the edges where they are.
func coverageCrop(acc *Accumulator) Rect {
	cover := localExtreme(localExtreme(acc.Count, acc.W, acc.H, rejectionFill, true), acc.W, acc.H, rejectionFill, false)
	need := 0.95 * typicalCoverage(cover)
	low := make([]bool, len(cover))
	for i, c := range cover {
		low[i] = c < need || c == 0
	}
	outside := fromBorder(low, acc.W, acc.H)
	return largestRect(acc.W, acc.H, func(sum float64, _ int) float64 { return sum }, nil, 0,
		func(bx, by int) bool {
			for y := by * cropBlock; y < (by+1)*cropBlock; y++ {
				for x := bx * cropBlock; x < (bx+1)*cropBlock; x++ {
					if outside[y*acc.W+x] {
						return false
					}
				}
			}
			return true
		})
}

// fromBorder marks the low pixels connected to the image's edge: those are
// the frame's ragged or weak edges. Low pixels enclosed by covered ones are
// holes inside the frame, where saturated galaxy cores and bright stars
// were left out of every sub, and don't limit a crop.
func fromBorder(low []bool, w, h int) []bool {
	out := make([]bool, len(low))
	stack := make([]int, 0, 2*(w+h))
	push := func(i int) {
		if low[i] && !out[i] {
			out[i] = true
			stack = append(stack, i)
		}
	}
	for x := range w {
		push(x)
		push((h-1)*w + x)
	}
	for y := range h {
		push(y * w)
		push(y*w + w - 1)
	}
	for len(stack) > 0 {
		i := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		x, y := i%w, i/w
		if x > 0 {
			push(i - 1)
		}
		if x < w-1 {
			push(i + 1)
		}
		if y > 0 {
			push(i - w)
		}
		if y < h-1 {
			push(i + w)
		}
	}
	return out
}

// typicalCoverage is the coverage most of the frame has: the 20th
// percentile of covered pixels, below any overlap of framings (which covers
// only part of the frame) and above the thin dithered edges.
func typicalCoverage(cover []float32) float32 {
	var s []float32
	for i := 0; i < len(cover); i += 13 {
		if cover[i] > 0 {
			s = append(s, cover[i])
		}
	}
	if len(s) == 0 {
		return 0
	}
	slices.Sort(s)
	return s[len(s)/5]
}

// rejectionFill is the width of the local maximum that fills rejected
// pixels: wider than a grown satellite trail or a bright star's core.
const rejectionFill = 25

// localExtreme is a w×h image's maximum (or minimum) over size×size
// windows, computed separably. For the minimum, outside the image counts
// as 0, uncovered, so a closing can't fill an empty border back in.
func localExtreme(src []float32, w, h, size int, maximum bool) []float32 {
	pick := func(a, b float32) float32 {
		if maximum {
			return max(a, b)
		}
		return min(a, b)
	}
	r := size / 2
	tmp := make([]float32, len(src))
	for y := range h {
		row := src[y*w : (y+1)*w]
		for x := range w {
			m := row[x]
			if !maximum && (x-r < 0 || x+r > w-1) {
				m = 0
			}
			for k := max(0, x-r); k <= min(w-1, x+r); k++ {
				m = pick(m, row[k])
			}
			tmp[y*w+x] = m
		}
	}
	out := make([]float32, len(src))
	for x := range w {
		for y := range h {
			m := tmp[y*w+x]
			if !maximum && (y-r < 0 || y+r > h-1) {
				m = 0
			}
			for k := max(0, y-r); k <= min(h-1, y+r); k++ {
				m = pick(m, tmp[k*w+x])
			}
			out[y*w+x] = m
		}
	}
	return out
}

// dataCrop is the largest rectangle of blocks with image data in nearly
// every pixel, for mosaics, whose canvas is empty outside the panels.
func dataCrop(w, h int, data []float32) Rect {
	return largestRect(w, h, func(sum float64, n int) float64 { return sum / float64(n) },
		func(i int) float64 {
			if data[i] != 0 {
				return 1
			}
			return 0
		}, 0.98)
}

// largestRect scores cropBlock-sized blocks with value (averaged by agg),
// or with keepBlock when given, keeps those at least share of the best block, and returns the largest
// rectangle of kept blocks in pixels. It returns the whole image when
// nothing qualifies.
func largestRect(w, h int, agg func(float64, int) float64, value func(int) float64, share float64,
	keepBlock ...func(bx, by int) bool) Rect {
	bw, bh := w/cropBlock, h/cropBlock
	if bw == 0 || bh == 0 {
		return Rect{0, 0, w, h}
	}
	scores := make([]float64, bw*bh)
	best := 0.0
	if len(keepBlock) > 0 {
		// The caller decides per block; score kept blocks 1.
		for by := range bh {
			for bx := range bw {
				if keepBlock[0](bx, by) {
					scores[by*bw+bx] = 1
				}
			}
		}
		best, share = 1, 1
	}
	for by := range bh {
		if len(keepBlock) > 0 {
			break
		}
		for bx := range bw {
			var sum float64
			for y := by * cropBlock; y < (by+1)*cropBlock; y++ {
				for x := bx * cropBlock; x < (bx+1)*cropBlock; x++ {
					sum += value(y*w + x)
				}
			}
			s := agg(sum, cropBlock*cropBlock)
			scores[by*bw+bx] = s
			best = max(best, s)
		}
	}
	if best <= 0 {
		return Rect{0, 0, w, h}
	}
	// Largest rectangle of kept blocks: per row, the height of the kept
	// run ending there, then the largest rectangle under that histogram.
	heights := make([]int, bw)
	var r Rect
	area := 0
	stack := make([]int, 0, bw+1)
	for by := range bh {
		for bx := range bw {
			if scores[by*bw+bx] >= share*best {
				heights[bx]++
			} else {
				heights[bx] = 0
			}
		}
		stack = stack[:0]
		for bx := 0; bx <= bw; bx++ {
			cur := 0
			if bx < bw {
				cur = heights[bx]
			}
			for len(stack) > 0 && heights[stack[len(stack)-1]] >= cur {
				top := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				left := 0
				if len(stack) > 0 {
					left = stack[len(stack)-1] + 1
				}
				ht := heights[top]
				if a := ht * (bx - left); a > area {
					area = a
					r = Rect{X: left, Y: by - ht + 1, W: bx - left, H: ht}
				}
			}
			stack = append(stack, bx)
		}
	}
	if area == 0 {
		return Rect{0, 0, w, h}
	}
	return Rect{X: r.X * cropBlock, Y: r.Y * cropBlock, W: r.W * cropBlock, H: r.H * cropBlock}
}

// crop copies rect out of a single-channel image.
func crop(data []float32, w int, r Rect) []float32 {
	out := make([]float32, 0, r.W*r.H)
	for y := r.Y; y < r.Y+r.H; y++ {
		out = append(out, data[y*w+r.X:y*w+r.X+r.W]...)
	}
	return out
}

// recropMasters redoes the crop and preview of masters cropped by an older
// rule, from their saved state, and their targets' covers.
func (p *Pipeline) recropMasters(ctx context.Context) {
	var stacks []app.Stack
	if err := p.db.WithContext(ctx).Where("state_key IS NOT NULL AND (crop_version IS NULL OR crop_version < ?)", CropVersion).
		Find(&stacks).Error; err != nil {
		slog.Warn("Could not find masters to recrop", "error", err)
		return
	}
	objects := map[string]bool{}
	for i := range stacks {
		if p.stopping(ctx) {
			return
		}
		s := &stacks[i]
		if err := p.recrop(ctx, s); err != nil {
			slog.Warn("Could not recrop master", "object", s.Object, "filter", s.Filter, "error", err)
			continue
		}
		objects[s.Object] = true
	}
	for o := range objects {
		p.refreshCover(ctx, o)
	}
	if len(stacks) > 0 {
		slog.Info("Recropped masters", "masters", len(stacks), "targets", len(objects))
	}
}

func (p *Pipeline) recrop(ctx context.Context, s *app.Stack) error {
	acc, err := p.loadState(ctx, s)
	if err != nil {
		return err
	}
	r := coverageCrop(acc)
	master := acc.Master(s.ScaleExposure)
	jpg, err := preview.Render(&imagedata.Image{W: r.W, H: r.H, C: 1, Data: crop(master, acc.W, r)}, preview.DefaultOptions())
	if err != nil {
		return err
	}
	key := path.Join(stackPrefix(s), "preview.jpg")
	if err := p.putBytes(ctx, key, jpg, minio.PutObjectOptions{ContentType: contentTypeJPEG}); err != nil {
		return err
	}
	return p.db.WithContext(ctx).Model(s).Updates(map[string]any{
		"crop_x": r.X, "crop_y": r.Y, "crop_w": r.W, "crop_h": r.H, "crop_version": CropVersion,
		"preview_key": key,
	}).Error
}
