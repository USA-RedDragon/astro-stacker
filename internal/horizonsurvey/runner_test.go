package horizonsurvey_test

import (
	"context"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	hs "github.com/USA-RedDragon/astro-stacker/internal/horizonsurvey"
	"github.com/USA-RedDragon/astro-stacker/internal/horizonsurvey/sim"
)

type harness struct {
	sim   *sim.Sim
	clock *sim.Clock
	cfg   hs.RunConfig
	phd   *hs.PHD2
}

func newHarness(t *testing.T, opts sim.Options, mode string, at time.Time) *harness {
	t.Helper()
	if opts.Camera.W == 0 {
		opts.Camera = smallCamera(sim.DuskCamera())
		if mode == hs.ModeNight {
			opts.Camera = smallCamera(sim.NightCamera())
		}
	}
	opts.Site = testSite()
	opts.Clock = sim.NewClock(at)
	s, err := sim.Start(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	phd, err := hs.DialPHD2(t.Context(), s.PHD2Addr())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = phd.Close() })
	pc := testPlanConfig()
	pc.AzStep = 30
	pc.AzStart = 5
	site := testSite()
	return &harness{sim: s, clock: opts.Clock, phd: phd, cfg: hs.RunConfig{
		Out: t.TempDir(), Mode: mode, Plan: pc, Site: &site, Safety: hs.DefaultSafety(), Settle: 2 * time.Second, MaxFrames: 400, Log: io.Discard,
	}}
}

func (h *harness) run(ctx context.Context) error {
	nina := hs.NewNINA(h.sim.NINAURL())
	nina.TrackingWait = 5 * time.Millisecond
	return hs.NewRunner(h.cfg, nina, h.phd, h.clock).Run(ctx)
}

func assertAllowlisted(t *testing.T, s *sim.Sim) {
	t.Helper()
	for _, c := range s.Calls() {
		if !hs.NINAAllowed(c) || strings.Contains(c, "switch") || strings.Contains(c, "flatdevice") {
			t.Fatalf("called NINA endpoint %s", c)
		}
	}
	for _, c := range s.PHD2Calls() {
		if !hs.PHD2Allowed(c) {
			t.Fatalf("called PHD2 method %s", c)
		}
	}
}

func assertHomedAfterMoving(t *testing.T, s *sim.Sim) {
	t.Helper()
	m := s.Mount()
	if m.Slews == 0 {
		t.Fatal("the run never moved the mount")
	}
	if m.Homes == 0 || !m.AtHome {
		t.Fatalf("the mount was not sent home (homes %d, at home %v)", m.Homes, m.AtHome)
	}
	if m.Tracking {
		t.Fatal("tracking was left on")
	}
}

