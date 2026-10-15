package discoverysource

import (
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

func TestIndexComparesOnlyNeighboringBands(t *testing.T) {
	t.Parallel()
	ix := &index{bands: map[int][]*node{}}
	for i := range 1000 {
		dec := 28 + float64(i)*bandArcsec/3600
		ix.add(&node{src: app.DiscoverySource{Kind: app.DiscoveryKindChange, Dec: dec}}, dec)
	}
	c := app.DiscoveryCandidate{Kind: app.DiscoveryKindChange, Dec: 28 + 500.5*bandArcsec/3600}
	near := ix.near(c)
	if len(near) != 3 {
		t.Fatalf("%d sources compared, want the 3 in the candidate's band and its neighbors", len(near))
	}
	for _, n := range near {
		if d := (n.src.Dec - c.Dec) * 3600; d < -2*bandArcsec || d > 2*bandArcsec {
			t.Errorf("source %.1f″ away compared", d)
		}
	}
	a, b := ix.bands[band(c.Dec)][0], ix.bands[band(c.Dec)+1][0]
	join(a, b)
	if got := len(ix.near(c)); got != 2 {
		t.Errorf("%d sources after a join, want 2", got)
	}
}
