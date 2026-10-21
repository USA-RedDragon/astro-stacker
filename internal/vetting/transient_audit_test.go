package vetting

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/discoverysource"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/ownimage"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/surveys"
)

// zNight is a night of n subs in filter at signal to noise z on an error
// of 10, zero point 25.
func zNight(night, filter string, z float64, n int, flagged bool) ownimage.FluxNight {
	const err = 10.0
	fn := ownimage.FluxNight{Night: night, Filter: filter, N: n, Flux: z * err, Err: err, Local: err, Flagged: flagged, Thin: n < 3, Limit: 25 - 2.5*math.Log10(3*err)}
	if z >= 3 {
		fn.Mag = 25 - 2.5*math.Log10(fn.Flux)
	}
	return fn
}

const ha = "H-a"

func curveOf(nights ...ownimage.FluxNight) *ownimage.FluxCurve {
	return &ownimage.FluxCurve{Comps: 9, Zero: map[string]float64{lum: 25, "Red": 25, "Green": 25, "Blue": 25, ha: 25}, Nights: nights}
}

// lightAt is the reported light that gives signal to noise z on an error
// of 10 at zero point 25.
func lightAt(z float64, night string) map[string]reported {
	return map[string]reported{discoveries.GroupBroadband + "\x00" + night: {mag: 25 - 2.5*math.Log10(z*10), ok: true}}
}

func TestReportedLightIsInjected(t *testing.T) {
	t.Parallel()
	// AT 2026acsi (source 182), a real nova: 6.2 at the spot, 12.0 with its
	// reported light added.
	c := fluxCheck(curveOf(zNight("2026-10-11", lum, 6.2, 12, true)), lightAt(5.8, "2026-10-11"))
	if c.Status != check.Pass {
		t.Errorf("AT 2026acsi: %s %s", c.Status, c.Evidence)
	}
	// 60465 in M 31's bulge: 0.4, and 7.0 with the light reported.
	c = fluxCheck(curveOf(zNight("2025-10-26", lum, 0.4, 12, true)), lightAt(6.6, "2025-10-26"))
	if c.Status != check.Fail || !strings.Contains(c.Evidence, "would show at 7") {
		t.Errorf("60465: %s %s", c.Status, c.Evidence)
	}
	// 36597: 2.6 against 7.0, not ruled out by itself.
	if c := fluxCheck(curveOf(zNight("2025-10-17", lum, 2.6, 12, true)), lightAt(4.4, "2025-10-17")); c.Status != check.NotRun {
		t.Errorf("36597: %s %s", c.Status, c.Evidence)
	}
	// 60670: 2.0 against 4.4: the light reported is too faint to rule out.
	if c := fluxCheck(curveOf(zNight("2025-10-15", lum, 1.9, 12, true)), lightAt(2.4, "2025-10-15")); c.Status != check.NotRun || !strings.Contains(c.Evidence, "too faint") {
		t.Errorf("60670: %s %s", c.Status, c.Evidence)
	}
}

