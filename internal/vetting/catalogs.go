package vetting

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/USA-RedDragon/astro-stacker/internal/starcat"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/tnsmirror"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/period"
)

// listedArcsec is how close a catalog entry must be to name the spot's
// star; nearArcsec how close a different entry is still worth naming.
const (
	listedArcsec = 2.5
	// tnsArcsec is how close a TNS report must be: reported transient
	// positions are often a few arcsec off.
	tnsArcsec    = 4.0
	nearArcsec   = 30.0
	simbadArcsec = 5.0
	expectArcsec = 2.0
)

// Mirrors are the local catalog copies, read first.
type Mirrors struct {
	VSX *starcat.VSX
	// Variables are the survey variable star lists by name: gaia-dr3-vari
	// and the lists of surveyLists.
	Variables map[string]*starcat.Variables
	TNS       func() *tnsmirror.Index
}

// SimbadObject is one SIMBAD entry near the spot.
type SimbadObject struct {
	MainID string   `json:"main_id"`
	RA     float64  `json:"ra"`
	Dec    float64  `json:"dec"`
	OType  string   `json:"otype"`
	IDs    []string `json:"ids,omitempty"`
}

// Simbad looks up SIMBAD within radius degrees.
type Simbad func(ctx context.Context, ra, dec, radius float64) ([]SimbadObject, error)

// Known is what the catalogs say about the spot.
type Known struct {
	Checks []check.Check
	// Listed names the catalog entry that already knows the star as
	// variable or transient, empty when none does.
	Listed string
	// ListedPeriod is the period in days the entry in Listed gives, 0
	// when it gives none.
	ListedPeriod float64
	// Reported is the TNS report at the spot, empty when none: a transient
	// TNS knows outranks the image checks, since its own light can sit in
	// the reference.
	Reported string
	// Name is the star's best known designation, when SIMBAD has one.
	Name string
	// Expected is "young star" or "supergiant" when SIMBAD lists the star
	// as one within expectArcsec, a kind whose brightness wanders without
	// a fixed period, with its SIMBAD name in ExpectedName.
	Expected     string
	ExpectedName string
}

// list records the first catalog entry that knows the star as variable,
// with its period in days or 0.
func (k *Known) list(what string, period float64) {
	if k.Listed != "" {
		return
	}
	k.Listed = what
	if period > 0 {
		k.ListedPeriod = period
	}
}

// periodRevisedFraction is how far, as a fraction, the vet's own period
// must sit from the listed one to call the listed period wrong.
const periodRevisedFraction = 0.02

// noCatalog is the clause a new periodic star's verdict ends with.
const noCatalog = ", and no catalog lists the star as variable"

// periodRevised is the verdict on a listed star whose own survey data
// stand up as periodic (ps real) at a period other than the one listed.
func periodRevised(known Known, ps *Suggestion, pr *period.Result) (Suggestion, bool) {
	if ps == nil || ps.State != app.VetReal || pr == nil || pr.Ephemeris == nil || known.ListedPeriod <= 0 {
		return Suggestion{}, false
	}
	p := pr.Ephemeris.Period
	if !(p > 0) || samePeriod(p, known.ListedPeriod) {
		return Suggestion{}, false
	}
	return Suggestion{State: app.VetKnown, Say: fmt.Sprintf("Known variable, period revised to %s days", shortPeriod(p)), Confidence: ps.Confidence, Kind: ps.Kind,
		Why: joinSentences(sentence(known.Listed), fmt.Sprintf("The survey data here repeat every %s days instead of %s.", periodText(p), periodText(known.ListedPeriod)), strings.ReplaceAll(ps.Why, noCatalog+".", "."))}, true
}

