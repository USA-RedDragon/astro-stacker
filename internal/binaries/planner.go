package binaries

import (
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/sky"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/period"
)

// Purposes of a planned eclipse.
const (
	PurposeFlag   = "flag"
	PurposePeriod = "period"
	PurposeOwn    = "own"
)

// RequestView is an imaging request as the planner shows it.
type RequestView struct {
	ID    int    `json:"id"`
	State string `json:"state"`
	Note  string `json:"note"`
	// Auto says the app asked for it, for the star's seasonal timings.
	Auto bool `json:"auto"`
}

// TimeSpan is a UTC span.
type TimeSpan struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// PlannedEclipse is one row of the eclipse planner.
type PlannedEclipse struct {
	SourceID       int          `json:"source_id"`
	Name           string       `json:"name"`
	Field          string       `json:"field"`
	Magnitude      string       `json:"magnitude"`
	PeriodText     string       `json:"period_text"`
	AltPeriodText  string       `json:"alt_period_text"`
	Kind           string       `json:"kind"`
	KindLabel      string       `json:"kind_label"`
	Mid            time.Time    `json:"mid"`
	DayLabel       string       `json:"day_label"`
	TimeLabel      string       `json:"time_label"`
	Zone           string       `json:"zone"`
	ErrMinutes     float64      `json:"err_minutes"`
	DurationHours  *float64     `json:"duration_hours"`
	Observable     bool         `json:"observable"`
	ObservableNote string       `json:"observable_note"`
	Purpose        string       `json:"purpose"`
	Why            string       `json:"why"`
	Window         TimeSpan     `json:"window"`
	Request        *RequestView `json:"request"`
}

// EclipsePlan is the planner page.
type EclipsePlan struct {
	From      time.Time        `json:"from"`
	To        time.Time        `json:"to"`
	ZoneNote  string           `json:"zone_note"`
	SiteKnown bool             `json:"site_known"`
	Eclipses  []PlannedEclipse `json:"eclipses"`
	Hidden    int              `json:"hidden"`
}

// PlanInput is what the planner needs.
type PlanInput struct {
	Stars    []Star
	Site     *sky.Site
	Loc      *time.Location
	From, To time.Time
	All      bool
	Requests []app.EclipseRequest
}

// maxHalfWindow is how far before now an eclipse's middle may be while
// its imaging window is still open.
const maxHalfWindow = 7 * 24 * time.Hour

// requestMatch is how close a request's middle must be to a prediction's
// to be its request.
const requestMatch = 2 * time.Hour

// Plan lists the predicted eclipses of every star, observable ones only
// unless All.
func Plan(in PlanInput) EclipsePlan {
	loc := in.Loc
	if loc == nil {
		loc = time.UTC
	}
	out := EclipsePlan{From: in.From, To: in.To, ZoneNote: ZoneNote(loc, in.From, in.To), SiteKnown: in.Site != nil, Eclipses: []PlannedEclipse{}}
	for _, s := range in.Stars {
		e, dbl := s.ephemeris()
		if e == nil || s.Facts == nil || s.Facts.Gaia == nil {
			continue
		}
		b := s.View(nil)
		mid := in.From.Add(in.To.Sub(in.From) / 2)
		ra, dec := s.Facts.Gaia.Now(mid)
		for _, p := range Predict(e, s.Facts.Deeper, ra, dec, in.From.Add(-maxHalfWindow), in.To) {
			p = s.withPhaseError(p, e)
			if _, end := p.Window(); !end.After(in.From) {
				continue
			}
			day, clock, zone := Labels(p.Mid, loc)
			row := PlannedEclipse{SourceID: s.Source.ID, Name: b.Name, Field: b.Field, Magnitude: b.Magnitude, PeriodText: b.PeriodText,
				Kind: p.Kind, KindLabel: kindLabel(p, dbl && s.Facts.Alike), Mid: p.Mid, DayLabel: day, TimeLabel: clock, Zone: zone,
				ErrMinutes: p.Err.Minutes()}
			if b.AltPeriodText != "" {
				row.AltPeriodText = "(" + b.AltPeriodText + ")"
			}
			if p.Duration > 0 {
				h := p.Duration.Hours()
				row.DurationHours = &h
			}
			ws, we := p.Window()
			row.Window = TimeSpan{Start: ws, End: we}
			if in.Site != nil {
				row.Observable, row.ObservableNote = Seen(*in.Site, ra, dec, p.Mid)
			} else {
				row.ObservableNote = "Site location not known yet"
			}
			row.Purpose, row.Why = purpose(s, b, p)
			row.Request = matchRequest(in.Requests, s.Source.ID, p)
			if !row.Observable && !in.All {
				out.Hidden++
				continue
			}
			out.Eclipses = append(out.Eclipses, row)
		}
	}
	slices.SortStableFunc(out.Eclipses, func(a, b PlannedEclipse) int { return a.Mid.Compare(b.Mid) })
	return out
}