func TestGoneTheNextNight(t *testing.T) {
	t.Parallel()
	// 17751: 3.4 on 2026-07-19, then -0.8 on 07-20 where its reported light
	// would add 3.5.
	fc := curveOf(zNight("2026-07-18", lum, 0.3, 8, false), zNight("2026-07-19", lum, 3.4, 10, true), zNight("2026-07-20", lum, -0.8, 9, false))
	if c := nextCheck(fc, lightAt(3.5, "2026-07-19")); c.Status != check.Fail {
		t.Errorf("17751: %s %s", c.Status, c.Evidence)
	}
	s := transientSuggest(transientGroups(map[string]check.Status{check.TransFlux: check.NotRun, check.TransNext: check.Fail}), transientFacts{})
	if s.State != app.VetArtifact || s.Say != "Artifact: gone the next night" {
		t.Errorf("suggestion %+v", s)
	}
	// A nova caught rising: nothing the night before, still there after.
	fc = curveOf(zNight("2025-11-10", lum, 0.1, 8, false), zNight("2025-11-11", lum, 7, 10, true), zNight("2025-11-12", lum, 9, 10, false))
	if c := nextCheck(fc, lightAt(7, "2025-11-11")); c.Status != check.Pass {
		t.Errorf("rising nova: %s %s", c.Status, c.Evidence)
	}
	// Two frames the next night do not judge it.
	fc = curveOf(zNight("2025-11-11", lum, 7, 10, true), zNight("2025-11-12", lum, 0, 2, false))
	if c := nextCheck(fc, lightAt(7, "2025-11-11")); c.Status != check.NotRun {
		t.Errorf("thin next night: %s %s", c.Status, c.Evidence)
	}
	// AT 2025adss: a faint first sighting on 2025-10-15 with nothing on the
	// next nights, and its peak on 2025-11-21 still there the night after.
	fc = curveOf(zNight("2025-10-15", lum, 4, 8, true), zNight("2025-10-17", lum, 0.2, 8, false), zNight("2025-11-21", lum, 20, 12, true), zNight("2025-11-22", lum, 15, 10, false))
	reps := map[string]reported{}
	for k, v := range lightAt(4, "2025-10-15") {
		reps[k] = v
	}
	for k, v := range lightAt(20, "2025-11-21") {
		reps[k] = v
	}
	if c := nextCheck(fc, reps); c.Status != check.Pass {
		t.Errorf("AT 2025adss: %s %s", c.Status, c.Evidence)
	}
}

func TestThinNightsNeitherConfirmNorSetTheTrend(t *testing.T) {
	t.Parallel()
	// 66053: light on 2025-01-21, then the skipped run of 01-23 with one
	// Green and one Blue sub read as 17.6, and a night a season later that
	// made the text say "brightened 0.5 mag".
	fc := curveOf(zNight(wingNight, lum, 6, 12, true), zNight("2025-01-22", lum, 1, 10, false), zNight("2025-01-23", lum, 9, 2, false))
	c := nightsCheckTransient(fc)
	if c.Status != check.NotRun || !strings.Contains(c.Evidence, "from 2 frames only") {
		t.Errorf("66053: %s %s", c.Status, c.Evidence)
	}
	fc.Nights = append(fc.Nights, zNight("2025-01-26", lum, 5.5, 10, false), zNight("2025-09-30", lum, 20, 10, false))
	c = nightsCheckTransient(fc)
	if c.Status != check.Pass || strings.Contains(c.Evidence, "brightened") {
		t.Errorf("a night a season later set the trend: %s %s", c.Status, c.Evidence)
	}
}

func TestOwnFramesAloneNeedFreshNights(t *testing.T) {
	t.Parallel()
	night := func(d string, fresh float64, n int) ownimage.FluxNight {
		fn := zNight(d, lum, 6, 10, d == "2025-11-11")
		fn.FreshN, fn.FreshFlux, fn.FreshErr = n, fresh*10, 10
		return fn
	}
	if c := freshCheck(curveOf(night("2025-11-11", 0, 0), night("2025-11-12", 6, 10))); c.Status != check.NotRun {
		t.Errorf("one fresh night: %s %s", c.Status, c.Evidence)
	}
	if c := freshCheck(curveOf(night("2025-11-11", 0, 0), night("2025-11-12", 6, 10), night("2025-11-14", 5.2, 8))); c.Status != check.Pass {
		t.Errorf("two fresh nights: %s %s", c.Status, c.Evidence)
	}
	st := ownOnly()
	st[check.TransFresh] = check.NotRun
	if s := transientSuggest(transientGroups(st), transientFacts{}); s.State != app.VetUnsure {
		t.Errorf("one night of the detection's own frames: %+v", s)
	}
}

