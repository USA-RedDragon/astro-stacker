package binaries

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/period"
	"gorm.io/gorm"
)

// tested says the report came from a pipeline that tests a found period
// against false periods, so its verdict can tell an eclipsing binary.
func tested(r *vetting.Report) bool {
	_, ok := checkOf(r, check.Echo)
	return ok
}

func passed(r *vetting.Report, id string) bool {
	c, ok := checkOf(r, id)
	return ok && c.Status == check.Pass
}

func failed(r *vetting.Report, id string) bool {
	c, ok := checkOf(r, id)
	return ok && c.Status == check.Fail
}

// echoCleared says a period next to a daily echo is shown by a record
// that nightly sampling cannot fool, or is not next to one.
func echoCleared(r *vetting.Report, p float64) bool {
	return period.EchoOf(p) == "" || passed(r, check.Echo) || passed(r, check.TESS)
}

// EclipsingBinary says the vet's latest report makes the star an
// eclipsing binary with a settled period: a real verdict that is an
// eclipsing binary, from a pipeline that tests periods against false
// ones, whose period is not a daily echo nothing confirms. A report from
// before those tests cannot tell and is not one until the star is vetted
// again.
func EclipsingBinary(r *vetting.Report) bool {
	if !Periodic(r) || r.Suggestion.State != app.VetReal || !tested(r) {
		return false
	}
	if r.Suggestion.Kind != "" && r.Suggestion.Kind != vetting.KindEclipsingBinary {
		return false
	}
	if r.Suggestion.Kind == "" && (failed(r, check.Echo) || failed(r, check.Compare)) && !passed(r, check.TESS) {
		return false
	}
	e, _ := adopt(r)
	return echoCleared(r, r.Ephemeris.Period) && echoCleared(r, e.Period)
}

// vetKind is what the vet's latest report makes a confirmed star that is
// not an eclipsing binary: periodic, fades, brightens, or empty when the
// report cannot tell.
func vetKind(r *vetting.Report) string {
	if r == nil || r.Suggestion.State != app.VetReal {
		return ""
	}
	switch k := r.Suggestion.Kind; k {
	case vetting.KindPeriodic:
		if r.Ephemeris != nil && r.Ephemeris.Period > 0 && echoCleared(r, r.Ephemeris.Period) {
			return k
		}
		return ""
	case vetting.KindFades, vetting.KindBrightens:
		return k
	case "":
	default:
		return ""
	}
	switch e := r.Ephemeris; {
	case e != nil && e.Kind != period.KindEclipsing && e.Period > 0 && tested(r) && echoCleared(r, e.Period):
		return vetting.KindPeriodic
	case e == nil && r.Fades.Confirms():
		return vetting.KindFades
	}
	return ""
}

// StarType is a kind of variable star the user can name a confirmed star
// that is not an eclipsing binary: Key is stored, Words name it in plain
// language, Verb says what its period is, and VSX is its VSX type, empty
// when VSX has no single one.
type StarType struct {
	Key   string `json:"key"`
	Words string `json:"words"`
	Verb  string `json:"verb"`
	VSX   string `json:"vsx"`
}

// StarTypes are the kinds the user can pick.
func StarTypes() []StarType {
	return []StarType{
		{Key: "rot", Words: "Spotted star", Verb: "rotates", VSX: "ROT"},
		{Key: "dipper", Words: "Dipper", Verb: "dips recur"},
		{Key: "pulsating", Words: "Pulsating star", Verb: "pulses"},
		{Key: "irregular", Words: "Irregular variable", Verb: "changes", VSX: "L"},
		{Key: "variable", Words: "Variable star", Verb: "repeats", VSX: "VAR"},
	}
}

func starType(key string) (StarType, bool) {
	for _, t := range StarTypes() {
		if t.Key == key {
			return t, true
		}
	}
	return StarType{}, false
}

// Other is one confirmed star that is not an eclipsing binary, as the
// pages show it.
type Other struct {
	SourceID   int       `json:"source_id"`
	Name       string    `json:"name"`
	AltName    string    `json:"alt_name"`
	Field      string    `json:"field"`
	Magnitude  string    `json:"magnitude"`
	VetKind    string    `json:"vet_kind"`
	VetLine    string    `json:"vet_line"`
	Waiting    string    `json:"waiting"`
	Period     float64   `json:"period"`
	PeriodText string    `json:"period_text"`
	Confidence string    `json:"confidence"`
	StarType   *StarType `json:"star_type"`
	VSX        VSXState  `json:"vsx"`
	Notes      string    `json:"notes"`
	Share      Share     `json:"share"`
}

