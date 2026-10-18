package vetting

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"

	"github.com/USA-RedDragon/astro-stacker/internal/starcat"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/surveys"
)

// StarData is what the vet knows about a source's star without pulling
// anything new: the Gaia star at the spot with its close neighbours, and
// every survey light curve its caches hold.
type StarData struct {
	// RA and Dec are the spot's position in degrees.
	RA, Dec    float64
	Field      surveys.Field
	Gaia       *starcat.GaiaStar
	Neighbours []starcat.GaiaStar
	Series     []lc.Series
	// States says how each survey's read went: done, pending, failed or
	// uncovered.
	States map[string]surveys.State
}

// ErrNoGaia says the Gaia mirror is off.
var ErrNoGaia = errors.New("the local Gaia copy is off")

// Star reads src's star as the vet saw it. s.Surveys should be offline (see
// surveys.Offline) so a field the vet has not pulled stays unpulled.
func (s *Service) Star(ctx context.Context, src app.DiscoverySource) (StarData, error) {
	var out StarData
	ra, dec := src.RA, src.Dec
	out.RA, out.Dec = ra, dec
	g, nb, err := s.gaiaAt(ctx, ra, dec, s.refMag(ctx, src.ID))
	if err != nil {
		return out, err
	}
	out.Gaia, out.Neighbours = g, nb
	f, _, err := s.field(ctx, src.Object)
	if err != nil {
		return out, err
	}
	out.Field = f
	if s.Surveys == nil {
		return out, nil
	}
	t := surveys.Target{RA: ra, Dec: dec}
	if g != nil {
		t = surveys.Target{GaiaID: g.SourceID, RA: g.RA, Dec: g.Dec, PMRA: finite(g.PMRA), PMDec: finite(g.PMDec), G: float64(g.G)}
		for _, n := range nb {
			if math.IsNaN(float64(n.G)) || math.IsNaN(n.RA) || math.IsNaN(n.Dec) {
				continue
			}
			t.Neighbours = append(t.Neighbours, surveys.Neighbour{GaiaID: n.SourceID, RA: n.RA, Dec: n.Dec, G: float64(n.G), Sep: arcsec(g.RA, g.Dec, n.RA, n.Dec)})
		}
	}
	out.States = map[string]surveys.State{}
	for _, fe := range []surveys.Fetch{s.Surveys.ZTF(ctx, f, t), s.Surveys.ATLAS(ctx, f, t), s.Surveys.TESS(ctx, f, t), s.Surveys.NEOWISE(ctx, f, t)} {
		out.States[fe.Survey] = fe.State
		out.Series = append(out.Series, fe.Series...)
	}
	return out, nil
}

// refMag is the median reference magnitude of source id's changes, NaN
// when none has one or the database is off.
func (s *Service) refMag(ctx context.Context, id int) float64 {
	if s.App == nil || id == 0 {
		return math.NaN()
	}
	var cands []app.DiscoveryCandidate
	if err := s.App.WithContext(ctx).Select("kind", "ref_mag").Where("source_id = ?", id).Find(&cands).Error; err != nil {
		return math.NaN()
	}
	return refMagOf(cands)
}

// gaiaAt is the Gaia star the vet takes for the spot, as identify picks
// it, with the other stars within neighbourArcsec.
func (s *Service) gaiaAt(ctx context.Context, ra, dec, refMag float64) (*starcat.GaiaStar, []starcat.GaiaStar, error) {
	if s.Gaia == nil {
		return nil, nil, ErrNoGaia
	}
	stars, err := s.Gaia.Cone(ctx, ra, dec, neighbourArcsec/3600, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("read Gaia at the spot: %w", err)
	}
	sort.Slice(stars, func(i, j int) bool {
		return arcsec(ra, dec, stars[i].RA, stars[i].Dec) < arcsec(ra, dec, stars[j].RA, stars[j].Dec)
	})
	best := pickStar(stars, ra, dec, epochYear(s.now()), refMag)
	if best < 0 {
		return nil, stars, nil
	}
	g := stars[best]
	nb := make([]starcat.GaiaStar, 0, len(stars)-1)
	for i, n := range stars {
		if i != best {
			nb = append(nb, n)
		}
	}
	return &g, nb, nil
}
