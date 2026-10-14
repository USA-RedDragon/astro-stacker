package discoveries

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBrokersAndTNS(t *testing.T) {
	t.Parallel()
	var tnsAgent, tnsKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/alerce"):
			fmt.Fprint(w, `{"items":[{"oid":"ZTF20aaaaaaa","meanra":10.0001,"meandec":41.0,"class":"SNIa"},{"oid":"ZTF20far","meanra":10.1,"meandec":41.0,"class":null}]}`)
		case r.URL.Path == "/fink":
			fmt.Fprint(w, `[{"i:objectId":"ZTF21bbb","i:ra":10.0,"i:dec":41.0002,"v:classification":"SN candidate"},{"i:objectId":"ZTF21bbb","i:ra":10.0,"i:dec":41.0002}]`)
		case r.URL.Path == "/tns":
			_ = r.ParseForm()
			tnsAgent, tnsKey = r.Header.Get("User-Agent"), r.PostForm.Get("api_key")
			fmt.Fprint(w, `{"data":{"reply":[{"prefix":"SN","objname":"2026abc"}]}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := &Catalogs{AlerceURL: srv.URL + "/alerce/", FinkURL: srv.URL + "/fink", TNSURL: srv.URL + "/tns"}
	as, checked, failed := c.AlertsFrom()(context.Background(), 10, 41, 3)
	if len(as) != 2 || len(checked) != 2 || len(failed) != 0 || as[0].Class != "SNIa" || as[1].ID != "ZTF21bbb" {
		t.Errorf("alerts %+v checked %v failed %v", as, checked, failed)
	}
	c.TNSKey, c.TNSBotID, c.TNSBotName = "k", "42", "bot"
	as, checked, _ = c.AlertsFrom()(context.Background(), 10, 41, 3)
	if len(as) != 3 || as[2].ID != "SN 2026abc" || len(checked) != 3 || tnsKey != "k" || !strings.Contains(tnsAgent, `"tns_id":"42"`) {
		t.Errorf("tns %+v %v %q %q", as, checked, tnsKey, tnsAgent)
	}
	c.FinkURL = srv.URL + "/missing"
	lookup := c.AlertsFrom()
	_, _, failed = lookup(context.Background(), 10, 41, 3)
	if len(failed) != 1 || failed[0] != sourceFink {
		t.Errorf("failed %v", failed)
	}
	c.FinkURL = srv.URL + "/fink"
	_, _, failed = lookup(context.Background(), 10, 41, 3)
	if len(failed) != 1 {
		t.Errorf("a service that failed was asked again in the same run: failed %v", failed)
	}
}

func TestVizierParsing(t *testing.T) {
	t.Parallel()
	b := []byte("#\n#Title\nName\tType\tmax\tmin\tn_max\tPeriod\tRAJ2000\tDEJ2000\n \t \t \t \t \t \t \t \n----\t----\t---\t---\t---\t---\t---\t---\nII Com\tUGSU\t 15.0\t 17.5\tV\t\t189.30894\t+14.44218\nbad\t\t\t\t\t\t\t\n")
	vs, err := ParseVSX(b)
	if err != nil || len(vs) != 1 || vs[0].Name != "II Com" || vs[0].Max != 15 || vs[0].Dec != 14.44218 {
		t.Errorf("%+v %v", vs, err)
	}
	if _, err := ParseVSX([]byte("# nothing\n")); err == nil {
		t.Error("no table should fail")
	}
}

func TestSIPRoundTrip(t *testing.T) {
	t.Parallel()
	kw := map[string]string{"CTYPE1": "RA---TAN-SIP", "CTYPE2": "DEC--TAN-SIP", "CRPIX1": "3124.5", "CRPIX2": "2088.5", "CRVAL1": "188.288060506723",
		"CRVAL2": "14.2555474767701", "CDELT1": "-0.000534219422728293", "CDELT2": "0.000534201311953114", "PC1_1": "-0.999992078092394", "PC1_2": "0.003980421140354",
		"PC2_1": "-0.00405269528571044", "PC2_2": "-0.99999178779674", "A_ORDER": "3", "A_2_0": "-3.56460238489372E-08", "A_1_1": "-3.04768845506931E-08",
		"A_0_2": "1.26023966573482E-08", "A_3_0": "-8.37662264906807E-11", "A_2_1": "3.77128605811934E-12", "A_1_2": "-8.24368277984232E-11", "A_0_3": "-4.39512746156984E-12",
		"B_ORDER": "3", "B_2_0": "3.01150963676541E-09", "B_1_1": "-3.11730422861609E-08", "B_0_2": "-3.33662506083976E-08", "B_3_0": "-1.94226661341187E-13",
		"B_2_1": "-7.63002707170175E-11", "B_1_2": "4.49576089760209E-12", "B_0_3": "-9.19361220037948E-11", "AP_ORDER": "1", "AP_0_0": "0", "BP_ORDER": "1"}
	w, err := ParseWCS(kw, 4176)
	if err != nil {
		t.Fatal(err)
	}
	w.Fix = [6]float64{0.3, 1e-5, -2e-5, -0.2, 3e-5, 1e-5}
	for _, p := range [][2]float64{{0, 0}, {6247, 4175}, {3124, 2088}, {100, 4000}} {
		ra, dec := w.SkyOf(p[0], p[1])
		x, y, ok := w.PixelOf(ra, dec)
		if !ok || math.Hypot(x-p[0], y-p[1]) > 1e-4 {
			t.Errorf("%v -> %.6f %.6f -> %.6f %.6f", p, ra, dec, x, y)
		}
	}
	if s := w.PixelScale(); math.Abs(s-1.923) > 0.001 {
		t.Errorf("scale %.4f", s)
	}
	s := w.Shifted(1000, 2000, StampSize)
	ra, dec := w.SkyOf(1010, 2020)
	ra2, dec2 := s.SkyOf(10, 20)
	if Separation(ra, dec, ra2, dec2)*3600 > 0.05 {
		t.Errorf("stamp plate solution off by %.3f\"", Separation(ra, dec, ra2, dec2)*3600)
	}
}

func TestStampsRender(t *testing.T) {
	t.Parallel()
	p := NewPlane(100, 100)
	for i := range p.Data {
		p.Data[i] = 0.01 * float32(i%7)
	}
	addGauss(p.Data, 100, 100, 50, 50, 5, 1.5)
	st := Cut(p, p, p, 50, 50)
	n, r, d, err := st.PNGs()
	if err != nil || len(n) == 0 || len(r) == 0 || len(d) == 0 {
		t.Fatal(err)
	}
	w, err := ParseWCS(synthWCS(), 100)
	if err != nil {
		t.Fatal(err)
	}
	f, err := st.FITS(w, nil)
	if err != nil || len(f)%2880 != 0 {
		t.Fatal(err)
	}
	edge := Cut(p, p, p, 2, 2)
	if !isNaN32(edge.Night[0]) {
		t.Error("pixels off the image should be NaN")
	}
	b := Body{RA: 359.999, Dec: 10, DRA: 3600, DDec: -3600}
	ra, dec := b.At(time.Hour)
	if math.Abs(dec-9) > 1e-9 || ra > 1.1 {
		t.Errorf("body at %.4f %.4f", ra, dec)
	}
}
