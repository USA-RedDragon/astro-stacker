package discoveries

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/url"
	"strconv"
	"sync"

	"github.com/USA-RedDragon/astro-stacker/internal/starcat"
)

const (
	catalogGaiaVari    = "Gaia DR3 variability"
	catalogATLAS       = "ATLAS variables"
	catalogZTFVar      = "ZTF variables"
	catalogASASSN      = "ASAS-SN variables"
	surveyVizierMaxRow = 200000
	atlasDubious       = "dubious"
	colRAJ2000         = "RAJ2000"
	colDEJ2000         = "DEJ2000"
)

// surveyCatalogNames are the survey catalogues of variable stars. A change
// matches one of their stars at simbadMatchArcsec, as they list faint
// stars as densely as SIMBAD does.
func surveyCatalogNames() []string {
	return []string{catalogGaiaVari, catalogATLAS, catalogZTFVar, catalogASASSN}
}

// tightMatch says whether a catalogue's variables match at
// simbadMatchArcsec rather than vsxMatchArcsec.
func tightMatch(catalog string) bool {
	if catalog == catalogSIMBAD {
		return true
	}
	for _, c := range surveyCatalogNames() {
		if catalog == c {
			return true
		}
	}
	return false
}

// surveyCatalog is one survey catalogue of variable stars: its mirror,
// and its VizieR table's columns for when the mirror cannot answer.
type surveyCatalog struct {
	name   string
	cat    *starcat.VarCatalog
	mirror *starcat.Variables
	// cols are VizieR's columns for the ID or name, RA, Dec, class, period,
	// magnitude, amplitude; a blank one is not asked for.
	cols [7]string
	// prefix goes before the catalogue's name for a star, and vizPrefix
	// before VizieR's alone.
	prefix, vizPrefix string
	band              string
}

// SurveyVariables reads the survey catalogues of variable stars: Gaia
// DR3's variability classification, ATLAS, ZTF and ASAS-SN.
type SurveyVariables struct{ cats []surveyCatalog }

// NewSurveyVariables reads the survey catalogues from their mirrors in a
// bucket at their usual prefixes, and from VizieR while a mirror cannot
// answer; a nil bucket reads VizieR only.
func NewSurveyVariables(b starcat.Bucket) *SurveyVariables {
	cats := []surveyCatalog{
		{name: catalogGaiaVari, cat: starcat.GaiaVariability(), cols: [7]string{"Source", "RA_ICRS", "DE_ICRS", "Class"}, prefix: "Gaia DR3 "},
		{name: catalogATLAS, cat: starcat.ATLASVariables(), cols: [7]string{"ATOID", colRAJ2000, colDEJ2000, "Class", "", "vf-o-med"}, prefix: "ATO ", band: "o"},
		{name: catalogZTFVar, cat: starcat.ZTFVariables(), cols: [7]string{"ID", colRAJ2000, colDEJ2000, "Type", "Per", "rmag", "rAmp"}, band: "r"},
		{name: catalogASASSN, cat: starcat.ASASSNVariables(), cols: [7]string{"ASASSN-V", colRAJ2000, colDEJ2000, "Type", "Per", "Vmag", "Amp"}, vizPrefix: "ASASSN-V ", band: "V"},
	}
	if b != nil {
		for i := range cats {
			cats[i].mirror = starcat.NewVariables(b, cats[i].cat.Prefix)
		}
	}
	return &SurveyVariables{cats: cats}
}

// surveyVariables adds the survey catalogues' variable stars in the field
// to k, each catalogue listed as checked, or as failed when neither its
// mirror nor VizieR, nor an older VizieR answer for the field, answers.
// ATLAS's dubious candidates, which may not vary, are left out.
func (r *Runner) surveyVariables(ctx context.Context, object string, ra, dec, radius float64, k *Known) {
	s := r.opts.SurveyVariables
	if s == nil {
		s = NewSurveyVariables(nil)
	}
	found := make([][]Variable, len(s.cats))
	errs := make([]error, len(s.cats))
	var wg sync.WaitGroup
	for i, c := range s.cats {
		wg.Go(func() { found[i], errs[i] = r.surveyCone(ctx, c, object, ra, dec, radius) })
	}
	wg.Wait()
	for i, c := range s.cats {
		if errs[i] != nil {
			slog.Warn("A variable star catalog could not be read", "catalog", c.name, "error", errs[i])
			k.Failed = append(k.Failed, c.name)
			continue
		}
		k.Variables = append(k.Variables, found[i]...)
		k.Checked = append(k.Checked, c.name)
	}
}

