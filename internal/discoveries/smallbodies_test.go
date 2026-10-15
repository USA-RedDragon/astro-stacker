package discoveries

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const localJSON = `{"epoch_jd_utc":2460735.844356,"observer":"geocentre","orbits":"2026-10-14","grid":"2026-10-14","count":3,"bodies":[
{"number":"433","name":"Eros","class":"NEA>Amor","kind":"asteroid","ra":188.1,"dec":14.2,"v":17.25,"dra":-20.5,"ddec":10.25,"sep_arcsec":12},
{"number":"","name":"C/2023 A3 (Tsuchinshan-ATLAS)","class":"Comet","kind":"comet","ra":188.2,"dec":14.3,"v":null,"dra":1,"ddec":2,"sep_arcsec":20},
{"number":"","name":"Jupiter","class":"Planet","kind":"planet","ra":188.3,"dec":14.4,"v":null,"dra":null,"ddec":null,"sep_arcsec":30}]}`

const skybotText = `# Flag: 1
# Num | Name | RA(h) | DE(deg) | Class | Mv | Err(arcsec) | d(arcsec) | dRA(arcsec/h) | dDEC(arcsec/h) | Dg(ua) | Dh(ua)
433 | Eros | 12 32 24.0000 | +14 12 00.000 | NEA>Amor | 17.2 | 0.010 | 12.0 | -20.5 | 10.2 | 0.5 | 1.4
 - | 2015 XX1 | 12 33 00.0000 | +14 00 00.000 | MB>Inner | 19.0 | 0.1 | 50 | -10 | 5 | 1.5 | 2.4
`

func TestParseSmallBodies(t *testing.T) {
	t.Parallel()
	bs, err := ParseSmallBodies([]byte(localJSON))
	if err != nil || len(bs) != 2 {
		t.Fatalf("%+v %v", bs, err)
	}
	if bs[0].Number != "433" || bs[0].Name != "Eros" || bs[0].V != 17.25 || bs[0].DRA != -20.5 || bs[0].DDec != 10.25 || bs[0].Class != "NEA>Amor" {
		t.Errorf("eros %+v", bs[0])
	}
	if bs[1].V != 0 || bs[1].Number != "" {
		t.Errorf("comet with no magnitude %+v", bs[1])
	}
	if _, err := ParseSmallBodies([]byte(`{"error":"x"}`)); err == nil {
		t.Error("an error reply parsed")
	}
}

