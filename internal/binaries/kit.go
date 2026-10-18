package binaries

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/eclfacts"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/period"
)

// Field states.
const (
	StateValue       = "value"
	StateNotMeasured = "not_measured"
	StateNotLooked   = "not_looked_up"
	StateEmpty       = "empty"
	StateNotSet      = "not_set"
)

// KitField is one VSX New Star Form field.
type KitField struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Value    string `json:"value"`
	State    string `json:"state"`
	Hint     string `json:"hint"`
	Editable bool   `json:"editable"`
}

// File states.
const (
	FileReady   = "ready"
	FilePending = "pending"
	FileNone    = "none"
	FileFailed  = "failed"
)

// FileFact is one rendered file: its object key and size, or why there is
// none.
type FileFact struct {
	State   string `json:"state"`
	Key     string `json:"key,omitempty"`
	Bytes   int    `json:"bytes,omitempty"`
	Caption string `json:"caption,omitempty"`
	Note    string `json:"note,omitempty"`
}

// KitFile is one supporting file of the kit.
type KitFile struct {
	Key     string `json:"key"`
	Title   string `json:"title"`
	State   string `json:"state"`
	URL     string `json:"url"`
	Caption string `json:"caption"`
	Bytes   int    `json:"bytes"`
	Note    string `json:"note"`
}

// KitCheck is one checklist item.
type KitCheck struct {
	Key  string `json:"key"`
	Text string `json:"text"`
	Done bool   `json:"done"`
	Auto bool   `json:"auto"`
	Note string `json:"note"`
}

// Kit is the VSX submission kit of one binary.
type Kit struct {
	Star      Binary     `json:"star"`
	Lede      string     `json:"lede"`
	Fields    []KitField `json:"fields"`
	Files     []KitFile  `json:"files"`
	Checklist []KitCheck `json:"checklist"`
}

// References VSX takes for the survey data.
const (
	ZTFReference  = "Masci, F. J.; et al., 2019, The Zwicky Transient Facility: Data Processing, Products, and Archive"
	ZTFBibcode    = "2019PASP..131a8003M"
	TESSReference = "Ricker, G. R.; et al., 2014, Transiting Exoplanet Survey Satellite (TESS)"
	TESSBibcode   = "2014SPIE.9143E..20R"
)

// File keys.
const (
	FilePhased = "phased"
	FileOwn    = "own"
	FileChart  = "chart"
)

// vsxClearArcsec is how far no VSX entry may be for a new star.
const vsxClearArcsec = 10.0

// Files reads a stored files map.
func Files(row *app.BinaryFacts) map[string]FileFact {
	out := map[string]FileFact{}
	if row != nil && row.Files != "" {
		_ = json.Unmarshal([]byte(row.Files), &out)
	}
	return out
}

// SexRA is right ascension in degrees as "hh mm ss.ss".
func SexRA(deg float64) string {
	cs := int64(math.Round(math.Mod(deg+360, 360) / 15 * 3600 * 100))
	cs %= 24 * 3600 * 100
	h, m, s := cs/360000, cs/6000%60, cs%6000
	return fmt.Sprintf("%02d %02d %05.2f", h, m, float64(s)/100)
}

// SexDec is declination in degrees as "+dd mm ss.s".
func SexDec(deg float64) string {
	sign := "+"
	if deg < 0 {
		sign = "-"
	}
	ds := int64(math.Round(math.Abs(deg) * 3600 * 10))
	d, m, s := ds/36000, ds/600%60, ds%600
	return fmt.Sprintf("%s%02d %02d %04.1f", sign, d, m, float64(s)/10)
}

