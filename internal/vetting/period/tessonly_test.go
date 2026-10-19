package period

import (
	"testing"
)

// TestTESSOnlyDips17189 is source 17189 (Cygnus Loop Panel 2, Gaia DR3
// 1859426140000463488, G 15.95) with the ZTF points from IRSA and the
// ATLAS and TESS light curves the vet cached. The vet called it an
// eclipsing binary at 26.82863 days from two TESS dips of 0.09 mag, while
// ZTF and ATLAS measure 0.01 mag or less inside the dips on 24 nights,
// two of them during TESS's own dips, and sector 82 covers a predicted
// dip and stays flat. The dips are not on the star: the vet must not let
// TESS confirm them.
func TestTESSOnlyDips17189(t *testing.T) {
	t.Parallel()
	series, _ := caseSeries(t, "testdata/tessonly/17189.csv.zst")
	e := &Ephemeris{Period: 26.828629216149583, T0: 2459058.2969000363, Kind: KindEclipsing,
		Eclipses: []Eclipse{{Phase: 0, Width: 0.0070227768983285935, Shape: 0.3}}}
	res, err := AnalyzeAt(series, e, Options{})
	if err != nil {
		t.Fatal(err)
	}
	r := res.Robust
	if r == nil {
		t.Fatal("no robust tests")
	}
	t.Logf("TESS %.3f, ZTF and ATLAS %.3f", r.TESSDepth, r.GroundDepth)
	if !r.TESSOnly || r.TESS {
		t.Errorf("TESSOnly %v, TESS %v; want the dips found in TESS only", r.TESSOnly, r.TESS)
	}
}
