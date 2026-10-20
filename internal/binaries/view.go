package binaries

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/eclfacts"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/period"
)

// EclipseInfo is one dip as the pages show it.
type EclipseInfo struct {
	Kind          string   `json:"kind"`
	Phase         float64  `json:"phase"`
	Depth         *float64 `json:"depth"`
	Band          string   `json:"band"`
	DurationHours *float64 `json:"duration_hours"`
}

// VSXState is whether the user submitted the star to VSX.
type VSXState struct {
	Submitted   bool       `json:"submitted"`
	Ref         string     `json:"ref"`
	SubmittedAt *time.Time `json:"submitted_at"`
}

// Share is the annotated picture of the discovery.
type Share struct {
	State   string `json:"state"`
	URL     string `json:"url"`
	Version int    `json:"version"`
	Note    string `json:"note"`
}

// Binary is one confirmed binary as the pages show it.
type Binary struct {
	SourceID      int           `json:"source_id"`
	Name          string        `json:"name"`
	AltName       string        `json:"alt_name"`
	Field         string        `json:"field"`
	Magnitude     string        `json:"magnitude"`
	Type          string        `json:"type"`
	TypeWords     string        `json:"type_words"`
	Period        float64       `json:"period"`
	PeriodText    string        `json:"period_text"`
	AltPeriodText string        `json:"alt_period_text"`
	Eclipses      []EclipseInfo `json:"eclipses"`
	DepthText     string        `json:"depth_text"`
	Confidence    string        `json:"confidence"`
	PeriodSettled bool          `json:"period_settled"`
	Flags         []Flag        `json:"flags"`
	VSX           VSXState      `json:"vsx"`
	Notes         string        `json:"notes"`
	FactsState    string        `json:"facts_state"`
	FactsError    string        `json:"facts_error"`
	Share         Share         `json:"share"`
	Timing        TimingSummary `json:"timing"`
}

// Facts states.
const (
	FactsReady   = "ready"
	FactsPending = "pending"
	FactsFailed  = "failed"
)

// Star is everything stored about one confirmed binary.
type Star struct {
	Source app.DiscoverySource
	Report *vetting.Report
	Facts  *Facts
	Row    *app.BinaryFacts
	Book   app.BinaryBook
	Share  *app.ShareImage
	// Timings are the star's eclipses timed in the user's own frames.
	Timings []app.EclipseTiming
	// Dipper is the star's stored dip cycle, nil when it has none.
	Dipper *app.DipperEphemeris
}

// ephemeris is the ephemeris the pages use: the worked out one, else the
// vet's own until the facts are ready.
func (s Star) ephemeris() (*period.Ephemeris, bool) {
	if s.Facts != nil && s.Facts.Ephemeris != nil {
		return s.Facts.Ephemeris, s.Facts.Doubled
	}
	e, d := adopt(s.Report)
	return e, d
}

func (s Star) deeper() int {
	if s.Facts != nil {
		return s.Facts.Deeper
	}
	e, _ := s.ephemeris()
	return deeper(e)
}

// catalogName is the vet's name for the star, "2MASS J00433343+4139446",
// without its magnitude.
func (s Star) catalogName() string {
	if s.Report == nil {
		return ""
	}
	name, _, _ := strings.Cut(s.Report.Star, " · ")
	return strings.TrimSpace(name)
}

func (s Star) gaiaName() string {
	if s.Facts != nil && s.Facts.Gaia != nil {
		return "Gaia DR3 " + strconv.FormatInt(s.Facts.Gaia.ID, 10)
	}
	if n := s.catalogName(); strings.HasPrefix(n, "Gaia DR3 ") {
		return n
	}
	return ""
}

// Names are the primary name and the second line: the user's own name,
// else the vet's catalog name, else the Gaia DR3 name.
func (s Star) Names() (string, string) {
	gaia, cat := s.gaiaName(), s.catalogName()
	primary := strings.TrimSpace(s.Book.PrimaryName)
	if primary == "" {
		primary = cat
	}
	if primary == "" {
		primary = gaia
	}
	alt := ""
	switch {
	case gaia != "" && gaia != primary:
		alt = gaia
	case cat != "" && cat != primary:
		alt = cat
	}
	return primary, alt
}