func TestLuminanceShowsWhatTheColourFramesShow(t *testing.T) {
	t.Parallel()
	const d = "2025-11-02"
	// 39478: -0.8 in Luminance, 3.9 in Blue.
	fc := curveOf(zNight(d, lum, -0.8, 10, true), zNight(d, "Blue", 3.9, 5, true), zNight(d, "Red", 0.5, 5, true))
	if c := colorCheck(fc); c.Status != check.Fail {
		t.Errorf("39478: %s %s", c.Status, c.Evidence)
	}
	// The faint night before AT 2025adss was found: 2.5 against 5.4.
	if c := colorCheck(curveOf(zNight(d, lum, 2.5, 10, true), zNight(d, "Green", 5.4, 5, true))); c.Status != check.Pass {
		t.Errorf("AT 2025adss: %s %s", c.Status, c.Evidence)
	}
	// SN 2026bhf: 48 against 31.
	if c := colorCheck(curveOf(zNight(d, lum, 48, 10, true), zNight(d, "Red", 31, 5, true))); c.Status != check.Pass {
		t.Errorf("SN 2026bhf: %s %s", c.Status, c.Evidence)
	}
}

func TestNarrowbandSupernovaNeedsBroadbandLight(t *testing.T) {
	t.Parallel()
	const d = "2026-02-23"
	fc := curveOf(zNight(d, ha, 7.6, 10, true), zNight(d, "Red", 0.2, 4, true), zNight(d, "Green", 0.8, 4, true), zNight(d, "Blue", -0.5, 4, true))
	if c := broadCheck(fc, []string{ha}); c.Status != check.Fail {
		t.Errorf("H-a only: %s %s", c.Status, c.Evidence)
	}
	if c := broadCheck(fc, []string{ha, discoveries.GroupBroadband}); c.Status != check.Pass {
		t.Errorf("found in broadband: %s %s", c.Status, c.Evidence)
	}
}

func TestGalaxyNucleusResidual(t *testing.T) {
	t.Parallel()
	// 54004: H-a 7.6 at the spot, 6.4 px (12") from NGC 4313's nucleus,
	// up to 7.0 on the same circle.
	fc := curveOf(zNight("2026-02-23", ha, 7.6, 10, true))
	fc.Scale = 1.922
	fc.Ring = &ownimage.Ring{SepPx: 6.4, Nights: []ownimage.RingNight{{Night: "2026-02-23", Filter: ha, Spot: 7.6, Max: 7.0}}}
	gal := []discoveries.Match{{Catalog: catalogGalaxy, Name: "NGC 4313", SepArcsec: 12}}
	if c := nucleusCheck(fc, gal); c.Status != check.Fail {
		t.Errorf("54004: %s %s", c.Status, c.Evidence)
	}
	fc.Ring.Nights[0].Spot = 25
	if c := nucleusCheck(fc, gal); c.Status != check.Pass {
		t.Errorf("a point source above the ring: %s %s", c.Status, c.Evidence)
	}
	fc.Ring.Nights[0].Spot = 7.6
	if c := nucleusCheck(fc, []discoveries.Match{{Catalog: catalogGalaxy, Name: "NGC 4313", SepArcsec: 60}}); c.Status != check.Pass {
		t.Errorf("a bright place that is not the galaxy's centre: %s %s", c.Status, c.Evidence)
	}
	s := transientSuggest(transientGroups(map[string]check.Status{check.TransFlux: check.Pass, check.TransNucleus: check.Fail}), transientFacts{class: discoveries.ClassSupernova})
	if s.State != app.VetArtifact || !strings.Contains(s.Say, "nucleus") {
		t.Errorf("suggestion %+v", s)
	}
}

func TestBrightStarWingIsNeverReal(t *testing.T) {
	t.Parallel()
	wing := func(sigma float64, measured bool) *ownimage.FluxCurve {
		fc := curveOf(zNight(wingNight, lum, 7.5, 12, true))
		fc.Wing = &ownimage.Wing{G: 14.65, SepPx: 5.6, Night: wingNight, Filter: lum, Sigma: sigma, Far: 12, Measured: measured}
		return fc
	}
	if c := wingCheck(wing(-0.4, true)); c.Status != check.Fail {
		t.Errorf("66053: %s %s", c.Status, c.Evidence)
	}
	if c := wingCheck(wing(4.9, true)); c.Status != check.Pass {
		t.Errorf("AT 2026uti: %s %s", c.Status, c.Evidence)
	}
	if c := wingCheck(wing(0, false)); c.Status != check.NotRun {
		t.Errorf("unmeasured: %s %s", c.Status, c.Evidence)
	}
	if c := wingCheck(curveOf()); c.Status != check.Pass {
		t.Errorf("no star: %s %s", c.Status, c.Evidence)
	}
	st := ownOnly()
	st[check.TransWing] = check.Fail
	s := transientSuggest(transientGroups(st), transientFacts{class: discoveries.ClassNova})
	if s.State != app.VetUnsure || !strings.Contains(s.Say, "wing") {
		t.Errorf("own frames, a star's wing: %+v", s)
	}
	st[check.TransWing] = check.NotRun
	if s := transientSuggest(transientGroups(st), transientFacts{class: discoveries.ClassNova}); s.State != app.VetUnsure {
		t.Errorf("own frames, the wing unmeasured: %+v", s)
	}
}