// samePeriod says whether the vet's period p and a listed period agree
// within periodRevisedFraction, counting twice or half the listed period
// and a one-day alias of it as agreeing: catalogs list eclipsing binaries
// at half their period, and nightly sampling makes one-day aliases, so
// neither shows the listing wrong.
func samePeriod(p, listed float64) bool {
	for _, k := range []float64{1, 2, 0.5} {
		if math.Abs(p/(k*listed)-1) <= periodRevisedFraction {
			return true
		}
	}
	return math.Abs(math.Abs(1/p-1/listed)-1) <= periodRevisedFraction
}

func arcsec(ra1, dec1, ra2, dec2 float64) float64 {
	return starcat.Separation(ra1, dec1, ra2, dec2) * 3600
}

func catalogChecks(ctx context.Context, m Mirrors, sb Simbad, ra, dec float64, gaiaID int64) Known {
	var k Known
	k.Checks = append(k.Checks, vsxCheck(ctx, m, ra, dec, &k))
	k.Checks = append(k.Checks, gaiaVariCheck(ctx, m, ra, dec, gaiaID, &k))
	k.Checks = append(k.Checks, surveyVarCheck(ctx, m, ra, dec, gaiaID, &k))
	k.Checks = append(k.Checks, tnsCheck(m, ra, dec, &k))
	k.Checks = append(k.Checks, simbadCheck(ctx, sb, ra, dec, &k))
	return k
}

func vsxCheck(ctx context.Context, m Mirrors, ra, dec float64, k *Known) check.Check {
	if m.VSX == nil {
		return check.Skipped(check.VSX, "Not run: the local copy of VSX is off.")
	}
	stars, err := m.VSX.Cone(ctx, ra, dec, nearArcsec/3600, nil)
	if err != nil {
		return check.Skipped(check.VSX, "Not run: the local copy of VSX could not be read.")
	}
	sort.Slice(stars, func(i, j int) bool {
		return arcsec(ra, dec, stars[i].RA, stars[i].Dec) < arcsec(ra, dec, stars[j].RA, stars[j].Dec)
	})
	if len(stars) == 0 {
		return check.New(check.VSX, check.Pass, "Not listed.")
	}
	s := stars[0]
	sep := arcsec(ra, dec, s.RA, s.Dec)
	if sep <= listedArcsec {
		k.list("VSX lists it as "+s.Name, s.Period)
		ev := fmt.Sprintf("Listed as %s, %s, %.1f arcsec away.", s.Name, typeWords(s.Type), sep)
		if s.Period > 0 {
			ev = fmt.Sprintf("Listed as %s, %s repeating every %s days, %.1f arcsec away.", s.Name, typeWords(s.Type), periodText(s.Period), sep)
		}
		return check.New(check.VSX, check.Fail, ev)
	}
	return check.New(check.VSX, check.Pass, fmt.Sprintf("Not listed. The nearest entry, %.1f arcsec away, is a different star.", sep))
}

func gaiaVariCheck(ctx context.Context, m Mirrors, ra, dec float64, gaiaID int64, k *Known) check.Check {
	v := m.Variables["gaia-dr3-vari"]
	if v == nil {
		return check.Skipped(check.GaiaVari, "Not run: the local copy of Gaia's variable star list is off.")
	}
	stars, err := v.Cone(ctx, ra, dec, 10.0/3600, nil)
	if err != nil {
		return check.Skipped(check.GaiaVari, "Not run: the local copy of Gaia's variable star list could not be read.")
	}
	for _, s := range stars {
		if (gaiaID != 0 && s.ID == gaiaID) || (gaiaID == 0 && arcsec(ra, dec, s.RA, s.Dec) <= listedArcsec/3) {
			k.Listed = cmpOr(k.Listed, "Gaia lists it as variable")
			return check.New(check.GaiaVari, check.Fail, fmt.Sprintf("Listed as %s.", typeWords(s.Type)))
		}
	}
	return check.New(check.GaiaVari, check.Pass, "Not listed.")
}

