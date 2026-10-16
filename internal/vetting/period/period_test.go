package period

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
	"github.com/klauspost/compress/zstd"
)

const andDir = "3_And"

type star struct {
	dir      string
	ra, dec  float64
	dilution float64
}

func readCSV(t *testing.T, path string) []map[string]string {
	t.Helper()
	raw, err := os.ReadFile(path)
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
	out := make([]map[string]string, 0, len(rows))
	for _, r := range rows[1:] {
		m := map[string]string{}
		for i, h := range rows[0] {
			m[h] = r[i]
		}
		out = append(out, m)
	}
	return out
}

func num(t *testing.T, s string) float64 {
	t.Helper()
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return math.NaN()
	}
	return v
}

func loadZTF(t *testing.T, s star) []lc.Series {
	t.Helper()
	by := map[string]*lc.Series{}
	var order []string
	for _, r := range readCSV(t, filepath.Join("testdata", s.dir, "ztf.csv.zst")) {
		oid := r["oid"]
		se, ok := by[oid]
		if !ok {
			se = &lc.Series{Survey: lc.ZTF, Band: strings.TrimPrefix(r["filtercode"], "z"), Dilution: 1}
			by[oid] = se
			order = append(order, oid)
		}
		mid := num(t, r["mjd"]) + num(t, r["exptime"])/2/86400
		se.Points = append(se.Points, lc.Point{BJD: lc.BJD(lc.MJDTime(mid), s.ra, s.dec), Mag: num(t, r["mag"]), Err: num(t, r["magerr"])})
	}
	out := make([]lc.Series, 0, len(order))
	for _, o := range order {
		out = append(out, *by[o])
	}
	return out
}

func ztfMedian(series []lc.Series, band string) float64 {
	var v []float64
	for _, s := range series {
		if s.Band != band {
			continue
		}
		for _, p := range s.Points {
			v = append(v, p.Mag)
		}
	}
	return median(v)
}

func loadATLAS(t *testing.T, s star, ztf []lc.Series) []lc.Series {
	t.Helper()
	ref := map[string]float64{
		"o": ztfMedian(ztf, "r"),
		"c": (ztfMedian(ztf, "g") + ztfMedian(ztf, "r")) / 2,
	}
	by := map[string]*lc.Series{}
	var order []string
	for _, r := range readCSV(t, filepath.Join("testdata", s.dir, "atlas.csv.zst")) {
		f := r["f"]
		total := math.Pow(10, -0.4*(ref[f]-23.9)) / s.dilution
		flux := total + num(t, r["ujy"])
		if flux <= 0.05*total {
			continue
		}
		key := r["cam"] + " " + f
		se, ok := by[key]
		if !ok {
			se = &lc.Series{Survey: lc.ATLAS, Band: f, Relative: true, Dilution: s.dilution, Note: "camera " + r["cam"]}
			by[key] = se
			order = append(order, key)
		}
		se.Points = append(se.Points, lc.Point{
			BJD: lc.BJD(lc.MJDTime(num(t, r["mjd"])+15.0/86400), s.ra, s.dec),
			Mag: -2.5 * math.Log10(flux/total), Err: 1.0857 * num(t, r["dujy"]) / flux,
		})
	}
	slices.Sort(order)
	out := make([]lc.Series, 0, len(order))
	for _, k := range order {
		out = append(out, *by[k])
	}
	return out
}

func loadTESS(t *testing.T, s star) []lc.Series {
	t.Helper()
	by := map[string]*lc.Series{}
	var order []string
	for _, r := range readCSV(t, filepath.Join("testdata", s.dir, "tess.csv.zst")) {
		f := num(t, r["flux"])
		if !(f > 0) {
			continue
		}
		sec := r["sector"]
		se, ok := by[sec]
		if !ok {
			se = &lc.Series{Survey: lc.TESS, Band: "T", Note: "Sector " + sec, Relative: true, Dilution: 1, Deblended: true}
			by[sec] = se
			order = append(order, sec)
		}
		se.Points = append(se.Points, lc.Point{BJD: num(t, r["btjd"]) + 2457000, Mag: -2.5 * math.Log10(f)})
	}
	out := make([]lc.Series, 0, len(order))
	for _, o := range order {
		out = append(out, *by[o])
	}
	return out
}

