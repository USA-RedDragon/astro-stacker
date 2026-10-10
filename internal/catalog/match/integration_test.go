package match_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/catalog"
	"github.com/USA-RedDragon/astro-stacker/internal/catalog/match"
)

func TestMatchEmbeddedCatalogue(t *testing.T) {
	t.Parallel()
	ix, err := catalog.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		c    match.Candidate
		want []string
	}{
		{match.Candidate{Name: m31Spelled}, []string{m31}},
		{match.Candidate{Name: "Andromeda Galaxy"}, []string{m31}},
		{match.Candidate{Name: sh2129Spelled}, []string{sh2129}},
		{match.Candidate{Name: ngc1313Spelled}, []string{ngc1313}},
		{match.Candidate{Name: ic4604Panel6}, []string{ic4604}},
		{match.Candidate{Name: garlicName, RA: 23.987 * 15, Dec: 62.44, HasCoords: true}, []string{garlicID}},
		{match.Candidate{Name: "Mystery", RA: 23.987 * 15, Dec: 62.44, HasCoords: true}, []string{garlicID}},
		{match.Candidate{Name: centarusA, RA: 201.365, Dec: -43.019, HasCoords: true}, []string{ngc5128}},
		{match.Candidate{Name: cygnisLoop, RA: 312.2, Dec: 31.2, HasCoords: true, Kind: match.KindProject}, []string{"G074.0-08.5", sh2103}},
	}
	for _, tc := range cases {
		t.Run(tc.c.Name, func(t *testing.T) {
			t.Parallel()
			results, err := match.Match(context.Background(), ix, tc.c)
			if err != nil {
				t.Fatal(err)
			}
			if len(results) > 0 {
				o := results[0].Object
				for _, w := range tc.want {
					if o.Designation == w || slices.Contains(o.Aliases, w) {
						return
					}
				}
			}
			t.Fatalf("%q: want one of %v first, got %v", tc.c.Name, tc.want, summary(results))
		})
	}
	_, err = match.Match(context.Background(), ix, match.Candidate{Name: comet, RA: 200, Dec: 10, HasCoords: true})
	if !errors.Is(err, match.ErrNotCatalogue) {
		t.Fatalf("comet: %v", err)
	}
}

func summary(results []match.Result) []string {
	out := make([]string, 0, len(results))
	for _, r := range results {
		out = append(out, r.Object.Designation+" "+r.Object.Name+" "+r.Method)
	}
	return out
}
