package vetting

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/starcat"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/ownimage"
)

func TestSpotTakesTheStarTheVetJudges(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := &starcat.MemBucket{}
	// Gaia DR3 within 10" of 28076 (Abell 85, 2026-01-17).
	if err := starcat.WriteGaia(ctx, b, "gaia", []starcat.GaiaStar{
		{SourceID: 431450733567930496, RA: 3.10334670551, Dec: 63.40110534262, G: 18.913303, PMRA: -1.463, PMDec: -0.601},
		{SourceID: 431450737862635136, RA: 3.11162632656, Dec: 63.40230768844, G: 18.882170, PMRA: -0.764, PMDec: -0.668},
		{SourceID: 431450737869829248, RA: 3.10948654780, Dec: 63.40013152508, G: 14.082265, PMRA: 0.597, PMDec: -0.149},
	}); err != nil {
		t.Fatal(err)
	}
	s := &Service{Gaia: starcat.NewGaia(b, "gaia"), Now: func() time.Time { return time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC) }}
	ha, oiii := 14.08, 14.68
	members := []app.DiscoveryCandidate{
		{Kind: app.DiscoveryKindChange, RefMag: &ha},
		{Kind: app.DiscoveryKindChange, RefMag: &oiii},
	}
	sp := s.withStar(ctx, ownimage.Spot{RA: 3.10764743113441, Dec: 63.400786563708536}, members)
	if sp.GaiaG == nil || math.Abs(*sp.GaiaG-14.08) > 0.01 {
		t.Fatalf("28076: spot star %v, want G 14.08", sp.GaiaG)
	}
	if sp := s.withStar(ctx, ownimage.Spot{RA: 3.10764743113441, Dec: 63.400786563708536}, nil); sp.GaiaG != nil {
		t.Errorf("no reference magnitude and no star within 2\": spot star %v, want none", *sp.GaiaG)
	}
	g := 15.0
	if sp := s.withStar(ctx, ownimage.Spot{RA: 3.10764743113441, Dec: 63.400786563708536, GaiaG: &g}, members); *sp.GaiaG != 15 {
		t.Errorf("a source's own star replaced: %v", *sp.GaiaG)
	}
}
