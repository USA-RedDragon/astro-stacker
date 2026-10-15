package discoveries

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	vizierURL     = "https://vizier.cds.unistra.fr/viz-bin/asu-tsv"
	skybotURL     = "https://ssp.imcce.fr/webservices/skybot/api/conesearch.php"
	alerceURL     = "https://api.alerce.online/ztf/v1/objects/"
	finkURL       = "https://api.ztf.fink-portal.org/api/v1/conesearch"
	tnsURL        = "https://www.wis-tns.org/api/get/search"
	simbadURL     = "https://simbad.cds.unistra.fr/simbad/sim-tap/sync"
	simbadMaxRows = 200000
	userAgent     = "astro-stacker/1 (+https://github.com/USA-RedDragon/astro-stacker)"
	gaiaFaintG    = 21.5
	gaiaMaxRows   = 400000
	vsxMaxRows    = 100000
	catalogTimout = 8 * time.Minute
	gaiaTimeout   = 7 * time.Minute
	sourceAlerce  = "ALeRCE"
	sourceFink    = "Fink"
	sourceTNS     = "TNS"
	catalogVSX    = "VSX"
	catalogSIMBAD = "SIMBAD"
)

// Catalogs fetches the catalogues a night's changes are checked against.
// An empty URL uses the public service.
type Catalogs struct {
	Client    *http.Client
	VizierURL string
	SkyBoTURL string
	AlerceURL string
	FinkURL   string
	TNSURL    string
	SIMBADURL string
	// TNS needs a bot: with no key it is skipped.
	TNSKey, TNSBotID, TNSBotName string
	// Local is the local known-bodies service; unset asks SkyBoT.
	Local SmallBodies
}

func (c *Catalogs) client() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	return &http.Client{Timeout: catalogTimout}
}

func or(s, def string) string {
	if s != "" {
		return s
	}
	return def
}

func (c *Catalogs) get(ctx context.Context, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	return c.do(req)
}

func (c *Catalogs) do(req *http.Request) ([]byte, error) {
	resp, err := c.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", req.URL.Host, resp.Status)
	}
	return b, nil
}

// vizierTSV parses VizieR's tab separated output into rows by column name.
func vizierTSV(b []byte) ([]map[string]string, error) {
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var cols []string
	inData := false
	var out []map[string]string
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "\t")
		switch {
		case cols == nil:
			for _, c := range f {
				cols = append(cols, strings.TrimSpace(c))
			}
		case !inData:
			if strings.HasPrefix(strings.TrimSpace(f[0]), "-") {
				inData = true
			}
		default:
			row := make(map[string]string, len(cols))
			for i, c := range cols {
				if i < len(f) {
					row[c] = strings.TrimSpace(f[i])
				}
			}
			out = append(out, row)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if cols == nil {
		return nil, errors.New("vizier: no table in the response")
	}
	return out, nil
}

func num(s string) (float64, bool) {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return v, err == nil
}

// Gaia returns Gaia DR3 stars within radius degrees, brightest first, at
// their 2016.0 positions with their proper motions, at most 400000: to G 21.5, or in a Milky Way field VizieR can't sort that
// deep in time, to G 19.5 or 18.
func (c *Catalogs) Gaia(ctx context.Context, ra, dec, radius float64) ([]Star, error) {
	var err error
	for _, faint := range []float64{gaiaFaintG, 19.5, 18} {
		var stars []Star
		if stars, err = c.gaia(ctx, ra, dec, radius, faint); err == nil {
			return stars, nil
		}
		if ctx.Err() != nil {
			return nil, err
		}
	}
	return nil, err
}

func (c *Catalogs) gaia(ctx context.Context, ra, dec, radius, faint float64) ([]Star, error) {
	ctx, cancel := context.WithTimeout(ctx, gaiaTimeout)
	defer cancel()
	q := url.Values{}
	q.Set("-source", "I/355/gaiadr3")
	q.Set("-c", fmt.Sprintf("%.6f %+.6f", ra, dec))
	q.Set("-c.rd", fmt.Sprintf("%.4f", radius))
	q.Set("-out", "RA_ICRS,DE_ICRS,Gmag,pmRA,pmDE")
	q.Set("Gmag", fmt.Sprintf("<%g", faint))
	q.Set("-out.max", strconv.Itoa(gaiaMaxRows))
	q.Set("-sort", "Gmag")
	b, err := c.get(ctx, or(c.VizierURL, vizierURL)+"?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("gaia: %w", err)
	}
	rows, err := vizierTSV(b)
	if err != nil {
		return nil, err
	}
	out := make([]Star, 0, len(rows))
	for _, r := range rows {
		a, ok1 := num(r["RA_ICRS"])
		d, ok2 := num(r["DE_ICRS"])
		g, ok3 := num(r["Gmag"])
		if ok1 && ok2 && ok3 {
			st := Star{RA: a, Dec: d, G: g}
			st.PMRA, _ = num(r["pmRA"])
			st.PMDec, _ = num(r["pmDE"])
			out = append(out, st)
		}
	}
	return out, nil
}

