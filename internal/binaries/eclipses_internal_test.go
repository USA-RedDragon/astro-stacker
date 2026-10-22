package binaries

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/sky"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/eclfacts"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/period"
)

const testRA, testDec = 312.5, 31.8

func chicago(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Skip("no time zone data")
	}
	return loc
}

func TestPredictAcrossDaylightTimeEnd(t *testing.T) {
	t.Parallel()
	loc := chicago(t)
	first := time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC)
	e := &period.Ephemeris{Period: 0.5, PeriodErr: 1e-5, T0: lc.BJD(first, testRA, testDec), T0Err: 0.001, Kind: period.KindEclipsing,
		Eclipses: []period.Eclipse{{Phase: 0, Width: 0.05, Shape: 1, Depth: map[string]float64{keyZTFr: 0.3}}}}
	ps := Predict(e, 0, testRA, testDec, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC))
	if len(ps) != 2 {
		t.Fatalf("got %d eclipses, want 2", len(ps))
	}
	if d := ps[0].Mid.Sub(first); d < -time.Second || d > time.Second {
		t.Errorf("first mid %s, want %s", ps[0].Mid, first)
	}
	want := [][3]string{{"Sun Nov 1", "1:30 AM", "CDT"}, {"Sun Nov 1", "12:30 PM", "CST"}}
	for i, p := range ps {
		day, clock, zone := Labels(p.Mid, loc)
		if [3]string{day, clock, zone} != want[i] {
			t.Errorf("eclipse %d: %s %s %s, want %v", i, day, clock, zone, want[i])
		}
	}
	errMin := math.Hypot(0.001, 1*1e-5) * 1440
	if got := ps[1].Err.Minutes(); math.Abs(got-errMin) > 0.01 {
		t.Errorf("error after one cycle %.3f min, want %.3f", got, errMin)
	}
	if got := ps[0].Duration; math.Abs(got.Hours()-eclfacts.Duration(e, 0)*24) > 1e-6 {
		t.Errorf("duration %s", got)
	}
	start, end := ps[0].Window()
	if !start.Before(ps[0].Mid.Add(-ps[0].Duration)) || !end.After(ps[0].Mid.Add(ps[0].Duration)) {
		t.Errorf("window %s to %s does not cover the eclipse and its edges", start, end)
	}
}

func TestPredictErrorGrowsWithCycles(t *testing.T) {
	t.Parallel()
	e := &period.Ephemeris{Period: 2, PeriodErr: 1e-4, T0: 2459000.5, T0Err: 0.001, Kind: period.KindEclipsing,
		Eclipses: []period.Eclipse{{Phase: 0, Width: 0.05, Shape: 1}, {Phase: 0.5, Width: 0.05, Shape: 1}}}
	from := lc.UTCOfBJD(2459000.5+1000*2-0.1, testRA, testDec)
	ps := Predict(e, 0, testRA, testDec, from, from.Add(48*time.Hour))
	if len(ps) != 2 || ps[0].Kind != "primary" || ps[1].Kind != "secondary" {
		t.Fatalf("got %+v", ps)
	}
	want := math.Hypot(0.001, 1000*1e-4) * 1440
	if got := ps[0].Err.Minutes(); math.Abs(got-want) > 0.05 {
		t.Errorf("error after 1000 cycles %.2f min, want %.2f", got, want)
	}
}

func TestZoneNote(t *testing.T) {
	t.Parallel()
	loc := chicago(t)
	from := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	if got := ZoneNote(loc, from, from.AddDate(0, 0, 30)); got != "Times are US Central (CDT until Nov 1, then CST)." {
		t.Errorf("note %q", got)
	}
	if got := ZoneNote(loc, from.AddDate(0, 1, 0), from.AddDate(0, 1, 20)); got != "Times are US Central (CST)." {
		t.Errorf("note %q", got)
	}
}