// KitOf makes the VSX kit of s; files are its rendered files and fileURL
// their links.
func KitOf(s Star, discoverer string, files map[string]FileFact, fileURL func(id int, name string) string, shareURL func(id, version int) string) Kit {
	b := s.View(shareURL)
	k := Kit{Star: b, Fields: []KitField{}, Files: []KitFile{}, Checklist: []KitCheck{}}
	e, dbl := s.ephemeris()
	f := s.Facts
	k.Lede = lede(s, b)
	add := func(key, label, value, state, hint string, editable bool) {
		if value != "" && state == "" {
			state = StateValue
		}
		k.Fields = append(k.Fields, KitField{Key: key, Label: label, Value: value, State: state, Hint: hint, Editable: editable})
	}
	if f != nil && f.Gaia != nil {
		ra, dec := f.Gaia.J2000()
		add("position", "Position (J2000)", SexRA(ra)+" "+SexDec(dec), "", "From Gaia DR3, moved by its proper motion to the year 2000 position.", false)
	} else {
		add("position", "Position (J2000)", "", StateNotMeasured, "Comes from Gaia DR3 once the survey facts are worked out.", false)
	}
	add("primary_name", "Primary name", b.Name, "", "Or your own name. Your choice; edit it here and the kit uses it.", true)
	var others []string
	for _, n := range []string{s.catalogName(), s.gaiaName()} {
		if n != "" && n != b.Name && !slices.Contains(others, n) {
			others = append(others, n)
		}
	}
	if len(others) > 0 {
		add("other_names", "Other names", strings.Join(others, ", "), "", "Other catalog names the app has for this star.", false)
	} else {
		add("other_names", "Other names", "", StateNotLooked, "2MASS and TIC names for this star; the app has no copy of those catalogs.", false)
	}
	if e != nil {
		add("type", "Variable type", b.Type, "", TypeHint(b.Type), false)
	}
	add("spectral_type", "Spectral type", "", StateEmpty, "", false)
	maxMag, minMag := magnitudes(s, e)
	add("max_mag", "Maximum magnitude", maxMag.Value, maxMag.State, maxMag.Hint, false)
	add("min_mag", "Minimum magnitude", minMag.Value, minMag.State, minMag.Hint, false)
	if e != nil {
		hint := ""
		for _, fl := range b.Flags {
			switch fl.ID {
			case FlagTwice:
				hint = "Shortest period that fits. Twice that, " + periodText(2*e.Period, e.PeriodErr) + ", fits too."
			case FlagHalf:
				hint = "Half of it, " + periodText(e.Period/2, e.PeriodErr) + ", fits too."
			}
		}
		if dbl && hint == "" {
			hint = "Twice the period the vet found, since its odd and even cycles differ."
		}
		add("period", "Period (days)", b.PeriodText, "", hint, false)
		deep := s.deeper()
		bjd := e.T0 + e.Eclipses[deep].Phase*e.Period
		if f != nil && f.Gaia != nil {
			ra, dec := f.Gaia.J2000()
			hjd := lc.HJD(lc.UTCOfBJD(bjd, ra, dec), ra, dec)
			add("epoch", "Epoch (HJD)", fmt.Sprintf("%.4f", hjd), "", "Middle of the deeper eclipse, heliocentric Julian date in UTC.", false)
		} else {
			add("epoch", "Epoch (HJD)", "", StateNotMeasured, "Needs the star's position from the survey facts.", false)
		}
		pct := durationPct(e, deep)
		add("duration", "Eclipse duration (%)", fmt.Sprintf("%.1f", pct), "",
			fmt.Sprintf("The deeper eclipse lasts about %s, %.1f%% of the period, from its fitted shape in the survey data, counted where it is deeper than 5%% of its full depth.",
				hoursText(pct/100*e.Period*24), pct), false)
	}
	if strings.TrimSpace(discoverer) == "" {
		add("discoverer", "Discoverer", "", StateNotSet, "Enter it once, as you want it on the VSX page; every kit uses it.", true)
	} else {
		add("discoverer", "Discoverer", discoverer, "", "As you want it on the VSX page; every kit uses it.", true)
	}
	add("reference_name", "Reference name", ZTFReference, "", "", false)
	add("reference_bibcode", "Reference bibcode", ZTFBibcode, "", "", false)
	if f != nil && len(f.Sectors) > 0 {
		add("second_reference", "Second reference", TESSReference, "", "Bibcode "+TESSBibcode+". Add it in the draft, after the first save.", false)
	}
	if e != nil {
		add("remarks", "Remarks", Remarks(s, b), "", "Facts about the star only. The second eclipse goes here because the form has no field for it.", false)
	}
	for _, spec := range []struct{ key, title, none string }{
		{FilePhased, "Supporting file 1: phased light curve", "Made once the survey facts are worked out."},
		{FileOwn, "Supporting file 2: your own eclipse", "Not measured yet. Your frames have not timed an eclipse of this star; the Eclipse planner lists the next ones."},
		{FileChart, "Finding chart", "Not made yet. It is made from your own stacked image of the field, with north and east marked."},
	} {
		ff, ok := files[spec.key]
		kf := KitFile{Key: spec.key, Title: spec.title, State: FilePending, Note: spec.none}
		if ok {
			kf.State, kf.Caption, kf.Bytes = ff.State, ff.Caption, ff.Bytes
			if ff.Note != "" {
				kf.Note = ff.Note
			}
			if ff.State == FileReady && fileURL != nil {
				kf.URL, kf.Note = fileURL(s.Source.ID, spec.key+".png"), ""
			}
		}
		k.Files = append(k.Files, kf)
	}
	k.Checklist = checklist(s)
	return k
}

