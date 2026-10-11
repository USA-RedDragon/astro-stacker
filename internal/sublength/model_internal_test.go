package sublength

import (
	"math"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/moon"
)

func TestBucketEdges(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		glare float64
		want  Bucket
	}{{0, BucketDark}, {0.049, BucketDark}, {0.05, BucketLow}, {0.249, BucketLow}, {0.25, BucketMid}, {0.499, BucketMid}, {0.5, BucketBright}, {1, BucketBright}, {math.NaN(), BucketDark}} {
		if got := BucketOf(c.glare); got != c.want {
			t.Errorf("BucketOf(%v) = %s, want %s", c.glare, got, c.want)
		}
	}
}

func TestGlareFollowsThePlugin(t *testing.T) {
	t.Parallel()
	at := time.Date(2025, 11, 6, 8, 28, 0, 0, time.UTC)
	if g := Glare(at, 10.68, 41.27, 33, -97); !(g > 0.8) {
		t.Errorf("full moon high and 39° from M31: glare %v, want above 0.8", g)
	}
	newMoon := time.Date(2025, 11, 20, 6, 0, 0, 0, time.UTC)
	if g := Glare(newMoon, 10.68, 41.27, 33, -97); g > 0.01 {
		t.Errorf("new moon: glare %v, want about 0", g)
	}
}

func TestSensorAt(t *testing.T) {
	t.Parallel()
	if s, ok := SensorAt("ZWO ASI2600MM Pro", 100); !ok || s.ReadNoise != 1.2 || s.EPerADU != 0.252 {
		t.Errorf("gain 100: %+v %v", s, ok)
	}
	if _, ok := SensorAt("ZWO ASI2600MM Pro", 50); ok {
		t.Error("gain 50 was never measured but has a sensor")
	}
	if _, ok := SensorAt("QHY 600M", 0); ok {
		t.Error("another camera has the 2600's sensor")
	}
}

func TestFitRejection(t *testing.T) {
	t.Parallel()
	r := FitRejection([]Point{{120, 307, 5}, {300, 10487, 127}, {600, 4716, 88}})
	if r.MaxLength != 600 || !(r.PerSecond > 0) || !(r.Base > 0) {
		t.Fatalf("fit %+v", r)
	}
	if k := r.Kept(300); math.Abs(k-(1-r.Base-r.PerSecond*300)) > 1e-12 || !(k > 0.98) {
		t.Errorf("kept at 300 s %v", k)
	}
	flat := FitRejection([]Point{{120, 100, 10}, {300, 100, 1}})
	if flat.PerSecond != 0 {
		t.Errorf("rejection falling with length fitted a negative slope %v", flat.PerSecond)
	}
}

func TestSaturatedShareScalesWithLength(t *testing.T) {
	t.Parallel()
	s := Stars{Exposure: 300, Ranks: []int{32, 64, 128, 256}, Flux: []float64{8000, 4000, 2000, 1000}, Saturated: []bool{true, true, false, false}, Detected: 1000}
	if f, ok := s.SaturatedShare(300); !ok || f != 0.064 {
		t.Errorf("at its own length: %v %v, want 0.064", f, ok)
	}
	if f, _ := s.SaturatedShare(600); f != 0.128 {
		t.Errorf("at 600 s: %v, want 0.128", f)
	}
	if f, _ := s.SaturatedShare(120); f != 0 {
		t.Errorf("at 120 s: %v, want 0 (no listed rank reaches saturation)", f)
	}
	if _, ok := (Stars{Exposure: 300, Ranks: []int{32}, Flux: []float64{1}, Saturated: []bool{false}, Detected: 10}).SaturatedShare(600); ok {
		t.Error("a sub with no saturated star can't tell where saturation starts")
	}
}

func sensor0() Sensor { s, _ := SensorAt("ASI2600MM", 0); return s }

func TestRecommendFaintSkyGoesLonger(t *testing.T) {
	t.Parallel()
	rej := FitRejection([]Point{{120, 307, 5}, {300, 10487, 127}, {600, 4716, 88}})
	rec, ok := Recommend(Input{Sensor: sensor0(), Sky: 0.1, SkySubs: 100, SkySource: SourceTarget, Current: 300, Overhead: 13.5,
		Rejection: rej, DarkLengths: []float64{120, 300, 600}})
	if !ok || rec.Exposure != 600 {
		t.Fatalf("dark sky, no saturation data: %+v, want 600 s (300 s keeps less than 95%% of 600 s)", rec.Exposure)
	}
	if rec.SNRChange <= 0 {
		t.Errorf("going longer on a read-noise-bound sky should gain: %v", rec.SNRChange)
	}
}