// Magnitude is the star's Gaia G, "G 15.05".
func (s Star) Magnitude() string {
	switch {
	case s.Facts != nil && s.Facts.Gaia != nil && s.Facts.Gaia.G > 0:
		return fmt.Sprintf("G %.2f", s.Facts.Gaia.G)
	case s.Source.GaiaG != nil:
		return fmt.Sprintf("G %.2f", *s.Source.GaiaG)
	}
	return ""
}

// depthKey is the band the pages quote depths in: ZTF r where the dip
// has it, else ZTF g, else the first band it has.
func depthKey(d map[string]float64) string {
	for _, k := range []string{keyZTFr, "ZTF g"} {
		if _, ok := d[k]; ok {
			return k
		}
	}
	if ks := sortedKeys(d); len(ks) > 0 {
		return ks[0]
	}
	return ""
}

// eccentric says the second dip is far enough from mid-cycle that the
// orbit is not a circle: more than ecc of a cycle.
const ecc = 0.005

// TypeOf is the VSX type of an ephemeris, EA, EB or EW by how much of the
// cycle the dips take and how alike they are, and the words the cards use,
// which add that the orbit is oval when the second dip is off mid-cycle.
func TypeOf(e *period.Ephemeris) (string, string) { return typeOf(e, nil) }

// typeOf is TypeOf with the surveys' own measure of the second dip's
// phase: the orbit counts as oval only when the dip is strongZ sigma off
// mid-cycle as well as more than ecc of a cycle.
func typeOf(e *period.Ephemeris, t *eclfacts.Twin) (string, string) {
	var share float64
	for k := range e.Eclipses {
		share += eclfacts.Duration(e, k) / e.Period
	}
	typ := "EA"
	if share >= 0.6 {
		typ = "EB"
		if e.Period < 1 && len(e.Eclipses) == 2 {
			a, b := e.Eclipses[0].Depth, e.Eclipses[1].Depth
			k := depthKey(a)
			if da, db := a[k], b[k]; da > 0 && db > 0 && math.Min(da, db)/math.Max(da, db) > 0.7 {
				typ = "EW"
			}
		}
	}
	if len(e.Eclipses) == 2 && math.Abs(e.Eclipses[1].Phase-0.5) > ecc && (t == nil || t.PhaseErr <= 0 || t.PhaseZ >= strongZ) {
		return typ, typ + ", oval orbit"
	}
	return typ, typ
}

// TypeHint is the plain words for a VSX type.
func TypeHint(typ string) string {
	switch typ {
	case "EW":
		return "Contact eclipsing binary: light changes all the time, the eclipses nearly equal."
	case "EB":
		return "Eclipsing binary whose light changes all the time, with eclipses of different depths."
	}
	return "Detached eclipsing binary: the light is steady between the eclipses."
}

// order is the dips deeper first.
func order(e *period.Ephemeris, deep int) []int {
	if len(e.Eclipses) == 1 {
		return []int{0}
	}
	return []int{deep, 1 - deep}
}

