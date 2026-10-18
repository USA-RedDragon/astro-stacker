package vetting

import (
	"context"
	"math"
	"strings"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/starcat"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
)

// TestMaizListMakesAStarKnown checks discovery 34867's star, Gaia DR3
// 151460926477704192, which Maiz Apellaniz 2023 flags as variable in G, BP
// and RP without a period, next to a steady star 2 arcsec away.
func TestMaizListMakesAStarKnown(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const star, steady = int64(151460926477704192), int64(151460926477704000)
	nan := math.NaN()
	rows := []starcat.VarStar{
		{ID: star, RA: 68.96098370080, Dec: 26.37141657410, Mag: 15.85615, Type: "VVV", Amp: 0.025381886, Score: 91.4, Period: nan},
		{ID: steady, RA: 68.96098370080, Dec: 26.37141657410 + 2.0/3600, Mag: 14.2, Type: "NMN", Amp: 0.0004, Score: 0.6, Period: nan},
	}
	b := &starcat.MemBucket{}
	c := starcat.MaizVariability()
	if err := starcat.WriteVariables(ctx, b, c.Prefix, c, rows); err != nil {
		t.Fatal(err)
	}
	m := Mirrors{Variables: map[string]*starcat.Variables{c.Name: starcat.NewVariables(b, c.Prefix)}}
	for _, tc := range []struct {
		name    string
		ra, dec float64
		gaiaID  int64
		known   bool
	}{
		{"by its Gaia ID", 68.96090, 26.37117, star, true},
		{"nearest without a Gaia ID", 68.96090, 26.37117, 0, true},
		{"the steady star by its Gaia ID", 68.96098370080, 26.37141657410 + 1.5/3600, steady, false},
		{"the steady star nearest", 68.96098370080, 26.37141657410 + 1.8/3600, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var k Known
			ch := surveyVarCheck(ctx, m, tc.ra, tc.dec, tc.gaiaID, &k)
			if !tc.known {
				if ch.Status != check.Pass || k.Listed != "" {
					t.Fatalf("a steady star: %+v, listed %q", ch, k.Listed)
				}
				return
			}
			want := "Maiz Apellaniz 2023 lists it as varying in Gaia's own measurements, with a spread of 0.025 mag in G"
			if ch.Status != check.Fail || k.Listed != want || !strings.HasPrefix(ch.Evidence, "Maiz Apellaniz 2023 lists it") {
				t.Fatalf("got %+v, listed %q", ch, k.Listed)
			}
			if s := sentence(k.Listed); !strings.HasSuffix(s, "in G.") {
				t.Fatalf("verdict reason %q", s)
			}
		})
	}
}
