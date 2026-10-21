package binaries

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image/color"
	"image/jpeg"
	"math"
	"slices"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

const (
	nightW = 1280
	nightH = 800
)

//nolint:gochecknoglobals // the night chart's fixed light colours
var (
	nInk   = color.RGBA{40, 40, 46, 255}
	nDim   = color.RGBA{95, 95, 105, 255}
	nGrid  = color.RGBA{228, 228, 231, 255}
	nDark  = color.RGBA{245, 158, 11, 255}
	nBand  = color.RGBA{14, 146, 196, 255}
	nLevel = color.RGBA{0, 105, 160, 255}
	nBin   = color.RGBA{125, 70, 190, 255}
	nCatch = color.RGBA{205, 45, 50, 255}
	nLow   = color.RGBA{120, 120, 130, 255}
	nWhite = color.RGBA{255, 255, 255, 255}
)

// verdictLabel is a stored night's verdict in plain words.
func verdictLabel(n app.RunNight) string {
	switch n.State {
	case app.DipSeen:
		return "Dip seen"
	case app.DipNone:
		return "No dip"
	case LiveFollowing:
		return "On time"
	case LiveOff:
		return "Off the prediction"
	}
	return "Could not judge"
}

// runKindLabel is the run's kind in plain words.
func runKindLabel(n app.RunNight) string {
	if n.Kind == app.RunNightEclipse {
		return "eclipse request"
	}
	return "dip window"
}

// nightPlot is a night chart being drawn: its canvas, plot area, time
// and magnitude ranges and the night.
type nightPlot struct {
	c             *canvas
	n             app.RunNight
	loc           *time.Location
	l, r, t, b    float64
	t0, t1        time.Time
	y0, y1        float64
	curve         []CurveSample
	ptCol, barCol color.RGBA
}

func (p *nightPlot) hm(t time.Time) string { return t.In(p.loc).Format("15:04") }

func (p *nightPlot) sx(x time.Time) float64 {
	return p.l + float64(x.Sub(p.t0))/float64(p.t1.Sub(p.t0))*(p.r-p.l)
}

func (p *nightPlot) sy(v float64) float64 {
	return p.t + (math.Max(p.y0, math.Min(p.y1, v))-p.y0)/(p.y1-p.y0)*(p.b-p.t)
}

func (p *nightPlot) clampX(x time.Time) float64 { return math.Max(p.l, math.Min(p.r, p.sx(x))) }

