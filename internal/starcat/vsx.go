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

// VSXNoFlag is VSXStar.V when the file leaves the variability flag blank.
const VSXNoFlag uint8 = 255

// VSXStar is a row of CDS's copy of AAVSO's VSX (B/vsx, vsx.dat): J2000
// positions in degrees, magnitudes, the epoch as HJD and the period in days.
// A blank number is NaN; strings are trimmed of blanks.
type VSXStar struct {
	OID                 int64
	Name                string
	V                   uint8
	RA, Dec             float64
	Type                string
	LMax                string
	Max                 float64
	UMax, NMax          string
	FMin, LMin          string
	Min                 float64
	UMin, NMin          string
	Epoch               float64
	UEpoch, LPeriod     string
	Period              float64
	UPeriod, SpectralTy string
}

// vsxLineLen is vsx.dat's record length.
const vsxLineLen = 209

func vsxSchema() schema[VSXStar] {
	return schema[VSXStar]{
		name: "vsx/1",
		pos:  func(s *VSXStar) (float64, float64) { return s.RA, s.Dec },
		key:  func(s *VSXStar) int64 { return s.OID },
		enc: func(w *colWriter, rows []VSXStar) {
			n := len(rows)
			id := make([]int64, n)
			v := make([]uint8, n)
			for i := range rows {
				id[i], v[i] = rows[i].OID, rows[i].V
			}
			str := func(get func(*VSXStar) string) {
				out := make([]string, n)
				for i := range rows {
					out[i] = get(&rows[i])
				}
				w.str(out)
			}
			f64 := func(get func(*VSXStar) float64) {
				out := make([]float64, n)
				for i := range rows {
					out[i] = get(&rows[i])
				}
				w.f64(out)
			}
			w.i64(id)
			w.u8(v)
			f64(func(s *VSXStar) float64 { return s.RA })
			f64(func(s *VSXStar) float64 { return s.Dec })
			f64(func(s *VSXStar) float64 { return s.Max })
			f64(func(s *VSXStar) float64 { return s.Min })
			f64(func(s *VSXStar) float64 { return s.Epoch })
			f64(func(s *VSXStar) float64 { return s.Period })
			str(func(s *VSXStar) string { return s.Name })
			str(func(s *VSXStar) string { return s.Type })
			str(func(s *VSXStar) string { return s.LMax })
			str(func(s *VSXStar) string { return s.UMax })
			str(func(s *VSXStar) string { return s.NMax })
			str(func(s *VSXStar) string { return s.FMin })
			str(func(s *VSXStar) string { return s.LMin })
			str(func(s *VSXStar) string { return s.UMin })
			str(func(s *VSXStar) string { return s.NMin })
			str(func(s *VSXStar) string { return s.UEpoch })
			str(func(s *VSXStar) string { return s.LPeriod })
			str(func(s *VSXStar) string { return s.UPeriod })
			str(func(s *VSXStar) string { return s.SpectralTy })
		},
		dec: func(r *colReader, n int) []VSXStar {
			id, v := r.i64(n), r.u8(n)
			ra, dec, mx, mn, ep, per := r.f64(n), r.f64(n), r.f64(n), r.f64(n), r.f64(n), r.f64(n)
			var s [13][]string
			for i := range s {
				s[i] = r.str(n)
			}
			if r.err != nil {
				return nil
			}
			out := make([]VSXStar, n)
			for i := range out {
				out[i] = VSXStar{OID: id[i], V: v[i], RA: ra[i], Dec: dec[i], Max: mx[i], Min: mn[i], Epoch: ep[i], Period: per[i],
					Name: s[0][i], Type: s[1][i], LMax: s[2][i], UMax: s[3][i], NMax: s[4][i], FMin: s[5][i], LMin: s[6][i],
					UMin: s[7][i], NMin: s[8][i], UEpoch: s[9][i], LPeriod: s[10][i], UPeriod: s[11][i], SpectralTy: s[12][i]}
			}
			return out
		},
	}
}

// ParseVSXDat reads vsx.dat and calls fn with each row.
func ParseVSXDat(r io.Reader, fn func(VSXStar) error) (int, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 4096), 1<<16)
	rows := 0
	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if len(line) > vsxLineLen {
			return rows, fmt.Errorf("vsx: line %d is %d bytes", rows+1, len(line))
		}
		s, err := vsxRow(line)
		if err != nil {
			return rows, fmt.Errorf("vsx: line %d: %w", rows+1, err)
		}
		if err := fn(s); err != nil {
			return rows, err
		}
		rows++
	}
	return rows, sc.Err()
}

func vsxRow(line []byte) (VSXStar, error) {
	pad := make([]byte, vsxLineLen)
	for i := range pad {
		pad[i] = ' '
	}
	copy(pad, line)
	field := func(a, b int) string { return strings.TrimSpace(string(pad[a-1 : b])) }
	var err error
	num := func(a, b int, required bool) float64 {
		t := field(a, b)
		if t == "" {
			if required && err == nil {
				err = fmt.Errorf("blank bytes %d-%d", a, b)
			}
			return math.NaN()
		}
		v, e := strconv.ParseFloat(t, 64)
		if e != nil && err == nil {
			err = e
		}
		return v
	}
	oid, e := strconv.ParseInt(field(1, 8), 10, 64)
	if e != nil {
		return VSXStar{}, e
	}
	s := VSXStar{OID: oid, Name: field(10, 39), V: VSXNoFlag, RA: num(43, 51, true), Dec: num(53, 61, true), Type: field(63, 92),
		LMax: field(94, 94), Max: num(96, 102, false), UMax: field(104, 104), NMax: field(106, 115),
		FMin: field(117, 117), LMin: field(119, 119), Min: num(121, 127, false), UMin: field(129, 129), NMin: field(131, 138),
		Epoch: num(140, 153, false), UEpoch: field(155, 155), LPeriod: field(157, 157), Period: num(159, 177, false),
		UPeriod: field(179, 179), SpectralTy: field(181, 209)}
	if f := field(41, 41); f != "" {
		v, e := strconv.ParseUint(f, 10, 8)
		if e != nil {
			return VSXStar{}, e
		}
		s.V = uint8(v)
	}
	if err == nil && (s.RA < 0 || s.RA >= 360 || s.Dec < -90 || s.Dec > 90) {
		err = errors.New("position out of range")
	}
	return s, err
}
