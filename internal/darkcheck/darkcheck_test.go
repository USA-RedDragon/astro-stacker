package darkcheck_test

import (
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/darkcheck"
)

const (
	testW = 1024
	testH = 768
)

func frame(seed uint64, level, ramp float64) []float32 {
	r := rand.New(rand.NewPCG(seed, seed))
	d := make([]float32, testW*testH)
	for y := range testH {
		for x := range testW {
			n := level + 7*r.NormFloat64()
			d[y*testW+x] = float32((n + ramp*float64(testW-x+testH-y)/float64(testW+testH)) / 65535)
		}
	}
	return d
}

func fp(v float64) *float64 { return &v }

func biasMeasures(n int) []darkcheck.Measures {
	out := make([]darkcheck.Measures, n)
	for i := range n {
		out[i] = darkcheck.Measure(frame(uint64(100+i), 500, 0), testW, testH)
	}
	return out
}

func TestMeasure(t *testing.T) {
	t.Parallel()
	m := darkcheck.Measure(frame(1, 500, 0), testW, testH)
	if m.Median < 499 || m.Median > 501 {
		t.Errorf("median %v", m.Median)
	}
	if m.Noise < 6 || m.Noise > 8 {
		t.Errorf("noise %v", m.Noise)
	}
	if m.Spread > 3 {
		t.Errorf("clean spread %v", m.Spread)
	}
	leak := darkcheck.Measure(frame(2, 500, 150), testW, testH)
	if leak.Spread < 100 {
		t.Errorf("leak spread %v", leak.Spread)
	}
}

func TestBiasReferenceNeedsMinFrames(t *testing.T) {
	t.Parallel()
	if _, ok := darkcheck.BiasReference(biasMeasures(2)); ok {
		t.Fatal("two bias frames gave a reference")
	}
	r, ok := darkcheck.BiasReference([]darkcheck.Measures{{Median: 502, Spread: 1}, {Median: 503, Spread: 1.2}, {Median: 501, Spread: 0.9}})
	if !ok || r.Level != 502 || r.LevelTol != 1 || r.SpreadMax != 1.2 || r.Frames != 3 {
		t.Fatalf("%+v", r)
	}
	if r.SpreadTol < 0.29 || r.SpreadTol > 0.31 {
		t.Errorf("spread tol %v", r.SpreadTol)
	}
}

func TestJudgeAgainstMeasuredReferences(t *testing.T) {
	t.Parallel()
	bias, ok := darkcheck.BiasReference(biasMeasures(8))
	if !ok {
		t.Fatal("no bias reference")
	}
	night := time.Date(2026, 10, 9, 8, 0, 0, 0, time.UTC)
	s := darkcheck.Setup{Exposure: 300, Gain: 100, Offset: 50, BinX: 1, SetTemp: -10, TakenAt: night}
	cases := []struct {
		name  string
		data  []float32
		state string
	}{
		{"clean", frame(3, 500, 0), darkcheck.StateClean},
		{"daylight", frame(4, 500, 150), darkcheck.StateLeak},
		{"dawn ramp", frame(5, 500, 6), darkcheck.StateLeak},
	}
	for _, c := range cases {
		v := darkcheck.Judge(s, darkcheck.Measure(c.data, testW, testH), &bias, nil)
		if v.State != c.state {
			t.Errorf("%s: %s %s", c.name, v.State, v.Reason)
		}
	}
	v := darkcheck.Judge(s, darkcheck.Measure(frame(3, 500, 0), testW, testH), &bias, nil)
	if !strings.Contains(v.Reason, "level not checked") {
		t.Errorf("reason %q", v.Reason)
	}
}

func TestJudgeLevelUsesDarkCurrentFromWarmerDarks(t *testing.T) {
	t.Parallel()
	bias, _ := darkcheck.BiasReference(biasMeasures(5))
	night := time.Date(2026, 10, 9, 8, 0, 0, 0, time.UTC)
	s := darkcheck.Setup{Exposure: 300, Gain: 100, Offset: 50, BinX: 1, SetTemp: -10, TakenAt: night}
	warm := darkcheck.Sample{
		Setup:    darkcheck.Setup{Exposure: 600, Gain: 100, Offset: 50, BinX: 1, SetTemp: -5, TakenAt: night.Add(-72 * time.Hour)},
		Measures: darkcheck.Measure(frame(30, 506, 0), testW, testH),
	}
	cold := darkcheck.Sample{
		Setup:    darkcheck.Setup{Exposure: 600, Gain: 100, Offset: 50, BinX: 1, SetTemp: -20, TakenAt: night.Add(-72 * time.Hour)},
		Measures: darkcheck.Measure(frame(31, 900, 0), testW, testH),
	}
	sibling := darkcheck.Sample{
		Setup:    darkcheck.Setup{Exposure: 300, Gain: 100, Offset: 50, BinX: 1, SetTemp: -10, TakenAt: night.Add(time.Hour)},
		Measures: darkcheck.Measure(frame(32, 900, 0), testW, testH),
	}
	d, ok := darkcheck.DarkReference(s, bias, []darkcheck.Sample{warm, cold, sibling}, 36*time.Hour)
	if !ok || d.RateFrames != 1 || d.ColdestC != -5 {
		t.Fatalf("%+v", d)
	}
	if d.Rate < 0.008 || d.Rate > 0.012 {
		t.Errorf("rate %v", d.Rate)
	}
	if v := darkcheck.Judge(s, darkcheck.Measure(frame(33, 503, 0), testW, testH), &bias, &d); v.State != darkcheck.StateClean {
		t.Errorf("clean: %s", v.Reason)
	}
	if v := darkcheck.Judge(s, darkcheck.Measure(frame(34, 520, 0), testW, testH), &bias, &d); v.State != darkcheck.StateLeak ||
		!strings.Contains(v.Reason, "median") {
		t.Errorf("flat glow: %s %s", v.State, v.Reason)
	}
}

func TestJudgeWithoutBiasIsUnchecked(t *testing.T) {
	t.Parallel()
	v := darkcheck.Judge(darkcheck.Setup{Gain: 0, Offset: 50, BinX: 1}, darkcheck.Measures{Spread: 200}, nil, nil)
	if v.State != darkcheck.StateUnchecked || !strings.Contains(v.Reason, "bias") {
		t.Errorf("%+v", v)
	}
}

func TestJudgeRejectsADarkOffItsSetpoint(t *testing.T) {
	t.Parallel()
	bias, _ := darkcheck.BiasReference(biasMeasures(3))
	m := darkcheck.Measure(frame(3, 500, 0), testW, testH)
	v := darkcheck.Judge(darkcheck.Setup{Exposure: 300, Gain: 100, Offset: 50, BinX: 1, SetTemp: -15, CCDTemp: fp(-12.5)}, m, &bias, nil)
	if v.State != darkcheck.StateOffTemp || !strings.Contains(v.Reason, "2.5") {
		t.Errorf("%+v", v)
	}
	v = darkcheck.Judge(darkcheck.Setup{Exposure: 300, Gain: 100, Offset: 50, BinX: 1, SetTemp: -15, CCDTemp: fp(-13.5)}, m, &bias, nil)
	if v.State != darkcheck.StateClean {
		t.Errorf("%+v", v)
	}
}
