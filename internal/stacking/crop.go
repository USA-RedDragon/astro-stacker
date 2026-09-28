package stacking

// Rect is a crop in full-resolution pixels.
type Rect struct{ X, Y, W, H int }

// cropBlock is the block size crops are computed on.
const cropBlock = 8

// coverageCrop is the largest rectangle whose blocks have nearly every
// pixel covered by at least half the subs of the best-covered pixel.
// Registration leaves dithered edges covered by few subs, well below half;
// a band a quarter of the subs missed, as from another rotator angle, stays
// in, where requiring 80% cropped Andromeda to 40% of its frame. Rejected
// star pixels and trails are too few to disqualify a block.
func coverageCrop(acc *Accumulator) Rect {
	var most float32
	for _, c := range acc.Count {
		most = max(most, c)
	}
	return largestRect(acc.W, acc.H, func(sum float64, n int) float64 { return sum / float64(n) },
		func(i int) float64 {
			if acc.Count[i] >= most/2 {
				return 1
			}
			return 0
		}, 0.95)
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
// keeps those at least share of the best block, and returns the largest
// rectangle of kept blocks in pixels. It returns the whole image when
// nothing qualifies.
func largestRect(w, h int, agg func(float64, int) float64, value func(int) float64, share float64) Rect {
	bw, bh := w/cropBlock, h/cropBlock
	if bw == 0 || bh == 0 {
		return Rect{0, 0, w, h}
	}
	scores := make([]float64, bw*bh)
	best := 0.0
	for by := range bh {
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
