package stacking

import (
	"context"
	"log/slog"
	"path"

	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
	"github.com/USA-RedDragon/astro-stacker/internal/preview"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/minio/minio-go/v7"
)

// CropVersion changes with the crop rule, so masters cropped by an older
// rule get their crop and preview redone.
const CropVersion = 2

// Rect is a crop in full-resolution pixels.
type Rect struct{ X, Y, W, H int }

// cropBlock is the block size crops are computed on.
const cropBlock = 8

// coverageCrop trims the ragged, nearly empty borders registration leaves:
// it is the largest rectangle of blocks where some pixel has at least a
// tenth of the best-covered pixel's subs (and at least one), less one block
// each side for blocks the edge crosses. It does not crop to where most subs
// overlap: a master whose subs were framed differently keeps its whole frame
// rather than being cut to the overlap, and pixels where stars were
// rejected, dense in masters of few subs, don't count against a block.
func coverageCrop(acc *Accumulator) Rect {
	var most float32
	for _, c := range acc.Count {
		most = max(most, c)
	}
	need := max(1, most/10)
	r := largestRect(acc.W, acc.H, func(sum float64, _ int) float64 { return sum },
		func(i int) float64 { return float64(acc.Count[i]) }, 0, blockMaxAtLeast(acc, need))
	return shrink(r, cropBlock, acc.W, acc.H)
}

// blockMaxAtLeast keeps blocks where some pixel's count reaches need.
func blockMaxAtLeast(acc *Accumulator, need float32) func(bx, by int) bool {
	return func(bx, by int) bool {
		for y := by * cropBlock; y < (by+1)*cropBlock; y++ {
			for x := bx * cropBlock; x < (bx+1)*cropBlock; x++ {
				if acc.Count[y*acc.W+x] >= need {
					return true
				}
			}
		}
		return false
	}
}

// shrink insets r by d on each side it doesn't share with the image edge.
func shrink(r Rect, d, w, h int) Rect {
	out := r
	if r.X > 0 {
		out.X += d
		out.W -= d
	}
	if r.Y > 0 {
		out.Y += d
		out.H -= d
	}
	if r.X+r.W < w {
		out.W -= d
	}
	if r.Y+r.H < h {
		out.H -= d
	}
	if out.W <= 0 || out.H <= 0 {
		return r
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
	if err := p.db.WithContext(ctx).Where("state_key IS NOT NULL AND crop_version < ?", CropVersion).
		Find(&stacks).Error; err != nil {
		slog.Warn("Could not find masters to recrop", "error", err)
		return
	}
	objects := map[string]bool{}
	for i := range stacks {
		if ctx.Err() != nil {
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
	jpg, err := preview.Render(&imagedata.Image{W: r.W, H: r.H, C: 1, Data: crop(master, acc.W, r)}, preview.DefaultOptions)
	if err != nil {
		return err
	}
	key := path.Join(stackPrefix(s), "preview.jpg")
	if err := p.putBytes(ctx, key, jpg, minio.PutObjectOptions{ContentType: "image/jpeg"}); err != nil {
		return err
	}
	return p.db.WithContext(ctx).Model(s).Updates(map[string]any{
		"crop_x": r.X, "crop_y": r.Y, "crop_w": r.W, "crop_h": r.H, "crop_version": CropVersion,
		"preview_key": key,
	}).Error
}
