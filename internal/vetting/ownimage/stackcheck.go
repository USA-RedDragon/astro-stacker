package ownimage

import (
	"fmt"
	"math"
	"strings"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
)

// StarGlow is how a stack check refuting a track as star glow says so.
const StarGlow = "It is the glow of those stars, not a moving object"

// StackVerdict is what stacking a source's frames says of it.
type StackVerdict string

// Stack verdicts: Confirmed shows it as a real source of its brightness
// would show, Refuted finds it absent where such a source would stand
// clear, Doubtful neither, and Untested could not judge.
const (
	StackUntested  StackVerdict = "untested"
	StackConfirmed StackVerdict = "confirmed"
	StackDoubtful  StackVerdict = "doubtful"
	StackRefuted   StackVerdict = "refuted"
)

// The thresholds of a stack to confirm, set on the audits of 2026-10-17.
// Ten moving objects of 2025 and 2026 that were star residuals, hot
// pixels and diffuse light stacked to 0.08 to 0.49 of the fakes of their
// brightness put in the same frames (67203: 3.3 against 7.2), and the
// frames of the same night the detection did not use showed -1.5 to 1.4
// where the fakes showed 6.5 to 29.7. Four known asteroids of the same
// nights stacked to 0.76 to 1.0 of their fakes (58453: 87.6 against
// 87.2), above every wrong motion and random place, and their unused
// frames showed 6.4 to 26.7 where the fakes showed 9.2 to 21.7. AT
// 2026acsi (source 182), a real nova, measured 6.2 where its reported
// light alone gives 5.8.
const (
	// StackNoAboveControls is the why of a stack refuted with no unused frame
	// able to show the source: it stands no higher than empty places.
	StackNoAboveControls = "no higher than empty places"
	// StackInjMin is the least a fake must show at for a stack to judge,
	// the 5 sigma the audits asked of an unused frame.
	StackInjMin = 5.0
	// stackReal is the least share of the fake's signal to noise the stack
	// must show to confirm, stackFake the share under which it is absent
	// when the unused frames are flat too: midway between the artifacts'
	// 0.49 and the asteroids' 0.76, as 27203 and 54782 stood at 0.48 and
	// 0.49.
	stackReal = 0.6
	stackFake = 0.6
	// stackShows and stackShowsShare are the least signal to noise, and
	// share of the fake's, frames the detection did not use must show the
	// source at, and stackFlat the most they may show for it to be absent.
	stackShows      = 3.0
	stackShowsShare = 0.35
	stackFlat       = 2.0
)

// StackColourAllowance is how much fainter, in magnitudes against the
// Gaia G calibrated stars of each filter's master, a moving object may be
// in a filter it was not found in before flat frames in that filter refute
// it. Asteroids and comets shine by reflected sunlight: asteroids have V-R
// about 0.4 to 0.5 and B-V about 0.7 to 0.9, against the Sun's 0.36 and
// 0.65, BP-RP about 0.9 to 1.1. The stars that set a zero point have a
// median BP-RP of about 0.8 to 1.8, and a magnitude of BP-RP moves a blue
// filter about 1.1 magnitudes against a red one, so a reflected-light body
// is off the stars' scale by under a magnitude between any two filters,
// the worst a blue filter against a red one in a field of reddened stars.
// A comet's gas adds light in broadband (C2, CN) and takes none away; a
// track found in broadband is not tested in narrowband, which passes only
// the dust's light. A source bright in a narrowband filter and absent in
// broadband is line emission, and no line emitter moves like a body of the
// solar system. 1.5 magnitudes clears the worst colour with a margin for
// the zero points' scatter. 23765 (Cygnus Loop, 2026-09-04), found in
// H-alpha at G-scale magnitude 17.07, stacked to -0.84 in the night's 14
// red, green and blue frames along its path, where a fake of that
// magnitude by each filter's zero point showed 53.6 and one 1.5 magnitudes
// fainter about 13.5.
const StackColourAllowance = 1.5

// StackNoColour is the why of a stack refuted by frames in filters the
// source was not found in.
const StackNoColour = "flat in other filters"

