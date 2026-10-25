package dustcheck

import "math"

// RimBright is the brightest a target's template may be, over its sky, for
// a sub's pixel to be divided by it: on a galaxy core or a bright nebula
// the sub's seeing and the master's differ by more than a streak.
const RimBright = 1.3

// MasterMap bins a target's master BinFactor×BinFactor and divides it by
// its median, as SubMap does a sub; ok is false for a master whose sky is
// not above zero.
func MasterMap(pix []float32, w, h int) (Map, bool) {
	m, err := SubMap(pix, w, h)
	return m, err == nil
}

// SensorTemplate resamples the binned map of a target's master into the
// binned sensor frame (w×h map pixels) of one of its subs, whose registered
// pixels map to the sensor by toSensor (x' = a·x + b·y + c, y' = d·x + e·y
// + f; imagedata.PropSensorMap). Pixels the master doesn't cover are NaN;
// ok is false for a map that can't be inverted.
func SensorTemplate(master Map, toSensor []float64, w, h int) (Map, bool) {
	if len(toSensor) != 6 {
		return Map{}, false
	}
	a, b, c, d, e, f := toSensor[0], toSensor[1], toSensor[2], toSensor[3], toSensor[4], toSensor[5]
	det := a*e - b*d
	if math.Abs(det) < 1e-6 || math.IsNaN(det) {
		return Map{}, false
	}
	out := NewMap(w, h)
	for v := range h {
		for u := range w {
			sx, sy := (float64(u)+0.5)*BinFactor-0.5, (float64(v)+0.5)*BinFactor-0.5
			dx, dy := sx-c, sy-f
			x, y := (e*dx-b*dy)/det, (a*dy-d*dx)/det
			out.Pix[v*w+u] = bilinear(master, (x+0.5)/BinFactor-0.5, (y+0.5)/BinFactor-0.5)
		}
	}
	return out, true
}

func bilinear(m Map, x, y float64) float32 {
	x0, y0 := int(math.Floor(x)), int(math.Floor(y))
	if x0 < 0 || y0 < 0 || x0+1 >= m.W || y0+1 >= m.H {
		return float32(math.NaN())
	}
	tx, ty := float32(x-float64(x0)), float32(y-float64(y0))
	i := y0*m.W + x0
	return (1-ty)*((1-tx)*m.Pix[i]+tx*m.Pix[i+1]) + ty*((1-tx)*m.Pix[i+m.W]+tx*m.Pix[i+m.W+1])
}