// OtherView is how the pages show s, a confirmed star that is not an
// eclipsing binary; shareURL makes a share picture's link.
func (s Star) OtherView(shareURL func(id, version int) string) Other {
	name, alt := s.Names()
	o := Other{SourceID: s.Source.ID, Name: name, AltName: alt, Field: s.Source.Object, Magnitude: s.Magnitude(), Notes: s.Book.Notes,
		VSX: VSXState{Submitted: s.Book.VSXSubmitted, Ref: s.Book.VSXRef, SubmittedAt: s.Book.VSXSubmittedAt}}
	if t, ok := starType(s.Book.StarType); ok {
		o.StarType = &t
	}
	if s.Share != nil {
		o.Share = s.shareView(shareURL)
	} else {
		o.Share = Share{State: ShareNone}
	}
	r := s.Report
	o.VetKind = vetKind(r)
	switch o.VetKind {
	case vetting.KindPeriodic:
		o.Period = r.Ephemeris.Period
		o.PeriodText = periodText(r.Ephemeris.Period, r.Ephemeris.PeriodErr)
		o.VetLine = fmt.Sprintf("Repeats every %s days", o.PeriodText)
	case vetting.KindFades:
		o.VetLine = "Fades now and then"
	case vetting.KindBrightens:
		o.VetLine = "Brightens now and then"
	}
	switch {
	case r == nil:
		o.Waiting = "Not vetted yet."
	case o.VetKind != "":
		o.Confidence = r.Suggestion.Confidence
	case r.Suggestion.State == app.VetReal && !tested(r) && r.Ephemeris != nil:
		o.Waiting = "The vet's last look came before its period checks; it looks again soon."
	case r.Suggestion.State == app.VetReal && r.Ephemeris == nil:
		o.VetLine, o.Confidence = sayLine(r.Suggestion.Say), r.Suggestion.Confidence
	case r.Suggestion.State == app.VetReal:
		o.Waiting = "The vet found it real but named no kind."
	default:
		o.Waiting = "The vet is not sure about it; your verdict stands."
	}
	return o
}

// Others are the confirmed stars that are not eclipsing binaries:
// vetted real by the user, not a moving object, and not an eclipsing
// binary by the vet's latest report.
func (s *Service) Others(ctx context.Context) ([]Star, error) {
	all, err := s.load(ctx, nil)
	if err != nil {
		return nil, err
	}
	var out []Star
	for _, st := range all {
		if st.Source.Kind != app.DiscoveryKindMover && !EclipsingBinary(st.Report) {
			out = append(out, st)
		}
	}
	return out, nil
}

// Other is one confirmed star that is not an eclipsing binary;
// gorm.ErrRecordNotFound when id is not one.
func (s *Service) Other(ctx context.Context, id int) (Star, error) {
	all, err := s.load(ctx, []int{id})
	if err != nil {
		return Star{}, err
	}
	if len(all) != 1 || all[0].Source.Vetting != app.VetReal || all[0].Source.Kind == app.DiscoveryKindMover || EclipsingBinary(all[0].Report) {
		return Star{}, gorm.ErrRecordNotFound
	}
	return all[0], nil
}

// ErrStarType says the star type is not one the user can pick.
var ErrStarType = errors.New("unknown star type")

func validStarType(key string) bool {
	_, ok := starType(strings.TrimSpace(key))
	return ok || strings.TrimSpace(key) == ""
}

// sayLine is a real verdict's words without its opening, "Fades now and
// then", for a report written before verdicts kept their kind; it is
// shown only, never used to decide what the star is.
func sayLine(say string) string {
	say = strings.TrimSpace(say)
	for _, p := range []string{"Looks like a new variable star, ", "Looks like "} {
		if rest, ok := strings.CutPrefix(say, p); ok {
			say = rest
			break
		}
	}
	if say == "" {
		return ""
	}
	return strings.ToUpper(say[:1]) + say[1:]
}
