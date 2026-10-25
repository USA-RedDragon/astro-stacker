package vetting

import (
	"context"
	"strings"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/discoverysource"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/ownimage"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/surveys"
)

// TestBeforeRadius41445 is 41445 in the Rosette Nebula, in frames of
// 1.92 arcsec per px with stars 2.9 px wide. Pan-STARRS lists a very red
// source of r 21.9 2.8 arcsec away, which the 1.5 arcsec look missed. The
// look must cover the star width and name it.
func TestBeforeRadius41445(t *testing.T) {
	t.Parallel()
	near := []PS1Object{{R: 21.9, I: 21.5, SepArcsec: 2.8}}
	var asked float64
	s := &Service{PS1: func(_ context.Context, _, _, radius float64) ([]PS1Object, error) {
		asked = radius
		var out []PS1Object
		for _, o := range near {
			if o.SepArcsec <= radius {
				out = append(out, o)
			}
		}
		return out, nil
	}}
	own := &ownimage.Result{Flux: &ownimage.FluxCurve{Scale: 1.92, Sigma: 2.9 / fwhmSigma}}
	fx := s.transientFacts(context.Background(), surveys.Field{}, discoverysource.Summary{}, own, true)
	if asked < 3.4 {
		t.Errorf("asked Pan-STARRS within %.1f arcsec, under the 2.9 px star width at 1.92 arcsec per px", asked)
	}
	c := beforeCheck(fx)
	t.Logf("%s: %s", c.Status, c.Evidence)
	if !strings.Contains(c.Evidence, "2.8 arcsec away") {
		t.Errorf("the before check did not name the source 2.8 arcsec away: %s", c.Evidence)
	}
	if got := beforeArcsec(nil); got != counterpartArcsec {
		t.Errorf("no flux curve gave %.1f arcsec, want %.1f", got, counterpartArcsec)
	}
}

// TestBeforeHostCore is a supernova of 16.0 3.5 arcsec from its host's
// core, which Pan-STARRS lists at r 15.8: the core is listed but is not
// the spot.
func TestBeforeHostCore(t *testing.T) {
	t.Parallel()
	fc := &ownimage.FluxCurve{Scale: 1.92, Sigma: 2.9 / fwhmSigma, Nights: []ownimage.FluxNight{{Night: "2026-01-10", Filter: "Red", N: 10, Flux: 1, Err: 0.05, Mag: 16.0, Flagged: true}}}
	fx := transientFacts{flux: fc, ps1Objs: []PS1Object{{R: 15.8, SepArcsec: 3.5}}, gaiaErr: errNoCatalog}
	c := beforeCheck(fx)
	t.Logf("%s: %s", c.Status, c.Evidence)
	if c.Status != check.Pass || !strings.Contains(c.Evidence, "3.5 arcsec away") {
		t.Errorf("a host core 3.5 arcsec away refused the new light: %s %s", c.Status, c.Evidence)
	}
}
