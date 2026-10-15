package ownimage

import (
	"context"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
)

func seriesOf(_ *Curve) []lc.Series { return nil }

func measure(_ context.Context, _ Data, _ string, spots []Spot, _ Options) ([]Result, error) {
	out := make([]Result, len(spots))
	for i, s := range spots {
		out[i] = Result{SourceID: s.SourceID}
	}
	return out, nil
}
