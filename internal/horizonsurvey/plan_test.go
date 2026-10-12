package horizonsurvey_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	hs "github.com/USA-RedDragon/astro-stacker/internal/horizonsurvey"
)

func testPlanConfig() hs.PlanConfig {
	return hs.PlanConfig{AzStep: 10, AltMin: 0, AltMax: 35, Overlap: 0.3, FieldW: 3.67, FieldH: 2.06}
}

func TestSafetyLimitsCannotBeLoosened(t *testing.T) {
	t.Parallel()
	for name, mutate := range map[string]func(*hs.Safety){
		"sun separation below 45": func(s *hs.Safety) { s.MinSunSep = 44.9 },
		"sun above horizon":       func(s *hs.Safety) { s.MaxSunAlt = 0.5 },
		"sun above -3":            func(s *hs.Safety) { s.MaxSunAlt = -2.9 },
		"negative minimum alt":    func(s *hs.Safety) { s.MinAlt = -1 },
		"huge path margin":        func(s *hs.Safety) { s.PathMargin = 10 },
		"short sun look-ahead":    func(s *hs.Safety) { s.Window = time.Minute },
	} {
		s := hs.DefaultSafety()
		mutate(&s)
		if err := s.Validate(); !errors.Is(err, hs.ErrBadConfig) {
			t.Fatalf("%s: got %v, want ErrBadConfig", name, err)
		}
	}
	if err := hs.DefaultSafety().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestEveryPlannedPositionKeepsAwayFromTheSun(t *testing.T) {
	t.Parallel()
	site := testSite()
	start := duskTime()
	s := hs.DefaultSafety()
	p, err := hs.MakePlan(site, testPlanConfig(), s, start, 15*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if p.SunErr != nil {
		t.Fatalf("dusk plan refused: %v", p.SunErr)
	}
	ok, skipped := 0, 0
	for _, pp := range p.Positions {
		if pp.Err != nil {
			skipped++
			continue
		}
		ok++
		colStart := start.Add(time.Duration(pp.Column) * 45 * time.Second)
		for d := time.Duration(0); d <= s.Window; d += time.Minute {
			if sep := site.SunSeparation(colStart.Add(d), pp.Az, pp.Alt); sep < 45 {
				t.Fatalf("planned az %.1f alt %.1f is %.1f deg from the Sun at +%s", pp.Az, pp.Alt, sep, d)
			}
		}
		if pp.Alt < s.MinAlt {
			t.Fatalf("planned altitude %v is below the limit", pp.Alt)
		}
	}
	if ok == 0 || skipped == 0 {
		t.Fatalf("expected some positions to pass and some near the Sun to be skipped, got %d and %d", ok, skipped)
	}
	sunAz, _ := site.SunAltAz(start)
	for _, pp := range p.Positions {
		if d := hs.Separation(hs.HorVec(pp.Az, 0), hs.HorVec(sunAz, 0)); d < 30 && pp.Err == nil {
			t.Fatalf("az %.1f is %.1f deg from the Sun's azimuth and was not skipped", pp.Az, d)
		}
	}
}

func TestPointAndPathChecks(t *testing.T) {
	t.Parallel()
	site := testSite()
	at := duskTime()
	ck := hs.NewChecker(site, hs.DefaultSafety(), at)
	sunAz, sunAlt := site.SunAltAz(at)
	if err := ck.PointOK(sunAz, sunAlt+44); !errors.Is(err, hs.ErrUnsafe) {
		t.Fatalf("a point 44 deg above the Sun passed: %v", err)
	}
	if err := ck.PointOK(sunAz+180, 10); err != nil {
		t.Fatalf("the anti-Sun point failed: %v", err)
	}
	if err := ck.PointOK(sunAz+180, -0.5); !errors.Is(err, hs.ErrUnsafe) {
		t.Fatalf("a point below the minimum altitude passed: %v", err)
	}
	ha1, dec1 := site.AltAzToHADec(wrapAz(sunAz-60), 5)
	from := ck.Safety.Candidates(ha1, dec1)[0]
	ha2, dec2 := site.AltAzToHADec(wrapAz(sunAz+60), 5)
	to := ck.Safety.Candidates(ha2, dec2)
	sweeps := false
	for _, m := range to {
		if err := ck.PathOK(from, m); errors.Is(err, hs.ErrUnsafe) {
			sweeps = true
		}
	}
	if !sweeps {
		t.Fatal("a low slew across the Sun's azimuth was not flagged")
	}
	if _, err := ck.Route(from, wrapAz(sunAz+60), 5); err == nil {
		t.Fatal("a route whose only paths pass the Sun was accepted")
	}
}

func wrapAz(a float64) float64 {
	for a < 0 {
		a += 360
	}
	for a >= 360 {
		a -= 360
	}
	return a
}

func TestSunTooHighRefuses(t *testing.T) {
	t.Parallel()
	site := testSite()
	day := time.Date(2026, 10, 12, 20, 0, 0, 0, time.UTC)
	ck := hs.NewChecker(site, hs.DefaultSafety(), day)
	if err := ck.SunOK(); !errors.Is(err, hs.ErrSunTooHigh) {
		t.Fatalf("daytime check gave %v", err)
	}
	sunset := time.Date(2026, 10, 12, 23, 55, 0, 0, time.UTC)
	if err := hs.NewChecker(site, hs.DefaultSafety(), sunset).SunOK(); !errors.Is(err, hs.ErrSunTooHigh) {
		t.Fatalf("Sun at +1 deg gave %v", err)
	}
	p, err := hs.MakePlan(site, testPlanConfig(), hs.DefaultSafety(), day, 15*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	p.Write(&b)
	if !strings.Contains(b.String(), "REFUSE") {
		t.Fatalf("daytime dry run does not say REFUSE:\n%s", b.String()[:200])
	}
}

func TestMechanicalModel(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ ha, dec float64 }{{-60, 20}, {45, -30}, {170, 50}, {-5, 80}} {
		for _, pier := range []string{hs.PierEast, hs.PierWest} {
			m, _ := hs.MechFor(c.ha, c.dec, pier)
			ha, dec := m.HADec()
			if !near(wrapAz(ha-c.ha+180), 180, 1e-9) || !near(dec, c.dec, 1e-9) {
				t.Fatalf("%v on %s came back as %v %v", c, pier, ha, dec)
			}
			if m.Pier() != pier {
				t.Fatalf("pier %s came back as %s", pier, m.Pier())
			}
		}
	}
	if ha, dec := hs.Home().HADec(); dec != 90 {
		t.Fatalf("home is at HA %v dec %v, want the pole", ha, dec)
	}
	s := hs.DefaultSafety()
	if n := len(s.Candidates(-5, 10)); n != 2 {
		t.Fatalf("near the meridian both pier sides must be checked, got %d", n)
	}
	if n := len(s.Candidates(-70, 10)); n != 1 {
		t.Fatalf("far east only one pier side is reachable, got %d", n)
	}
}

func TestGrid(t *testing.T) {
	t.Parallel()
	pc := testPlanConfig()
	rows := pc.Rows(0)
	if rows[0] > 1.1 || rows[len(rows)-1] < 33.9 || rows[len(rows)-1] > 34 {
		t.Fatalf("rows %v do not span 0-35 with the field", rows)
	}
	for i := 1; i < len(rows); i++ {
		if d := rows[i] - rows[i-1]; d > 2.06*0.7+0.01 {
			t.Fatalf("row step %v leaves less than 30%% overlap", d)
		}
	}
	if r := pc.Rows(5); r[0] < 5 {
		t.Fatalf("rows start at %v below the 5 deg limit", r[0])
	}
	pc.Exclude = []hs.AzRange{{From: 350, To: 20}}
	for _, az := range pc.Columns() {
		if az >= 350 || az <= 20 {
			t.Fatalf("excluded azimuth %v planned", az)
		}
	}
	if n := len(pc.Columns()); n != 32 {
		t.Fatalf("got %d columns, want 32", n)
	}
}