func TestKnownBodiesLocalTopocentricAndCrossChecks(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var localQueries, skybotQueries, sbidentQueries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/v1/cone":
			localQueries = append(localQueries, r.URL.RawQuery)
			fmt.Fprint(w, localJSON)
		case "/skybot":
			skybotQueries = append(skybotQueries, r.URL.RawQuery)
			fmt.Fprint(w, skybotText)
		case "/sbident":
			sbidentQueries = append(sbidentQueries, r.URL.RawQuery)
			fmt.Fprint(w, `{"fields_second":["Object name","Astrometric RA (hh:mm:ss)","Astrometric Dec (dd mm'ss\")","Dist. from center RA (\")","Dist. from center Dec (\")","Dist. from center Norm (\")","Visual magnitude (V)","RA rate (\"/h)","Dec rate (\"/h)"],"n_second_pass":1,"data_second_pass":[["433 Eros (A898 PA)","12:32:24.00","+14 12'00.0\"","1","1","1","17.2","-2.05E+01","1.02E+01"]]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	done := make(chan struct{})
	c := &Catalogs{SkyBoTURL: srv.URL + "/skybot", Local: SmallBodies{
		URL:          srv.URL,
		Topocentric:  true,
		Site:         func(context.Context) (float64, float64, float64, bool) { return 12.5, -45.25, 300, true },
		CrossCheck:   []string{CrossCheckSkyBoT, CrossCheckSBIdent},
		SBIdentURL:   srv.URL + "/sbident",
		crossChecked: func() { close(done) },
	}}
	epoch := time.Date(2025, 3, 1, 8, 15, 52, 0, time.UTC)
	bs, err := c.KnownBodies(context.Background(), 188.1, 14.2, 1, epoch)
	if err != nil || len(bs) != 2 {
		t.Fatalf("%+v %v", bs, err)
	}
	<-done
	mu.Lock()
	defer mu.Unlock()
	if len(localQueries) != 2 || !strings.Contains(localQueries[0], "site=12.500000%2C-45.250000%2C300.0") || strings.Contains(localQueries[1], "site=") {
		t.Errorf("local queries %q: want one topocentric and one geocentric for the cross-checks", localQueries)
	}
	for _, q := range append(append([]string{}, skybotQueries...), sbidentQueries...) {
		if strings.Contains(q, "12.5") || strings.Contains(q, "45.25") {
			t.Errorf("the site went to a public service: %q", q)
		}
	}
	if len(skybotQueries) != 1 || !strings.Contains(skybotQueries[0], "-loc=500") || len(sbidentQueries) != 1 || !strings.Contains(sbidentQueries[0], "mpc-code=500") {
		t.Errorf("cross-checks skybot %q sbident %q", skybotQueries, sbidentQueries)
	}
}

func TestSmallBodiesErrorHidesSite(t *testing.T) {
	t.Parallel()
	c := &Catalogs{Local: SmallBodies{
		URL:         "http://127.0.0.1:1",
		Topocentric: true,
		Site:        func(context.Context) (float64, float64, float64, bool) { return 12.5, -45.25, 300, true },
	}}
	_, err := c.KnownBodies(context.Background(), 188.1, 14.2, 1, time.Now())
	if err == nil || strings.Contains(err.Error(), "12.5") || strings.Contains(err.Error(), "site") {
		t.Errorf("error %v", err)
	}
}

func TestKnownBodiesFallsBackToSkyBoT(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, skybotText) }))
	defer srv.Close()
	c := &Catalogs{SkyBoTURL: srv.URL}
	bs, err := c.KnownBodies(context.Background(), 188.1, 14.2, 1, time.Now())
	if err != nil || len(bs) != 2 || bs[1].Name != "2015 XX1" {
		t.Errorf("%+v %v", bs, err)
	}
}

func TestParseSBIdent(t *testing.T) {
	t.Parallel()
	b := []byte(`{"fields_second":["Object name","Astrometric RA (hh:mm:ss)","Astrometric Dec (dd mm'ss\")","Dist. from center RA (\")","Dist. from center Dec (\")","Dist. from center Norm (\")","Visual magnitude (V)","RA rate (\"/h)","Dec rate (\"/h)"],"n_second_pass":2,
"data_second_pass":[["64676 (2001 XE68)","12:31:43.19","+14 29'00.0\"","-2.E3","840.","1765.","19.0","-2.776E+01","1.687E+01"],["C/2023 A3 (Tsuchinshan-ATLAS)","00:10:00.00","-05 30'00.0\"","0","0","0","n.a.","1","2"]]}`)
	bs, err := ParseSBIdent(b)
	if err != nil || len(bs) != 2 {
		t.Fatalf("%+v %v", bs, err)
	}
	if bs[0].Number != "64676" || bs[0].Name != "2001 XE68" || bs[0].V != 19 || bs[0].DRA != -27.76 {
		t.Errorf("%+v", bs[0])
	}
	if bs[1].Number != "" || bs[1].Name != "C/2023 A3 (Tsuchinshan-ATLAS)" || bs[1].Dec != -5.5 || bs[1].RA != 2.5 {
		t.Errorf("%+v", bs[1])
	}
	if n, name := splitSBIdentName("433 Eros (A898 PA)"); n != "433" || name != "Eros" {
		t.Errorf("%q %q", n, name)
	}
	if sexaDMS(-5.5) != "M05-30-00.00" || sexaHMS(12.5, "-") != "12-30-00.00" || sexaHMS(1+59.0/60+59.9999/3600, "-") != "02-00-00.00" {
		t.Errorf("%s %s %s", sexaDMS(-5.5), sexaHMS(12.5, "-"), sexaHMS(1+59.0/60+59.9999/3600, "-"))
	}
}

func TestCompareBodies(t *testing.T) {
	t.Parallel()
	local := []Body{
		{Number: "433", Name: "Eros", RA: 10, Dec: 10},
		{Name: "2015 XX1", RA: 11, Dec: 10},
		{Name: "2020 AB", RA: 12, Dec: 10},
	}
	other := []Body{
		{Number: "433", Name: "Eros", RA: 10, Dec: 10 + 0.5/3600},
		{Name: "2015 xx1", RA: 11, Dec: 10},
		{Name: "renamed", RA: 12, Dec: 10 + 2.0/3600},
		{Number: "9", Name: "Metis", RA: 13, Dec: 10, V: 9.5},
	}
	r := CompareBodies("skybot", local, other)
	if r.Matched != 3 || len(r.Missing) != 1 || r.Missing[0] != "(9) Metis" || len(r.MissingV) != 1 || len(r.Extra) != 0 {
		t.Errorf("%+v", r)
	}
	if r.MaxArc < 1.99 || r.MaxArc > 2.01 {
		t.Errorf("max %v", r.MaxArc)
	}
}
