package starcat

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

// GaiaEpoch is Gaia DR3's reference epoch, J2016.0, as a Julian year.
const GaiaEpoch = 2016.0

// Gaia variability flags.
const (
	VariableNotAvailable uint8 = 0
	VariableVariable     uint8 = 1
)

// GaiaStar is a Gaia DR3 gaia_source row: positions at GaiaEpoch in
// degrees, errors in mas, proper motions in mas/yr (pmRA with cos dec),
// parallax in mas. A column Gaia leaves empty is NaN. Float32 fields are
// float32 in Gaia's own data model, so they hold its exact values.
type GaiaStar struct {
	SourceID      int64
	RA, Dec       float64
	RAError       float32
	DecError      float32
	Parallax      float64
	ParallaxError float32
	PMRA          float64
	PMRAError     float32
	PMDec         float64
	PMDecError    float32
	G, BP, RP     float32
	RUWE          float32
	Variable      uint8
}

// At returns the star's position at a Julian year by its proper motion.
func (s *GaiaStar) At(year float64) (ra, dec float64) {
	return Propagate(s.RA, s.Dec, s.PMRA, s.PMDec, year-GaiaEpoch)
}

func gaiaSchema() schema[GaiaStar] {
	return schema[GaiaStar]{
		name: "gaia-dr3/1",
		pos:  func(s *GaiaStar) (float64, float64) { return s.RA, s.Dec },
		key:  func(s *GaiaStar) int64 { return s.SourceID },
		enc: func(w *colWriter, rows []GaiaStar) {
			n := len(rows)
			id := make([]int64, n)
			f64 := func(get func(*GaiaStar) float64) {
				v := make([]float64, n)
				for i := range rows {
					v[i] = get(&rows[i])
				}
				w.f64(v)
			}
			f32 := func(get func(*GaiaStar) float32) {
				v := make([]float32, n)
				for i := range rows {
					v[i] = get(&rows[i])
				}
				w.f32(v)
			}
			fl := make([]uint8, n)
			for i := range rows {
				id[i], fl[i] = rows[i].SourceID, rows[i].Variable
			}
			w.i64(id)
			f64(func(s *GaiaStar) float64 { return s.RA })
			f64(func(s *GaiaStar) float64 { return s.Dec })
			f32(func(s *GaiaStar) float32 { return s.RAError })
			f32(func(s *GaiaStar) float32 { return s.DecError })
			f64(func(s *GaiaStar) float64 { return s.Parallax })
			f32(func(s *GaiaStar) float32 { return s.ParallaxError })
			f64(func(s *GaiaStar) float64 { return s.PMRA })
			f32(func(s *GaiaStar) float32 { return s.PMRAError })
			f64(func(s *GaiaStar) float64 { return s.PMDec })
			f32(func(s *GaiaStar) float32 { return s.PMDecError })
			f32(func(s *GaiaStar) float32 { return s.G })
			f32(func(s *GaiaStar) float32 { return s.BP })
			f32(func(s *GaiaStar) float32 { return s.RP })
			f32(func(s *GaiaStar) float32 { return s.RUWE })
			w.u8(fl)
		},
		dec: func(r *colReader, n int) []GaiaStar {
			id := r.i64(n)
			ra, dec := r.f64(n), r.f64(n)
			rae, dece := r.f32(n), r.f32(n)
			plx, plxe := r.f64(n), r.f32(n)
			pma, pmae := r.f64(n), r.f32(n)
			pmd, pmde := r.f64(n), r.f32(n)
			g, bp, rp, ruwe := r.f32(n), r.f32(n), r.f32(n), r.f32(n)
			fl := r.u8(n)
			if r.err != nil {
				return nil
			}
			out := make([]GaiaStar, n)
			for i := range out {
				out[i] = GaiaStar{SourceID: id[i], RA: ra[i], Dec: dec[i], RAError: rae[i], DecError: dece[i],
					Parallax: plx[i], ParallaxError: plxe[i], PMRA: pma[i], PMRAError: pmae[i], PMDec: pmd[i], PMDecError: pmde[i],
					G: g[i], BP: bp[i], RP: rp[i], RUWE: ruwe[i], Variable: fl[i]}
			}
			return out
		},
	}
}

// gaiaColumns are the gaia_source columns read, with their ECSV datatype.
func gaiaColumns() [][2]string {
	const f32, f64 = "float32", "float64"
	return [][2]string{
		{"source_id", "int64"}, {"ref_epoch", f64}, {"ra", f64}, {"ra_error", f32},
		{"dec", f64}, {"dec_error", f32}, {"parallax", f64}, {"parallax_error", f32},
		{"pmra", f64}, {"pmra_error", f32}, {"pmdec", f64}, {"pmdec_error", f32},
		{"ruwe", f32}, {"phot_g_mean_mag", f32}, {"phot_bp_mean_mag", f32},
		{"phot_rp_mean_mag", f32}, {"phot_variable_flag", "string"},
	}
}