func lede(s Star, b Binary) string {
	what := "An eclipsing binary"
	if strings.Contains(b.TypeWords, "oval") {
		what = "An eclipsing binary with an oval orbit"
	}
	if b.Period == 0 {
		return fmt.Sprintf("Find #%d in %s.", s.Source.ID, s.Source.Object)
	}
	return fmt.Sprintf("Find #%d in %s. %s that repeats every %s days.", s.Source.ID, s.Source.Object, what, b.PeriodText)
}

type fieldValue struct{ Value, State, Hint string }

// magnitudes are the VSX maximum and minimum: the median ZTF magnitude
// outside the eclipses, and that plus the deeper eclipse's depth in the
// same band.
func magnitudes(s Star, e *period.Ephemeris) (fieldValue, fieldValue) {
	none := fieldValue{State: StateNotMeasured, Hint: "Needs ZTF r or g points outside the eclipses."}
	if s.Facts == nil || s.Facts.Survey == nil || s.Facts.Survey.Normal == nil || e == nil {
		return none, none
	}
	n := s.Facts.Survey.Normal
	mx := fieldValue{Value: fmt.Sprintf("%.2f", n.Mag), Hint: fmt.Sprintf("Passband %s. Median of %d ZTF %s points outside the eclipses.", n.Band, n.Points, n.Band)}
	key := "ZTF " + n.Band
	d, ok := e.Eclipses[s.deeper()].Depth[key]
	if !ok {
		return mx, fieldValue{State: StateNotMeasured, Hint: "The deeper eclipse has no depth measured in ZTF " + n.Band + "."}
	}
	mn := fieldValue{Value: fmt.Sprintf("%.2f", n.Mag+d), Hint: fmt.Sprintf("Passband %s. Maximum plus the deeper eclipse's fitted depth (%s mag).", n.Band, mag2(d))}
	if s.Facts.Gaia != nil && s.Facts.Gaia.RUWE > blendRUWE {
		mx.Hint += " Light of both stars of the close pair together."
		mn.Hint += " The depth is diluted by the other star."
	}
	return mx, mn
}

func durationPct(e *period.Ephemeris, k int) float64 {
	return eclfacts.Duration(e, k) / e.Period * 100
}

// Remarks are the VSX remarks: facts only, from the ephemeris, the flags
// and the catalogs.
func Remarks(s Star, b Binary) string {
	e, dbl := s.ephemeris()
	var out []string
	deep := s.deeper()
	n := (*eclfacts.Normal)(nil)
	if s.Facts != nil && s.Facts.Survey != nil {
		n = s.Facts.Survey.Normal
	}
	switch {
	case dbl && s.Facts != nil && s.Facts.Alike && len(b.Eclipses) > 0 && b.Eclipses[0].Depth != nil:
		out = append(out, fmt.Sprintf("Two minima of equal depth per cycle, %s mag in %s.", mag2(*b.Eclipses[0].Depth), b.Eclipses[0].Band))
	case dbl && s.Facts == nil:
		out = append(out, "Odd and even cycles of the shortest period differ, so the period is twice that.")
	case len(e.Eclipses) == 2:
		k := 1 - deep
		ph := frac(e.Eclipses[k].Phase - e.Eclipses[deep].Phase)
		lead := ""
		if strings.Contains(b.TypeWords, "oval") {
			lead = "Eccentric system. "
		}
		if n != nil {
			if d, ok := e.Eclipses[k].Depth["ZTF "+n.Band]; ok {
				out = append(out, fmt.Sprintf("%sMin II = %.2f %s at phase %.3f.", lead, n.Mag+d, n.Band, ph))
				break
			}
		}
		out = append(out, fmt.Sprintf("%sMin II at phase %.3f.", lead, ph))
	default:
		t := "Only one minimum per cycle is seen"
		if s.Facts != nil && s.Facts.Survey != nil && s.Facts.Survey.Secondary != nil {
			l := s.Facts.Survey.Secondary
			t += fmt.Sprintf("; no secondary deeper than %.3f mag in %s at phase 0.5", l.Mag, l.Band)
		}
		out = append(out, t+".")
	}
	for _, f := range b.Flags {
		switch f.ID {
		case FlagTwice:
			out = append(out, fmt.Sprintf("The period may be twice as long (%s d) with two equal minima.", periodText(2*e.Period, e.PeriodErr)))
		case FlagHalf:
			out = append(out, fmt.Sprintf("The period may be half as long (%s d) with one minimum per cycle.", periodText(e.Period/2, e.PeriodErr)))
		case FlagFew:
			out = append(out, strings.TrimSuffix(f.Why, ".")+"; minima are not densely sampled.")
		case FlagBlend:
			out = append(out, fmt.Sprintf("Gaia DR3 shows an unresolved close pair (RUWE %s); the eclipsing component is not identified and the amplitude is diluted.", ruweText(s.Facts.Gaia.RUWE)))
		}
	}
	out = append(out, factRemarks(s)...)
	return strings.Join(out, " ")
}

