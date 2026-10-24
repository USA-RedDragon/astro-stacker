package morningreport

import (
	"math"
	"slices"
	"sort"
	"strings"
	"time"
)

// NINA log lines, as Loki holds them: the line's own time and fields
// separated by "|", the message last.
const (
	msgUnsafe       = "SafetyMonitorInfo state changed to Unsafe"
	msgCoverOpened  = "Finishing Category: Flat Device, Item: OpenCover"
	msgCoverClosed  = "Finishing Category: Flat Device, Item: CloseCover"
	msgShutdown     = "Application shutting down"
	msgSMConnect    = "Successfully connected Safety Monitor"
	msgSMDisconnect = "Disconnected Safety Monitor"
	msgCloseFailed  = "Item: CloseCover - "
)

// FlapQuery selects the NINA lines the flap timeline is read from.
const FlapQuery = `{job="observatory", app="nina"} |~ "SafetyMonitorInfo state changed to Unsafe|Finishing Category: Flat Device, Item: (Open|Close)Cover|Application shutting down|Successfully connected Safety Monitor|Disconnected Safety Monitor|Item: CloseCover - "`

// CoverQuery selects only the flap's open and close steps, for the flap's
// state when the window starts.
const CoverQuery = `{job="observatory", app="nina"} |~ "Finishing Category: Flat Device, Item: (Open|Close)Cover"`

// GuiderQuery selects PHD2's warnings and errors as NINA logs them.
const GuiderQuery = `{job="observatory", app="nina"} |= "PHD2Guider.cs" |~ "[|](WARNING|ERROR)[|]"`

// An unsafe reading this close after NINA starts shutting down, or this close
// before the safety monitor disconnects, is NINA closing, not the weather; one
// this close after the monitor connects is its first reading.
const (
	shutdownGrace   = 30 * time.Second
	disconnectGrace = 3 * time.Second
	connectGrace    = 10 * time.Second
)

// SeedOpen reads whether the flap was open from the last cover step before
// the window.
func SeedOpen(last *Line) bool {
	return last != nil && strings.Contains(last.Text, msgCoverOpened)
}

// Flap is the flap's timeline over a window.
type Flap struct {
	Open time.Duration
	// OpenSpans are when the flap was open.
	OpenSpans []Span
	Unsafe    []UnsafeEvent
	Restarts  []UnsafeEvent
	// Moves counts the open and close steps in the window.
	Moves int
	// FailedCloses are the close steps that failed since the flap last
	// closed, when it was still open at the window's end.
	FailedCloses []CloseFailure
}

// CloseFailure is a close step that failed, and why in plain words.
type CloseFailure struct {
	At     time.Time
	Reason string
}

// closeFailReason is why a close step failed, in plain words.
func closeFailReason(text string) string {
	first, _, _ := strings.Cut(text, "\n")
	_, msg, _ := strings.Cut(first, msgCloseFailed)
	msg = strings.TrimSpace(msg)
	if strings.Contains(strings.ToLower(msg), "not connected") {
		return "flat panel unreachable"
	}
	if r := []rune(msg); len(r) > 60 {
		msg = string(r[:60]) + "…"
	}
	if msg == "" {
		return "close step failed"
	}
	return strings.ToLower(msg)
}

