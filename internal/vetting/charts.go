package vetting

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoverysource"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/ownimage"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/period"
)

const maxChartPoints = 600

func chartsOf(sum discoverysource.Summary, curve *ownimage.Curve, fx fetches, pr *period.Result, c change) []Chart {
	var e *period.Ephemeris
	if pr != nil {
		e = pr.Ephemeris
	}
	out := []Chart{nightChart(sum, curve, e)}
	if sum.Source.Kind == kindMover {
		return out
	}
	survey := fx.all()
	var all []lc.Series
	for _, f := range survey {
		all = append(all, f.Series...)
	}
	s, ok := chartSeries(period.AlignSrc(all, e))
	if !ok {
		return out
	}
	if e != nil {
		out = append(out, foldChart(s, e))
	} else {
		out = append(out, historyChart(s, c))
	}
	return out
}

func chartSeries(all []lc.Series) (lc.Series, bool) {
	rank := func(s lc.Series) int {
		switch s.Survey + " " + s.Band {
		case ztfR:
			return 0
		case ztfG:
			return 1
		case "ATLAS o":
			return 2
		case "ATLAS c":
			return 4
		}
		if s.Survey == lc.TESS {
			return 3
		}
		return 5
	}
	best, found := lc.Series{}, false
	for _, s := range all {
		if len(s.Points) < 10 {
			continue
		}
		if !found || rank(s) < rank(best) || (rank(s) == rank(best) && len(s.Points) > len(best.Points)) {
			best, found = s, true
		}
	}
	if found && best.Survey == lc.TESS {
		var merged lc.Series
		for _, s := range all {
			if s.Survey == lc.TESS {
				merged.Points = append(merged.Points, s.Points...)
			}
		}
		merged.Survey, merged.Band, merged.Relative = lc.TESS, "", true
		best = merged
	}
	return best, found
}

func seriesName(s lc.Series) string {
	if s.Band == "" {
		return s.Survey
	}
	return s.Survey + " " + s.Band
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	c := append([]float64(nil), v...)
	sort.Float64s(c)
	if len(c)%2 == 1 {
		return c[len(c)/2]
	}
	return (c[len(c)/2-1] + c[len(c)/2]) / 2
}

func levelOf(s lc.Series) float64 {
	m := make([]float64, len(s.Points))
	for i, p := range s.Points {
		m[i] = p.Mag
	}
	return median(m)
}

func thin(n int) []int {
	if n <= maxChartPoints {
		idx := make([]int, n)
		for i := range idx {
			idx[i] = i
		}
		return idx
	}
	idx := make([]int, maxChartPoints)
	for i := range idx {
		idx[i] = i * n / maxChartPoints
	}
	return idx
}

func yearOf(bjd float64) float64 { return 2000 + (bjd-2451545.0)/365.25 }

func nightChart(sum discoverysource.Summary, curve *ownimage.Curve, e *period.Ephemeris) Chart {
	night := sum.Best.Night.UTC()
	ch := Chart{ID: "night", Title: "Your night, " + dateWords(night), XUnit: unitHours, YUnit: unitDMag, YLabel: magFainter,
		Sub: "Your own brightness for this star against steady nearby stars."}
	if curve == nil {
		ch.Title, ch.Empty = "Your night", "Not measured yet"
		return ch
	}
	key := night.Format(time.DateOnly)
	byFilter := map[string][]ownimage.Point{}
	for _, p := range curve.Points {
		if p.Night == key {
			byFilter[p.Filter] = append(byFilter[p.Filter], p)
		}
	}
	filter, pts := "", []ownimage.Point(nil)
	for f, ps := range byFilter {
		if len(ps) > len(pts) || (len(ps) == len(pts) && f < filter) {
			filter, pts = f, ps
		}
	}
	if len(pts) == 0 {
		ch.Empty = "Not measured yet"
		return ch
	}
	sort.Slice(pts, func(i, j int) bool { return pts[i].BJD < pts[j].BJD })
	mid := pts[len(pts)/2].BJD
	zero := mid
	band := ""
	if e != nil {
		zero = nearestMinimum(e, mid)
		band = bandOf(e, filter)
		ch.XLabel = "Hours from the predicted minimum"
		ch.VRef = new(float64)
	} else {
		ch.XLabel = "Hours from the middle of your frames"
	}
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, p := range pts {
		x := (p.BJD - zero) * 24
		ch.Points = append(ch.Points, ChartPoint{X: round(x, 4), Y: round(p.DMag, 4), Err: round(p.Err, 4)})
		lo, hi = math.Min(lo, p.DMag-p.Err), math.Max(hi, p.DMag+p.Err)
	}
	x0, x1 := ch.Points[0].X, ch.Points[len(ch.Points)-1].X
	pad := math.Max(0.1, (x1-x0)*0.08)
	ch.X0, ch.X1 = x0-pad, x1+pad
	if e != nil {
		for i := 0; i <= 120; i++ {
			x := ch.X0 + (ch.X1-ch.X0)*float64(i)/120
			y := e.Predict(zero+x/24, band)
			ch.Line = append(ch.Line, [2]float64{round(x, 4), round(y, 4)})
			lo, hi = math.Min(lo, y), math.Max(hi, y)
		}
	}
	lo, hi = math.Min(lo, 0), math.Max(hi, 0)
	ch.Y0, ch.Y1 = padRange(lo, hi)
	ch.XTicks, ch.YTicks = ticks(ch.X0, ch.X1, 5), ticks(ch.Y0, ch.Y1, 5)
	ch.Sub = fmt.Sprintf("%d %s frames against %d steady nearby stars. Lower is fainter.", len(pts), filter, curve.Comps-curve.Dropped)
	ch.Legend = []LegendItem{{Style: styleDot, Label: "Your frames"}}
	if len(ch.Line) > 0 {
		ch.Legend = append(ch.Legend, LegendItem{Style: "line", Label: "Predicted dip"})
	}
	return ch
}

