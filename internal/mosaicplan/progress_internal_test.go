package mosaicplan

import (
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/goals"
	"github.com/USA-RedDragon/astro-stacker/internal/mosaics"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

func TestPlateauKeepsItsProgress(t *testing.T) {
	t.Parallel()
	gp := goals.Progress{Kind: goals.KindSNR, Goal: 10, Achieved: 6, SNR: 6, Progress: 0.36, Plateau: true, Done: true, HoursNeeded: 20, EffectiveHours: 3}
	pf, ok := panelFilter(fRed, app.Stack{Object: "P1", EffectiveSeconds: 3 * 3600}, true, nil, gp, true)
	if !ok || pf.Progress != 0.36 || !pf.Done || pf.DoneReason != DonePlateau {
		t.Fatalf("plateau %+v", pf)
	}
	reached, _ := panelFilter(fRed, app.Stack{Object: "P1"}, true, nil, goals.Progress{Progress: 1.2, Done: true}, true)
	if reached.Progress != 1.2 || reached.DoneReason != DoneGoal {
		t.Errorf("goal reached %+v", reached)
	}
	p := Panel{Filters: []PanelFilter{pf, {Filter: fGreen, Progress: 0.5}}}
	if got := schedulingProgress(p); got != 0.5 {
		t.Errorf("scheduling progress %v", got)
	}
}

const (
	fRed   = "Red"
	fGreen = "Green"
	fBlue  = "Blue"
)

func TestPanelLeastCountsPlateauAsCompleteAndCountsAsUnmeasured(t *testing.T) {
	t.Parallel()
	plateau := PanelFilter{Filter: fRed, Progress: 0.36, Done: true, Measured: true}
	green := PanelFilter{Filter: fGreen, Progress: 0.5, Measured: true}
	v, w, past := panelLeast([]PanelFilter{plateau, green})
	if v == nil || *v != 0.5 || w != fGreen || past {
		t.Fatalf("%v %q %v", v, w, past)
	}
	v, _, past = panelLeast([]PanelFilter{plateau})
	if v == nil || *v != 1 || !past {
		t.Fatalf("plateau only %v %v", v, past)
	}
	counts := PanelFilter{Filter: fBlue, Progress: 0.9, Source: SourceTS, NotMeasured: NoGoalMeasurement}
	v, w, _ = panelLeast([]PanelFilter{green, counts})
	if v != nil || w != fBlue {
		t.Fatalf("count-only filter must read as not measured: %v %q", v, w)
	}
	half, tenth := 0.5, 0.1
	panels := make([]Panel, 0, 3)
	panels = append(panels, Panel{Number: 1, Progress: &half, Weakest: fGreen}, Panel{Number: 2, Progress: &tenth, Weakest: fRed})
	c, n, f, avg := Completion(panels)
	if c == nil || *c != 0.1 || n != 2 || f != fRed || avg == nil || *avg != 0.3 {
		t.Fatalf("%v %d %s %v", c, n, f, avg)
	}
	panels = append(panels, Panel{Number: 3, Weakest: fBlue, Filters: []PanelFilter{counts}})
	c, n, f, avg = Completion(panels)
	if c != nil || n != 3 || f != fBlue || avg != nil || notMeasuredOf(panels, n, f) != NoGoalMeasurement {
		t.Fatalf("%v %d %s %v", c, n, f, avg)
	}
}

func TestNightsLeftCountsNightsAboveTheThreshold(t *testing.T) {
	t.Parallel()
	site := mosaics.Site{Lat: 32, Lon: -97}
	pts := []mosaics.Point{{RA: 186, Dec: 13}}
	now := time.Date(2026, 3, 1, 3, 0, 0, 0, time.UTC)
	n := nightsLeft(now, site, pts, 20)
	if n == 0 || n > 200 {
		t.Fatalf("nights left %d", n)
	}
	first := time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC)
	if h := mosaics.DarkHours(first.AddDate(0, 0, n-1), site, pts, 20); h < usableMonthly {
		t.Errorf("last counted night has %v dark hours", h)
	}
	if h := mosaics.DarkHours(first.AddDate(0, 0, n), site, pts, 20); h >= usableMonthly {
		t.Errorf("first uncounted night has %v dark hours", h)
	}
	if nightsLeft(time.Date(2026, 10, 1, 3, 0, 0, 0, time.UTC), site, pts, 20) != 0 {
		t.Error("an autumn night counted for a spring target")
	}
}

