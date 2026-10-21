package morningreport

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// MaxMessage is Telegram's limit on one message's text.
const MaxMessage = 4096

// MaxCaption is Telegram's limit on a photo caption.
const MaxCaption = 1024

const notMeasured = "not measured"

// Formatter renders a report as Telegram HTML.
type Formatter struct {
	Loc *time.Location
	// Zone is the label after every time, such as "CT".
	Zone string
	// WebURL is the astro-processing site the discoveries line links to.
	WebURL string
}

func esc(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func (f Formatter) clock(t time.Time) string {
	return t.In(f.Loc).Format("15:04") + " " + f.Zone
}

func (f Formatter) span(a, b time.Time) string {
	if a.In(f.Loc).Format("15:04") == b.In(f.Loc).Format("15:04") {
		return f.clock(a)
	}
	return a.In(f.Loc).Format("15:04") + "–" + f.clock(b)
}

// hm is a duration as hours and minutes.
func hm(d time.Duration) string {
	m := int(d.Round(time.Minute) / time.Minute)
	if m < 60 {
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%dh %02dm", m/60, m%60)
}

// ms is a short duration as minutes and seconds.
func ms(d time.Duration) string {
	s := int(d.Round(time.Second) / time.Second)
	if s < 60 {
		return fmt.Sprintf("%ds", s)
	}
	if s < 3600 {
		return fmt.Sprintf("%dm %02ds", s/60, s%60)
	}
	return hm(d)
}

func num(v float64, prec int) string {
	s := fmt.Sprintf("%.*f", prec, v)
	if s == "-0" || strings.HasPrefix(s, "-0.") && strings.Trim(s[3:], "0") == "" {
		s = s[1:]
	}
	return s
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// discoveries is one line naming the targets with new candidates, when
// there are any.
func (f Formatter) discoveries(r Report) string {
	if len(r.Discoveries) == 0 {
		return ""
	}
	parts := make([]string, 0, len(r.Discoveries))
	for i, d := range r.Discoveries {
		part := fmt.Sprintf("%d on %s", d.N, esc(d.Label))
		if i == 0 {
			part = plural(d.N, "new candidate", "new candidates") + " on " + esc(d.Label)
		}
		if n := r.Novae[d.Label]; n > 0 {
			part += " (" + plural(n, "possible nova", "possible novae") + ")"
		}
		parts = append(parts, part)
	}
	line := "<b>Discoveries</b> " + strings.Join(parts, ", ")
	if f.WebURL != "" {
		u := strings.TrimSuffix(f.WebURL, "/") + "/discoveries?night=" + r.Night.Format(time.DateOnly)
		line += `: <a href="` + esc(u) + `">astro-processing</a>`
	}
	return line
}

func (f Formatter) title(r Report) string {
	return "<b>Observatory, night of " + r.Night.Format("Mon Jan 2") + "</b>"
}

// Text is the report as one Telegram message.
func (f Formatter) Text(r Report) string {
	if r.Subs != nil && r.Subs.Count == 0 {
		return f.idle(r)
	}
	head := []string{f.title(r), f.summary(r), f.dust(r), f.discoveries(r), f.known(r), f.claims(r)}
	tail := []string{f.unsafe(r), f.dew(r), f.autofocus(r), f.guiding(r), f.guiderErrors(r), f.camera(r)}
	subs := f.subs(r)
	for drop := 0; ; drop++ {
		lines := append(append(append([]string{}, head...), f.limitTargets(subs, drop)...), tail...)
		out := strings.Join(nonEmpty(lines), "\n")
		if len(out) <= MaxMessage || drop > len(subs) {
			return truncate(out, MaxMessage)
		}
	}
}

func nonEmpty(lines []string) []string {
	out := lines[:0:0]
	for _, l := range lines {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// truncate cuts s to at most n bytes at its last line break that fits, or
// else at a character outside any tag or entity.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	limit := n - len("\n…")
	if cut := strings.LastIndex(s[:limit], "\n"); cut >= 0 {
		return s[:cut] + "\n…"
	}
	return cutText(s, n-len("…")) + "…"
}

// cutText is the longest prefix of s that ends on a character outside a
// tag or entity and, with the tags it left open closed, fits in n bytes.
func cutText(s string, n int) string {
	inTag, inEntity := false, false
	var open, kept []string
	cut := 0
	closers := func(tags []string) (k int) {
		for _, t := range tags {
			k += len("</>") + len(t)
		}
		return k
	}
	for i, r := range s {
		switch {
		case r == '<':
			inTag = true
			if end := strings.IndexByte(s[i:], '>'); end > 0 {
				tag := s[i+1 : i+end]
				if name, closing := strings.CutPrefix(tag, "/"); closing {
					if k := len(open); k > 0 && open[k-1] == name {
						open = open[:k-1]
					}
				} else {
					open = append(open, tag)
				}
			}
		case r == '>' && inTag:
			inTag = false
		case r == '&' && !inTag:
			inEntity = true
		case r == ';' && inEntity:
			inEntity = false
		}
		if inTag || inEntity {
			continue
		}
		end := i + utf8.RuneLen(r)
		if end+closers(open) > n {
			break
		}
		cut, kept = end, slices.Clone(open)
	}
	var b strings.Builder
	b.WriteString(s[:cut])
	for i := len(kept) - 1; i >= 0; i-- {
		b.WriteString("</" + kept[i] + ">")
	}
	return b.String()
}

// limitTargets drops the last drop target lines for an "and N more" line.
func (f Formatter) limitTargets(lines []string, drop int) []string {
	if drop == 0 {
		return lines
	}
	targets := 0
	for _, l := range lines {
		if strings.HasPrefix(l, "• ") {
			targets++
		}
	}
	drop = min(drop, targets)
	out := make([]string, 0, len(lines))
	seen := 0
	for _, l := range lines {
		if strings.HasPrefix(l, "• ") {
			seen++
			if seen > targets-drop {
				if seen == targets-drop+1 {
					out = append(out, fmt.Sprintf("• and %d more %s", drop, map[bool]string{true: "target", false: "targets"}[drop == 1]))
				}
				continue
			}
		}
		out = append(out, l)
	}
	return out
}

// of is what the summary's times are out of: the dark hours, or the whole
// window when they are not measured.
func (f Formatter) of(r Report) string {
	if r.Dark != nil {
		return " of " + hm(*r.Dark) + " dark"
	}
	return " of the whole " + hm(r.End.Sub(r.Start)) + " (dark hours " + notMeasured + ")"
}

func (f Formatter) summary(r Report) string {
	safe := "Safe " + notMeasured
	if r.Safe != nil {
		safe = "Safe " + hm(*r.Safe) + f.of(r)
	}
	flap := "flap open " + notMeasured
	if r.FlapOpen != nil {
		flap = "flap open " + hm(*r.FlapOpen)
	}
	imaged := "imaged " + notMeasured
	if r.Imaged != nil {
		imaged = "imaged " + hm(*r.Imaged)
	}
	if r.Safe == nil {
		imaged += f.of(r)
	}
	return safe + " · " + flap + " · " + imaged
}

func (f Formatter) idle(r Report) string {
	why := "nothing imaged"
	switch {
	case r.Safe == nil:
		why += "; safety " + notMeasured
	case r.SafetyConnected != nil && *r.SafetyConnected == 0:
		why += "; the safety monitor never connected"
	case *r.Safe == 0 && r.Dark != nil:
		why += "; unsafe all night"
	default:
		why += "; safe for " + hm(*r.Safe) + f.of(r)
		if r.FlapOpen != nil {
			why += fmt.Sprintf(" with the flap open %s", hm(*r.FlapOpen))
		}
		why += ", but no subs were taken"
	}
	out := f.title(r) + ": " + why + "."
	if len(r.Unsafe)+len(r.Restarts) > 0 {
		out += "\n" + f.unsafe(r)
	}
	if d := f.dust(r); d != "" {
		out += "\n" + d
	}
	if c := f.claims(r); c != "" {
		out += "\n" + c
	}
	return out
}

func filterRank(name string) int {
	switch strings.ToLower(name) {
	case "luminance", "l":
		return 0
	case "red", "r":
		return 1
	case "green", "g":
		return 2
	case "blue", "b":
		return 3
	case "h-a", "ha", "h-alpha":
		return 4
	case "o-iii", "oiii":
		return 5
	case "s-ii", "sii":
		return 6
	}
	return 7
}

func (f Formatter) subs(r Report) []string {
	if r.Subs == nil {
		return []string{"<b>Subs</b> " + notMeasured}
	}
	s := r.Subs
	head := fmt.Sprintf("<b>Subs</b> %d kept, %d rejected", s.Kept, s.Rejected)
	if s.Pending > 0 {
		head += fmt.Sprintf(", %d not stacked yet", s.Pending)
	}
	out := []string{head}
	for _, t := range s.Targets {
		parts := make([]string, 0, len(t.Filters))
		for _, fs := range t.Filters {
			p := fmt.Sprintf("%s %d", esc(fs.Filter), fs.Kept)
			var extra []string
			if fs.Rejected > 0 {
				extra = append(extra, fmt.Sprintf("%d rejected", fs.Rejected))
			}
			if fs.Pending > 0 {
				extra = append(extra, fmt.Sprintf("%d waiting", fs.Pending))
			}
			if len(extra) > 0 {
				p += " (" + strings.Join(extra, ", ") + ")"
			}
			parts = append(parts, p)
		}
		out = append(out, "• "+esc(t.Object)+": "+strings.Join(parts, ", "))
	}
	if len(s.Reasons) > 0 {
		parts := make([]string, 0, 3)
		for _, c := range s.Reasons[:min(3, len(s.Reasons))] {
			parts = append(parts, fmt.Sprintf("%d %s", c.N, esc(c.Label)))
		}
		out = append(out, "Top rejection reasons: "+strings.Join(parts, ", "))
	}
	return out
}

func (f Formatter) closeTextAt(e UnsafeEvent, end time.Time) string {
	d, ok := e.Latency()
	if !ok {
		return "<b>flap still open</b> at " + f.clock(end)
	}
	s := "flap closed after " + ms(d)
	if d > SlowClose {
		s += " <b>(over 30s)</b>"
	}
	return s
}

// maxUnsafe is how many unsafe events, and how many NINA restarts, the
// message lists.
const maxUnsafe = 10

func (f Formatter) unsafe(r Report) string {
	if r.UnsafeMissing != "" {
		return "<b>Unsafe</b> " + notMeasured
	}
	if len(r.Unsafe) == 0 && len(r.Restarts) == 0 {
		return "<b>Unsafe</b> none while the flap was open"
	}
	lines := []string{"<b>Unsafe</b>"}
	shown := r.Unsafe[:min(len(r.Unsafe), maxUnsafe)]
	for _, e := range shown {
		lines = append(lines, "• "+f.clock(e.At)+", "+f.closeTextAt(e, r.End))
	}
	if n := len(r.Unsafe) - len(shown); n > 0 {
		slow, open := 0, 0
		for _, e := range r.Unsafe[len(shown):] {
			if d, ok := e.Latency(); !ok {
				open++
			} else if d > SlowClose {
				slow++
			}
		}
		lines = append(lines, fmt.Sprintf("• and %d more, %d closed in over 30s, %d left open", n, slow, open))
	}
	restarts := r.Restarts[:min(len(r.Restarts), maxUnsafe)]
	for _, e := range restarts {
		closed := "<b>flap still open</b> at " + f.clock(r.End)
		if d, ok := e.Latency(); ok {
			closed = "flap closed after " + ms(d)
		}
		lines = append(lines, "• NINA restarted at "+f.clock(e.At)+" with the flap open, "+closed)
	}
	if n := len(r.Restarts) - len(restarts); n > 0 {
		lines = append(lines, fmt.Sprintf("• and %s with the flap open", plural(n, "more NINA restart", "more NINA restarts")))
	}
	if len(r.Unsafe) == 0 {
		lines = append(lines, "• no unsafe readings while the flap was open")
	}
	return strings.Join(lines, "\n")
}

func (f Formatter) dew(r Report) string {
	if r.Dew == nil {
		return "<b>Dew</b> " + notMeasured
	}
	d := r.Dew
	where := "while tracking"
	switch {
	case !d.TrackingMeasured:
		where = "(mount tracking " + notMeasured + ", so over the whole window)"
	case !d.Tracking:
		where = "(the mount never tracked)"
	}
	s := fmt.Sprintf("<b>Dew</b> lowest margin %s °C at %s %s", num(d.Lowest, 1), f.clock(d.LowestAt), where)
	if d.ImagingLow != nil && d.ImagingHigh != nil {
		s += fmt.Sprintf(" (%s–%s °C while imaging)", num(*d.ImagingLow, 1), num(*d.ImagingHigh, 1))
	}
	if d.Under == 0 {
		return s + fmt.Sprintf("; never under %s °C, so no heater boost was needed", num(BoostMargin, 0))
	}
	s += fmt.Sprintf("; under %s °C for %s", num(BoostMargin, 0), hm(d.Under))
	switch {
	case d.Boosted:
		s += ". Heaters boosted to 100%"
	case d.HeaterMax == nil:
		s += ". Heater output " + notMeasured
	default:
		s += fmt.Sprintf(". Heaters <b>did not boost</b> to 100%% (highest %s%%)", num(*d.HeaterMax, 0))
	}
	return s
}

// maxAutofocus is how many autofocus runs the message lists by time.
const maxAutofocus = 12

func (f Formatter) autofocus(r Report) string {
	if r.AutofocusMissing != "" {
		return "<b>Autofocus</b> " + notMeasured
	}
	if len(r.Autofocus) == 0 {
		return "<b>Autofocus</b> no runs"
	}
	failed, loops := 0, 0
	times := make([]string, 0, min(len(r.Autofocus), maxAutofocus))
	for i, a := range r.Autofocus {
		at := a.Finished
		if a.Started != nil {
			at = *a.Started
		}
		var notes []string
		if !a.Success {
			failed++
			notes = append(notes, "failed")
		}
		if a.BackToBack {
			loops++
			notes = append(notes, "right after the last")
		}
		if i >= maxAutofocus {
			continue
		}
		t := at.In(f.Loc).Format("15:04")
		if len(notes) > 0 {
			t += " (" + strings.Join(notes, ", ") + ")"
		}
		times = append(times, t)
	}
	if n := len(r.Autofocus) - len(times); n > 0 {
		times = append(times, fmt.Sprintf("and %d more", n))
	}
	s := "<b>Autofocus</b> " + plural(len(r.Autofocus), "run", "runs")
	switch {
	case failed == 0 && loops == 0:
		s += ", all good"
	default:
		var bad []string
		if failed > 0 {
			bad = append(bad, fmt.Sprintf("<b>%d failed</b>", failed))
		}
		if loops > 0 {
			bad = append(bad, fmt.Sprintf("<b>%d back to back</b>", loops))
		}
		s += ", " + strings.Join(bad, ", ")
	}
	return s + ": " + strings.Join(times, ", ") + " " + f.Zone
}

func (f Formatter) guiding(r Report) string {
	if r.Guiding == nil {
		return "<b>Guiding</b> " + notMeasured
	}
	g := r.Guiding
	s := fmt.Sprintf("<b>Guiding</b> median RMS %s″", num(g.Median, 2))
	if !g.WorstMeasured {
		return s + ", worst 10 min " + notMeasured + " (too few readings)"
	}
	return s + fmt.Sprintf(", worst 10 min %s″ at %s", num(g.Worst, 2), f.span(g.WorstAt, g.WorstEnd))
}

// maxBursts is how many guider error bursts the message lists.
const maxBursts = 5

func (f Formatter) guiderErrors(r Report) string {
	if r.GuiderErrorsMissing != "" {
		return "<b>Guider errors</b> " + notMeasured
	}
	if len(r.GuiderErrors) == 0 {
		return "<b>Guider errors</b> none"
	}
	size := func(b ErrorBurst) (n int) {
		for _, k := range b.Kinds {
			n += k.N
		}
		return n
	}
	total := 0
	for _, b := range r.GuiderErrors {
		total += size(b)
	}
	shown := slices.Clone(r.GuiderErrors)
	if len(shown) > maxBursts {
		sort.SliceStable(shown, func(i, j int) bool { return size(shown[i]) > size(shown[j]) })
		shown = shown[:maxBursts]
		sort.SliceStable(shown, func(i, j int) bool { return shown[i].From.Before(shown[j].From) })
	}
	lines := []string{fmt.Sprintf("<b>Guider errors</b> %d in %s", total, plural(len(r.GuiderErrors), "burst", "bursts"))}
	rest := total
	for _, b := range shown {
		kinds := make([]string, 0, len(b.Kinds))
		for _, k := range b.Kinds {
			kinds = append(kinds, fmt.Sprintf("%d %s", k.N, esc(k.Label)))
		}
		lines = append(lines, "• "+f.span(b.From, b.To)+": "+strings.Join(kinds, "; "))
		rest -= size(b)
	}
	if n := len(r.GuiderErrors) - len(shown); n > 0 {
		lines = append(lines, fmt.Sprintf("• %d more in %s", rest, plural(n, "smaller burst", "smaller bursts")))
	}
	return strings.Join(lines, "\n")
}

func (f Formatter) camera(r Report) string {
	c := r.Camera
	set := "setpoint " + notMeasured
	if len(c.Setpoints) > 0 {
		parts := make([]string, 0, len(c.Setpoints))
		for _, v := range c.Setpoints {
			parts = append(parts, fmt.Sprintf("%s °C (%s)", num(v.C, 0), plural(v.Subs, "sub", "subs")))
		}
		set = "setpoint " + strings.Join(parts, ", ")
		if len(parts) > 1 {
			set = "setpoints " + strings.Join(parts, ", ")
		}
	}
	cool := "cooler power " + notMeasured
	if c.CoolerMin != nil && c.CoolerMax != nil {
		if math.Round(*c.CoolerMin) == math.Round(*c.CoolerMax) {
			cool = "cooler power " + num(*c.CoolerMax, 0) + "%"
		} else {
			cool = "cooler power " + num(*c.CoolerMin, 0) + "–" + num(*c.CoolerMax, 0) + "%"
		}
	}
	return "<b>Camera</b> " + set + "; " + cool
}

// Caption is a photo's caption.
func (f Formatter) Caption(im Image) string {
	total := " · " + hm(im.Exposure) + " total"
	if im.ExposureMissing {
		total = " · total exposure " + notMeasured
	}
	tail := total + " · +" + plural(im.NewSubs, "sub", "subs") + " last night"
	switch {
	case im.CometSubs > 0:
		tail = " · " + f.cometNote(im) + tail
	case im.ByNight:
		tail += " (a moving target: last night's subs are in that night's own stack, not this image)"
	case !im.Current:
		tail += " (this stack does not include last night yet)"
	}
	label := " · " + esc(im.Label)
	s := "<b>" + esc(im.Object) + "</b>" + label + tail
	if len(s) <= MaxCaption {
		return s
	}
	name := []rune(im.Object)
	for len(name) > 1 && len("<b>…</b>")+len(esc(string(name)))+len(label)+len(tail) > MaxCaption {
		name = name[:len(name)-1]
	}
	s = "<b>" + esc(string(name)) + "…</b>" + label + tail
	if len(s) > MaxCaption {
		return cutText(s, MaxCaption-len("…")) + "…"
	}
	return s
}

// cometNote says what a comet cover is: the comet stacked on itself, and
// from which nights.
func (f Formatter) cometNote(im Image) string {
	n := len(im.CometNights)
	switch {
	case n == 0:
		return "the comet stacked on itself from " + plural(im.CometSubs, "sub", "subs") + "; which nights they are from is " + notMeasured
	case n == 1 && im.Current:
		return "the comet stacked on itself from last night's subs alone"
	}
	days := make([]string, 0, n)
	for _, d := range im.CometNights {
		days = append(days, d.Format("Jan 2"))
	}
	s := "the comet stacked on itself from " + plural(n, "night", "nights") + " (" + strings.Join(days, ", ") + ")"
	if im.Current {
		return s + ", last night included"
	}
	return s + ", <b>last night not in it yet</b>"
}
