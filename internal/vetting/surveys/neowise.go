package surveys

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
)

const (
	// WISEMatchRadius is how far, in arcsec, a WISE detection may be from
	// the star.
	WISEMatchRadius = 4.0
	// WISEDilutionRadius is how close, in arcsec, a Gaia neighbour must
	// be to blend into WISE photometry; the WISE PSF is about 6 arcsec.
	WISEDilutionRadius = 6.0
	neowiseTable       = "neowiser_p1bs_psd"
	allwiseTable       = "allwise_p3as_mep"
	neowiseEpoch       = 2019.0
	allwiseEpoch       = 2010.5
	maxTAP             = 256 << 20
	colRA              = "ra"
	colDec             = "dec"
	colMJD             = "mjd"
)

type wiseTable struct {
	name    string
	file    string
	epoch   float64
	cols    []string
	w1, w2  string
	e1, e2  string
	frame   []string
	hasQual bool
}

func wiseTables() []wiseTable {
	return []wiseTable{
		{
			name: neowiseTable, file: "neowise", epoch: neowiseEpoch,
			cols: []string{colRA, colDec, colMJD, "w1mpro", "w1sigmpro", "w2mpro", "w2sigmpro", "qual_frame", "qi_fact", "saa_sep", "moon_masked", "cc_flags", "nb", "na", "scan_id", "frame_num"},
			w1:   "w1mpro", e1: "w1sigmpro", w2: "w2mpro", e2: "w2sigmpro",
			frame: []string{"scan_id", "frame_num"}, hasQual: true,
		},
		{
			name: allwiseTable, file: "allwise", epoch: allwiseEpoch,
			cols: []string{colRA, colDec, colMJD, "w1mpro_ep", "w1sigmpro_ep", "w2mpro_ep", "w2sigmpro_ep", "qi_fact", "saa_sep", "moon_masked", "cc_flags", "nb", "na", "frame_id"},
			w1:   "w1mpro_ep", e1: "w1sigmpro_ep", w2: "w2mpro_ep", e2: "w2sigmpro_ep",
			frame: []string{"frame_id"},
		},
	}
}

// tapQuery runs one ADQL query on IRSA's synchronous TAP service and
// gives back the CSV.
func (f *Fetcher) tapQuery(ctx context.Context, adql string) ([]byte, error) {
	form := url.Values{}
	form.Set("QUERY", adql)
	form.Set("FORMAT", "csv")
	var out []byte
	err := f.do(ctx, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.irsa()+"/TAP/sync", strings.NewReader(form.Encode()))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return req, nil
	}, func(r *http.Response) error {
		b, err := io.ReadAll(io.LimitReader(r.Body, maxTAP))
		out = b
		return err
	})
	if err != nil {
		return nil, err
	}
	if t := bytes.TrimSpace(out); bytes.HasPrefix(t, []byte("<")) {
		msg := string(t)
		if i := strings.Index(msg, `value="ERROR">`); i >= 0 {
			msg = msg[i+len(`value="ERROR">`):]
			if j := strings.Index(msg, "<"); j >= 0 {
				msg = msg[:j]
			}
		}
		return nil, fmt.Errorf("TAP error: %.300s", msg)
	}
	return out, nil
}

