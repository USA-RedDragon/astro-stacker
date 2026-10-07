package quality_test

import (
	"fmt"
	"math"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/quality"
)

func TestFraming(t *testing.T) {
	t.Parallel()
	// Half a turn apart is the same field: the meridian flip, 90 and 270.
	for _, c := range []struct{ a, b float64 }{{89.9, 270.2}, {0.6, 180.94}, {359.5, 0.4}, {-90, 90}} {
		if quality.Framing(c.a) != quality.Framing(c.b) {
			t.Errorf("Framing(%v) = %d, Framing(%v) = %d, want equal", c.a, quality.Framing(c.a), c.b, quality.Framing(c.b))
		}
	}
	if quality.Framing(0) == quality.Framing(90) || quality.Framing(120) == quality.Framing(150) {
		t.Error("a field turned by 30 degrees or more should frame apart")
	}
	if quality.Framing(math.NaN()) != -1 {
		t.Error("an unknown angle should be its own framing")
	}
}

func TestExcess(t *testing.T) {
	t.Parallel()
	if got := quality.Excess(1091.5, 993); math.Abs(got-98.5) > 1e-9 {
		t.Errorf("Excess = %v, want 98.5", got)
	}
	// Missing statistics decode as 0 and mean nothing is known.
	if !math.IsNaN(quality.Excess(0, 993)) || !math.IsNaN(quality.Excess(1091.5, 0)) || !math.IsNaN(quality.Excess(math.NaN(), 993)) {
		t.Error("missing ADU statistics should give NaN")
	}
	// Under cloud only sky is left.
	if got := quality.Excess(990, 993); got != -3 {
		t.Errorf("Excess = %v, want -3", got)
	}
}

func TestTransparencyReference(t *testing.T) {
	t.Parallel()
	bright := make([]float64, 0, 10)
	for i := range 10 {
		bright = append(bright, 30+float64(i))
	}
	if got := quality.TransparencyReference(bright); got != quality.Reference(bright) {
		t.Errorf("reference = %v, want the %vth percentile %v", got, quality.ReferencePercentile, quality.Reference(bright))
	}
	if got := quality.TransparencyReference(bright[:9]); !math.IsNaN(got) {
		t.Errorf("reference of 9 subs = %v, want NaN", got)
	}
	if got := quality.TransparencyReference(append(bright[:9:9], math.NaN())); !math.IsNaN(got) {
		t.Errorf("reference of 9 measured subs = %v, want NaN", got)
	}
	// A galaxy in a dark field: a few ADU, as much rounding and hot pixels
	// as light.
	faint := make([]float64, 0, 20)
	for i := range 20 {
		faint = append(faint, 3+float64(i%5)*0.2)
	}
	if got := quality.TransparencyReference(faint); !math.IsNaN(got) {
		t.Errorf("reference of a dark field = %v, want NaN", got)
	}
}

func TestTransparency(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ excess, ref, want float64 }{
		{98.5, 139.4, 98.5 / 139.4},
		{150, 139.4, 1}, // capped
		{-3, 40, 0},     // nothing but sky
		{math.NaN(), 40, 1},
		{20, math.NaN(), 1},
		{20, 0, 1},
	} {
		if got := quality.Transparency(c.excess, c.ref); math.Abs(got-c.want) > 1e-12 {
			t.Errorf("Transparency(%v, %v) = %v, want %v", c.excess, c.ref, got, c.want)
		}
	}
}

// transparencyMeta is an acquired image's metadata with the fields scoring
// and transparency read.
func transparencyMeta(file, filter string, gain int, rotation, hfr, median, mean float64) string {
	return fmt.Sprintf(`{"FileName":"A:\\NINA\\T\\LIGHT\\%s","FilterName":%q,"ExposureDuration":300,"Gain":%d,"Offset":50,`+
		`"DetectedStars":1500,"HFR":%v,"ADUMedian":%v,"ADUMean":%v,"RotatorPosition":%v}`, file, filter, gain, hfr, median, mean, rotation)
}

