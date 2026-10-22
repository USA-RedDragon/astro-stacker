// Package binaries keeps the confirmed eclipsing binaries: the facts the
// vet's checks and the surveys give for each, the flags those raise, the
// VSX kit, the predicted eclipses and imaging requests for them.
package binaries

import (
	"cmp"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/starcat"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/eclfacts"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/period"
)

// FactsVersion is the version of the rules that work out Facts; facts
// from an older one are worked out again.
const FactsVersion = 3

// GaiaFacts is the Gaia DR3 star at the spot, at epoch 2016.0.
type GaiaFacts struct {
	ID    int64   `json:"id"`
	RA    float64 `json:"ra"`
	Dec   float64 `json:"dec"`
	PMRA  float64 `json:"pmra"`
	PMDec float64 `json:"pmdec"`
	G     float64 `json:"g"`
	RUWE  float64 `json:"ruwe"`
}

// Near is a catalogued star near the variable, Sep arcseconds away.
type Near struct {
	Name   string  `json:"name"`
	Type   string  `json:"type,omitempty"`
	G      float64 `json:"g,omitempty"`
	Sep    float64 `json:"sep"`
	Period float64 `json:"period,omitempty"`
}

// Facts is what the surveys, catalogs and the vet's checks say about one
// confirmed binary, worked out once per vet report.
type Facts struct {
	Gaia       *GaiaFacts        `json:"gaia,omitempty"`
	Neighbours []Near            `json:"neighbours,omitempty"`
	VSXNear    []Near            `json:"vsx_near,omitempty"`
	VSXChecked *time.Time        `json:"vsx_checked,omitempty"`
	Surveys    map[string]string `json:"surveys,omitempty"`
	// Ephemeris is the one the pages use: the vet's, or for a star whose
	// one dip differs on odd and even cycles by the vet's own check, twice
	// its period with the dip on both halves (Doubled).
	Ephemeris *period.Ephemeris `json:"ephemeris"`
	Doubled   bool              `json:"doubled,omitempty"`
	// Alike says a doubled star's two halves have the same depth by the
	// surveys, so half its period fits too.
	Alike bool `json:"alike,omitempty"`
	// Deeper is the index of the deeper dip in Ephemeris.
	Deeper int `json:"deeper"`
	// Survey is what the survey light curves say at Ephemeris, from the
	// vet's report or, for an older report, from the vet's caches;
	// SurveyFrom says which.
	Survey     *eclfacts.Summary `json:"survey,omitempty"`
	SurveyFrom string            `json:"survey_from,omitempty"`
	FirstYear  int               `json:"first_year,omitempty"`
	LastYear   int               `json:"last_year,omitempty"`
	Bands      []string          `json:"bands,omitempty"`
	Sectors    []int             `json:"tess_sectors,omitempty"`
}

// Eclipse kinds: the deeper dip and the other.
const (
	KindPrimary   = "primary"
	KindSecondary = "secondary"
)

// keyZTFr is the depth key of ZTF r, the band the pages quote first.
const keyZTFr = "ZTF r"

// Where survey facts came from.
const (
	FromReport = "report"
	FromCaches = "caches"
)

func checkOf(r *vetting.Report, id string) (check.Check, bool) {
	if r == nil {
		return check.Check{}, false
	}
	for _, g := range r.Groups {
		for _, c := range g.Checks {
			if c.ID == id {
				return c, true
			}
		}
	}
	return check.Check{}, false
}

// sameDepthOnly says the double check's only reason was that a single dip
// is the same on odd and even cycles, which two equal eclipses at twice
// the period also give.
func sameDepthOnly(c check.Check) bool {
	return strings.HasPrefix(c.Evidence, period.SameDepthOddEven) || strings.HasPrefix(c.Evidence, period.OldSameDepthOddEven)
}