// NightChart draws a stored night as a JPEG in fixed light colours: each
// sub, the averages with their errors, the normal level, the depths of
// past dips, the smallest dip it could catch, the dark and high enough
// time and the stretch after the star dropped below the height limit.
func NightChart(n app.RunNight, loc *time.Location, zone string) ([]byte, error) {
	if len(n.Points) == 0 {
		return nil, fmt.Errorf("no measured subs")
	}
	p := &nightPlot{c: newCanvas(nightW, nightH, nWhite), n: n, loc: loc, l: 116, r: 1248, t: 140, b: 700,
		t0: n.First.Add(-15 * time.Minute), t1: n.Last.Add(15 * time.Minute), ptCol: nInk, barCol: nInk}
	p.ptCol.A, p.barCol.A = 140, 46
	if n.Kind == app.RunNightEclipse {
		var v LiveCurve
		if json.Unmarshal([]byte(n.Card), &v) == nil && v.Eclipse != nil {
			p.curve = v.Eclipse.Curve
		}
	}
	p.y0, p.y1 = yRange(n, p.curve)
	night, _ := time.Parse(time.DateOnly, n.Night)
	p.c.text(32, 52, fmt.Sprintf("%d · %s · night of %s", n.SourceID, runKindLabel(n), night.Format("Jan 2")), 30, true, nInk, 0)
	p.c.text(32, 92, fmt.Sprintf("%s · %d subs, %s–%s %s", verdictLabel(n), len(n.Points), p.hm(n.First), p.hm(n.Last), zone), 26, false, nInk, 0)
	p.bands()
	p.axes()
	p.marks()
	p.labels()
	p.legend()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, p.c.img, &jpeg.Options{Quality: 90}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (p *nightPlot) bands() {
	n := p.n
	if n.DarkStart != nil && n.DarkEnd != nil && n.DarkEnd.After(p.t0) && n.DarkStart.Before(p.t1) {
		p.c.fill(p.clampX(*n.DarkStart), p.t, p.clampX(*n.DarkEnd), p.b, nDark, 0.14)
	}
	if n.LowFrom != nil && n.LowFrom.Before(p.t1) {
		p.c.fill(p.clampX(*n.LowFrom), p.t, p.r, p.b, nLow, 0.16)
	}
	if n.Kind == app.RunNightDip && n.DepthHigh > 0 {
		p.c.fill(p.l, p.sy(n.DepthLow), p.r, p.sy(n.DepthHigh), nBand, 0.16)
	}
}

func (p *nightPlot) axes() {
	for v := math.Ceil(p.y0*10-1e-9) / 10; v <= p.y1+1e-9; v += 0.1 {
		v = math.Round(v*10) / 10
		p.c.line(p.l, p.sy(v), p.r, p.sy(v), 2, nGrid)
		label := fmt.Sprintf("%+.1f", v)
		if label == "-0.0" || label == "+0.0" {
			label = "0.0"
		}
		p.c.text(p.l-12, p.sy(v)+8, label, 22, false, nDim, 1)
	}
	for h := p.t0.Truncate(time.Hour).Add(time.Hour); !h.After(p.t1); h = h.Add(time.Hour) {
		p.c.line(p.sx(h), p.b, p.sx(h), p.b+8, 2, nDim)
		p.c.text(p.sx(h), p.b+36, p.hm(h), 22, false, nDim, 0.5)
	}
	p.c.text(p.l, p.t-10, "mag from normal, fainter down", 20, false, nDim, 0)
	p.c.line(p.l, p.sy(0), p.r, p.sy(0), 4, nLevel)
	for i := 1; i < len(p.curve); i++ {
		a, q := p.curve[i-1], p.curve[i]
		p.c.line(p.sx(a.T), p.sy(a.V), p.sx(q.T), p.sy(q.V), 3, nLevel)
	}
	if p.n.Kind == app.RunNightDip && p.n.Smallest > 0 {
		p.c.dashed(p.l, p.sy(p.n.Smallest), p.r, 3, 12, 8, nCatch)
	}
}

func (p *nightPlot) marks() {
	for _, pt := range p.n.Points {
		x := p.sx(pt.Time)
		p.c.line(x, p.sy(pt.Mag-pt.Err), x, p.sy(pt.Mag+pt.Err), 2, p.barCol)
		p.c.disc(x, p.sy(pt.Mag), 4.8, p.ptCol)
	}
	span := nBin
	span.A = 153
	for _, bn := range p.n.Bins {
		x, y := p.sx(bn.Time), p.sy(bn.Mag)
		lo, hi := p.sy(bn.Mag-bn.Err), p.sy(bn.Mag+bn.Err)
		p.c.line(p.sx(bn.Start), y, p.sx(bn.End), y, 3, span)
		p.c.line(x, lo, x, hi, 4, nBin)
		p.c.line(x-10, lo, x+10, lo, 4, nBin)
		p.c.line(x-10, hi, x+10, hi, 4, nBin)
		p.c.fill(x-13, y-13, x+13, y+13, nWhite, 1)
		p.c.fill(x-10, y-10, x+10, y+10, nBin, 1)
	}
}

func (p *nightPlot) labels() {
	n := p.n
	if n.DarkStart != nil && n.DarkEnd != nil && n.DarkEnd.After(p.t0) && n.DarkStart.Before(p.t1) {
		p.c.label(p.clampX(*n.DarkStart)+8, p.t+26, "dark and high enough to "+p.hm(*n.DarkEnd), nDim, 0)
	}
	if n.LowFrom != nil && n.LowFrom.Before(p.t1) {
		p.c.label(p.r-8, p.t+58, "star below the height limit from "+p.hm(*n.LowFrom), nDim, 1)
	}
	if n.Kind == app.RunNightDip && n.DepthHigh > 0 {
		p.c.label(p.r-8, p.sy(n.DepthHigh)-10, "past dips: "+trimNum(n.DepthLow)+" to "+trimNum(n.DepthHigh)+" mag", nLevel, 1)
	}
	p.c.label(p.l+8, p.sy(0)-10, "normal level", nLevel, 0)
	if n.Kind == app.RunNightDip && n.Smallest > 0 {
		p.c.label(p.l+8, p.sy(n.Smallest)+28, fmt.Sprintf("smallest dip it could catch, %.2f mag", n.Smallest), nCatch, 0)
	}
}

func (p *nightPlot) legend() {
	x, ly := 116.0, 776.0
	item := func(label string) {
		p.c.text(x, ly, label, 22, false, nDim, 0)
		x += textWidth(label, 22, false) + 36
	}
	p.c.disc(x+8, ly-8, 6, p.ptCol)
	x += 28
	item("each sub")
	if len(p.n.Bins) > 0 {
		p.c.fill(x, ly-18, x+18, ly, nBin, 1)
		x += 28
		item(binLegend(p.n))
	}
	if p.n.Kind == app.RunNightDip && p.n.Smallest > 0 {
		p.c.dashed(x, ly-8, x+32, 3, 10, 6, nCatch)
		x += 44
		item("smallest dip it could catch")
	}
	if len(p.curve) > 1 {
		p.c.line(x, ly-8, x+32, ly-8, 3, nLevel)
		x += 44
		item("expected eclipse")
	}
}

// binLegend names the averages: how many subs each holds and about how
// long a run of them lasts.
func binLegend(n app.RunNight) string {
	full := n.Bins[0].Subs
	out := fmt.Sprintf("average of %d subs", full)
	if n.RunMinutes > 0 {
		out += ", " + aboutSpan(time.Duration(n.RunMinutes*float64(time.Minute)))
	}
	if last := n.Bins[len(n.Bins)-1].Subs; len(n.Bins) > 1 && last != full {
		out += fmt.Sprintf("; the last %d", last)
	}
	return out
}

// aboutSpan is a run's length in plain words: "about an hour", "about 40
// min" or "about 1 h 30 min".
func aboutSpan(d time.Duration) string {
	m := int(d.Round(5*time.Minute) / time.Minute)
	switch {
	case m >= 50 && m <= 70:
		return "about an hour"
	case m < 60:
		return fmt.Sprintf("about %d min", max(m, 5))
	case m%60 == 0:
		return fmt.Sprintf("about %d hours", m/60)
	}
	return fmt.Sprintf("about %d h %02d min", m/60, m%60)
}

// yRange is the chart's magnitude range: the points within reason, the
// past dips and the smallest catchable dip, in steps of 0.1.
func yRange(n app.RunNight, curve []CurveSample) (float64, float64) {
	mags := make([]float64, 0, len(n.Points))
	for _, p := range n.Points {
		mags = append(mags, p.Mag)
	}
	slices.Sort(mags)
	lo, hi := mags[len(mags)*2/100], mags[len(mags)-1-len(mags)*2/100]
	lo, hi = math.Min(lo, -0.2), math.Max(hi, math.Max(n.DepthHigh, n.Smallest)+0.1)
	for _, cs := range curve {
		hi = math.Max(hi, cs.Hi+0.1)
	}
	return math.Floor(lo*10-0.5) / 10, math.Ceil(hi*10+0.5) / 10
}

func trimNum(v float64) string {
	s := fmt.Sprintf("%.2f", v)
	for s[len(s)-1] == '0' && s[len(s)-2] != '.' {
		s = s[:len(s)-1]
	}
	return s
}

// fill paints the rectangle with col at opacity a.
func (c *canvas) fill(x0, y0, x1, y1 float64, col color.RGBA, a float64) {
	if x1 < x0 {
		x0, x1 = x1, x0
	}
	if y1 < y0 {
		y0, y1 = y1, y0
	}
	for j := int(math.Round(y0)); j < int(math.Round(y1)); j++ {
		for i := int(math.Round(x0)); i < int(math.Round(x1)); i++ {
			c.blend(i, j, col, a)
		}
	}
}

// label draws s at size 22 over a pale box so marks under it do not hide
// it.
func (c *canvas) label(x, y float64, s string, col color.RGBA, align float64) {
	w := textWidth(s, 22, false)
	x0 := x - align*w
	c.fill(x0-4, y-20, x0+w+4, y+6, nWhite, 0.85)
	c.text(x, y, s, 22, false, col, align)
}

// dashed strokes a horizontal dashed line from x0 to x1 at y.
func (c *canvas) dashed(x0, y, x1, w, on, off float64, col color.RGBA) {
	for x := x0; x < x1; x += on + off {
		c.line(x, y, math.Min(x+on, x1), y, w, col)
	}
}
