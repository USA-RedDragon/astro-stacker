package binaries

import (
	"bytes"
	"cmp"
	"fmt"
	"image/color"
	"image/png"
	"math"
	"slices"
	"strings"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/period"
)

// plotSeries is one set of points with errors.
type plotSeries struct {
	Label  string
	Color  color.RGBA
	Points []vetting.ChartPoint
}

// plot is a light curve chart: Y grows downward (fainter is lower).
type plot struct {
	Title, Sub     string
	XLabel, YLabel string
	X0, X1, Y0, Y1 float64
	XTicks, YTicks []float64
	Series         []plotSeries
	Line           [][2]float64
	LineLabel      string
	VRefs          []float64
}

//nolint:gochecknoglobals // the plots' colours
var (
	ink     = color.RGBA{30, 30, 36, 255}
	grey    = color.RGBA{120, 120, 130, 255}
	gridCol = color.RGBA{225, 225, 230, 255}
	gCol    = color.RGBA{36, 140, 70, 200}
	rCol    = color.RGBA{205, 50, 45, 200}
	lineCol = color.RGBA{40, 80, 200, 255}
)

const (
	plotW = 1200
	plotH = 760
)

// render draws p as a PNG.
func (p plot) render() ([]byte, error) {
	c := newCanvas(plotW, plotH, color.RGBA{255, 255, 255, 255})
	l, r, t, b := 110.0, float64(plotW-40), 110.0, float64(plotH-90)
	c.text(l, 44, p.Title, 26, true, ink, 0)
	c.text(l, 76, p.Sub, 18, false, grey, 0)
	sx := func(x float64) float64 { return l + (x-p.X0)/(p.X1-p.X0)*(r-l) }
	sy := func(y float64) float64 { return t + (y-p.Y0)/(p.Y1-p.Y0)*(b-t) }
	for _, x := range p.XTicks {
		c.line(sx(x), t, sx(x), b, 1, gridCol)
		c.text(sx(x), b+28, tickText(x), 17, false, ink, 0.5)
	}
	for _, y := range p.YTicks {
		c.line(l, sy(y), r, sy(y), 1, gridCol)
		c.text(l-12, sy(y)+6, tickText(y), 17, false, ink, 1)
	}
	for _, v := range p.VRefs {
		c.line(sx(v), t, sx(v), b, 1.5, color.RGBA{150, 150, 160, 255})
	}
	c.rect(l, t, r, b, 1.5, ink)
	c.text((l+r)/2, float64(plotH)-28, p.XLabel, 19, false, ink, 0.5)
	c.text(l, t-12, p.YLabel, 17, false, grey, 0)
	inside := func(x, y float64) bool { return x >= l && x <= r && y >= t && y <= b }
	for _, s := range p.Series {
		for _, q := range s.Points {
			x, y := sx(q.X), sy(q.Y)
			if !inside(x, y) {
				continue
			}
			if q.Err > 0 {
				e := math.Abs(sy(q.Y+q.Err) - y)
				if e > 2 {
					c.line(x, math.Max(t, y-e), x, math.Min(b, y+e), 1, color.RGBA{s.Color.R, s.Color.G, s.Color.B, 70})
				}
			}
			c.disc(x, y, 2.6, s.Color)
		}
	}
	for i := 1; i < len(p.Line); i++ {
		a, bb := p.Line[i-1], p.Line[i]
		if inside(sx(a[0]), sy(a[1])) && inside(sx(bb[0]), sy(bb[1])) {
			c.line(sx(a[0]), sy(a[1]), sx(bb[0]), sy(bb[1]), 2.5, lineCol)
		}
	}
	lx := r - 10
	items := slices.Clone(p.Series)
	for i := len(items) - 1; i >= 0; i-- {
		s := items[i]
		w := textWidth(s.Label, 17, false)
		c.text(lx, t-12, s.Label, 17, false, ink, 1)
		c.disc(lx-w-12, t-18, 5, s.Color)
		lx -= w + 40
	}
	if p.LineLabel != "" {
		w := textWidth(p.LineLabel, 17, false)
		c.text(lx, t-12, p.LineLabel, 17, false, ink, 1)
		c.line(lx-w-30, t-18, lx-w-8, t-18, 3, lineCol)
	}
	var out bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&out, c.img); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func tickText(v float64) string {
	s := strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", v), "0"), ".")
	if s == "-0" {
		return "0"
	}
	return s
}

// ticks are round values between a and b, about n of them.
func ticks(a, b float64, n int) []float64 {
	lo, hi := math.Min(a, b), math.Max(a, b)
	span := hi - lo
	if !(span > 0) {
		return nil
	}
	raw := span / float64(n)
	mag := math.Pow(10, math.Floor(math.Log10(raw)))
	step := mag
	for _, m := range []float64{1, 2, 2.5, 5, 10} {
		if m*mag >= raw {
			step = m * mag
			break
		}
	}
	var out []float64
	for v := math.Ceil(lo/step) * step; v <= hi+1e-9; v += step {
		out = append(out, math.Round(v/step)*step)
	}
	return out
}

