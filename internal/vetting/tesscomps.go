package vetting

import (
	"context"
	"log/slog"
	"sort"

	"github.com/USA-RedDragon/astro-stacker/internal/starcat"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/period"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/surveys"
)

// Other stars to test the star's TESS minima against: the tessCompMax
// closest Gaia stars within tessCompArcsec that the TESS job fitted in
// the same sector. Those within tessBlendArcsec, three TESS pixels, carry
// the star's own dips.
const (
	tessCompArcsec  = 1200.0
	tessCompMax     = 20
	tessBlendArcsec = 63.0
)

// tessComparisons reads the light curves of other stars in the sectors
// TESS caught st in, from the field's TESS cache.
func (s *Service) tessComparisons(ctx context.Context, f surveys.Field, st star, sectors []int) []period.TESSComp {
	if s.Surveys == nil || s.Gaia == nil || !st.found || len(sectors) == 0 {
		return nil
	}
	t := st.target
	have := map[int][]int64{}
	ids := map[int64]bool{}
	for _, sec := range sectors {
		list, err := s.Surveys.TESSFieldStars(ctx, f, sec)
		if err != nil {
			slog.Warn("Could not list the field's TESS light curves", "sector", sec, "error", err)
			continue
		}
		for _, id := range list {
			if id != t.GaiaID {
				have[sec] = append(have[sec], id)
				ids[id] = true
			}
		}
	}
	if len(ids) == 0 {
		return nil
	}
	stars, err := s.Gaia.Cone(ctx, t.RA, t.Dec, tessCompArcsec/3600, func(g *starcat.GaiaStar) bool { return ids[g.SourceID] })
	if err != nil {
		slog.Warn("Could not read Gaia around the star for TESS comparison stars", "error", err)
		return nil
	}
	sep := map[int64]float64{}
	for _, g := range stars {
		if d := arcsec(t.RA, t.Dec, g.RA, g.Dec); d > compSelfArc {
			sep[g.SourceID] = d
		}
	}
	var out []period.TESSComp
	for _, sec := range sectors {
		var near []int64
		for _, id := range have[sec] {
			if _, ok := sep[id]; ok {
				near = append(near, id)
			}
		}
		sort.SliceStable(near, func(i, j int) bool { return sep[near[i]] < sep[near[j]] })
		for _, id := range near[:min(len(near), tessCompMax)] {
			fl, err := s.Surveys.TESSFieldFlux(ctx, f, sec, id)
			if err != nil {
				slog.Warn("Could not read a TESS comparison star", "sector", sec, "gaia", id, "error", err)
				continue
			}
			out = append(out, period.TESSComp{Sector: sec, Blended: sep[id] < tessBlendArcsec, T: fl.T, F: fl.F, E: fl.E})
		}
	}
	return out
}