// StackUnused is frames the detection did not use, stacked at the source:
// SNR what they show, Inj what a fake of its brightness shows in them.
// Required ones must show it for the stack to confirm it; the others only
// count against it.
// Colour ones are frames in filters it was not found in, Inj there a fake
// StackColourAllowance magnitudes fainter than it was found at: flat where
// that fake shows, they refute it whatever else the stack shows.
type StackUnused struct {
	SNR, Inj float64
	Required bool
	Colour   bool
}

// StackFacts are what a stack to confirm measured, in signal to noise:
// Obs the source in the frames that found it, Peak the brightest point by
// it, Inj a fake of its brightness in the same frames, and Control99 the
// 99th percentile of the controls, when HasControl.
type StackFacts struct {
	Obs, Peak, Inj float64
	Control99      float64
	HasControl     bool
	Unused         []StackUnused
}

func (u StackUnused) testable() bool { return u.Inj >= StackInjMin }

func (u StackUnused) shows() bool {
	return u.SNR >= math.Max(stackShows, stackShowsShare*u.Inj)
}

func (u StackUnused) flat() bool { return u.SNR < stackFlat }

// JudgeStack weighs a stack to confirm: refuted when frames in filters it
// was not found in are flat where a fake StackColourAllowance magnitudes
// fainter shows; otherwise confirmed when the source shows at
// stackReal of its fake or more, above the controls, and in the required
// unused frames that could show it; refuted when it shows under stackFake
// of its fake and every unused frame that could show it is flat, or, with
// no unused frame able to show it, when it does not stand above the
// controls either; doubtful otherwise, with why. The frames that found it
// then show nothing a fake of its light would and nothing empty sky does
// not: 58729 (Horsehead Nebula, 2025-11-21) stacked to 3.0 against a fake's
// 10.8 and empty places' 4.2, and 54898 (Crab Nebula, 2026-02-05), fitted
// beside a star, to 1.3 against 5.8 and 3.0. A fake too faint to judge in
// the frames that found it leaves the stack untested unless the unused
// frames can judge it: 54896 (Crab Nebula, 2026-02-05), fitted beside a
// star, stacked to 1.2 against a fake's 4.4, and the 7 frames of that night
// the detection did not use to -0.2 against 7.8.
func JudgeStack(f StackFacts) (StackVerdict, string) {
	for _, u := range f.Unused {
		if u.Colour && u.testable() && u.flat() {
			return StackRefuted, StackNoColour
		}
	}
	var testable, flat, missing int
	for _, u := range f.Unused {
		if !u.testable() {
			continue
		}
		testable++
		switch {
		case u.flat():
			flat++
		case u.Required && !u.shows():
			missing++
		}
	}
	if f.Obs < stackFake*f.Inj && testable > 0 && flat == testable {
		return StackRefuted, ""
	}
	if !(f.Inj >= StackInjMin) {
		return StackUntested, ""
	}
	if f.Obs < stackFake*f.Inj && testable == 0 && f.HasControl && !(f.Peak > f.Control99) {
		return StackRefuted, StackNoAboveControls
	}
	var why []string
	if f.Obs < stackReal*f.Inj {
		why = append(why, fmt.Sprintf("it shows at only %.0f%% of what the fake shows", 100*math.Max(f.Obs, 0)/f.Inj))
	}
	switch {
	case !f.HasControl:
		why = append(why, "the controls could not be measured")
	case !(f.Peak > f.Control99):
		why = append(why, "it does not stand above the controls")
	}
	if flat > 0 {
		why = append(why, "frames the detection did not use are flat where the fake shows")
	} else if missing > 0 {
		why = append(why, "frames the detection did not use show it too weakly")
	}
	if len(why) > 0 {
		return StackDoubtful, strings.Join(why, ", and ")
	}
	return StackConfirmed, ""
}