// phaseOf is bjd's place in the cycle, 0 at the deeper dip, in
// [-0.25, 0.75).
func phaseOf(e *period.Ephemeris, deep int, bjd float64) float64 {
	x := (bjd-e.T0)/e.Period - e.Eclipses[deep].Phase
	x -= math.Floor(x)
	if x >= 0.75 {
		x--
	}
	return x
}

// PhasedPlot draws the ZTF g and r points folded on e, deeper dip at 0,
// with the fitted dips in r. name and the caption go in the title lines.
func PhasedPlot(series []lc.Series, e *period.Ephemeris, deep int, name string, years [2]int) ([]byte, string, error) {
	var out []plotSeries
	var ys []float64
	for _, band := range []string{"g", "r"} {
		ps := plotSeries{Label: "ZTF " + band, Color: gCol}
		if band == "r" {
			ps.Color = rCol
		}
		for _, s := range series {
			if s.Survey != lc.ZTF || s.Band != band || s.Relative {
				continue
			}
			for _, q := range s.Points {
				if math.IsNaN(q.Mag) || math.IsNaN(q.BJD) {
					continue
				}
				ps.Points = append(ps.Points, vetting.ChartPoint{X: phaseOf(e, deep, q.BJD), Y: q.Mag, Err: q.Err})
				ys = append(ys, q.Mag)
			}
		}
		if len(ps.Points) > 0 {
			out = append(out, ps)
		}
	}
	if len(ys) < 20 {
		return nil, "", fmt.Errorf("too few ZTF g and r points to plot (%d)", len(ys))
	}
	slices.Sort(ys)
	lo, hi := ys[len(ys)/200], ys[len(ys)-1-len(ys)/200]
	n := normalOf(series, e, "r")
	if !math.IsNaN(n) {
		for _, d := range e.Eclipses {
			if v, ok := d.Depth[keyZTFr]; ok {
				hi = math.Max(hi, n+v)
			}
		}
	}
	pad := 0.08 * (hi - lo)
	p := plot{Title: name + ", folded on " + periodText(e.Period, e.PeriodErr) + " days",
		Sub:    fmt.Sprintf("ZTF g and r points %d to %d. The deeper eclipse is at 0. Lower is fainter.", years[0], years[1]),
		XLabel: "Part of the cycle", YLabel: "Magnitude", X0: -0.25, X1: 0.75, Y0: lo - pad, Y1: hi + pad,
		XTicks: ticks(-0.25, 0.75, 8), YTicks: ticks(lo-pad, hi+pad, 6), Series: out}
	if !math.IsNaN(n) {
		for i := 0; i <= 800; i++ {
			x := -0.25 + float64(i)/800
			bjd := e.T0 + (x+e.Eclipses[deep].Phase)*e.Period
			p.Line = append(p.Line, [2]float64{x, n + e.Predict(bjd, keyZTFr)})
		}
		p.LineLabel = "Fitted eclipses (r)"
	}
	caption := fmt.Sprintf("ZTF g and r points from %d to %d, folded on %s days. The deeper eclipse is at 0", years[0], years[1], periodText(e.Period, e.PeriodErr))
	if len(e.Eclipses) == 2 {
		caption += fmt.Sprintf(", the second one at %.2f of the cycle", frac(e.Eclipses[1-deep].Phase-e.Eclipses[deep].Phase))
	}
	b, err := p.render()
	return b, caption + ".", err
}

func normalOf(series []lc.Series, e *period.Ephemeris, band string) float64 {
	var v []float64
	for _, s := range series {
		if s.Survey != lc.ZTF || s.Band != band || s.Relative {
			continue
		}
		for _, q := range s.Points {
			if e.Predict(q.BJD, "ZTF "+band) == 0 && !math.IsNaN(q.Mag) {
				v = append(v, q.Mag)
			}
		}
	}
	if len(v) == 0 {
		return math.NaN()
	}
	slices.SortFunc(v, cmp.Compare[float64])
	return v[len(v)/2]
}

// OwnPlot draws the vet's chart of the user's own night, when it timed an
// eclipse.
func OwnPlot(ch vetting.Chart, name string) ([]byte, string, error) {
	if len(ch.Points) == 0 {
		return nil, "", fmt.Errorf("the night chart has no points")
	}
	p := plot{Title: name + ": " + ch.Title, Sub: ch.Sub, XLabel: ch.XLabel, YLabel: ch.YLabel,
		X0: ch.X0, X1: ch.X1, Y0: ch.Y0, Y1: ch.Y1, XTicks: ch.XTicks, YTicks: ch.YTicks,
		Series: []plotSeries{{Label: "Your frames", Color: color.RGBA{30, 30, 36, 230}, Points: ch.Points}}, Line: ch.Line}
	if len(ch.Line) > 0 {
		p.LineLabel = "Predicted eclipse"
	}
	if ch.VRef != nil {
		p.VRefs = []float64{*ch.VRef}
	}
	b, err := p.render()
	return b, ch.Title + ". " + ch.Sub, err
}
