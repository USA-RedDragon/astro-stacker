package dustcheck

import "testing"

func TestSensorTemplateFollowsTheMap(t *testing.T) {
	t.Parallel()
	master := NewMap(200, 150)
	for i := range master.Pix {
		master.Pix[i] = 1
	}
	master.Pix[60*master.W+80] = 5
	toSensor := []float64{-1, 0, 799, 0, -1, 599}
	tm, ok := SensorTemplate(master, toSensor, 200, 150)
	if !ok {
		t.Fatal("map not inverted")
	}
	best, at := float32(0), 0
	for i, v := range tm.Pix {
		if !isNaN(v) && v > best {
			best, at = v, i
		}
	}
	if x, y := at%tm.W, at/tm.W; x != 200-1-80 || y != 150-1-60 {
		t.Errorf("master pixel 80,60 landed at %d,%d, want %d,%d", x, y, 200-1-80, 150-1-60)
	}
	if _, ok := SensorTemplate(master, []float64{0, 0, 0, 0, 0, 0}, 10, 10); ok {
		t.Error("a singular map was inverted")
	}
}