func kindLabel(p Predicted, dbl bool) string {
	switch {
	case dbl && p.Kind == KindPrimary:
		return "Eclipse A (both equally deep)"
	case dbl:
		return "Eclipse B (both equally deep)"
	case p.Kind == KindPrimary:
		return "Deeper eclipse"
	}
	return "Second eclipse"
}

// purpose is why an eclipse is worth imaging: it settles a flag, else it
// adds the user's own eclipse to the VSX package, else it checks the
// period over time.
func purpose(s Star, b Binary, p Predicted) (string, string) {
	for _, f := range b.Flags {
		switch f.ID {
		case FlagTwice:
			return PurposeFlag, fmt.Sprintf("Settles %s or %s days. Catch two eclipses in a row: if their depths differ, it repeats every %s days.",
				b.PeriodText, periodText(2*b.Period, 0), periodText(2*b.Period, 0))
		case FlagHalf:
			return PurposeFlag, fmt.Sprintf("Settles %s or %s days. Catch two eclipses in a row and compare their depths: if they differ, %s days is right.",
				b.PeriodText, periodText(b.Period/2, 0), b.PeriodText)
		case FlagFew:
			if p.Kind == KindPrimary {
				return PurposeFlag, "Few eclipses on record (" + lowerFirst(f.Why) + ") A timed deeper eclipse pins its depth and checks the period."
			}
		}
	}
	if !s.ownEclipse() {
		why := "Adds your own eclipse to the VSX package."
		if slices.ContainsFunc(b.Flags, func(f Flag) bool { return f.ID == FlagBlend }) {
			why += " Your images cannot split the close pair either."
		}
		return PurposeOwn, why
	}
	return PurposePeriod, "Another timed eclipse to check the period over time."
}

// ownEclipse says the user's own frames already timed an eclipse.
func (s Star) ownEclipse() bool {
	c, ok := checkOf(s.Report, check.OwnTiming)
	return ok && c.Status == check.Pass
}

func matchRequest(reqs []app.EclipseRequest, id int, p Predicted) *RequestView {
	var best *app.EclipseRequest
	for i, r := range reqs {
		if r.SourceID != id || r.Kind != p.Kind {
			continue
		}
		if d := r.Mid.Sub(p.Mid); d < -requestMatch || d > requestMatch {
			continue
		}
		if best == nil || r.ID > best.ID {
			best = &reqs[i]
		}
	}
	if best == nil || best.State == app.EclipseCancel {
		return nil
	}
	return &RequestView{ID: best.ID, State: best.State, Note: best.Note, Auto: best.Auto}
}

// withPhaseError adds the second dip's own phase error to its timing
// error: its place in the cycle is fitted, not fixed at half.
func (s Star) withPhaseError(p Predicted, e *period.Ephemeris) Predicted {
	if p.Kind != KindSecondary || s.Facts == nil || s.Facts.Survey == nil || s.Facts.Survey.Twin == nil || s.Facts.Doubled {
		return p
	}
	if pe := s.Facts.Survey.Twin.PhaseErr; pe > 0 {
		p.Err = time.Duration(math.Hypot(p.Err.Seconds(), pe*e.Period*86400) * 1e9)
	}
	return p
}
