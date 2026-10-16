package surveys

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
	"github.com/USA-RedDragon/astro-stacker/internal/ztflc"
	"github.com/klauspost/compress/zstd"
	"github.com/parquet-go/parquet-go"
)

const (
	// ZTFDilutionRadius is how close, in arcsec, a Gaia neighbour must be
	// to blend into a ZTF light curve; ZTF's seeing is about 2 arcsec.
	ZTFDilutionRadius = 2.0
	// ztfEpoch is the Julian year the star is moved to for matching, near
	// the middle of the ZTF survey.
	ztfMatchYear = 2021.5
	ztfCacheTTL  = 30 * 24 * time.Hour
	manifestName = "manifest.json.zst"
	parquetBatch = 128
	maxListing   = 8 << 20
	maxAPIBody   = 64 << 20
	ztfMargin    = 0.02
	// quadrantHalfDiagonal is half the diagonal of a ZTF CCD quadrant,
	// 3072 by 3080 pixels of 1.01 arcsec, in degrees, rounded up.
	quadrantHalfDiagonal = 0.62
)

var (
	reRelease = regexp.MustCompile(`href="lc_dr(\d+)/"`)
	reTopDir  = regexp.MustCompile(`href="(\d+)/"`)
	reField   = regexp.MustCompile(`href="field(\d{6})/"`)
)

// ZTFManifest records a field's ZTF pull: the release, the footprint and
// each light curve file read, so a stopped pull resumes where it was.
type ZTFManifest struct {
	Release  string                  `json:"release"`
	RA       float64                 `json:"ra"`
	Dec      float64                 `json:"dec"`
	Radius   float64                 `json:"radius"`
	Complete bool                    `json:"complete"`
	Files    map[string]ZTFFileEntry `json:"files"`
	// Quadrants are the light curve files that cover the footprint, kept
	// so a resumed pull need not ask IRSA's image index again.
	Quadrants []ZTFQuadrant `json:"quadrants,omitempty"`
	Updated   time.Time     `json:"updated"`
}

// ZTFFileEntry is one ZTF light curve file (one field, CCD quadrant and
// filter) cut to the footprint and stored as tiles.
type ZTFFileEntry struct {
	Tiles []int64 `json:"tiles"`
	// Objects and Epochs count what was kept: objects in the footprint
	// and their epochs with catflags 0.
	Objects int   `json:"objects"`
	Epochs  int   `json:"epochs"`
	Bytes   int64 `json:"bytes"`
	// Source is the size of the file at IRSA.
	Source int64 `json:"source"`
}

// ZTFQuadrant is one ZTF light curve file: field, CCD, quadrant and
// filter code ("zg", "zr" or "zi").
type ZTFQuadrant struct {
	Field  int    `json:"field"`
	CCD    int    `json:"ccd"`
	Q      int    `json:"q"`
	Filter string `json:"filter"`
}

func (q ZTFQuadrant) stem(release string) string {
	return fmt.Sprintf("ztf_%06d_%s_c%02d_q%d_%s", q.Field, q.Filter, q.CCD, q.Q, release)
}

type ztfRow struct {
	ObjectID int64   `parquet:"objectid"`
	ObjRA    float32 `parquet:"objra"`
	ObjDec   float32 `parquet:"objdec"`
	HMJD     struct {
		List []struct {
			Item float64 `parquet:"item"`
		} `parquet:"list"`
	} `parquet:"hmjd"`
	Mag struct {
		List []struct {
			Item float32 `parquet:"item"`
		} `parquet:"list"`
	} `parquet:"mag"`
	MagErr struct {
		List []struct {
			Item float32 `parquet:"item"`
		} `parquet:"list"`
	} `parquet:"magerr"`
	CatFlags struct {
		List []struct {
			Item int32 `parquet:"item"`
		} `parquet:"list"`
	} `parquet:"catflags"`
}

func fieldRadius(field Field) float64 {
	if field.Radius > 0 {
		return field.Radius
	}
	return 1
}

