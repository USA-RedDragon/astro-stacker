package period

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
)

// NoDarkCompanion starts the half check's evidence when the vet took two
// equal eclipses per orbit because a companion dark enough to hide its
// own eclipse could not block that much light.
const NoDarkCompanion = "Two equal eclipses per orbit"

// msStar is one main-sequence spectral type: effective temperature in K,
// radius in solar radii and Gaia absolute G magnitude.
type msStar struct {
	teff, r, mg float64
}

// mainSequence is the dwarf sequence of Pecaut and Mamajek (2013), as
// updated in E. Mamajek's "A Modern Mean Dwarf Stellar Color and
// Effective Temperature Sequence" (version 2022.04.16), A0V to M9V.
var mainSequence = []msStar{
	{9700, 2.193, 1.00}, {8800, 2.117, 1.345}, {8100, 1.785, 1.98}, {7760, 1.750, 2.19},
	{7220, 1.728, 2.51}, {6820, 1.622, 2.89}, {6550, 1.473, 3.26}, {6280, 1.324, 3.66},
	{6180, 1.221, 3.90}, {5930, 1.100, 4.325}, {5770, 1.012, 4.635}, {5680, 0.991, 4.757},
	{5660, 0.977, 4.801}, {5480, 0.914, 5.098}, {5380, 0.853, 5.34}, {5270, 0.813, 5.553},
	{5170, 0.797, 5.65}, {5100, 0.783, 5.83}, {4830, 0.755, 6.20}, {4600, 0.713, 6.53},
	{4440, 0.701, 6.83}, {4300, 0.669, 7.02}, {4100, 0.630, 7.57}, {3990, 0.615, 7.74},
	{3930, 0.608, 8.03}, {3850, 0.588, 8.16}, {3770, 0.544, 8.44}, {3660, 0.501, 8.82},
	{3620, 0.482, 8.98}, {3560, 0.446, 9.29}, {3470, 0.421, 9.67}, {3430, 0.361, 10.05},
	{3270, 0.300, 10.87}, {3210, 0.274, 11.21}, {3110, 0.217, 12.04}, {3060, 0.196, 12.45},
	{2930, 0.156, 13.35}, {2810, 0.137, 14.26}, {2740, 0.126, 14.40}, {2680, 0.120, 14.72},
	{2630, 0.116, 15.20}, {2570, 0.114, 15.20}, {2420, 0.104, 15.90}, {2380, 0.102, 16.20},
}

const (
	// ldMax is the most a limb-darkened star's centre outshines its mean
	// surface: 1/(1-u/3) for a linear law with u = 0.9, more than any
	// FGKM dwarf has in the optical.
	ldMax = 1.43
	// defaultMinR is the smallest primary, in solar radii, assumed
	// without a parallax: an M4V. Around a later M dwarf a brown dwarf or
	// giant planet can block a third of the light and stay dark, which the
	// light curve alone cannot rule out.
	defaultMinR = 0.274
	// pairBoost is how much brighter in G an unresolved equal pair is than
	// its primary alone.
	pairBoost = 0.753
	// minParallaxZ is the least parallax over its error that sets the
	// smallest primary from the star's absolute magnitude.
	minParallaxZ = 5.0
	// inflation is how much larger than the main sequence a low-mass star
	// in a close binary can be: M dwarfs in eclipsing binaries come out up
	// to about 10% larger than the sequence at their mass and temperature
	// (Torres, Andersen and Gimenez 2010; Kraus et al. 2011), so a
	// companion of a given radius may be as cool as a dwarf 10% smaller.
	inflation = 1.1
	// darkLimitZ is how many sigma the upper limit on a second eclipse at
	// phase 0.5 takes.
	darkLimitZ = 3.0
)

// bandNM is a survey band's effective wavelength in nm.
func bandNM(survey, band string) float64 {
	switch survey {
	case lc.TESS:
		return 800
	case lc.NEOWISE:
		if strings.EqualFold(band, "W2") {
			return 4600
		}
		return 3400
	case lc.ATLAS:
		if strings.EqualFold(band, "c") {
			return 530
		}
		return 680
	}
	switch strings.ToLower(band) {
	case "g":
		return 480
	case "i":
		return 790
	}
	return 640
}

// planckRatio is the surface brightness of a t2 black body over a t1 one
// at wavelength nm.
func planckRatio(t2, t1, nm float64) float64 {
	c := 1.4387769e7 / nm
	return math.Expm1(c/t1) / math.Expm1(c/t2)
}

