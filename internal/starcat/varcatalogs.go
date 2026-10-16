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
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/klauspost/compress/gzip"
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
	return []*VarCatalog{GaiaVariability(), ATLASVariables(), ZTFVariables(), ASASSNVariables()}
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

// parse reads one record, n counting from 1.
func (c *VarCatalog) parse(line []byte, n int64) (VarStar, error) {
	f := &fixedLine{b: line}
	s := c.row(f, n)
	if f.err == nil && !(s.RA >= 0 && s.RA < 360 && s.Dec >= -90 && s.Dec <= 90) {
		f.err = errors.New("position out of range")
	}
	return s, f.err
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
	parts := map[int64][]VarStar{}
	types := map[string]string{}
	var rows, size int64
	sum := sha256.New()
	var n, total atomic.Int64
	src := o.BaseURL + c.File + ".gz"
	var badRow error
	err = retry(ctx, src, func() error {
		clear(parts)
		badRow = nil
		rows, size = 0, 0
		sum.Reset()
		n.Store(0)
		return o.downloadResuming(ctx, src, &n, &total, func(r io.Reader, resp *http.Response) error {
			o.Report.Track("Downloading the catalogue", "bytes", resp.ContentLength, &n)
			tee := io.TeeReader(r, sum)
			zr, err := gzip.NewReader(tee)
			if err != nil {
				return err
			}
			sc := bufio.NewScanner(zr)
			sc.Buffer(make([]byte, 64<<10), 1<<20)
			for sc.Scan() {
				line := sc.Bytes()
				s, err := c.parse(line, rows+1)
				if len(line) > lrecl {
					err = fmt.Errorf("%d bytes, the ReadMe says %d", len(line), lrecl)
				}
				if err != nil {
					bad := fmt.Errorf("%s: line %d: %w", c.Name, rows+1, err)
					if !sc.Scan() && sc.Err() != nil {
						return sc.Err()
					}
					badRow = bad
					return nil
				}
				t, ok := types[s.Type]
				if !ok {
					t = s.Type
					types[t] = t
				}
				s.Type = t
				p := Pixel(c.Order, s.RA, s.Dec)
				parts[p] = append(parts[p], s)
				rows++
			}
			if err := sc.Err(); err != nil {
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
	if err != nil {
		return err
	}
	if rows != records {
		return fmt.Errorf("%s: %d rows, the ReadMe lists %d", c.Name, rows, records)
	}
	slog.Info("Variable star catalogue read", "catalog", c.Name, "rows", rows, "partitions", len(parts), "downloaded MB", size>>20,
		"took", time.Since(t0).Round(time.Second))
	vprefix := path.Join(o.Prefix, version)
	man := &Manifest{Catalog: c.Name, Schema: varSchema().name, Order: c.Order, Version: version, Source: src, Created: time.Now().UTC(), Rows: rows,
		Inputs: []Input{{File: c.File + ".gz", SHA256: hex.EncodeToString(sum.Sum(nil)), Bytes: size, Rows: rows}}}
	if err := o.storeVars(ctx, vprefix, c.Order, parts, man); err != nil {
		return err
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

// storeVars encodes and stores each partition's rows as one piece.
func (o *MirrorOptions) storeVars(ctx context.Context, vprefix string, order int, parts map[int64][]VarStar, man *Manifest) error {
	keys := make([]int64, 0, len(parts))
	for p := range parts {
		keys = append(keys, p)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	for i, p := range keys {
		o.Report.Set("Storing the catalogue", int64(i), int64(len(keys)), "pieces")
		data, err := encodePiece(varSchema(), parts[p])
		if err != nil {
			return err
		}
		pc := Piece{Partition: p, Key: path.Join(vprefix, "order"+strconv.Itoa(order), strconv.FormatInt(p, 10)+".scat"),
			Rows: int64(len(parts[p])), Bytes: int64(len(data)), SHA256: sha256Hex(data)}
		if err := o.Bucket.Put(ctx, pc.Key, data); err != nil {
			return err
		}
		man.Pieces = append(man.Pieces, pc)
		man.Bytes += pc.Bytes
		delete(parts, p)
	}
	return nil
}
