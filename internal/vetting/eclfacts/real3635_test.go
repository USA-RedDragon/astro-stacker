package eclfacts

import (
	"bytes"
	"encoding/csv"
	"io"
	"os"
	"strconv"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/period"
	"github.com/klauspost/compress/zstd"
)

// series3635 reads the star's survey series from the vet input dump of
// source 3635 that the period package's tests use.
func series3635(t *testing.T) []lc.Series {
	t.Helper()
	raw, err := os.ReadFile("../period/testdata/vet3635/3635.csv.zst")
	if err != nil {
		t.Fatal(err)
	}
	dec, err := zstd.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()
	body, err := io.ReadAll(dec)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(bytes.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	col := map[string]int{}
	for i, h := range rows[0] {
		col[h] = i
	}
	f := func(r []string, k string) float64 {
		v, _ := strconv.ParseFloat(r[col[k]], 64)
		return v
	}
	type sk struct{ survey, band string }
	by := map[sk]*lc.Series{}
	var order []sk
	for _, r := range rows[1:] {
		if r[col["who"]] != "t" {
			continue
		}
		k := sk{r[col["survey"]], r[col["band"]]}
		s, ok := by[k]
		if !ok {
			s = &lc.Series{Survey: k.survey, Band: k.band, Note: r[col["note"]], Relative: r[col["rel"]] == "true", Deblended: r[col["deb"]] == "true", Dilution: f(r, "dil")}
			by[k] = s
			order = append(order, k)
		}
		src, _ := strconv.ParseInt(r[col["oid"]], 10, 64)
		s.Points = append(s.Points, lc.Point{BJD: f(r, "bjd") + 2450000, Mag: f(r, "mag"), Err: f(r, "err"), Src: src})
	}
	out := make([]lc.Series, 0, len(order))
	for _, k := range order {
		out = append(out, *by[k])
	}
	return out
}

// TestTwinRuns3635 is source 3635 at twice the star:15 vet's one dip
// period: bands disagree on which half's dip is deeper, so the
// conservative rule found no band and the twin test reported no depths
// while the report said the dips were alike. It must report the depths
// of the band that measures the difference best.
func TestTwinRuns3635(t *testing.T) {
	t.Parallel()
	one := &period.Ephemeris{Period: 3.919735367653492, PeriodErr: 1.2857e-05, T0: 2458317.3639296764, T0Err: 0.0058, Kind: period.KindEclipsing,
		Eclipses: []period.Eclipse{{Phase: 0, Width: 0.03166251527803854, Shape: 1.39416297165816, Depth: map[string]float64{
			"ATLAS c": 0.14438179898922565, atlasO: 0.19014528947232395, "NEOWISE W1": 0.30880236827873486,
			"NEOWISE W2": 0.2355212102749828, "TESS": 0.11856824085436138, "ZTF r": 0.19577203821877576}}}}
	s := Summarize(series3635(t), Doubled(one))
	if s == nil || s.Twin == nil {
		t.Fatal("no twin test")
	}
	tw := s.Twin
	t.Logf("%s: %.3f +- %.3f and %.3f +- %.3f, %.1f sigma; phase %.4f +- %.4f", tw.Band, tw.Depth1.Value, tw.Depth1.Err, tw.Depth2.Value, tw.Depth2.Err, tw.DepthZ, tw.Phase2, tw.PhaseErr)
	if tw.Band == "" || !(tw.Depth1.Value > 0.05) || !(tw.Depth2.Value > 0.05) || !(tw.Depth1.Err > 0) {
		t.Errorf("twin test reported no depths: %+v", tw)
	}
}
