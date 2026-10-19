// Package discoverywatch keeps the new finds the vet calls real at low
// confidence in Watching: it records when each watch started and its fade
// nights, asks Target Scheduler for the field on a few nights in the gaps
// between planned targets, and moves the find out once the evidence
// settles.
//
// A find enters Watching once its latest vet calls it real at low
// confidence with no survey answer pending. While watched, the vet runs it
// again weekly with fresh survey data, as it does the finds the user marked
// real. It leaves for Needs a look once FadesToLeave more fade nights than
// at the start show up, in the user's own frames or in ZTF with nearby
// stars steady, or once the vet rises to medium or high confidence; it
// leaves for Likely artifacts once the vet calls it an artifact. Imaging
// requests stop after QuietStop with no new fade, or when the user asks.
package discoverywatch

import (
	"encoding/json"
	"slices"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoverysource"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting"
)

const (
	// FadesToLeave is how many fade nights not among those at the start
	// move a find to Needs a look.
	FadesToLeave = 2
	// QuietStop is how long without a new fade night before the watch
	// stops asking for imaging.
	QuietStop = 90 * 24 * time.Hour
)

// FadeNights are a find's fade nights by date, each night once: the
// nights its change was found in the user's own frames, plus the ZTF
// nights, from one up, on which it fades beyond nearby stars that stay
// steady. known is false until a vet report listed the ZTF nights.
func FadeNights(sum discoverysource.Summary, rep *vetting.Report) ([]string, bool) {
	if rep == nil || !rep.FadesRead || !rep.FadeNightsRead {
		return nil, false
	}
	out := []string{}
	for _, m := range sum.Members {
		out = append(out, m.Night.Format(time.DateOnly))
	}
	out = append(out, rep.ZTFFadeNights...)
	slices.Sort(out)
	return slices.Compact(out), true
}

// newNights counts the nights not among those at the start.
func newNights(w app.DiscoveryWatch, nights []string) int {
	var start []string
	_ = json.Unmarshal([]byte(w.StartNights), &start)
	n := 0
	for _, d := range nights {
		if !slices.Contains(start, d) {
			n++
		}
	}
	return n
}

// setStart takes nights as the watch's fade nights at the start.
func setStart(w *app.DiscoveryWatch, nights []string) {
	b, _ := json.Marshal(nights)
	n, f := len(nights), len(nights)
	w.StartNights, w.FadesAtStart, w.Fades = string(b), &n, &f
}

// Outcome is what one look at a find does to its watch.
type Outcome struct {
	Watch app.DiscoveryWatch
	// Changed says Watch must be saved; Started that it began now.
	Changed bool
	Started bool
	// Cancel says open imaging requests are withdrawn.
	Cancel bool
}

// Advance applies the rules to a find and its watch, have nil when it was
// never watched. A find that left Watching for a reason other than its
// fade nights and comes back takes up its old watch again, with the fade
// nights at its first start and its quiet clock, so a find that goes back
// and forth between verdicts is not imaged again each time. nights are its
// fade nights when known. A watch from before the start nights were kept
// takes the nights of its next look as its start. It says false when the
// find has no watch and needs none.
func Advance(sum discoverysource.Summary, have *app.DiscoveryWatch, nights []string, known bool, now time.Time) (Outcome, bool) {
	fades := len(nights)
	if have == nil || have.EndedAt != nil {
		if !sum.Watching() {
			if have != nil {
				return Outcome{Watch: *have}, true
			}
			return Outcome{}, false
		}
		if have != nil {
			w := *have
			w.EndedAt, w.EndReason, w.UpdatedAt = nil, "", now
			return Outcome{Watch: w, Changed: true}, true
		}
		w := app.DiscoveryWatch{SourceID: sum.Source.ID, StartedAt: now, Group: sum.Best.Group, LastFadeAt: now, CreatedAt: now, UpdatedAt: now}
		if known {
			setStart(&w, nights)
		}
		return Outcome{Watch: w, Changed: true, Started: true}, true
	}
	w := *have
	out := Outcome{Watch: w}
	if known {
		switch {
		case w.FadesAtStart == nil || w.StartNights == "":
			setStart(&w, nights)
			out.Changed = true
		case w.Fades == nil || fades != *w.Fades:
			if w.Fades == nil || fades > *w.Fades {
				w.LastFadeAt = now
			}
			w.Fades = &fades
			out.Changed = true
		}
	}
	end := ""
	switch {
	case sum.Source.Vetting != "":
		end = app.WatchEndUser
	case sum.LikelyArtifact:
		end = app.WatchEndArtifact
	case known && newNights(w, nights) >= FadesToLeave:
		end = app.WatchEndFades
	case sum.Vet != nil && sum.Vet.Suggested == app.VetReal && sum.Vet.Confidence != discoverysource.ConfidenceLow && sum.Vet.Confidence != "":
		end = app.WatchEndRose
	case !sum.Watching():
		end = app.WatchEndOther
	}
	if end != "" {
		w.EndedAt, w.EndReason = &now, end
		out.Changed, out.Cancel = true, true
	} else if w.ImagingStoppedAt == nil && now.Sub(w.LastFadeAt) >= QuietStop {
		w.ImagingStoppedAt, w.StopReason = &now, app.WatchStopQuiet
		out.Changed, out.Cancel = true, true
	}
	if out.Changed {
		w.UpdatedAt = now
	}
	out.Watch = w
	return out, true
}

// Needed is the fade night count that moves a watch to Needs a look, nil
// until the count at the start is known.
func Needed(w *app.DiscoveryWatch) *int {
	if w == nil || w.FadesAtStart == nil {
		return nil
	}
	n := *w.FadesAtStart + FadesToLeave
	return &n
}

// Asking says the watch still asks for imaging.
func Asking(w *app.DiscoveryWatch) bool {
	return w != nil && w.EndedAt == nil && w.ImagingStoppedAt == nil
}
