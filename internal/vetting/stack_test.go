package vetting

import (
	"math"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/ownimage"
)

// auditLight is the light, at zero point 25, the detections of the
// transient audit reported, set to 1000 so each night's error makes its
// fake show as the audit measured.
const auditLight = 1000.0

// auditNight is a night of 12 subs whose own signal to noise is z where
// the reported light shows at inj; the empty places around the spot give
// under 2.6 in 99 of 100, a little over what a normal scatter gives,
// 2.3, as the audit's empty places stood in units of their scatter.
func auditNight(night string, z, inj float64, flagged bool) ownimage.FluxNight {
	e := auditLight / inj
	return ownimage.FluxNight{Night: night, Filter: lum, N: 12, Flux: z * e, Err: e, Local: e, Empty99: 2.6 * e, Flagged: flagged}
}

func auditReps(night string) map[string]reported {
	return lightAt(auditLight/10, night)
}

func TestAuditedTransientsStackToTheirKnownAnswers(t *testing.T) {
	t.Parallel()
	// audit-novas/data/analysis.json, adjacent.json and dia_known.json: the
	// flagged night's local z and the reported light's, then the nights 1 to
	// 3 days after with 3 subs or more.
	cases := []struct {
		name   string
		nights []ownimage.FluxNight
		want   ownimage.StackVerdict
	}{
		{"182, AT 2026acsi", []ownimage.FluxNight{auditNight("2026-10-11", 6.2, 5.8, true)}, ownimage.StackConfirmed},
		{"AT 2026uti", []ownimage.FluxNight{auditNight("2026-07-19", 10.7, 7.1, true), auditNight("2026-07-20", 9.4, 6.5, false)}, ownimage.StackConfirmed},
		{"AT 2025abrs", []ownimage.FluxNight{auditNight("2025-10-26", 10.3, 6.4, true), auditNight("2025-10-27", 7.9, 8.2, false)}, ownimage.StackConfirmed},
		{"AT 2025adss", []ownimage.FluxNight{auditNight("2025-11-21", 30.8, 10.7, true), auditNight("2025-11-22", 18.5, 4.4, false), auditNight("2025-11-24", 9.8, 11.8, false)}, ownimage.StackConfirmed},
		{"60465, bulge", []ownimage.FluxNight{auditNight("2025-10-26", 0.8, 12.1, true), auditNight("2025-10-27", -0.3, 8.4, false)}, ownimage.StackRefuted},
		{"60670, bulge", []ownimage.FluxNight{auditNight("2025-10-15", 2.7, 6.3, true), auditNight("2025-10-17", 0.0, 5.9, false), auditNight("2025-10-18", 0.3, 3.6, false)}, ownimage.StackRefuted},
		{"35876, bulge", []ownimage.FluxNight{auditNight("2025-10-18", 1.9, 10.1, true), auditNight("2025-10-19", -0.7, 5.2, false)}, ownimage.StackRefuted},
		{"60345, bulge with no later night", []ownimage.FluxNight{auditNight("2025-10-27", 0.3, 12.7, true)}, ownimage.StackDoubtful},
		{"35878, bulge", []ownimage.FluxNight{auditNight("2025-10-18", 3.3, 7.9, true), auditNight("2025-10-19", 1.0, 4.7, false)}, ownimage.StackDoubtful},
		{"58090, on a star", []ownimage.FluxNight{auditNight("2025-11-21", 3.5, 5.7, true), auditNight("2025-11-22", -1.5, 7.6, false)}, ownimage.StackDoubtful},
		{"66053, a star's wing", []ownimage.FluxNight{auditNight("2025-01-21", 0.4, 4.3, true)}, ownimage.StackUntested},
	}
	for _, c := range cases {
		fc := curveOf(c.nights...)
		var first string
		for _, n := range c.nights {
			if n.Flagged {
				first = n.Night
			}
		}
		ch, v := transientStack(fc, auditReps(first))
		if v != c.want {
			t.Errorf("%s: %s, want %s: %s", c.name, v, c.want, ch.Evidence)
		}
	}
}

func TestStackReadsTheWingFitBesideABrightStar(t *testing.T) {
	t.Parallel()
	// 66233, 6.6 px from a G 13.94 star on 2025-01-21: light in the plain
	// fit, none left once the star is fitted with each sub's own shape.
	fc := curveOf(auditNight("2025-01-21", 9.0, 8.2, true))
	fc.Wing = &ownimage.Wing{G: 13.94, SepPx: 6.6, Night: "2025-01-21", Filter: lum, Excess: 2.4 * 100, Sigma: 2.4, Err: 100, Measured: true}
	c, v := transientStack(fc, auditReps("2025-01-21"))
	if v == ownimage.StackConfirmed || c.Status == check.Pass {
		t.Errorf("a wing confirmed: %s %s", v, c.Evidence)
	}
	fc.Wing.Sigma, fc.Wing.Excess = 9.5, 950
	if _, v := transientStack(fc, auditReps("2025-01-21")); v != ownimage.StackConfirmed {
		t.Errorf("new light beside a star, AT 2026uti-like, not confirmed: %s", v)
	}
}

func TestStackFreshFramesMustShowIt(t *testing.T) {
	t.Parallel()
	n := auditNight("2026-10-11", 12, 11, true)
	n.FreshN, n.FreshErr = 6, n.Err*math.Sqrt(2)
	n.FreshFlux = 0.2 * n.FreshErr
	if c, v := transientStack(curveOf(n), auditReps("2026-10-11")); v != ownimage.StackDoubtful {
		t.Errorf("flat fresh frames: %s %s", v, c.Evidence)
	}
	n.FreshFlux = 3.5 * n.FreshErr
	if c, v := transientStack(curveOf(n), auditReps("2026-10-11")); v != ownimage.StackConfirmed {
		t.Errorf("fresh frames showing it: %s %s", v, c.Evidence)
	}
}
