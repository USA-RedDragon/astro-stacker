package minima

import (
	"math"
	"testing"
)

const (
	ocPeriod  = 5.2
	ocStart   = 1500.0
	minuteDay = 1.0 / 1440
)

type trend struct {
	offset, curve float64
	primSlope     float64
	secSlope      float64
	waveAmp       float64
	wavePeriod    float64
}

func (tr trend) at(day float64, second bool) float64 {
	e := day / ocPeriod
	v := tr.offset + tr.curve*e*e + tr.waveAmp*math.Sin(2*math.Pi*day/math.Max(tr.wavePeriod, 1))
	if second {
		return v + tr.secSlope*e
	}
	return v + tr.primSlope*e
}

// timings spreads n timings over years, in observing seasons of about
// five months, every third one the second eclipse when withSec.
func timings(r *rng, n int, years float64, errMin float64, withSec bool, tr trend) []Timing {
	out := make([]Timing, 0, n)
	for i := range n {
		season := math.Floor(float64(i) * years / float64(n))
		day := ocStart + season*365.25 + r.uniform()*150
		second := withSec && i%3 == 2
		e := math.Round(day / ocPeriod)
		if second {
			e += 0.5
		}
		err := errMin * minuteDay * (0.7 + 0.6*r.uniform())
		out = append(out, Timing{Cycle: e, Second: second, Day: day, OC: tr.at(day, second) + err*r.normal(), Err: err})
	}
	return out
}

func prior() Prior {
	return Prior{Period: ocPeriod, T0Err: 0.8 * minuteDay, PeriodErr: 2e-6}
}

func TestTooFewTimingsNeverFlag(t *testing.T) {
	t.Parallel()
	r := &rng{s: 1}
	for n := range 3 {
		a := Analyze(timings(r, n, 1, 1, true, trend{offset: 30 * minuteDay}), prior())
		for _, x := range []Test{a.Ephemeris, a.Period, a.Orbit, a.Third} {
			if x.State != StateNeed || x.Need == "" {
				t.Errorf("%d timings: %+v", n, x)
			}
		}
	}
}

func TestNoFlagsOnNoise(t *testing.T) {
	t.Parallel()
	r := &rng{s: 2}
	flags := map[string]int{}
	trials := 400
	for range trials {
		a := Analyze(timings(r, 14, 2.6, 1.5, true, trend{}), prior())
		for id, x := range map[string]Test{TrendEphemeris: a.Ephemeris, TrendPeriod: a.Period, TrendOrbit: a.Orbit} {
			if x.State == StateFlag {
				flags[id]++
			}
		}
		if a.Third.State != StateNeed {
			t.Fatalf("a wave was looked for with %d timings over %.0f days", a.N, a.SpanDays)
		}
	}
	for id, n := range flags {
		if float64(n)/float64(trials) > 0.01 {
			t.Errorf("%s flagged on %d of %d noise-only records", id, n, trials)
		}
	}
}

func TestNoWaveOnNoise(t *testing.T) {
	t.Parallel()
	r := &rng{s: 3}
	flagged := 0
	trials := 25
	for range trials {
		a := Analyze(timings(r, 24, 5, 1.2, true, trend{}), prior())
		if a.Third.State == StateNeed {
			t.Fatal("no wave search on 24 timings over 5 years")
		}
		if a.Third.State == StateFlag {
			flagged++
		}
	}
	if flagged > 0 {
		t.Errorf("a third star was flagged on %d of %d noise-only records", flagged, trials)
	}
}

func TestEphemerisOffsetFlagged(t *testing.T) {
	t.Parallel()
	r := &rng{s: 4}
	a := Analyze(timings(r, 4, 0.3, 0.8, false, trend{offset: 6 * minuteDay}), prior())
	if a.Ephemeris.State != StateFlag {
		t.Fatalf("a 6 minute offset with 0.8 minute timings was not flagged: %+v", a.Ephemeris)
	}
	if got := a.Now / minuteDay; math.Abs(got-6) > 3*a.NowErr/minuteDay {
		t.Errorf("offset now %.1f min ± %.1f, want 6", got, a.NowErr/minuteDay)
	}
	if a.Period.State != StateNeed || a.Orbit.State != StateNeed {
		t.Errorf("period %+v orbit %+v from one season", a.Period, a.Orbit)
	}
}

func TestPeriodChangeFlagged(t *testing.T) {
	t.Parallel()
	r := &rng{s: 5}
	cycles := (ocStart + 4*365.25) / ocPeriod
	c := 20 * minuteDay / (cycles * cycles)
	hits := 0
	for range 20 {
		a := Analyze(timings(r, 16, 4, 1.2, true, trend{curve: c}), prior())
		if a.Period.State == StateFlag {
			hits++
			if a.Period.Value <= 0 {
				t.Errorf("period change of the wrong sign: %+v", a.Period)
			}
		}
		if a.Orbit.State == StateFlag {
			t.Errorf("orbit flagged on a period change: %+v", a.Orbit)
		}
	}
	if hits < 18 {
		t.Errorf("period change flagged in only %d of 20", hits)
	}
}

func TestOrbitTurningFlagged(t *testing.T) {
	t.Parallel()
	r := &rng{s: 6}
	cycles := 3 * 365.25 / ocPeriod
	s := 6 * minuteDay / cycles
	hits := 0
	for range 20 {
		ts := timings(r, 21, 3, 1, true, trend{primSlope: s, secSlope: -s})
		a := Analyze(ts, prior())
		if a.Orbit.State == StateFlag {
			hits++
			if a.Orbit.Value <= 0 {
				t.Errorf("drift apart of the wrong sign: %+v", a.Orbit)
			}
		}
	}
	if hits < 18 {
		t.Errorf("orbit turning flagged in only %d of 20", hits)
	}
}

func TestThirdStarFlagged(t *testing.T) {
	t.Parallel()
	r := &rng{s: 8}
	hits := 0
	for range 6 {
		a := Analyze(timings(r, 40, 6, 1, false, trend{waveAmp: 5 * minuteDay, wavePeriod: 700}), prior())
		if a.Third.State == StateFlag {
			hits++
			if math.Abs(a.Third.Period-700) > 120 {
				t.Errorf("wave period %.0f days, want 700", a.Third.Period)
			}
		}
	}
	if hits < 5 {
		t.Errorf("third star flagged in only %d of 6", hits)
	}
}

func TestOutlierLeftOut(t *testing.T) {
	t.Parallel()
	r := &rng{s: 10}
	ts := timings(r, 8, 1, 1, false, trend{})
	ts[3].OC += 60 * minuteDay
	a := Analyze(ts, prior())
	if a.Used[3] {
		t.Error("an hour-off timing was used")
	}
	if a.Ephemeris.State == StateFlag {
		t.Errorf("one bad timing flagged the ephemeris: %+v", a.Ephemeris)
	}
}
