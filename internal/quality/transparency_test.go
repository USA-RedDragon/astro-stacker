package quality_test

import (
	"fmt"
	"math"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/measure"
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
func transparencyMeta(file, filter string, gain int, rotation, median, mean float64) string {
	return fmt.Sprintf(`{"FileName":"A:\\NINA\\T\\LIGHT\\%s","FilterName":%q,"ExposureDuration":300,"Gain":%d,"Offset":50,`+
		`"DetectedStars":1500,"HFR":1.7,"ADUMedian":%v,"ADUMean":%v,"RotatorPosition":%v}`, file, filter, gain, median, mean, rotation)
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
		add(1, transparencyMeta(fmt.Sprintf("clear_%02d.xisf", i), "Luminance", 0, rot, 900, 900+130+float64(i)), quality.GradingAccepted)
	}
	// A rejected sub's light doesn't set the reference.
	add(1, transparencyMeta("rejected.xisf", "Luminance", 0, 90, 900, 900+400), quality.GradingRejected)
	// The hazy sub of 2025-12-26 02:36: as sharp to NINA, the sky up,
	// 98.5 ADU of light.
	add(1, transparencyMeta("hazy.xisf", "Luminance", 0, 89.9, 993, 993+98.5), quality.GradingAccepted)
	// The same light at another gain or turned to another angle has no
	// reference of its own, so it is scored as before.
	add(1, transparencyMeta("gain100.xisf", "Luminance", 100, 90, 993, 993+98.5), quality.GradingAccepted)
	add(1, transparencyMeta("turned.xisf", "Luminance", 0, 45, 993, 993+98.5), quality.GradingAccepted)
	// One without ADU statistics.
	add(1, `{"FileName":"A:\\NINA\\T\\LIGHT\\nostats.xisf","FilterName":"Luminance","ExposureDuration":300,"Offset":50,"HFR":1.7,"ADUMedian":993}`, quality.GradingAccepted)
	// A dark field: 4 ADU of light, too little to tell haze by.
	for i := range 12 {
		add(2, transparencyMeta(fmt.Sprintf("dark_%02d.xisf", i), filterRed, 0, 0, 700, 700+4+float64(i%3)), quality.GradingAccepted)
	}

	scores, err := quality.LoadScores(t.Context(), db, quality.Pedestals{Configured: 506}, []quality.Measured{
		{File: "own.fits", Target: "Orion", Filter: "Luminance", Exposure: 300, SkyADU: 900, Offset: 50, HFR: 1.7, Stars: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The reference is the 90th percentile of the field's subs, the hazy
	// one with the twelve clear ones.
	field := make([]float64, 0, 13)
	field = append(field, 98.5)
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

// photometry is a sub's star photometry: a field whose star at rank k has
// flux 1e8/k, times gain, the brightest ranks clipped as in Orion's
// Luminance (about 400 stars) when clear.
func photometry(gain float64) *measure.Photometry {
	ranks := measure.Ranks()
	p := &measure.Photometry{Noise: 34, Flux: make([]float64, len(ranks)), Saturated: make([]bool, len(ranks))}
	for j, k := range ranks {
		f := gain * 1e8 / float64(k)
		if f > 2.5e5 {
			p.Saturated[j] = true
			f = 2.5e5
		}
		p.Flux[j] = f
	}
	return p
}

func TestCoreTransparency(t *testing.T) {
	t.Parallel()
	field := make([]*measure.Photometry, 0, 12)
	for i := range 12 {
		field = append(field, photometry(1+0.01*float64(i%3)))
	}
	ref := quality.CoreReference(field)
	if got := quality.CoreTransparency(photometry(0.44), ref); math.Abs(got-0.44/1.02) > 0.01 {
		t.Errorf("hazy sub's core transparency = %.3f, want %.3f", got, 0.44/1.02)
	}
	if got := quality.CoreTransparency(photometry(1.1), ref); got != 1 {
		t.Errorf("a clearer sub = %v, want capped at 1", got)
	}
	if got := quality.CoreTransparency(nil, ref); !math.IsNaN(got) {
		t.Errorf("no photometry = %v, want NaN", got)
	}
	if ref := quality.CoreReference(field[:9]); ref != nil {
		t.Errorf("9 subs gave a reference %v", ref)
	}
	// Ranks that clip in the field's subs, or are faint, are not used.
	for j, k := range measure.Ranks() {
		clipped := 1.02e8/float64(k) > 2.5e5
		faint := 1e8/float64(k)/(34*math.Sqrt(113)) < 100
		if (clipped || faint) != math.IsNaN(ref[j]) {
			t.Errorf("rank %d (clipped %v, faint %v): reference %v", k, clipped, faint, ref[j])
		}
	}
}

// Where the field's photometry tells it, transparency is the starlight's:
// haze that spreads light into halos keeps the light above the sky (0.71 on
// Orion's 02:36 sub) but not the stars' (0.44). A stacker reject is scored.
func TestScoresTakeCoreTransparency(t *testing.T) {
	t.Parallel()
	db := emptyScheduler(t)
	if err := db.Exec(`INSERT INTO target ("Id", name) VALUES (1, 'Orion')`).Error; err != nil {
		t.Fatal(err)
	}
	var measured []quality.Measured
	add := func(id int, file string, grading int, reason any, median, mean float64, phot *measure.Photometry) {
		t.Helper()
		if err := db.Exec(`INSERT INTO acquiredimage ("Id", "targetId", "gradingStatus", metadata, rejectreason) VALUES (?, 1, ?, ?, ?)`,
			id, grading, transparencyMeta(file, "Luminance", 0, 90, median, mean), reason).Error; err != nil {
			t.Fatal(err)
		}
		if phot != nil {
			measured = append(measured, quality.Measured{File: file, Target: "Orion", Filter: "Luminance", Exposure: 300, Photometry: phot})
		}
	}
	for i := range 12 {
		add(i+1, fmt.Sprintf("clear_%02d.xisf", i), quality.GradingAccepted, nil, 900, 900+135, photometry(1))
	}
	add(20, "halo.xisf", quality.GradingAccepted, nil, 993, 993+0.71*135, photometry(0.44))
	add(21, "unmeasured.xisf", quality.GradingAccepted, nil, 993, 993+0.71*135, nil)
	add(22, "verdict.xisf", quality.GradingRejected, "stacker: sky", 993, 993+0.71*135, photometry(0.44))
	add(23, "graded.xisf", quality.GradingRejected, "HFR", 900, 900+135, photometry(1))
	scores, err := quality.LoadScores(t.Context(), db, quality.Pedestals{Configured: 506}, measured)
	if err != nil {
		t.Fatal(err)
	}
	if got := scores["halo.xisf"].Transparency; math.Abs(got-0.44) > 0.01 {
		t.Errorf("photometered hazy sub: transparency %.3f, want 0.44", got)
	}
	if got := scores["unmeasured.xisf"].Transparency; math.Abs(got-0.71) > 0.02 {
		t.Errorf("hazy sub without photometry: transparency %.3f, want 0.71 from the light above the sky", got)
	}
	v := scores["verdict.xisf"]
	if !v.StackerRejected || !(v.Score > 0) || v.Score != scores["halo.xisf"].Score {
		t.Errorf("stacker's reject = %+v, want scored as the same sub accepted", v)
	}
	if g := scores["graded.xisf"]; g.StackerRejected || g.Score != 0 {
		t.Errorf("Target Scheduler's reject = %+v, want score 0", g)
	}
	h := scores["halo.xisf"]
	if math.Abs(h.Score-h.PlainScore*0.44*0.44) > 0.01*h.PlainScore || h.PlainTargetBest != 1 {
		t.Errorf("halo sub: score %v, plain %v (best %v)", h.Score, h.PlainScore, h.PlainTargetBest)
	}
}
