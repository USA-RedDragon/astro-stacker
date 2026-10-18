package starcat

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"path"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/klauspost/compress/gzip"
	"golang.org/x/sync/errgroup"
)

// VarCatalog is a survey's catalogue of variable stars as CDS serves it:
// one fixed-width file, gzipped, described by the catalogue's ReadMe.
type VarCatalog struct {
	// Name is the mirror's catalogue name in its manifest.
	Name string
	// Prefix is where the mirror lives in the survey bucket.
	Prefix string
	// BaseURL is the catalogue's CDS directory, with a trailing slash.
	BaseURL string
	// File is the data file as the ReadMe lists it; CDS serves File.gz.
	File string
	// Order is the HEALPix order the mirror is partitioned at.
	Order int
	// VizieR is the table's VizieR name.
	VizieR string
	// lrecl is the record length the byte ranges row reads are for.
	lrecl int
	row   func(f *fixedLine, n int64) VarStar
	// plain is set when CDS serves File itself rather than File.gz.
	plain bool
	// keep, when set, says which records the mirror stores; the others
	// are read and counted but left out.
	keep func(s VarStar) bool
	// flushRows, when set, stores the rows read so far each time this
	// many are held, so a catalogue too large to hold in memory is
	// mirrored in pieces; a partition then has a piece from each store it
	// had rows in.
	flushRows int64
}

// TESSEBNew is the TESS Ten Thousand Catalog's 7,936 new eclipsing binaries
// (Kostov+ 2025, J/ApJS/279/50 table3): TIC number, period and TESS
// magnitude.
func TESSEBNew() *VarCatalog {
	return &VarCatalog{Name: "tess-eb-new", Prefix: "catalogs/tess-eb-new", BaseURL: "https://cdsarc.cds.unistra.fr/ftp/J/ApJS/279/50/",
		File: "table3.dat", Order: 3, VizieR: "J/ApJS/279/50/table3", lrecl: 250, row: tessEBNewRow}
}

// TESSEBKnown is the TESS Ten Thousand Catalog's 2,065 known eclipsing
// binaries with their TESS periods (Kostov+ 2025, J/ApJS/279/50 table4).
func TESSEBKnown() *VarCatalog {
	return &VarCatalog{Name: "tess-eb-known", Prefix: "catalogs/tess-eb-known", BaseURL: "https://cdsarc.cds.unistra.fr/ftp/J/ApJS/279/50/",
		File: "table4.dat", Order: 3, VizieR: "J/ApJS/279/50/table4", lrecl: 147, row: tessEBKnownRow, plain: true}
}

// TESSEBPrsa is the TESS eclipsing binaries of sectors 1 to 26 (Prsa+ 2022,
// J/ApJS/258/16): TIC number, period and TESS magnitude; a TIC can have two
// ephemerides, so ID is the record number.
func TESSEBPrsa() *VarCatalog {
	return &VarCatalog{Name: "tess-eb-prsa", Prefix: "catalogs/tess-eb-prsa", BaseURL: "https://cdsarc.cds.unistra.fr/ftp/J/ApJS/258/16/",
		File: "tess-ebs.dat", Order: 3, VizieR: "J/ApJS/258/16/tess-ebs", lrecl: 625, row: tessEBPrsaRow}
}

// ZTFSuspected is the ZTF catalogue of suspected variables (Chen+ 2020,
// J/ApJS/249/18 table3): stars that vary in ZTF without a class, with the
// r (else g) period, mean r magnitude and amplitude; ID is the record
// number.
func ZTFSuspected() *VarCatalog {
	return &VarCatalog{Name: "ztf-sus", Prefix: "catalogs/ztf-sus", BaseURL: "https://cdsarc.cds.unistra.fr/ftp/J/ApJS/249/18/",
		File: "table3.dat", Order: 3, VizieR: "J/ApJS/249/18/table3", lrecl: 120, row: ztfSuspectedRow}
}

// TESSRotation is the TESS rotation periods of young stars in nearby
// groups (Kounkel+ 2022, J/AJ/164/137), one record per star and sector,
// those with a measured period; ID is the record number.
func TESSRotation() *VarCatalog {
	return &VarCatalog{Name: "tess-rot", Prefix: "catalogs/tess-rot", BaseURL: "https://cdsarc.cds.unistra.fr/ftp/J/AJ/164/137/",
		File: "table1.dat", Order: 3, VizieR: "J/AJ/164/137/table1", lrecl: 411, row: tessRotRow, plain: true,
		keep: func(s VarStar) bool { return s.Period > 0 }}
}