// msTeffAt is the effective temperature of the main-sequence star of
// radius r, or 0 below the sequence's end, where a brown dwarf or planet
// of that size gives no light worth counting.
func msTeffAt(r float64) float64 {
	n := len(mainSequence)
	if r < mainSequence[n-1].r {
		return 0
	}
	if r >= mainSequence[0].r {
		return mainSequence[0].teff
	}
	for i := 1; i < n; i++ {
		hi, lo := mainSequence[i-1], mainSequence[i]
		if r >= lo.r {
			if hi.r == lo.r {
				return lo.teff
			}
			return lo.teff + (hi.teff-lo.teff)*(r-lo.r)/(hi.r-lo.r)
		}
	}
	return 0
}

// darkestCompanion is the faintest surface brightness, relative to its
// primary at wavelength nm, a main-sequence companion of at least k
// primary radii can have, over every main-sequence primary of at least
// minR solar radii, the companion allowed inflation times its sequence
// radius.
func darkestCompanion(k, nm, minR float64) float64 {
	best := math.Inf(1)
	for _, p := range mainSequence {
		if p.r < minR {
			continue
		}
		t2 := msTeffAt(k * p.r / inflation)
		j := 0.0
		if t2 > 0 {
			j = planckRatio(math.Min(t2, p.teff), p.teff, nm)
		}
		best = math.Min(best, j)
	}
	if math.IsInf(best, 1) {
		return 0
	}
	return best
}

// smallestPrimary is the radius of the smallest main-sequence star that
// can be the primary of a star of apparent G magnitude g and parallax plx
// (mas): one no fainter than the star's absolute magnitude plus an equal
// pair's boost, at two sigma more parallax. Without a good parallax it is
// defaultMinR.
func smallestPrimary(g, plx, plxErr float64) (float64, bool) {
	if !(g > 0) || !(plx > 0) || !(plxErr > 0) || plx/plxErr < minParallaxZ {
		return defaultMinR, false
	}
	mg := g + 5*math.Log10((plx+2*plxErr)/100) + pairBoost
	minR := mainSequence[len(mainSequence)-1].r
	for _, s := range mainSequence {
		if s.mg <= mg {
			minR = s.r
		}
	}
	return minR, true
}

// darkTest is the dark companion test of one dip per cycle in its most
// telling band.
type darkTest struct {
	tested    bool
	decided   bool
	seen      bool
	band      string
	depth     float64
	frac      float64
	k         float64
	limit     float64
	need      float64
	minR      float64
	parallax  bool
	seenLevel float64
}

