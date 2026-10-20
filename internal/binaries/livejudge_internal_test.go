package binaries

import (
	"math"
	"math/rand/v2"
	"strings"
	"testing"
	"time"
)

var liveMid = time.Date(2026, 10, 21, 3, 41, 0, 0, time.UTC)

func liveModel() eclipseModel {
	half := 105 * time.Minute
	return eclipseModel{mid: liveMid, err: 4 * time.Minute, half: half, depth: 0.45, shape: func(dt time.Duration) float64 {
		u := dt.Seconds() / half.Seconds()
		if math.Abs(u) >= 1 {
			return 0
		}
		return math.Pow(1-u*u, 0.7)
	}}
}

// synthRun is subs every 5 minutes from first to last minutes from the
// expected middle, the dip shift late and depth deep, on a level of 12.3
// with noise sigma, and the nearby stars steady to 0.008 mag.
func synthRun(first, last int, shift time.Duration, depth, sigma float64) []LivePoint {
	m := liveModel()
	r := rand.New(rand.NewPCG(3, 5))
	var out []LivePoint
	for k := first; k <= last; k += 5 {
		t := liveMid.Add(time.Duration(k) * time.Minute)
		y := 12.3 + depth*m.at(t, shift) + sigma*r.NormFloat64()
		out = append(out, LivePoint{FrameID: len(out) + 1, Time: t, Filter: "R", Mag: y, Err: sigma, Comp: 0.008 * r.NormFloat64()})
	}
	return out
}

func TestEclipseFollowingTheCurve(t *testing.T) {
	t.Parallel()
	j := judgeEclipse(synthRun(-130, 60, 3*time.Minute, 0.45, 0.02), liveModel())
	if j.state != LiveFollowing {
		t.Fatalf("state %s (%s), want following", j.state, j.why)
	}
	if math.Abs(j.shift.Minutes()-3) > 3 || j.shiftErr > 4*time.Minute {
		t.Errorf("timing %.1f ± %.1f min, truth 3", j.shift.Minutes(), j.shiftErr.Minutes())
	}
	if math.Abs(j.depth-0.45) > 0.04 || math.Abs(j.zero-12.3) > 0.02 {
		t.Errorf("depth %.3f level %.3f, truth 0.45 and 12.3", j.depth, j.zero)
	}
}

func TestEclipseLateIsOff(t *testing.T) {
	t.Parallel()
	j := judgeEclipse(synthRun(-130, 80, 25*time.Minute, 0.45, 0.02), liveModel())
	if j.state != LiveOff || !strings.Contains(j.why, "min late") {
		t.Fatalf("state %s (%s), want off and late", j.state, j.why)
	}
	if math.Abs(j.shift.Minutes()-25) > 4 {
		t.Errorf("timing %.1f min, truth 25", j.shift.Minutes())
	}
}

func TestEclipseShallowIsOff(t *testing.T) {
	t.Parallel()
	j := judgeEclipse(synthRun(-130, 60, 0, 0.25, 0.02), liveModel())
	if j.state != LiveOff || !strings.Contains(j.why, "shallower") || strings.Contains(j.why, "late") || strings.Contains(j.why, "early") {
		t.Fatalf("state %s (%s), want off and shallower only", j.state, j.why)
	}
}

func TestEclipseTooEarly(t *testing.T) {
	t.Parallel()
	if j := judgeEclipse(synthRun(-130, -110, 0, 0.45, 0.02), liveModel()); j.state != LiveEarly || !strings.Contains(j.why, "6 subs needed") {
		t.Errorf("five subs: state %s (%s), want early", j.state, j.why)
	}
	if j := judgeEclipse(synthRun(-130, -60, 0, 0.45, 0.02), liveModel()); j.state != LiveEarly || !strings.Contains(j.why, "middle") {
		t.Errorf("none near the middle: state %s (%s), want early", j.state, j.why)
	}
}

func TestEclipseHeldBackWhenNearbyStarsMove(t *testing.T) {
	t.Parallel()
	pts := synthRun(-130, 60, 0, 0.45, 0.02)
	for i := len(pts) - 5; i < len(pts); i++ {
		pts[i].Comp += 0.3
		pts[i].Mag += 0.02
	}
	if j := judgeEclipse(pts, liveModel()); j.state != LiveComps || !strings.Contains(j.why, "0.3") {
		t.Errorf("state %s (%s), want nearby stars moving", j.state, j.why)
	}
}

// synthWatch is n subs 3 minutes apart on a level of 16.9 with noise
// sigma, faded by fade for subs from to to.
func synthWatch(n int, sigma float64, fade float64, from, to int) []LivePoint {
	r := rand.New(rand.NewPCG(9, 1))
	var out []LivePoint
	for i := range n {
		y := 16.9 + sigma*r.NormFloat64()
		if i >= from && i <= to {
			y += fade
		}
		out = append(out, LivePoint{FrameID: i + 1, Time: liveMid.Add(time.Duration(3*i) * time.Minute), Filter: "L", Mag: y, Err: sigma, Comp: 0.008 * r.NormFloat64()})
	}
	return out
}

func TestDipWatchQuiet(t *testing.T) {
	t.Parallel()
	j := judgeDip(synthWatch(30, 0.03, 0, -1, -1))
	if j.state != LiveNoDip || j.found {
		t.Fatalf("state %s (%s), want no dip", j.state, j.why)
	}
	if j.smallest < 0.05 || j.smallest > 0.09 || j.over != 12*time.Minute {
		t.Errorf("smallest dip %.3f over %s, want about 0.06 over 12m", j.smallest, j.over)
	}
	if math.Abs(j.zero-16.9) > 0.02 {
		t.Errorf("level %.3f, truth 16.9", j.zero)
	}
}

func TestDipWatchSeesADip(t *testing.T) {
	t.Parallel()
	j := judgeDip(synthWatch(30, 0.03, 0.3, 12, 25))
	if j.state != LiveDip {
		t.Fatalf("state %s (%s), want dip", j.state, j.why)
	}
	if want := liveMid.Add(36 * time.Minute); !j.start.Equal(want) {
		t.Errorf("dip starts %s, want %s", j.start, want)
	}
	if math.Abs(j.depth-0.3) > 0.04 || math.Abs(j.zero-16.9) > 0.02 {
		t.Errorf("depth %.3f level %.3f, truth 0.3 and 16.9", j.depth, j.zero)
	}
}

func TestDipWatchCloudIsNoDip(t *testing.T) {
	t.Parallel()
	pts := synthWatch(30, 0.03, 0.3, 12, 18)
	for i := 12; i <= 18; i++ {
		pts[i].Comp += 0.4
	}
	if j := judgeDip(pts); j.state != LiveNoDip || j.found {
		t.Errorf("state %s (%s), want no dip while the nearby stars faded too", j.state, j.why)
	}
	pts = synthWatch(30, 0.03, 0.3, 25, 29)
	for i := 25; i < 30; i++ {
		pts[i].Comp += 0.4
	}
	if j := judgeDip(pts); j.state != LiveComps {
		t.Errorf("state %s (%s), want nearby stars moving", j.state, j.why)
	}
}