func TestHaloAndEdgeCapOwnFrames(t *testing.T) {
	t.Parallel()
	fc := curveOf(zNight("2025-12-29", lum, 6, 12, true))
	fc.EdgeOK, fc.EdgePx = true, 57
	if c := clearCheck(fc); c.Status != check.Fail {
		t.Errorf("57 px from the edge: %s %s", c.Status, c.Evidence)
	}
	fc.EdgePx, fc.Halo = 150, &ownimage.Halo{V: 1.74, SepArcsec: 2.4 * 60, Reach: 396}
	if c := clearCheck(fc); c.Status != check.Fail || !strings.Contains(c.Evidence, "1.7") {
		t.Errorf("by Alnitak: %s %s", c.Status, c.Evidence)
	}
	st := ownOnly()
	st[check.TransClear] = check.Fail
	if s := transientSuggest(transientGroups(st), transientFacts{}); s.State != app.VetUnsure {
		t.Errorf("own frames by the edge: %+v", s)
	}
	// AT 2024aiji (21669) is 28 px from the edge, and ZTF alerted on it.
	st[check.TransZTF] = check.Pass
	if s := transientSuggest(transientGroups(st), transientFacts{}); s.State != app.VetReal {
		t.Errorf("AT 2024aiji: %+v", s)
	}
}

func TestZTFLookedAndSawNothing(t *testing.T) {
	t.Parallel()
	// 37624: IC 1396 Panel 2's 2025-08-03, ZTF imaged in the same hour to
	// 20.5 and 20.8 with no ALeRCE alert.
	night := time.Date(2025, 8, 3, 0, 0, 0, 0, time.UTC)
	fx := transientFacts{flux: curveOf(zNight("2025-08-03", lum, 60, 8, true)), flagged: []time.Time{night}, ztfAsked: true, imgAsked: true,
		images: []surveys.ZTFImage{{JD: 2460891.826, Band: "r", MagLimit: 20.78}, {JD: 2460891.761, Band: "g", MagLimit: 20.47}}}
	c := ztfImagesCheck(fx, false)
	if c.Status != check.Fail || !strings.Contains(c.Evidence, "20.8 in r") {
		t.Errorf("37624: %s %s", c.Status, c.Evidence)
	}
	if c := ztfImagesCheck(fx, true); c.Status != check.Pass {
		t.Errorf("alerted: %s %s", c.Status, c.Evidence)
	}
	inM31 := fx
	inM31.matches = []discoveries.Match{{Catalog: catalogGalaxy, Name: "M 31"}}
	if c := ztfImagesCheck(inM31, false); c.Status != check.NotRun {
		t.Errorf("AT 2025xtl-like nova in M 31 without an alert: %s %s", c.Status, c.Evidence)
	}
	listed := fx
	listed.ztf = []ZTFObject{{OID: "ZTF25aaaaaaa"}}
	if c := ztfImagesCheck(listed, false); c.Status != check.NotRun {
		t.Errorf("ALeRCE lists an object: %s %s", c.Status, c.Evidence)
	}
	hAlpha := fx
	hAlpha.flux = curveOf(zNight("2025-08-03", ha, 60, 8, true))
	if c := ztfImagesCheck(hAlpha, false); c.Status != check.NotRun {
		t.Errorf("light in H-a only: %s %s", c.Status, c.Evidence)
	}
	fx.images[0].MagLimit, fx.images[1].MagLimit = 18.3, 18.2
	if c := ztfImagesCheck(fx, false); c.Status != check.NotRun {
		t.Errorf("shallow images: %s %s", c.Status, c.Evidence)
	}
	fx.images = nil
	if c := ztfImagesCheck(fx, false); c.Status != check.NotRun {
		t.Errorf("no images: %s %s", c.Status, c.Evidence)
	}
}