// FlapTimeline reads how long the flap was open in [start, end), and the
// unsafe readings and NINA restarts that found it open with when it closed.
func FlapTimeline(open bool, lines []Line, start, end time.Time) Flap {
	var out Flap
	openedAt := start
	var pendingUnsafe, pendingRestart []int
	var failed []CloseFailure
	for i, l := range lines {
		if l.T.Before(start) || !l.T.Before(end) {
			continue
		}
		switch {
		case strings.Contains(l.Text, msgCoverOpened):
			out.Moves++
			if !open {
				open, openedAt = true, l.T
			}
		case strings.Contains(l.Text, msgCoverClosed):
			out.Moves++
			if open {
				out.Open += l.T.Sub(openedAt)
				out.OpenSpans = append(out.OpenSpans, Span{From: openedAt, To: l.T})
				open = false
			}
			t := l.T
			for _, j := range pendingUnsafe {
				out.Unsafe[j].Closed = &t
			}
			for _, j := range pendingRestart {
				out.Restarts[j].Closed = &t
			}
			pendingUnsafe, pendingRestart, failed = nil, nil, failed[:0]
		case strings.Contains(l.Text, msgCloseFailed):
			if !open {
				continue
			}
			why := closeFailReason(l.Text)
			failed = append(failed, CloseFailure{At: l.T, Reason: why})
			for _, j := range pendingUnsafe {
				out.Unsafe[j].CloseFailed = why
			}
			for _, j := range pendingRestart {
				out.Restarts[j].CloseFailed = why
			}
		case strings.Contains(l.Text, msgShutdown):
			if open {
				out.Restarts = append(out.Restarts, UnsafeEvent{At: l.T})
				pendingRestart = append(pendingRestart, len(out.Restarts)-1)
			}
		case strings.Contains(l.Text, msgUnsafe):
			if open && !closing(lines, i) {
				out.Unsafe = append(out.Unsafe, UnsafeEvent{At: l.T})
				pendingUnsafe = append(pendingUnsafe, len(out.Unsafe)-1)
			}
		}
	}
	if open {
		out.Open += end.Sub(openedAt)
		out.OpenSpans = append(out.OpenSpans, Span{From: openedAt, To: end})
		out.FailedCloses = failed
	}
	return out
}

// closing reports whether the unsafe reading at lines[i] came from NINA
// shutting down or the safety monitor connecting.
func closing(lines []Line, i int) bool {
	at := lines[i].T
	for j := i - 1; j >= 0 && at.Sub(lines[j].T) <= shutdownGrace; j-- {
		if strings.Contains(lines[j].Text, msgShutdown) {
			return true
		}
		if strings.Contains(lines[j].Text, msgSMConnect) && at.Sub(lines[j].T) <= connectGrace {
			return true
		}
	}
	for j := i + 1; j < len(lines) && lines[j].T.Sub(at) <= disconnectGrace; j++ {
		if strings.Contains(lines[j].Text, msgSMDisconnect) {
			return true
		}
	}
	return false
}

// MergeRestarts folds NINA restarts less than RestartGap apart into the
// first, which keeps the latest close.
func MergeRestarts(events []UnsafeEvent) []UnsafeEvent {
	var out []UnsafeEvent
	for _, e := range events {
		if n := len(out); n > 0 && e.At.Sub(out[n-1].At) < RestartGap {
			out[n-1].Closed = e.Closed
			continue
		}
		out = append(out, e)
	}
	return out
}

// AutofocusQuery selects the autofocus engine's start and outcome lines.
const AutofocusQuery = `{job="observatory", app="nina"} |~ "Starting AutoFocus with initial position|AutoFocus completed with focuser|AutoFocus did not complete successfully"`

const (
	msgAFStart  = "Starting AutoFocus with initial position"
	msgAFDone   = "AutoFocus completed with focuser"
	msgAFFailed = "AutoFocus did not complete successfully"
)

// AutofocusRuns reads the autofocus runs from NINA's log: each start, and
// whether it completed. A run with no outcome before the next start or the
// window's end did not complete.
func AutofocusRuns(lines []Line) []AutofocusRun {
	var out []AutofocusRun
	open := false
	for _, l := range lines {
		switch {
		case strings.Contains(l.Text, msgAFStart):
			t := l.T
			out = append(out, AutofocusRun{Started: &t, Finished: t, Error: "did not finish"})
			open = true
		case strings.Contains(l.Text, msgAFDone), strings.Contains(l.Text, msgAFFailed):
			if !open {
				out = append(out, AutofocusRun{})
			}
			r := &out[len(out)-1]
			r.Finished, r.Success, r.Error = l.T, strings.Contains(l.Text, msgAFDone), ""
			if !r.Success {
				r.Error = "did not complete"
			}
			open = false
		}
	}
	markBackToBack(out)
	return out
}