func TestSeenNeedsDarkAndHigh(t *testing.T) {
	t.Parallel()
	site := sky.Site{Latitude: 10, Longitude: 20}
	midnight := time.Date(2026, 3, 20, 22, 40, 0, 0, time.UTC)
	ra := sky.LST(midnight, site.Longitude)
	if ok, note := Seen(site, ra, site.Latitude, midnight); !ok || !strings.HasPrefix(note, "Dark and 90° up") {
		t.Errorf("overhead at local midnight: %v %q", ok, note)
	}
	if ok, note := Seen(site, ra, site.Latitude-70, midnight); ok || note != "Only 20° up" {
		t.Errorf("low star: %v %q", ok, note)
	}
	noon := midnight.Add(-12 * time.Hour)
	if ok, note := Seen(site, ra, site.Latitude, noon); ok || note != "Sun up" {
		t.Errorf("at noon: %v %q", ok, note)
	}
}

func TestJ2000MovesByProperMotion(t *testing.T) {
	t.Parallel()
	g := GaiaFacts{RA: 100, Dec: 20, PMDec: 1000}
	_, dec := g.J2000()
	if want := 20 - 16.0/3600; math.Abs(dec-want) > 1e-9 {
		t.Errorf("dec %.8f, want %.8f", dec, want)
	}
	ra, _ := (GaiaFacts{RA: 100, Dec: 60, PMRA: 2000}).J2000()
	if want := 100 - 32.0/3600/math.Cos(60*math.Pi/180); math.Abs(ra-want) > 1e-6 {
		t.Errorf("ra %.8f, want %.8f", ra, want)
	}
}

func TestSexagesimal(t *testing.T) {
	t.Parallel()
	cases := []struct {
		ra, dec   float64
		raS, decS string
	}{
		{32.46212916, 57.01925, "02 09 50.91", "+57 01 09.3"},
		{359.9999999, -0.0000001, "00 00 00.00", "-00 00 00.0"},
		{15 + 59.9999/240, -(1 + 59.99/60/60), "01 01 00.00", "-01 01 00.0"},
	}
	for _, c := range cases {
		if got := SexRA(c.ra); got != c.raS {
			t.Errorf("RA %.7f: %s, want %s", c.ra, got, c.raS)
		}
		if got := SexDec(c.dec); got != c.decS {
			t.Errorf("Dec %.7f: %s, want %s", c.dec, got, c.decS)
		}
	}
}

func TestMagnitudesFromZTFOutsideEclipses(t *testing.T) {
	t.Parallel()
	e := twoDips(0.43, 0.27)
	st := Star{Report: &vetting.Report{Ephemeris: e}, Facts: &Facts{Ephemeris: e, Gaia: &GaiaFacts{RUWE: 1},
		Survey: &eclfacts.Summary{Normal: &eclfacts.Normal{Survey: "ZTF", Band: "r", Mag: 15.02, Points: 1800}}}}
	mx, mn := magnitudes(st, e)
	if mx.Value != "15.02" || mn.Value != "15.50" {
		t.Errorf("max %q min %q, want 15.02 and 15.50 (15.02 + 0.48)", mx.Value, mn.Value)
	}
	if !strings.Contains(mx.Hint, "Passband r") || !strings.Contains(mx.Hint, "Median of 1800 ZTF r points outside the eclipses") {
		t.Errorf("max hint %q", mx.Hint)
	}
	if !strings.Contains(mn.Hint, "deeper eclipse") {
		t.Errorf("min hint %q", mn.Hint)
	}
	none, _ := magnitudes(Star{Report: &vetting.Report{Ephemeris: e}}, e)
	if none.State != StateNotMeasured {
		t.Errorf("without facts: %+v", none)
	}
}

func TestHJDOfEpoch(t *testing.T) {
	t.Parallel()
	at := time.Date(2020, 6, 1, 3, 0, 0, 0, time.UTC)
	b := lc.BJD(at, testRA, testDec)
	back := lc.UTCOfBJD(b, testRA, testDec)
	if d := back.Sub(at); d < -time.Millisecond || d > time.Millisecond {
		t.Errorf("UTC from BJD is %s off", d)
	}
	hjd := lc.HJD(at, testRA, testDec)
	if d := (b - hjd) * 86400; d < 60 || d > 80 {
		t.Errorf("BJD TDB minus HJD UTC is %.1f s, want about 69 s of TDB-UTC plus a few seconds", d)
	}
}

