package stacking

import "testing"

func TestCoverageCropIsWhereAllSubsOverlap(t *testing.T) {
	const w, h = 320, 200
	acc := NewAccumulator(w, h)
	for y := range h {
		for x := range w {
			c := float32(20)
			switch {
			case x < 12:
				c = 0 // empty registration border
			case y < 30:
				c = 12 // a weak edge only some subs reach
			}
			// Rejected stars and a satellite trail inside the good area.
			if (x*7+y*3)%53 == 0 || (x > 100 && x < 106) {
				c = max(0, c-3)
			}
			acc.Count[y*w+x] = c
		}
	}
	r := coverageCrop(acc)
	if r.X < 12 || r.X > 24 {
		t.Errorf("left edge at %d, want just past the empty 12 px", r.X)
	}
	if r.Y < 30 || r.Y > 40 {
		t.Errorf("top at %d, want just below the weak 30 px", r.Y)
	}
	if r.W < 280 || r.H < 150 {
		t.Errorf("crop %+v: rejections inside shrank it", r)
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
