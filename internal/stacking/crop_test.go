package stacking

import "testing"

func TestCoverageCropDropsThinEdges(t *testing.T) {
	const w, h = 160, 80
	acc := NewAccumulator(w, h)
	for y := range h {
		for x := range w {
			c := float32(20)
			// A dithered left edge and a rotated bottom-right corner.
			if x < 12 || (y > 60 && x > 120) {
				c = 6
			}
			// A few rejected star pixels inside the good area.
			if (x*7+y*3)%97 == 0 {
				c = 17
			}
			acc.Count[y*w+x] = c
		}
	}
	r := coverageCrop(acc)
	if r.X < 12 || r.X > 16 {
		t.Errorf("left edge at %d, want just past the dithered 12 px", r.X)
	}
	if r.X+r.W > w || r.Y+r.H > h || r.W*r.H < 100*50 {
		t.Errorf("crop %+v too small or out of bounds", r)
	}
	for y := r.Y; y < r.Y+r.H; y++ {
		for x := r.X; x < r.X+r.W; x++ {
			if acc.Count[y*w+x] == 6 {
				t.Fatalf("crop %+v includes thin coverage at %d,%d", r, x, y)
			}
		}
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