// StackCheck judges a moving object's frames stacked along its motion.
func StackCheck(ts *TrackStack) (check.Check, StackVerdict) {
	if ts == nil {
		return check.Skipped(check.MoverStack, "Not run: the other checks already settle it."), StackUntested
	}
	if c, ok := colourCheck(ts); ok {
		return c, StackRefuted
	}
	if ts.Note != "" {
		return check.Skipped(check.MoverStack, "Not measured. "+ts.Note), StackUntested
	}
	if ts.Glow {
		return glowCheck(ts)
	}
	if besideStars(ts) {
		return besideCheck(ts)
	}
	f := StackFacts{Obs: ts.SNR, Peak: ts.Peak, Inj: ts.Injected}
	if ts.Wrong > 0 || ts.Random > 0 {
		f.HasControl = true
		f.Control99 = math.Max(ts.Wrong99, ts.Random99)
	}
	if ts.Unused > 0 {
		f.Unused = []StackUnused{{SNR: ts.UnusedSNR, Inj: ts.UnusedInjected, Required: true}}
	}
	if ts.Other > 0 {
		f.Unused = append(f.Unused, colourUnused(ts))
	}
	v, why := JudgeStack(f)
	ev := stackWords(ts)
	switch v {
	case StackUntested:
		return check.Skipped(check.MoverStack, fmt.Sprintf("Not clear. A fake of its brightness shows at only %.1f in its frames, too faint for the stack to tell. ", ts.Injected)+ev), v
	case StackRefuted:
		if why == StackNoAboveControls {
			return check.New(check.MoverStack, check.Fail, ev+" It is not there: a real object of the brightness it was found at would show far more strongly, and it stands no higher than the controls."), v
		}
		return check.New(check.MoverStack, check.Fail, ev+" It is not there: a real object of the brightness it was found at would show far more strongly, and the frames it was not found in show nothing clear (signal to noise under 2)."), v
	case StackDoubtful:
		return check.New(check.MoverStack, check.Fail, ev+" Not confirmed: "+why+"."), v
	case StackConfirmed:
	}
	return check.New(check.MoverStack, check.Pass, ev+" It shows as a real object of its brightness would, above 99 of 100 controls."), v
}

func stackWords(ts *TrackStack) string {
	fake := "a fake of the same brightness"
	if ts.Mag > 0 {
		fake = fmt.Sprintf("a fake of the same brightness (magnitude %.1f)", ts.Mag)
	}
	ev := fmt.Sprintf("Stacked along its motion over its %s it shows at signal to noise %.1f; %s shows at %.1f in the same frames (%.1f to %.1f).",
		FrameWord(ts.Frames), ts.SNR, fake, ts.Injected, ts.InjectedLo, ts.InjectedHi)
	if ts.Wrong > 0 {
		ev += fmt.Sprintf(" Stacked along %d wrong motions, the brightest point by its path is under %.1f in 99 of 100 (%.1f at most); its own is %.1f.", ts.Wrong, ts.Wrong99, ts.WrongMax, ts.Peak)
	}
	if ts.Random > 0 {
		ev += fmt.Sprintf(" At %d random places stacked along its motion it is under %.1f in 99 of 100.", ts.Random, ts.Random99)
	}
	if ts.Unused > 0 {
		ev += fmt.Sprintf(" In %s of the same night the detection did not use it shows at %.1f, where the fake shows at %.1f.", FrameWord(ts.Unused), ts.UnusedSNR, ts.UnusedInjected)
	} else {
		ev += " No frames of the same night that the detection did not use cross its path."
	}
	if ts.Other > 0 {
		ev += " " + colourWords(ts)
	}
	return ev
}

func colourUnused(ts *TrackStack) StackUnused {
	return StackUnused{SNR: ts.OtherSNR, Inj: ts.OtherFaint, Colour: true}
}

func colourWords(ts *TrackStack) string {
	return fmt.Sprintf("In the %s of the same night in %s, filters it was not found in, that cross its path it shows at %.1f, where a fake of its magnitude, given each filter's light by that filter's stars, shows at %.1f, and one %.1f magnitudes fainter at %.1f.",
		FrameWord(ts.Other), joinAnd(ts.OtherFilters), ts.OtherSNR, ts.OtherInjected, StackColourAllowance, ts.OtherFaint)
}