func loadNEOWISE(t *testing.T, s star) []lc.Series {
	t.Helper()
	w1 := lc.Series{Survey: lc.NEOWISE, Band: "W1", Dilution: s.dilution}
	w2 := lc.Series{Survey: lc.NEOWISE, Band: "W2", Dilution: s.dilution}
	for _, r := range readCSV(t, filepath.Join("testdata", s.dir, "neowise.csv.zst")) {
		b := lc.BJD(lc.MJDTime(num(t, r["mjd"])), s.ra, s.dec)
		if m, e := num(t, r["w1"]), num(t, r["w1err"]); !math.IsNaN(m) && e > 0 {
			w1.Points = append(w1.Points, lc.Point{BJD: b, Mag: m, Err: e})
		}
		if m, e := num(t, r["w2"]), num(t, r["w2err"]); !math.IsNaN(m) && e > 0 {
			w2.Points = append(w2.Points, lc.Point{BJD: b, Mag: m, Err: e})
		}
	}
	return []lc.Series{w1, w2}
}

func loadAll(t *testing.T, s star) []lc.Series {
	t.Helper()
	z := loadZTF(t, s)
	out := append([]lc.Series{}, z...)
	out = append(out, loadATLAS(t, s, z)...)
	out = append(out, loadTESS(t, s)...)
	return append(out, loadNEOWISE(t, s)...)
}

func rows(r *Result) map[string]check.Check {
	out := map[string]check.Check{}
	for _, c := range r.Checks {
		out[c.ID] = c
	}
	return out
}

func logResult(t *testing.T, r *Result, took time.Duration) {
	t.Helper()
	if e := r.Ephemeris; e != nil {
		t.Logf("kind %s P %.6f +- %.6f T0 %.5f +- %.5f in %v", e.Kind, e.Period, e.PeriodErr, e.T0, e.T0Err, took)
		for i, ec := range e.Eclipses {
			t.Logf("dip %d phase %.4f width %.4f shape %.2f depth %v", i+1, ec.Phase, ec.Width, ec.Shape, ec.Depth)
		}
	}
	for _, f := range r.Fits {
		t.Logf("fit %+v", f)
	}
	for _, c := range r.Checks {
		t.Logf("%-8s %-4s %s", c.ID, c.Status, c.Evidence)
	}
}

type want struct {
	period, periodErr float64
	t0                float64
	phase2            float64
	depthR            [2]float64
	half, double      check.Status
}

func checkPeriod(t *testing.T, e *Ephemeris, w want) {
	t.Helper()
	tol := math.Max(3*math.Hypot(e.PeriodErr, w.periodErr), 1e-4*w.period)
	if math.Abs(e.Period-w.period) > tol {
		t.Errorf("period %.6f, want %.6f +- %.6f", e.Period, w.period, tol)
	}
	n := math.Round((e.T0 - w.t0) / w.period)
	ref := w.t0 + n*w.period
	if d := math.Abs(e.T0 - ref); d > 3*math.Hypot(e.T0Err, math.Abs(n)*w.periodErr)+0.003 {
		t.Errorf("T0 %.5f is %.4f d from the reference %.5f", e.T0, d, ref)
	}
	if len(e.Eclipses) != 2 {
		t.Fatalf("got %d dips, want 2", len(e.Eclipses))
	}
	if d := math.Abs(e.Eclipses[1].Phase - w.phase2); d > 0.003 {
		t.Errorf("second dip at phase %.4f, want %.4f", e.Eclipses[1].Phase, w.phase2)
	}
	for k := range 2 {
		if got := e.Eclipses[k].Depth["ZTF r"]; math.Abs(got-w.depthR[k]) > 0.04 {
			t.Errorf("dip %d depth in r %.3f, want %.3f", k+1, got, w.depthR[k])
		}
	}
}

func hjdToBJD(mjdHJD float64) float64 { return mjdHJD + 2400000.5 + 69.184/86400 }

