package surveys

import (
	"math"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
)

const refCSV = `in_row_id,in_ra,in_dec,field,ccdid,qid,filtercode
1,323.277,58.6343,802,15,1,zg
1,323.277,58.6343,802,15,1,zr
1,323.277,58.6343,1835,9,3,zg
`

func ztfServer(t *testing.T, parquetHits *atomic.Int32) http.Handler {
	t.Helper()
	pq := readFixture(t, "ztf_000802_zg_c15_q1_dr24.parquet")
	mux := http.NewServeMux()
	mux.HandleFunc("/data/ZTF/lc/", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/data/ZTF/lc/":
			_, _ = w.Write([]byte(`<a href="lc_dr23/">lc_dr23/</a> <a href="lc_dr24/">lc_dr24/</a> <a href="lc_dr3/">lc_dr3/</a>`))
		case "/data/ZTF/lc/lc_dr24/":
			_, _ = w.Write([]byte(`<a href="0/">0/</a> <a href="1/">1/</a> <a href="README.txt">README.txt</a>`))
		case "/data/ZTF/lc/lc_dr24/0/":
			_, _ = w.Write([]byte(`<a href="field000802/">field000802/</a>`))
		case "/data/ZTF/lc/lc_dr24/1/":
			_, _ = w.Write([]byte(`<a href="field001835/">field001835/</a>`))
		case "/data/ZTF/lc/lc_dr24/0/field000802/ztf_000802_zg_c15_q1_dr24.parquet":
			parquetHits.Add(1)
			_, _ = w.Write(pq)
		default:
			http.NotFound(w, r)
		}
	})
	mux.HandleFunc("/ibe/search/ztf/products/ref", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("POS") == "" || r.URL.Query().Get("SIZE") != "1.0000" {
			http.Error(w, "bad query", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(refCSV))
	})
	mux.HandleFunc("/cgi-bin/ZTF/nph_light_curves", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("BANDNAME") != "g,r,i" || !strings.HasPrefix(q.Get("POS"), "CIRCLE 323.27") {
			http.Error(w, "bad query", http.StatusBadRequest)
			return
		}
		_, _ = w.Write(readFixture(t, "ztf_api_ic1396.csv"))
	})
	return mux
}

func TestZTFPrefetchAndRead(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	f, st := testFetcher(t, ztfServer(t, &hits))
	ctx := t.Context()
	field := ic1396Field()
	if err := f.Prefetch(ctx, field); err != nil {
		t.Fatal(err)
	}
	if err := f.Prefetch(ctx, field); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 1 {
		t.Fatalf("parquet fetched %d times, want once", hits.Load())
	}
	m, err := f.ztfManifest(ctx, field)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Complete || m.Release != "dr24" || len(m.Files) != 3 {
		t.Fatalf("manifest %+v", m)
	}
	e := m.Files["ztf_000802_zg_c15_q1_dr24"]
	if e.Objects != 4 || len(e.Tiles) == 0 {
		t.Fatalf("file entry %+v: want the 4 objects inside the footprint", e)
	}
	for _, k := range st.keys() {
		if !strings.HasPrefix(k, "vetting/cache/ic-1396-panel-1/ztf/") || !strings.HasSuffix(k, ".zst") {
			t.Errorf("unexpected key %s", k)
		}
	}
	fe := f.ZTF(ctx, field, ic1396Star())
	if fe.State != Done || len(fe.Series) != 1 || fe.Series[0].Band != "g" {
		t.Fatalf("fetch %+v", fe)
	}
	if !strings.Contains(fe.Note, "DR24 light curve files") {
		t.Errorf("note %q", fe.Note)
	}
	s := fe.Series[0]
	if s.Relative || s.Survey != lc.ZTF || s.Dilution != 1 {
		t.Errorf("series %+v", s)
	}
	api := f.ZTF(ctx, Field{Slug: "elsewhere"}, ic1396Star())
	if api.State != Done || !strings.Contains(api.Note, "not pulled yet") {
		t.Fatalf("fallback %+v", api)
	}
	var apiG lc.Series
	for _, x := range api.Series {
		if x.Band == "g" {
			apiG = x
		}
	}
	matched := 0
	for _, p := range s.Points {
		for _, q := range apiG.Points {
			if math.Abs(p.BJD-q.BJD) < 2e-5 {
				matched++
				if math.Abs(p.Mag-q.Mag) > 0.0006 || math.Abs(p.Err-q.Err)/q.Err > 0.03 {
					t.Errorf("mag %v±%v against %v±%v", p.Mag, p.Err, q.Mag, q.Err)
				}
			}
		}
	}
	t.Logf("%d field-file epochs, %d service epochs, %d matched", len(s.Points), len(apiG.Points), matched)
	if matched < 40 {
		t.Errorf("only %d of %d field-file epochs match the light curve service", matched, len(s.Points))
	}
}

