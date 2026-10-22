package eclfacts

import (
	"math"
	"strconv"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/period"
)

// starSeries is the star's own ZTF and TESS points in a period testdata
// case: one ZTF series per band and object id, one TESS series per sector.
func starSeries(t *testing.T, path string) []lc.Series {
	t.Helper()
	rows := readZst(t, path)
	by := map[string]*lc.Series{}
	var order []string
	for _, r := range rows[1:] {
		if r[0] != "t" {
			continue
		}
		k := r[1] + r[2] + r[6]
		if by[k] == nil {
			by[k] = &lc.Series{Survey: r[1], Band: r[2], Dilution: 1, Relative: r[1] == lc.TESS}
			order = append(order, k)
		}
		b, _ := strconv.ParseFloat(r[3], 64)
		m, _ := strconv.ParseFloat(r[4], 64)
		er, _ := strconv.ParseFloat(r[5], 64)
		src, _ := strconv.ParseInt(r[6], 10, 64)
		by[k].Points = append(by[k].Points, lc.Point{BJD: b + 2450000, Mag: m, Err: er, Src: src})
	}
	out := make([]lc.Series, 0, len(order))
	for _, k := range order {
		out = append(out, *by[k])
	}
	return out
}

// TestSecondPhaseMatchesTESSTimings needs the second dip's measured phase
// within two errors of where TESS times it, eclipse by eclipse, from
// TESScut cutouts of every sector (2026-10-22), on the ephemerides the vet
// gave the stars: the vet's phase moved by the second eclipses' mean
// O-C, as a prediction from the vet's epoch needs it. Before, survey points of all kinds scored phase by phase
// on points dropped phase by phase put 15362's second dip 27 minutes
// early of the vet's phase at 1.7 minutes error, where TESS has it 13
// minutes late.
func TestSecondPhaseMatchesTESSTimings(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		path          string
		p, t0         float64
		dips          [2]period.Eclipse
		tess, tessErr float64
		maxErrMinutes float64
	}{
		{"15362", "../period/testdata/confirmed/15362.csv.zst", 52.38972384039701, 2458311.6927924524,
			[2]period.Eclipse{{Phase: 0, Width: 0.003139777785061313, Shape: 2.1909783212715106}, {Phase: 0.39607621907240603, Width: 0.003679951939718748, Shape: 0.4495859758510877}},
			0.396248, 0.000064, 30},
		{"1592", "../period/testdata/confirmed/1592.csv.zst", 49.64110549826058, 2458324.3930310966,
			[2]period.Eclipse{{Phase: 0, Width: 0.0024530244432351377, Shape: 2.7694060810599086}, {Phase: 0.29220355419075744, Width: 0.004753109930523441, Shape: 1.4442337038175566}},
			0.291795, 0.000097, 60},
		{"33136", "../period/testdata/audit/c33136.csv.zst", 44.55472781535003, 2458318.380911188,
			[2]period.Eclipse{{Phase: 0, Width: 0.004497691056512113, Shape: 0.5180592797508035}, {Phase: 0.22145839230790842, Width: 0.005950645350309733, Shape: 0.8362533421954098}},
			0.221452, 0.000145, 90},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			e := &period.Ephemeris{Period: c.p, T0: c.t0, Kind: period.KindEclipsing, Eclipses: c.dips[:]}
			tw := TwinOf(starSeries(t, c.path), e)
			if tw == nil || !(tw.PhaseErr > 0) {
				t.Fatalf("no second dip phase with an error: %+v", tw)
			}
			mins := c.p * 1440
			off := (tw.Phase2 - c.tess) * mins
			sig := math.Hypot(tw.PhaseErr, c.tessErr) * mins
			if math.Abs(off) > 2*sig {
				t.Errorf("second dip %.1f min from TESS's, error %.1f min", off, sig)
			}
			if tw.PhaseErr*mins > c.maxErrMinutes {
				t.Errorf("second dip timed only to %.1f min, want under %.0f", tw.PhaseErr*mins, c.maxErrMinutes)
			}
		})
	}
}