// surveyCone reads a catalogue's stars in the field from its mirror, or
// while the mirror cannot answer from VizieR, cached for 30 days like
// SIMBAD's answers, an older cache standing in while VizieR is down.
func (r *Runner) surveyCone(ctx context.Context, c surveyCatalog, object string, ra, dec, radius float64) ([]Variable, error) {
	if c.mirror != nil {
		got, err := c.mirror.Cone(ctx, ra, dec, radius, func(s *starcat.VarStar) bool { return s.Type != atlasDubious })
		if err == nil {
			out := make([]Variable, len(got))
			for i, s := range got {
				name := s.Name
				if name == "" {
					name = strconv.FormatInt(s.ID, 10)
				}
				out[i] = c.variable(name, s.Type, s.RA, s.Dec, s.Period, f64(s.Mag), f64(s.Amp))
			}
			return out, nil
		}
		if !errors.Is(err, starcat.ErrUnavailable) {
			return nil, err
		}
		slog.Debug("A variable star mirror cannot answer; asking VizieR", "catalog", c.name, "error", err)
	}
	key := keyPrefix + "cache/" + objectSlug(object) + "/" + c.cat.Name + ".json.zst"
	var cc vsxCache
	cached := r.getJSON(ctx, key, &cc) == nil && Separation(ra, dec, cc.RA, cc.Dec)+radius <= cc.Radius+1e-9
	if cached && r.now().Sub(cc.Fetched) < vsxMaxAge {
		return cc.Stars, nil
	}
	vs, err := c.vizier(ctx, r.cat, ra, dec, radius)
	if err != nil {
		if cached {
			slog.Warn("VizieR could not be reached; using its older answer", "catalog", c.name, "object", object, "fetched", cc.Fetched, "error", err)
			return cc.Stars, nil
		}
		return nil, err
	}
	if err := r.putJSON(ctx, key, vsxCache{RA: ra, Dec: dec, Radius: radius, Fetched: r.now(), Stars: vs}); err != nil {
		slog.Warn("Could not cache a variable star catalog's answer", "catalog", c.name, "object", object, "error", err)
	}
	return vs, nil
}

// f64 widens a float32 to the float64 its shortest decimal reads as.
func f64(v float32) float64 {
	w, err := strconv.ParseFloat(strconv.FormatFloat(float64(v), 'g', -1, 32), 64)
	if err != nil {
		return float64(v)
	}
	return w
}

func (c surveyCatalog) variable(name, typ string, ra, dec, period, mag, amp float64) Variable {
	v := Variable{Catalog: c.name, Name: c.prefix + name, Type: typ, RA: ra, Dec: dec}
	if !math.IsNaN(mag) && mag != 0 {
		v.Max, v.Band = mag, c.band
		if !math.IsNaN(amp) && amp != 0 {
			v.Min, v.Amplitude = amp, true
		}
	}
	if !math.IsNaN(period) {
		v.Period = period
	}
	return v
}

func (c surveyCatalog) vizier(ctx context.Context, cat *Catalogs, ra, dec, radius float64) ([]Variable, error) {
	q := url.Values{}
	q.Set("-source", c.cat.VizieR)
	q.Set("-c", fmt.Sprintf("%.6f %+.6f", ra, dec))
	q.Set("-c.rd", fmt.Sprintf("%.4f", radius))
	out := ""
	for _, col := range c.cols {
		if col != "" {
			if out != "" {
				out += ","
			}
			out += col
		}
	}
	q.Set("-out", out)
	q.Set("-out.max", strconv.Itoa(surveyVizierMaxRow))
	b, err := cat.get(ctx, or(cat.VizierURL, vizierURL)+"?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("%s: %w", c.name, err)
	}
	rows, err := vizierTSV(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", c.name, err)
	}
	vs := make([]Variable, 0, len(rows))
	for _, row := range rows {
		a, ok1 := num(row[c.cols[1]])
		d, ok2 := num(row[c.cols[2]])
		if !ok1 || !ok2 || row[c.cols[0]] == "" || row[c.cols[3]] == atlasDubious {
			continue
		}
		f := func(col string) float64 {
			if v, ok := num(row[col]); ok && col != "" {
				return v
			}
			return math.NaN()
		}
		vs = append(vs, c.variable(c.vizPrefix+row[c.cols[0]], row[c.cols[3]], a, d, f(c.cols[4]), f(c.cols[5]), f(c.cols[6])))
	}
	return vs, nil
}