func markBackToBack(runs []AutofocusRun) {
	for i := 1; i < len(runs); i++ {
		start := runs[i].Finished
		if runs[i].Started != nil {
			start = *runs[i].Started
		}
		runs[i].BackToBack = start.Sub(runs[i-1].Finished) < BackToBackGap
	}
}

// guiderKind is a PHD2 warning or error in plain words.
func guiderKind(text string) string {
	switch {
	case strings.Contains(text, "Cannot initiate dither while dither is in progress"):
		return "dither refused, one already running"
	case strings.Contains(text, "timed-out waiting for guider to settle"):
		return "settle timed out"
	case strings.Contains(text, "did not send SettleDone"):
		return "no settle reply"
	case strings.Contains(text, "Guiding stopped"):
		return "guiding stopped"
	case strings.Contains(strings.ToLower(text), "star lost"):
		return "guide star lost"
	}
	lower := strings.ToLower(text)
	if strings.Contains(lower, "error while sending") || strings.Contains(lower, "connection lost") {
		return "PHD2 not reachable"
	}
	first, _, _ := strings.Cut(text, "\n")
	parts := strings.SplitN(first, "|", 6)
	msg := strings.TrimSpace(parts[len(parts)-1])
	for _, p := range []string{"PHDError:", "PHD2 error:", "Phd2 -"} {
		msg = strings.TrimSpace(strings.TrimPrefix(msg, p))
	}
	if r := []rune(msg); len(r) > 60 {
		msg = string(r[:60]) + "…"
	}
	return strings.ToLower(msg)
}

// BurstGap is the longest quiet time inside one burst of guider errors.
const BurstGap = 5 * time.Minute

// GuiderBursts groups guider errors that came close together.
func GuiderBursts(lines []Line) []ErrorBurst {
	var out []ErrorBurst
	var counts map[string]int
	var order []string
	flush := func() {
		if len(out) == 0 {
			return
		}
		b := &out[len(out)-1]
		for _, k := range order {
			b.Kinds = append(b.Kinds, Count{Label: k, N: counts[k]})
		}
		sort.SliceStable(b.Kinds, func(i, j int) bool { return b.Kinds[i].N > b.Kinds[j].N })
	}
	for _, l := range lines {
		if len(out) == 0 || l.T.Sub(out[len(out)-1].To) > BurstGap {
			flush()
			out = append(out, ErrorBurst{From: l.T, To: l.T})
			counts, order = map[string]int{}, nil
		}
		k := guiderKind(l.Text)
		if counts[k] == 0 {
			order = append(order, k)
		}
		counts[k]++
		out[len(out)-1].To = l.T
	}
	flush()
	return out
}

// Span is a stretch of time, such as a light's exposure.
type Span struct{ From, To time.Time }

func inside(t time.Time, spans []Span) bool {
	i := sort.Search(len(spans), func(i int) bool { return !spans[i].To.Before(t) })
	return i < len(spans) && !t.Before(spans[i].From)
}

// sortSpans orders spans by start and merges overlaps.
func sortSpans(spans []Span) []Span {
	s := slices.Clone(spans)
	sort.Slice(s, func(i, j int) bool { return s[i].From.Before(s[j].From) })
	var out []Span
	for _, e := range s {
		if n := len(out); n > 0 && !e.From.After(out[n-1].To) {
			if e.To.After(out[n-1].To) {
				out[n-1].To = e.To
			}
			continue
		}
		out = append(out, e)
	}
	return out
}

// During keeps the points inside the spans.
func During(pts []Point, spans []Span) []Point {
	spans = sortSpans(spans)
	var out []Point
	for _, p := range pts {
		if inside(p.T, spans) {
			out = append(out, p)
		}
	}
	return out
}

