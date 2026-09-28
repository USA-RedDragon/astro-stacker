package stacking

import "testing"

func TestCoverageCropTrimsOnlyNearlyEmptyEdges(t *testing.T) {
	const w, h = 160, 80
	acc := NewAccumulator(w, h)
	for y := range h {
		for x := range w {
			c := float32(20)
			switch {
			case x < 12 || (y > 60 && x > 120):
				// A dithered edge and a rotated corner: a sub or none.
				c = float32((x + y) % 2)
			case y < 30:
				// Most subs framed lower: the top is thin but real data.
				c = 5
			}
			// Rejected star pixels, dense as in a master of few subs.
			if (x*7+y*3)%11 == 0 {
				c = 0
			}
			acc.Count[y*w+x] = c
		}
	}
	r := coverageCrop(acc)
	if r.X < 12 || r.X > 24 {
		t.Errorf("left edge at %d, want just past the dithered 12 px", r.X)
	}
	if r.Y != 0 {
		t.Errorf("crop %+v cut the thinly covered top, which has data", r)
	}
	if r.W*r.H < 100*55 {
		t.Errorf("crop %+v too small", r)
	}
}

func TestCoverageCropKeepsAFewSubsOffsetFrame(t *testing.T) {
	// Horsehead H-a: 4 subs, 3 framed lower, so most of the frame has 1.
	const w, h = 160, 80
	acc := NewAccumulator(w, h)
	for y := range h {
		for x := range w {
			c := float32(1)
			if y > 48 && x < 150 {
				c = 4
			}
			if (x*5+y*9)%7 == 0 {
				c = 0 // stars rejected
			}
			acc.Count[y*w+x] = c
		}
	}
	if r := coverageCrop(acc); r.W*r.H < 150*75 {
		t.Errorf("crop %+v, want nearly the whole frame", r)
	}
}

func TestDataCropOfMosaic(t *testing.T) {
	// Two panels offset vertically, as a mosaic canvas leaves them.
	const w, h = 120, 160
	data := make([]float32, w*h)
	for y := range h {
		for x := range w {
			if (y < 90 && x >= 10) || (y >= 70 && x < 110) {
				data[y*w+x] = 1
			}
		}
	}
	r := dataCrop(w, h, data)
	for y := r.Y; y < r.Y+r.H; y++ {
		for x := r.X; x < r.X+r.W; x++ {
			if data[y*w+x] == 0 {
				t.Fatalf("crop %+v includes empty canvas at %d,%d", r, x, y)
			}
		}
	}
	if r.W*r.H < 90*140 {
		t.Errorf("crop %+v smaller than the 96x152 region with data", r)
	}
}

func TestCommonCropIsTheOverlap(t *testing.T) {
	a := fracCrop(layer{}, 10, 0, 80, 100, 100, 100) // x 0.1-0.9
	b := fracCrop(layer{}, 0, 20, 100, 60, 100, 100) // y 0.2-0.8
	whole := layer{}
	r := commonCrop([]layer{a, b, whole}, 50, 50)
	if r != (Rect{X: 5, Y: 10, W: 40, H: 30}) {
		t.Errorf("got %+v", r)
	}
}