// splitCSV splits a CSV line into at most n fields, honouring double quotes.
func splitCSV(line []byte, dst [][]byte, n int) [][]byte {
	dst = dst[:0]
	start, quoted := 0, false
	for i := 0; i < len(line) && len(dst) < n; i++ {
		switch line[i] {
		case '"':
			quoted = !quoted
		case ',':
			if !quoted {
				dst = append(dst, line[start:i])
				start = i + 1
			}
		}
	}
	if len(dst) < n {
		dst = append(dst, line[start:])
	}
	return dst
}

func unquote(b []byte) []byte {
	if len(b) >= 2 && b[0] == '"' && b[len(b)-1] == '"' {
		return b[1 : len(b)-1]
	}
	return b
}

// ParseGaiaECSV reads one of ESA's gaia_source ECSV files (uncompressed)
// and calls fn with each row. It checks the columns' datatypes against
// Gaia's data model and that every row's ref_epoch is 2016.0.
func ParseGaiaECSV(r io.Reader, fn func(GaiaStar) error) (int, error) {
	br := bufio.NewReaderSize(r, 1<<20)
	want := gaiaColumns()
	types := map[string]string{}
	var col map[string]int
	var last string
	var fields [][]byte
	var idx [17]int
	maxIdx := 0
	rows := 0
	for {
		line, err := br.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			var long []byte
			long = append(long, line...)
			for errors.Is(err, bufio.ErrBufferFull) {
				line, err = br.ReadSlice('\n')
				long = append(long, line...)
			}
			line = long
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return rows, err
		}
		line = bytes.TrimRight(line, "\r\n")
		switch {
		case len(line) == 0:
		case line[0] == '#':
			t := strings.TrimRight(string(line[1:]), " ")
			if v, ok := strings.CutPrefix(t, "   name: "); ok {
				last = strings.TrimSpace(v)
			} else if v, ok := strings.CutPrefix(t, "   datatype: "); ok && last != "" {
				types[last] = strings.TrimSpace(v)
				last = ""
			}
		case col == nil:
			col = map[string]int{}
			for i, f := range splitCSV(line, nil, math.MaxInt) {
				col[string(unquote(f))] = i
			}
			for i, c := range want {
				j, ok := col[c[0]]
				if !ok {
					return 0, fmt.Errorf("gaia: no %s column", c[0])
				}
				if types[c[0]] != c[1] {
					return 0, fmt.Errorf("gaia: %s is %q, want %s", c[0], types[c[0]], c[1])
				}
				idx[i] = j
				maxIdx = max(maxIdx, j)
			}
		default:
			fields = splitCSV(line, fields, maxIdx+1)
			if len(fields) <= maxIdx {
				return rows, fmt.Errorf("gaia: row %d has %d fields", rows+1, len(fields))
			}
			s, err := gaiaRow(fields, &idx)
			if err != nil {
				return rows, fmt.Errorf("gaia: row %d: %w", rows+1, err)
			}
			if err := fn(s); err != nil {
				return rows, err
			}
			rows++
		}
		if err != nil {
			break
		}
	}
	if col == nil {
		return 0, errors.New("gaia: no header")
	}
	return rows, nil
}

func gaiaRow(f [][]byte, idx *[17]int) (GaiaStar, error) {
	var err error
	f64 := func(i int, nullable bool) float64 {
		b := f[idx[i]]
		if string(b) == "null" || len(b) == 0 {
			if !nullable && err == nil {
				err = fmt.Errorf("empty %s", gaiaColumns()[i][0])
			}
			return math.NaN()
		}
		v, e := strconv.ParseFloat(string(b), 64)
		if e != nil && err == nil {
			err = e
		}
		return v
	}
	f32 := func(i int) float32 {
		b := f[idx[i]]
		if string(b) == "null" || len(b) == 0 {
			return float32(math.NaN())
		}
		v, e := strconv.ParseFloat(string(b), 32)
		if e != nil && err == nil {
			err = e
		}
		return float32(v)
	}
	id, e := strconv.ParseInt(string(f[idx[0]]), 10, 64)
	if e != nil {
		return GaiaStar{}, e
	}
	if ep := f64(1, false); ep != GaiaEpoch {
		return GaiaStar{}, fmt.Errorf("source %d: ref_epoch %v", id, ep)
	}
	s := GaiaStar{SourceID: id, RA: f64(2, false), RAError: f32(3), Dec: f64(4, false), DecError: f32(5),
		Parallax: f64(6, true), ParallaxError: f32(7), PMRA: f64(8, true), PMRAError: f32(9),
		PMDec: f64(10, true), PMDecError: f32(11), RUWE: f32(12), G: f32(13), BP: f32(14), RP: f32(15)}
	if !(s.RA >= 0 && s.RA <= 360 && s.Dec >= -90 && s.Dec <= 90) {
		return GaiaStar{}, fmt.Errorf("source %d: position (%v, %v)", id, s.RA, s.Dec)
	}
	switch string(unquote(f[idx[16]])) {
	case "NOT_AVAILABLE":
		s.Variable = VariableNotAvailable
	case "VARIABLE":
		s.Variable = VariableVariable
	default:
		return GaiaStar{}, fmt.Errorf("source %d: phot_variable_flag %q", id, f[idx[16]])
	}
	return s, err
}