func TestAnalyzeStars(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		star star
		ztf  bool
		want want
	}{
		{"And", star{andDir, 10.889321, 41.662433, 1}, false, want{9.987150067, 6.13e-6, hjdToBJD(59063.30971988), 0.5002, [2]float64{0.531, 0.322}, check.Pass, check.Pass}},
		{"IC1396", star{"1_IC1396", 323.277032, 58.634305, 0.42}, false, want{19.119383164, 1.30e-5, hjdToBJD(58861.18372725), 0.8106, [2]float64{0.6145, 0.3538}, check.Pass, check.Pass}},
		{"NAN all surveys", star{"2_NAN", 316.161804, 43.320665, 1}, false, want{34.406453284, 1.08e-4, hjdToBJD(59082.72313083), 0.5099, [2]float64{0.2174, 0.1880}, check.Pass, check.Pass}},
		{"NAN ZTF only", star{"2_NAN", 316.161804, 43.320665, 1}, true, want{34.406453284, 1.08e-4, hjdToBJD(59082.72313083), 0.5099, [2]float64{0.2174, 0.1880}, check.Pass, check.NotRun}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if underRace && !c.ztf {
				t.Skip("every survey together is too slow under the race detector; the ZTF case covers the same code")
			}
			var in []lc.Series
			if c.ztf {
				in = loadZTF(t, c.star)
			} else {
				in = loadAll(t, c.star)
			}
			start := time.Now()
			r, err := Analyze(in, Options{})
			took := time.Since(start)
			if err != nil {
				t.Fatal(err)
			}
			logResult(t, r, took)
			if r.Ephemeris.Kind != KindEclipsing {
				t.Fatalf("kind %s, want eclipsing", r.Ephemeris.Kind)
			}
			checkPeriod(t, r.Ephemeris, c.want)
			e := r.Ephemeris
			if v := e.Predict(e.T0+3*e.Period, "ZTF r"); math.Abs(v-e.Eclipses[0].Depth["ZTF r"]) > 1e-9 {
				t.Errorf("predict at the deep minimum %.3f, want its depth", v)
			}
			if v := e.Predict(e.T0+(e.Eclipses[1].Phase-2)*e.Period, "ZTF r"); math.Abs(v-e.Eclipses[1].Depth["ZTF r"]) > 1e-9 {
				t.Errorf("predict at the shallow minimum %.3f, want its depth", v)
			}
			if v := e.Predict(e.T0+0.3*e.Period, "ZTF r"); v != 0 {
				t.Errorf("predict between dips %.3f, want 0", v)
			}
			if ph := e.Phase(e.T0 + 0.8*e.Period); math.Abs(ph+0.2) > 1e-9 {
				t.Errorf("phase %.3f, want -0.2", ph)
			}
			rs := rows(r)
			if got := rs[check.Half].Status; got != c.want.half {
				t.Errorf("half: %s, want %s", got, c.want.half)
			}
			if got := rs[check.Double].Status; got != c.want.double {
				t.Errorf("double: %s, want %s", got, c.want.double)
			}
			if got := rs[check.ZTF].Status; got != check.Pass {
				t.Errorf("ZTF row: %s", got)
			}
		})
	}
}

func TestAndromedaHalfPeriod(t *testing.T) {
	t.Parallel()
	r, err := Analyze(loadZTF(t, star{andDir, 10.889321, 41.662433, 1}), Options{})
	if err != nil {
		t.Fatal(err)
	}
	ev := rows(r)[check.Half].Evidence
	if !strings.Contains(ev, "different depths") || !strings.Contains(ev, "4.99") {
		t.Errorf("half evidence %q", ev)
	}
}