// darkCompanion decides whether one dip per cycle of m can be a star
// eclipsed by a dark companion, or must be the two equal eclipses of a
// circular orbit at twice the period.
//
// On a circular orbit both eclipses hide the same area A, so the second
// eclipse (the companion behind the primary) is shallower than the first
// by the companion's surface brightness over the primary's, J, within the
// primary's limb darkening: f2/f1 >= J/ldMax. A dip that takes f1 of the
// light needs a companion of at least k = sqrt(f1/ldMax) primary radii,
// since even over the primary's centre it hides at most ldMax k² of the
// light. The points at phase 0.5 bound f2 from above; a band bounds J by
// J < ldMax f2/f1. On the main sequence a companion of k primary radii
// is never darker than darkestCompanion(k), the faintest such companion
// of any primary from A0V down to the smallest the star can be. When some
// band's bound on J is below that, no main-sequence companion can hide its
// eclipse and the single dip is the two equal eclipses at twice the
// period. For source 16040 (r 0.47 mag deep, 35% of the light, f2/f1
// under 0.03) k is at least 0.49 and the darkest companion that size has
// J about 0.09 in r; for a dip of 0.1 mag, k is 0.25 and a red dwarf or
// brown dwarf companion stays dark enough, so the period stands.
//
// A band with a second eclipse at phase 0.5 at three sigma is a real
// shallow second eclipse: the period stands and the dip is not doubled.
func (a *analysis) darkCompanion(one *ecFit) darkTest {
	minR, plx := smallestPrimary(a.opts.G, a.opts.Parallax, a.opts.ParallaxErr)
	out := darkTest{minR: minR, parallax: plx}
	if len(one.dips) != 1 {
		return out
	}
	f := a.f
	type acc struct{ smy, smm, rr, nr float64 }
	accs := make([]acc, f.ng)
	cycles := make([]map[int]bool, f.ng)
	dp := one.dips[0]
	for i := range f.t {
		if !one.keep[i] {
			continue
		}
		g := f.g[i]
		if !one.active[g][0] || one.depth[g][0] <= 0 {
			continue
		}
		r := f.y[i] - one.offset[f.s[i]]
		x := (f.t[i]-one.T0)/one.P - dp.phase
		w := 1 / (f.e[i] * f.e[i])
		if shapeOnly(wrapPhase(x), dp.width, dp.shape) == 0 {
			accs[g].rr += w * r * r
			accs[g].nr++
		}
		sh := shapeOnly(wrapPhase(x-0.5), dp.width, dp.shape)
		if sh <= 0.5 {
			continue
		}
		mm := one.depth[g][0] * sh
		accs[g].smy += w * r * mm
		accs[g].smm += w * mm * mm
		if cycles[g] == nil {
			cycles[g] = map[int]bool{}
		}
		cycles[g][int(math.Round(x-0.5))] = true
	}
	bestMargin := 0.0
	for g, ac := range accs {
		if ac.smm == 0 || len(cycles[g]) < 3 || one.depth[g][0] < 5*one.depthSigma(g, 0) {
			continue
		}
		s := ac.smy / ac.smm
		se := 1 / math.Sqrt(ac.smm)
		if ac.nr > 10 {
			se *= math.Max(1, math.Sqrt(ac.rr/ac.nr))
		}
		gr := a.d.groups[g]
		depth := undilute(one.depth[g][0], gr.dil)
		f1 := 1 - math.Pow(10, -0.4*depth)
		if s >= darkLimitZ*se && s*depth >= 0.005 {
			out.seen, out.band, out.seenLevel = true, gr.key, s*one.depth[g][0]
			out.decided = false
			return out
		}
		lim := math.Max(s, 0) + darkLimitZ*se
		f2 := 1 - math.Pow(10, -0.4*lim*depth)
		jLim := ldMax * f2 / f1
		k := math.Sqrt(f1 / ldMax)
		need := darkestCompanion(k, bandNM(gr.survey, gr.band), minR)
		out.tested = true
		margin := need / jLim
		if margin > bestMargin || out.band == "" {
			bestMargin = margin
			out.band, out.depth, out.frac, out.k, out.limit, out.need = gr.key, depth, f1, k, jLim, need
			out.decided = need > jLim
		}
	}
	return out
}

// primaryWords says what the test assumed about the primary.
func (d darkTest) primaryWords() string {
	if d.parallax {
		return fmt.Sprintf("Gaia's parallax allows a primary down to %s solar radii", strconv.FormatFloat(d.minR, 'f', 2, 64))
	}
	return "without a good parallax the primary is taken to be no smaller than an M4 dwarf"
}

// doubledWords is the half check's evidence for two equal eclipses taken
// by the dark companion rule, the single-dip period being half.
func (d darkTest) doubledWords(half string) string {
	return fmt.Sprintf("%s: one dip every %s days hides %s%% of the light in %s, which needs a companion of at least %s times the primary's radius, "+
		"and nothing shows half a cycle later (the companion's surface brightness is under %s of the primary's at three sigma). "+
		"No main-sequence companion that size is that dark (at least %s), so a dark companion is ruled out (%s) and the true period is not %s days.",
		NoDarkCompanion, half, strconv.FormatFloat(100*d.frac, 'f', 0, 64), d.band, strconv.FormatFloat(d.k, 'f', 2, 64),
		strconv.FormatFloat(d.limit, 'f', 3, 64), strconv.FormatFloat(d.need, 'f', 3, 64), d.primaryWords(), half)
}

// keptWords says why one dip per cycle keeps its period.
func (d darkTest) keptWords() string {
	switch {
	case d.seen:
		return fmt.Sprintf("a shallow dip of %s mag shows half a cycle later in %s", mag2(d.seenLevel), d.band)
	case !d.tested:
		return "too few points half a cycle after a dip to tell whether a dark companion could hide its eclipse"
	}
	return fmt.Sprintf("a companion dark enough to hide its own eclipse can still block %s%% of the light in %s (%s)",
		strconv.FormatFloat(100*d.frac, 'f', 0, 64), d.band, d.primaryWords())
}

// halfDark is the dark companion test of a two dip fit's half period: the
// first dip alone every half cycle.
func (a *analysis) halfDark(m *ecFit) darkTest {
	one := fitEclipse(a.f, a.d, ecInit{P: m.P / 2, T0: m.T0, dips: []dipP{{0, m.dips[0].width * 2, m.dips[0].shape}}, freeP: true})
	return a.darkCompanion(one)
}