func TestDuskSurveyEndToEnd(t *testing.T) {
	t.Parallel()
	h := newHarness(t, sim.Options{}, hs.ModeDusk, duskTime())
	if err := h.run(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertHomedAfterMoving(t, h.sim)
	assertAllowlisted(t, h.sim)
	m := h.sim.Mount()
	if m.TrackOffs < m.Slews {
		t.Fatalf("tracking was turned off %d times for %d slews", m.TrackOffs, m.Slews)
	}
	cal := h.sim.Camera().Calibration()
	res, err := hs.Analyze(hs.AnalyzeOptions{Dirs: []string{h.cfg.Out}, Calibration: &cal})
	if err != nil {
		t.Fatal(err)
	}
	worst, edges, at := checkSkyline(res.Skyline, h.sim.Camera(), sim.DemoSkyline)
	t.Logf("%d frames, %d edge bins, worst error %.3f deg at az %.2f, coverage %.0f%%", len(res.Frames), edges, worst, at, 100*res.Skyline.Coverage())
	if edges < 20 || worst > 0.5 {
		t.Fatalf("%d edge bins, worst error %.3f at az %.2f", edges, worst, at)
	}
	for _, f := range res.Frames {
		if f.Record.SunSep < 45 {
			t.Fatalf("frame %s was taken %.1f deg from the Sun", f.Record.File, f.Record.SunSep)
		}
		if f.Record.ExposureMS < 1 || f.Record.SkyLevel > 0.92 {
			t.Fatalf("frame %s exposure %d ms sky level %.2f", f.Record.File, f.Record.ExposureMS, f.Record.SkyLevel)
		}
	}
}

func TestNightSurveyUsesPlateSolvesOverMountPointing(t *testing.T) {
	t.Parallel()
	h := newHarness(t, sim.Options{Solve: true, PointingError: 0.8}, hs.ModeNight, nightTime())
	if err := h.run(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertHomedAfterMoving(t, h.sim)
	res, err := hs.Analyze(hs.AnalyzeOptions{Dirs: []string{h.cfg.Out}})
	if err != nil {
		t.Fatal(err)
	}
	worst, edges, at := checkSkyline(res.Skyline, h.sim.Camera(), sim.DemoSkyline)
	t.Logf("%d frames, %d edge bins, worst error %.3f deg at az %.2f", len(res.Frames), edges, worst, at)
	if edges < 20 || worst > 0.5 {
		t.Fatalf("%d edge bins, worst error %.3f at az %.2f", edges, worst, at)
	}
	c := res.Calibration
	want := h.sim.Camera().Calibration()
	if c == nil {
		t.Fatal("no calibration was derived from the plate solves")
	}
	th, _ := want.ThetaFor(c.PierSide)
	if math.Abs(math.Mod(c.Theta-th+540, 360)-180) > 0.2 || c.Parity != want.Parity || math.Abs(c.Scale-want.Scale) > 0.1 {
		t.Fatalf("derived calibration %+v, want theta %v parity %d scale %v", *c, th, want.Parity, want.Scale)
	}
}

func TestAbortsHomeTheMount(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		opts sim.Options
		want error
	}{
		{"slew fails", sim.Options{FailSlewAt: 4}, hs.ErrSlewFailed},
		{"roof becomes unsafe", sim.Options{UnsafeAfter: 5}, hs.ErrRoofUnsafe},
		{"mount reports the wrong place", sim.Options{ReportOffset: 1}, hs.ErrPointing},
		{"mount reports an unknown pier side", sim.Options{ForcePier: hs.PierUnknown}, hs.ErrPier},
		{"mount picks an unchecked pier side", sim.Options{ForcePier: hs.PierEast}, hs.ErrPier},
		{"sequence starts during the run", sim.Options{SequenceAfter: 3}, hs.ErrSequence},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, c.opts, hs.ModeDusk, duskTime())
			err := h.run(t.Context())
			if !errors.Is(err, c.want) {
				t.Fatalf("got %v, want %v", err, c.want)
			}
			assertHomedAfterMoving(t, h.sim)
			assertAllowlisted(t, h.sim)
			if h.sim.Mount().Stops == 0 {
				t.Fatal("the abort did not stop the slew")
			}
		})
	}
}

func TestCancelHomesTheMount(t *testing.T) {
	t.Parallel()
	h := newHarness(t, sim.Options{}, hs.ModeDusk, duskTime())
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		for h.sim.Mount().Slews < 3 {
			time.Sleep(time.Millisecond)
		}
		cancel()
	}()
	if err := h.run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
	assertHomedAfterMoving(t, h.sim)
}