func TestOwnTiming(t *testing.T) {
	t.Parallel()
	if underRace {
		t.Skip("two full fits are too slow under the race detector")
	}
	s := star{andDir, 10.889321, 41.662433, 1}
	r, err := Analyze(loadZTF(t, s), Options{})
	if err != nil {
		t.Fatal(err)
	}
	all, err := Analyze(loadAll(t, s), Options{})
	if err != nil {
		t.Fatal(err)
	}
	var pts []lc.Point
	for _, row := range readCSV(t, filepath.Join("testdata", andDir, "own.csv.zst")) {
		if row["night"] != "2026-10-11" {
			continue
		}
		pts = append(pts, lc.Point{BJD: num(t, row["hjd"]) + 69.184/86400, Mag: num(t, row["dmag"]), Err: num(t, row["err"])})
	}
	tm, ok := TimeMinimum(r.Ephemeris, pts, "Luminance")
	if !ok {
		t.Fatal("no minimum covered")
	}
	t.Logf("O-C %.1f +- %.1f min, depth %.3f, dip %d, %d points", tm.Offset, tm.OffsetErr, tm.Depth, tm.Dip, tm.Points)
	if tm.Dip != 2 || tm.Points != 28 {
		t.Errorf("dip %d with %d points, want the second dip with 28", tm.Dip, tm.Points)
	}
	if math.Abs(tm.Offset+4.6) > 4 || tm.OffsetErr < 2 || tm.OffsetErr > 8 {
		t.Errorf("O-C %.1f +- %.1f min, want about -4.6 +- 4.2", tm.Offset, tm.OffsetErr)
	}
	if math.Abs(tm.Depth-0.31) > 0.04 {
		t.Errorf("depth %.3f, want about 0.31", tm.Depth)
	}
	ref := &Ephemeris{Period: 9.987150067027478, T0: hjdToBJD(59063.30971988075), Kind: KindEclipsing, Eclipses: []Eclipse{
		{Phase: 0, Width: 0.14072579680090022 / 9.987150067027478, Shape: 5, Depth: map[string]float64{"ZTF r": 0.5310204904175049}},
		{Phase: 0.500204454313903, Width: 0.1273366739329821 / 9.987150067027478, Shape: 3.4120691316525624, Depth: map[string]float64{"ZTF r": 0.3222765699258643}},
	}}
	rt, ok := TimeMinimum(ref, pts, "ZTF r")
	if !ok {
		t.Fatal("no minimum covered with the reference ephemeris")
	}
	t.Logf("against the reference ZTF ephemeris: O-C %.1f +- %.1f min, depth %.3f", rt.Offset, rt.OffsetErr, rt.Depth)
	if math.Abs(rt.Offset+4.6) > 0.5 || math.Abs(rt.OffsetErr-4.2) > 0.6 || math.Abs(rt.Depth-0.31) > 0.01 {
		t.Errorf("reference ephemeris: O-C %.1f +- %.1f min depth %.3f, want -4.6 +- 4.2 and 0.31", rt.Offset, rt.OffsetErr, rt.Depth)
	}
	at, ok := TimeMinimum(all.Ephemeris, pts, "Luminance")
	if !ok {
		t.Fatal("no minimum covered with the all-survey ephemeris")
	}
	t.Logf("against every survey: O-C %.1f +- %.1f min, depth %.3f", at.Offset, at.OffsetErr, at.Depth)
	if math.Abs(at.Offset+4.6) > 1.5 || at.OffsetErr < 3 || at.OffsetErr > 6 {
		t.Errorf("all-survey ephemeris: O-C %.1f +- %.1f min, want about -4.6 +- 4.2", at.Offset, at.OffsetErr)
	}
	if _, ok := TimeMinimum(r.Ephemeris, pts[20:], "Luminance"); ok {
		t.Error("egress alone should not time a minimum")
	}
}

func TestFlatStar(t *testing.T) {
	t.Parallel()
	in := loadZTF(t, star{"cyg_flat", 314.32090, 28.71857, 1})
	start := time.Now()
	r, err := Analyze(in, Options{ChangeMag: 0.5})
	took := time.Since(start)
	if !errors.Is(err, ErrNoPeriod) {
		t.Fatalf("err %v, want no period", err)
	}
	logResult(t, r, took)
	if r.Ephemeris == nil || r.Ephemeris.Kind != KindFlat {
		t.Fatalf("ephemeris %+v, want flat", r.Ephemeris)
	}
	rs := rows(r)
	z := rs[check.ZTF]
	if z.Status != check.Fail || !strings.Contains(z.Evidence, "would have shown") || !strings.HasPrefix(z.Evidence, "Flat: ") {
		t.Errorf("ZTF row %+v", z)
	}
	if p := rs[check.Period]; p.Status != check.NotRun || !strings.Contains(p.Evidence, "flat") {
		t.Errorf("period row %+v", p)
	}
}

