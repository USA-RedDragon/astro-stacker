package vetting

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/ownimage"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/surveys"
)

func TestSurveyLightBesideAStarWingNotReal(t *testing.T) {
	t.Parallel()
	// 54898 (Crab Nebula, 2026-02-05): ATLAS saw 16.2 a day before, 6.4 px
	// from a G 14.1 star, where the wing fit left 1.3 against a fake's 5.8.
	st := map[string]check.Status{check.TransATLAS: check.Pass, check.TransWing: check.Fail, check.TransStack: check.Fail}
	s := transientSuggest(transientGroups(st), transientFacts{stackV: ownimage.StackRefuted})
	if s.State != app.VetArtifact || s.Confidence != Medium {
		t.Errorf("54898: %+v, want a medium artifact", s)
	}
	st[check.TransWing] = check.Pass
	if s := transientSuggest(transientGroups(st), transientFacts{stackV: ownimage.StackRefuted}); s.State != app.VetUnsure {
		t.Errorf("away from a star: %+v, want unsure", s)
	}
	st[check.TransWing], st[check.TransStack] = check.Fail, check.Pass
	if s := transientSuggest(transientGroups(st), transientFacts{stackV: ownimage.StackConfirmed}); s.State != app.VetReal {
		t.Errorf("confirmed beside a star: %+v, want real", s)
	}
}

const (
	novaNight = "2026-07-19"
	lum       = "Luminance"
)

func fluxNight(night string, flux float64, flagged bool) ownimage.FluxNight {
	const err = 40.0
	zp := 25.0
	n := ownimage.FluxNight{Night: night, Filter: lum, N: 10, Flux: flux, Err: err, Flagged: flagged, Limit: zp - 2.5*math.Log10(3*err)}
	if flux >= 3*err {
		n.Mag = zp - 2.5*math.Log10(flux)
	}
	return n
}

func novaCurve(earlier float64) *ownimage.FluxCurve {
	peakFlux := math.Pow(10, -0.4*(17.0-25))
	return &ownimage.FluxCurve{Comps: 9, Zero: map[string]float64{lum: 25}, Nights: []ownimage.FluxNight{
		fluxNight("2026-07-10", earlier, false),
		fluxNight("2026-07-14", earlier, false),
		fluxNight(novaNight, peakFlux, true),
		fluxNight("2026-07-22", peakFlux*math.Pow(10, -0.4*0.6), false),
	}}
}

func TestOwnLightAtTheSpot(t *testing.T) {
	t.Parallel()
	fc := novaCurve(0)
	c := fluxCheck(fc, nil)
	if c.Status != check.Pass || !strings.Contains(c.Evidence, "magnitude 17.0 in Luminance") {
		t.Errorf("flux: %s %s", c.Status, c.Evidence)
	}
	c = nightsCheckTransient(fc)
	if c.Status != check.Pass || !strings.Contains(c.Evidence, "nothing on 2026-07-14") || !strings.Contains(c.Evidence, "faded 0.6 mag") {
		t.Errorf("nights: %s %s", c.Status, c.Evidence)
	}
	c = nightsCheckTransient(novaCurve(math.Pow(10, -0.4*(17.1-25))))
	if c.Status != check.Fail {
		t.Errorf("already there before: %s %s", c.Status, c.Evidence)
	}
	dark := &ownimage.FluxCurve{Comps: 9, Zero: map[string]float64{lum: 25}, Nights: []ownimage.FluxNight{fluxNight(novaNight, 20, true)}}
	if c := fluxCheck(dark, nil); c.Status != check.Fail || !strings.Contains(c.Evidence, "no new light") {
		t.Errorf("no light: %s %s", c.Status, c.Evidence)
	}
}

func TestQuiescentCounterpartSetsTheAmplitude(t *testing.T) {
	t.Parallel()
	fx := transientFacts{flux: novaCurve(0)}
	c := beforeCheck(fx)
	if c.Status != check.Pass || !strings.Contains(c.Evidence, "rose at least 5.0 mag") {
		t.Errorf("nothing there: %s %s", c.Status, c.Evidence)
	}
	fx.ps1Objs = []PS1Object{{R: 21.3, G: 21.8, SepArcsec: 0.4}}
	if c := beforeCheck(fx); c.Status != check.Pass || !strings.Contains(c.Evidence, "4.3 mag brighter") {
		t.Errorf("faint source: %s %s", c.Status, c.Evidence)
	}
	fx.gaia = []counterpart{{g: 17.2, sep: 0.3}}
	if c := beforeCheck(fx); c.Status != check.Fail {
		t.Errorf("a star as bright: %s %s", c.Status, c.Evidence)
	}
}