func TestZTFTimesAreMidExposureBJD(t *testing.T) {
	t.Parallel()
	f := &Fetcher{}
	csv := "oid,expid,hjd,mjd,mag,magerr,catflags,filtercode,ra,dec\n" +
		"802115100046506,45447379,2458208.971546364,58208.473796300001,15.7305088,0.0143027212,0,zg,323.2769904,58.6343187\n" +
		"802115100046506,45550327,2458210.0010275492,58209.503275499999,15.8036652,0.0144547774,32768,zg,323.2769628,58.6343102\n"
	eps, err := f.parseZTFAPI([]byte(csv), 323.2769976, 58.6342899)
	if err != nil {
		t.Fatal(err)
	}
	if len(eps) != 1 {
		t.Fatalf("got %d epochs, want the catflags 0 one", len(eps))
	}
	got := lc.BJD(lc.MJDTime(eps[0].MJD), 323.2769976, 58.6342899)
	want := lc.BJD(lc.MJDTime(58208.473796300001+15.0/86400), 323.2769976, 58.6342899)
	if d := math.Abs(got-want) * 86400; d > 0.1 {
		t.Errorf("BJD from hjd is %.3f s from BJD of mid-exposure", d)
	}
}

func TestZTFUncoveredFallbackFails(t *testing.T) {
	t.Parallel()
	f, _ := testFetcher(t, http.NotFoundHandler())
	fe := f.ZTF(t.Context(), ic1396Field(), ic1396Star())
	if fe.State != Failed || !strings.Contains(fe.Note, "not pulled yet") {
		t.Fatalf("%+v", fe)
	}
}

func TestTileRoundTrip(t *testing.T) {
	t.Parallel()
	a := &ztfObject{ID: 802115100046506, RA: 323.277, Dec: 58.6343, Band: 'g'}
	a.add(58209.5026485, 15.8037, 0.0145)
	a.add(58208.4731693, 15.7305, 0.0143)
	b := &ztfObject{ID: 802115100046507, RA: 323.2753, Dec: 58.6339, Band: 'g'}
	b.add(58208.4731692, 16.1, 0.02)
	objs, err := decodeTile(encodeTile('g', []*ztfObject{a, b}), func(ra, _ float64) bool { return ra > 323.276 })
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 1 || objs[0].ID != a.ID || len(objs[0].T) != 2 {
		t.Fatalf("%+v", objs)
	}
	if d := math.Abs(objs[0].mjd(1)-58209.5026485) * 86400; d > 0.44 {
		t.Errorf("time off by %.3f s", d)
	}
	if objs[0].Mag[1] != 15804 || math.Abs(objs[0].magErr(0)-0.0143)/0.0143 > 0.016 {
		t.Errorf("mag %v err %v", objs[0].Mag, objs[0].magErr(0))
	}
	if _, err := decodeTile([]byte("ZTFT\x01g\x05"), func(float64, float64) bool { return true }); err == nil {
		t.Error("truncated tile read without error")
	}
}

func TestTilesNear(t *testing.T) {
	t.Parallel()
	for _, p := range [][2]float64{{0, 0}, {323.277, 58.63}, {10.68, 89.99}, {359.9999, -10}} {
		got := tilesNear(p[0], p[1], 2.5)
		if got[0] != tileOf(p[0], p[1]) {
			t.Errorf("%v: first tile is not the star's", p)
		}
	}
}
