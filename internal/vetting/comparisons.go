package vetting

import (
	"context"
	"log/slog"
	"math"
	"sort"

	"github.com/USA-RedDragon/astro-stacker/internal/starcat"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/surveys"
)

// Comparison stars for the period tests: Gaia stars within compArcsec
// of the star and within compMag of its G, single by RUWE and not flagged
// variable by Gaia, closest in brightness first; compStars of them with
// at least compMinPoints ZTF points in a band.
const (
	compArcsec    = 240.0
	compMag       = 0.6
	compRUWE      = 1.4
	compStars     = 5
	compTry       = 12
	compMinPoints = 60
	compSelfArc   = 1.0
)

// comparisonTargets are the Gaia stars to try as comparison stars for st.
func (s *Service) comparisonTargets(ctx context.Context, st star) ([]surveys.Target, error) {
	if s.Gaia == nil || !st.found || st.target.G == 0 {
		return nil, nil
	}
	t := st.target
	stars, err := s.Gaia.Cone(ctx, t.RA, t.Dec, compArcsec/3600, func(g *starcat.GaiaStar) bool {
		return g.SourceID != t.GaiaID && math.Abs(float64(g.G)-t.G) < compMag && g.RUWE < compRUWE && g.Variable != starcat.VariableVariable &&
			arcsec(t.RA, t.Dec, g.RA, g.Dec) > compSelfArc
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(stars, func(i, j int) bool {
		return math.Abs(float64(stars[i].G)-t.G) < math.Abs(float64(stars[j].G)-t.G)
	})
	out := make([]surveys.Target, 0, min(len(stars), compTry))
	for _, g := range stars {
		if len(out) == compTry {
			break
		}
		out = append(out, surveys.Target{GaiaID: g.SourceID, RA: g.RA, Dec: g.Dec, PMRA: finite(g.PMRA), PMDec: finite(g.PMDec), G: float64(g.G)})
	}
	return out, nil
}

// comparisons are the ZTF light curves of st's comparison stars.
func (s *Service) comparisons(ctx context.Context, f surveys.Field, st star) [][]lc.Series {
	if s.Surveys == nil {
		return nil
	}
	ts, err := s.comparisonTargets(ctx, st)
	if err != nil {
		slog.Warn("Could not read Gaia around the star for comparison stars", "error", err)
		return nil
	}
	if len(ts) == 0 {
		return nil
	}
	var out [][]lc.Series
	for _, fe := range s.Surveys.ZTFStars(ctx, f, ts) {
		if fe.State != surveys.Done || !enoughPoints(fe.Series) {
			continue
		}
		out = append(out, fe.Series)
		if len(out) == compStars {
			break
		}
	}
	return out
}

func enoughPoints(ss []lc.Series) bool {
	for _, s := range ss {
		if len(s.Points) >= compMinPoints {
			return true
		}
	}
	return false
}
