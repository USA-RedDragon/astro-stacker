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

// Nearby stars to test a flat star's ZTF fades against: Gaia stars within
// fadeArcsec of the star and within fadeMag of its G, the fadeTry closest
// on the sky, of which at least fadeMinStars must have ZTF points.
const (
	fadeArcsec   = 180.0
	fadeMag      = 1.0
	fadeTry      = 50
	fadeMinStars = 20
)

// fadeTargets are the Gaia stars to read for st's fade test.
func (s *Service) fadeTargets(ctx context.Context, st star) ([]surveys.Target, error) {
	if s.Gaia == nil || !st.found || st.target.G == 0 {
		return nil, nil
	}
	t := st.target
	stars, err := s.Gaia.Cone(ctx, t.RA, t.Dec, fadeArcsec/3600, func(g *starcat.GaiaStar) bool {
		return g.SourceID != t.GaiaID && math.Abs(float64(g.G)-t.G) < fadeMag && arcsec(t.RA, t.Dec, g.RA, g.Dec) > compSelfArc
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(stars, func(i, j int) bool {
		return arcsec(t.RA, t.Dec, stars[i].RA, stars[i].Dec) < arcsec(t.RA, t.Dec, stars[j].RA, stars[j].Dec)
	})
	out := make([]surveys.Target, 0, min(len(stars), fadeTry))
	for _, g := range stars[:min(len(stars), fadeTry)] {
		out = append(out, surveys.Target{GaiaID: g.SourceID, RA: g.RA, Dec: g.Dec, PMRA: finite(g.PMRA), PMDec: finite(g.PMDec), G: float64(g.G)})
	}
	return out, nil
}

// fadeNeighbours are the ZTF light curves of the stars around st, read
// as the star's own are, or nil when fewer than fadeMinStars have points.
func (s *Service) fadeNeighbours(ctx context.Context, f surveys.Field, st star) [][]lc.Series {
	if s.Surveys == nil {
		return nil
	}
	ts, err := s.fadeTargets(ctx, st)
	if err != nil {
		slog.Warn("Could not read Gaia around the star for the fade test", "error", err)
		return nil
	}
	if len(ts) < fadeMinStars {
		return nil
	}
	var out [][]lc.Series
	for _, fe := range s.Surveys.ZTFStars(ctx, f, ts) {
		if fe.State == surveys.Done && len(fe.Series) > 0 {
			out = append(out, fe.Series)
		}
	}
	if len(out) < fadeMinStars {
		return nil
	}
	return out
}
