package ownimage

import (
	"strings"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
)

// auditStack is one moving object of the audits of 2026-10-17, stacked as
// they did: the detection frames' signal to noise along the motion and its
// brightest point, the fake's, the larger of the wrong motions' and random
// places' 99th percentiles, and the unused frames with their fake.
type auditStack struct {
	name                 string
	obs, peak, inj, ctrl float64
	unused, unusedInj    float64
	unusedN              int
	want                 StackVerdict
}

// auditStacks are movers-rest/out/*_highpass.json and mover-67203's
// step1.json and inject.json.
func auditStacks() []auditStack {
	return []auditStack{
		{"632, a known asteroid", 12.20, 12.64, 16.0, 6.73, 7.18, 13.00, 8, StackConfirmed},
		{"58453, a known asteroid, its 2 unused frames too few to stack", 87.63, 90.78, 87.2, 23.41, 0, 0, 0, StackConfirmed},
		{"27926, a known asteroid", 20.17, 20.72, 22.8, 10.81, 6.42, 9.20, 3, StackConfirmed},
		{"27934, a known asteroid", 11.40, 11.51, 12.9, 6.91, 9.21, 18.00, 15, StackConfirmed},
		{"67203, star glow", 3.20, 3.28, 7.2, 8.17, -0.65, 6.5, 4, StackRefuted},
		{"67212, on two stars", 5.12, 5.66, 12.5, 6.58, 0.66, 16.77, 12, StackRefuted},
		{"48654, star glow", 0.75, 2.45, 14.1, 11.76, -0.38, 9.58, 3, StackRefuted},
		{"33324, star residual", 1.18, 1.91, 11.0, 3.02, 0.63, 17.20, 16, StackRefuted},
		{"33327, star residual", -0.98, -0.40, 16.4, 3.13, -1.49, 29.67, 18, StackRefuted},
		{"58457, star residual", 1.77, 2.68, 13.6, 4.59, 1.42, 24.18, 16, StackRefuted},
		{"27939, hot pixel", 4.91, 5.24, 11.6, 99.62, 0.86, 18.39, 13, StackRefuted},
		{"54782, hot pixel", 4.46, 4.76, 9.3, 3.54, 0.44, 12.42, 13, StackRefuted},
		{"27203 (54323), sensor corner", 6.87, 7.06, 14.1, 15.97, -1.14, 26.00, 16, StackRefuted},
		{"67045 (67049), diffuse light", 4.25, 4.57, 14.2, 5.57, -0.52, 20.05, 15, StackRefuted},
	}
}

func TestAuditedMoversStackToTheirKnownAnswers(t *testing.T) {
	t.Parallel()
	for _, a := range auditStacks() {
		ts := &TrackStack{Frames: 6, SNR: a.obs, Peak: a.peak, Injected: a.inj, InjectedLo: a.inj - 1, InjectedHi: a.inj + 1,
			Wrong: 300, Wrong99: a.ctrl, WrongMax: a.ctrl, Random: 700, Random99: a.ctrl,
			Unused: a.unusedN, UnusedSNR: a.unused, UnusedInjected: a.unusedInj}
		c, v := StackCheck(ts)
		if v != a.want {
			t.Errorf("%s: %s, want %s: %s", a.name, v, a.want, c.Evidence)
		}
		want := check.Pass
		if a.want != StackConfirmed {
			want = check.Fail
		}
		if c.Status != want {
			t.Errorf("%s: status %s", a.name, c.Status)
		}
	}
}

func TestFakeLightOnlyInTheFiltersFound(t *testing.T) {
	t.Parallel()
	sp := &stackPlan{}
	sp.light, sp.mag = trackLight([]TrackPoint{{Filter: "Luminance", Flux: 2, Mag: 18}, {Filter: "Luminance", Flux: 1, Mag: 18.75}})
	if v, ok := sp.lightIn("Luminance"); !ok || v < 1 || v > 2 {
		t.Errorf("Luminance light %.2f %v", v, ok)
	}
	if _, ok := sp.lightIn("Red"); ok {
		t.Errorf("a filter the track was not found in got a fake")
	}
}