func atlasRows(nightFlux float64) []surveys.ATLASRow {
	rows := make([]surveys.ATLASRow, 0, 120)
	start := mjdOf(time.Date(2026, 5, 1, 6, 0, 0, 0, time.UTC))
	for d := range 120 {
		mjd := start + float64(d)
		flux := 3 * math.Sin(float64(d))
		if math.Abs(mjd-mjdOf(nightDate(novaNight).Add(nightMid))) < 0.6 {
			flux = nightFlux
		}
		rows = append(rows, surveys.ATLASRow{MJD: mjd, UJy: flux, DUJy: 15, Filter: "o", Obs: "01a59123o0123o"})
	}
	return rows
}

func TestATLASConfirmsOrContradicts(t *testing.T) {
	t.Parallel()
	night := []time.Time{nightDate(novaNight)}
	peakUJy := math.Pow(10, -0.4*(17.0-atlasZero))
	fx := transientFacts{flux: novaCurve(0), flagged: night, atlas: surveys.Fetch{State: surveys.Done}, rows: atlasRows(peakUJy)}
	if c := atlasCheckTransient(fx); c.Status != check.Pass || !strings.Contains(c.Evidence, "magnitude 17.0 in its orange band") {
		t.Errorf("ATLAS saw it: %s %s", c.Status, c.Evidence)
	}
	fx.rows = atlasRows(1)
	if c := atlasCheckTransient(fx); c.Status != check.Fail {
		t.Errorf("ATLAS saw nothing: %s %s", c.Status, c.Evidence)
	}
	fx.atlas = surveys.Fetch{State: surveys.Pending}
	if c := atlasCheckTransient(fx); c.Status != check.NotRun || !strings.Contains(c.Evidence, "queued") {
		t.Errorf("pending: %s %s", c.Status, c.Evidence)
	}
}

func TestZTFAlerts(t *testing.T) {
	t.Parallel()
	night := []time.Time{nightDate(novaNight)}
	at := mjdOf(nightDate(novaNight).Add(nightMid))
	fx := transientFacts{flagged: night, ztfAsked: true, ztf: []ZTFObject{{OID: "ZTF26aaaaaaa", SepArcsec: 0.5, Detections: []ZTFDetection{{MJD: at + 1, Band: "r", Mag: 17.4, Positive: true}}}}}
	if c := ztfCheckTransient(fx); c.Status != check.Pass || !strings.Contains(c.Evidence, "magnitude 17.4 in r") {
		t.Errorf("alerted: %s %s", c.Status, c.Evidence)
	}
	fx.ztf[0].Detections[0].MJD = at - 400
	if c := ztfCheckTransient(fx); c.Status != check.Fail {
		t.Errorf("alerted long before: %s %s", c.Status, c.Evidence)
	}
	fx.ztf = nil
	if c := ztfCheckTransient(fx); c.Status != check.NotRun {
		t.Errorf("no alert: %s %s", c.Status, c.Evidence)
	}
}

// transientGroups are the transient checks at statuses, not run where
// not given, and the shape, wing, clearance and asteroid checks, passed
// unless given.
func transientGroups(statuses map[string]check.Status) []Group {
	ids := []string{check.TransFlux, check.TransStack, check.TransNights, check.TransNext, check.TransFresh, check.TransColor, check.TransWing, check.TransClear,
		check.TransBefore, check.TransATLAS, check.TransZTF, check.TransZTFImages, check.TransHost}
	for _, id := range []string{check.TransNucleus, check.TransBroad} {
		if _, ok := statuses[id]; ok {
			ids = append(ids, id)
		}
	}
	cs := make([]check.Check, 0, len(ids))
	for _, id := range ids {
		st, ok := statuses[id]
		if !ok {
			st = check.NotRun
		}
		cs = append(cs, check.New(id, st, id+" evidence."))
	}
	passed := func(id string) check.Check {
		st, ok := statuses[id]
		if !ok {
			st = check.Pass
		}
		return check.New(id, st, id+" evidence.")
	}
	for i, c := range cs {
		if c.ID == check.TransWing || c.ID == check.TransClear {
			cs[i] = passed(c.ID)
		}
	}
	return []Group{{ID: check.GroupImage, Checks: []check.Check{passed(check.PSF)}}, {ID: check.GroupTransient, Checks: cs},
		{ID: check.GroupMoving, Checks: []check.Check{passed(check.Stays)}}}
}