// factRemarks are the remarks from the catalogs and the survey data: a
// listed variable nearby, the years and bands of ZTF data, TESS sectors.
func factRemarks(s Star) []string {
	var out []string
	if f := s.Facts; f != nil {
		for _, v := range f.VSXNear {
			if v.Sep > 30 || v.Sep <= 2 {
				continue
			}
			t := fmt.Sprintf("%s, %.1f\" away, is a VSX %s variable", v.Name, v.Sep, v.Type)
			if v.Period > 0 {
				t += fmt.Sprintf(" (P = %s d)", periodText(v.Period, 0))
			}
			out = append(out, t+".")
		}
		if f.FirstYear > 0 {
			bands := []string{}
			for _, k := range f.Bands {
				if b, ok := strings.CutPrefix(k, "ZTF "); ok {
					bands = append(bands, b)
				}
			}
			out = append(out, fmt.Sprintf("Period from ZTF %s data %d-%d.", joinAnd(bands), f.FirstYear, f.LastYear))
		}
		if len(f.Sectors) > 0 {
			tc, ok := checkOf(s.Report, check.TESS)
			t := fmt.Sprintf("TESS %s %s", plural2(len(f.Sectors), "sector", "sectors"), intsText(f.Sectors))
			if ok && tc.Status == check.Pass {
				t += " show the minima at the predicted times"
			} else {
				t += " cover the star"
			}
			out = append(out, t+".")
		}
	}
	return out
}

func plural2(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func intsText(v []int) string {
	s := make([]string, len(v))
	for i, x := range v {
		s[i] = fmt.Sprint(x)
	}
	return joinAnd(s)
}

func joinAnd(s []string) string {
	switch len(s) {
	case 0:
		return ""
	case 1:
		return s[0]
	}
	return strings.Join(s[:len(s)-1], ", ") + " and " + s[len(s)-1]
}

// checkVSXClear is the checklist item the app ticks itself.
const checkVSXClear = "vsx_clear"

// checkItems are the checklist items, in order.
func checkItems() []struct{ key, text string } {
	return []struct{ key, text string }{
		{checkVSXClear, "No VSX entry within 10\" of this position"},
		{"check_name", "On the form, press \"Check name\". It must find no other star."},
		{"spot_check", "On the form, press \"Spot check\". Stop if a star shows within 2\"."},
		{"upload_lc", "Upload the phased light curve (under 1 MB)"},
		{"upload_own", "Upload your own eclipse plot (optional)"},
		{"upload_chart", "Upload a finding chart (optional)"},
		{"draft_first", "Tick \"Draft only\" on the first save, then add the second reference"},
		{"submit", "Read every field against this page, then untick \"Draft only\" and submit"},
	}
}

func checklist(s Star) []KitCheck {
	ticks := map[string]bool{}
	if s.Book.Checklist != "" {
		_ = json.Unmarshal([]byte(s.Book.Checklist), &ticks)
	}
	items := checkItems()
	out := make([]KitCheck, 0, len(items))
	for _, it := range items {
		c := KitCheck{Key: it.key, Text: it.text, Done: ticks[it.key]}
		if it.key == checkVSXClear {
			c.Auto, c.Done = true, false
			switch f := s.Facts; {
			case f == nil || f.VSXChecked == nil:
				c.Note = "Not checked yet: the app's copy of VSX is read when the survey facts are worked out."
			default:
				var near *Near
				for i, v := range f.VSXNear {
					if v.Sep <= vsxClearArcsec {
						near = &f.VSXNear[i]
						break
					}
				}
				if near != nil {
					c.Note = fmt.Sprintf("The app's copy of VSX lists %s %.1f\" away. Check it before you submit.", near.Name, near.Sep)
				} else {
					c.Done = true
					c.Note = "Checked in the app's copy of VSX on " + f.VSXChecked.Format(time.DateOnly)
				}
			}
		}
		if it.key == "draft_first" && (s.Facts == nil || len(s.Facts.Sectors) == 0) {
			c.Text = "Tick \"Draft only\" on the first save"
		}
		out = append(out, c)
	}
	return out
}