// Variable is a VSX star, or a SIMBAD object of a variable type.
type Variable struct {
	// Catalog is SIMBAD for a SIMBAD object, empty for VSX.
	Catalog string  `json:"catalog,omitempty"`
	Name    string  `json:"name"`
	Type    string  `json:"type"`
	RA      float64 `json:"ra"`
	Dec     float64 `json:"dec"`
	Max     float64 `json:"max,omitempty"`
	Min     float64 `json:"min,omitempty"`
	Band    string  `json:"band,omitempty"`
	Period  float64 `json:"period,omitempty"`
	// Amplitude says Min is an amplitude, not the faintest magnitude.
	Amplitude bool `json:"amplitude,omitempty"`
}

// VSX returns AAVSO's variable stars within radius degrees, from VizieR's
// copy (B/vsx), as the AAVSO API is behind a browser check.
func (c *Catalogs) VSX(ctx context.Context, ra, dec, radius float64) ([]Variable, error) {
	q := url.Values{}
	q.Set("-source", "B/vsx/vsx")
	q.Set("-c", fmt.Sprintf("%.6f %+.6f", ra, dec))
	q.Set("-c.rd", fmt.Sprintf("%.4f", radius))
	q.Set("-out", "Name,Type,max,f_min,min,n_max,Period,RAJ2000,DEJ2000")
	q.Set("-out.max", strconv.Itoa(vsxMaxRows))
	b, err := c.get(ctx, or(c.VizierURL, vizierURL)+"?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("vsx: %w", err)
	}
	return ParseVSX(b)
}

// ParseVSX reads VizieR's B/vsx table.
func ParseVSX(b []byte) ([]Variable, error) {
	rows, err := vizierTSV(b)
	if err != nil {
		return nil, err
	}
	out := make([]Variable, 0, len(rows))
	for _, r := range rows {
		a, ok1 := num(r["RAJ2000"])
		d, ok2 := num(r["DEJ2000"])
		if !ok1 || !ok2 {
			continue
		}
		v := Variable{Name: r["Name"], Type: r["Type"], RA: a, Dec: d, Band: r["n_max"], Amplitude: r["f_min"] == "("}
		v.Max, _ = num(r["max"])
		v.Min, _ = num(r["min"])
		v.Period, _ = num(r["Period"])
		out = append(out, v)
	}
	return out, nil
}

// SIMBAD returns SIMBAD's variable stars within radius degrees: objects
// of the variable star types and their kinds (Orion, rotating, long
// period, Mira, RR Lyrae, eclipsing, Cepheid, cataclysmic, ...), and T Tauri
// and Herbig Ae/Be stars, which vary by nature. SIMBAD knows variables VSX
// lacks, such as Haro 4-411, an Orion variable in Orion's H-alpha field.
func (c *Catalogs) SIMBAD(ctx context.Context, ra, dec, radius float64) ([]Variable, error) {
	form := url.Values{}
	form.Set("REQUEST", "doQuery")
	form.Set("LANG", "ADQL")
	form.Set("FORMAT", "tsv")
	form.Set("QUERY", fmt.Sprintf("SELECT TOP %d main_id, ra, dec, otype FROM basic WHERE CONTAINS(POINT('ICRS', ra, dec), CIRCLE('ICRS', %.6f, %.6f, %.4f)) = 1 "+
		"AND (otype = 'V*..' OR otype = 'TT*..' OR otype = 'Ae*..')", simbadMaxRows, ra, dec, radius))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, or(c.SIMBADURL, simbadURL), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	b, err := c.do(req)
	if err != nil {
		return nil, fmt.Errorf("simbad: %w", err)
	}
	return ParseSIMBAD(b)
}

// ParseSIMBAD reads the TSV of SIMBAD's TAP service: main_id, ra, dec and
// otype, strings quoted. An error comes back as a VOTable instead.
func ParseSIMBAD(b []byte) ([]Variable, error) {
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "main_id\t") {
		msg := string(b)
		if i := strings.Index(msg, `name="QUERY_STATUS" value="ERROR">`); i >= 0 {
			msg = msg[i+len(`name="QUERY_STATUS" value="ERROR">`):]
			msg, _, _ = strings.Cut(msg, "<")
		}
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return nil, fmt.Errorf("simbad: unexpected response: %s", strings.TrimSpace(msg))
	}
	unq := func(s string) string { return strings.Trim(strings.TrimSpace(s), `"`) }
	out := make([]Variable, 0, len(lines)-1)
	for _, line := range lines[1:] {
		f := strings.Split(line, "\t")
		if len(f) < 4 {
			continue
		}
		a, ok1 := num(f[1])
		d, ok2 := num(f[2])
		if !ok1 || !ok2 {
			continue
		}
		out = append(out, Variable{Catalog: catalogSIMBAD, Name: unq(f[0]), Type: unq(f[3]), RA: a, Dec: d})
	}
	return out, nil
}

