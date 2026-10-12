package schedauto

import (
	"math"
	"sort"
	"time"
)

const (
	// MaxSwitchGap is the longest gap between the last sub of one target and
	// the first of the next that counts as a switch. In the NINA logs of
	// 2026-10-07 to 2026-10-12 every switch without a wait took 3.1 to 4.7
	// minutes; every gap with a wait, a flap or a sequence restart in it took
	// 11.3 minutes or more.
	MaxSwitchGap = 10 * time.Minute
	// RecentNights is how many past nights the switching share is shown for.
	RecentNights = 14
	// OverheadNights is how many past nights the switch time is measured on.
	OverheadNights = 365
)

// Light is one light frame: the object it was taken for, its start and its
// length in seconds.
type Light struct {
	Object   string
	Start    time.Time
	Exposure float64
}

// NightShare is the time one night spent switching targets.
type NightShare struct {
	Night         string  `json:"night"`
	Switches      int     `json:"switches"`
	SwitchMinutes float64 `json:"switchMinutes"`
	ImagingHours  float64 `json:"imagingHours"`
	Share         float64 `json:"share"`
}

// Overhead is the measured time a target switch takes: from the end of the
// last sub of one target to the start of the first sub of the next, which
// covers the slew, centering, rotation, autofocus, guider start and the
// first download.
type Overhead struct {
	Switches      int          `json:"switches"`
	Nights        int          `json:"nights"`
	MedianMinutes float64      `json:"medianMinutes"`
	P90Minutes    float64      `json:"p90Minutes"`
	MeanMinutes   float64      `json:"meanMinutes"`
	LongerGaps    int          `json:"longerGaps"`
	From          *time.Time   `json:"from,omitempty"`
	To            *time.Time   `json:"to,omitempty"`
	Recent        []NightShare `json:"recent"`
	Basis         string       `json:"basis"`
}

// NightOf is the local date the night of t started on, by mean solar time
// at east longitude lon.
func NightOf(t time.Time, lon float64) string {
	return t.UTC().Add(time.Duration(lon/15*float64(time.Hour)) - 12*time.Hour).Format(time.DateOnly)
}

func quantile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(math.Ceil(p*float64(len(sorted)))) - 1
	return sorted[max(0, min(len(sorted)-1, i))]
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// MeasureOverhead measures the switch time from lights over the
// OverheadNights before now, and the switching share of the last
// RecentNights nights with lights.
func MeasureOverhead(lights []Light, lon float64, now time.Time) Overhead {
	from := now.Add(-OverheadNights * 24 * time.Hour)
	ls := make([]Light, 0, len(lights))
	seen := map[string]bool{}
	for _, l := range lights {
		if l.Start.IsZero() || !(l.Exposure > 0) || l.Start.Before(from) || l.Start.After(now) {
			continue
		}
		k := l.Object + "|" + l.Start.UTC().Format(time.RFC3339Nano)
		if seen[k] {
			continue
		}
		seen[k] = true
		ls = append(ls, l)
	}
	sort.Slice(ls, func(i, j int) bool { return ls[i].Start.Before(ls[j].Start) })
	o := Overhead{Recent: []NightShare{}, Basis: "Gaps between the last sub of one target and the first sub of the next on the same night, " +
		"from the stacker's lights; gaps over 10 minutes hold a wait, a flap close or a sequence restart and are left out"}
	byNight := map[string]*NightShare{}
	spans := map[string][2]time.Time{}
	var gaps []float64
	var order []string
	for i, l := range ls {
		n := NightOf(l.Start, lon)
		end := l.Start.Add(time.Duration(l.Exposure * float64(time.Second)))
		sp, ok := spans[n]
		if !ok {
			sp = [2]time.Time{l.Start, end}
			order = append(order, n)
			byNight[n] = &NightShare{Night: n}
		}
		if end.After(sp[1]) {
			sp[1] = end
		}
		spans[n] = sp
		if i == 0 {
			continue
		}
		p := ls[i-1]
		if p.Object == l.Object || NightOf(p.Start, lon) != n {
			continue
		}
		gap := l.Start.Sub(p.Start.Add(time.Duration(p.Exposure * float64(time.Second))))
		if gap < 0 {
			continue
		}
		if gap > MaxSwitchGap {
			o.LongerGaps++
			continue
		}
		gaps = append(gaps, gap.Minutes())
		byNight[n].Switches++
		byNight[n].SwitchMinutes += gap.Minutes()
	}
	o.Nights = len(order)
	if len(ls) > 0 {
		f, t := ls[0].Start, ls[len(ls)-1].Start
		o.From, o.To = &f, &t
	}
	if len(gaps) > 0 {
		sort.Float64s(gaps)
		sum := 0.0
		for _, g := range gaps {
			sum += g
		}
		o.Switches = len(gaps)
		o.MedianMinutes = round2(quantile(gaps, 0.5))
		o.P90Minutes = round2(quantile(gaps, 0.9))
		o.MeanMinutes = round2(sum / float64(len(gaps)))
	}
	for i := len(order) - 1; i >= 0 && len(o.Recent) < RecentNights; i-- {
		n := byNight[order[i]]
		sp := spans[order[i]]
		n.ImagingHours = round2(sp[1].Sub(sp[0]).Hours())
		n.SwitchMinutes = round2(n.SwitchMinutes)
		if n.ImagingHours > 0 {
			n.Share = math.Round(n.SwitchMinutes/(n.ImagingHours*60)*1000) / 1000
		}
		o.Recent = append(o.Recent, *n)
	}
	return o
}