func nearestMinimum(e *period.Ephemeris, bjd float64) float64 {
	best, bd := e.T0, math.Inf(1)
	phases := make([]float64, 0, 1+len(e.Eclipses))
	phases = append(phases, 0)
	for _, ec := range e.Eclipses {
		phases = append(phases, ec.Phase)
	}
	n := math.Round((bjd - e.T0) / e.Period)
	for _, k := range []float64{n - 1, n, n + 1} {
		for _, ph := range phases {
			t := e.T0 + (k+ph)*e.Period
			if d := math.Abs(t - bjd); d < bd {
				best, bd = t, d
			}
		}
	}
	return best
}

func foldChart(s lc.Series, e *period.Ephemeris) Chart {
	name := seriesName(s)
	ch := Chart{ID: "fold", Title: fmt.Sprintf("%s, folded on %s days", name, periodText(e.Period)), XLabel: "Part of the cycle", YLabel: magFainter,
		XUnit: "phase", YUnit: unitDMag, X0: -0.25, X1: 0.75}
	level := 0.0
	if !s.Relative {
		level = levelOf(s)
	}
	pts := append([]lc.Point(nil), s.Points...)
	sort.Slice(pts, func(i, j int) bool { return pts[i].BJD < pts[j].BJD })
	lo, hi := 0.0, 0.0
	for _, i := range thin(len(pts)) {
		p := pts[i]
		y := p.Mag - level
		ch.Points = append(ch.Points, ChartPoint{X: round(e.Phase(p.BJD), 4), Y: round(y, 4)})
		lo, hi = math.Min(lo, y), math.Max(hi, y)
	}
	band := name
	for _, ph := range modelPhases(e) {
		y := e.Predict(e.T0+ph*e.Period, band)
		ch.Line = append(ch.Line, [2]float64{round(ph, 5), round(y, 4)})
		hi = math.Max(hi, y)
	}
	ch.Y0, ch.Y1 = padRange(lo, hi)
	ch.XTicks, ch.YTicks = []float64{-0.25, 0, 0.25, 0.5, 0.75}, ticks(ch.Y0, ch.Y1, 5)
	y0, y1 := yearOf(pts[0].BJD), yearOf(pts[len(pts)-1].BJD)
	ch.Sub = fmt.Sprintf("%s of %s points from %d to %d.", thousands(len(ch.Points)), thousands(len(pts)), int(y0), int(y1))
	if len(ch.Points) == len(pts) {
		ch.Sub = fmt.Sprintf("%s points from %d to %d.", thousands(len(pts)), int(y0), int(y1))
	}
	if e.Kind == period.KindEclipsing && len(e.Eclipses) > 1 {
		ch.Sub += fmt.Sprintf(" Deeper dip at 0, shallower dip at %s.", trimZeros(fmt.Sprintf("%.2f", e.Eclipses[1].Phase)))
	} else if e.Kind == period.KindEclipsing {
		ch.Sub += " The dip is at 0."
	}
	ch.Legend = []LegendItem{{Style: styleDot, Label: name + " points"}, {Style: "line", Label: "Fitted shape"}}
	if e.Kind == period.KindEclipsing {
		ch.Legend[1].Label = "Fitted eclipse shape"
	}
	return ch
}