// colourCheck refutes a track whose frames in filters it was not found in
// are flat where a fake StackColourAllowance magnitudes fainter than it
// was found at shows.
func colourCheck(ts *TrackStack) (check.Check, bool) {
	if ts.Other == 0 {
		return check.Check{}, false
	}
	if v, why := JudgeStack(StackFacts{Unused: []StackUnused{colourUnused(ts)}}); v != StackRefuted || why != StackNoColour {
		return check.Check{}, false
	}
	ev := colourWords(ts)
	if ts.Note == "" && !ts.Glow && ts.Frames > 0 && ts.Injected > 0 {
		ev = stackWords(ts)
	}
	return check.New(check.MoverStack, check.Fail, ev+fmt.Sprintf(" It is not there: those frames show nothing clear (signal to noise under 2), and a moving object shines by reflected sunlight, which no asteroid or comet colour makes %.1f magnitudes fainter there than in the filter it was found in.", StackColourAllowance)), true
}

// glowCheck judges a track whose path lies on stars the masters show in
// most of its detection frames: star glow, as the movers audited on
// 2026-10-17 mostly were, unless frames the detection did not use show it
// off the stars.
func glowCheck(ts *TrackStack) (check.Check, StackVerdict) {
	ev := fmt.Sprintf("In %d of its %s its path lies on stars your masters show, so it could not be stacked there.", ts.OnStars, FrameWord(ts.Frames))
	u := StackUnused{SNR: ts.UnusedSNR, Inj: ts.UnusedInjected}
	if ts.Unused > 0 {
		ev += fmt.Sprintf(" In %s of the same night the detection did not use it shows at %.1f, where a fake of its brightness shows at %.1f.", FrameWord(ts.Unused), u.SNR, u.Inj)
		if !u.flat() {
			return check.Skipped(check.MoverStack, "Not clear. "+ev+" Away from the stars it may be there."), StackUntested
		}
	} else {
		ev += " No frames of the same night that the detection did not use cross its path."
	}
	if ts.Unused > 0 && u.testable() {
		return check.New(check.MoverStack, check.Fail, ev+" "+StarGlow+": away from them, where a real object would stand clear, it is not there."), StackRefuted
	}
	return check.New(check.MoverStack, check.Fail, ev+" "+StarGlow+": its detections sit on stars in most of its frames."), StackRefuted
}

// besideStars says the path passes beside stars in most of the stacked
// detection frames, and the frames the detection did not use cannot show
// it there: the stack's light may then be the stars' leftover light alone.
func besideStars(ts *TrackStack) bool {
	if float64(ts.Beside) < math.Max(stackMinFrames, stackGlowShare*float64(ts.Frames)) {
		return false
	}
	u := StackUnused{SNR: ts.UnusedSNR, Inj: ts.UnusedInjected}
	return ts.Unused == 0 || !u.testable() || !u.shows()
}

// besideCheck judges a track beside stars: star glow when the frames the
// detection did not use could show it away from them and are flat, and
// not clear otherwise.
func besideCheck(ts *TrackStack) (check.Check, StackVerdict) {
	ev := fmt.Sprintf("In %d of its %s its path passes within %.0f star widths of a star your masters show at %.0f times its light or more, whose leftover light a stack there cannot tell from it.",
		ts.Beside, FrameWord(ts.Frames), stackBesideWidths, stackBesideLight)
	u := StackUnused{SNR: ts.UnusedSNR, Inj: ts.UnusedInjected}
	if ts.Unused == 0 || !u.testable() {
		if ts.Unused > 0 {
			ev += fmt.Sprintf(" In %s of the same night the detection did not use, a fake of its brightness shows at only %.1f, too faint to tell.", FrameWord(ts.Unused), u.Inj)
		} else {
			ev += " No frames of the same night that the detection did not use cross its path."
		}
		return check.Skipped(check.MoverStack, "Not clear. "+ev), StackUntested
	}
	ev += fmt.Sprintf(" In %s of the same night the detection did not use it shows at %.1f, where a fake of its brightness shows at %.1f.", FrameWord(ts.Unused), u.SNR, u.Inj)
	if !u.flat() {
		return check.Skipped(check.MoverStack, "Not clear. "+ev+" Away from the stars it may be there."), StackUntested
	}
	return check.New(check.MoverStack, check.Fail, ev+" "+StarGlow+": away from them, where a real object would stand clear, it is not there."), StackRefuted
}