// View is how the pages show s; shareURL makes a share picture's link.
func (s Star) View(shareURL func(id, version int) string) Binary {
	name, alt := s.Names()
	b := Binary{SourceID: s.Source.ID, Name: name, AltName: alt, Field: s.Source.Object, Magnitude: s.Magnitude(),
		Notes: s.Book.Notes, VSX: VSXState{Submitted: s.Book.VSXSubmitted, Ref: s.Book.VSXRef, SubmittedAt: s.Book.VSXSubmittedAt},
		Flags: []Flag{}, Eclipses: []EclipseInfo{}, FactsState: FactsPending, Timing: TimingSummary{Text: "No timings yet.", Flags: []string{}}}
	if s.Report != nil {
		b.Confidence = s.Report.Suggestion.Confidence
	}
	if s.Row != nil {
		b.FactsError = s.Row.Error
		switch {
		case s.Facts != nil:
			b.FactsState = FactsReady
		case s.Row.State == FactsFailed:
			b.FactsState = FactsFailed
		}
	}
	e, dbl := s.ephemeris()
	if e == nil {
		return b
	}
	b.Period = e.Period
	b.PeriodText = periodText(e.Period, e.PeriodErr)
	var tw *eclfacts.Twin
	if s.Facts != nil && s.Facts.Survey != nil {
		tw = s.Facts.Survey.Twin
	}
	b.Type, b.TypeWords = typeOf(e, tw)
	deep := s.deeper()
	var depths []string
	band := ""
	for i, k := range order(e, deep) {
		d := e.Eclipses[k]
		info := EclipseInfo{Kind: KindPrimary, Phase: frac(d.Phase - e.Eclipses[deep].Phase)}
		if i > 0 {
			info.Kind = KindSecondary
		}
		if key := depthKey(d.Depth); key != "" {
			v := d.Depth[key]
			info.Depth, info.Band = &v, bandWords(key)
			depths = append(depths, mag2(v))
			if band == "" {
				band = info.Band
			} else if band != info.Band {
				depths[len(depths)-1] += " (" + info.Band + ")"
			}
		}
		h := eclfacts.Duration(e, k) * 24
		info.DurationHours = &h
		b.Eclipses = append(b.Eclipses, info)
	}
	switch {
	case len(depths) == 2:
		b.DepthText = fmt.Sprintf("%s and %s mag (%s)", depths[0], depths[1], band)
	case len(depths) == 1 && len(e.Eclipses) == 1:
		b.DepthText = fmt.Sprintf("%s mag (%s), no second eclipse seen", depths[0], band)
	case len(depths) == 1:
		b.DepthText = fmt.Sprintf("%s mag (%s)", depths[0], band)
	}
	switch {
	case dbl && len(depths) > 0 && s.Facts != nil && s.Facts.Alike:
		b.DepthText = fmt.Sprintf("%s mag (%s), both eclipses alike", depths[0], band)
	case dbl && len(depths) > 0 && s.Facts == nil:
		b.DepthText = fmt.Sprintf("%s mag (%s), odd and even cycles not compared yet", depths[0], band)
	}
	if s.Report != nil {
		f := Facts{Ephemeris: e, Doubled: dbl, Deeper: deep}
		if s.Facts != nil {
			f = *s.Facts
		}
		if fl := Flags(s.Report, f); fl != nil {
			b.Flags = fl
		}
	}
	b.AltPeriodText = altPeriods(e, b.Flags)
	b.PeriodSettled = b.AltPeriodText == ""
	b.Share = s.shareView(shareURL)
	b.Timing = s.Summary()
	return b
}

// altPeriods are the other periods the flags leave open, "or 10.78 (twice
// that)".
func altPeriods(e *period.Ephemeris, flags []Flag) string {
	var alts []string
	for _, fl := range flags {
		switch fl.ID {
		case FlagTwice:
			alts = append(alts, periodText(2*e.Period, e.PeriodErr)+" (twice that)")
		case FlagHalf:
			alts = append(alts, periodText(e.Period/2, e.PeriodErr)+" (half that)")
		}
	}
	if len(alts) == 0 {
		return ""
	}
	return "or " + strings.Join(alts, " or ")
}

func (s Star) shareView(shareURL func(id, version int) string) Share {
	if s.Share == nil {
		return Share{State: SharePending, Note: "Not made yet."}
	}
	out := Share{State: s.Share.State, Version: s.Share.Version, Note: s.Share.Note}
	if s.Share.State == ShareReady && shareURL != nil {
		out.URL = shareURL(s.Source.ID, s.Share.Version)
	}
	return out
}

func frac(x float64) float64 { return x - math.Floor(x) }

// decodeFacts reads a stored facts row; nil when it holds none of this
// version.
func decodeFacts(row *app.BinaryFacts) *Facts {
	if row == nil || row.Facts == "" || row.Version != FactsVersion || row.State != FactsReady {
		return nil
	}
	var f Facts
	if json.Unmarshal([]byte(row.Facts), &f) != nil {
		return nil
	}
	return &f
}

// Share picture states.
const (
	ShareReady   = "ready"
	SharePending = "pending"
	ShareNone    = "none"
	ShareFailed  = "failed"
)