func TestSinusoid(t *testing.T) {
	t.Parallel()
	const P, amp = 3.7311, 0.15
	var g, r lc.Series
	g = lc.Series{Survey: lc.ZTF, Band: "g", Dilution: 1}
	r = lc.Series{Survey: lc.ZTF, Band: "r", Dilution: 1}
	rng := lcg(12345)
	noise := rng.gauss
	for i := range 900 {
		tt := 2458300 + float64(i)*2.3 + 0.1*math.Sin(float64(i))
		ph := 2 * math.Pi * (tt - 2459000.25) / P
		g.Points = append(g.Points, lc.Point{BJD: tt, Mag: 15 + amp*math.Cos(ph) + 0.015*noise(), Err: 0.015})
		r.Points = append(r.Points, lc.Point{BJD: tt + 0.03, Mag: 14.5 + 0.8*amp*math.Cos(ph+2*math.Pi*0.03/P) + 0.015*noise(), Err: 0.015})
	}
	res, err := Analyze([]lc.Series{g, r}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	logResult(t, res, 0)
	e := res.Ephemeris
	if e.Kind != KindSinusoidal || math.Abs(e.Period-P) > 1e-3 {
		t.Fatalf("got %s at %.5f, want sinusoidal at %.4f", e.Kind, e.Period, P)
	}
	if a := e.Amplitude["ZTF g"]; math.Abs(a-2*amp) > 0.02 {
		t.Errorf("amplitude in g %.3f, want %.2f", a, 2*amp)
	}
	if v := e.Predict(2459000.25, "ZTF g"); math.Abs(v-amp) > 0.01 {
		t.Errorf("predict at maximum fade %.3f, want %.2f", v, amp)
	}
	if ph := e.Phase(2459000.25 + 10*P); math.Abs(ph) > 0.01 {
		t.Errorf("phase %.3f, want 0", ph)
	}
	rs := rows(res)
	for _, id := range []string{check.Half, check.Double, check.Fold} {
		if rs[id].Status != check.Pass {
			t.Errorf("%s: %+v", id, rs[id])
		}
	}
}

type lcg uint64

func (l *lcg) next() float64 {
	*l = *l*6364136223846793005 + 1442695040888963407
	return float64(uint64(*l)>>11) / (1 << 53)
}

func (l *lcg) gauss() float64 {
	var s float64
	for range 12 {
		s += l.next()
	}
	return s - 6
}

func TestShortPeriodEclipses(t *testing.T) {
	t.Parallel()
	const P, t0 = 1.23457, 2459000.3
	rng := lcg(7)
	var g, r lc.Series
	g = lc.Series{Survey: lc.ZTF, Band: "g", Dilution: 1}
	r = lc.Series{Survey: lc.ZTF, Band: "r", Dilution: 1}
	model := func(tt, d1, d2 float64) float64 {
		x := (tt - t0) / P
		return d1*shapeOnly(wrapPhase(x), 0.05, 1.5) + d2*shapeOnly(wrapPhase(x-0.5), 0.05, 1.5)
	}
	for night := range 1400 {
		tt := 2458300 + float64(night)*1.7 + 0.3*rng.next()
		g.Points = append(g.Points, lc.Point{BJD: tt, Mag: 16 + model(tt, 0.45, 0.2) + 0.02*rng.gauss(), Err: 0.02})
		r.Points = append(r.Points, lc.Point{BJD: tt + 0.02, Mag: 15.6 + model(tt+0.02, 0.4, 0.22) + 0.02*rng.gauss(), Err: 0.02})
	}
	res, err := Analyze([]lc.Series{g, r}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	logResult(t, res, 0)
	e := res.Ephemeris
	if e.Kind != KindEclipsing || math.Abs(e.Period-P) > 3*e.PeriodErr+1e-5 {
		t.Fatalf("got %s at %.6f +- %.6f, want eclipsing at %.5f", e.Kind, e.Period, e.PeriodErr, P)
	}
	if len(e.Eclipses) != 2 || math.Abs(e.Eclipses[1].Phase-0.5) > 0.005 {
		t.Fatalf("dips %+v", e.Eclipses)
	}
	if d := e.Eclipses[0].Depth["ZTF g"]; math.Abs(d-0.45) > 0.05 {
		t.Errorf("deep dip in g %.3f, want 0.45", d)
	}
	if rs := rows(res); rs[check.Half].Status != check.Pass || rs[check.Double].Status != check.Pass {
		t.Errorf("half %+v double %+v", rs[check.Half], rs[check.Double])
	}
}

func TestNoSurveyData(t *testing.T) {
	t.Parallel()
	r, err := Analyze([]lc.Series{{Survey: lc.Own, Band: "L", Points: []lc.Point{{BJD: 2460000, Mag: 0.1, Err: 0.01}}}}, Options{})
	if !errors.Is(err, ErrNoPeriod) || r == nil || len(r.Checks) != 8 {
		t.Fatalf("got %+v, %v", r, err)
	}
	for _, c := range r.Checks {
		if c.Status != check.NotRun || c.Evidence == "" || c.Name == "" {
			t.Errorf("row %+v", c)
		}
	}
}

func TestCommas(t *testing.T) {
	t.Parallel()
	for in, want := range map[int]string{0: "0", 955: "955", 1261: "1,261", 1234567: "1,234,567", -4500: "-4,500"} {
		if got := commas(in); got != want {
			t.Errorf("commas(%d) = %s, want %s", in, got, want)
		}
	}
	if got := fmt.Sprint(fmtPeriod(9.98715007, 6e-6)); got != "9.98715" {
		t.Errorf("period text %s", got)
	}
}