// surveyList is a local copy of a survey variable star list the catalog
// check reads: key in Mirrors.Variables, who lists the star, and a short
// name for a list that could not be read. listed, when set, says what the
// list's entries within listedArcsec tell of the star, empty when they do
// not list it as variable; otherwise any entry lists it. A note-only list
// is reported in the evidence but never makes the star known: it flags
// scatter, not a variable star with a type or period, and every eclipsing
// binary has scatter.
type surveyList struct {
	key, who, short string
	listed          func(stars []starcat.VarStar, ra, dec float64, gaiaID int64) string
	noteOnly        bool
}

func surveyLists() []surveyList {
	return []surveyList{
		{"atlas-var", "ATLAS", "ATLAS", nil, false},
		{"ztf-var", "ZTF", "ZTF", nil, false},
		{"asassn-var", "ASAS-SN", "ASAS-SN", nil, false},
		{"ztf-sus", "ZTF's list of suspected variables (Chen 2020)", "ZTF suspected", nil, false},
		{"tess-eb-new", "the TESS eclipsing binary catalog (Kostov 2025)", "TESS new binaries", nil, false},
		{"tess-eb-known", "the TESS eclipsing binary catalog (Kostov 2025)", "TESS known binaries", nil, false},
		{"tess-eb-prsa", "the TESS eclipsing binary catalog (Prsa 2022)", "TESS sectors 1 to 26 binaries", nil, false},
		{"tess-rot", "the TESS rotation periods of young stars (Kounkel 2022)", "TESS rotation", nil, false},
		{"gcvs-evs", "the GCVS list of variables in other galaxies", "GCVS other galaxies", nil, false},
		{"maiz-var", "Maiz Apellaniz 2023", "Gaia brightness spread", maizListed, true},
		{"lamost-per", "Qiao 2024's LAMOST periodic variables", "LAMOST periodic", nil, false},
		{"lamost-var", "Xu 2022", "LAMOST variable candidates", lamostVarListed, true},
		{"gaia-lav", "Mowlavi 2021", "Gaia DR2 large amplitude", gaiaLAVListed, true},
		{"tess-eb-cand", "Kostov 2025", "TESS binary candidates", tessEBCandListed, true},
	}
}

// nearestListed is the entry within listedArcsec nearest the spot, nil
// when none is.
func nearestListed(stars []starcat.VarStar, ra, dec float64) *starcat.VarStar {
	var star *starcat.VarStar
	for i := range stars {
		s := &stars[i]
		if d := arcsec(ra, dec, s.RA, s.Dec); d <= listedArcsec && (star == nil || d < arcsec(ra, dec, star.RA, star.Dec)) {
			star = s
		}
	}
	return star
}

// lamostVarListed reads Xu 2022's LAMOST variable candidates, picked by
// the scatter of their ZTF light curves without a class or period.
func lamostVarListed(stars []starcat.VarStar, ra, dec float64, _ int64) string {
	if nearestListed(stars, ra, dec) == nil {
		return ""
	}
	return "Xu 2022 lists it as a likely LAMOST variable from the scatter of its ZTF light curve, without a type or period"
}

// gaiaLAVListed reads Mowlavi 2021's large-amplitude variables of Gaia
// DR2, picked by their spread in G alone: the star is the entry with its
// source_id, which Gaia DR3 mostly kept from DR2, else the nearest.
func gaiaLAVListed(stars []starcat.VarStar, ra, dec float64, gaiaID int64) string {
	star := nearestListed(stars, ra, dec)
	for i := range stars {
		if gaiaID != 0 && stars[i].ID == gaiaID {
			star = &stars[i]
		}
	}
	if star == nil {
		return ""
	}
	return fmt.Sprintf("Mowlavi 2021 lists it among Gaia DR2's large amplitude variables from its spread in G alone, without a type or period (amplitude measure %.3f)", star.Amp)
}