// GuidingStats is the median of the guiding RMS points and the worst mean
// over WorstStretch. Points at zero are PHD2 not guiding and are left out.
func GuidingStats(pts []Point) *Guiding {
	var g []Point
	for _, p := range pts {
		if p.V > 0 && !math.IsNaN(p.V) && !math.IsInf(p.V, 0) {
			g = append(g, p)
		}
	}
	if len(g) == 0 {
		return nil
	}
	vals := make([]float64, len(g))
	for i, p := range g {
		vals[i] = p.V
	}
	slices.Sort(vals)
	med := vals[len(vals)/2]
	if len(vals)%2 == 0 {
		med = (vals[len(vals)/2-1] + vals[len(vals)/2]) / 2
	}
	out := &Guiding{Median: med, Samples: len(g)}
	minPts := max(1, int(WorstStretch/Step)/2)
	found := false
	for i := range g {
		sum, n := 0.0, 0
		j := i
		for ; j < len(g) && g[j].T.Sub(g[i].T) < WorstStretch; j++ {
			sum += g[j].V
			n++
		}
		if n < minPts {
			continue
		}
		if m := sum / float64(n); !found || m > out.Worst {
			out.Worst, out.WorstAt, out.WorstEnd, found = m, g[i].T, g[j-1].T, true
		}
	}
	out.WorstMeasured = found
	return out
}

// DewStats reads the dew margin and the heaters. margin, tracking and heater
// are on the same step grid.
func DewStats(margin, tracking, heater []Point) *Dew {
	if len(margin) == 0 {
		return nil
	}
	track := map[int64]bool{}
	for _, p := range tracking {
		if p.V == 1 {
			track[p.T.Unix()] = true
		}
	}
	heat := map[int64]float64{}
	for _, p := range heater {
		heat[p.T.Unix()] = p.V
	}
	d := &Dew{Tracking: len(track) > 0, TrackingMeasured: len(tracking) > 0}
	found := false
	for _, p := range margin {
		if d.Tracking && !track[p.T.Unix()] {
			continue
		}
		if !found || p.V < d.Lowest {
			d.Lowest, d.LowestAt, found = p.V, p.T, true
		}
		if p.V >= BoostMargin {
			continue
		}
		d.Under += Step
		if h, ok := heat[p.T.Unix()]; ok {
			if d.HeaterMax == nil || h > *d.HeaterMax {
				v := h
				d.HeaterMax = &v
			}
			if h >= 100 {
				d.Boosted = true
			}
		}
	}
	if !found {
		return nil
	}
	return d
}

// SafeTime is how long the safety monitor read safe while connected inside
// the dark spans, and how long it was connected at all.
func SafeTime(safe, connected []Point, dark []Span) (safeFor, connectedFor time.Duration) {
	conn := map[int64]bool{}
	for _, p := range connected {
		if p.V == 1 {
			conn[p.T.Unix()] = true
			connectedFor += Step
		}
	}
	dark = sortSpans(dark)
	for _, p := range safe {
		if p.V == 1 && conn[p.T.Unix()] && within(p.T, dark) {
			safeFor += Step
		}
	}
	return safeFor, connectedFor
}

// within reports whether t is in one of the sorted spans, end excluded.
func within(t time.Time, spans []Span) bool {
	i := sort.Search(len(spans), func(i int) bool { return spans[i].To.After(t) })
	return i < len(spans) && !t.Before(spans[i].From)
}

// Range is the lowest and highest point.
func Range(pts []Point) (lo, hi *float64) {
	for _, p := range pts {
		if math.IsNaN(p.V) {
			continue
		}
		v := p.V
		if lo == nil || v < *lo {
			lo = &v
		}
		w := p.V
		if hi == nil || w > *hi {
			hi = &w
		}
	}
	return lo, hi
}