func (f *Fetcher) ztfManifest(ctx context.Context, field Field) (*ZTFManifest, error) {
	raw, err := f.getZ(ctx, f.dir(field, "ztf")+manifestName)
	if err != nil {
		return nil, err
	}
	var m ZTFManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func (f *Fetcher) putZTFManifest(ctx context.Context, field Field, m *ZTFManifest) error {
	m.Updated = f.now().UTC()
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return f.putZ(ctx, f.dir(field, "ztf")+manifestName, raw)
}

func (f *Fetcher) ztfRelease(ctx context.Context) (string, error) {
	if f.ZTFRelease != "" {
		return f.ZTFRelease, nil
	}
	b, err := f.getBytes(ctx, f.irsa()+"/data/ZTF/lc/", maxListing)
	if err != nil {
		return "", fmt.Errorf("ZTF releases: %w", err)
	}
	best := 0
	for _, m := range reRelease.FindAllSubmatch(b, -1) {
		if n, err := strconv.Atoi(string(m[1])); err == nil && n > best {
			best = n
		}
	}
	if best == 0 {
		return "", errors.New("ZTF releases: none listed")
	}
	return fmt.Sprintf("dr%d", best), nil
}

// ztfQuadrants asks IRSA's reference image index which ZTF fields, CCD
// quadrants and filters touch the footprint; each has one light curve
// file.
func (f *Fetcher) ztfQuadrants(ctx context.Context, field Field) ([]ZTFQuadrant, error) {
	q := url.Values{}
	q.Set("POS", fmt.Sprintf("%.6f,%.6f", field.RA, field.Dec))
	q.Set("SIZE", fmt.Sprintf("%.4f", 2*fieldRadius(field)))
	q.Set("INTERSECT", "OVERLAPS")
	q.Set("ct", "csv")
	q.Set("columns", "field,ccdid,qid,filtercode,ra,dec")
	b, err := f.getBytes(ctx, f.irsa()+"/ibe/search/ztf/products/ref?"+q.Encode(), maxListing)
	if err != nil {
		return nil, fmt.Errorf("ZTF reference index: %w", err)
	}
	rows, col, err := readCSV(b)
	if err != nil {
		return nil, fmt.Errorf("ZTF reference index: %w", err)
	}
	for _, c := range []string{"field", "ccdid", "qid", "filtercode"} {
		if _, ok := col[c]; !ok {
			return nil, fmt.Errorf("ZTF reference index: no %s column", c)
		}
	}
	seen := map[ZTFQuadrant]bool{}
	var out []ZTFQuadrant
	for _, r := range rows {
		fl, e1 := strconv.Atoi(r[col["field"]])
		cc, e2 := strconv.Atoi(r[col["ccdid"]])
		qq, e3 := strconv.Atoi(r[col["qid"]])
		fc := strings.TrimSpace(r[col["filtercode"]])
		if e1 != nil || e2 != nil || e3 != nil || len(fc) != 2 || quadrantMisses(field, r, col) {
			continue
		}
		k := ZTFQuadrant{Field: fl, CCD: cc, Q: qq, Filter: fc}
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	slices.SortFunc(out, func(a, b ZTFQuadrant) int {
		return strings.Compare(a.stem(""), b.stem(""))
	})
	return out, nil
}

// quadrantMisses says a quadrant, whose centre the index gives, cannot
// reach the footprint circle: the index answers for a square around it.
func quadrantMisses(field Field, r []string, col map[string]int) bool {
	ci, ok1 := col[colRA]
	cj, ok2 := col[colDec]
	if !ok1 || !ok2 {
		return false
	}
	ra, e1 := strconv.ParseFloat(r[ci], 64)
	dec, e2 := strconv.ParseFloat(r[cj], 64)
	if e1 != nil || e2 != nil {
		return false
	}
	return sepArcsec(field.RA, field.Dec, ra, dec) > (fieldRadius(field)+quadrantHalfDiagonal)*3600
}

// ztfFieldDirs maps each ZTF field to the directory its light curve
// files sit in.
func (f *Fetcher) ztfFieldDirs(ctx context.Context, release string) (map[int]string, error) {
	base := f.irsa() + "/data/ZTF/lc/lc_" + release + "/"
	b, err := f.getBytes(ctx, base, maxListing)
	if err != nil {
		return nil, fmt.Errorf("ZTF %s listing: %w", release, err)
	}
	out := map[int]string{}
	for _, m := range reTopDir.FindAllSubmatch(b, -1) {
		top := string(m[1])
		lb, err := f.getBytes(ctx, base+top+"/", maxListing)
		if err != nil {
			return nil, fmt.Errorf("ZTF %s listing: %w", release, err)
		}
		for _, fm := range reField.FindAllSubmatch(lb, -1) {
			if n, err := strconv.Atoi(string(fm[1])); err == nil {
				out[n] = base + top + "/" + "field" + string(fm[1]) + "/"
			}
		}
	}
	return out, nil
}

func (f *Fetcher) prefetchZTF(ctx context.Context, field Field, progress func(done, total int)) error {
	m, err := f.ztfManifest(ctx, field)
	if err != nil && !isNotFound(err) {
		return err
	}
	same := m != nil && sepArcsec(m.RA, m.Dec, field.RA, field.Dec) < 1 && math.Abs(m.Radius-fieldRadius(field)) < 1e-6
	if same && m.Complete && (f.ZTFRelease == "" || f.ZTFRelease == m.Release) {
		return nil
	}
	release, err := f.ztfRelease(ctx)
	if err != nil {
		return err
	}
	if !same || m.Release != release {
		m = &ZTFManifest{Release: release, RA: field.RA, Dec: field.Dec, Radius: fieldRadius(field), Files: map[string]ZTFFileEntry{}}
	}
	if len(m.Quadrants) == 0 {
		quads, err := f.ztfQuadrants(ctx, field)
		if err != nil {
			return err
		}
		m.Quadrants = quads
		if err := f.putZTFManifest(ctx, field, m); err != nil {
			return err
		}
	}
	dirs, err := f.ztfFieldDirs(ctx, release)
	if err != nil {
		return err
	}
	for i, q := range m.Quadrants {
		if progress != nil {
			progress(i, len(m.Quadrants))
		}
		stem := q.stem(release)
		if _, ok := m.Files[stem]; ok {
			continue
		}
		d, ok := dirs[q.Field]
		var e ZTFFileEntry
		if ok {
			e, err = f.pullZTFFile(ctx, field, release, d+stem+".parquet", q)
			if err != nil {
				return fmt.Errorf("%s: %w", stem, err)
			}
		}
		m.Files[stem] = e
		if err := f.putZTFManifest(ctx, field, m); err != nil {
			return err
		}
	}
	if progress != nil {
		progress(len(m.Quadrants), len(m.Quadrants))
	}
	m.Complete = true
	return f.putZTFManifest(ctx, field, m)
}

func (f *Fetcher) download(ctx context.Context, u string) (*os.File, int64, error) {
	tmp, err := os.CreateTemp(f.WorkDir, "ztf-*.parquet")
	if err != nil {
		return nil, 0, err
	}
	_ = os.Remove(tmp.Name())
	var n int64
	for try := range httpTries {
		err = f.do(ctx, func() (*http.Request, error) {
			if _, err := tmp.Seek(0, io.SeekStart); err != nil {
				return nil, err
			}
			if err := tmp.Truncate(0); err != nil {
				return nil, err
			}
			return http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		}, func(r *http.Response) error {
			var err error
			n, err = io.Copy(tmp, r.Body)
			if err == nil && r.ContentLength > 0 && n != r.ContentLength {
				err = fmt.Errorf("got %d of %d bytes", n, r.ContentLength)
			}
			return err
		})
		var he *httpError
		if err == nil || (errors.As(err, &he) && he.Status < 500) || ctx.Err() != nil || try == httpTries-1 {
			break
		}
		slog.Warn("ZTF download failed; trying again", "url", u, "try", try+1, "error", err)
		if err := sleepCtx(ctx, time.Duration(try+1)*time.Minute); err != nil {
			break
		}
	}
	if err != nil {
		tmp.Close()
		return nil, 0, err
	}
	return tmp, n, nil
}

func (f *Fetcher) pullZTFFile(ctx context.Context, field Field, release, u string, q ZTFQuadrant) (ZTFFileEntry, error) {
	var e ZTFFileEntry
	file, size, err := f.download(ctx, u)
	if err != nil {
		var he *httpError
		if errors.As(err, &he) && he.Status == http.StatusNotFound {
			return e, nil
		}
		return e, err
	}
	defer file.Close()
	e.Source = size
	tiles, err := f.readZTFFile(file, size, field, q)
	if err != nil {
		return e, err
	}
	keys := make([]int64, 0, len(tiles))
	for p := range tiles {
		keys = append(keys, p)
	}
	slices.Sort(keys)
	for _, p := range keys {
		objs := tiles[p]
		for _, o := range objs {
			e.Epochs += len(o.T)
		}
		e.Objects += len(objs)
		z, err := f.zstdEncodeLevel(encodeTile(q.Filter[1], objs), zstd.SpeedBestCompression)
		if err != nil {
			return e, err
		}
		if err := f.Store.Put(ctx, f.tileKey(field, release, p, q.stem(release)), z, "application/zstd"); err != nil {
			return e, err
		}
		e.Bytes += int64(len(z))
		e.Tiles = append(e.Tiles, p)
	}
	return e, nil
}

func (f *Fetcher) tileKey(field Field, release string, pix int64, stem string) string {
	return fmt.Sprintf("%s%s/%d/%s.bin.zst", f.dir(field, "ztf"), release, pix, stem)
}

// readZTFFile keeps the light curves inside the footprint, their epochs
// with catflags 0, grouped by tile.
func (f *Fetcher) readZTFFile(r io.ReaderAt, size int64, field Field, q ZTFQuadrant) (map[int64][]*ztfObject, error) {
	pf, err := parquet.OpenFile(r, size)
	if err != nil {
		return nil, err
	}
	h, err := f.helio()
	if err != nil {
		return nil, err
	}
	limit := (fieldRadius(field) + ztfMargin) * 3600
	out := map[int64][]*ztfObject{}
	rd := parquet.NewGenericReader[ztfRow](pf)
	defer rd.Close()
	buf := make([]ztfRow, parquetBatch)
	for {
		n, rerr := rd.Read(buf)
		for i := range buf[:n] {
			row := &buf[i]
			ra, dec := float64(row.ObjRA), float64(row.ObjDec)
			if sepArcsec(field.RA, field.Dec, ra, dec) > limit {
				continue
			}
			o := &ztfObject{ID: row.ObjectID, RA: row.ObjRA, Dec: row.ObjDec, Band: q.Filter[1]}
			k := min(len(row.HMJD.List), len(row.Mag.List), len(row.MagErr.List), len(row.CatFlags.List))
			for j := range k {
				if row.CatFlags.List[j].Item != 0 {
					continue
				}
				mag, me := float64(row.Mag.List[j].Item), float64(row.MagErr.List[j].Item)
				if !goodMag(mag, me) {
					continue
				}
				utc, err := h.utc(row.HMJD.List[j].Item, ra, dec)
				if err != nil {
					return nil, err
				}
				o.add(utc, mag, me)
			}
			if len(o.T) == 0 {
				continue
			}
			p := tileOf(ra, dec)
			out[p] = append(out[p], o)
		}
		if errors.Is(rerr, io.EOF) {
			return out, nil
		}
		if rerr != nil {
			return nil, rerr
		}
	}
}

func goodMag(mag, magErr float64) bool {
	return mag > 0 && magErr > 0 && !math.IsNaN(mag) && !math.IsInf(mag, 0) && !math.IsNaN(magErr) && !math.IsInf(magErr, 0)
}

type ztfEpoch struct {
	Band   string  `json:"b"`
	OID    int64   `json:"o"`
	MJD    float64 `json:"t"`
	Mag    float64 `json:"m"`
	MagErr float64 `json:"e"`
}

func (f *Fetcher) ztf(ctx context.Context, field Field, t Target) Fetch {
	ra, dec := t.at(ztfMatchYear)
	m, err := f.ztfManifest(ctx, field)
	if err != nil && !isNotFound(err) {
		return failed(lc.ZTF, "could not read the field's ZTF manifest: %v", err)
	}
	if m != nil && m.Complete && sepArcsec(m.RA, m.Dec, ra, dec) <= m.Radius*3600 {
		eps, n, err := f.ztfFromTiles(ctx, field, m, ra, dec)
		if err != nil {
			return failed(lc.ZTF, "could not read the field's ZTF tiles: %v", err)
		}
		if n == 0 && dec < -31 {
			return Fetch{Survey: lc.ZTF, State: Uncovered, Note: "ZTF does not reach this far south"}
		}
		fe := f.ztfFetch(t, eps)
		fe.Note = joinNotes(fmt.Sprintf("from the field's ZTF %s light curve files", strings.ToUpper(m.Release)), fe.Note)
		return fe
	}
	why := "the field's ZTF files are not pulled yet"
	if m != nil && m.Complete {
		why = "the star is outside the field's ZTF pull"
	}
	eps, err := f.ztfFromAPI(ctx, field, t, ra, dec)
	if err != nil {
		return failed(lc.ZTF, "%s, and the IRSA light curve service failed: %v", why, err)
	}
	fe := f.ztfFetch(t, eps)
	fe.Note = joinNotes(why+"; used the IRSA light curve service for this position", fe.Note)
	return fe
}

func joinNotes(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + ". " + b
}

func (f *Fetcher) ztfFromTiles(ctx context.Context, field Field, m *ZTFManifest, ra, dec float64) ([]ztfEpoch, int, error) {
	near := tilesNear(ra, dec, ztflc.MatchRadius+1)
	var keys []string
	stems := make([]string, 0, len(m.Files))
	for s := range m.Files {
		stems = append(stems, s)
	}
	sort.Strings(stems)
	for _, p := range near {
		for _, s := range stems {
			if slices.Contains(m.Files[s].Tiles, p) {
				keys = append(keys, f.tileKey(field, m.Release, p, s))
			}
		}
	}
	var out []ztfEpoch
	for _, k := range keys {
		raw, err := f.getZ(ctx, k)
		if err != nil {
			return nil, len(keys), err
		}
		objs, err := decodeTile(raw, func(ora, odec float64) bool {
			return sepArcsec(ra, dec, ora, odec) <= ztflc.MatchRadius
		})
		if err != nil {
			return nil, len(keys), fmt.Errorf("%s: %w", k, err)
		}
		for _, o := range objs {
			for i := range o.T {
				out = append(out, ztfEpoch{Band: string(o.Band), OID: o.ID, MJD: o.mjd(i), Mag: o.mag(i), MagErr: o.magErr(i)})
			}
		}
	}
	return out, len(keys), nil
}

type ztfAPICache struct {
	Fetched time.Time  `json:"fetched"`
	RA      float64    `json:"ra"`
	Dec     float64    `json:"dec"`
	Epochs  []ztfEpoch `json:"epochs"`
}

func (f *Fetcher) ztfFromAPI(ctx context.Context, field Field, t Target, ra, dec float64) ([]ztfEpoch, error) {
	key := f.dir(field, "ztf") + "api/" + starKey(t) + ".json.zst"
	if raw, err := f.getZ(ctx, key); err == nil {
		var c ztfAPICache
		if json.Unmarshal(raw, &c) == nil && f.now().Sub(c.Fetched) < ztfCacheTTL {
			return c.Epochs, nil
		}
	}
	q := url.Values{}
	q.Set("POS", fmt.Sprintf("CIRCLE %.7f %.7f %.7f", ra, dec, ztflc.MatchRadius/3600))
	q.Set("BANDNAME", "g,r,i")
	q.Set("BAD_CATFLAGS_MASK", strconv.Itoa(ztflc.BadCatFlags))
	q.Set("FORMAT", "csv")
	u := f.irsa() + "/cgi-bin/ZTF/nph_light_curves?" + strings.ReplaceAll(q.Encode(), "+", "%20")
	b, err := f.getBytes(ctx, u, maxAPIBody)
	if err != nil {
		return nil, err
	}
	eps, err := f.parseZTFAPI(b, ra, dec)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(ztfAPICache{Fetched: f.now().UTC(), RA: ra, Dec: dec, Epochs: eps})
	if err == nil {
		err = f.putZ(ctx, key, raw)
	}
	if err != nil {
		slog.Warn("Could not cache the ZTF light curve", "key", key, "error", err)
	}
	return eps, nil
}

// parseZTFAPI reads the light curve service's CSV, keeping epochs with
// catflags 0, timed at mid-exposure in UTC from the hjd column.
func (f *Fetcher) parseZTFAPI(b []byte, ra, dec float64) ([]ztfEpoch, error) {
	rows, col, err := readCSV(b)
	if err != nil {
		return nil, err
	}
	for _, c := range []string{"oid", "hjd", "mag", "magerr", "catflags", "filtercode"} {
		if _, ok := col[c]; !ok {
			return nil, fmt.Errorf("no %s column", c)
		}
	}
	h, err := f.helio()
	if err != nil {
		return nil, err
	}
	out := make([]ztfEpoch, 0, len(rows))
	for _, r := range rows {
		flags, err := strconv.Atoi(r[col["catflags"]])
		if err != nil || flags != 0 {
			continue
		}
		hjd, e1 := strconv.ParseFloat(r[col["hjd"]], 64)
		mag, e2 := strconv.ParseFloat(r[col["mag"]], 64)
		me, e3 := strconv.ParseFloat(r[col["magerr"]], 64)
		oid, e4 := strconv.ParseInt(r[col["oid"]], 10, 64)
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil || !goodMag(mag, me) {
			continue
		}
		utc, err := h.utc(hjd-2400000.5, ra, dec)
		if err != nil {
			return nil, err
		}
		out = append(out, ztfEpoch{Band: strings.TrimPrefix(r[col["filtercode"]], "z"), OID: oid, MJD: utc, Mag: mag, MagErr: me})
	}
	return out, nil
}

func (f *Fetcher) ztfFetch(t Target, eps []ztfEpoch) Fetch {
	d, n := dilution(t, ZTFDilutionRadius)
	byBand := map[string][]lc.Point{}
	for _, e := range eps {
		byBand[e.Band] = append(byBand[e.Band], lc.Point{BJD: lc.BJD(lc.MJDTime(e.MJD), t.RA, t.Dec), Mag: e.Mag, Err: e.MagErr})
	}
	fe := Fetch{Survey: lc.ZTF, State: Done}
	for _, b := range []string{"g", "r", "i"} {
		pts := byBand[b]
		if len(pts) == 0 {
			continue
		}
		sort.Slice(pts, func(i, j int) bool { return pts[i].BJD < pts[j].BJD })
		fe.Series = append(fe.Series, lc.Series{Survey: lc.ZTF, Band: b, Points: pts, Dilution: d, Note: blendNote(d, n, ZTFDilutionRadius, "ZTF")})
	}
	if len(fe.Series) == 0 {
		fe.Note = fmt.Sprintf("no ZTF source within %.1f arcsec has good epochs", ztflc.MatchRadius)
	}
	return fe
}

func readCSV(b []byte) ([][]string, map[string]int, error) {
	cr := csv.NewReader(bytes.NewReader(b))
	cr.FieldsPerRecord = -1
	cr.Comment = '#'
	recs, err := cr.ReadAll()
	if err != nil {
		return nil, nil, err
	}
	if len(recs) == 0 {
		return nil, nil, errors.New("empty answer")
	}
	col := map[string]int{}
	for i, h := range recs[0] {
		col[strings.TrimSpace(h)] = i
	}
	rows := recs[1:]
	for i, r := range rows {
		for j := range r {
			r[j] = strings.TrimSpace(r[j])
		}
		if len(r) < len(recs[0]) {
			rows[i] = append(r, make([]string, len(recs[0])-len(r))...)
		}
	}
	return rows, col, nil
}
