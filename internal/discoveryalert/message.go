package discoveryalert

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

// ClassWords names a class in plain words.
func ClassWords(class string) string {
	switch class {
	case discoveries.ClassSupernova:
		return "possible supernova"
	case ClassNova:
		return "possible nova"
	case discoveries.ClassNewVariable:
		return "possible new variable star"
	case discoveries.ClassUnknownMover:
		return "unknown moving object"
	}
	return strings.ReplaceAll(class, "_", " ")
}

func esc(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

// RA formats a right ascension in degrees as hours, minutes and seconds to
// 0.01 s, about 0.1".
func RA(deg float64) string {
	deg = math.Mod(math.Mod(deg, 360)+360, 360)
	cs := int64(math.Round(deg / 15 * 3600 * 100))
	cs %= 24 * 3600 * 100
	h := cs / 360000
	m := cs / 6000 % 60
	s := float64(cs%6000) / 100
	return fmt.Sprintf("%02dh %02dm %05.2fs", h, m, s)
}

// Dec formats a declination in degrees as degrees, minutes and seconds to
// 0.1".
func Dec(deg float64) string {
	sign := "+"
	if deg < 0 {
		sign = "-"
	}
	ds := int64(math.Round(math.Abs(deg) * 36000))
	d := ds / 36000
	m := ds / 600 % 60
	s := float64(ds%600) / 10
	return fmt.Sprintf("%s%02d° %02d′ %04.1f″", sign, d, m, s)
}

// NightLink is the discoveries page of a night.
func NightLink(webURL string, night time.Time) string {
	if webURL == "" {
		return ""
	}
	return strings.TrimSuffix(webURL, "/") + "/discoveries?night=" + night.Format(time.DateOnly)
}

func matches(c app.DiscoveryCandidate) []discoveries.Match {
	var ms []discoveries.Match
	if c.Matches != "" {
		_ = json.Unmarshal([]byte(c.Matches), &ms)
	}
	return ms
}

// host is the galaxy a change lies in, if any.
func host(c app.DiscoveryCandidate) string {
	for _, m := range matches(c) {
		if m.Catalog == "galaxy" && m.Name != "" {
			return m.Name
		}
	}
	return ""
}

func brightness(c app.DiscoveryCandidate) string {
	if c.Kind == app.DiscoveryKindMover {
		parts := []string{}
		if finite(c.NightMag) {
			parts = append(parts, fmt.Sprintf("magnitude %.1f", *c.NightMag))
		}
		if finite(c.RateArcmin) {
			parts = append(parts, fmt.Sprintf("moving %.2f″ a minute", *c.RateArcmin))
		}
		if len(parts) == 0 {
			return "brightness not measured"
		}
		return strings.Join(parts, ", ")
	}
	var b strings.Builder
	m := discoveries.NewLight(c)
	switch {
	case finite(m) && finite(c.RefMag):
		fmt.Fprintf(&b, "magnitude %.2f of new light (%.2f with what was there)", *m, *c.NightMag)
	case finite(m):
		fmt.Fprintf(&b, "magnitude %.2f", *m)
	case finite(c.NightMag):
		fmt.Fprintf(&b, "magnitude %.2f at the spot", *c.NightMag)
	default:
		b.WriteString("magnitude not measured")
	}
	switch {
	case finite(c.DeltaMag) && finite(c.RefMag):
		word := "brighter"
		if *c.DeltaMag > 0 {
			word = "fainter"
		}
		fmt.Fprintf(&b, ", %.2f magnitudes %s than the reference (%.2f)", math.Abs(*c.DeltaMag), word, *c.RefMag)
	case c.Sign > 0:
		b.WriteString(", nothing measured there in the reference")
	default:
		b.WriteString(", fainter than the reference")
	}
	return b.String()
}

// what names a candidate in a list: its class, or for a known object we
// have early data on, its TNS label.
func what(c app.DiscoveryCandidate) string {
	if t, ok := EarlyKnown(c); ok {
		return discoveries.KnownText(t) + "; early data"
	}
	return ClassWords(c.Class)
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// Caption is one candidate's alert text, as Telegram HTML.
func Caption(c app.DiscoveryCandidate, webURL string) string {
	lines := []string{
		fmt.Sprintf("<b>New find: %s in %s</b>", esc(ClassWords(c.Class)), esc(c.Object)),
		fmt.Sprintf("Night of %s, %s filters", c.Night.Format("Mon Jan 2"), esc(c.Group)),
		esc(brightness(c)),
		fmt.Sprintf("RA %s, Dec %s", RA(c.RA), Dec(c.Dec)),
	}
	if t, ok := EarlyKnown(c); ok {
		lines[0] = fmt.Sprintf("<b>Early data on %s in %s</b>", esc(t.Name), esc(c.Object))
		lines = append(lines[:1], append([]string{esc(upperFirst(discoveries.KnownText(t))), esc(discoveries.EarlyText)}, lines[1:]...)...)
	}
	if h := host(c); h != "" && c.Kind != app.DiscoveryKindMover {
		lines = append(lines, "In "+esc(h))
	}
	if c.SubsTested > 0 {
		lines = append(lines, fmt.Sprintf("Seen in %d of %d subs", c.PresentIn, c.SubsTested))
	}
	lines = append(lines, "Pictures: this night, the reference, the difference")
	if u := NightLink(webURL, c.Night); u != "" {
		lines = append(lines, `<a href="`+esc(u)+`">Check it on astro-processing</a>`)
	}
	return strings.Join(lines, "\n")
}

// GroupText is the message that leads several candidates of one run, sent
// before their pictures.
func GroupText(cands []app.DiscoveryCandidate, webURL string) string {
	c0 := cands[0]
	head := "%d new finds in %s"
	if slices.ContainsFunc(cands, func(c app.DiscoveryCandidate) bool { _, early := EarlyKnown(c); return early }) {
		head = "%d finds in %s"
	}
	lines := []string{fmt.Sprintf("<b>"+head+"</b>", len(cands), esc(c0.Object)),
		fmt.Sprintf("Night of %s, %s filters", c0.Night.Format("Mon Jan 2"), esc(c0.Group))}
	for i, c := range cands {
		line := fmt.Sprintf("%d. %s: %s; RA %s, Dec %s", i+1, esc(what(c)), esc(brightness(c)), RA(c.RA), Dec(c.Dec))
		if h := host(c); h != "" && c.Kind != app.DiscoveryKindMover {
			line += "; in " + esc(h)
		}
		if c.SubsTested > 0 {
			line += fmt.Sprintf("; seen in %d of %d subs", c.PresentIn, c.SubsTested)
		}
		lines = append(lines, line)
	}
	lines = append(lines, "Pictures follow: this night, the reference, the difference")
	if u := NightLink(webURL, c0.Night); u != "" {
		lines = append(lines, `<a href="`+esc(u)+`">Check them on astro-processing</a>`)
	}
	return strings.Join(lines, "\n")
}

// ShortCaption labels one picture of a group.
func ShortCaption(i int, c app.DiscoveryCandidate) string {
	return fmt.Sprintf("%d. %s, RA %s, Dec %s", i+1, esc(what(c)), RA(c.RA), Dec(c.Dec))
}

// SummaryText is the one message for a run with more than MaxPerRun new
// candidates, total in all, of which cands were not alerted before.
func SummaryText(cands []app.DiscoveryCandidate, total int, webURL string) string {
	c0 := cands[0]
	counts := map[string]int{}
	order := []string{}
	for _, c := range cands {
		if counts[c.Class] == 0 {
			order = append(order, c.Class)
		}
		counts[c.Class]++
	}
	parts := make([]string, 0, len(order))
	for _, cl := range order {
		parts = append(parts, fmt.Sprintf("%d %s", counts[cl], esc(ClassWords(cl))))
	}
	lines := []string{
		fmt.Sprintf("<b>%d new finds in %s in one night</b>", total, esc(c0.Object)),
		fmt.Sprintf("Night of %s, %s filters", c0.Night.Format("Mon Jan 2"), esc(c0.Group)),
	}
	if len(cands) < total {
		lines = append(lines, fmt.Sprintf("Not alerted before: %s", strings.Join(parts, ", ")))
	} else {
		lines = append(lines, strings.Join(parts, ", "))
	}
	lines = append(lines, "So many at once is most often a bad night or a fault, so they are not listed one by one and no follow-up is asked for.")
	if u := NightLink(webURL, c0.Night); u != "" {
		lines = append(lines, `<a href="`+esc(u)+`">Check them on astro-processing</a>`)
	}
	return strings.Join(lines, "\n")
}
