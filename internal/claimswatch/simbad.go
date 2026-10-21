package claimswatch

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// SIMBAD finds the objects within Radius arcseconds of a star and the
// papers SIMBAD links to them.
type SIMBAD struct {
	Client *http.Client
	URL    string
	Radius float64
}

const simbadTAP = "https://simbad.cds.unistra.fr/simbad/sim-tap/sync"

func (s *SIMBAD) Where() string { return WhereSIMBAD }
func (s *SIMBAD) Off() string   { return "" }

// SIMBADRow is an object near a star with one of its papers, or none.
type SIMBADRow struct {
	MainID, OType string
	RA, Dec       float64
	Bibcode       string
	Year          int
	Title         string
}

// ParseSIMBADRefs reads the TSV answer of the object and paper search.
func ParseSIMBADRefs(b []byte) ([]SIMBADRow, error) {
	if !bytes.HasPrefix(b, []byte("main_id\t")) {
		msg := string(b)
		if i := strings.Index(msg, `value="ERROR">`); i >= 0 {
			msg, _, _ = strings.Cut(msg[i+len(`value="ERROR">`):], "<")
		}
		if len(msg) > 160 {
			msg = msg[:160]
		}
		return nil, fmt.Errorf("simbad: unexpected answer: %s", strings.TrimSpace(msg))
	}
	r := csv.NewReader(bytes.NewReader(b))
	r.Comma = '\t'
	r.LazyQuotes = true
	r.FieldsPerRecord = -1
	recs, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("simbad: %w", err)
	}
	var out []SIMBADRow
	for _, f := range recs[1:] {
		if len(f) < 7 {
			continue
		}
		ra, _ := strconv.ParseFloat(f[2], 64)
		dec, _ := strconv.ParseFloat(f[3], 64)
		y, _ := strconv.Atoi(f[5])
		out = append(out, SIMBADRow{MainID: f[0], OType: f[1], RA: ra, Dec: dec, Bibcode: f[4], Year: y, Title: f[6]})
	}
	return out, nil
}

func (s *SIMBAD) Check(ctx context.Context, stars []Star, _ State) (Outcome, error) {
	out := newOutcome()
	radius := s.Radius
	if radius <= 0 {
		radius = 5
	}
	u := s.URL
	if u == "" {
		u = simbadTAP
	}
	fails := 0
	for _, st := range stars {
		form := url.Values{"REQUEST": {"doQuery"}, "LANG": {"ADQL"}, "FORMAT": {"tsv"}, "QUERY": {fmt.Sprintf(
			`SELECT b.main_id, b.otype, b.ra, b.dec, r.bibcode, r."year", r.title FROM basic AS b `+
				`LEFT JOIN has_ref AS h ON h.oidref = b.oid LEFT JOIN ref AS r ON r.oidbib = h.oidbibref `+
				`WHERE CONTAINS(POINT('ICRS', b.ra, b.dec), CIRCLE('ICRS', %.6f, %.6f, %.7f)) = 1`, st.RA, st.Dec, radius/3600)}}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(form.Encode()))
		if err != nil {
			return out, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		b, err := do(s.Client, req, 16<<20)
		var rows []SIMBADRow
		if err == nil {
			rows, err = ParseSIMBADRefs(b)
		}
		if err != nil {
			out.Failed[st.SourceID] = err.Error()
			fails++
			continue
		}
		out.Found[st.SourceID] = SIMBADMentions(st, rows)
	}
	if fails == len(stars) && fails > 0 {
		return out, fmt.Errorf("simbad: every search failed")
	}
	return out, nil
}

// SIMBADMentions are one mention per object and one per paper.
func SIMBADMentions(st Star, rows []SIMBADRow) []Mention {
	var out []Mention
	seen := map[string]bool{}
	for _, r := range rows {
		obj := "obj:" + r.MainID
		if !seen[obj] {
			seen[obj] = true
			out = append(out, Mention{Key: obj, Title: r.MainID,
				Detail: fmt.Sprintf(`Object type %s, %.1f" from your star.`, r.OType, SepArcsec(st.RA, st.Dec, r.RA, r.Dec)),
				URL:    "https://simbad.cds.unistra.fr/simbad/sim-id?Ident=" + url.QueryEscape(r.MainID)})
		}
		if r.Bibcode == "" || seen["bib:"+r.Bibcode] {
			continue
		}
		seen["bib:"+r.Bibcode] = true
		m := Mention{Key: "bib:" + r.Bibcode, Title: r.Bibcode, Detail: "Paper SIMBAD links to " + r.MainID,
			URL: "https://ui.adsabs.harvard.edu/abs/" + url.PathEscape(r.Bibcode)}
		if r.Title != "" {
			m.Detail += ": " + r.Title
		}
		m.Detail += "."
		if r.Year > 0 {
			m.Title = fmt.Sprintf("%s (%d)", r.Bibcode, r.Year)
		}
		out = append(out, m)
	}
	return out
}
