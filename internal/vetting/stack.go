package vetting

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strings"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/discoverysource"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/ownimage"
)

// stackPerRun is how many moving objects a run stacks along their motion
// by default: each reads its night's frames again.
const stackPerRun = 12

const (
	stackSettled = "Not run: the other checks already settle it."
	stackLater   = "Not run yet: this run stacked its share of moving objects, and a later run stacks this one."
	stackUnread  = "Not measured yet. Its frames or the stars near it could not be read; a later run tries again."
	// stackPending names the stack in a report's list of what a later run
	// fills in.
	stackPending = "its frames stacked along its motion"
)

func (s *Service) stackCap() int {
	if s.StackCap > 0 {
		return s.StackCap
	}
	return stackPerRun
}

// stackMovers vets the target's moving objects with the cheaper checks
// and stacks along their motion the frames of those the checks leave real
// or unsure, up to the run's share; with all, every one asked for.
func (s *Service) stackMovers(ctx context.Context, w *objectWork, dues []Due, all bool) {
	var pick []ownimage.TrackSpot
	for _, d := range dues {
		sum := d.Summary
		if sum.Source.Kind != kindMover {
			continue
		}
		id := sum.Source.ID
		mv := s.vetMover(ctx, sum, w.byID[id], w.runs, w.tracks[id])
		w.movers[id] = mv
		if st := mv.rep.Suggestion.State; mv.final || (!all && st != app.VetReal && st != app.VetUnsure) {
			continue
		}
		t, ok := trackSpotOf(id, w.byID[id])
		if !ok {
			continue
		}
		if !all && s.stacked >= s.stackCap() {
			w.stackLater[id] = true
			continue
		}
		s.stacked++
		w.stackAsked[id] = true
		pick = append(pick, t)
	}
	if len(pick) == 0 {
		return
	}
	label := w.object + ": stacking moving objects to confirm them"
	s.Report.Set(label, 0, 0, "")
	res, err := ownimage.StackTracks(ctx, s.Data, w.object, pick, ownimage.Options{Threads: s.threads(), Progress: func(done, total int) {
		s.Report.Set(label, int64(done), int64(total), "images")
	}})
	if err != nil {
		slog.Warn("Stacking moving objects failed", "object", w.object, "error", err)
		return
	}
	for i := range res {
		w.stacks[res[i].SourceID] = &res[i]
	}
}

// moverReport is a moving object's final report, with its stack judged.
func (s *Service) moverReport(ctx context.Context, w *objectWork, sum discoverysource.Summary) *Report {
	id := sum.Source.ID
	mv := w.movers[id]
	if mv == nil {
		mv = s.vetMover(ctx, sum, w.byID[id], w.runs, w.tracks[id])
	}
	switch {
	case w.stacks[id] != nil:
		c, v := ownimage.StackCheck(w.stacks[id])
		return mv.stacked(c, v)
	case w.stackLater[id]:
		mv.rep.Pending = append(mv.rep.Pending, stackPending)
		return mv.stacked(check.Skipped(check.MoverStack, stackLater), ownimage.StackUntested)
	case w.stackAsked[id]:
		mv.rep.Pending = append(mv.rep.Pending, stackPending)
		return mv.stacked(check.Skipped(check.MoverStack, stackUnread), ownimage.StackUntested)
	}
	return mv.rep
}