func TestPreflightRefusesWithoutMoving(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		opts sim.Options
		at   time.Time
		want error
	}{
		{"sequence running", sim.Options{SequenceBusy: true}, duskTime(), hs.ErrSequence},
		{"guiding", sim.Options{PHD2State: "Guiding"}, duskTime(), hs.ErrPHD2Busy},
		{"no safety monitor", sim.Options{SafetyOff: true}, duskTime(), hs.ErrRoofUnsafe},
		{"daylight", sim.Options{}, time.Date(2026, 10, 12, 21, 0, 0, 0, time.UTC), hs.ErrSunTooHigh},
		{"Sun on the horizon", sim.Options{}, time.Date(2026, 10, 13, 0, 5, 0, 0, time.UTC), hs.ErrSunTooHigh},
		{"unknown pixel scale", sim.Options{NoPixelScale: true}, duskTime(), hs.ErrNoPixelScale},
		{"mount clock is off", sim.Options{ClockSkew: 1}, duskTime(), hs.ErrSite},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, c.opts, hs.ModeAuto, c.at)
			err := h.run(t.Context())
			if !errors.Is(err, c.want) || !errors.Is(err, hs.ErrPreflight) {
				t.Fatalf("got %v, want a preflight %v", err, c.want)
			}
			m := h.sim.Mount()
			if m.Slews != 0 || m.Homes != 0 || m.TrackOffs != 0 || m.Stops != 0 {
				t.Fatalf("preflight failure moved or commanded the mount: %+v", m)
			}
			assertAllowlisted(t, h.sim)
		})
	}
}

func TestClientsRefuseUnlistedEndpoints(t *testing.T) {
	t.Parallel()
	for _, p := range []string{"/equipment/switch/set", "/equipment/flatdevice/set-cover", "/equipment/flatdevice/set-light", "/equipment/mount/park", "/equipment/mount/sync", "/sequence/start"} {
		if hs.NINAAllowed(p) {
			t.Fatalf("%s is allowlisted", p)
		}
	}
	for _, m := range []string{"guide", "dither", "set_connected", "set_exposure", "loop", "set_paused"} {
		if hs.PHD2Allowed(m) {
			t.Fatalf("PHD2 %s is allowlisted", m)
		}
	}
	s, err := sim.Start(t.Context(), sim.Options{Site: testSite(), Camera: smallCamera(sim.DuskCamera()), Clock: sim.NewClock(duskTime())})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	phd, err := hs.DialPHD2(t.Context(), s.PHD2Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = phd.Close() }()
	if err := phd.Call(t.Context(), "dither", nil, nil); !errors.Is(err, hs.ErrNotAllowed) {
		t.Fatalf("dither got %v", err)
	}
	if len(s.PHD2Calls()) != 0 {
		t.Fatalf("a refused PHD2 call reached the server: %v", s.PHD2Calls())
	}
}

func TestSiteMismatchRefuses(t *testing.T) {
	t.Parallel()
	h := newHarness(t, sim.Options{}, hs.ModeDusk, duskTime())
	h.cfg.Site = &hs.Site{Lat: 35.0, Lon: -110.0}
	if err := h.run(t.Context()); !errors.Is(err, hs.ErrSite) || !errors.Is(err, hs.ErrPreflight) {
		t.Fatalf("got %v, want a preflight ErrSite", err)
	}
	if m := h.sim.Mount(); m.Slews != 0 || m.Homes != 0 {
		t.Fatalf("site mismatch moved the mount: %+v", m)
	}
}

func TestTrackingLeftOnAfterHomeIsAnError(t *testing.T) {
	t.Parallel()
	h := newHarness(t, sim.Options{TrackingStuck: true}, hs.ModeDusk, duskTime())
	h.cfg.MaxFrames = 2
	err := h.run(t.Context())
	if !errors.Is(err, hs.ErrHomeFailed) {
		t.Fatalf("got %v, want ErrHomeFailed for tracking still on", err)
	}
}

func TestTrackingOffRetriesWhileNINASettles(t *testing.T) {
	t.Parallel()
	h := newHarness(t, sim.Options{TrackingFailures: 3}, hs.ModeDusk, duskTime())
	h.cfg.MaxFrames = 3
	err := h.run(t.Context())
	if !errors.Is(err, hs.ErrFrameLimit) {
		t.Fatalf("got %v, want the frame limit after retried tracking-off calls", err)
	}
	m := h.sim.Mount()
	if m.TrackFails < 3 || m.Tracking {
		t.Fatalf("tracking-off was not retried through the settling errors: %+v", m)
	}
	assertHomedAfterMoving(t, h.sim)
}

