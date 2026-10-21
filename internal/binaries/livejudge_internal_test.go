package binaries

import (
	"bytes"
	"encoding/json"
	"math"
	"math/rand/v2"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
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

func TestEclipseFarLateIsOff(t *testing.T) {
	t.Parallel()
	j := judgeEclipse(synthRun(-130, 330, 150*time.Minute, 0.45, 0.02), liveModel())
	if j.state != LiveOff || !strings.Contains(j.why, "min late") {
		t.Fatalf("state %s (%s), want off and late", j.state, j.why)
	}
	if math.Abs(j.shift.Minutes()-150) > 4 {
		t.Errorf("timing %.1f min, truth 150", j.shift.Minutes())
	}
	if before := judgeEclipse(synthRun(-130, 330, 150*time.Minute, 0.45, 0.02)[:30], liveModel()); before.state == LiveFollowing {
		t.Errorf("before the late dip ends: state %s (%s)", before.state, before.why)
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

func TestEclipseNotHeldWhenTheNearbyStarsDimTogether(t *testing.T) {
	t.Parallel()
	pts := synthRun(-130, 60, 0, 0.45, 0.02)
	for i := len(pts) - 5; i < len(pts); i++ {
		pts[i].Comp += 0.3
		pts[i].Agree, pts[i].Present = 12, 12
	}
	if j := judgeEclipse(pts, liveModel()); j.state != LiveFollowing || math.Abs(j.comps.shift-0.3) > 0.03 {
		t.Errorf("state %s (%s) shift %.3f, want following with the 0.3 mag shared dimming noted", j.state, j.why, j.comps.shift)
	}
}

func TestEclipseHeldBackWhenNearbyStarsDisagree(t *testing.T) {
	t.Parallel()
	pts := synthRun(-130, 60, 0, 0.45, 0.02)
	for i := len(pts) - 5; i < len(pts); i++ {
		pts[i].Agree, pts[i].Present = 5, 12
	}
	if j := judgeEclipse(pts, liveModel()); j.state != LiveComps || !strings.Contains(j.why, "only 5 of the 12 nearby stars agree") {
		t.Errorf("state %s (%s), want nearby stars disagreeing", j.state, j.why)
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
	j := judgeDip(synthWatch(30, 0.03, 0, -1, -1), goalOf(0.15, 2))
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
	j := judgeDip(synthWatch(30, 0.03, 0.3, 12, 25), goalOf(0.15, 2))
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

func TestDipWatchSharedDimmingIsNoDip(t *testing.T) {
	t.Parallel()
	pts := synthWatch(30, 0.03, 0, -1, -1)
	for i := 12; i < 30; i++ {
		pts[i].Comp += 0.4
	}
	if j := judgeDip(pts, goalOf(0.15, 2)); j.state != LiveNoDip || j.found {
		t.Errorf("state %s (%s), want no dip while the nearby stars dim together", j.state, j.why)
	}
	pts = synthWatch(30, 0.03, 0.3, 12, 18)
	for i := 12; i <= 18; i++ {
		pts[i].Agree, pts[i].Present = 4, 12
	}
	if j := judgeDip(pts, goalOf(0.15, 2)); j.found {
		t.Errorf("state %s (%s), want no dip while the nearby stars disagree", j.state, j.why)
	}
	for i := 25; i < 30; i++ {
		pts[i].Agree, pts[i].Present = 4, 12
	}
	if j := judgeDip(pts, goalOf(0.15, 2)); j.state != LiveComps {
		t.Errorf("state %s (%s), want nearby stars disagreeing", j.state, j.why)
	}
}

// tonight is the dip watch on find 15523 of 2026-10-21 UTC, 02:10 to
// 06:59, as the live card measures it: 87 subs, 76 of them below the
// cut, while the nearby stars dimmed together 0.4 mag as the field sank
// toward the horizon.
func tonight(t *testing.T) []LivePoint {
	t.Helper()
	raw, err := os.ReadFile("testdata/live15523-20261021.json.zst")
	if err != nil {
		t.Fatal(err)
	}
	dec, err := zstd.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()
	var rows []struct {
		T       time.Time `json:"t"`
		Mag     float64   `json:"mag"`
		Err     float64   `json:"err"`
		Comp    float64   `json:"comp"`
		Spread  float64   `json:"spread"`
		Agree   int       `json:"agree"`
		Present int       `json:"present"`
	}
	if err := json.NewDecoder(dec).Decode(&rows); err != nil {
		t.Fatal(err)
	}
	out := make([]LivePoint, len(rows))
	for i, r := range rows {
		out[i] = LivePoint{FrameID: i + 1, Time: r.T, Filter: redFilter, Mag: r.Mag, Err: r.Err, Comp: r.Comp, Spread: r.Spread, Agree: r.Agree, Present: r.Present}
	}
	return out
}

func TestDipWatchTonightIsNotHeldBack(t *testing.T) {
	t.Parallel()
	pts := tonight(t)
	last := make([]float64, 0, compRecent)
	for _, p := range pts[len(pts)-compRecent:] {
		last = append(last, p.Comp)
	}
	if m := medianOf(last); m < 0.2 {
		t.Fatalf("the nearby stars read %.2f mag fainter over the last subs, want tonight's 0.24", m)
	}
	j := judgeDip(pts, goalOf(0.15, 2))
	if j.state != LiveNoDip || j.found {
		t.Fatalf("state %s (%s), want no dip and no hold", j.state, j.why)
	}
	if j.run < 12 || j.run > 18 || j.smallest > 0.16 || math.Abs(j.latest) > 0.1 {
		t.Errorf("runs of %d subs, smallest dip %.3f, latest %.3f", j.run, j.smallest, j.latest)
	}
}

func TestDipWatchTonightSeesADip(t *testing.T) {
	t.Parallel()
	pts := tonight(t)
	from := len(pts) - 30
	for i := from; i < len(pts); i++ {
		pts[i].Mag += 0.3
	}
	j := judgeDip(pts, goalOf(0.15, 2))
	if j.state != LiveDip || math.Abs(j.depth-0.3) > 0.08 {
		t.Fatalf("state %s (%s) depth %.3f, want a 0.3 mag dip", j.state, j.why, j.depth)
	}
	if j.start.Before(pts[from-j.run].Time) || j.start.After(pts[from+j.run].Time) {
		t.Errorf("dip starts %s, truth %s", j.start, pts[from].Time)
	}
}

func TestDipWatchTonightTooFaintIsHeld(t *testing.T) {
	t.Parallel()
	pts := tonight(t)
	for i := len(pts) - compRecent; i < len(pts); i++ {
		pts[i].Err = 0.3
	}
	if j := judgeDip(pts, goalOf(0.15, 2)); j.state != LiveFaint || !strings.Contains(j.why, "measured only to 0.30 mag") {
		t.Errorf("state %s (%s), want too faint", j.state, j.why)
	}
}
