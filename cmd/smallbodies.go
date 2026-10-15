package cmd

import (
	"context"
	"log/slog"

	"github.com/USA-RedDragon/astro-stacker/internal/config"
	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/store"
)

// smallBodies is the local known-bodies service the discoveries ask, with
// the observatory's site when its positions are to be topocentric.
func smallBodies(cfg *config.Config, appStore store.Store) discoveries.SmallBodies {
	d := cfg.Discoveries
	sb := discoveries.SmallBodies{URL: d.SmallBodiesURL, Topocentric: d.Topocentric, CrossCheck: d.CrossCheck}
	if !d.Topocentric || d.SmallBodiesURL == "" {
		return sb
	}
	site := siteFunc(cfg, appStore)
	sb.Site = func(ctx context.Context) (float64, float64, float64, bool) {
		s, err := site(ctx)
		if err != nil {
			slog.Warn("Known bodies are geocentric: the observatory site is unknown", "error", err)
			return 0, 0, 0, false
		}
		h := 0.0
		if s.Elevation != nil {
			h = *s.Elevation
		}
		return s.Latitude, s.Longitude, h, true
	}
	return sb
}