func TestTrackingOffGivesUpAfterBoundedRetries(t *testing.T) {
	t.Parallel()
	h := newHarness(t, sim.Options{TrackingFailures: 1000}, hs.ModeDusk, duskTime())
	err := h.run(t.Context())
	if !errors.Is(err, hs.ErrNINA) {
		t.Fatalf("got %v, want the NINA error after the retries", err)
	}
	if m := h.sim.Mount(); m.TrackFails < hs.TrackingOffTries || m.Homes == 0 {
		t.Fatalf("tracking-off retried %d times, homes %d", m.TrackFails, m.Homes)
	}
}

func TestCaptureSavesThroughSaveImageWithoutAnEvent(t *testing.T) {
	t.Parallel()
	saveDir := t.TempDir()
	s, err := sim.Start(t.Context(), sim.Options{Site: testSite(), Camera: smallCamera(sim.DuskCamera()), Clock: sim.NewClock(duskTime()), NoFrameEvent: true, SaveDir: saveDir})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	phd, err := hs.DialPHD2(t.Context(), s.PHD2Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = phd.Close() }()
	path := filepath.Join(t.TempDir(), "frame.fits")
	if err := phd.Capture(t.Context(), 5, path); err != nil {
		t.Fatal(err)
	}
	if _, _, err := hs.LoadFrame(path); err != nil {
		t.Fatalf("the saved frame does not load: %v", err)
	}
	saved := s.SavedImages()
	if len(saved) != 1 {
		t.Fatalf("save_image was called %d times, want 1", len(saved))
	}
	if _, err := os.Stat(saved[0]); !os.IsNotExist(err) {
		t.Fatalf("PHD2's own copy %s was left behind (%v)", saved[0], err)
	}
}

func TestNightLampsDoNotShortenTheExposure(t *testing.T) {
	t.Parallel()
	cam := smallCamera(sim.NightCamera())
	cam.Lamps = []sim.Lamp{{Az: 5, Alt: 4.5, Radius: 0.6, Rate: 0.01}}
	h := newHarness(t, sim.Options{Camera: cam}, hs.ModeNight, deepNight())
	h.cfg.Plan.AzStep, h.cfg.Plan.AzStart, h.cfg.Full = 90, 5, true
	if err := h.run(t.Context()); err != nil {
		t.Fatal(err)
	}
	_, frames, err := hs.ReadRun(h.cfg.Out)
	if err != nil {
		t.Fatal(err)
	}
	lit := 0
	for _, f := range frames {
		if f.ExposureMS != 2000 {
			t.Fatalf("%s was taken at %d ms; lamps must not shorten night exposures", f.File, f.ExposureMS)
		}
		if f.TargetAz == 5 && f.TargetAlt < 6.2 {
			lit++
			if f.Class == hs.ClassSky {
				t.Fatalf("%s with a lamp in the foreground came back as sky", f.File)
			}
		}
	}
	if lit == 0 {
		t.Fatal("no frame looked at the lamp")
	}
}

func pierChanges(t *testing.T, dir string) (int, map[int]map[string]bool, map[int]int) {
	t.Helper()
	_, frames, err := hs.ReadRun(dir)
	if err != nil {
		t.Fatal(err)
	}
	changes := 0
	sides := map[int]map[string]bool{}
	counts := map[int]int{}
	prev := ""
	for _, f := range frames {
		if sides[f.Column] == nil {
			sides[f.Column] = map[string]bool{}
		}
		sides[f.Column][f.Mount.PierSide] = true
		counts[f.Column]++
		if prev != "" && f.Mount.PierSide != prev {
			changes++
		}
		prev = f.Mount.PierSide
		if f.PlannedPier != f.Mount.PierSide {
			t.Fatalf("%s was planned on %s and taken on %s", f.File, f.PlannedPier, f.Mount.PierSide)
		}
	}
	return changes, sides, counts
}

