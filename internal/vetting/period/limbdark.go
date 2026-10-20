package period

import (
	"math"
	"strings"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
)

// ldPoint is a linear limb darkening coefficient u at effective
// temperature teff in K.
type ldPoint struct{ teff, u float64 }

// ldMargin is added to every tabulated u. It covers a quadratic law's
// brighter centre (up to 0.05 more in u for the coolest dwarfs in V),
// metallicity outside -0.5 to +0.5, and the spread between model and
// measured coefficients.
const ldMargin = 0.1

// ldTables is, per passband, the largest linear limb darkening
// coefficient (least squares fit) over log g 4.0 and 4.5, [M/H] -0.5 to
// +0.5 and microturbulence 2 km/s, for ATLAS and PHOENIX models alike,
// from 2200 to 10000 K, the dwarfs a primary can be.
//
// Sources: Claret and Bloemen (2011, A&A 529, A75; VizieR J/A+A/529/A75)
// for Sloan g', r', i' (from 3500 K), Johnson B, V, R, I and K (PHOENIX,
// below 3500 K) and Spitzer IRAC 3.6 and 4.5 um (from 3500 K); Claret
// (2017, A&A 600, A30; VizieR J/A+A/600/A30) for TESS. Where the nearest
// passband is not tabulated, below 3500 K, the next one out is used: V
// for g', R for r', I for i' and K, whose limb darkening is stronger, for
// IRAC. "blue" is the larger of B and g' (B and V below 3500 K) for a band
// the vet does not know.
var ldTables = map[string][]ldPoint{
	"g": {
		{2200, 1.163}, {2400, 1.119}, {2600, 1.040}, {2800, 0.938}, {3000, 0.825}, {3200, 0.796}, {3400, 0.831}, {3500, 0.869},
		{4000, 0.873}, {4250, 0.889}, {4500, 0.883}, {4750, 0.865}, {6500, 0.681}, {6750, 0.658}, {7000, 0.639}, {7250, 0.624},
		{7500, 0.664}, {8000, 0.637}, {8250, 0.630}, {9250, 0.561}, {9800, 0.535}, {10000, 0.528},
	},
	"r": {
		{2200, 1.088}, {2400, 1.056}, {2600, 0.982}, {2800, 0.875}, {3000, 0.762}, {3200, 0.720}, {3400, 0.736}, {3500, 0.759},
		{3750, 0.803}, {4000, 0.810}, {4250, 0.790}, {4800, 0.729}, {6250, 0.578}, {6750, 0.533}, {7250, 0.497}, {7500, 0.538},
		{8000, 0.513}, {8800, 0.463}, {9600, 0.430}, {10000, 0.421},
	},
	"i": {
		{2200, 1.016}, {2400, 0.995}, {2600, 0.925}, {2800, 0.813}, {3000, 0.681}, {3200, 0.653}, {3400, 0.677}, {3500, 0.627},
		{3800, 0.662}, {4000, 0.682}, {4250, 0.676}, {4800, 0.622}, {6000, 0.520}, {6500, 0.474}, {7000, 0.434}, {7250, 0.418},
		{7500, 0.444}, {8250, 0.411}, {9000, 0.366}, {9800, 0.345}, {10000, 0.342},
	},
	"blue": {
		{2200, 1.184}, {2400, 1.119}, {2600, 1.026}, {2800, 0.922}, {3000, 0.808}, {3200, 0.788}, {3400, 0.814}, {3500, 0.869},
		{3600, 0.869}, {3750, 0.875}, {3800, 0.919}, {4000, 0.893}, {4200, 0.910}, {4250, 0.919}, {4400, 0.909}, {4600, 0.913},
		{4750, 0.895}, {4800, 0.912}, {5000, 0.874}, {5200, 0.847}, {5250, 0.851}, {5400, 0.831}, {5500, 0.827}, {5750, 0.801},
		{5800, 0.803}, {6000, 0.774}, {6750, 0.699}, {7000, 0.728}, {7200, 0.707}, {7250, 0.660}, {7400, 0.696}, {7500, 0.694},
		{7600, 0.682}, {7750, 0.677}, {7800, 0.669}, {8200, 0.657}, {8250, 0.662}, {8400, 0.653}, {8500, 0.644}, {8600, 0.661},
		{8750, 0.622}, {8800, 0.653}, {9000, 0.629}, {9200, 0.617}, {9250, 0.589}, {9400, 0.605}, {9500, 0.576}, {9600, 0.594},
		{9750, 0.565}, {9800, 0.584}, {10000, 0.556},
	},
	"tess": {
		{2200, 0.911}, {2400, 0.890}, {2500, 0.863}, {2600, 0.841}, {2700, 0.805}, {2800, 0.764}, {2900, 0.729}, {3000, 0.698},
		{3100, 0.533}, {3200, 0.532}, {3400, 0.541}, {3500, 0.542}, {3600, 0.548}, {3700, 0.562}, {3800, 0.585}, {3900, 0.601},
		{4000, 0.621}, {4100, 0.637}, {4200, 0.641}, {4900, 0.589}, {5100, 0.567}, {5900, 0.513}, {8000, 0.405}, {8800, 0.359},
		{9000, 0.351}, {9600, 0.340}, {10000, 0.338},
	},
	"w1": {
		{2200, 0.538}, {2600, 0.399}, {3000, 0.305}, {3200, 0.299}, {3400, 0.318}, {3500, 0.222}, {3750, 0.247}, {4000, 0.257},
		{4250, 0.256}, {6500, 0.178}, {9250, 0.108}, {10000, 0.102},
	},
	"w2": {
		{2200, 0.538}, {2600, 0.399}, {3000, 0.305}, {3200, 0.299}, {3400, 0.318}, {3500, 0.181}, {3750, 0.183}, {4000, 0.199},
		{4250, 0.208}, {4500, 0.204}, {5250, 0.180}, {6500, 0.159}, {8000, 0.120}, {8500, 0.105}, {10000, 0.087},
	},
}