// A hazy sub keeps its old score times t^2, against the best subs of its
// field only: framed the same way (a flip is the same), the same gain, and
// with light enough to measure.
func TestScoresTakeTransparency(t *testing.T) {
	t.Parallel()
	db := emptyScheduler(t)
	if err := db.Exec(`INSERT INTO target ("Id", name) VALUES (1, 'Orion'), (2, 'Markarian')`).Error; err != nil {
		t.Fatal(err)
	}
	id := 0
	add := func(target int, meta string, grading int) {
		t.Helper()
		id++
		if err := db.Exec(`INSERT INTO acquiredimage ("Id", "targetId", "gradingStatus", metadata) VALUES (?, ?, ?, ?)`,
			id, target, grading, meta).Error; err != nil {
			t.Fatal(err)
		}
	}
	// Orion, Luminance: twelve clear subs on either side of the flip, with
	// 130 to 141 ADU above the sky, as on 2025-12-19 to 24.
	for i := range 12 {
		rot := 90.2
		if i%2 == 1 {
			rot = 270.1
		}
		add(1, transparencyMeta(fmt.Sprintf("clear_%02d.xisf", i), "Luminance", 0, rot, 1.70, 900, 900+130+float64(i)), quality.GradingAccepted)
	}
	// A rejected sub's light doesn't set the reference.
	add(1, transparencyMeta("rejected.xisf", "Luminance", 0, 90, 1.70, 900, 900+400), quality.GradingRejected)
	// The hazy sub of 2025-12-26 02:36: as sharp to NINA, the sky up,
	// 98.5 ADU of light.
	add(1, transparencyMeta("hazy.xisf", "Luminance", 0, 89.9, 1.70, 993, 993+98.5), quality.GradingAccepted)
	// The same light at another gain or turned to another angle has no
	// reference of its own, so it is scored as before.
	add(1, transparencyMeta("gain100.xisf", "Luminance", 100, 90, 1.70, 993, 993+98.5), quality.GradingAccepted)
	add(1, transparencyMeta("turned.xisf", "Luminance", 0, 45, 1.70, 993, 993+98.5), quality.GradingAccepted)
	// One without ADU statistics.
	add(1, `{"FileName":"A:\\NINA\\T\\LIGHT\\nostats.xisf","FilterName":"Luminance","ExposureDuration":300,"Offset":50,"HFR":1.7,"ADUMedian":993}`, quality.GradingAccepted)
	// A dark field: 4 ADU of light, too little to tell haze by.
	for i := range 12 {
		add(2, transparencyMeta(fmt.Sprintf("dark_%02d.xisf", i), "Red", 0, 0, 1.70, 700, 700+4+float64(i%3)), quality.GradingAccepted)
	}

	scores, err := quality.LoadScores(t.Context(), db, 506, []quality.Measured{
		{File: "own.fits", Target: "Orion", Filter: "Luminance", Exposure: 300, SkyADU: 900, Offset: 50, HFR: 1.7, Stars: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The reference is the 90th percentile of the field's subs, the hazy
	// one with the twelve clear ones.
	field := []float64{98.5}
	for i := range 12 {
		field = append(field, 130+float64(i))
	}
	ref := quality.Reference(field)
	hazy := scores["hazy.xisf"]
	if want := 98.5 / ref; math.Abs(hazy.Transparency-want) > 1e-9 {
		t.Errorf("hazy transparency = %v, want %v", hazy.Transparency, want)
	}
	// Its score without transparency is what the same sub scores where
	// nothing is known of its light.
	old := scores["nostats.xisf"].Score
	if want := old * hazy.Transparency * hazy.Transparency; math.Abs(hazy.Score-want) > 1e-12 {
		t.Errorf("hazy score = %v, want %v x t^2 = %v", hazy.Score, old, want)
	}
	for _, f := range []string{"gain100.xisf", "turned.xisf", "nostats.xisf", "dark_03.xisf", "own.fits"} {
		if s := scores[f]; s.Transparency != 1 {
			t.Errorf("%s transparency = %v, want 1", f, s.Transparency)
		}
	}
	if s := scores["gain100.xisf"]; s.Score != old {
		t.Errorf("gain100 score = %v, want %v", s.Score, old)
	}
	for i := range 12 {
		f := fmt.Sprintf("clear_%02d.xisf", i)
		if s := scores[f]; !(s.Transparency > 0.9) {
			t.Errorf("%s transparency = %v, want above 0.9", f, s.Transparency)
		}
	}
	if s := scores["rejected.xisf"]; s.Score != 0 {
		t.Errorf("rejected score = %v, want 0", s.Score)
	}
}
