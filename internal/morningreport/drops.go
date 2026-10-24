package morningreport

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// DropQuery selects the lines equipment drops are read from: a device NINA
// held reporting itself gone, the exposures after it, and NINA closing.
const DropQuery = `{job="observatory", app="nina"} |~ "should be connected but reports to be disconnected|(Starting|Finishing) Category: [^,]*, Item: TakeExposure|Item: TakeExposure.* - Camera not connected|Application shutting down"`

const (
	msgLost       = " should be connected but reports to be disconnected"
	msgExposure   = "Item: TakeExposure"
	msgCameraGone = "Camera not connected"
)

// DropGap is the longest time between lost devices that still counts as
// one drop.
const DropGap = 10 * time.Minute

// HangMargin is how far past its length an exposure may run before it
// counts as hung.
const HangMargin = 5 * time.Minute

// Drop is equipment NINA lost while it held it, and what happened to the
// exposures after it until NINA closed.
type Drop struct {
	At      time.Time
	Devices []string
	// Hung are the exposures that ran more than HangMargin past their
	// length.
	Hung []Hang
	// Refused counts the exposures NINA refused because the camera was not
	// connected, from RefusedFrom to RefusedTo.
	Refused                int
	RefusedFrom, RefusedTo time.Time
}

// Hang is an exposure that ran long: when it started and ended, and the
// length it asked for.
type Hang struct {
	From, To time.Time
	Length   time.Duration
}

// Took is how long the exposure ran.
func (h Hang) Took() time.Duration { return h.To.Sub(h.From) }

var exposureTime = regexp.MustCompile(`ExposureTime ([0-9.]+)`)

// message is the text after a NINA line's fields.
func message(text string) string {
	first, _, _ := strings.Cut(text, "\n")
	parts := strings.SplitN(first, "|", 6)
	return strings.TrimSpace(parts[len(parts)-1])
}

// Drops reads the times NINA lost equipment it held from its log, oldest
// first. A drop lasts until NINA closes; the exposures in it that hung or
// were refused for a missing camera are counted against it.
func Drops(lines []Line) []Drop {
	var out []Drop
	active := false
	var lastLost, started time.Time
	var length time.Duration
	pending := false
	for _, l := range lines {
		msg := message(l.Text)
		switch {
		case strings.Contains(msg, msgShutdown):
			active, pending = false, false
		case strings.Contains(msg, msgLost):
			if !active || l.T.Sub(lastLost) > DropGap {
				out = append(out, Drop{At: l.T})
				active = true
			}
			lastLost = l.T
			d := &out[len(out)-1]
			name, _, _ := strings.Cut(msg, msgLost)
			if name = strings.TrimSpace(name); name != "" && !slices.Contains(d.Devices, name) {
				d.Devices = append(d.Devices, name)
			}
		case !strings.Contains(msg, msgExposure):
		case strings.Contains(msg, msgCameraGone):
			pending = false
			if !active {
				continue
			}
			d := &out[len(out)-1]
			if d.Refused == 0 {
				d.RefusedFrom = l.T
			}
			d.Refused++
			d.RefusedTo = l.T
		case strings.HasPrefix(msg, "Starting "):
			started, length, pending = l.T, 0, true
			if m := exposureTime.FindStringSubmatch(msg); m != nil {
				if s, err := strconv.ParseFloat(m[1], 64); err == nil {
					length = time.Duration(s * float64(time.Second))
				}
			}
		case strings.HasPrefix(msg, "Finishing "):
			if pending && active && length > 0 && l.T.Sub(started) > length+HangMargin {
				d := &out[len(out)-1]
				d.Hung = append(d.Hung, Hang{From: started, To: l.T, Length: length})
			}
			pending = false
		}
	}
	return out
}