// ldTable is the limb darkening table for a survey band: ZTF g, r and i
// take Sloan g', r' and i'; ATLAS c (420 to 650 nm) the larger of its
// neighbours g' and r', which is g'; ATLAS o (560 to 820 nm) the larger of
// r' and i', which is r'; NEOWISE W1 and W2 take IRAC 3.6 and 4.5 um.
func ldTable(survey, band string) []ldPoint {
	b := strings.ToLower(band)
	switch survey {
	case lc.TESS:
		return ldTables["tess"]
	case lc.NEOWISE:
		if b == "w2" {
			return ldTables["w2"]
		}
		return ldTables["w1"]
	case lc.ATLAS:
		if b == "c" {
			return ldTables["g"]
		}
		return ldTables["r"]
	case lc.ZTF:
		switch b {
		case "g", "r", "i":
			return ldTables[b]
		}
	}
	return ldTables["blue"]
}

// ldAt is table tab interpolated at teff, held at its ends.
func ldAt(tab []ldPoint, teff float64) float64 {
	if teff <= tab[0].teff {
		return tab[0].u
	}
	for i := 1; i < len(tab); i++ {
		if teff <= tab[i].teff {
			lo, hi := tab[i-1], tab[i]
			return lo.u + (hi.u-lo.u)*(teff-lo.teff)/(hi.teff-lo.teff)
		}
	}
	return tab[len(tab)-1].u
}

// centreToMean is the most a primary's centre can outshine its mean
// surface in a survey band, 1/(1-u/3) for a linear law, with u the
// largest tabulated coefficient for any primary from tlo to thi K plus
// ldMargin.
func centreToMean(survey, band string, tlo, thi float64) float64 {
	tab := ldTable(survey, band)
	if thi < tlo {
		tlo, thi = thi, tlo
	}
	u := math.Max(ldAt(tab, tlo), ldAt(tab, thi))
	for _, p := range tab {
		if p.teff > tlo && p.teff < thi {
			u = math.Max(u, p.u)
		}
	}
	return 1 / (1 - (u+ldMargin)/3)
}

// primaryTeffs is the range of effective temperature the primaries
// darkestCompanion tries can have: main-sequence stars of at least minR
// solar radii up to maxT (A0V when 0), or maxT alone when the smallest
// primary is hotter.
func primaryTeffs(minR, maxT float64) (float64, float64) {
	hi := mainSequence[0].teff
	if maxT > 0 {
		hi = math.Min(hi, maxT)
	}
	lo := msTeffAt(minR)
	if lo == 0 {
		lo = mainSequence[len(mainSequence)-1].teff
	}
	return math.Min(lo, hi), hi
}