func modelPhases(e *period.Ephemeris) []float64 {
	set := map[float64]bool{}
	for i := 0; i <= 200; i++ {
		set[-0.25+float64(i)/200] = true
	}
	for _, ec := range e.Eclipses {
		w := math.Max(ec.Width, 0.002)
		for i := -60; i <= 60; i++ {
			ph := ec.Phase + w*1.2*float64(i)/60
			if ph >= 0.75 {
				ph--
			}
			if ph < -0.25 {
				ph++
			}
			set[ph] = true
		}
	}
	out := make([]float64, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Float64s(out)
	return out
}

func historyChart(s lc.Series, c change) Chart {
	name := seriesName(s)
	pts := append([]lc.Point(nil), s.Points...)
	sort.Slice(pts, func(i, j int) bool { return pts[i].BJD < pts[j].BJD })
	y0, y1 := yearOf(pts[0].BJD), yearOf(pts[len(pts)-1].BJD)
	span := int(math.Round(y1 - y0))
	ch := Chart{ID: "history", Title: fmt.Sprintf("%s over %s years", name, numberWord(max(span, 1))), XLabel: "Year", XUnit: "year"}
	level := levelOf(s)
	ch.YLabel, ch.YUnit = labelMag, unitMag
	if s.Relative {
		ch.YLabel, ch.YUnit = magFainter, unitDMag
	}
	lo, hi := level, level
	for _, i := range thin(len(pts)) {
		p := pts[i]
		ch.Points = append(ch.Points, ChartPoint{X: round(yearOf(p.BJD), 3), Y: round(p.Mag, 4)})
		lo, hi = math.Min(lo, p.Mag), math.Max(hi, p.Mag)
	}
	ch.Refs = []ChartRef{{Y: round(level, 3), Style: "base"}}
	ch.Legend = []LegendItem{{Style: styleDot, Label: name + " points"}}
	if c.Mag > 0 {
		at := level + c.Mag
		word := "fade"
		if !c.Fainter {
			at, word = level-c.Mag, "brightening"
		}
		ch.Refs = append(ch.Refs, ChartRef{Y: round(at, 3), Style: "fade"})
		ch.Legend = append(ch.Legend, LegendItem{Style: "dash", Label: fmt.Sprintf("Where a %.2f mag %s would sit", c.Mag, word)})
		lo, hi = math.Min(lo, at), math.Max(hi, at)
	}
	ch.X0, ch.X1 = math.Floor(y0), math.Ceil(y1+0.01)
	ch.Y0, ch.Y1 = padRange(lo, hi)
	ch.XTicks, ch.YTicks = ticks(ch.X0, ch.X1, 5), ticks(ch.Y0, ch.Y1, 4)
	flat := "No period found."
	if !ztfVaries([]lc.Series{s}) {
		flat = "No period: the light curve is flat."
	}
	ch.Sub = fmt.Sprintf("%s of %s points. %s Lower is fainter.", thousands(len(ch.Points)), thousands(len(pts)), flat)
	if len(ch.Points) == len(pts) {
		ch.Sub = fmt.Sprintf("%s points. %s Lower is fainter.", thousands(len(pts)), flat)
	}
	return ch
}

func padRange(lo, hi float64) (float64, float64) {
	if hi-lo < 0.05 {
		m := (hi + lo) / 2
		lo, hi = m-0.025, m+0.025
	}
	p := (hi - lo) * 0.08
	return round(lo-p, 4), round(hi+p, 4)
}

func ticks(lo, hi float64, n int) []float64 {
	span := hi - lo
	if span <= 0 || n < 2 {
		return nil
	}
	raw := span / float64(n)
	mag := math.Pow(10, math.Floor(math.Log10(raw)))
	step := mag
	for _, m := range []float64{1, 2, 2.5, 5, 10} {
		if raw <= m*mag {
			step = m * mag
			break
		}
	}
	var out []float64
	for v := math.Ceil(lo/step) * step; v <= hi+step*1e-9; v += step {
		out = append(out, round(v, 6))
	}
	return out
}
