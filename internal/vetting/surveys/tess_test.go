package surveys

import (
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"testing"
)

const tessCSV = `bjd,flux,flux_err,aper_flux,aper_flux_err,quality
2458738.5100,1.0010,0.0020,1.0005,0.0021,0
2458738.5308,0.9990,0.0020,0.9995,0.0021,0
2458738.5517,0.8000,0.0020,0.8100,0.0021,0
2458738.5725,1.0000,0.0020,1.0000,0.0021,128
2458738.5933,nan,0.0020,1.0000,0.0021,0
`

func putStatus(t *testing.T, st *memStore, s TESSStatus) {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Put(t.Context(), "vetting/cache/ic-1396-panel-1/tess/2179309875368546432/status.json", b, "application/json"); err != nil {
		t.Fatal(err)
	}
}

func TestTESSContract(t *testing.T) {
	t.Parallel()
	f, st := testFetcher(t, http.NotFoundHandler())
	ctx := t.Context()
	field, star := ic1396Field(), ic1396Star()

	fe := f.TESS(ctx, field, star)
	if fe.State != Pending {
		t.Fatalf("%+v", fe)
	}
	raw, err := st.Get(ctx, "vetting/tess/requests/2179309875368546432.json")
	if err != nil {
		t.Fatal(err)
	}
	var req map[string]any
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"gaia_id", "ra", "dec", "pmra", "pmdec", "g", "neighbours", "field", "requested_at"} {
		if _, ok := req[k]; !ok {
			t.Errorf("request has no %s", k)
		}
	}
	if !strings.Contains(string(raw), `"gaia_id":2179309875368546432`) || !strings.Contains(string(raw), `"slug":"ic-1396-panel-1"`) {
		t.Errorf("request %s", raw)
	}

	z, err := zstdEncode([]byte(tessCSV))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Put(ctx, "vetting/cache/ic-1396-panel-1/tess/2179309875368546432/s0016.csv.zst", z, "application/zstd"); err != nil {
		t.Fatal(err)
	}
	putStatus(t, st, TESSStatus{GaiaID: star.GaiaID, SectorsAvailable: []int{16, 17, 56}, SectorsDone: []int{16}})
	fe = f.TESS(ctx, field, star)
	if fe.State != Pending || len(fe.Series) != 1 || len(fe.Sectors) != 1 || fe.Sectors[0] != 16 {
		t.Fatalf("partial %+v", fe)
	}
	s := fe.Series[0]
	if s.Band != "S16" || !s.Relative || !s.Deblended || s.Dilution != 1 || len(s.Points) != 3 {
		t.Fatalf("series %+v", s)
	}
	if d := s.Points[2].Mag - 2.5*math.Log10(1/0.8); math.Abs(d) > 1e-9 {
		t.Errorf("dip %v", s.Points[2].Mag)
	}

	putStatus(t, st, TESSStatus{GaiaID: star.GaiaID, SectorsAvailable: []int{16, 17, 56}, SectorsDone: []int{16}, SectorsFailed: map[string]string{"17": "no data", "56": "fit failed"}})
	fe = f.TESS(ctx, field, star)
	if fe.State != Done || !strings.Contains(fe.Note, "sector 56 failed: fit failed") {
		t.Fatalf("complete %+v", fe)
	}

	putStatus(t, st, TESSStatus{GaiaID: star.GaiaID})
	if fe := f.TESS(ctx, field, star); fe.State != Uncovered {
		t.Fatalf("no sectors %+v", fe)
	}
}
