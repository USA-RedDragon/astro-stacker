package goalmeasure

import (
	"context"
	"errors"
	"log/slog"
	"sort"

	"github.com/USA-RedDragon/astro-stacker/internal/goals"
	"github.com/USA-RedDragon/astro-stacker/internal/starcat"
)

// MirrorFetcher reads stars from the Gaia DR3 mirror with VizierFetcher's
// query: G from goals.CatalogBrightG to goals.CatalogFaintG, brightest
// first, at most 20000. It asks fallback while the mirror cannot answer.
func MirrorFetcher(g *starcat.Gaia, fallback StarFetcher) StarFetcher {
	return func(ctx context.Context, ra, dec, radius float64) ([]goals.CatalogStar, error) {
		got, err := g.Cone(ctx, starcat.Round(ra, 6), starcat.Round(dec, 6), starcat.Round(radius, 4), func(s *starcat.GaiaStar) bool {
			return float64(s.G) >= goals.CatalogBrightG && float64(s.G) <= goals.CatalogFaintG
		})
		if errors.Is(err, starcat.ErrUnavailable) && fallback != nil {
			slog.Debug("Gaia mirror unavailable; asking VizieR", "error", err)
			return fallback(ctx, ra, dec, radius)
		}
		if err != nil {
			return nil, err
		}
		sort.SliceStable(got, func(i, j int) bool { return got[i].G < got[j].G })
		if len(got) > gaiaMaxRows {
			got = got[:gaiaMaxRows]
		}
		out := make([]goals.CatalogStar, len(got))
		for i, s := range got {
			out[i] = goals.CatalogStar{RA: s.RA, Dec: s.Dec, G: float64(s.G)}
		}
		return out, nil
	}
}