// tessEBCandListed reads Kostov 2025's list of TESS stars a neural
// network found eclipse-like dips in, which no one has checked.
func tessEBCandListed(stars []starcat.VarStar, ra, dec float64, _ int64) string {
	star := nearestListed(stars, ra, dec)
	if star == nil {
		return ""
	}
	return fmt.Sprintf("Listed as an unvetted TESS eclipsing binary candidate (%s in Kostov 2025), found by a neural network and not checked, without a period", star.Name)
}

// maizListed reads the Maiz Apellaniz 2023 list, which holds every Gaia
// DR3 star to G 17, variable or not: the star is its entry with the
// star's Gaia DR3 source_id, or without one its nearest, and the list
// names it as variable when its spread in G is at least three times its
// uncertainty.
func maizListed(stars []starcat.VarStar, ra, dec float64, gaiaID int64) string {
	var star *starcat.VarStar
	for i := range stars {
		s := &stars[i]
		switch {
		case gaiaID != 0:
			if s.ID == gaiaID {
				star = s
			}
		case star == nil || arcsec(ra, dec, s.RA, s.Dec) < arcsec(ra, dec, star.RA, star.Dec):
			star = s
		}
	}
	if star == nil || !starcat.MaizVariesInG(star) {
		return ""
	}
	return fmt.Sprintf("Maiz Apellaniz 2023 lists it as varying in Gaia's own measurements, with a spread of %.3f mag in G", star.Amp)
}

func surveyVarCheck(ctx context.Context, m Mirrors, ra, dec float64, gaiaID int64, k *Known) check.Check {
	names := surveyLists()
	var listed, notes, failed []string
	period := 0.0
	for _, n := range names {
		v := m.Variables[n.key]
		if v == nil {
			failed = append(failed, n.short)
			continue
		}
		stars, err := v.Cone(ctx, ra, dec, listedArcsec/3600, nil)
		if err != nil {
			failed = append(failed, n.short)
			continue
		}
		if n.listed != nil {
			if l := n.listed(stars, ra, dec, gaiaID); l != "" {
				if n.noteOnly {
					notes = append(notes, sentence(l))
				} else {
					listed = append(listed, l)
				}
			}
			continue
		}
		for _, s := range stars {
			if arcsec(ra, dec, s.RA, s.Dec) > listedArcsec {
				continue
			}
			l := fmt.Sprintf("%s lists it as %s", n.who, typeWords(s.Type))
			if s.Period > 0 {
				l += fmt.Sprintf(" repeating every %s days", periodText(s.Period))
				if len(listed) == 0 {
					period = s.Period
				}
			}
			listed = append(listed, l)
			break
		}
	}
	note := ""
	if len(notes) > 0 {
		note = " " + strings.Join(notes, " ")
	}
	switch {
	case len(listed) > 0:
		k.list(listed[0], period)
		return check.New(check.SurveyVar, check.Fail, sentence(strings.Join(listed, "; "))+note)
	case len(failed) == len(names):
		return check.Skipped(check.SurveyVar, "Not run: the local copies could not be read.")
	case len(failed) > 0:
		word := "list"
		if len(failed) > 1 {
			word = "lists"
		}
		return check.New(check.SurveyVar, check.Pass, fmt.Sprintf("Not listed. The %s %s could not be read.%s", joinWords(failed), word, note))
	}
	return check.New(check.SurveyVar, check.Pass, "Not listed."+note)
}

func tnsCheck(m Mirrors, ra, dec float64, k *Known) check.Check {
	if m.TNS == nil || m.TNS() == nil {
		return check.Skipped(check.TNS, "Not run: the local copy of the transient reports is off.")
	}
	hits := m.TNS().Near(ra, dec, tnsArcsec)
	if len(hits) == 0 {
		return check.New(check.TNS, check.Pass, "No match.")
	}
	h := hits[0]
	name := strings.TrimSpace(h.Prefix + " " + h.Name)
	k.Listed = cmpOr(k.Listed, "reported to TNS as "+name)
	k.Reported = "reported to TNS as " + name
	return check.New(check.TNS, check.Fail, fmt.Sprintf("Reported as %s on %s, %.1f arcsec away.", name, h.Discovered.Format("2006-01-02"), h.SepArcsec))
}

