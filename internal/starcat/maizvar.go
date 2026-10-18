package starcat

import (
	"errors"
	"math"
	"strings"
)

// maizFlushRows is how many rows the Maiz Apellaniz mirror holds before it
// stores them.
const maizFlushRows = 8_000_000

// MaizVariability is the Gaia DR3 three-band photometric dispersions of
// the 145,677,450 five-parameter sources to G 17 (Maiz Apellaniz+ 2023,
// J/A+A/677/A137), variable or not: ID is the Gaia DR3 source_id, the
// position is ICRS at 2016.0, Mag is G, Type is the variability flags in
// G, BP and RP (each B bad, N not variable, M marginal, V variable), Amp is
// S_G, the star's own scatter in G in magnitudes (an rms, not a range),
// and Score is S_G over its uncertainty. Name and Period are blank. To
// store compactly the position is rounded to under 1 mas, G to 0.001 mag,
// S_G to 0.1% and Score to 0.4%.
func MaizVariability() *VarCatalog {
	return &VarCatalog{Name: "maiz-var", Prefix: "catalogs/maiz-var", BaseURL: "https://cdsarc.cds.unistra.fr/ftp/J/A+A/677/A137/",
		File: "catalog.dat", Order: 6, VizieR: "J/A+A/677/A137/catalog", lrecl: 403, row: maizRow, flushRows: maizFlushRows}
}

// MaizVariesInG says whether a MaizVariability star is variable in G:
// its scatter in G is at least three times its uncertainty.
func MaizVariesInG(s *VarStar) bool { return len(s.Type) == 3 && s.Type[0] == 'V' }

func maizRow(f *fixedLine, _ int64) VarStar {
	if f.str(1, 9) != "Gaia DR3" && f.err == nil {
		f.err = errors.New("no Gaia DR3 name")
	}
	flags := f.str(298, 300)
	if (len(flags) != 3 || strings.Trim(flags, "BNMV") != "") && f.err == nil {
		f.err = errors.New("variability flags " + flags)
	}
	sg, ssg := f.num(136, 146, true), f.num(148, 156, true)
	score := nan32()
	if ssg > 0 {
		score = coarse32(sg/ssg, 8)
	}
	return VarStar{ID: f.i64(10, 28), RA: onGrid(f.num(30, 44, true)), Dec: onGrid(f.num(46, 60, true)), Mag: coarse32(f.num(62, 69, true), 14),
		Type: flags, Amp: coarse32(sg/1000, 10), Score: score, Period: math.NaN()}
}

// onGrid rounds a position in degrees to the nearest 2^-22 degree (0.86
// mas), so its low mantissa bits are zero and compress away.
func onGrid(v float64) float64 { return math.Round(v*(1<<22)) / (1 << 22) }

// coarse32 rounds v to bits of float32 mantissa, so the rest are zero.
func coarse32(v float64, bits uint) float32 {
	b := math.Float32bits(float32(v))
	drop := 23 - bits
	b = (b + 1<<(drop-1)) &^ (1<<drop - 1)
	return math.Float32frombits(b)
}