func TestStackJudgement(t *testing.T) {
	t.Parallel()
	shows := []StackUnused{{SNR: 8, Inj: 10, Required: true}}
	cases := []struct {
		name string
		f    StackFacts
		want StackVerdict
		why  string
	}{
		{"too faint to tell", StackFacts{Obs: 1, Peak: 1, Inj: 4.9, HasControl: true, Control99: 3}, StackUntested, ""},
		{"54896, wing fit too faint to tell, its night's unused frames flat", StackFacts{Obs: 1.2, Peak: 1.2, Inj: 4.4, HasControl: true, Control99: 1.9,
			Unused: []StackUnused{{SNR: -0.2, Inj: 7.8, Required: true}}}, StackRefuted, ""},
		{"too faint to tell, its unused frames too", StackFacts{Obs: 1, Peak: 1, Inj: 4.4, Unused: []StackUnused{{SNR: 0, Inj: 4, Required: true}}}, StackUntested, ""},
		{"too faint to tell, its unused frames show it", StackFacts{Obs: 2, Peak: 2, Inj: 4.4, Unused: []StackUnused{{SNR: 6, Inj: 7.8, Required: true}}}, StackUntested, ""},
		{"no unused frames, no higher than the controls", StackFacts{Obs: 1, Peak: 1, Inj: 10, HasControl: true, Control99: 3}, StackRefuted, StackNoAboveControls},
		{"58729, no unused frames", StackFacts{Obs: 3.0, Peak: 3.0, Inj: 10.8, HasControl: true, Control99: 4.2}, StackRefuted, StackNoAboveControls},
		{"54898, wing fit, no unused frames", StackFacts{Obs: 1.3, Peak: 1.3, Inj: 5.8, HasControl: true, Control99: 3.0}, StackRefuted, StackNoAboveControls},
		{"no unused frames, above the controls", StackFacts{Obs: 4, Peak: 4, Inj: 10, HasControl: true, Control99: 3}, StackDoubtful, "only 40%"},
		{"no unused frames, no controls", StackFacts{Obs: 1, Peak: 1, Inj: 10}, StackDoubtful, "could not be measured"},
		{"unused frames too faint to judge", StackFacts{Obs: 1, Peak: 1, Inj: 10, HasControl: true, Control99: 3, Unused: []StackUnused{{SNR: 0, Inj: 4, Required: true}}}, StackRefuted, StackNoAboveControls},
		{"in a crowded field", StackFacts{Obs: 9, Peak: 9, Inj: 10, HasControl: true, Control99: 11, Unused: shows}, StackDoubtful, "controls"},
		{"no controls", StackFacts{Obs: 9, Peak: 9, Inj: 10, Unused: shows}, StackDoubtful, "could not be measured"},
		{"unused too weak", StackFacts{Obs: 9, Peak: 9, Inj: 10, HasControl: true, Control99: 3, Unused: []StackUnused{{SNR: 3.2, Inj: 20, Required: true}}}, StackDoubtful, "too weakly"},
		{"a later night flat", StackFacts{Obs: 9, Peak: 9, Inj: 10, HasControl: true, Control99: 3, Unused: append([]StackUnused{{SNR: 0.5, Inj: 8}}, shows...)}, StackDoubtful, "flat"},
		{"a later night faded", StackFacts{Obs: 9, Peak: 9, Inj: 10, HasControl: true, Control99: 3, Unused: append([]StackUnused{{SNR: 2.5, Inj: 8}}, shows...)}, StackConfirmed, ""},
		{"under the share with flat unused frames", StackFacts{Obs: 5.5, Peak: 5.5, Inj: 10, HasControl: true, Control99: 3, Unused: []StackUnused{{SNR: 0, Inj: 10, Required: true}}}, StackRefuted, ""},
		{"under the share with unused frames that show it", StackFacts{Obs: 5.5, Peak: 5.5, Inj: 10, HasControl: true, Control99: 3, Unused: shows}, StackDoubtful, "only 55%"},
	}
	for _, c := range cases {
		v, why := JudgeStack(c.f)
		if v != c.want || !strings.Contains(why, c.why) {
			t.Errorf("%s: %s %q, want %s with %q", c.name, v, why, c.want, c.why)
		}
	}
}

// TestBesideStarsKnownAnswers is 67203 (Cygnus Loop, 2025-11-10) as the
// vet of 2026-10-19 stacked it: 11.4 against a fake's 12.0 in its 7
// detection frames, every one beside a star of 3 to 7 times its light,
// and -0.5 against 9.0 in its 3 unused frames. Without frames that can
// show it away from the stars it is not clear; with ones that show it, it
// is judged as any stack.
func TestBesideStarsKnownAnswers(t *testing.T) {
	t.Parallel()
	base := TrackStack{Frames: 7, SNR: 11.4, Peak: 12.6, Injected: 12.0, InjectedLo: 11.3, InjectedHi: 13.0,
		Wrong: 290, Wrong99: 7.3, WrongMax: 8.6, Random: 618, Random99: 5.9, Beside: 7, Unused: 3, UnusedSNR: -0.5, UnusedInjected: 9.0}
	cases := []struct {
		name string
		edit func(*TrackStack)
		want StackVerdict
	}{
		{"67203", func(*TrackStack) {}, StackRefuted},
		{"67203 with no unused frames", func(ts *TrackStack) { ts.Unused, ts.UnusedSNR, ts.UnusedInjected = 0, 0, 0 }, StackUntested},
		{"67203 with unused frames too shallow", func(ts *TrackStack) { ts.UnusedInjected = 4 }, StackUntested},
		{"67203 with unused frames that show it weakly", func(ts *TrackStack) { ts.UnusedSNR = 2.5 }, StackUntested},
		{"67203 with unused frames that show it", func(ts *TrackStack) { ts.UnusedSNR = 7.9 }, StackConfirmed},
		{"67203 beside stars in only 4 of its 7 frames", func(ts *TrackStack) { ts.Beside = 4 }, StackDoubtful},
	}
	for _, c := range cases {
		ts := base
		c.edit(&ts)
		ch, v := StackCheck(&ts)
		if v != c.want {
			t.Errorf("%s: %s, want %s: %s", c.name, v, c.want, ch.Evidence)
		}
		if v == StackRefuted && !strings.Contains(ch.Evidence, "glow of those stars") {
			t.Errorf("%s: refuted for another reason: %s", c.name, ch.Evidence)
		}
	}
}