// adopt is the ephemeris the pages use for a vet report: a single dip
// whose odd and even cycles differ by the vet's own double check is taken
// at twice the period.
func adopt(r *vetting.Report) (*period.Ephemeris, bool) {
	e := r.Ephemeris
	if len(e.Eclipses) != 1 {
		return e, false
	}
	if c, ok := checkOf(r, check.Double); ok && c.Status == check.Fail && !sameDepthOnly(c) {
		return eclfacts.Doubled(e), true
	}
	return e, false
}

// Periodic says the report has an eclipsing ephemeris with a period and
// one or two dips.
func Periodic(r *vetting.Report) bool {
	return r != nil && r.Ephemeris != nil && r.Ephemeris.Kind == period.KindEclipsing && r.Ephemeris.Period > 0 &&
		len(r.Ephemeris.Eclipses) > 0 && len(r.Ephemeris.Eclipses) <= 2
}

// deeper is the index of the deeper dip: by ZTF r where both dips have a
// depth there, else by the first band both have.
func deeper(e *period.Ephemeris) int {
	if len(e.Eclipses) < 2 {
		return 0
	}
	a, b := e.Eclipses[0].Depth, e.Eclipses[1].Depth
	keys := append([]string{keyZTFr}, sortedKeys(a)...)
	for _, k := range keys {
		da, ok1 := a[k]
		db, ok2 := b[k]
		if ok1 && ok2 {
			if db > da {
				return 1
			}
			return 0
		}
	}
	return 0
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// Work works out a binary's facts from its vet report, the star's cached
// survey data and the VSX stars near it, checked at vsxChecked.
func Work(r *vetting.Report, sd vetting.StarData, vsx []starcat.VSXStar, vsxChecked time.Time) Facts {
	e, dbl := adopt(r)
	f := Facts{Ephemeris: e, Doubled: dbl, Deeper: deeper(e), Surveys: map[string]string{}}
	for s, st := range sd.States {
		f.Surveys[s] = string(st)
	}
	if g := sd.Gaia; g != nil {
		f.Gaia = &GaiaFacts{ID: g.SourceID, RA: g.RA, Dec: g.Dec, PMRA: zeroNaN(g.PMRA), PMDec: zeroNaN(g.PMDec), G: zeroNaN(float64(g.G)), RUWE: zeroNaN(float64(g.RUWE))}
		for _, n := range sd.Neighbours {
			if math.IsNaN(n.RA) || math.IsNaN(n.Dec) {
				continue
			}
			f.Neighbours = append(f.Neighbours, Near{Name: "Gaia DR3 " + strconv.FormatInt(n.SourceID, 10), G: zeroNaN(float64(n.G)), Sep: sepArcsec(g.RA, g.Dec, n.RA, n.Dec)})
		}
		slices.SortFunc(f.Neighbours, func(a, b Near) int { return cmp.Compare(a.Sep, b.Sep) })
	}
	ra, dec := f.position(sd)
	for _, v := range vsx {
		f.VSXNear = append(f.VSXNear, Near{Name: v.Name, Type: v.Type, Sep: sepArcsec(ra, dec, v.RA, v.Dec), Period: zeroNaN(v.Period)})
	}
	slices.SortFunc(f.VSXNear, func(a, b Near) int { return cmp.Compare(a.Sep, b.Sep) })
	if !vsxChecked.IsZero() {
		t := vsxChecked.UTC()
		f.VSXChecked = &t
	}
	switch {
	case dbl && r.SurveyDoubled != nil:
		f.Survey, f.SurveyFrom = r.SurveyDoubled, FromReport
	case !dbl && r.Survey != nil:
		f.Survey, f.SurveyFrom = r.Survey, FromReport
	default:
		if s := eclfacts.Summarize(sd.Series, e); s != nil {
			f.Survey, f.SurveyFrom = s, FromCaches
		}
	}
	f.freshTwin(sd)
	if dbl {
		f.refineDoubled(sd)
	}
	bands := map[string]bool{}
	for _, s := range sd.Series {
		if len(s.Points) == 0 || s.Survey == lc.Own {
			continue
		}
		if s.Survey == lc.TESS {
			if n, ok := sectorOf(s); ok && !slices.Contains(f.Sectors, n) {
				f.Sectors = append(f.Sectors, n)
			}
			continue
		}
		bands[period.BandKey(s.Survey, s.Band)] = true
		if s.Survey != lc.ZTF {
			continue
		}
		for _, p := range s.Points {
			y := lc.UTCOfBJD(p.BJD, ra, dec).Year()
			if f.FirstYear == 0 || y < f.FirstYear {
				f.FirstYear = y
			}
			f.LastYear = max(f.LastYear, y)
		}
	}
	f.Bands = sortedKeys(bands)
	slices.Sort(f.Sectors)
	return f
}

// freshTwin measures the two dips' comparison again in the cached survey
// series, so a report vetted under older rules gets the second dip's
// phase error as the rules now measure it.
func (f *Facts) freshTwin(sd vetting.StarData) {
	if f.Survey == nil || f.SurveyFrom != FromReport || len(sd.Series) == 0 {
		return
	}
	if t := eclfacts.TwinOf(sd.Series, f.Ephemeris); t != nil {
		s := *f.Survey
		s.Twin = t
		f.Survey = &s
	}
}

// refineDoubled settles what a doubled star's two halves are by the
// surveys: a second dip as deep as the first (Alike), a second dip of its
// own depth, or none at all, the dip coming every other cycle only.
func (f *Facts) refineDoubled(sd vetting.StarData) {
	e := f.Ephemeris
	if f.Survey == nil || f.Survey.Twin == nil || f.Survey.Twin.Band == "" || len(e.Eclipses) != 2 {
		return
	}
	t := f.Survey.Twin
	switch {
	case t.DepthZ < strongZ && t.PhaseZ < strongZ:
		f.Alike = true
	case t.Depth2.Value < 3*t.Depth2.Err:
		one := *e
		one.Eclipses = e.Eclipses[:1]
		f.Ephemeris, f.Deeper = &one, 0
		if s := eclfacts.Summarize(sd.Series, &one); s != nil {
			f.Survey, f.SurveyFrom = s, FromCaches
		} else {
			cov := f.Survey.Coverage
			if len(cov) > 1 {
				cov = cov[:1]
			}
			f.Survey = &eclfacts.Summary{Period: one.Period, Coverage: cov, Normal: f.Survey.Normal}
		}
	default:
		two := *e
		two.Eclipses = []period.Eclipse{e.Eclipses[0], e.Eclipses[1]}
		two.Eclipses[0].Depth = map[string]float64{t.Band: t.Depth1.Value}
		two.Eclipses[1].Depth = map[string]float64{t.Band: t.Depth2.Value}
		f.Ephemeris = &two
		f.Deeper = 0
		if t.Depth2.Value > t.Depth1.Value {
			f.Deeper = 1
		}
	}
}

func sectorOf(s lc.Series) (int, bool) {
	for _, t := range []string{s.Band, s.Note} {
		t = strings.TrimSpace(t)
		t = strings.TrimPrefix(t, "Sector ")
		t = strings.TrimPrefix(t, "S")
		if n, err := strconv.Atoi(t); err == nil {
			return n, true
		}
	}
	return 0, false
}

func zeroNaN(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return v
}

// position is where the star is in degrees, near enough for separations
// and light travel time: Gaia's, else the spot's.
func (f Facts) position(sd vetting.StarData) (float64, float64) {
	if f.Gaia != nil {
		return f.Gaia.RA, f.Gaia.Dec
	}
	return sd.RA, sd.Dec
}

func sepArcsec(ra1, dec1, ra2, dec2 float64) float64 {
	r := math.Pi / 180
	a := math.Sin((dec2 - dec1) * r / 2)
	b := math.Sin((ra2 - ra1) * r / 2)
	s := a*a + math.Cos(dec1*r)*math.Cos(dec2*r)*b*b
	return 2 * math.Asin(math.Min(1, math.Sqrt(s))) / r * 3600
}
