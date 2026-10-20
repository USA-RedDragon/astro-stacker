package period

import (
	"math"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
)

// Thresholds of the test that refutes a smooth cycle ZTF and ATLAS show
// where TESS would see it and does not; set on 28741 (Crab, 2026-02-05),
// whose 0.996882-day cycle of 0.025 mag in ZTF r TESS held to 3 to 6
// mmag.
const (
	tessRuleShare   = 0.5
	tessRuleSigma   = 3.0
	tessInjShare    = 0.75
	tessRuleSectors = 2
)

// groundClaim is the smallest semi-amplitude, blending taken out, of the
// smooth cycle in the ZTF and ATLAS bands whose own fit shows it at three
// sigma, with its band; 0 when none does.
func (a *analysis) groundClaim() (float64, string) {
	claim, band := 0.0, ""
	for g, gr := range a.d.groups {
		if !testedSurvey(gr.survey) || !(a.sine.amp[g] > 3*a.sine.res.sigma(2+g)) {
			continue
		}
		if amp := undilute(a.sine.amp[g], gr.dil); claim == 0 || amp < claim {
			claim, band = amp, gr.key
		}
	}
	return claim, band
}

// TESSRule is how TESS rules out a smooth cycle the ground surveys show.
type TESSRule struct {
	// Claim is the ground surveys' semi-amplitude in Band, and Limit the
	// largest three sigma upper limit TESS sets on it, at the period, its
	// double or its half, with blending taken out, both in magnitudes.
	Claim float64 `json:"claim"`
	Band  string  `json:"band"`
	Limit float64 `json:"limit"`
	// Recovered is the smallest semi-amplitude a cycle of Claim put into
	// the TESS points comes back at, with blending taken out.
	Recovered float64 `json:"recovered"`
	// Sectors counts the TESS series that rule it out at every one of
	// the three periods.
	Sectors int `json:"sectors"`
}

// tessRules tests a smooth cycle that TESS's own fit does not show
// against TESS: in at least
// tessRuleSectors TESS series spanning two cycles, the cycle at the
// period, at twice it and at half of it is under tessRuleShare of the
// ground claim at tessRuleSigma, with blending taken out, while the same
// cycle put into those points comes back at tessInjShare of the claim or
// more; and no TESS series shows the cycle at that share. 28741's TESS
// sectors 43 to 45 and 71 held it to 3 to 6 mmag against 25 mmag in ZTF r.
func (a *analysis) tessRules() *TESSRule {
	if a.kind != KindSinusoidal || a.sine == nil {
		return nil
	}
	for g, gr := range a.d.groups {
		if gr.survey == lc.TESS && a.sine.amp[g] > 3*a.sine.res.sigma(2+g) {
			return nil
		}
	}
	claim, band := a.groundClaim()
	if claim <= 0 {
		return nil
	}
	P := a.eph.Period
	rule := &TESSRule{Claim: claim, Band: band, Recovered: math.Inf(1)}
	for _, se := range a.d.series {
		if se.survey != lc.TESS || len(se.srch) < 20 {
			continue
		}
		pts := make([]pt, len(se.srch))
		for i, o := range se.srch {
			pts[i] = pt{t: o.t, y: o.y, e: math.Max(o.e, minErr)}
		}
		span := spanOf(pts)
		ok := true
		limit, rec := 0.0, math.Inf(1)
		for _, q := range []float64{P, 2 * P, P / 2} {
			if span < 2*q {
				ok = false
				break
			}
			f, fok := fitSineAt(pts, q)
			if !fok {
				ok = false
				break
			}
			if f.amp >= tessRuleSigma*f.ampErr && undilute(f.amp, se.dil) >= tessRuleShare*claim {
				return nil
			}
			lim := undilute(f.amp+tessRuleSigma*f.ampErr, se.dil)
			got, gok := injected(pts, q, dilute(claim, se.dil))
			if lim >= tessRuleShare*claim || !gok {
				ok = false
				break
			}
			limit, rec = math.Max(limit, lim), math.Min(rec, undilute(got, se.dil))
		}
		if !ok || rec < tessInjShare*claim {
			continue
		}
		rule.Sectors++
		rule.Limit = math.Max(rule.Limit, limit)
		rule.Recovered = math.Min(rule.Recovered, rec)
	}
	if rule.Sectors < tessRuleSectors {
		return nil
	}
	return rule
}

// injected is the semi-amplitude a cycle of semi-amplitude amp at period
// q comes back at once added to pts, worst over four phases.
func injected(pts []pt, q, amp float64) (float64, bool) {
	worst := math.Inf(1)
	cp := make([]pt, len(pts))
	for k := range 4 {
		ph := float64(k) * math.Pi / 2
		for i, p := range pts {
			cp[i] = pt{t: p.t, y: p.y + amp*math.Sin(2*math.Pi*p.t/q+ph), e: p.e}
		}
		f, ok := fitSineAt(cp, q)
		if !ok {
			return 0, false
		}
		worst = math.Min(worst, f.amp)
	}
	return worst, true
}
