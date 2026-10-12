package moonkeep

import (
	"fmt"
	"testing"
	"time"
)

type altBin struct {
	alt          float64
	subs, kept   int
	nights       int
	narrowband   bool
	moonAltitude float64
}

func altSamples(bins ...altBin) []Sample {
	var out []Sample
	start := time.Date(2026, 1, 1, 4, 0, 0, 0, time.UTC)
	for _, b := range bins {
		for i := range b.subs {
			night := i % b.nights
			out = append(out, Sample{Night: fmt.Sprintf("2026-01-%02d", night+1), Narrowband: b.narrowband, Kept: i < b.kept,
				Exposure: 300, Start: start.AddDate(0, 0, night), Altitude: b.alt, Moon: State{Altitude: b.moonAltitude - 1}})
		}
	}
	return out
}

func row(t *testing.T, m *AltModel, filter string, nb bool, bin int) AltRow {
	t.Helper()
	for _, r := range m.Rows {
		if r.Filter == filter && r.Narrowband == nb && r.Bin == bin {
			return r
		}
	}
	t.Fatalf("no row %q %v %d", filter, nb, bin)
	return AltRow{}
}

func lowSky() []Sample {
	s := altSamples(altBin{alt: 60, subs: 400, kept: 340, nights: 20}, altBin{alt: 22, subs: 200, kept: 110, nights: 20},
		altBin{alt: 17, subs: 200, kept: 0, nights: 20}, altBin{alt: 12, subs: 10, kept: 0, nights: 2},
		altBin{alt: 17, subs: 300, kept: 300, nights: 20, moonAltitude: 30})
	for i := range s {
		s[i].Filter = "L"
	}
	return s
}

func TestAltitudeRowsWeighAndSkipLowBins(t *testing.T) {
	t.Parallel()
	m := FitAltitude(lowSky())
	if r := row(t, m, "", false, 8); r.Worth != 1 || !r.Usable || r.Skip || r.Subs != 400 {
		t.Errorf("reference %+v", r)
	}
	if r := row(t, m, "", false, 4); !(r.Worth > 0.5 && r.Worth < 0.75) || r.Skip {
		t.Errorf("20-25 is worth about 0.65 and kept: %+v", r)
	}
	if r := row(t, m, "", false, 3); !r.Skip || r.High >= SkipBelow || r.Subs != 200 {
		t.Errorf("15-20 kept none of 200 with the Moon down and is skipped: %+v", r)
	}
	if r := row(t, m, "L", false, 3); !r.Skip || r.Filter != "L" {
		t.Errorf("the filter's own row skips too: %+v", r)
	}
	if r := row(t, m, "", false, 2); r.Usable || r.Skip {
		t.Errorf("10 subs are too few: %+v", r)
	}
	if m.Lookup("l", false, 17) == nil || !m.Lookup("L", false, 17).Skip || m.Lookup("L", false, 12) != nil || m.Lookup("L", false, -3) != nil {
		t.Error("lookup by filter, case-blind, nothing for thin or negative altitudes")
	}
	if r := m.Lookup("Red", false, 22); r == nil || r.Filter != "" {
		t.Errorf("a filter without rows takes its class row: %+v", r)
	}
}

func TestAltitudeRowsIgnoreTheMoon(t *testing.T) {
	t.Parallel()
	if r := row(t, FitAltitude(altSamples(high(true))), "", true, 8); r.Subs != 400 || r.Worth != 1 {
		t.Errorf("narrowband reference %+v", r)
	}
	m := FitAltitude(altSamples(high(false), altBin{alt: 17, subs: 300, kept: 0, nights: 20, moonAltitude: 30}))
	if r := row(t, m, "", false, 3); r.Subs != 0 || r.Usable {
		t.Errorf("Moon-up subs count: %+v", r)
	}
}

func TestAltitudeReplayLeavesOutEachNight(t *testing.T) {
	t.Parallel()
	rp := RunAltitude(lowSky())
	if rp.Nights != 20 || rp.Subs != 810 || rp.SkippedSubs != 200 || rp.KeptLost != 0 || rp.Weighted != 800 {
		t.Fatalf("%+v", rp)
	}
	if !(rp.WorthKept > rp.WorthLeft) || rp.SkippedHours != 16.7 {
		t.Errorf("kept subs carry more worth than the rest: %+v", rp)
	}
}

func TestAltTableRows(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	rows := AltTableRows(FitAltitude(lowSky()), at)
	classes, filters := 0, 0
	for _, r := range rows {
		if r.Filter == "" {
			classes++
		} else {
			filters++
		}
		if !r.ComputedAt.Equal(at) {
			t.Fatal("computed at")
		}
	}
	if classes != 2*(len(TargetAltEdges())-1) || filters != 4 {
		t.Errorf("%d class rows, %d filter rows", classes, filters)
	}
	if len(AltTableRows(nil, at)) != 0 {
		t.Error("nil model")
	}
}

func high(nb bool) altBin { return altBin{alt: 60, subs: 400, kept: 340, nights: 20, narrowband: nb} }

func TestSamplesCarryTheTargetAltitude(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 3, 1, 6, 0, 0, 0, time.UTC)
	s := Samples([]Input{{Object: "Polaris", Filter: "L", Exposure: 300, Start: at, Status: "added", RA: 37.95, Dec: 89.26, HasPos: true}}, 35.0, -110.0)
	if len(s) != 1 || s[0].Altitude < 30.7 || s[0].Altitude > 32.3 {
		t.Fatalf("Polaris stands at the latitude: %+v", s)
	}
}

func TestSubsWithoutAFilterCountOnceInTheClassRows(t *testing.T) {
	t.Parallel()
	s := altSamples(altBin{alt: 60, subs: 100, kept: 80, nights: 10})
	m := Fit(s)
	for _, r := range m.Rows {
		if r.Down && r.Filter == "" && !r.Narrowband && r.Subs != 100 {
			t.Errorf("Moon-down class row has %d subs", r.Subs)
		}
	}
	downRows := 0
	for _, r := range m.Rows {
		if r.Down && !r.Narrowband {
			downRows++
		}
	}
	if downRows != 1 {
		t.Errorf("%d broadband Moon-down rows, want the class row only", downRows)
	}
	if r := row(t, FitAltitude(s), "", false, 8); r.Subs != 100 {
		t.Errorf("altitude class row has %d subs", r.Subs)
	}
}