func TestRecommendBrightMoonGoesShorterAndCapsSaturation(t *testing.T) {
	t.Parallel()
	rej := FitRejection([]Point{{120, 307, 5}, {300, 10487, 127}, {600, 4716, 88}})
	stars := []Stars{{Exposure: 300, Ranks: []int{32, 64, 128, 256}, Flux: []float64{8000, 4000, 2000, 1000}, Saturated: []bool{true, true, false, false}, Detected: 1000}}
	rec, ok := Recommend(Input{Sensor: sensor0(), Sky: 3, SkySubs: 30, SkySource: SourceTarget, Current: 300, Overhead: 13.5,
		Rejection: rej, Stars: stars, DarkLengths: []float64{120, 300, 600}})
	if !ok || rec.Exposure != 120 {
		t.Fatalf("bright moon: %v s, want 120 s", rec.Exposure)
	}
	for _, c := range rec.Candidates {
		if c.Exposure == 600 && c.Blocked == "" {
			t.Error("600 s saturates twice the stars 300 s does and was not blocked")
		}
		if c.Exposure == 60 && c.Darks {
			t.Error("60 s has no darks")
		}
	}
	if rec.Ideal != rec.Exposure || len(rec.Basis) == 0 {
		t.Errorf("ideal %v basis %q", rec.Ideal, rec.Basis)
	}
}

func TestRecommendProposesANewLengthOnlyForARealGain(t *testing.T) {
	t.Parallel()
	g100, _ := SensorAt("ASI2600MM", 100)
	rec, ok := Recommend(Input{Sensor: g100, Sky: 0.02, SkySubs: 100, SkySource: SourceTarget, Current: 600, Overhead: 13.5,
		Rejection: Rejection{MaxLength: 600}, DarkLengths: []float64{60}})
	if !ok || rec.Exposure != 60 || rec.Ideal == 60 || !(rec.IdealGain >= NewLengthGain) {
		t.Fatalf("only 60 s darks on a faint narrowband sky: use %v ideal %v gain %v; want a longer proposal", rec.Exposure, rec.Ideal, rec.IdealGain)
	}
	rec, _ = Recommend(Input{Sensor: g100, Sky: 0.3, SkySubs: 100, SkySource: SourceTarget, Current: 600, Overhead: 13.5,
		Rejection: Rejection{MaxLength: 600}, DarkLengths: []float64{600}})
	if rec.Ideal != rec.Exposure {
		t.Errorf("a shorter length within 5%% is no reason for new darks: use %v ideal %v", rec.Exposure, rec.Ideal)
	}
}

func TestRecommendWithoutDarksFallsBack(t *testing.T) {
	t.Parallel()
	rec, ok := Recommend(Input{Sensor: sensor0(), Sky: 0.3, SkySubs: 10, Current: 300, Overhead: 13.5, Rejection: Rejection{MaxLength: 600}})
	if !ok || rec.Exposure != 0 || !(rec.Ideal > 0) {
		t.Errorf("no darks at all: use %v ideal %v; want no length to use and a proposal", rec.Exposure, rec.Ideal)
	}
	if _, ok := Recommend(Input{Sensor: sensor0(), Sky: 0}); ok {
		t.Error("no sky measured gave a recommendation")
	}
}

func TestMoonAgeFollowsTheRightAscensionGap(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		at   time.Time
		want float64
	}{
		{time.Date(2025, 11, 5, 13, 19, 0, 0, time.UTC), lunarCycleDays / 2},
		{time.Date(2025, 11, 20, 6, 47, 0, 0, time.UTC), 0},
	} {
		got := moonAge(c.at, moon.At(c.at).RA)
		d := math.Abs(got - c.want)
		d = math.Min(d, lunarCycleDays-d)
		if d > 0.6 {
			t.Errorf("%s: age %.2f d, want about %.2f", c.at, got, c.want)
		}
	}
	if ra := sunRA(time.Date(2026, 3, 20, 14, 46, 0, 0, time.UTC)); math.Min(ra, 360-ra) > 0.2 {
		t.Errorf("sun RA at the March equinox %.3f°, want 0", ra)
	}
}
