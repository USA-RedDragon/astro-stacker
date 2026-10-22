package morningreport

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

// Run night states of an eclipse request, as the live card names them.
const (
	eclipseOnTime = "following"
	eclipseOff    = "off"
)

// RunChart is one stored night of a dip window or eclipse request, with
// its chart.
type RunChart struct {
	Night app.RunNight
	JPEG  []byte
}

// runs reads the night's stored dip window and eclipse request runs and
// draws their charts.
func (c Collector) runs(ctx context.Context, r *Report) {
	if c.App == nil {
		return
	}
	db := c.App.WithContext(ctx)
	if !db.Migrator().HasTable(&app.RunNight{}) {
		return
	}
	var rows []app.RunNight
	if err := db.Where("night = ?", NightKey(r.Night)).Order("first").Find(&rows).Error; err != nil {
		slog.Warn("Morning report: could not read the dip and eclipse runs", "error", err)
		r.RunsMissing = notMeasured
		return
	}
	stale, err := app.StaleRunGUIDs(db)
	if err != nil {
		slog.Warn("Morning report: could not read which eclipse requests were withdrawn", "error", err)
	}
	rows, dropped := app.MergeRunNights(rows, stale)
	if len(dropped) > 0 {
		slog.Warn("Morning report: left out stored nights of runs already in the report", "rows", dropped)
	}
	for _, row := range rows {
		rc := RunChart{Night: row}
		if c.RunChart != nil && len(row.Points) > 0 {
			jpg, err := c.RunChart(row)
			if err != nil {
				slog.Warn("Morning report: no run chart", "project", row.ProjectGUID, "error", err)
			} else {
				rc.JPEG = jpg
			}
		}
		r.Runs = append(r.Runs, rc)
	}
}

// runs is the Dip and eclipse runs block: one bullet per run imaged in
// the night, or a line saying none was.
func (f Formatter) runs(r Report) string {
	if r.RunsMissing != "" {
		return "<b>Dip and eclipse runs</b> " + r.RunsMissing
	}
	lines := []string{"<b>Dip and eclipse runs</b>"}
	dips, eclipses := 0, 0
	for _, rc := range r.Runs {
		if rc.Night.Kind == app.RunNightEclipse {
			eclipses++
		} else {
			dips++
		}
		lines = append(lines, "• "+f.runLine(rc.Night))
	}
	if dips == 0 {
		lines = append(lines, "• Dip windows: none were imaged.")
	}
	if eclipses == 0 {
		lines = append(lines, "• Eclipse requests: none were imaged.")
	}
	return strings.Join(lines, "\n")
}

func kindLabel(n app.RunNight) string {
	if n.Kind == app.RunNightEclipse {
		return "eclipse request"
	}
	return "dip window"
}

// verdict is the run's outcome in bold with its words.
func (f Formatter) verdict(n app.RunNight) string {
	switch n.State {
	case app.DipSeen:
		out := fmt.Sprintf("<b>dip seen</b>: faded %s mag", num(n.Depth, 2))
		if n.DipStart != nil {
			out += " from " + f.clock(*n.DipStart)
		}
		return out + ", nearby stars steady"
	case app.DipNone:
		return fmt.Sprintf("<b>no dip</b>: steady to within %s mag over every run of %d subs", num(n.Smallest, 2), n.Run)
	case eclipseOnTime:
		return "<b>on time</b>: " + esc(n.Note)
	case eclipseOff:
		return "<b>off the prediction</b>: " + esc(n.Note)
	}
	why := strings.TrimSuffix(n.Note, ".")
	if why == "" {
		why = "no reason was stored"
	}
	return "<b>couldn't judge</b>: " + esc(why)
}

// runLine is one run's bullet.
func (f Formatter) runLine(n app.RunNight) string {
	name := fmt.Sprintf("<b>%d</b> %s", n.SourceID, kindLabel(n))
	if n.Object != "" {
		name += " (" + esc(n.Object) + ")"
	}
	out := name + ": " + f.verdict(n) + ". "
	subs := plural(n.Subs, "sub", "subs")
	if n.Measured != n.Subs {
		subs += fmt.Sprintf(" (%d measured)", n.Measured)
	}
	if n.Measured > 0 {
		subs += ", " + f.span(n.First, n.Last) + " (" + hm(n.Last.Sub(n.First)) + ")"
	}
	out += subs + "."
	if n.Kind == app.RunNightDip && n.Smallest > 0 && n.State != app.DipUnsure {
		out += " It would have caught a dip of " + num(n.Smallest, 2) + " mag or more"
		if n.RunMinutes > 0 {
			out += " lasting " + aboutSpan(time.Duration(n.RunMinutes*float64(time.Minute)))
		}
		if n.DepthHigh > 0 {
			out += "; past dips were " + trimNum(n.DepthLow) + " to " + trimNum(n.DepthHigh) + " mag"
		}
		out += "."
	}
	if n.Comps > 0 && n.AgreeHigh > 0 {
		if n.AgreeLow >= n.Comps {
			out += fmt.Sprintf(" In every sub all %d nearby stars agreed.", n.Comps)
		} else {
			out += fmt.Sprintf(" In every sub at least %d of the %d nearby stars agreed.", n.AgreeLow, n.Comps)
		}
	}
	return out
}

// RunCaption is a run chart's caption.
func (f Formatter) RunCaption(n app.RunNight) string {
	night, _ := time.Parse(time.DateOnly, n.Night)
	label := map[string]string{app.DipSeen: "dip seen", app.DipNone: "no dip", eclipseOnTime: "on time", eclipseOff: "off the prediction"}[n.State]
	if label == "" {
		label = "couldn't judge"
	}
	out := fmt.Sprintf("<b>%d</b> · %s, night of %s · <b>%s</b> · %s", n.SourceID, kindLabel(n), night.Format("Jan 2"), label, plural(n.Measured, "sub", "subs"))
	if n.Measured > 0 {
		out += ", " + f.span(n.First, n.Last)
	}
	if len(n.Bins) > 0 {
		full, last := n.Bins[0].Subs, n.Bins[len(n.Bins)-1].Subs
		out += fmt.Sprintf(" · squares average %d subs each", full)
		if len(n.Bins) > 1 && last != full {
			out += fmt.Sprintf(", the last %d", last)
		}
	}
	return truncate(out, MaxCaption)
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

// trimNum is v to two decimals without a trailing zero: 0.15, 0.6.
func trimNum(v float64) string {
	s := fmt.Sprintf("%.2f", v)
	for s[len(s)-1] == '0' && s[len(s)-2] != '.' {
		s = s[:len(s)-1]
	}
	return s
}