func simbadCheck(ctx context.Context, sb Simbad, ra, dec float64, k *Known) check.Check {
	if sb == nil {
		return check.Skipped(check.SIMBAD, "Not run.")
	}
	objs, err := sb(ctx, ra, dec, simbadArcsec/3600)
	if err != nil {
		return check.Skipped(check.SIMBAD, "Not run: SIMBAD could not be reached.")
	}
	sort.Slice(objs, func(i, j int) bool {
		return arcsec(ra, dec, objs[i].RA, objs[i].Dec) < arcsec(ra, dec, objs[j].RA, objs[j].Dec)
	})
	if len(objs) == 0 {
		return check.New(check.SIMBAD, check.Pass, "Nothing listed within 5 arcsec.")
	}
	o := objs[0]
	sep := arcsec(ra, dec, o.RA, o.Dec)
	if sep <= listedArcsec {
		k.Name = bestName(o)
	}
	for _, x := range objs {
		if w := expectedOType(x.OType); w != "" && arcsec(ra, dec, x.RA, x.Dec) <= expectArcsec {
			k.Expected, k.ExpectedName = w, x.MainID
			break
		}
	}
	for _, x := range objs {
		if variableOType(x.OType) && arcsec(ra, dec, x.RA, x.Dec) <= listedArcsec {
			k.Listed = cmpOr(k.Listed, "SIMBAD lists it as "+x.MainID)
			return check.New(check.SIMBAD, check.Fail, fmt.Sprintf("Listed as %s, %s.", x.MainID, otypeWords(x.OType)))
		}
	}
	if k.Expected != "" {
		return check.New(check.SIMBAD, check.Pass, fmt.Sprintf("Not listed as variable, but listed as %s, a %s; such stars change without a fixed period.", k.ExpectedName, k.Expected))
	}
	if sep <= listedArcsec {
		return check.New(check.SIMBAD, check.Pass, "Not listed as variable.")
	}
	return check.New(check.SIMBAD, check.Pass, fmt.Sprintf("Not listed as variable. The nearest object, %.1f arcsec away, is a different star.", sep))
}

func bestName(o SimbadObject) string {
	for _, id := range o.IDs {
		if strings.HasPrefix(id, "2MASS J") {
			return id
		}
	}
	if strings.HasPrefix(o.MainID, "Gaia DR3 ") || strings.HasPrefix(o.MainID, "TIC ") {
		for _, id := range o.IDs {
			if strings.HasPrefix(id, "Gaia DR3 ") {
				return id
			}
		}
	}
	return o.MainID
}

func variableOType(t string) bool {
	t = strings.TrimSpace(t)
	if strings.HasSuffix(t, "..") {
		return true
	}
	if b, ok := strings.CutSuffix(t, "?"); ok {
		t = b + "*"
	}
	switch t {
	case "V*", "EB*", "Ce*", "cC*", "RR*", "LP*", "Mi*", "CV*", "No*", "SN*", "Ro*", "Er*", "BY*", "RS*", "Pu*", "dS*", "gD*", "El*", "Ir*", "Or*",
		"Fl*", "XB*", "LX*", "HX*", "LXB", "HXB", "TT*", "Ae*", "WV*", "RV*", "bC*", "ZZ*", "sr*", "Sy*":
		return true
	}
	return false
}

// expectedOType is "young star" or "supergiant" for SIMBAD object types of
// stars whose brightness wanders without a fixed period: young stellar
// objects and their candidates, and red, yellow and blue supergiants and
// their candidates, and emission-line stars: 33852 (Horsehead Nebula), ESO-HA
// 1638, an M2.5 member of the sigma Orionis cluster, spread over 1.81 mag in
// ZTF r without a period. T Tauri, Orion and Herbig Ae/Be types are
// variable types, already listed by variableOType.
func expectedOType(t string) string {
	switch strings.TrimSpace(t) {
	case "Y*O", "Y*?", "YSO", "HH":
		return expectYoung
	case "Em*":
		return expectEmission
	case "s*r", "s*y", "s*b", "sg*", "s?r", "s?y", "s?b", "sg?", "RSG", "YSG", "BSG", "RedSG*", "YellowSG*", "BlueSG*", "Supergiant":
		return expectSupergiant
	}
	return ""
}