// GCVSExtragalactic is the GCVS catalogue of variable stars in other
// galaxies (B/gcvs/evs_cat), M31 and M33 among them: number, name, type
// and period, at their B1950 positions moved to J2000; stars without a
// position are left out.
func GCVSExtragalactic() *VarCatalog {
	return &VarCatalog{Name: "gcvs-evs", Prefix: "catalogs/gcvs-evs", BaseURL: "https://cdsarc.cds.unistra.fr/ftp/B/gcvs/",
		File: "evs_cat.dat", Order: 3, VizieR: "B/gcvs/evs_cat", lrecl: 160, row: gcvsEVSRow,
		keep: func(s VarStar) bool { return !math.IsNaN(s.RA) && !math.IsNaN(s.Dec) }}
}

// GaiaVariability is Gaia DR3's classification of its variable sources
// (vari_classifier_result, I/358/vclassre): source_id, best class and its
// score, at the sources' 2016.0 ICRS positions. Name is blank.
func GaiaVariability() *VarCatalog {
	return &VarCatalog{Name: "gaia-dr3-vari", Prefix: "catalogs/gaia-dr3-vari", BaseURL: "https://cdsarc.cds.unistra.fr/ftp/I/358/",
		File: "vclassre.dat", Order: 5, VizieR: "I/358/vclassre", lrecl: 137, row: gaiaVariRow}
}

// ATLASVariables is the ATLAS catalogue of candidate variable stars
// (Heinze+ 2018, J/AJ/156/241 table4): the ATLAS name, the Pan-STARRS
// objID, the final class (dubious for a candidate that may not vary) and
// the median o-band magnitude.
func ATLASVariables() *VarCatalog {
	return &VarCatalog{Name: "atlas-var", Prefix: "catalogs/atlas-var", BaseURL: "https://cdsarc.cds.unistra.fr/ftp/J/AJ/156/241/",
		File: "table4.dat", Order: 4, VizieR: "J/AJ/156/241/table4", lrecl: 1911, row: atlasRow}
}

// ZTFVariables is the ZTF catalogue of periodic variables (Chen+ 2020,
// J/ApJS/249/18 table2): name, sequence number, period, class, mean r
// magnitude and r amplitude.
func ZTFVariables() *VarCatalog {
	return &VarCatalog{Name: "ztf-var", Prefix: "catalogs/ztf-var", BaseURL: "https://cdsarc.cds.unistra.fr/ftp/J/ApJS/249/18/",
		File: "table2.dat", Order: 3, VizieR: "J/ApJS/249/18/table2", lrecl: 228, row: ztfRow}
}

// ASASSNVariables is the ASAS-SN catalogue of variable stars (Jayasinghe+,
// II/366, its 2021 version): name, class, its probability, period, mean V
// magnitude and amplitude. Its own IDs are not all numbers, so ID is the
// record number.
func ASASSNVariables() *VarCatalog {
	return &VarCatalog{Name: "asassn-var", Prefix: "catalogs/asassn-var", BaseURL: "https://cdsarc.cds.unistra.fr/ftp/II/366/",
		File: "catv2021.dat", Order: 3, VizieR: "II/366/catv2021", lrecl: 799, row: asassnRow}
}

// VarCatalogs returns the survey catalogues of variable stars.
func VarCatalogs() []*VarCatalog {
	return []*VarCatalog{GaiaVariability(), ATLASVariables(), ZTFVariables(), ASASSNVariables(),
		TESSEBNew(), TESSEBKnown(), TESSEBPrsa(), ZTFSuspected(), TESSRotation(), GCVSExtragalactic(), MaizVariability()}
}

// fixedLine reads the fields of a fixed-width record by their 1-based
// byte ranges, as CDS ReadMes give them; the first bad field is kept.
type fixedLine struct {
	b   []byte
	err error
}

func (f *fixedLine) str(a, b int) string {
	if a > len(f.b) {
		return ""
	}
	return strings.TrimSpace(string(f.b[a-1 : min(b, len(f.b))]))
}

func (f *fixedLine) num(a, b int, required bool) float64 {
	t := f.str(a, b)
	if t == "" {
		if required && f.err == nil {
			f.err = fmt.Errorf("blank bytes %d-%d", a, b)
		}
		return math.NaN()
	}
	v, err := strconv.ParseFloat(t, 64)
	if err != nil {
		if f.err == nil {
			f.err = fmt.Errorf("bytes %d-%d: %w", a, b, err)
		}
		return math.NaN()
	}
	return v
}