func TestNightRunFlipsOnceAcrossTheMeridian(t *testing.T) {
	t.Parallel()
	h := newHarness(t, sim.Options{}, hs.ModeNight, time.Date(2026, 10, 13, 6, 30, 0, 0, time.UTC))
	h.cfg.Plan.AzStep, h.cfg.Plan.AzStart = 10, 150
	h.cfg.Plan.Exclude = []hs.AzRange{{From: 215, To: 145}}
	if err := h.run(t.Context()); err != nil {
		t.Fatal(err)
	}
	changes, sides, _ := pierChanges(t, h.cfg.Out)
	if changes != 1 {
		t.Fatalf("the mount changed pier side %d times, want 1", changes)
	}
	for c, s := range sides {
		if len(s) != 1 {
			t.Fatalf("column %d used both pier sides", c)
		}
	}
	assertHomedAfterMoving(t, h.sim)
}

func TestNightRunKeepsOnePierSidePerColumn(t *testing.T) {
	t.Parallel()
	h := newHarness(t, sim.Options{}, hs.ModeNight, deepNight())
	h.cfg.Plan.AzStep, h.cfg.Plan.AzStart = 30, 0
	if err := h.run(t.Context()); err != nil {
		t.Fatal(err)
	}
	changes, sides, counts := pierChanges(t, h.cfg.Out)
	if changes != 1 {
		t.Fatalf("the mount changed pier side %d times over the night, want 1", changes)
	}
	for c, s := range sides {
		if len(s) != 1 {
			t.Fatalf("column %d used both pier sides", c)
		}
	}
	if m := h.sim.Mount(); m.Homes != 1 {
		t.Fatalf("the mount went home %d times; slews between frames should never route through home", m.Homes)
	}
	later := 0
	for c, n := range counts {
		if c > 0 {
			later += n
		}
	}
	if avg := float64(later) / float64(len(counts)-1); avg > 6 {
		t.Fatalf("columns after the first took %.1f frames on average; they should start at the previous edge", avg)
	}
	t.Logf("%d frames in the first column, %d in the other %d columns", counts[0], later, len(counts)-1)
	cal := h.sim.Camera().Calibration()
	res, err := hs.Analyze(hs.AnalyzeOptions{Dirs: []string{h.cfg.Out}, Calibration: &cal})
	if err != nil {
		t.Fatal(err)
	}
	if worst, edges, at := checkSkyline(res.Skyline, h.sim.Camera(), sim.DemoSkyline); edges < 20 || worst > 0.5 {
		t.Fatalf("%d edge bins, worst error %.3f at az %.2f", edges, worst, at)
	}
}

func TestColumnFollowsTheMountsPierChoice(t *testing.T) {
	t.Parallel()
	h := newHarness(t, sim.Options{ForcePier: hs.PierEast}, hs.ModeNight, deepNight())
	h.cfg.Plan.AzStep, h.cfg.Plan.AzStart = 10, 0
	h.cfg.Plan.Exclude = []hs.AzRange{{From: 5, To: 355}}
	if err := h.run(t.Context()); err != nil {
		t.Fatal(err)
	}
	_, frames, err := hs.ReadRun(h.cfg.Out)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) < 2 {
		t.Fatalf("only %d frames", len(frames))
	}
	for i, f := range frames {
		if f.Mount.PierSide != hs.PierEast {
			t.Fatalf("%s was taken on %s", f.File, f.Mount.PierSide)
		}
		if i > 0 && f.PlannedPier != hs.PierEast {
			t.Fatalf("%s was still planned on %s after the mount chose pierEast", f.File, f.PlannedPier)
		}
	}
}