// Body is a known solar system object from SkyBoT at one epoch.
type Body struct {
	Number string  `json:"number,omitempty"`
	Name   string  `json:"name"`
	Class  string  `json:"class"`
	RA     float64 `json:"ra"`
	Dec    float64 `json:"dec"`
	V      float64 `json:"v"`
	ErrArc float64 `json:"err_arcsec"`
	// DRA (RA·cos Dec) and DDec in arcsec per hour.
	DRA  float64 `json:"dra"`
	DDec float64 `json:"ddec"`
}

// At is the body's position dt from the epoch, moving at its rates.
func (b Body) At(dt time.Duration) (ra, dec float64) {
	h := dt.Hours()
	dec = b.Dec + b.DDec*h/3600
	ra = b.RA + b.DRA*h/3600/math.Cos(b.Dec*deg)
	return math.Mod(ra+360, 360), dec
}

// SkyBoT returns the known asteroids and comets within radius degrees at
// epoch, as seen from the geocentre (no site is sent).
func (c *Catalogs) SkyBoT(ctx context.Context, ra, dec, radius float64, epoch time.Time) ([]Body, error) {
	q := url.Values{}
	q.Set("-ep", fmt.Sprintf("%.6f", julian(epoch)))
	q.Set("-ra", fmt.Sprintf("%.6f", ra))
	q.Set("-dec", fmt.Sprintf("%.6f", dec))
	q.Set("-rd", fmt.Sprintf("%.4f", radius))
	q.Set("-mime", "text")
	q.Set("-output", "all")
	q.Set("-loc", "500")
	q.Set("-filter", "0")
	q.Set("-objFilter", "111")
	q.Set("-refsys", "EQJ2000")
	q.Set("-from", "astro-stacker")
	b, err := c.get(ctx, or(c.SkyBoTURL, skybotURL)+"?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("skybot: %w", err)
	}
	return ParseSkyBoT(b)
}

// ParseSkyBoT reads SkyBoT's text output.
func ParseSkyBoT(b []byte) ([]Body, error) {
	var out []Body
	var cols []string
	for line := range strings.SplitSeq(string(b), "\n") {
		if strings.HasPrefix(line, "# Num") {
			for _, c := range strings.Split(strings.TrimPrefix(line, "# "), "|") {
				cols = append(cols, strings.TrimSpace(c))
			}
			continue
		}
		if strings.HasPrefix(line, "#") || strings.Count(line, "|") < 6 {
			continue
		}
		f := strings.Split(line, "|")
		get := func(name string, def int) string {
			for i, c := range cols {
				if strings.HasPrefix(c, name) && i < len(f) {
					return strings.TrimSpace(f[i])
				}
			}
			if def < len(f) {
				return strings.TrimSpace(f[def])
			}
			return ""
		}
		ra, ok1 := sexagesimal(get("RA(h)", 2), 15)
		dec, ok2 := sexagesimal(get("DE(deg)", 3), 1)
		if !ok1 || !ok2 {
			continue
		}
		bd := Body{Number: strings.Trim(get("Num", 0), " -"), Name: get("Name", 1), Class: get("Class", 4), RA: ra, Dec: dec}
		bd.V, _ = num(get("Mv", 5))
		bd.ErrArc, _ = num(get("Err", 6))
		bd.DRA, _ = num(get("dRA", 8))
		bd.DDec, _ = num(get("dDEC", 9))
		out = append(out, bd)
	}
	if out == nil && !strings.Contains(string(b), "# Flag") {
		return nil, errors.New("skybot: unexpected response")
	}
	return out, nil
}

func sexagesimal(s string, mult float64) (float64, bool) {
	f := strings.Fields(s)
	if len(f) != 3 {
		return 0, false
	}
	a, err1 := strconv.ParseFloat(f[0], 64)
	b, err2 := strconv.ParseFloat(f[1], 64)
	c, err3 := strconv.ParseFloat(f[2], 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return 0, false
	}
	sign := 1.0
	if strings.HasPrefix(f[0], "-") {
		sign, a = -1, -a
	}
	return sign * (a + b/60 + c/3600) * mult, true
}

func julian(t time.Time) float64 {
	return float64(t.UTC().UnixNano())/86400e9 + 2440587.5
}