func (f *fixedLine) i64(a, b int) int64 {
	v, err := strconv.ParseInt(f.str(a, b), 10, 64)
	if err != nil && f.err == nil {
		f.err = fmt.Errorf("bytes %d-%d: %w", a, b, err)
	}
	return v
}

// f32 narrows v, and reads null (a catalogue's stand-in for blank) as NaN.
func f32(v float64, null ...float64) float32 {
	for _, n := range null {
		if v == n {
			return float32(math.NaN())
		}
	}
	return float32(v)
}

func nan32() float32 { return float32(math.NaN()) }

func gaiaVariRow(f *fixedLine, _ int64) VarStar {
	return VarStar{ID: f.i64(1, 19), Type: f.str(53, 78), Score: f32(f.num(80, 92, false)), RA: f.num(94, 114, true), Dec: f.num(116, 137, true),
		Period: math.NaN(), Mag: nan32(), Amp: nan32()}
}

func atlasRow(f *fixedLine, _ int64) VarStar {
	return VarStar{ID: f.i64(1894, 1911), Name: f.str(1, 17), RA: f.num(19, 27, true), Dec: f.num(29, 37, true), Type: f.str(1523, 1529),
		Mag: f32(f.num(1035, 1040, false)), Period: math.NaN(), Amp: nan32(), Score: nan32()}
}

func ztfRow(f *fixedLine, _ int64) VarStar {
	return VarStar{ID: f.i64(24, 29), Name: f.str(1, 22), RA: f.num(31, 39, true), Dec: f.num(41, 49, true), Period: f.num(51, 61, false),
		Mag: f32(f.num(97, 102, false), 0), Amp: f32(f.num(188, 192, false), 0), Type: f.str(212, 216), Score: nan32()}
}

func asassnRow(f *fixedLine, n int64) VarStar {
	return VarStar{ID: n, Name: f.str(21, 48), RA: f.num(81, 89, true), Dec: f.num(91, 99, true), Mag: f32(f.num(121, 125, false)),
		Amp: f32(f.num(127, 131, false)), Period: f.num(133, 144, false), Type: f.str(146, 153), Score: f32(f.num(155, 159, false))}
}

func ticName(f *fixedLine, a, b int) string {
	if n, err := strconv.ParseInt(f.str(a, b), 10, 64); err == nil {
		return "TIC " + strconv.FormatInt(n, 10)
	}
	return ""
}

func tessEBNewRow(f *fixedLine, _ int64) VarStar {
	return VarStar{ID: f.i64(1, 10), Name: ticName(f, 1, 10), RA: f.num(12, 23, true), Dec: f.num(25, 36, true), Mag: f32(f.num(38, 43, false)),
		Period: f.num(45, 53, true), Type: "EB", Amp: nan32(), Score: nan32()}
}

func tessEBKnownRow(f *fixedLine, _ int64) VarStar {
	return VarStar{ID: f.i64(1, 10), Name: ticName(f, 1, 10), RA: f.num(12, 23, true), Dec: f.num(25, 36, true), Period: f.num(38, 46, true),
		Type: "EB", Mag: nan32(), Amp: nan32(), Score: nan32()}
}

func tessEBPrsaRow(f *fixedLine, n int64) VarStar {
	return VarStar{ID: n, Name: ticName(f, 1, 10), RA: f.num(91, 109, true), Dec: f.num(111, 130, true), Mag: f32(f.num(161, 168, false)),
		Period: f.num(212, 232, false), Type: "EB", Amp: nan32(), Score: nan32()}
}

// orNaN reads null (a catalogue's stand-in for blank) as NaN.
func orNaN(v, null float64) float64 {
	if v == null {
		return math.NaN()
	}
	return v
}

func ztfSuspectedRow(f *fixedLine, n int64) VarStar {
	per, amp := orNaN(f.num(70, 80, false), 0), f32(f.num(88, 92, false), 0)
	if math.IsNaN(per) {
		per, amp = orNaN(f.num(58, 68, false), 0), f32(f.num(82, 86, false), 0)
	}
	return VarStar{ID: n, Name: f.str(1, 22), RA: f.num(24, 32, true), Dec: f.num(34, 42, true), Mag: f32(f.num(51, 56, false), 0),
		Period: per, Amp: amp, Type: "VAR", Score: nan32()}
}