func TestKitFieldsInFormOrder(t *testing.T) {
	t.Parallel()
	e := twoDips(0.43, 0.27)
	r := report(e, check.New(check.Half, check.Pass, "differ"), check.New(check.Double, check.Pass, "Ruled out"))
	r.Star = "Gaia DR3 123 · Gaia G 15.05"
	st := Star{Report: r, Facts: &Facts{Ephemeris: e, Gaia: &GaiaFacts{ID: 123, RA: 32.46, Dec: 57.02, G: 15.05, RUWE: 1}, Sectors: []int{58},
		Survey: &eclfacts.Summary{Coverage: goodCoverage(), Normal: &eclfacts.Normal{Survey: "ZTF", Band: "r", Mag: 15.02, Points: 900}}}}
	st.Source.ID, st.Source.Object = 7, "Made-up field"
	k := KitOf(st, "", map[string]FileFact{FilePhased: {State: FileReady, Key: "k", Bytes: 1000}}, func(_ int, n string) string { return n }, nil)
	keys := make([]string, 0, len(k.Fields))
	for _, f := range k.Fields {
		keys = append(keys, f.Key)
	}
	want := "position primary_name other_names type spectral_type max_mag min_mag period epoch duration discoverer reference_name reference_bibcode second_reference remarks"
	if strings.Join(keys, " ") != want {
		t.Errorf("fields %v", keys)
	}
	for _, f := range k.Fields {
		switch f.Key {
		case "discoverer":
			if f.State != StateNotSet || !f.Editable {
				t.Errorf("discoverer %+v", f)
			}
		case "reference_bibcode":
			if f.Value != ZTFBibcode {
				t.Errorf("bibcode %q", f.Value)
			}
		case "remarks":
			if !strings.Contains(f.Value, "Eccentric system. Min II = 15.29 r at phase 0.430.") || !strings.Contains(f.Value, "TESS sector 58") {
				t.Errorf("remarks %q", f.Value)
			}
		}
	}
	if k.Files[0].State != FileReady || k.Files[0].URL != "phased.png" || k.Files[1].State != FilePending {
		t.Errorf("files %+v", k.Files)
	}
	if len(k.Checklist) != len(checkItems()) || !k.Checklist[0].Auto {
		t.Errorf("checklist %+v", k.Checklist)
	}
}

func TestSubLength(t *testing.T) {
	t.Parallel()
	cases := []struct {
		g    float64
		d    time.Duration
		plan float64
		want float64
	}{
		{11, 6 * time.Hour, 300, 30},
		{15, 6 * time.Hour, 300, 120},
		{17, 6 * time.Hour, 300, 180},
		{17, 6 * time.Hour, 90, 90},
		{17, 30 * time.Minute, 300, 90},
		{17, 2 * time.Minute, 300, 10},
	}
	for _, c := range cases {
		if got := SubLength(c.g, c.d, c.plan); got != c.want {
			t.Errorf("G %.0f, %s eclipse, %gs plan: %gs, want %gs", c.g, c.d, c.plan, got, c.want)
		}
	}
}

// star27822 is find 27822 with the vet's ephemeris and the surveys'
// second dip phase error of the 2026-10-19 vet, 0.00025279 of a cycle.
func star27822(pe float64) (Star, *period.Ephemeris) {
	e := &period.Ephemeris{Period: 8.159505837678363, PeriodErr: 6.944427764970278e-06, T0: 2458271.8100552075, T0Err: 0.001313703929629083, Kind: period.KindEclipsing,
		Eclipses: []period.Eclipse{
			{Phase: 0, Width: 0.014020551109073798, Shape: 1.2162970470852297, Depth: map[string]float64{keyZTFr: 0.3784689277215908}},
			{Phase: 0.5090730285476748, Width: 0.01617862327522726, Shape: 1.5818934912252416, Depth: map[string]float64{"ATLAS c": 0.055335927516169336}},
		}}
	st := Star{Source: app.DiscoverySource{ID: 27822}, Facts: &Facts{Ephemeris: e, Gaia: &GaiaFacts{RA: 358.51204581824777, Dec: 62.1389340578219},
		Survey: &eclfacts.Summary{Twin: &eclfacts.Twin{Band: "ATLAS c", Phase2: 0.5093763777340852, PhaseErr: pe}}}}
	return st, e
}