func TestCatalogedStarNearbyIsNotATransient(t *testing.T) {
	t.Parallel()
	ref := 14.7
	// 48806: Gaia G 14.06 3.0" away, 14.7 at the spot in the reference.
	fx := transientFacts{flux: curveOf(zNight("2025-11-02", lum, 7, 10, true)), refMag: &ref, near: []counterpart{{g: 14.06, sep: 3.0}}}
	if c := beforeCheck(fx); c.Status != check.Fail || !strings.Contains(c.Evidence, "14.1") {
		t.Errorf("48806: %s %s", c.Status, c.Evidence)
	}
	// AT 2026uti: G 15.77 5.4" away, 18.4 at the spot before.
	ref = 18.4
	fx.near = []counterpart{{g: 15.77, sep: 5.4}}
	if c := beforeCheck(fx); c.Status != check.Pass {
		t.Errorf("AT 2026uti: %s %s", c.Status, c.Evidence)
	}
}

func TestAsteroidCheckMustRun(t *testing.T) {
	t.Parallel()
	st := ownOnly()
	st[check.Stays] = check.NotRun
	s := transientSuggest(transientGroups(st), transientFacts{})
	if s.State != app.VetUnsure || !strings.Contains(s.Say, "asteroids") {
		t.Errorf("asteroid list unread: %+v", s)
	}
}

func TestSurveyConfirmedTransientsStayReal(t *testing.T) {
	t.Parallel()
	// The keep set: AT 2025aesw, AT 2025adss, SN 2026bhf and AT 2024aiji
	// have ALeRCE or TNS matches and take the survey branch, whatever the
	// checks of the user's own frames alone say.
	st := map[string]check.Status{check.TransFlux: check.Pass, check.TransZTF: check.Pass, check.TransNext: check.Fail, check.TransColor: check.Fail,
		check.TransWing: check.Fail, check.TransClear: check.Fail, check.TransFresh: check.NotRun, check.Stays: check.NotRun, check.PSF: check.NotRun}
	if s := transientSuggest(transientGroups(st), transientFacts{class: discoveries.ClassNova}); s.State != app.VetReal || s.Confidence != High {
		t.Errorf("survey confirmed: %+v", s)
	}
}

func TestReportedLightComesFromTheStrongestBrightening(t *testing.T) {
	t.Parallel()
	nm, rm, hm := 18.06, 18.49, 16.0
	night := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	sum := discoverysource.Summary{Best: app.DiscoveryCandidate{Sign: 1}, Members: []app.DiscoveryCandidate{
		{Sign: 1, SNR: 8, Night: night, Group: discoveries.GroupBroadband, NightMag: &nm, RefMag: &rm},
		{Sign: 1, SNR: 20, Night: night, Group: ha, NightMag: &hm},
		{Sign: -1, SNR: 12, Night: night, Group: discoveries.GroupBroadband, NightMag: &rm},
	}}
	reps := reportedLights(sum)
	if r := reportedFor(reps, lum, "2026-10-11"); !r.ok || math.Abs(r.mag-19.26) > 0.02 {
		t.Errorf("AT 2026acsi's new light %+v, want 19.26", r)
	}
	if r := reportedFor(reps, ownimage.AllFilters, "2026-10-11"); math.Abs(r.mag-19.26) > 0.02 {
		t.Errorf("all filters take the broadband light: %+v", r)
	}
	if r := reportedFor(reps, ha, "2026-10-11"); r.mag != 16 {
		t.Errorf("H-a takes its own: %+v", r)
	}
	if r := reportedFor(reps, lum, "2026-10-12"); r.ok {
		t.Errorf("another night's light: %+v", r)
	}
}