func (f *Fetcher) wiseCone(ctx context.Context, field Field, t Target, tb wiseTable) ([]byte, error) {
	key := f.dir(field, "neowise") + starKey(t) + "." + tb.file + ".csv.zst"
	raw, err := f.getZ(ctx, key)
	if err == nil {
		return raw, nil
	}
	if !isNotFound(err) {
		return nil, err
	}
	ra, dec := t.at(tb.epoch)
	adql := fmt.Sprintf("SELECT %s FROM %s WHERE CONTAINS(POINT('ICRS',ra,dec),CIRCLE('ICRS',%.7f,%.7f,%.7f))=1",
		strings.Join(tb.cols, ","), tb.name, ra, dec, WISEMatchRadius/3600)
	raw, err = f.tapQuery(ctx, adql)
	if err != nil {
		return nil, err
	}
	if _, _, err := readCSV(raw); err != nil {
		return nil, err
	}
	if err := f.putZ(ctx, key, raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func (f *Fetcher) neowise(ctx context.Context, field Field, t Target) Fetch {
	d, n := dilution(t, WISEDilutionRadius)
	bands := map[string][]lc.Point{}
	for _, tb := range wiseTables() {
		raw, err := f.wiseCone(ctx, field, t, tb)
		if err != nil {
			return failed(lc.NEOWISE, "IRSA %s query failed: %v", tb.name, err)
		}
		pts, err := wisePoints(raw, tb, t)
		if err != nil {
			return failed(lc.NEOWISE, "could not read %s: %v", tb.name, err)
		}
		for b, p := range pts {
			bands[b] = append(bands[b], p...)
		}
	}
	fe := Fetch{Survey: lc.NEOWISE, State: Done}
	note := ""
	if n > 0 {
		note = blendNote(d, n, WISEDilutionRadius, "WISE") + ", an approximation in the infrared"
	}
	for _, b := range []string{"W1", "W2"} {
		pts := bands[b]
		if len(pts) == 0 {
			continue
		}
		sort.Slice(pts, func(i, j int) bool { return pts[i].BJD < pts[j].BJD })
		fe.Series = append(fe.Series, lc.Series{Survey: lc.NEOWISE, Band: b, Points: pts, Dilution: d, Note: note})
	}
	if len(fe.Series) == 0 {
		fe.Note = fmt.Sprintf("no good WISE single exposure within %.0f arcsec", WISEMatchRadius)
	}
	return fe
}

// wiseQualityOK is the cut on one WISE single exposure detection.
func wiseQualityOK(get func(string) string, hasQual bool) bool {
	num := func(c string) float64 {
		v, err := strconv.ParseFloat(get(c), 64)
		if err != nil {
			return math.NaN()
		}
		return v
	}
	if hasQual && !(num("qual_frame") > 0) {
		return false
	}
	return num("qi_fact") > 0 && num("saa_sep") > 0 &&
		strings.HasPrefix(get("moon_masked"), "00") && strings.HasPrefix(get("cc_flags"), "00")
}

// wisePoints keeps, per frame, the good detection nearest the star.
func wisePoints(raw []byte, tb wiseTable, t Target) (map[string][]lc.Point, error) {
	rows, col, err := readCSV(raw)
	if err != nil {
		return nil, err
	}
	for _, c := range append([]string{colRA, colDec, colMJD, tb.w1, tb.w2}, tb.frame...) {
		if _, ok := col[c]; !ok {
			return nil, fmt.Errorf("no %s column", c)
		}
	}
	ra, dec := t.at(tb.epoch)
	type best struct {
		sep float64
		row []string
	}
	frames := map[string]best{}
	for _, r := range rows {
		get := func(c string) string {
			if i, ok := col[c]; ok && i < len(r) {
				return r[i]
			}
			return ""
		}
		if !wiseQualityOK(get, tb.hasQual) {
			continue
		}
		rra, e1 := strconv.ParseFloat(get(colRA), 64)
		rdec, e2 := strconv.ParseFloat(get(colDec), 64)
		if e1 != nil || e2 != nil {
			continue
		}
		sep := sepArcsec(ra, dec, rra, rdec)
		if sep > WISEMatchRadius {
			continue
		}
		var fk []string
		for _, c := range tb.frame {
			fk = append(fk, get(c))
		}
		k := strings.Join(fk, "/")
		if b, ok := frames[k]; !ok || sep < b.sep {
			frames[k] = best{sep: sep, row: r}
		}
	}
	out := map[string][]lc.Point{}
	for _, b := range frames {
		num := func(c string) (float64, error) {
			return strconv.ParseFloat(b.row[col[c]], 64)
		}
		mjd, err := num(colMJD)
		if err != nil {
			continue
		}
		bjd := lc.BJD(lc.MJDTime(mjd), t.RA, t.Dec)
		for _, band := range [][3]string{{"W1", tb.w1, tb.e1}, {"W2", tb.w2, tb.e2}} {
			m, e1 := num(band[1])
			e, e2 := 0.0, error(nil)
			if _, ok := col[band[2]]; ok {
				e, e2 = num(band[2])
			}
			if errors.Join(e1, e2) != nil || !goodMag(m, math.Max(e, 1e-6)) {
				continue
			}
			out[band[0]] = append(out[band[0]], lc.Point{BJD: bjd, Mag: m, Err: e})
		}
	}
	return out, nil
}