func tessRotRow(f *fixedLine, n int64) VarStar {
	per := f.num(138, 146, false)
	for _, r := range [][2]int{{193, 200}, {243, 250}} {
		if !(per > 0) {
			per = f.num(r[0], r[1], false)
		}
	}
	return VarStar{ID: n, Name: ticName(f, 21, 30), RA: f.num(32, 41, true), Dec: f.num(43, 52, true), Mag: f32(f.num(90, 95, false)),
		Period: per, Amp: f32(f.num(177, 183, false)), Type: "ROT", Score: nan32()}
}

func gcvsEVSRow(f *fixedLine, _ int64) VarStar {
	s := VarStar{ID: f.i64(1, 7), Name: f.str(9, 20), Type: f.str(41, 48), Mag: f32(f.num(49, 53, false)), Period: f.num(81, 93, false),
		Amp: nan32(), Score: nan32(), RA: math.NaN(), Dec: math.NaN()}
	rh, rm, rs := f.num(22, 23, false), f.num(24, 25, false), f.num(26, 30, false)
	dd, dm, ds := f.num(32, 33, false), f.num(34, 35, false), f.num(36, 39, false)
	if math.IsNaN(rh + rm + rs + dd + dm + ds) {
		return s
	}
	dec := dd + dm/60 + ds/3600
	if f.str(31, 31) == "-" {
		dec = -dec
	}
	s.RA, s.Dec = b1950ToJ2000(15*(rh+rm/60+rs/3600), dec)
	return s
}

// b1950ToJ2000 moves a B1950 (FK4) position to J2000 (FK5) by the
// standard rotation, without the E-terms of aberration (under 0.35
// arcsec) or proper motion.
func b1950ToJ2000(ra, dec float64) (float64, float64) {
	m := [3][3]float64{
		{0.9999256782, -0.0111820611, -0.0048579477},
		{0.0111820610, 0.9999374784, -0.0000271765},
		{0.0048579479, -0.0000271474, 0.9999881997},
	}
	r, d := ra*math.Pi/180, dec*math.Pi/180
	v := [3]float64{math.Cos(d) * math.Cos(r), math.Cos(d) * math.Sin(r), math.Sin(d)}
	var w [3]float64
	for i := range 3 {
		for j := range 3 {
			w[i] += m[i][j] * v[j]
		}
	}
	ra2 := math.Atan2(w[1], w[0]) * 180 / math.Pi
	if ra2 < 0 {
		ra2 += 360
	}
	return ra2, math.Asin(math.Max(-1, math.Min(1, w[2]))) * 180 / math.Pi
}

// parse reads one record, n counting from 1; keep is false for a record
// the catalogue's filter leaves out.
func (c *VarCatalog) parse(line []byte, n int64) (VarStar, bool, error) {
	f := &fixedLine{b: line}
	s := c.row(f, n)
	if f.err != nil {
		return s, false, f.err
	}
	if c.keep != nil && !c.keep(s) {
		return s, false, nil
	}
	if !(s.RA >= 0 && s.RA < 360 && s.Dec >= -90 && s.Dec <= 90) {
		return s, false, errors.New("position out of range")
	}
	return s, true, nil
}

// source is the URL of the catalogue's data file.
func (c *VarCatalog) source(base string) string {
	if c.plain {
		return base + c.File
	}
	return base + c.File + ".gz"
}