func secondary27822(t *testing.T, st Star, e *period.Ephemeris) Predicted {
	t.Helper()
	return secondaryNear(t, st, e, time.Date(2026, 10, 22, 6, 35, 0, 0, time.UTC))
}

func secondaryNear(t *testing.T, st Star, e *period.Ephemeris, at time.Time) Predicted {
	t.Helper()
	for _, p := range Predict(e, 0, st.Facts.Gaia.RA, st.Facts.Gaia.Dec, at.Add(-time.Hour), at.Add(time.Hour)) {
		if p.Kind == KindSecondary {
			return st.withPhaseError(p, e)
		}
	}
	t.Fatalf("no second eclipse predicted near %s", at)
	return Predicted{}
}

func TestSecondEclipse27822TakesInItsPhaseError(t *testing.T) {
	t.Parallel()
	st, e := star27822(0.00025279098867542205)
	p := secondary27822(t, st, e)
	if want := time.Date(2026, 10, 22, 6, 35, 12, 0, time.UTC); absDur(p.Mid.Sub(want)) > 2*time.Second {
		t.Errorf("middle %s, want the live card's %s", p.Mid, want)
	}
	if m := p.Err.Minutes(); math.Abs(m-5.14) > 0.02 {
		t.Errorf("error %.2f min, want the live card's 5.14: 4.20 from the epoch and period, 2.97 from the phase", m)
	}
	st, _ = star27822(0.0011122803501718)
	if m := secondary27822(t, st, e).Err.Minutes(); math.Abs(m-13.75) > 0.05 {
		t.Errorf("error %.2f min with ZTF's phase error alone, want 13.75", m)
	}
}

// TestOwnSecondTimingMovesThePhase times tonight's second eclipse 38 min
// early to 12 min and predicts the next one, 8.16 days on: weighted with
// the surveys' place, 5.14 min from the ephemeris and the phase, the
// timing moves it by 38 x 5.14^2 / (5.14^2 + 12^2) minutes; tonight's own
// eclipse ignores its own timing.
func TestOwnSecondTimingMovesThePhase(t *testing.T) {
	t.Parallel()
	st, e := star27822(0.00025279098867542205)
	tonight := secondary27822(t, st, e)
	next := time.Date(2026, 10, 30, 10, 25, 0, 0, time.UTC)
	before := secondaryNear(t, st, e, next)
	ra, dec := st.Facts.Gaia.RA, st.Facts.Gaia.Dec
	bjd := lc.BJD(tonight.Mid, ra, dec) - 38.0/1440
	st.Timings = []app.EclipseTiming{
		{SourceID: 27822, Kind: KindSecondary, Predicted: tonight.Mid, BJD: bjd, Err: 12.0 / 1440, State: app.TimingGood},
		{SourceID: 27822, Kind: KindSecondary, Predicted: tonight.Mid, BJD: bjd + 30.0/1440, Err: 1.0 / 1440, State: app.TimingDoubtful},
	}
	after := secondaryNear(t, st, e, next)
	sur := before.Err.Minutes()
	own := math.Hypot(12, e.PeriodErr*1440)
	w1, w2 := 1/(sur*sur), 1/(own*own)
	if got, want := after.Mid.Sub(before.Mid).Minutes(), -38*w2/(w1+w2); math.Abs(got-want) > 0.05 || want > -5 {
		t.Errorf("own timing 38 min early moved the next middle %.2f min, want %.2f", got, want)
	}
	if got, want := after.Err.Minutes(), math.Sqrt(1/(w1+w2)); math.Abs(got-want) > 0.05 {
		t.Errorf("error %.2f min, want %.2f", got, want)
	}
	if same := secondary27822(t, st, e); !same.Mid.Equal(tonight.Mid) || same.Err != tonight.Err {
		t.Errorf("tonight's own timing moved tonight's prediction by %s", same.Mid.Sub(tonight.Mid))
	}
	bare, _ := star27822(0)
	bare.Timings = st.Timings
	if got := secondaryNear(t, bare, e, next).Mid.Sub(before.Mid).Minutes(); math.Abs(got+38) > 0.05 {
		t.Errorf("with no survey phase error the own timing alone moved the next middle %.2f min, want -38", got)
	}
}
