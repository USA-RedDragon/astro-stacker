package period

import (
	"slices"
	"strconv"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
)

// vet3635 is the vet's own input for source 3635 (Gaia DR3
// 4315093031714485376, G 15.38), dumped from a star:15 dry-run on
// 2026-10-20: every survey series as the vet passed it to Analyze (who t,
// with the user's own points as who own) and the comparison stars it
// fetched (who c1, c2, ...), with each series' note, relative, deblended
// and dilution fields.
const vet3635 = "testdata/vet3635/3635.csv.zst"

// opts3635 is the Gaia photometry and parallax the vet used for 3635,
// with its Gaia DR3 BP-RP and GSP-Phot's 84th percentile of its
// E(BP-RP).
func opts3635(comps [][]lc.Series) Options {
	return Options{G: 15.383073806762695, Parallax: 0.3234230291357368, ParallaxErr: 0.03708537667989731,
		Colour:      &Colour{BPRP: 15.794554 - 14.797877, EBPRPUpper: 0.2113},
		Comparisons: func() [][]lc.Series { return comps }}
}

// vetDumpSeries reads a vet input dump: the star's series, its own
// points included, and the comparison stars in their order. Rows of one
// band with different notes are separate series, as the vet loads them:
// each ATLAS camera has its own zero point.
func vetDumpSeries(t *testing.T, path string) ([]lc.Series, [][]lc.Series) {
	t.Helper()
	type sk struct{ who, survey, band, note string }
	by := map[sk]*lc.Series{}
	var order []sk
	for _, r := range readCSV(t, path) {
		k := sk{r["who"], r["survey"], r["band"], r["note"]}
		s, ok := by[k]
		if !ok {
			dil := num(t, r["dil"])
			s = &lc.Series{Survey: k.survey, Band: k.band, Note: r["note"], Relative: r["rel"] == "true", Deblended: r["deb"] == "true", Dilution: dil}
			by[k] = s
			order = append(order, k)
		}
		src, _ := strconv.ParseInt(r["oid"], 10, 64)
		s.Points = append(s.Points, lc.Point{BJD: num(t, r["bjd"]) + 2450000, Mag: num(t, r["mag"]), Err: num(t, r["err"]), Src: src})
	}
	var target []lc.Series
	comps := map[string][]lc.Series{}
	var names []string
	for _, k := range order {
		if k.who == "t" || k.who == "own" {
			target = append(target, *by[k])
			continue
		}
		if _, ok := comps[k.who]; !ok {
			names = append(names, k.who)
		}
		comps[k.who] = append(comps[k.who], *by[k])
	}
	slices.SortFunc(names, func(a, b string) int {
		x, _ := strconv.Atoi(a[1:])
		y, _ := strconv.Atoi(b[1:])
		return x - y
	})
	out := make([][]lc.Series, 0, len(names))
	for _, n := range names {
		out = append(out, comps[n])
	}
	return target, out
}