// Mirror mirrors the catalogue when its ReadMe has changed since the
// stored version: the file is read once, its gzip checksum and length,
// record length and record count checked against the ReadMe, its rows
// split by HEALPix pixel into a new version's pieces and verified, the
// manifest switched to it, and older versions deleted. A broken download
// resumes from the byte it stopped at. A record that does not read fails
// the mirror, unless the download broke right after it, which is tried
// again.
func (c *VarCatalog) Mirror(ctx context.Context, o MirrorOptions) error {
	o.client()
	readme, err := o.fetchSmall(ctx, o.BaseURL+"ReadMe")
	if err != nil {
		return err
	}
	mm := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(c.File) + `\s+(\d+)\s+(\d+)\s`).FindSubmatch(readme)
	if mm == nil {
		return fmt.Errorf("%s: no %s line in the ReadMe", c.Name, c.File)
	}
	lrecl, _ := strconv.Atoi(string(mm[1]))
	records, _ := strconv.ParseInt(string(mm[2]), 10, 64)
	if lrecl != c.lrecl {
		return fmt.Errorf("%s: record length %d in the ReadMe, expected %d", c.Name, lrecl, c.lrecl)
	}
	version := sha256Hex(readme)[:16]
	if b, err := o.Bucket.Get(ctx, path.Join(o.Prefix, manifestName)); err == nil {
		if m, err := decodeManifest(b); err == nil && m.Version == version && !m.Verified.IsZero() {
			slog.Info("Variable star mirror is current", "catalog", c.Name, "version", version, "rows", m.Rows)
			return nil
		}
	}
	t0 := time.Now()
	vprefix := path.Join(o.Prefix, version)
	src := c.source(o.BaseURL)
	man := &Manifest{Catalog: c.Name, Schema: varSchema().name, Order: c.Order, Version: version, Source: src, Created: time.Now().UTC()}
	buf := &varBuffer{c: c, o: &o, vprefix: vprefix, man: man, parts: map[int64][]VarStar{}, types: map[string]string{}}
	var read, size int64
	sum := sha256.New()
	var n, total atomic.Int64
	var badRow error
	err = retry(ctx, src, func() error {
		buf.reset()
		badRow = nil
		read, size = 0, 0
		sum.Reset()
		n.Store(0)
		return o.downloadResuming(ctx, src, &n, &total, func(r io.Reader, resp *http.Response) error {
			o.Report.Track("Downloading the catalogue", "bytes", resp.ContentLength, &n)
			tee := io.TeeReader(r, sum)
			in := tee
			if !c.plain {
				zr, err := gzip.NewReader(tee)
				if err != nil {
					return err
				}
				in = zr
			}
			var err error
			if read, badRow, err = c.scan(ctx, buf, in, lrecl, func() {
				o.Report.Track("Downloading the catalogue", "bytes", resp.ContentLength, &n)
			}); err != nil || badRow != nil {
				return err
			}
			if _, err := io.Copy(io.Discard, tee); err != nil {
				return err
			}
			size = n.Load()
			if resp.ContentLength >= 0 && size != resp.ContentLength {
				return fmt.Errorf("%s: read %d bytes of %d", c.Name, size, resp.ContentLength)
			}
			return nil
		})
	})
	if err == nil {
		err = badRow
	}
	if err == nil && read != records {
		err = fmt.Errorf("%s: %d rows, the ReadMe lists %d", c.Name, read, records)
	}
	if err != nil {
		if len(man.Pieces) > 0 {
			return errors.Join(err, o.dropUnlisted(ctx, vprefix, nil))
		}
		return err
	}
	slog.Info("Variable star catalogue read", "catalog", c.Name, "rows", buf.rows, "partitions", len(buf.parts), "downloaded MB", size>>20,
		"took", time.Since(t0).Round(time.Second))
	rows := buf.rows
	man.Rows = rows
	man.Inputs = []Input{{File: path.Base(src), SHA256: hex.EncodeToString(sum.Sum(nil)), Bytes: size, Rows: read}}
	if err := o.storeVars(ctx, vprefix, c.Order, buf.parts, man, buf.seq); err != nil {
		return err
	}
	if buf.seq >= 0 {
		if err := o.dropUnlisted(ctx, vprefix, man.Pieces); err != nil {
			return err
		}
	}
	bad, err := verifyPieces(ctx, o.Bucket, varSchema(), c.Order, man.Pieces, o.Report)
	if err != nil {
		return err
	}
	if len(bad) > 0 {
		return fmt.Errorf("%s: %d pieces fail verification, first %s", c.Name, len(bad), bad[0])
	}
	man.Verified = time.Now().UTC()
	b, err := encodeManifest(man)
	if err != nil {
		return err
	}
	if err := o.Bucket.Put(ctx, path.Join(vprefix, manifestName), b); err != nil {
		return err
	}
	if err := o.Bucket.Put(ctx, path.Join(o.Prefix, manifestName), b); err != nil {
		return err
	}
	slog.Info("Variable star catalogue mirrored", "catalog", c.Name, "version", version, "rows", rows, "pieces", len(man.Pieces),
		"stored MB", fmt.Sprintf("%.1f", float64(man.Bytes)/1e6), "took", time.Since(t0).Round(time.Second))
	return o.pruneVSX(ctx, version)
}