// ownOnly are the statuses of new light every check of the user's own
// frames finds real.
func ownOnly() map[string]check.Status {
	return map[string]check.Status{check.TransFlux: check.Pass, check.TransStack: check.Pass, check.TransNights: check.Pass, check.TransBefore: check.Pass, check.TransFresh: check.Pass}
}

func TestTransientSuggestions(t *testing.T) {
	t.Parallel()
	nova := transientFacts{class: discoveries.ClassNova}
	s := transientSuggest(transientGroups(map[string]check.Status{check.TransFlux: check.Pass, check.TransATLAS: check.Pass}), nova)
	if s.State != app.VetReal || s.Confidence != High || s.Say != "Looks like a nova, seen by a survey too" {
		t.Errorf("confirmed: %+v", s)
	}
	s = transientSuggest(transientGroups(ownOnly()), transientFacts{class: discoveries.ClassNova, stackV: ownimage.StackConfirmed})
	if s.State != app.VetReal || s.Confidence != Medium {
		t.Errorf("own nights only: %+v", s)
	}
	s = transientSuggest(transientGroups(map[string]check.Status{check.TransFlux: check.Pass, check.TransATLAS: check.Fail}), nova)
	if s.State != app.VetArtifact {
		t.Errorf("ATLAS saw nothing: %+v", s)
	}
	s = transientSuggest(transientGroups(map[string]check.Status{check.TransFlux: check.Pass}), transientFacts{atlas: surveys.Fetch{State: surveys.Pending}})
	if s.State != app.VetUnsure || !strings.Contains(s.Why, "ATLAS data still being fetched") {
		t.Errorf("open: %+v", s)
	}
}

func TestTransientWithItsShapeUnmeasuredIsNotReal(t *testing.T) {
	t.Parallel()
	// 67045 on Sombrero Galaxy's 2025-12-29 had new light at signal to noise
	// 5 on one night only and nothing catalogued there, its shape too faint
	// to measure: "real, medium", for diffuse light near a frame corner.
	skipped := ownOnly()
	skipped[check.PSF] = check.NotRun
	s := transientSuggest(transientGroups(skipped), transientFacts{})
	if s.State != app.VetUnsure || !strings.Contains(s.Say, "shape") || !strings.Contains(s.Why, "psf evidence.") {
		t.Errorf("own frames, shape unmeasured: %+v", s)
	}
	skipped[check.TransATLAS] = check.Pass
	if s := transientSuggest(transientGroups(skipped), transientFacts{}); s.State != app.VetReal {
		t.Errorf("seen by ATLAS, shape unmeasured: %+v", s)
	}
}

func TestOwnFramesAloneNeedTheirStack(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		v     ownimage.StackVerdict
		state string
		say   string
	}{
		{ownimage.StackConfirmed, app.VetReal, "in your own frames"},
		{ownimage.StackDoubtful, app.VetUnsure, "do not confirm"},
		{ownimage.StackUntested, app.VetUnsure, sayNotSure},
		{ownimage.StackRefuted, app.VetArtifact, "stacked at the spot"},
	} {
		st := ownOnly()
		if c.v != ownimage.StackConfirmed {
			st[check.TransStack] = check.Fail
			if c.v == ownimage.StackUntested {
				st[check.TransStack] = check.NotRun
			}
		}
		s := transientSuggest(transientGroups(st), transientFacts{class: discoveries.ClassNova, stackV: c.v})
		if s.State != c.state || !strings.Contains(s.Say, c.say) {
			t.Errorf("%s: %+v", c.v, s)
		}
	}
	st := ownOnly()
	st[check.TransStack], st[check.TransATLAS] = check.Fail, check.Pass
	if s := transientSuggest(transientGroups(st), transientFacts{stackV: ownimage.StackRefuted}); s.State != app.VetUnsure || !strings.Contains(s.Say, "survey saw it") {
		t.Errorf("seen by ATLAS, absent from the stack: %+v", s)
	}
	// 65410 (Horsehead Nebula, 2025-01-24): ATLAS's 17.7 five days later
	// lay 2.8 arcmin from Alnitak, and the light the detection reported was
	// too faint to have been found.
	st[check.TransClear] = check.Fail
	if s := transientSuggest(transientGroups(st), transientFacts{stackV: ownimage.StackRefuted}); s.State != app.VetArtifact || !strings.Contains(s.Say, "halo") {
		t.Errorf("seen by ATLAS in a bright star's halo, absent from the stack: %+v", s)
	}
}