func TestImagingNightsCountOnlyYearsWithData(t *testing.T) {
	t.Parallel()
	jan, mar := time.Date(2020, 1, 5, 0, 0, 0, 0, time.UTC), time.Date(2023, 3, 7, 0, 0, 0, 0, time.UTC)
	h := nightHistory{hours: map[time.Time]float64{jan: 1, mar: 2}, first: jan, last: mar}
	m := h.perMonth()
	if m[0].SpanYears != 4 || m[0].Years != 2 || m[0].Nights == nil || *m[0].Nights != 0.5 {
		t.Errorf("january %+v", m[0])
	}
	if m[5].SpanYears != 3 || m[5].Years != 1 || m[5].Nights == nil || *m[5].Nights != 0 {
		t.Errorf("june %+v", m[5])
	}
}

func TestFramesVerdict(t *testing.T) {
	t.Parallel()
	conf, rule, sug := framesVerdict(&FramesProposal{Object: "M 101"})
	if conf != mosaics.ConfidenceLow || !strings.Contains(rule, "no mount coordinates") || strings.Contains(sug, "0.5") {
		t.Errorf("no coords: %s %q %q", conf, rule, sug)
	}
	sep := 0.27
	squid := &FramesProposal{Object: "SH2-129", Coords: true, Separation: &sep, Target: "Squid Nebula", Nearest: "Squid Nebula"}
	conf, rule, sug = framesVerdict(squid)
	if conf != mosaics.ConfidenceMedium || !strings.Contains(rule, "names don't match") || strings.Contains(sug, "object name") || !strings.Contains(sug, "don't match") {
		t.Errorf("coordinates only: %s %q %q", conf, rule, sug)
	}
	near := 0.1
	conf, _, sug = framesVerdict(&FramesProposal{Object: "Eagle Nebula", Coords: true, Separation: &near, Target: "Eagle Nebula Panel 1", NameMatch: true})
	if conf != mosaics.ConfidenceHigh || !strings.Contains(sug, "the names match") {
		t.Errorf("name and position: %s %q", conf, sug)
	}
	if framesIssue("Sadr Region", 6, true) == framesIssue("Sadr Region", 6, false) {
		t.Error("a project-named object reads like any other")
	}
}

func TestLayoutTextWrapsRotation(t *testing.T) {
	t.Parallel()
	d := Detail{RigKnown: true, Rows: 2, Cols: 1, Panels: []Panel{{Number: 1}, {Number: 2}}}
	for rot, want := range map[float64]string{359.7: "1 × 2 at 0°", 0.2: "1 × 2 at 0°", 135.4: "1 × 2 at 135°"} {
		d.Rotation = rot
		if got := layoutText(d); got == nil || *got != want {
			t.Errorf("rotation %v gave %v, want %q", rot, got, want)
		}
	}
}

func TestPlateauRollsUpToTheFurthestPanelFilter(t *testing.T) {
	t.Parallel()
	h, far, near := 25.0, 0.08, 0.7
	gp := goals.Progress{Progress: 1.4, Done: true, EffectiveHours: 2, PlateauAt: goals.Plateau{Hours: &h, Progress: &far}}
	met, _ := panelFilter(fRed, app.Stack{Object: "P1"}, true, nil, gp, true)
	if met.DoneReason != DoneGoal || met.PlateauAt.Progress == nil || *met.PlateauAt.Progress != far || *met.PlateauAt.Hours != h {
		t.Fatalf("%+v", met)
	}
	counts, _ := panelFilter(fBlue, app.Stack{}, false, []planRow{{Filter: fBlue, Enabled: 1, Desired: 10, Exposure: 300}}, goals.Progress{}, false)
	if counts.PlateauAt.Progress != nil || counts.PlateauAt.Unmeasured != NoGoalMeasurement || counts.PlateauAt.GainPct != goals.PlateauGainPct {
		t.Fatalf("%+v", counts)
	}
	green := PanelFilter{Filter: fGreen, PlateauAt: goals.Plateau{Progress: &near, Hours: &h}}
	p1 := Panel{Number: 1, Plateau: panelPlateau(1, []PanelFilter{green, met})}
	if p1.Plateau.Filter != fRed || *p1.Plateau.Progress != far {
		t.Fatalf("%+v", p1.Plateau)
	}
	p2 := Panel{Number: 2, Plateau: panelPlateau(2, []PanelFilter{green})}
	if w := mosaicPlateau([]Panel{p2, p1}); w.Panel != 1 || w.Filter != fRed {
		t.Fatalf("%+v", w)
	}
	p3 := Panel{Number: 3, Plateau: panelPlateau(3, []PanelFilter{green, counts})}
	if w := mosaicPlateau([]Panel{p1, p3}); w.Panel != 3 || w.Progress != nil || w.NotMeasured != NoGoalMeasurement {
		t.Fatalf("%+v", w)
	}
}