// scan reads the records into buf, calling stored after buf stores them.
// bad is a record that does not read, unless the download broke right
// after it, which is err.
func (c *VarCatalog) scan(ctx context.Context, buf *varBuffer, in io.Reader, lrecl int, stored func()) (read int64, bad, err error) {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Bytes()
		read++
		s, keep, err := c.parse(line, read)
		if len(line) > lrecl {
			err = fmt.Errorf("%d bytes, the ReadMe says %d", len(line), lrecl)
		}
		if err != nil {
			bad := fmt.Errorf("%s: line %d: %w", c.Name, read, err)
			if !sc.Scan() && sc.Err() != nil {
				return read, nil, sc.Err()
			}
			return read, bad, nil
		}
		if !keep {
			continue
		}
		did, err := buf.add(ctx, s)
		if err != nil {
			return read, nil, err
		}
		if did {
			stored()
		}
	}
	return read, nil, sc.Err()
}

// varBuffer holds a catalogue's rows by partition until they are stored.
type varBuffer struct {
	c       *VarCatalog
	o       *MirrorOptions
	vprefix string
	man     *Manifest
	parts   map[int64][]VarStar
	types   map[string]string
	// rows counts the rows added; held those not yet stored.
	rows, held int64
	// seq numbers the stores of a catalogue stored in several, and is -1
	// for one stored at the end.
	seq int
}

// reset empties the buffer and the manifest's pieces for a new read.
func (b *varBuffer) reset() {
	clear(b.parts)
	b.rows, b.held, b.seq = 0, 0, -1
	if b.c.flushRows > 0 {
		b.seq = 0
	}
	b.man.Pieces, b.man.Bytes = nil, 0
}

// add holds a row, storing every held row when the catalogue's flushRows
// are held; stored says it did.
func (b *varBuffer) add(ctx context.Context, s VarStar) (stored bool, err error) {
	t, ok := b.types[s.Type]
	if !ok {
		t = s.Type
		b.types[t] = t
	}
	s.Type = t
	p := Pixel(b.c.Order, s.RA, s.Dec)
	b.parts[p] = append(b.parts[p], s)
	b.rows++
	b.held++
	if b.c.flushRows <= 0 || b.held < b.c.flushRows {
		return false, nil
	}
	if err := b.o.storeVars(ctx, b.vprefix, b.c.Order, b.parts, b.man, b.seq); err != nil {
		return false, err
	}
	b.seq++
	b.held = 0
	return true, nil
}

// storeVars encodes and stores each partition's rows as a piece and
// empties parts. With seq < 0 a partition has one piece; otherwise this
// is the seq'th store of a catalogue stored in several, and a partition
// has a piece from each store it had rows in.
func (o *MirrorOptions) storeVars(ctx context.Context, vprefix string, order int, parts map[int64][]VarStar, man *Manifest, seq int) error {
	keys := make([]int64, 0, len(parts))
	for p := range parts {
		keys = append(keys, p)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	pieces := make([]Piece, len(keys))
	var done atomic.Int64
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(runtime.GOMAXPROCS(0))
	for i, p := range keys {
		rs := parts[p]
		g.Go(func() error {
			data, err := encodePiece(varSchema(), rs)
			if err != nil {
				return err
			}
			name := strconv.FormatInt(p, 10) + ".scat"
			if seq >= 0 {
				name = path.Join(strconv.FormatInt(p, 10), strconv.Itoa(seq)+".scat")
			}
			pc := Piece{Partition: p, Key: path.Join(vprefix, "order"+strconv.Itoa(order), name),
				Rows: int64(len(rs)), Bytes: int64(len(data)), SHA256: sha256Hex(data)}
			if err := o.Bucket.Put(gctx, pc.Key, data); err != nil {
				return err
			}
			pieces[i] = pc
			o.Report.Set("Storing the catalogue", done.Add(1), int64(len(keys)), "pieces")
			return nil
		})
	}
	err := g.Wait()
	clear(parts)
	if err != nil {
		return err
	}
	for _, pc := range pieces {
		man.Pieces = append(man.Pieces, pc)
		man.Bytes += pc.Bytes
	}
	return nil
}

// dropUnlisted deletes the objects under vprefix that are not pieces in
// keep: those of a store that was tried again, or of one that failed.
func (o *MirrorOptions) dropUnlisted(ctx context.Context, vprefix string, keep []Piece) error {
	listed := make(map[string]bool, len(keep))
	for _, p := range keep {
		listed[p.Key] = true
	}
	all, err := o.Bucket.List(ctx, vprefix+"/")
	if err != nil {
		return err
	}
	for _, k := range all {
		if !listed[k] {
			if err := o.Bucket.Delete(ctx, k); err != nil {
				return err
			}
		}
	}
	return nil
}
