package darkcheck_test

import (
	"math"
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

func fp(v float64) *float64 { return &v }

func frame(seed uint64, level, noise, ramp float64) []float32 {
	r := rand.New(rand.NewPCG(seed, seed))
	d := make([]float32, testW*testH)
	for y := range testH {
		for x := range testW {
			v := level + noise*r.NormFloat64() + ramp*float64(testW-x+testH-y)/float64(testW+testH)
			d[y*testW+x] = float32(math.Round(v) / 65535)
		}
	}
	return d
}

func setup(exp, temp float64) darkcheck.Setup {
	return darkcheck.Setup{Exposure: exp, Gain: 100, Offset: 50, BinX: 1, SetTemp: temp, TakenAt: time.Date(2026, 10, 9, 8, 0, 0, 0, time.UTC)}
}

func sample(id int, exp, temp float64, m darkcheck.Measures, clean bool) darkcheck.Sample {
	return darkcheck.Sample{ID: id, Setup: setup(exp, temp), Measures: m, Clean: clean}
}

func TestQuantizedBiasHasANonzeroSpread(t *testing.T) {
	t.Parallel()
	m := darkcheck.Measure(frame(1, 502, 3.5, 0), testW, testH)
	if m.Median != 502 {
		t.Errorf("median %v", m.Median)
	}
	if m.Spread <= 0 || m.Spread > 1 {
		t.Errorf("a flat integer bias frame should spread a little above 0 ADU, got %v", m.Spread)
	}
	if m.Noise < 2.5 || m.Noise > 4.5 {
		t.Errorf("noise %v", m.Noise)
	}
	if leak := darkcheck.Measure(frame(2, 502, 3.5, 150), testW, testH); leak.Spread < 100 {
		t.Errorf("leak spread %v", leak.Spread)
	}
}

func TestProductionDarksAreNotJudgedAgainstBias(t *testing.T) {
	t.Parallel()
	bias, ok := darkcheck.BiasReference([]darkcheck.Measures{{Median: 502}, {Median: 502}, {Median: 502}})
	if !ok {
		t.Fatal("no bias reference")
	}
	d := setup(600, -15)
	m := darkcheck.Measures{Median: 506, Spread: 3}
	if v := darkcheck.Judge(d, m, &bias, nil); v.State != darkcheck.StateUnchecked {
		t.Errorf("a 600 s dark with no dark references: %s %s", v.State, v.Reason)
	}
	darks := []darkcheck.Sample{
		sample(1, 600, -15, darkcheck.Measures{Median: 506, Spread: 2.8}, true),
		sample(2, 600, -15, darkcheck.Measures{Median: 507, Spread: 3.2}, true),
		sample(3, 600, -15, darkcheck.Measures{Median: 506, Spread: 3.0}, true),
	}
	ref, ok := darkcheck.SelectReference(9, d, darks)
	if !ok || ref.Kind != darkcheck.RefSameCombo {
		t.Fatalf("%+v", ref)
	}
	if v := darkcheck.Judge(d, m, &bias, &ref); v.State != darkcheck.StateClean {
		t.Errorf("3 ADU among 2.8 to 3.2 ADU darks: %s", v.Reason)
	}
	if v := darkcheck.Judge(d, darkcheck.Measures{Median: 506, Spread: 7}, &bias, &ref); v.State != darkcheck.StateLeak || !strings.Contains(v.Reason, "limit of 3.6 ADU") {
		t.Errorf("7 ADU: %s %s", v.State, v.Reason)
	}
	if v := darkcheck.Judge(d, darkcheck.Measures{Median: 520, Spread: 3}, &bias, &ref); v.State != darkcheck.StateLeak || !strings.Contains(v.Reason, "median") {
		t.Errorf("bright: %s %s", v.State, v.Reason)
	}
}

func TestBoundingDarksCheckANewCombo(t *testing.T) {
	t.Parallel()
	bias, _ := darkcheck.BiasReference([]darkcheck.Measures{{Median: 502}, {Median: 502}, {Median: 503}})
	d := setup(300, -10)
	darks := []darkcheck.Sample{
		sample(1, 600, -5, darkcheck.Measures{Median: 508, Spread: 3.1}, true),
		sample(2, 600, -5, darkcheck.Measures{Median: 508, Spread: 3.3}, true),
		sample(3, 600, -20, darkcheck.Measures{Median: 900, Spread: 30}, true),
	}
	ref, ok := darkcheck.SelectReference(9, d, darks)
	if !ok || ref.Kind != darkcheck.RefBounding || len(ref.Samples) != 2 {
		t.Fatalf("%+v", ref)
	}
	if v := darkcheck.Judge(d, darkcheck.Measures{Median: 505, Spread: 2.9}, &bias, &ref); v.State != darkcheck.StateClean {
		t.Errorf("clean: %s", v.Reason)
	}
	if v := darkcheck.Judge(d, darkcheck.Measures{Median: 512, Spread: 2.9}, &bias, &ref); v.State != darkcheck.StateLeak {
		t.Errorf("glow: %s %s", v.State, v.Reason)
	}
}

func TestSiblingsBootstrapTheFirstSet(t *testing.T) {
	t.Parallel()
	d := setup(300, -3)
	darks := []darkcheck.Sample{
		sample(1, 300, -3, darkcheck.Measures{Median: 503, Spread: 2.9}, false),
		sample(2, 300, -3, darkcheck.Measures{Median: 503, Spread: 3.0}, false),
		sample(3, 300, -3, darkcheck.Measures{Median: 530, Spread: 14}, false),
	}
	ref, ok := darkcheck.SelectReference(3, d, darks)
	if !ok || ref.Kind != darkcheck.RefSiblings {
		t.Fatalf("%+v", ref)
	}
	if v := darkcheck.Judge(d, darks[2].Measures, nil, &ref); v.State != darkcheck.StateLeak {
		t.Errorf("dawn-lit sibling: %s %s", v.State, v.Reason)
	}
}

func TestJudgeRejectsADarkOffItsSetpoint(t *testing.T) {
	t.Parallel()
	d := setup(300, -15)
	d.CCDTemp = fp(-12.5)
	if v := darkcheck.Judge(d, darkcheck.Measures{}, nil, nil); v.State != darkcheck.StateOffTemp || !strings.Contains(v.Reason, "2.5") {
		t.Errorf("%+v", v)
	}
	d.CCDTemp = fp(-13.5)
	if v := darkcheck.Judge(d, darkcheck.Measures{}, nil, nil); v.State != darkcheck.StateUnchecked {
		t.Errorf("%+v", v)
	}
}