// Alert is a known transient or variable seen by an alert broker or TNS.
type Alert struct {
	Source string  `json:"source"`
	ID     string  `json:"id"`
	Class  string  `json:"class,omitempty"`
	RA     float64 `json:"ra"`
	Dec    float64 `json:"dec"`
	SepArc float64 `json:"sep_arcsec"`
}

// Alerce returns ZTF objects ALeRCE knows within radius arcseconds.
func (c *Catalogs) Alerce(ctx context.Context, ra, dec, radius float64) ([]Alert, error) {
	q := url.Values{}
	q.Set("ra", fmt.Sprintf("%.6f", ra))
	q.Set("dec", fmt.Sprintf("%.6f", dec))
	q.Set("radius", fmt.Sprintf("%.1f", radius))
	b, err := c.get(ctx, or(c.AlerceURL, alerceURL)+"?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("alerce: %w", err)
	}
	var resp struct {
		Items []struct {
			OID   string  `json:"oid"`
			RA    float64 `json:"meanra"`
			Dec   float64 `json:"meandec"`
			Class *string `json:"class"`
		} `json:"items"`
	}
	if err := json.Unmarshal(b, &resp); err != nil {
		return nil, fmt.Errorf("alerce: %w", err)
	}
	var out []Alert
	for _, it := range resp.Items {
		a := Alert{Source: sourceAlerce, ID: it.OID, RA: it.RA, Dec: it.Dec, SepArc: Separation(ra, dec, it.RA, it.Dec) * 3600}
		if it.Class != nil {
			a.Class = *it.Class
		}
		out = append(out, a)
	}
	return out, nil
}

type finkQuery struct {
	RA     float64 `json:"ra"`
	Dec    float64 `json:"dec"`
	Radius float64 `json:"radius"`
	Format string  `json:"output-format"`
}

// Fink returns ZTF objects Fink knows within radius arcseconds.
func (c *Catalogs) Fink(ctx context.Context, ra, dec, radius float64) ([]Alert, error) {
	body, err := json.Marshal(finkQuery{RA: ra, Dec: dec, Radius: radius, Format: "json"})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, or(c.FinkURL, finkURL), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Content-Type", "application/json")
	b, err := c.do(req)
	if err != nil {
		return nil, fmt.Errorf("fink: %w", err)
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return nil, nil
	}
	var rows []map[string]any
	if err := json.Unmarshal(b, &rows); err != nil {
		return nil, fmt.Errorf("fink: %w", err)
	}
	seen := map[string]bool{}
	var out []Alert
	for _, r := range rows {
		id, _ := r["i:objectId"].(string)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		a := Alert{Source: sourceFink, ID: id}
		a.RA, _ = r["i:ra"].(float64)
		a.Dec, _ = r["i:dec"].(float64)
		if cl, ok := r["v:classification"].(string); ok {
			a.Class = cl
		} else if cl, ok := r["d:classification"].(string); ok {
			a.Class = cl
		}
		a.SepArc = Separation(ra, dec, a.RA, a.Dec) * 3600
		out = append(out, a)
	}
	return out, nil
}

// TNSEnabled says whether a TNS bot key is configured.
func (c *Catalogs) TNSEnabled() bool { return c.TNSKey != "" && c.TNSBotID != "" && c.TNSBotName != "" }

// TNS returns Transient Name Server objects within radius arcseconds.
func (c *Catalogs) TNS(ctx context.Context, ra, dec, radius float64) ([]Alert, error) {
	if !c.TNSEnabled() {
		return nil, nil
	}
	data, err := json.Marshal(map[string]string{"ra": fmt.Sprintf("%.6f", ra), "dec": fmt.Sprintf("%.6f", dec), "radius": fmt.Sprintf("%.1f", radius), "units": "arcsec"})
	if err != nil {
		return nil, err
	}
	form := url.Values{}
	form.Set("api_key", c.TNSKey)
	form.Set("data", string(data))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, or(c.TNSURL, tnsURL), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	marker, err := json.Marshal(map[string]string{"tns_id": c.TNSBotID, "type": "bot", "name": c.TNSBotName})
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "tns_marker"+string(marker))
	b, err := c.do(req)
	if err != nil {
		return nil, fmt.Errorf("tns: %w", err)
	}
	var resp struct {
		Data struct {
			Reply []struct {
				Prefix string `json:"prefix"`
				Name   string `json:"objname"`
			} `json:"reply"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &resp); err != nil {
		return nil, fmt.Errorf("tns: %w", err)
	}
	var out []Alert
	for _, r := range resp.Data.Reply {
		out = append(out, Alert{Source: sourceTNS, ID: strings.TrimSpace(r.Prefix + " " + r.Name)})
	}
	return out, nil
}
