//go:build binariesdev

package binaries

import (
	"encoding/csv"
	"encoding/json"
	"math"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/binaries/minima"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/ownimage"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/period"
)

// paperEphemeris is an ephemeris as the paper's analysis keeps it: epoch
// as an MJD on the HJD UTC scale, half widths in days.
type paperEphemeris struct {
	P      float64 `json:"P"`
	PErr   float64 `json:"P_err"`
	T0     float64 `json:"T0_mjd_hjd"`
	T0Err  float64 `json:"T0_err"`
	W1     float64 `json:"W1"`
	A1     float64 `json:"a1"`
	W2     float64 `json:"W2"`
	A2     float64 `json:"a2"`
	Phase2 float64 `json:"phase2"`
}

// utcOfHJD is the UTC instant whose HJD toward ra, dec is h.
func utcOfHJD(h, ra, dec float64) time.Time {
	t := julianTime(h)
	for range 4 {
		t = t.Add(time.Duration((h - lc.HJD(t, ra, dec)) * 86400e9))
	}
	return t
}

// TestOwnPhotometryTimings times every eclipse an own photometry CSV
// (HJD UTC) covers and prints the timing record as the pages would show
// it. BIN_OWN_CSV names the CSV, BIN_RA and BIN_DEC the star, and either
// BIN_REPORT a stored vet report or BIN_PAPER a paper ephemeris JSON.
// BIN_MAG and BIN_ERR name the magnitude and error columns.
func TestOwnPhotometryTimings(t *testing.T) {
	t.Parallel()
	path := os.Getenv("BIN_OWN_CSV")
	ra, err1 := strconv.ParseFloat(os.Getenv("BIN_RA"), 64)
	dec, err2 := strconv.ParseFloat(os.Getenv("BIN_DEC"), 64)
	if path == "" || err1 != nil || err2 != nil {
		t.Skip("set BIN_OWN_CSV, BIN_RA, BIN_DEC and BIN_REPORT or BIN_PAPER")
	}
	var e *period.Ephemeris
	switch {
	case os.Getenv("BIN_REPORT") != "":
		raw, err := os.ReadFile(os.Getenv("BIN_REPORT"))
		if err != nil {
			t.Fatal(err)
		}
		var r vetting.Report
		if err := json.Unmarshal(raw, &r); err != nil {
			t.Fatal(err)
		}
		e, _ = adopt(&r)
	case os.Getenv("BIN_PAPER") != "":
		raw, err := os.ReadFile(os.Getenv("BIN_PAPER"))
		if err != nil {
			t.Fatal(err)
		}
		var p paperEphemeris
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatal(err)
		}
		t0 := lc.BJD(utcOfHJD(p.T0+2400000.5, ra, dec), ra, dec)
		e = &period.Ephemeris{Period: p.P, PeriodErr: p.PErr, T0: t0, T0Err: p.T0Err, Kind: period.KindEclipsing, Eclipses: []period.Eclipse{
			{Phase: 0, Width: p.W1 / p.P, Shape: p.A1, Depth: map[string]float64{keyZTFr: 1}},
			{Phase: p.Phase2, Width: p.W2 / p.P, Shape: p.A2, Depth: map[string]float64{keyZTFr: 0.5}}}}
	default:
		t.Skip("set BIN_REPORT or BIN_PAPER")
	}
	magCol, errCol := os.Getenv("BIN_MAG"), os.Getenv("BIN_ERR")
	if magCol == "" {
		magCol, errCol = "dmag", "dmag_err"
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	col := map[string]int{}
	for i, h := range rows[0] {
		col[h] = i
	}
	var pts []ownimage.Point
	for i, r := range rows[1:] {
		h, _ := strconv.ParseFloat(r[col["HJD"]], 64)
		m, err1 := strconv.ParseFloat(r[col[magCol]], 64)
		er, err2 := strconv.ParseFloat(r[col[errCol]], 64)
		if err1 != nil || err2 != nil {
			continue
		}
		u := utcOfHJD(h, ra, dec)
		pts = append(pts, ownimage.Point{FrameID: i, BJD: lc.BJD(u, ra, dec), Filter: r[col["filter"]], DMag: m, Err: er})
	}
	slices.SortFunc(pts, func(a, b ownimage.Point) int { return cmpFloat(a.BJD, b.BJD) })
	st := Star{Source: app.DiscoverySource{ID: 1}, Facts: &Facts{Ephemeris: e, Gaia: &GaiaFacts{RA: ra, Dec: dec}, Deeper: 0}}
	loc, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Skip("no time zone data")
	}
	type key struct {
		dip   int
		cycle float64
	}
	groups := map[key][]ownimage.Point{}
	for _, p := range pts {
		for k, d := range e.Eclipses {
			half := dipHalf(e, k)
			n := math.Round((p.BJD-e.T0)/e.Period - d.Phase)
			tm := e.T0 + (n+d.Phase)*e.Period
			if math.Abs(p.BJD-tm) <= reachShare*half {
				groups[key{k, n}] = append(groups[key{k, n}], p)
			}
		}
	}
	keys := make([]key, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b key) int { return cmpFloat(a.cycle+e.Eclipses[a.dip].Phase, b.cycle+e.Eclipses[b.dip].Phase) })
	for _, k := range keys {
		d := e.Eclipses[k.dip]
		tm := e.T0 + (k.cycle+d.Phase)*e.Period
		half := dipHalf(e, k.dip)
		utc := lc.UTCOfBJD(tm, ra, dec)
		row := app.EclipseTiming{ID: len(st.Timings) + 1, SourceID: 1, Kind: kindOf(k.dip, 0), Predicted: utc, Night: nightOf(utc, loc), Frames: len(groups[k]), State: app.TimingNone, Method: minima.MethodModel}
		m, filter, why, ok := bestMinimum(&ownimage.Curve{Points: groups[k]}, minima.Dip{Mid: tm, Half: half, Shape: dipShape(e, k.dip)})
		if ok {
			row.BJD, row.Err, row.Filter, row.Points, row.Depth, row.State = m.T, m.Err, filter, m.Points, m.Depth, app.TimingGood
			if m.Check != nil {
				row.CheckBJD, row.CheckErr = m.Check.T, m.Check.Err
			}
			if !m.Agree {
				row.State = app.TimingDoubtful
			}
			t.Logf("%s %s cycle %.0f: %d points in %s, middle %.5f ± %.2f min (formal %.2f, bead %.2f, red %.2f), %.1f min against the ephemeris, check %v",
				row.Night, row.Kind, k.cycle, m.Points, filter, m.T, m.Err*1440, m.Formal*1440, m.Bead*1440, m.Red, (m.T-tm)*1440, m.Check)
		} else {
			row.Note = "Not timed: " + why + "."
			t.Logf("%s %s cycle %.0f: %d points, not timed: %s", row.Night, row.Kind, k.cycle, len(groups[k]), why)
		}
		st.Timings = append(st.Timings, row)
	}
	out, err := json.MarshalIndent(st.Record(loc), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if p := os.Getenv("BIN_OUT"); p != "" {
		if err := os.WriteFile(p, out, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func cmpFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