// Kinds of star whose brightness wanders without a fixed period.
const (
	expectYoung      = "young star"
	expectSupergiant = "supergiant"
	expectEmission   = "emission-line star"
)

func otypeWords(t string) string {
	switch strings.TrimSpace(t) {
	case "EB*":
		return "an eclipsing binary"
	case "RR*":
		return "an RR Lyrae star"
	case "Mi*":
		return "a Mira variable"
	case "LP*":
		return "a long period variable"
	case "CV*":
		return "a cataclysmic variable"
	case "No*":
		return "a nova"
	case "SN*":
		return "a supernova"
	case "Ce*", "cC*":
		return "a Cepheid"
	case "Ro*":
		return "a rotating variable"
	case "YO*", "TT*", "Or*":
		return "a young variable star"
	}
	return aVariable
}

func typeWords(t string) string {
	u := strings.ToUpper(strings.TrimSpace(t))
	switch {
	case u == "":
		return aVariable
	case strings.HasPrefix(u, "EA"), strings.HasPrefix(u, "EB"), strings.HasPrefix(u, "EW"), strings.HasPrefix(u, "ECL"), u == "E", strings.HasPrefix(u, "E|"), strings.HasPrefix(u, "E:"):
		return "an eclipsing binary"
	case strings.HasPrefix(u, "RR"):
		return "an RR Lyrae star"
	case strings.HasPrefix(u, "DCEP"), strings.HasPrefix(u, "CEP"), strings.HasPrefix(u, "CW"):
		return "a Cepheid"
	case strings.HasPrefix(u, "M"), strings.HasPrefix(u, "LPV"), strings.HasPrefix(u, "SR"), strings.HasPrefix(u, "L"):
		return "a long period variable"
	case strings.HasPrefix(u, "DSCT"), strings.HasPrefix(u, "GDOR"), strings.HasPrefix(u, "HADS"):
		return "a pulsating star"
	case strings.HasPrefix(u, "ROT"), strings.HasPrefix(u, "BY"), strings.HasPrefix(u, "RS"), strings.HasPrefix(u, "SOLAR_LIKE"):
		return "a spotted, rotating star"
	case strings.HasPrefix(u, "YSO"), strings.HasPrefix(u, "INS"), strings.HasPrefix(u, "IN"), strings.HasPrefix(u, "UX"):
		return "a young variable star"
	case strings.HasPrefix(u, "CV"), strings.HasPrefix(u, "UG"), strings.HasPrefix(u, "N"):
		return "a cataclysmic variable"
	}
	return aVariable
}

func periodText(p float64) string {
	switch {
	case p >= 100:
		return fmt.Sprintf("%.0f", p)
	case p >= 10:
		return trimZeros(fmt.Sprintf("%.5f", p))
	}
	return trimZeros(fmt.Sprintf("%.6f", p))
}

func trimZeros(s string) string {
	if strings.Contains(s, ".") {
		s = strings.TrimRight(s, "0")
		s = strings.TrimSuffix(s, ".")
	}
	return s
}

func sentence(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:] + "."
}

func joinWords(w []string) string {
	switch len(w) {
	case 0:
		return ""
	case 1:
		return w[0]
	case 2:
		return w[0] + " and " + w[1]
	}
	return strings.Join(w[:len(w)-1], ", ") + " and " + w[len(w)-1]
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func round(v float64, d int) float64 {
	p := math.Pow(10, float64(d))
	return math.Round(v*p) / p
}
