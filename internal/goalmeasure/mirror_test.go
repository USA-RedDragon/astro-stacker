package goalmeasure_test

import (
	"context"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/goalmeasure"
	"github.com/USA-RedDragon/astro-stacker/internal/goals"
	"github.com/USA-RedDragon/astro-stacker/internal/starcat"
)

func TestMirrorFetcherMatchesTheVizieRQuery(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fellBack := 0
	fallback := func(context.Context, float64, float64, float64) ([]goals.CatalogStar, error) {
		fellBack++
		return []goals.CatalogStar{{RA: 1, Dec: 2, G: 10}}, nil
	}
	b := &starcat.MemBucket{}
	fetch := goalmeasure.MirrorFetcher(starcat.NewGaia(b, "gaia"), fallback)
	if got, err := fetch(ctx, 50, 20, 1); err != nil || len(got) != 1 || fellBack != 1 {
		t.Fatalf("without a mirror: %v, %v, fell back %d times", got, err, fellBack)
	}
	if err := starcat.WriteGaia(ctx, b, "gaia", []starcat.GaiaStar{
		{SourceID: 1, RA: 50, Dec: 20, G: 13}, {SourceID: 2, RA: 50.1, Dec: 20, G: 9},
		{SourceID: 3, RA: 50.2, Dec: 20, G: 14}, {SourceID: 4, RA: 50.3, Dec: 20, G: 8.9}, {SourceID: 5, RA: 50.4, Dec: 20, G: 14.1},
	}); err != nil {
		t.Fatal(err)
	}
	fetch = goalmeasure.MirrorFetcher(starcat.NewGaia(b, "gaia"), fallback)
	got, err := fetch(ctx, 50, 20, 1)
	if err != nil || len(got) != 3 || got[0].G != 9 || got[1].G != 13 || got[2].G != 14 || fellBack != 1 {
		t.Fatalf("from the mirror: %v, %v, fell back %d times", got, err, fellBack)
	}
}
