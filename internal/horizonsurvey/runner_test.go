package horizonsurvey_test

import (
	"context"
	"errors"
	"io"
	"math"
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
	return hs.NewRunner(h.cfg, hs.NewNINA(h.sim.NINAURL()), h.phd, h.clock).Run(ctx)
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
	h.cfg.Site = &hs.Site{Lat: 31.5, Lon: -99.5}
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

func TestNightRunRoutesFlipsThroughHome(t *testing.T) {
	t.Parallel()
	h := newHarness(t, sim.Options{}, hs.ModeNight, time.Date(2026, 10, 13, 6, 30, 0, 0, time.UTC))
	h.cfg.Plan.AzStep, h.cfg.Plan.AzStart = 10, 150
	h.cfg.Plan.Exclude = []hs.AzRange{{From: 215, To: 145}}
	if err := h.run(t.Context()); err != nil {
		t.Fatal(err)
	}
	m := h.sim.Mount()
	if m.Homes < 2 {
		t.Fatalf("expected a route through home across the meridian, homes %d", m.Homes)
	}
	assertHomedAfterMoving(t, h.sim)
}