// transientStack judges the spot's frames stacked on its brightest
// flagged night against a fake of the light the detection reported, the
// same frames at empty places around it, and the frames the detection did
// not use: those of the same night, which must show it when the fake
// does, and those of the nights after. With a bright star's wing fitted
// at the spot, the wing fit's light stands for the stack: 66053 and the
// nine spots of Andromeda's 2025-01-21 showed light in a plain fit and
// none in the wing fit. The unused frames, measured without the wing fit,
// then do not count: 56176 (Caldwell 14, 2026-01-10) stood 0.6 sigma above
// its far nights in the wing fit while 2026-01-13's plain fit held the
// star's wing at 2.9. When the stack holds every sub the detection
// tested, a fake that shows at discoveries.MinLocalSNR or more there may
// refute it, under ownimage.StackInjMin too: the detection claimed light
// at that hold in these same subs, and tooWeakToFind refutes under it, so
// one threshold decides both sides. A real source at the hold stands under
// stackFake of it, 1.8 sigma low, in 3.6% of nights, and must then also
// stand no higher than the empty places' 99th percentile. Confirming still
// needs ownimage.StackInjMin.
func transientStack(fc *ownimage.FluxCurve, reps map[string]reported) (check.Check, ownimage.StackVerdict) {
	if fc == nil {
		return check.Skipped(check.TransStack, "Not measured yet. The spot's own light could not be measured."), ownimage.StackUntested
	}
	p, ok := stackNight(fc, reps)
	if !ok {
		return check.Skipped(check.TransStack, "Not measured yet. None of your frames from the flagged nights could be measured at the spot."), ownimage.StackUntested
	}
	rep := reportedFor(reps, p.Filter, p.Night).on(fc, p)
	lf, ok := rep.flux(fc, p.Filter)
	if !ok || !(p.Err > 0) {
		return check.Skipped(check.TransStack, fmt.Sprintf("Not measured. The detection reported no new light in %s on %s to put a fake of in the frames.", p.Filter, p.Night)), ownimage.StackUntested
	}
	fake := lf * fc.RespOf(p.Filter)
	f := ownimage.StackFacts{Obs: p.SNR(), Peak: p.SNR(), Inj: fake / p.Err}
	if p.Empty99 > 0 {
		f.HasControl, f.Control99 = true, p.Empty99/p.Err
	}
	lead := fmt.Sprintf("Stacked over its %s of %s in %s it shows at signal to noise %.1f; a fake of the light the detection reported (magnitude %.1f) shows at %.1f in the same frames.",
		ownimage.FrameWord(p.N), p.Night, p.Filter, f.Obs, rep.mag, f.Inj) + rep.basis()
	wing, wingLight := false, 0.0
	if w := fc.Wing; w != nil && w.Measured && w.Err > 0 {
		wrep := reportedFor(reps, w.Filter, w.Night)
		if wl, wok := wrep.flux(fc, w.Filter); wok {
			wing, wingLight = true, wl*fc.RespOf(w.Filter)
			f.Obs, f.Peak, f.Inj = w.Sigma, w.Sigma, wingLight/w.Err
			lead = fmt.Sprintf("Fitted beside a star of magnitude %.1f %.1f px away with each frame's own star shape, its frames of %s in %s show it %.1f times its error above its level on far nights; a fake of the light the detection reported (magnitude %.1f) shows at %.1f.",
				w.G, w.SepPx, w.Night, w.Filter, w.Sigma, wrep.mag, f.Inj)
		}
	}
	var words []string
	if f.HasControl {
		words = append(words, fmt.Sprintf("At empty places around it the same frames give under %.1f in 99 of 100.", f.Control99))
	}
	if p.FreshErr > 0 {
		u := ownimage.StackUnused{SNR: p.FreshSNR(), Inj: fake / p.FreshErr, Required: true}
		if !wing {
			f.Unused = append(f.Unused, u)
		}
		words = append(words, fmt.Sprintf("In %s of that night the detection did not use it shows at %.1f, where the fake shows at %.1f.", ownimage.FrameWord(p.FreshN), u.SNR, u.Inj))
	}
	for _, n := range inFilter(fc, p.Filter) {
		if d := daysBetween(p.Night, n.Night); d < 1 || d > followDays || n.Thin || !(n.Err > 0) {
			continue
		}
		u := ownimage.StackUnused{SNR: n.SNR(), Inj: fake / n.Err}
		if !wing {
			f.Unused = append(f.Unused, u)
		}
		words = append(words, fmt.Sprintf("On %s it shows at %.1f, where the fake shows at %.1f.", n.Night, u.SNR, u.Inj))
	}
	ev := strings.Join(append([]string{lead}, words...), " ")
	if !wing && rep.subs <= p.N {
		f.RefuteMin = discoveries.MinLocalSNR
	}
	v, why := ownimage.JudgeStack(f)
	if v == ownimage.StackUntested && !wing {
		if c, ok := tooWeakToFind(f, p.N, rep.subs, ev); ok {
			return c, ownimage.StackRefuted
		}
	}
	if v == ownimage.StackUntested && wing {
		if c, ok := lostInWing(fc.Wing, wingLight, rep.subs, ev); ok {
			return c, ownimage.StackRefuted
		}
	}
	switch v {
	case ownimage.StackUntested:
		return check.Skipped(check.TransStack, fmt.Sprintf("Not clear. The fake shows at only %.1f, too faint for your frames to tell. ", f.Inj)+ev), v
	case ownimage.StackRefuted:
		if why == ownimage.StackNoAboveControls {
			return check.New(check.TransStack, check.Fail, ev+" It is not there: new light of the brightness reported would show far more strongly, and the spot stands no higher than the empty places around it."), v
		}
		return check.New(check.TransStack, check.Fail, ev+" It is not there: new light of the brightness reported would show far more strongly, and the frames the detection did not use show nothing clear (signal to noise under 2)."), v
	case ownimage.StackDoubtful:
		return check.New(check.TransStack, check.Fail, ev+" Not confirmed: "+why+"."), v
	case ownimage.StackConfirmed:
	}
	return check.New(check.TransStack, check.Pass, ev+" It shows as new light of the brightness reported would, above 99 of 100 empty places."), v
}

// stackNight is the flagged night the stack runs on: the peak, or, when
// the detection reported no light in the peak's filters that night, the
// peak among the nights and filters it did report light in. 17028
// (Cygnus Loop Panel 2, 2026-08-10) was found in H-a at signal to noise
// 8.1 over 6 subs, and its peak was the night's all filters, which stand
// for broadband, where it reported nothing.
func stackNight(fc *ownimage.FluxCurve, reps map[string]reported) (ownimage.FluxNight, bool) {
	has := func(n ownimage.FluxNight) bool {
		r := reportedFor(reps, n.Filter, n.Night)
		return r.ok || r.bySNR()
	}
	p, ok := peak(fc)
	if !ok || has(p) {
		return p, ok
	}
	if q, qok := peakOf(fc, has); qok {
		return q, true
	}
	return p, true
}

// tooWeakToFind refutes new light whose reported brightness and whose own
// light at the spot would both stand under discoveries.MinLocalSNR in all
// the subs the detection tested, scaled from the frames stacked by the
// square root of their counts. The detection now holds such a source;
// those found before it measured the noise around a spot came from the
// picture-wide noise, 1.5 to 3.4 times too low on M 31's bulge. 60762
// (Andromeda, 2025-10-17) was reported at 8.5 where a fake of its light
// shows at 2.6 in 15 of its 17 subs and the spot at 0.8; AT 2026acsi, a
// real nova, gives 5.8.
func tooWeakToFind(f ownimage.StackFacts, n, subs int, ev string) (check.Check, bool) {
	if n <= 0 || !(f.Inj > 0) {
		return check.Check{}, false
	}
	k := math.Sqrt(math.Max(1, float64(subs)/float64(n)))
	inj, obs := f.Inj*k, f.Obs*k
	if inj >= discoveries.MinLocalSNR || obs >= discoveries.MinLocalSNR {
		return check.Check{}, false
	}
	return check.New(check.TransStack, check.Fail, ev+fmt.Sprintf(" It is too faint to have been found: over all %d subs the detection tested, the light it reported would stand at about %.1f and the spot at %.1f, both under the %.1f against the noise around a spot that a detection needs.",
		max(subs, n), inj, obs, discoveries.MinLocalSNR)), true
}

// lostInWing refutes new light beside a bright star when, with the star
// fitted in each sub's own star shape, the spot stands under
// ownimage.WingSigma above its level on far nights and the light the
// detection reported would stand under ownimage.WingSigma too with the
// wing fitted over all the subs the detection tested: the night's own
// noise shrinks by the square root of the subs, but not how much the
// wing's light at the spot moves from one far night to the next. The
// detection's light is then the wing's. AT 2026uti, 2.8 px from a G
// 15.77 star, stood +4.9 to +18.3.
func lostInWing(w *ownimage.Wing, light float64, subs int, ev string) (check.Check, bool) {
	if w == nil || !w.Measured || !(w.Floor > 0) || !(light > 0) || w.Sigma >= ownimage.WingSigma || w.Far <= 0 {
		return check.Check{}, false
	}
	e := w.NightErr
	if w.N > 0 && subs > w.N {
		e *= math.Sqrt(float64(w.N) / float64(subs))
	}
	spread := w.Floor / math.Sqrt(1+1/float64(w.Far))
	err := math.Hypot(math.Max(e, spread), spread/math.Sqrt(float64(w.Far)))
	k := light / err
	if k >= ownimage.WingSigma {
		return check.Check{}, false
	}
	return check.New(check.TransStack, check.Fail, ev+fmt.Sprintf(" It cannot be told from the star's wing: fitted with the star over all %d subs the detection tested, the light it reported would stand at only %.1f, under the %.0f new light beside a star needs, as the wing's light at the spot moves from one far night to the next.",
		max(subs, w.N), k, ownimage.WingSigma)), true
}
